package coordraft

import (
	"bytes"
	"errors"
	"fmt"
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
// port with the given app handler. Test cleanup closes the listener.
func newMuxedLayerForTest(t *testing.T, appHandler AppChannelHandler) *MuxedStreamLayer {
	t.Helper()
	sl, err := NewMuxedStreamLayer("127.0.0.1:0", nil, appHandler)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sl.Close() })
	return sl
}

// TestMuxed_DispatchesRaftAndApp verifies the most basic invariant: 'R' conns
// surface from Accept, 'A' conns are handed to the app handler. Concurrent
// dials must not interleave or starve.
func TestMuxed_DispatchesRaftAndApp(t *testing.T) {
	var appConns atomic.Int32
	var appReceived atomic.Int32
	appHandler := func(c net.Conn) {
		appConns.Add(1)
		defer c.Close()
		buf, err := ReadFramed(c)
		if err != nil {
			return
		}
		appReceived.Add(int32(len(buf)))
		_ = WriteFramed(c, []byte("ack"))
	}
	sl := newMuxedLayerForTest(t, appHandler)

	// Drain raft-accepted conns in the background.
	raftDone := make(chan struct{})
	go func() {
		defer close(raftDone)
		for {
			c, err := sl.Accept()
			if err != nil {
				return
			}
			// Echo back so the test side can sync.
			go func(c net.Conn) {
				defer c.Close()
				var buf [3]byte
				_, _ = io.ReadFull(c, buf[:])
				_, _ = c.Write([]byte("ok"))
			}(c)
		}
	}()

	// Fire 20 conns concurrently: 10 raft + 10 app.
	addr := sl.Addr().String()
	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			c, err := sl.Dial(raft.ServerAddress(addr), time.Second)
			require.NoError(t, err)
			defer c.Close()
			_, _ = c.Write([]byte("hi!"))
			var buf [2]byte
			_, _ = io.ReadFull(c, buf[:])
		}()
		go func() {
			defer wg.Done()
			c, err := sl.DialAppChannel(addr, time.Second)
			require.NoError(t, err)
			defer c.Close()
			require.NoError(t, WriteFramed(c, []byte("hello-app")))
			resp, err := ReadFramed(c)
			require.NoError(t, err)
			require.Equal(t, "ack", string(resp))
		}()
	}
	wg.Wait()

	// Allow the accept goroutine and handlers to finish.
	assert.Eventually(t, func() bool {
		return appConns.Load() == 10 && sl.AcceptedRaft() == 10 && sl.AcceptedApp() == 10
	}, 2*time.Second, 20*time.Millisecond, "counters: appConns=%d raft=%d app=%d", appConns.Load(), sl.AcceptedRaft(), sl.AcceptedApp())
	assert.Equal(t, int32(len("hello-app")*10), appReceived.Load())

	// Close stops the Accept loop.
	require.NoError(t, sl.Close())
	<-raftDone
}

// TestMuxed_GarbageHandshakeDropped verifies that an unrecognised first byte
// closes the conn and bumps the bad-handshake counter without affecting other
// traffic.
func TestMuxed_GarbageHandshakeDropped(t *testing.T) {
	sl := newMuxedLayerForTest(t, func(c net.Conn) { _ = c.Close() })

	// Background: drain raft-accepted (none expected, but keep the Accept
	// loop unblocked so close() returns cleanly).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := sl.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	c, err := net.DialTimeout("tcp", sl.Addr().String(), time.Second)
	require.NoError(t, err)
	_, _ = c.Write([]byte{'X'}) // not 'R' or 'A'
	// Server side should close. Read should return EOF / connection-reset.
	c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	_, err = c.Read(buf)
	assert.Error(t, err)
	_ = c.Close()

	require.Eventually(t, func() bool { return sl.BadHandshakes() == 1 }, time.Second, 20*time.Millisecond)
	assert.Equal(t, int64(0), sl.AcceptedRaft())
	assert.Equal(t, int64(0), sl.AcceptedApp())
	_ = sl.Close()
	<-done
}

// TestMuxed_HalfOpenCleanedUpUnderDeadline verifies that a client which
// opens a TCP conn but never writes the handshake byte gets dropped by the
// HandshakeTimeout, freeing the server-side conn.
func TestMuxed_HalfOpenCleanedUpUnderDeadline(t *testing.T) {
	// Use a short handshake timeout for the test.
	old := HandshakeTimeout
	HandshakeTimeout = 100 * time.Millisecond
	defer func() { HandshakeTimeout = old }()

	sl := newMuxedLayerForTest(t, func(c net.Conn) { _ = c.Close() })
	go func() {
		for {
			c, err := sl.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	c, err := net.DialTimeout("tcp", sl.Addr().String(), time.Second)
	require.NoError(t, err)
	defer c.Close()
	// Read end should close within ~handshake timeout.
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err = c.Read(buf)
	assert.Error(t, err, "server should close half-open conn within handshake timeout")
	require.Eventually(t, func() bool { return sl.BadHandshakes() >= 1 }, 2*time.Second, 20*time.Millisecond)
}

// TestMuxed_ConcurrentAcceptDial fires many concurrent Accept-loop consumers
// and concurrent Dialers and asserts the race detector stays quiet AND every
// conn is correctly classified.
func TestMuxed_ConcurrentAcceptDial(t *testing.T) {
	const N = 50
	var accepted atomic.Int32
	appHandler := func(c net.Conn) {
		defer c.Close()
		_, _ = ReadFramed(c)
		_ = WriteFramed(c, []byte("ok"))
	}
	sl := newMuxedLayerForTest(t, appHandler)
	go func() {
		for {
			c, err := sl.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c, err := sl.Dial(raft.ServerAddress(sl.Addr().String()), time.Second)
			if err == nil {
				_ = c.Close()
			}
		}()
		go func() {
			defer wg.Done()
			c, err := sl.DialAppChannel(sl.Addr().String(), time.Second)
			if err == nil {
				_ = WriteFramed(c, []byte("payload"))
				_, _ = ReadFramed(c)
				_ = c.Close()
			}
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		return sl.AcceptedRaft() == int64(N) && sl.AcceptedApp() == int64(N)
	}, 5*time.Second, 50*time.Millisecond)
}

// TestMuxed_FDCountStableUnderLoad fires 100 short-lived conns, then sleeps,
// and ensures the listener still accepts a fresh conn (i.e. nothing is stuck
// in CLOSE_WAIT consuming FDs on the server side from our code's POV — we
// don't read /proc/self/fd; we just check the listener is still healthy).
func TestMuxed_FDCountStableUnderLoad(t *testing.T) {
	appHandler := func(c net.Conn) {
		defer c.Close()
		_, _ = ReadFramed(c)
		_ = WriteFramed(c, []byte("ok"))
	}
	sl := newMuxedLayerForTest(t, appHandler)
	go func() {
		for {
			c, err := sl.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	for i := 0; i < 100; i++ {
		c, err := sl.DialAppChannel(sl.Addr().String(), time.Second)
		require.NoError(t, err)
		require.NoError(t, WriteFramed(c, []byte(fmt.Sprintf("payload-%d", i))))
		_, _ = ReadFramed(c)
		_ = c.Close()
	}
	// Sanity: a fresh conn still goes through.
	c, err := sl.Dial(raft.ServerAddress(sl.Addr().String()), time.Second)
	require.NoError(t, err)
	_ = c.Close()
}

// TestMuxed_LargePayloadFramedAcrossPartialReads verifies the framing helpers
// handle short reads cleanly. We send a >64KiB payload (which won't fit in a
// single TCP buffer on most systems) and assert the receiver reassembles it.
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
	go func() {
		for {
			c, err := sl.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	c, err := sl.DialAppChannel(sl.Addr().String(), time.Second)
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
