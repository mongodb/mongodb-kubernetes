package mongodbproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// socketDir returns a short directory: the path of a socket is limited to about 100 bytes, and t.TempDir() on
// macOS is longer than that.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenTCPLoopback(t *testing.T) {
	l, err := Listen("127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, l.Close())
}

func TestListenTCPRefusesANonLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8099", "10.0.0.1:8099", ":8099", "example.com:8099", "8099"} {
		_, err := Listen(addr)
		assert.Error(t, err, addr)
	}
}

func TestListenUnixCreatesASocketOnlyTheOwnerCanUse(t *testing.T) {
	path := filepath.Join(socketDir(t), "proxy.sock")

	l, err := Listen(UnixScheme + path)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSocket, "must be a socket")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestListenUnixServesHTTP(t *testing.T) {
	path := filepath.Join(socketDir(t), "proxy.sock")
	l, err := Listen(UnixScheme + path)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/process", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	resp, err := client.Get("http://proxy/v1/process")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
}

func TestListenUnixRemovesTheSocketFileOnClose(t *testing.T) {
	path := filepath.Join(socketDir(t), "proxy.sock")
	l, err := Listen(UnixScheme + path)
	require.NoError(t, err)
	require.NoError(t, l.Close())

	_, err = os.Lstat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestListenUnixReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "proxy.sock")

	// What a killed proxy leaves behind: a socket file nobody listens on.
	old, err := net.Listen("unix", path)
	require.NoError(t, err)
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, old.Close())
	_, err = os.Lstat(path)
	require.NoError(t, err, "the stale socket must exist for this test")

	l, err := Listen(UnixScheme + path)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
}

func TestListenUnixRefusesASocketInUse(t *testing.T) {
	path := filepath.Join(socketDir(t), "proxy.sock")
	first, err := Listen(UnixScheme + path)
	require.NoError(t, err)
	defer func() { _ = first.Close() }()

	_, err = Listen(UnixScheme + path)
	assert.ErrorContains(t, err, "in use")
}

func TestListenUnixDoesNotRemoveAFileThatIsNotASocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "proxy.sock")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))

	_, err := Listen(UnixScheme + path)
	assert.ErrorContains(t, err, "not a socket")

	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "data", string(content), "the file must be left alone")
}

func TestListenUnixDoesNotFollowASymlinkToAnotherFile(t *testing.T) {
	dir := socketDir(t)
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o600))
	path := filepath.Join(dir, "proxy.sock")
	require.NoError(t, os.Symlink(target, path))

	_, err := Listen(UnixScheme + path)
	assert.ErrorContains(t, err, "not a socket")

	content, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, "data", string(content))
}

func TestListenUnixRejectsBadPaths(t *testing.T) {
	dir := socketDir(t)
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	tests := map[string]string{
		"relative":                  "proxy.sock",
		"not clean (dot dot)":       dir + "/../" + filepath.Base(dir) + "/proxy.sock",
		"not clean (double slash)":  dir + "//proxy.sock",
		"too long":                  dir + "/" + strings.Repeat("a", 120),
		"directory does not exist":  dir + "/missing/proxy.sock",
		"parent is not a directory": file + "/proxy.sock",
	}
	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Listen(UnixScheme + path)
			assert.Error(t, err)
		})
	}
}
