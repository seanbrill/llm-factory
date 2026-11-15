package builder

import (
	"bufio"
	"context"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostMemGB is the computer's physical memory in GiB (what a spec sheet calls
// GB: a 24 GB Mac is 24), 0 when unknown. The factory usually runs in a
// container, and on macOS and Windows that container lives in Docker's VM and
// sees only the VM's memory, so the start scripts pass the host's total as
// FACTORY_HOST_MEM_BYTES. A factory running natively reads it itself.
func HostMemGB(ctx context.Context) float64 {
	if v, ok := os.LookupEnv("FACTORY_HOST_MEM_BYTES"); ok && strings.TrimSpace(v) != "" {
		return gibOf(v)
	}
	hostMemOnce.Do(func() { hostMemNative = nativeHostMemGB(ctx) })
	return hostMemNative
}

var (
	hostMemOnce   sync.Once
	hostMemNative float64
)

func nativeHostMemGB(ctx context.Context) float64 {
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		return gibOf(string(out))
	case "linux":
		// In a container this may be Docker Desktop's VM rather than the host,
		// so only a factory running directly on Linux trusts it.
		if inContainer() {
			return 0
		}
		return meminfoGiB("/proc/meminfo")
	}
	return 0
}

// gibOf turns a byte count ("25769803776") into GiB to one decimal; 0 if it isn't one.
func gibOf(s string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) {
		return 0
	}
	return math.Round(n/(1<<30)*10) / 10
}

// meminfoGiB reads MemTotal (kB) from a /proc/meminfo-format file.
func meminfoGiB(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				return 0
			}
			return gibOf(strconv.FormatFloat(kb*1024, 'f', 0, 64))
		}
	}
	return 0
}

func inContainer() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
