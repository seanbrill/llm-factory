//go:build windows

package builder

import "errors"

// DiskSpace isn't implemented on Windows hosts; the factory normally runs in a
// Linux container there. Callers treat the error as "unknown".
func DiskSpace(path string) (total, avail uint64, err error) {
	return 0, 0, errors.New("disk space: not supported on windows")
}
