package mongodbproxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// UnixScheme prefixes the address of a Unix domain socket: unix:///var/run/mongodb-proxy/proxy.sock.
	UnixScheme = "unix://"

	// sun_path holds 108 bytes on Linux and 104 on macOS, including the terminating NUL.
	maxSocketPathLength = 103

	// socketMode lets only the owner of the socket connect. The kernel checks it in connect(), before any
	// request reaches the proxy.
	socketMode os.FileMode = 0o600

	// socketUmask is the umask under which the socket is created, so that it never exists with wider
	// permissions, not even for a moment.
	socketUmask = 0o177
)

// Listen opens the listener for the process API.
//
// An address that starts with unix:// is a Unix domain socket with an absolute path. The socket is created with
// mode 0600, a stale socket file left by an earlier run is removed, and the file is removed again when the
// listener is closed. Anything else must be host:port on a loopback address.
func Listen(addr string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(addr, UnixScheme); ok {
		return listenUnix(path)
	}
	if err := requireLoopback(addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", addr)
}

// requireLoopback enforces the invariant that a TCP API is reachable only from inside the pod.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q is not a loopback address", addr)
	}
	return nil
}

func listenUnix(path string) (net.Listener, error) {
	if err := validateSocketPath(path); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	var (
		l   net.Listener
		err error
	)
	withUmask(socketUmask, func() { l, err = net.Listen("unix", path) })
	if err != nil {
		return nil, err
	}
	// The umask above already did it. This makes the mode independent of it and fails loudly if it cannot be set.
	if err := os.Chmod(path, socketMode); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("setting the mode of the socket %q: %w", path, err)
	}
	return l, nil
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
