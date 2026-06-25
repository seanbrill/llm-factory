package server

import (
	"path/filepath"
	"testing"
)

// Verifies build-duration stats persist and round-trip (drives the build ETA).
func TestBuildStatsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Server{statsPath: filepath.Join(dir, "build-stats.json")}
	s.loadStats()

	// Unknown compute has no recorded value and no default -> 0.
	if got := s.etaFor("zzz"); got != 0 {
		t.Fatalf("fresh stats: want 0, got %v", got)
	}

	s.recordBuild("cpu", 123.5)
	s.recordBuild("cuda", 600)

	// A new Server loading the same file should see the persisted values.
	s2 := &Server{statsPath: s.statsPath}
	s2.loadStats()
	if got := s2.etaFor("cpu"); got != 123.5 {
		t.Fatalf("cpu eta: want 123.5, got %v", got)
	}
	if got := s2.etaFor("cuda"); got != 600 {
		t.Fatalf("cuda eta: want 600, got %v", got)
	}
	if got := s2.etaFor("unknown"); got != 0 {
		t.Fatalf("unknown eta: want 0, got %v", got)
	}
}

func TestSanitizeRoute(t *testing.T) {
	cases := map[string]string{
		"ai":      "ai",
		"/ai":     "ai",
		"  /AI  ": "ai",
		"My Bot!": "mybot",
		"oni-7b":  "oni-7b",
		"a/b/c":   "abc",
		"":        "",
		"日本ai":    "ai",
	}
	for in, want := range cases {
		if got := sanitizeRoute(in); got != want {
			t.Errorf("sanitizeRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeEngine(t *testing.T) {
	cases := map[string]string{
		"":       "docker",
		"docker": "docker",
		"podman": "podman",
		"PODMAN": "docker", // unknown/cased -> docker default
		"weird":  "docker",
	}
	for in, want := range cases {
		if got := normalizeEngine(in); got != want {
			t.Errorf("normalizeEngine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeCompute(t *testing.T) {
	cases := map[string]string{
		"cpu":    "cpu",
		"cuda":   "cuda",
		"vulkan": "vulkan",
		"":       "cpu", // unknown -> safe runs-anywhere default
		"metal":  "cpu",
	}
	for in, want := range cases {
		if got := normalizeCompute(in); got != want {
			t.Errorf("normalizeCompute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProxyURLSuffix(t *testing.T) {
	if proxyURLSuffix("80") != "" || proxyURLSuffix("") != "" {
		t.Fatal("port 80 / empty should have no suffix")
	}
	if proxyURLSuffix("8088") != ":8088" {
		t.Fatalf("got %q", proxyURLSuffix("8088"))
	}
}
