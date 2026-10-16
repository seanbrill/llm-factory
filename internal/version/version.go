// Package version identifies the running factory build so two machines can
// confirm they run the same code. The start scripts stamp it at build time
// (scripts/version.sh, scripts/windows/version.ps1): Version is the VERSION
// file's major.minor plus the commit count, so every commit to main bumps it,
// and Commit is the short SHA ("-dirty" when tracked files had local edits).
package version

import "runtime/debug"

// Stamped via -ldflags "-X github.com/seanbrill/local-llm/internal/version.Version=…".
var (
	Version = "dev"
	Commit  = ""
)

// Info is the build identity reported by /api/version.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Get returns the stamped identity. An unstamped build (plain `go build` from a
// git checkout) still reports the commit Go embeds in the binary.
func Get() Info {
	info := Info{Version: Version, Commit: Commit}
	if info.Commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			info.Commit = vcsCommit(bi.Settings)
		}
	}
	return info
}

// vcsCommit formats Go's embedded VCS settings like the start scripts do.
func vcsCommit(settings []debug.BuildSetting) string {
	var rev string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev != "" && dirty {
		rev += "-dirty"
	}
	return rev
}
