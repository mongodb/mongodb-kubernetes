//go:build unix

package mongodbproxy

import "syscall"

// withUmask runs f under the given umask and restores the previous one. The umask is process-wide, so this is
// meant for the start-up of the proxy, before it has children or other goroutines that create files.
func withUmask(mask int, f func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	f()
}
