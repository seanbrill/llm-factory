//go:build !windows

package server

import (
	"os"
	"syscall"
)

// giveToDirOwner hands a file the factory just created to the owner of its
// folder. In its container the factory runs as root, while config/ is a bind
// mount owned by whoever ran the start script; on a Linux host a root-owned
// 0600 token file would be unreadable to them and to the script that prints
// the access link. Best effort, and only root can give a file away.
func giveToDirOwner(file, dir string) {
	if os.Geteuid() != 0 {
		return
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
		_ = os.Lchown(file, int(st.Uid), int(st.Gid))
	}
}
