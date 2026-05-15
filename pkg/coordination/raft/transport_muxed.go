package coordraft

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/raft"
	"golang.org/x/xerrors"
)

// Handshake bytes sent as the first byte of every connection accepted by a
// MuxedStreamLayer. The first byte determines which "channel" the rest of the
// conn belongs to.
const (
	HandshakeRaft byte = 'R' // raft internal traffic (replication, heartbeats, etc.)
	HandshakeApp  byte = 'A' // app-channel proposal forwarding (follower→leader)
)

// HandshakeTimeout is how long Accept waits for the 1-byte handshake before
// closing the connection.
var HandshakeTimeout = 5 * time.Second

// AppChannelHandler is invoked once per accepted app-channel connection. The
// handler is given the raw conn (already past the handshake byte) and owns
// closing it. Length-prefixed framing is the caller's responsibility — see
// the AppChannel helpers below.
type AppChannelHandler func(conn net.Conn)

// MuxedStreamLayer implements raft.StreamLayer by listening on a single TCP
// port and dispatching incoming connections by their first handshake byte:
//   - 'R' → returned from Accept (raft will use it).
//   - 'A' → handed to the configured AppChannelHandler.
//
// Dial defaults to a raft handshake; DialAppChannel writes the 'A' byte
// instead so the listener routes it to the app handler.
type MuxedStreamLayer struct {
	listener net.Listener
	advertise net.Addr

	// raftAccepted is the channel raft pulls 'R'-handshake conns from. We
	// buffer one so the accept loop doesn't block on a slow consumer.
	raftAccepted chan net.Conn
	// raftAcceptErr signals fatal listener errors to raft's Accept caller.
	raftAcceptErr chan error

	appHandler AppChannelHandler

	closeOnce sync.Once
	closed    chan struct{}
	closedFlag atomic.Bool

	// metrics — exposed for tests.
	acceptedRaft atomic.Int64
	acceptedApp  atomic.Int64
	badHandshake atomic.Int64
}

// NewMuxedStreamLayer binds to bindAddr (e.g. "127.0.0.1:0" for a random port
// in tests), spawns the accept loop, and returns a *MuxedStreamLayer.
//
// advertiseAddr is what raft will hand to peers (Dial calls). Usually the
// same as the listener's resolved address.
//
// appHandler is invoked synchronously in the accept goroutine for each
// 'A'-handshake conn; it should hand off to its own worker pool to avoid
// blocking the accept loop on slow proposals.
func NewMuxedStreamLayer(bindAddr string, advertise net.Addr, appHandler AppChannelHandler) (*MuxedStreamLayer, error) {
	l, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, xerrors.Errorf("listen %s: %w", bindAddr, err)
	}
	if advertise == nil {
		advertise = l.Addr()
	}
	sl := &MuxedStreamLayer{
		listener:      l,
		advertise:     advertise,
		raftAccepted:  make(chan net.Conn, 32),
		raftAcceptErr: make(chan error, 1),
		appHandler:    appHandler,
		closed:        make(chan struct{}),
	}
	go sl.acceptLoop()
	return sl, nil
}

// SetAppHandler swaps the app-channel handler. Used by the coordinator after
// the transport is constructed (Manager wires the handler after it sees the
// muxed layer).
func (m *MuxedStreamLayer) SetAppHandler(h AppChannelHandler) {
	m.appHandler = h
}

// Addr implements net.Listener.
func (m *MuxedStreamLayer) Addr() net.Addr {
	if m.advertise != nil {
		return m.advertise
	}
	return m.listener.Addr()
}

// Close implements net.Listener and raft.StreamLayer.
func (m *MuxedStreamLayer) Close() error {
	var err error
	m.closeOnce.Do(func() {
		m.closedFlag.Store(true)
		close(m.closed)
		err = m.listener.Close()
	})
	return err
}

// Accept implements net.Listener. Returns the next 'R'-handshake connection.
// App-channel conns are dispatched to the registered AppChannelHandler and
// never returned here.
func (m *MuxedStreamLayer) Accept() (net.Conn, error) {
	select {
	case <-m.closed:
		return nil, errClosed
	case c := <-m.raftAccepted:
		return c, nil
	case err := <-m.raftAcceptErr:
		return nil, err
	}
}

// Dial implements raft.StreamLayer. Writes the raft handshake byte then
// returns the conn.
func (m *MuxedStreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return dialWithHandshake(string(address), timeout, HandshakeRaft)
}

// DialAppChannel opens a TCP conn to addr and writes the app-channel
// handshake byte. Callers exchange length-prefixed payloads via
// WriteFramed / ReadFramed.
func (m *MuxedStreamLayer) DialAppChannel(addr string, timeout time.Duration) (net.Conn, error) {
	return dialWithHandshake(addr, timeout, HandshakeApp)
}

func dialWithHandshake(addr string, timeout time.Duration, b byte) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	if _, err := conn.Write([]byte{b}); err != nil {
		_ = conn.Close()
		return nil, xerrors.Errorf("write handshake: %w", err)
	}
	// Clear the write deadline for normal use.
	_ = conn.SetWriteDeadline(time.Time{})
	return conn, nil
}

// errClosed signals that the StreamLayer has been Closed (returned from
// Accept). We use a sentinel here so callers can distinguish closed-listener
// from transient I/O errors. The raft library tolerates io.EOF semantics.
var errClosed = errors.New("MuxedStreamLayer: closed")

// IsClosedErr returns true if err signals the StreamLayer was closed.
func IsClosedErr(err error) bool { return errors.Is(err, errClosed) }

// acceptLoop runs in a goroutine and reads incoming conns from the underlying
// listener, dispatching by handshake byte. It exits when Close() is called.
func (m *MuxedStreamLayer) acceptLoop() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			if m.closedFlag.Load() {
				return
			}
			// Listener errored before close. Surface once to Accept and bail.
			select {
			case m.raftAcceptErr <- err:
			default:
			}
			return
		}
		go m.handleNewConn(conn)
	}
}

func (m *MuxedStreamLayer) handleNewConn(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(HandshakeTimeout))
	var buf [1]byte
	n, err := io.ReadFull(conn, buf[:])
	// Clear deadline for normal use.
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || n != 1 {
		m.badHandshake.Add(1)
		_ = conn.Close()
		return
	}
	switch buf[0] {
	case HandshakeRaft:
		m.acceptedRaft.Add(1)
		select {
		case m.raftAccepted <- conn:
		case <-m.closed:
			_ = conn.Close()
		}
	case HandshakeApp:
		m.acceptedApp.Add(1)
		if m.appHandler != nil {
			// Run handler in a separate goroutine so the accept loop isn't
			// blocked by a slow proposal handler.
			go m.appHandler(conn)
		} else {
			// No handler registered → drop.
			_ = conn.Close()
		}
	default:
		m.badHandshake.Add(1)
		_ = conn.Close()
	}
}

// AcceptedRaft / AcceptedApp / BadHandshakes — atomic counters exposed for
// tests / metrics.
func (m *MuxedStreamLayer) AcceptedRaft() int64 { return m.acceptedRaft.Load() }
func (m *MuxedStreamLayer) AcceptedApp() int64  { return m.acceptedApp.Load() }
func (m *MuxedStreamLayer) BadHandshakes() int64 { return m.badHandshake.Load() }

// ============================================================================
// App-channel framing helpers. App-channel messages are length-prefixed with a
// 4-byte big-endian length followed by the raw payload. Both directions use
// this format.
// ============================================================================

// MaxAppFrameSize is the upper bound on a single app-channel frame. 16 MiB is
// far more than any reasonable proposal payload but small enough that a
// malicious / corrupt counterparty can't OOM us. Callers needing more should
// chunk their own payloads.
const MaxAppFrameSize = 16 << 20

// WriteFramed writes a length-prefixed frame to conn.
func WriteFramed(conn net.Conn, payload []byte) error {
	if len(payload) > MaxAppFrameSize {
		return xerrors.Errorf("frame too large: %d > %d", len(payload), MaxAppFrameSize)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := conn.Write(hdr[:]); err != nil {
		return xerrors.Errorf("write frame header: %w", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return xerrors.Errorf("write frame body: %w", err)
	}
	return nil
}

// ReadFramed reads a single length-prefixed frame from conn. Returns an error
// if the frame is larger than MaxAppFrameSize.
func ReadFramed(conn net.Conn) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxAppFrameSize {
		return nil, xerrors.Errorf("frame too large: %d > %d", n, MaxAppFrameSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
