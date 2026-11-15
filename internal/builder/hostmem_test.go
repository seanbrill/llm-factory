package builder

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHostMemGB(t *testing.T) {
	t.Setenv("FACTORY_HOST_MEM_BYTES", "25769803776") // a 24 GB Mac
	if got := HostMemGB(context.Background()); got != 24 {
		t.Fatalf("HostMemGB = %v, want 24", got)
	}
	t.Setenv("FACTORY_HOST_MEM_BYTES", "not a number")
	if got := HostMemGB(context.Background()); got != 0 {
		t.Fatalf("HostMemGB(garbage) = %v, want 0", got)
	}
	for in, want := range map[string]float64{"17179869184": 16, "0": 0, "-5": 0, "": 0, " 34359738368\n": 32} {
		if got := gibOf(in); got != want {
			t.Errorf("gibOf(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMeminfoGiB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(p, []byte("MemTotal:       65756372 kB\nMemFree:  100 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := meminfoGiB(p); got != 62.7 {
		t.Fatalf("meminfoGiB = %v, want 62.7", got)
	}
	if got := meminfoGiB(filepath.Join(t.TempDir(), "missing")); got != 0 {
		t.Fatalf("missing file = %v, want 0", got)
	}
}

func TestHostOS(t *testing.T) {
	for in, want := range map[string]string{
		"darwin": "darwin", "Darwin": "darwin", " macos\n": "darwin",
		"linux": "linux", "Linux": "linux",
		"windows": "windows", "Windows_NT": "windows", "MINGW64_NT-10.0-26100": "windows", "MSYS_NT-10.0": "windows",
		"freebsd": "freebsd",
	} {
		t.Setenv("FACTORY_HOST_OS", in)
		if got := HostOS(); got != want {
			t.Errorf("FACTORY_HOST_OS=%q: HostOS = %q, want %q", in, got, want)
		}
	}
	// Unset: a native factory knows its own OS; in a container it can't tell.
	t.Setenv("FACTORY_HOST_OS", "")
	want := runtime.GOOS
	if inContainer() {
		want = ""
	}
	if got := HostOS(); got != want {
		t.Errorf("unset: HostOS = %q, want %q", got, want)
	}
}
