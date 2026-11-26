package mongodbproxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
// The address must start with unix:// and name an absolute path reserved for one proxy instance.
// Listen removes any socket left at that path and binds with mode 0600. Close removes the socket.
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
	if err := removeSocket(path); err != nil {
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

// An emptyDir keeps the socket file when a container exits without closing it.
func removeSocket(path string) error {
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
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing socket %q: %w", path, err)
	}
	return nil
}
