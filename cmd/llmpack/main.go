// Command llmpack is the launcher of a native package (docs/NATIVE.md): a
// model, its llama.cpp runtime and its agent harness in one folder that runs
// on the computer itself, without Docker or Podman, at full GPU speed (Metal
// on a Mac).
//
// It reads the package's manifest.json, starts the bundled llama-server on a
// free loopback port with the right flags for the platform (on Apple Silicon
// every layer on the GPU), and serves, on localhost:
//
//   - the OpenAI-compatible API (everything llama-server serves, through the
//     same gate as the model images: the baked system prompt is injected and,
//     with a harness, chat requests run the tool loop of internal/agent);
//   - a small chat page at /, so the package works with nothing else installed.
//
// The harness's file tools work in the package's workspace folder only, and
// the shell tool is always off: there is no container around the model here.
//
// It is pure Go (CGO_ENABLED=0) so the factory cross-compiles it for every
// target from its Linux container; for darwin/arm64 the Go linker signs it
// ad hoc, which Apple Silicon requires of every program.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/seanbrill/local-llm/internal/gate"
	"github.com/seanbrill/local-llm/internal/native"
	"github.com/seanbrill/local-llm/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// options are the launcher's flags.
type options struct {
	pkgDir       string
	host         string
	port         int
	portSet      bool
	noBrowser    bool
	ctxSize      int
	gpuLayers    string
	threads      int
	workspace    string
	noHarness    bool
	quiet        bool
	verify       bool
	version      bool
	allowOrigins []string
	llamaArgs    []string // after "--": passed to llama-server as they are
}

type listFlag struct{ v *[]string }

func (l listFlag) String() string { return "" }
func (l listFlag) Set(s string) error {
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l.v = append(*l.v, strings.TrimRight(p, "/"))
		}
	}
	return nil
}

const usageHead = `Usage: llmpack [options] [-- llama-server options]

Starts the model in this package and opens its chat page in your browser.
The OpenAI-compatible API is at http://127.0.0.1:<port>/v1. Stop it with
Ctrl-C, or by closing its terminal window.

Options:
`

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("llmpack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usageHead)
		fs.PrintDefaults()
		fmt.Fprint(stderr, "\nAnything after -- goes to llama-server, e.g. llmpack -- --flash-attn on\n")
	}
	fs.StringVar(&o.pkgDir, "package", "", "the package folder (default: the folder this launcher is in)")
	fs.StringVar(&o.host, "host", "127.0.0.1", "address to listen on; 0.0.0.0 shares the model and its tools with your whole network")
	fs.IntVar(&o.port, "port", 8080, "port for the chat page and the API (when it's taken, the next free one is used unless you set it)")
	fs.BoolVar(&o.noBrowser, "no-browser", false, "don't open the chat page in a browser")
	fs.IntVar(&o.ctxSize, "ctx-size", 0, "context window in tokens (default: the package's setting)")
	fs.StringVar(&o.gpuLayers, "gpu-layers", "", `model layers on the GPU: "all", "auto" (as many as fit) or a number; 0 runs on the CPU (default: the package's setting)`)
	fs.IntVar(&o.threads, "threads", 0, "CPU threads (default: llama.cpp's choice)")
	fs.StringVar(&o.workspace, "workspace", "", "folder the harness's file tools work in (default: the package's workspace folder)")
	fs.BoolVar(&o.noHarness, "no-harness", false, "serve the plain model, without the agent harness's tools")
	fs.BoolVar(&o.quiet, "quiet", false, "write llama-server's log to llama-server.log in the package folder instead of the terminal")
	fs.Var(listFlag{&o.allowOrigins}, "allow-origin", "a web page origin (e.g. https://example.com) allowed to call the API from a browser; repeat or comma-separate")
	fs.BoolVar(&o.verify, "verify", false, "check every file of the package against SHA256SUMS, then exit")
	fs.BoolVar(&o.version, "version", false, "print the launcher and llama.cpp versions, then exit")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			o.portSet = true
		}
	})
	o.llamaArgs = fs.Args()
	if o.port < 0 || o.port > 65535 {
		return o, fmt.Errorf("--port %d is out of range", o.port)
	}
	if o.ctxSize < 0 {
		return o, errors.New("--ctx-size can't be negative")
	}
	if g := strings.TrimSpace(o.gpuLayers); g != "" && g != "all" && g != "auto" {
		if n, err := strconv.Atoi(g); err != nil || n < 0 {
			return o, fmt.Errorf(`--gpu-layers must be "all", "auto" or a number, not %q`, g)
		}
	}
	return o, nil
}

func versionLine() string {
	v := version.Get()
	s := "llmpack " + v.Version
	if v.Commit != "" {
		s += " (" + v.Commit + ")"
	}
	return s + fmt.Sprintf(", %s/%s; packages run llama.cpp %s (%s)", runtime.GOOS, runtime.GOARCH, native.LlamaCppRelease, native.LlamaCppBuild)
}

// findRoot is the package folder: --package, else the launcher's folder or
// its parent (the launcher lives in <package>/bin).
func findRoot(flagDir string) (string, error) {
	if flagDir != "" {
		abs, err := filepath.Abs(flagDir)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(abs, native.ManifestName)); err != nil {
			return "", fmt.Errorf("%s has no %s: is it a native package folder?", abs, native.ManifestName)
		}
		return abs, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Dir(exe)
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if _, err := os.Stat(filepath.Join(d, native.ManifestName)); err == nil {
			return d, nil
		}
	}
	return "", fmt.Errorf("no %s next to the launcher (%s): run it from inside the package folder, or pass --package", native.ManifestName, dir)
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	logger := log.New(stderr, "[llmpack] ", 0)
	o, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		logger.Print(err)
		return 2
	}
	if o.version {
		fmt.Fprintln(stdout, versionLine())
		return 0
	}
	root, err := findRoot(o.pkgDir)
	if err != nil {
		logger.Print(err)
		return 1
	}
	m, err := native.ReadManifest(root)
	if err != nil {
		logger.Printf("can't read the package: %v", err)
		return 1
	}
	if o.verify {
		return verify(root, stdout, logger)
	}
	l, err := prepare(root, m, o, getenv, logger)
	if err != nil {
		logger.Print(err)
		return 1
	}
	return l.serve(stdout, stderr)
}

func verify(root string, stdout io.Writer, logger *log.Logger) int {
	fmt.Fprintf(stdout, "Checking the files of %s against %s…\n", root, native.ChecksumsName)
	problems, err := native.VerifyPackage(root, func(p string) { fmt.Fprintf(stdout, "  %s\n", p) })
	if err != nil {
		logger.Printf("can't check the package: %v", err)
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			logger.Print(p)
		}
		logger.Printf("%d file(s) don't match: download the package again.", len(problems))
		return 1
	}
	fmt.Fprintln(stdout, "Every file matches.")
	return 0
}

// launcher is a prepared package, ready to start.
type launcher struct {
	root     string
	m        *native.Manifest
	o        options
	target   native.Target
	engine   native.Engine
	server   string // llama-server's path
	llama    gate.Llama
	prompt   string
	harness  *harnessSetup
	logger   *log.Logger
	ready    atomic.Bool
	loadedIn atomic.Int64 // ms
}

// prepare checks the package against this computer and works out llama-server's flags.
func prepare(root string, m *native.Manifest, o options, getenv func(string) string, logger *log.Logger) (*launcher, error) {
	t, ok := native.TargetByID(m.Target)
	if !ok {
		return nil, fmt.Errorf("the package is for %q, which this launcher doesn't know", m.Target)
	}
	if t.GOOS != runtime.GOOS || t.GOARCH != runtime.GOARCH {
		return nil, fmt.Errorf("this package is for %s, but this computer is %s/%s: make a package for it in the factory's Library", t.Label, runtime.GOOS, runtime.GOARCH)
	}
	e, _ := m.Engine(native.RoleChat)
	if e.Kind != "llama.cpp" {
		return nil, fmt.Errorf("the package's %s engine is %q; this launcher runs llama.cpp", e.Role, e.Kind)
	}
	l := &launcher{root: root, m: m, o: o, target: t, engine: e, logger: logger}
	var err error
	if l.server, err = native.Resolve(root, e.Server); err != nil {
		return nil, err
	}
	if _, err := os.Stat(l.server); err != nil {
		return nil, fmt.Errorf("llama-server is missing from the package (%s): unzip the whole package again", e.Server)
	}
	model, err := native.Resolve(root, e.Model)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("the model file is missing (%s): unzip the whole package again", e.Model)
	}
	var mmproj string
	if e.MMProj != "" {
		if mmproj, err = native.Resolve(root, e.MMProj); err != nil {
			return nil, err
		}
	}
	ctx := e.CtxSize
	if o.ctxSize > 0 {
		ctx = o.ctxSize
	}
	if ctx <= 0 {
		ctx = 8192
	}
	gpu := e.GPULayers
	if o.gpuLayers != "" {
		gpu = o.gpuLayers
	}
	l.llama = gate.Llama{
		Model: model, CtxSize: ctx, Threads: o.threads, GPULayers: gpu, MMProj: mmproj,
		Modality: e.Modality, NoWebUI: true, Extra: o.llamaArgs,
	}
	if m.SystemPromptFile != "" {
		p, err := native.Resolve(root, m.SystemPromptFile)
		if err != nil {
			return nil, err
		}
		if data, err := os.ReadFile(p); err == nil {
			l.prompt = strings.TrimSpace(string(data))
		}
	}
	if !o.noHarness {
		ws := o.workspace
		if ws == "" {
			ws = filepath.Join(root, native.WorkspaceDir)
		}
		if l.harness, err = loadHarness(root, m, ws, getenv, logger.Printf); err != nil {
			return nil, fmt.Errorf("harness: %w", err)
		}
		l.harnessAgent().SetContextSize(l.llama.CtxSize) // keeps conversations inside the window
	}
	return l, nil
}

// listen opens the public port: the one asked for, or (when it wasn't set
// and 8080 is taken) the next free one.
func listen(host string, port int, fixed bool) (net.Listener, error) {
	if fixed || port == 0 {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return nil, fmt.Errorf("can't listen on %s:%d (%v): is the package already running? Pick another port with --port", host, port, err)
		}
		return ln, nil
	}
	for p := port; p < port+20 && p <= 65535; p++ {
		if ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p))); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}

// freePort is a loopback port nothing listens on, for llama-server.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (l *launcher) serve(stdout, stderr io.Writer) int {
	ln, err := listen(l.o.host, l.o.port, l.o.portSet)
	if err != nil {
		l.logger.Print(err)
		return 1
	}
	port := ln.Addr().(*net.TCPAddr).Port
	up, err := freePort()
	if err != nil {
		l.logger.Printf("no free port for llama-server: %v", err)
		return 1
	}
	l.llama.Port = up
	upstream := "http://127.0.0.1:" + strconv.Itoa(up)
	// llama-server answers only to the gate: it gets a random API key through
	// its environment (LLAMA_API_KEY: not on its command line, not in the
	// log), and the gate sends it. A web page that finds its port gets 401.
	key, err := randomKey()
	if err != nil {
		l.logger.Printf("no random key for llama-server: %v", err)
		return 1
	}

	logOut, logErr := stdout, stderr
	if l.o.quiet {
		f, err := os.Create(filepath.Join(l.root, "llama-server.log"))
		if err != nil {
			l.logger.Printf("can't write llama-server.log (%v); its log stays in the terminal", err)
		} else {
			defer f.Close()
			logOut, logErr = f, f
		}
	}

	h := l.harnessAgent()
	front, err := gate.New(gate.Options{Upstream: upstream, SystemPrompt: l.prompt, InjectMode: l.m.InjectMode, Harness: h, Host: "llmpack", UpstreamKey: key})
	if err != nil {
		l.logger.Print(err)
		return 1
	}

	l.logger.Print(versionLine())
	l.logger.Printf("%s on %s: %s, %d-token context, GPU layers %s", l.m.Name, l.target.Label, accelLabel(l.engine.Accel), l.llama.CtxSize, orDefault(l.llama.GPULayers, "llama.cpp's default"))
	sup := native.NewSupervisor()
	args := l.llama.Args()
	l.logger.Printf("starting: %s %s", filepath.Base(l.server), strings.Join(args, " "))
	proc := native.Proc{Name: "llama-server", Path: l.server, Args: args, Dir: filepath.Dir(l.server), Env: []string{"LLAMA_API_KEY=" + key}, Stdout: logOut, Stderr: logErr}
	if err := sup.Start(proc); err != nil {
		l.logger.Print(err)
		l.logger.Print(startHint(err))
		return 1
	}

	srv := &http.Server{Handler: l.handler(front, h, port), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.logger.Printf("web server: %v", err)
		}
	}()
	browse := "http://" + browseHost(l.o.host) + ":" + strconv.Itoa(port)
	l.logger.Printf("Chat page: %s   OpenAI API: %s/v1 (no key)", browse, browse)
	if !isLoopback(l.o.host) {
		l.logger.Printf("WARNING: listening on %s: anyone who can reach this computer can use the model and its tools", l.o.host)
	}
	if !l.o.noBrowser {
		if err := openBrowser(browse); err != nil {
			l.logger.Printf("open %s in your browser (%v)", browse, err)
		}
	}

	healthCtx, stopHealth := context.WithCancel(context.Background())
	defer stopHealth()
	go l.waitReady(healthCtx, upstream)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, native.StopSignals...)
	defer signal.Stop(sigs)
	code := 0
	select {
	case s := <-sigs:
		l.logger.Printf("stopping (%s)…", s)
	case e := <-sup.Exited():
		code = e.Code()
		if code == 0 {
			code = 1 // llama-server never stops on its own
		}
		l.logger.Printf("llama-server stopped: %v", errText(e.Err))
		if !l.ready.Load() {
			l.logger.Print(loadHint(l.target))
		}
	}
	stopHealth()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	sup.Stop(10 * time.Second)
	l.logger.Print("stopped.")
	return code
}

// randomKey is a fresh API key for llama-server.
func randomKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// waitReady polls llama-server until the model is loaded (/health needs no key).
func (l *launcher) waitReady(ctx context.Context, upstream string) {
	start := time.Now()
	client := &http.Client{Timeout: 3 * time.Second}
	nextNote := 15 * time.Second
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/health", nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				took := time.Since(start)
				l.loadedIn.Store(took.Milliseconds())
				l.ready.Store(true)
				l.logger.Printf("model loaded in %s: ready.", took.Round(100*time.Millisecond))
				return
			}
		}
		if waited := time.Since(start); waited > nextNote {
			l.logger.Printf("still loading the model (%s)…", waited.Round(time.Second))
			nextNote += 15 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func errText(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func accelLabel(accel string) string {
	switch accel {
	case native.AccelMetal:
		return "Metal GPU"
	case native.AccelVulkan:
		return "Vulkan GPU (or the CPU without one)"
	default:
		return "CPU"
	}
}

// startHint explains a llama-server that couldn't be started at all.
func startHint(err error) string {
	if runtime.GOOS == "darwin" {
		return "macOS may have blocked it: open the package with Start.command (it clears the download quarantine once you approve it), or run: xattr -dr com.apple.quarantine <package folder>"
	}
	if errors.Is(err, os.ErrPermission) {
		return "llama-server isn't executable: unzip the package again with a tool that keeps file permissions (unzip, or your file manager)"
	}
	return "unzip the whole package again, then start it once more"
}

// loadHint explains a llama-server that stopped before the model was loaded.
func loadHint(t native.Target) string {
	h := "llama-server stopped before the model was ready; its log above says why. Most often there isn't enough memory for the model and its context: try --ctx-size 4096"
	if t.Accel == native.AccelMetal {
		h += ", or --gpu-layers auto. If macOS refused to run it (\"killed\"), start the package with Start.command"
	} else {
		h += ", or --gpu-layers 0 to run on the CPU only"
	}
	return h + "."
}
