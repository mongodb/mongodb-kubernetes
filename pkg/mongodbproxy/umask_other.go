//go:build !unix

package mongodbproxy

// withUmask has no effect where there is no umask. The proxy runs in Linux containers; the mode is set with
// os.Chmod after the socket is created.
func withUmask(_ int, f func()) { f() }
