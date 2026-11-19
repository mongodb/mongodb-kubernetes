package mongodbproxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// UnixScheme prefixes the address of a Unix domain socket: unix:///var/run/mongodb-proxy/proxy.sock.
	UnixScheme = "unix://"

	// sun_path holds 108 bytes on Linux and 104 on macOS, including the terminating NUL.
	maxSocketPathLength = 103

	// socketUmask is the umask under which the socket is created, so that it never exists with wider
	// permissions, not even for a moment.
	socketUmask = 0o177
)

// Listen opens the listener for the process API.
//
// The address must start with unix:// and name an absolute path. The socket is created with mode 0600, a stale
// socket file left by an earlier run is removed, and the file is removed again when the listener is closed.
func Listen(addr string) (net.Listener, error) {
	path, ok := strings.CutPrefix(addr, UnixScheme)
	if !ok {
		return nil, fmt.Errorf("listen address %q must start with %q", addr, UnixScheme)
	}
	return listenUnix(path)
}

func listenUnix(path string) (net.Listener, error) {
	if err := validateSocketPath(path); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	// The mask is process-wide. Set it only during the startup bind.
	oldMask := syscall.Umask(socketUmask)
	l, err := net.Listen("unix", path)
	syscall.Umask(oldMask)
	return l, err
}

func validateSocketPath(path string) error {
	switch {
	case !filepath.IsAbs(path):
		return fmt.Errorf("socket path %q must be absolute", path)
	case filepath.Clean(path) != path:
		return fmt.Errorf("socket path %q must be clean (no '..', '.', double or trailing slashes)", path)
	case len(path) > maxSocketPathLength:
		return fmt.Errorf("socket path %q is longer than %d bytes", path, maxSocketPathLength)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("directory of the socket: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", filepath.Dir(path))
	}
	return nil
}

// removeStaleSocket deletes the socket file of an earlier run. An emptyDir outlives a restart of the container, so
// the file is normally still there. It refuses to delete anything that is not a socket, and a socket that still
// accepts connections, because that one belongs to a running process.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %q: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%q exists and is not a socket, not removing it", path)
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = conn.Close()
		return fmt.Errorf("%q is in use by another process", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing the stale socket %q: %w", path, err)
	}
	return nil
}
