package server

import (
	"os"
	"path/filepath"
	"testing"
)

// Verifies build-duration stats persist and round-trip (drives the build ETA),
// scale with the model's size, and read the older one-number file.
func TestBuildStatsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Server{statsPath: filepath.Join(dir, "build-stats.json")}
	s.loadStats()

	// Unknown compute has no recorded value and no default -> 0.
	if got := s.etaFor("zzz", 5); got != 0 {
		t.Fatalf("fresh stats: want 0, got %v", got)
	}

	s.recordBuild("cpu", 123.5, 0)
	s.recordBuild("cuda", 600, 5)

	// A new Server loading the same file should see the persisted values.
	s2 := &Server{statsPath: s.statsPath}
	s2.loadStats()
	if got := s2.etaFor("cpu", 9); got != 123.5 {
		t.Fatalf("cpu eta (no size recorded): want 123.5, got %v", got)
	}
	if got := s2.etaFor("cuda", 10); got != 1200 {
		t.Fatalf("cuda eta for twice the size: want 1200, got %v", got)
	}
	if got := s2.etaFor("unknown", 1); got != 0 {
		t.Fatalf("unknown eta: want 0, got %v", got)
	}

	// The median of recent builds, each scaled (at most 3x) to the model.
	for _, b := range [][2]float64{{240, 6}, {960, 8}, {300, 6}} {
		s2.recordBuild("vulkan", b[0], b[1])
	}
	if got := s2.etaFor("vulkan", 6); got != 300 {
		t.Fatalf("vulkan median: want 300, got %v", got)
	}
	for i := 0; i < buildSamples+3; i++ {
		s2.recordBuild("cpu", 60, 1)
	}
	if n := len(s2.buildSecs["cpu"]); n != buildSamples {
		t.Fatalf("kept %d cpu builds, want %d", n, buildSamples)
	}

	// The older file: {"vulkan": 240.4}.
	if err := os.WriteFile(s.statsPath, []byte(`{"cpu":87.6,"vulkan":240.4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s3 := &Server{statsPath: s.statsPath}
	s3.loadStats()
	if got := s3.etaFor("vulkan", 8); got != 240.4 {
		t.Fatalf("old file: want 240.4, got %v", got)
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
