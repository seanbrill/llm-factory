package builder

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// compilingDockerfiles returns every Dockerfile under docker/ that compiles C/C++
// (has a `cmake --build`), keyed by file name.
func compilingDockerfiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", filepath.FromSlash(RuntimesDir), "Dockerfile*"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no Dockerfiles found: %v", err)
	}
	out := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if compilesFromSource(data) {
			out[filepath.Base(p)] = string(data)
		}
	}
	if len(out) < 8 {
		t.Fatalf("found only %d compiling Dockerfiles", len(out))
	}
	return out
}

// logical joins backslash-continued lines and drops comment lines, the way the
// Dockerfile parser sees the instructions.
func logical(df string) string {
	var b strings.Builder
	for _, line := range strings.Split(df, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			b.WriteString(strings.TrimSuffix(line, "\\"))
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

var nprocJobs = regexp.MustCompile(`-j\s*"?\$\(nproc\)`)

func TestDockerfilesSizeCompileJobs(t *testing.T) {
	for name, df := range compilingDockerfiles(t) {
		code := logical(df)
		if !strings.Contains(code, "ARG BUILD_JOBS=0\n") {
			t.Errorf("%s: no ARG BUILD_JOBS=0", name)
		}
		if nprocJobs.MatchString(code) {
			t.Errorf("%s: still compiles with -j $(nproc)", name)
		}
		for _, line := range strings.Split(code, "\n") {
			if strings.Contains(line, "cmake --build") && !strings.Contains(line, `cmake --build build --config Release -j "$JOBS"`) {
				t.Errorf("%s: a cmake --build doesn't use -j \"$JOBS\": %s", name, strings.TrimSpace(line))
			}
		}
		// The ARG has to come before the RUN that reads it.
		if a, r := strings.Index(code, "ARG BUILD_JOBS=0"), strings.Index(code, `MEM_KB="$(sed`); a < 0 || r < 0 || a > r {
			t.Errorf("%s: ARG BUILD_JOBS must precede the jobs snippet", name)
		}
		// CPU builds budget 1.5 GiB per job; Vulkan/CUDA builds 2 GiB.
		budget := "/ 2097152 ))"
		if strings.Contains(name, "cpu") {
			budget = "/ 1572864 ))"
		}
		if !strings.Contains(jobsSnippet(t, name, df), budget) {
			t.Errorf("%s: per-job budget isn't %s", name, budget)
		}
	}
}

// jobsSnippet extracts the jobs calculation (MEM_KB=... through the JOBS=1 floor).
func jobsSnippet(t *testing.T, name, df string) string {
	t.Helper()
	code := logical(df)
	start := strings.Index(code, `MEM_KB="$(sed`)
	if start < 0 {
		t.Fatalf("%s: no jobs snippet", name)
	}
	end := strings.Index(code[start:], "JOBS=1;")
	if end < 0 {
		t.Fatalf("%s: jobs snippet has no JOBS=1 floor", name)
	}
	return code[start : start+end+len("JOBS=1;")]
}

// TestDockerfileJobsMath runs each Dockerfile's own snippet under /bin/sh with
// a fake /proc/meminfo and nproc.
func TestDockerfileJobsMath(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	meminfo := func(availKB int) string {
		p := filepath.Join(dir, "meminfo-"+strconv.Itoa(availKB))
		body := "MemTotal:        2006432 kB\nMemFree:          112408 kB\n"
		if availKB >= 0 {
			body += "MemAvailable:    " + strconv.Itoa(availKB) + " kB\n"
		}
		body += "Buffers:            2048 kB\nCached:          1550000 kB\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const unset = "<unset>"
	type tc struct {
		label            string
		availKB, cpus    int
		buildJobs        string
		wantCPU, wantGPU int
	}
	cases := []tc{
		// The OOM: a 2 GiB, 7-CPU libkrun Podman machine used to run 7 compilers.
		{"2 GiB podman machine", 1654320, 7, unset, 1, 1},
		// Docker Desktop with 18 GiB / 14 CPUs: memory, not CPUs, is the limit.
		{"18 GiB docker desktop", 17301504, 14, unset, 11, 8},
		{"memory-rich, CPU-poor", 17301504, 4, "0", 4, 4},
		{"BUILD_JOBS forces", 1654320, 7, "3", 3, 3},
		{"non-numeric BUILD_JOBS is auto", 17301504, 14, "abc", 11, 8},
		{"empty BUILD_JOBS is auto", 17301504, 14, "", 11, 8},
		{"no MemAvailable (old kernel)", -1, 8, unset, 1, 1},
	}
	for name, df := range compilingDockerfiles(t) {
		snippet := jobsSnippet(t, name, df)
		for _, c := range cases {
			script := "set -eu; nproc() { echo " + strconv.Itoa(c.cpus) + "; }; " +
				strings.ReplaceAll(snippet, "/proc/meminfo", meminfo(c.availKB)) + ` echo "$JOBS"`
			cmd := exec.Command(sh, "-c", script)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
			if c.buildJobs != unset {
				cmd.Env = append(cmd.Env, "BUILD_JOBS="+c.buildJobs)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("%s / %s: %v\n%s", name, c.label, err, out)
				continue
			}
			want := c.wantGPU
			if strings.Contains(name, "cpu") {
				want = c.wantCPU
			}
			if got := strings.TrimSpace(string(out)); got != strconv.Itoa(want) {
				t.Errorf("%s / %s: %s jobs, want %d", name, c.label, got, want)
			}
		}
	}
}
