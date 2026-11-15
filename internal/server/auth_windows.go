//go:build windows

package server

// giveToDirOwner is a no-op on Windows, where the file takes its folder's ACL.
func giveToDirOwner(file, dir string) {}
