package builder

// The launcher (cmd/llmpack) for a native package's target. The factory
// image cross-compiles it for every target when it is built (Dockerfile.factory
// puts them in native/launchers/), so the factory needs no Go toolchain at run
// time. A factory run from source (start-dev, start-native) has Go, and
// cross-compiles the launcher from its own source instead.
//
// Either way it is a pure-Go build (CGO_ENABLED=0), linked by Go's internal
// linker. For darwin/arm64 that linker signs the program ad hoc, whatever
// system it runs on (cmd/link: NeedCodeSign is darwin && arm64), which Apple
// Silicon requires: checkBinary refuses a launcher without the signature.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/seanbrill/local-llm/internal/native"
	"github.com/seanbrill/local-llm/internal/version"
)

// LaunchersDir is where the factory image keeps its prebuilt launchers.
func (b *Builder) LaunchersDir() string { return filepath.Join(b.BaseDir, "native", "launchers") }

// LauncherFileName is a prebuilt launcher's name: llmpack-<goos>-<goarch>[.exe].
func LauncherFileName(t native.Target) string {
	name := "llmpack-" + t.GOOS + "-" + t.GOARCH
	if t.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// nativeLauncher returns the launcher for t: the prebuilt one, or one
// cross-compiled into work.
func (b *Builder) nativeLauncher(ctx context.Context, t native.Target, work string, log LogFunc) (string, error) {
	pre := filepath.Join(b.LaunchersDir(), LauncherFileName(t))
	var p string
	if fi, err := os.Stat(pre); err == nil && fi.Mode().IsRegular() {
		p = pre
		log(fmt.Sprintf("Launcher: llmpack for %s, built with this factory.", t.Label))
	} else {
		var err error
		if p, err = b.compileLauncher(ctx, t, work, log); err != nil {
			return "", err
		}
	}
	if err := checkBinary(p, t); err != nil {
		return "", fmt.Errorf("the launcher for %s: %w", t.Label, err)
	}
	if t.GOOS == "darwin" && t.GOARCH == "arm64" {
		log("The launcher is ad-hoc signed by the Go linker, as Apple Silicon requires.")
	}
	return p, nil
}

// compileLauncher cross-compiles cmd/llmpack for t with the Go toolchain on
// PATH, stamped with the factory's version.
func (b *Builder) compileLauncher(ctx context.Context, t native.Target, work string, log LogFunc) (string, error) {
	src := filepath.Join(b.BaseDir, "cmd", "llmpack")
	goBin, err := exec.LookPath("go")
	if _, serr := os.Stat(src); err != nil || serr != nil {
		return "", fmt.Errorf("there's no launcher for %s: the factory image builds them (rebuild it with the start script), or run the factory from source with Go installed", t.Label)
	}
	out := filepath.Join(work, LauncherFileName(t))
	v := version.Get()
	pkg := "github.com/seanbrill/local-llm/internal/version"
	ldflags := fmt.Sprintf("-s -w -X %s.Version=%s -X %s.Commit=%s", pkg, v.Version, pkg, v.Commit)
	log(fmt.Sprintf("Cross-compiling the launcher (llmpack) for %s ...", t.Label))
	cmd := exec.CommandContext(ctx, goBin, "build", "-trimpath", "-ldflags", ldflags, "-o", out, "./cmd/llmpack")
	cmd.Dir = b.BaseDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+t.GOOS, "GOARCH="+t.GOARCH, "GOTOOLCHAIN=local", "GOWORK=off")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build of the launcher for %s failed: %v\n%s", t.Label, err, strings.TrimSpace(buf.String()))
	}
	return out, nil
}
