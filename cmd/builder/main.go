// Command builder starts the local web GUI for building, running, and testing
// model-baked llama.cpp images.
//
//	go run ./cmd/builder            # serves http://127.0.0.1:8799 and opens it
//	go run ./cmd/builder -dir .     # project dir holding models/ and images/
//
// The API needs the access token (config/factory-token, or FACTORY_TOKEN;
// FACTORY_AUTH=off turns it off). The log shows an "Open:" link carrying the
// token, which signs a browser in; -open opens that link.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/server"
	"github.com/seanbrill/local-llm/internal/version"
)

func main() {
	// Force BuildKit for `docker build`. The daemon's integrated BuildKit works
	// without the buildx CLI plugin (the dev factory image ships only the docker
	// binary, so it would otherwise fall back to the legacy builder) AND shares the
	// layer cache — without this, a model image recompiles stable-diffusion.cpp
	// with CUDA from scratch (~15 min, all cores pegged) instead of reusing the
	// cached layer. Inherited by every `docker` exec the builder runs.
	if os.Getenv("DOCKER_BUILDKIT") == "" {
		_ = os.Setenv("DOCKER_BUILDKIT", "1")
	}

	dir := flag.String("dir", ".", "project directory holding config/, models/, images/")
	addr := flag.String("addr", "127.0.0.1:8799", "address to listen on")
	open := flag.Bool("open", true, "open the UI in a browser on startup")
	host := flag.String("host", envOr("MODEL_HOST", "127.0.0.1"),
		"host where launched model containers are reachable (set host.docker.internal when the factory runs in a container)")
	web := flag.String("web", envOr("WEB_DIR", ""),
		"serve the UI from this directory instead of the embedded copy (dev hot-reload); empty = embedded")
	flag.Parse()

	cat, err := catalog.Load(*dir)
	if err != nil {
		log.Fatalf("load catalog: %v", err)
	}
	b, err := builder.New(*dir)
	if err != nil {
		log.Fatalf("init builder: %v", err)
	}
	srv, err := server.New(b, cat, *host, *web)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The access link: the UI's address with the token, which signs a browser in.
	auth := srv.Auth()
	base := accessBase(*addr, os.Getenv("FACTORY_PORT"))
	link := base + "/"
	if auth.Required() {
		link += "?token=" + url.QueryEscape(auth.Token())
	}
	v := version.Get()
	log.Printf("local-llm factory v%s (%s)", v.Version, v.Commit)
	log.Printf("local-llm builder UI:  %s", base)
	log.Printf("  models:  %s", b.ModelsDir)
	log.Printf("  images:  %s", b.ImagesDir)
	log.Printf("  catalog: %s  (%d models)", cat.Path, len(cat.Models))
	log.Printf("  model host: %s", *host)
	if *web != "" {
		log.Printf("  UI (dev): serving from disk %s", *web)
	}
	if auth.Required() {
		log.Printf("Open: %s", link)
		log.Printf("  access token: %s (open the link to sign a browser in, or paste the token into the page)", auth.Source())
	} else {
		log.Print("WARNING: FACTORY_AUTH=off: the factory API has NO access token.")
		log.Printf("WARNING: anything that can reach %s can build, run and delete containers,", base)
		log.Print("WARNING: including commands a model runs in the harness's workspace terminal.")
		log.Print("WARNING: unset FACTORY_AUTH to turn the access token back on.")
	}

	if *open {
		go func() {
			time.Sleep(500 * time.Millisecond)
			openBrowser(link)
		}()
	}

	// After a host/VM reboot, bring autostart models back and rebuild the proxy
	// routes (Podman's restart policy doesn't reliably fire after a machine stop).
	// Runs in the background so it never blocks serving the UI.
	go b.RecoverOnStartup(context.Background(), func(s string) { log.Printf("startup recovery: %s", s) })

	log.Fatal(httpSrv.ListenAndServe())
}

// accessBase is the factory's address as a browser on this computer opens it.
// A wildcard bind (":8799", "0.0.0.0:8799", "[::]:8799") becomes localhost,
// with publishedPort (FACTORY_PORT, which the start scripts pass to the
// factory's container) in place of the port the factory listens on inside it.
func accessBase(addr, publishedPort string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
		if p, err := strconv.Atoi(strings.TrimSpace(publishedPort)); err == nil && p > 0 && p < 65536 {
			port = strconv.Itoa(p)
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open browser automatically: %v", err)
	}
}
