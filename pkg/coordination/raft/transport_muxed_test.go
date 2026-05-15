package coordraft

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMuxedLayerForTest builds a MuxedStreamLayer bound to a random localhost
// port. If appHandler is non-nil it also binds a second app listener on
// 127.0.0.1:0 with that handler. Test cleanup closes the listener(s).
func newMuxedLayerForTest(t *testing.T, appHandler AppChannelHandler) *MuxedStreamLayer {
	t.Helper()
	sl, err := NewMuxedStreamLayer("127.0.0.1:0", nil, appHandler)
	require.NoError(t, err)
	if appHandler != nil {
		require.NoError(t, sl.BindAppListener("127.0.0.1:0", appHandler))
	}
	t.Cleanup(func() { _ = sl.Close() })
	return sl
}

// TestMuxed_RaftPortPureTCP verifies the most basic invariant after G'5 iter
// 7's demux: raft connections are pure TCP — no handshake byte, the very
// first bytes a peer sends are unmodified application data (in production
// these are msgpack-encoded raft RPCs). The test sends a 3-byte payload and
// asserts the server-side accept hands back exactly those bytes.
func TestMuxed_RaftPortPureTCP(t *testing.T) {
	sl := newMuxedLayerForTest(t, nil)

	received := make(chan []byte, 1)
	go func() {
		c, err := sl.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var buf [3]byte
		_, _ = io.ReadFull(c, buf[:])
		received <- buf[:]
	}()

	c, err := sl.Dial(raft.ServerAddress(sl.Addr().String()), time.Second)
	require.NoError(t, err)
	_, _ = c.Write([]byte("hi!"))

	select {
	case got := <-received:
		assert.Equal(t, "hi!", string(got))
	case <-time.After(2 * time.Second):
		t.Fatal("server did not see the bytes")
	}
	_ = c.Close()

	require.Eventually(t, func() bool { return sl.AcceptedRaft() == 1 }, time.Second, 20*time.Millisecond)
	assert.Equal(t, int64(0), sl.AcceptedApp(), "no app conns")
}

// TestMuxed_AppListenerSeparatePort verifies that BindAppListener opens a
// second TCP listener on a distinct port and routes incoming framed payloads
// to the registered handler. The raft listener is untouched.
func TestMuxed_AppListenerSeparatePort(t *testing.T) {
	var appConns atomic.Int32
	appHandler := func(c net.Conn) {
		appConns.Add(1)
		defer c.Close()
		buf, err := ReadFramed(c)
		if err != nil {
			return
		}
		_ = WriteFramed(c, append([]byte("echo:"), buf...))
	}
	sl := newMuxedLayerForTest(t, appHandler)

	// App addr is separate from raft addr.
	require.NotNil(t, sl.AppAddr())
	require.NotEqual(t, sl.Addr().String(), sl.AppAddr().String())

	// Fire 10 app conns concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := dialApp(sl.AppAddr().String(), time.Second)
			require.NoError(t, err)
			defer c.Close()
			require.NoError(t, WriteFramed(c, []byte("hi")))
			resp, err := ReadFramed(c)
			require.NoError(t, err)
			require.Equal(t, "echo:hi", string(resp))
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		return appConns.Load() == 10 && sl.AcceptedApp() == 10
	}, 2*time.Second, 20*time.Millisecond)
	assert.Equal(t, int64(0), sl.AcceptedRaft(), "no raft conns")
}

// TestMuxed_ConcurrentRaftAndApp fires concurrent raft + app dials and asserts
// both flows succeed independently and the per-port counters track them.
func TestMuxed_ConcurrentRaftAndApp(t *testing.T) {
	appHandler := func(c net.Conn) {
		defer c.Close()
		_, _ = ReadFramed(c)
		_ = WriteFramed(c, []byte("ok"))
	}
	sl := newMuxedLayerForTest(t, appHandler)

	// Drain raft-accepted conns.
	go func() {
		for {
			c, err := sl.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var buf [3]byte
				_, _ = io.ReadFull(c, buf[:])
				_, _ = c.Write([]byte("ok"))
			}(c)
		}
	}()

	const N = 25
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c, err := sl.Dial(raft.ServerAddress(sl.Addr().String()), time.Second)
			require.NoError(t, err)
			defer c.Close()
			_, _ = c.Write([]byte("hi!"))
			var buf [2]byte
			_, _ = io.ReadFull(c, buf[:])
		}()
		go func() {
			defer wg.Done()
			c, err := dialApp(sl.AppAddr().String(), time.Second)
			require.NoError(t, err)
			defer c.Close()
			require.NoError(t, WriteFramed(c, []byte("payload")))
			_, _ = ReadFramed(c)
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		return sl.AcceptedRaft() == int64(N) && sl.AcceptedApp() == int64(N)
	}, 5*time.Second, 50*time.Millisecond)
}

// TestMuxed_LargePayloadFramedAcrossPartialReads verifies the framing helpers
// handle short reads cleanly when the app channel is in use. We send a >256
// KiB payload (which won't fit in a single TCP buffer on most systems) and
// assert the receiver reassembles it.
func TestMuxed_LargePayloadFramedAcrossPartialReads(t *testing.T) {
	const size = 256 * 1024 // 256 KiB
	want := bytes.Repeat([]byte{0xAB}, size)

	var gotPayload []byte
	gotCh := make(chan struct{})
	appHandler := func(c net.Conn) {
		defer c.Close()
		p, err := ReadFramed(c)
		require.NoError(t, err)
		gotPayload = p
		_ = WriteFramed(c, []byte("ok"))
		close(gotCh)
	}
	sl := newMuxedLayerForTest(t, appHandler)

	c, err := dialApp(sl.AppAddr().String(), time.Second)
	require.NoError(t, err)
	require.NoError(t, WriteFramed(c, want))
	resp, err := ReadFramed(c)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(resp))
	_ = c.Close()

	select {
	case <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("app handler did not see payload")
	}
	assert.Equal(t, size, len(gotPayload))
	assert.True(t, bytes.Equal(want, gotPayload))
}

// TestMuxed_ReadFramedRejectsOversized verifies that a corrupt/oversized
// length header is rejected instead of allocating gigabytes.
func TestMuxed_ReadFramedRejectsOversized(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	go func() {
		// Write a header that claims 1 GiB.
		var hdr [4]byte
		hdr[0] = 0x40 // 1 GiB
		_, _ = cli.Write(hdr[:])
	}()
	_, err := ReadFramed(srv)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "too large"), "got: %v", err)
}

// TestMuxed_CloseUnblocksAccept verifies Close() causes a blocked Accept() to
// return an error rather than hang.
func TestMuxed_CloseUnblocksAccept(t *testing.T) {
	sl, err := NewMuxedStreamLayer("127.0.0.1:0", nil, nil)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := sl.Accept()
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // let Accept block
	_ = sl.Close()
	select {
	case err := <-done:
		require.Error(t, err)
		// Either our errClosed or a listener-error — both acceptable.
		_ = errors.Is(err, errClosed) // just make sure no panic
	case <-time.After(time.Second):
		t.Fatal("Accept did not unblock after Close")
	}
}

// TestMuxed_AppPortFromRaftAddr exercises the conventional port+1 derivation.
func TestMuxed_AppPortFromRaftAddr(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"10.0.0.5:7000", "10.0.0.5:7001", false},
		{"127.0.0.1:0", "127.0.0.1:1", false},
		{"svc.cluster.local:7000", "svc.cluster.local:7001", false},
		{"[::1]:7000", "[::1]:7001", false},
		{"no-port", "", true},
		{"host:not-a-port", "", true},
		{"host:65535", "", true}, // out of range
	}
	for _, c := range cases {
		got, err := AppPortFromRaftAddr(c.in)
		if c.wantErr {
			assert.Error(t, err, "input %q", c.in)
			continue
		}
		assert.NoError(t, err, "input %q", c.in)
		assert.Equal(t, c.want, got, "input %q", c.in)
	}
}

// TestMuxed_AdvertiseAddrOverridesListenerAddr verifies that when a non-nil
// advertise is passed to NewMuxedStreamLayer, Addr() returns the advertise
// addr (not the listener's resolved bind addr). This is what production code
// uses to advertise an FQDN to raft peers while still binding 0.0.0.0:7000.
func TestMuxed_AdvertiseAddrOverridesListenerAddr(t *testing.T) {
	advertise := NewStringAddr("operator.cluster-1.svc.cluster.local:7000")
	sl, err := NewMuxedStreamLayer("127.0.0.1:0", advertise, nil)
	require.NoError(t, err)
	defer sl.Close()

	assert.Equal(t, "operator.cluster-1.svc.cluster.local:7000", sl.Addr().String(),
		"Addr() must return the advertise string verbatim when supplied")
	// Sanity: the underlying listener still has its kernel-picked loopback addr.
	host, _, err := net.SplitHostPort(sl.listener.Addr().String())
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host, "listener still bound on loopback")
}

// TestMuxed_AdvertiseAddrNilFallsThroughToListenerAddr is the existing
// (pre-iter-12) behaviour: when advertise is nil, Addr() returns the
// listener's resolved bind addr. Local-mode tests rely on this.
func TestMuxed_AdvertiseAddrNilFallsThroughToListenerAddr(t *testing.T) {
	sl, err := NewMuxedStreamLayer("127.0.0.1:0", nil, nil)
	require.NoError(t, err)
	defer sl.Close()

	listenerAddr := sl.listener.Addr().String()
	assert.Equal(t, listenerAddr, sl.Addr().String())
}

// TestMuxed_BindAppListenerTwiceRejected verifies the listener can only be
// bound once. The second call should error.
func TestMuxed_BindAppListenerTwiceRejected(t *testing.T) {
	sl, err := NewMuxedStreamLayer("127.0.0.1:0", nil, nil)
	require.NoError(t, err)
	defer sl.Close()

	require.NoError(t, sl.BindAppListener("127.0.0.1:0", func(c net.Conn) { _ = c.Close() }))
	err = sl.BindAppListener("127.0.0.1:0", func(c net.Conn) { _ = c.Close() })
	require.Error(t, err)
	require.Contains(t, err.Error(), "already bound")
}
