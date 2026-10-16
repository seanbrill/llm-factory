package version

import (
	"runtime/debug"
	"testing"
)

func TestVcsCommit(t *testing.T) {
	cases := []struct {
		settings []debug.BuildSetting
		want     string
	}{
		{nil, ""},
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "8a59bff0123456789"}}, "8a59bff"},
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "8a59bff0123456789"}, {Key: "vcs.modified", Value: "true"}}, "8a59bff-dirty"},
		{[]debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, ""},
	}
	for _, c := range cases {
		if got := vcsCommit(c.settings); got != c.want {
			t.Errorf("vcsCommit(%v) = %q, want %q", c.settings, got, c.want)
		}
	}
}

func TestGetPrefersStampedCommit(t *testing.T) {
	Version, Commit = "1.0.42", "abc1234"
	t.Cleanup(func() { Version, Commit = "dev", "" })
	if got := Get(); got != (Info{Version: "1.0.42", Commit: "abc1234"}) {
		t.Errorf("Get() = %+v", got)
	}
}
