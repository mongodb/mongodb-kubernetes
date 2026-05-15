package coordraft

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/raft"
	"golang.org/x/xerrors"
)

// AppChannelHandler is invoked once per accepted app-channel connection. The
// handler is given the raw conn and owns closing it. Length-prefixed framing
// is the caller's responsibility — see the AppChannel helpers below.
type AppChannelHandler func(conn net.Conn)

// MuxedStreamLayer implements raft.StreamLayer by listening on a dedicated
// raft TCP port and (optionally) a second TCP port for the app channel.
//
// Historically this type multiplexed both raft and app traffic onto a single
// port using a 1-byte handshake. Istio's `metadata_exchange` inbound filter
// rejects the handshake byte (it does not match the expected
// `istio-peer-exchange` framing) and resets every cross-cluster connection on
// the raw_buffer filter chain. As of G'5 iter 7 the listener is demuxed onto
// two TCP ports so each connection carries the unmodified wire protocol of
// its channel: pure hashicorp/raft msgpack on the raft port (a known-good
// shape against Istio's metadata_exchange / tcp_proxy fall-through), and
// length-prefixed plain TCP on the app port.
//
// The type retains the "MuxedStreamLayer" name to minimise call-site churn,
// but it no longer multiplexes anything in the bytes-on-the-wire sense.
type MuxedStreamLayer struct {
	listener  net.Listener
	advertise net.Addr

	// raftAccepted is the channel raft pulls accepted conns from. We buffer
	// one so the accept loop doesn't block on a slow consumer.
	raftAccepted chan net.Conn
	// raftAcceptErr signals fatal listener errors to raft's Accept caller.
	raftAcceptErr chan error

	// appListener is the second TCP listener that accepts proposal-forwarder
	// connections. Optional — nil iff no app channel is configured.
	appListener  net.Listener
	appAdvertise net.Addr
	appHandler   AppChannelHandler

	closeOnce  sync.Once
	closed     chan struct{}
	closedFlag atomic.Bool

	// metrics — exposed for tests.
	acceptedRaft atomic.Int64
	acceptedApp  atomic.Int64
}

// NewMuxedStreamLayer binds to bindAddr (e.g. "127.0.0.1:0" for a random port
// in tests), spawns the accept loop, and returns a *MuxedStreamLayer. The
// returned layer serves the raft TCP transport only.
//
// To enable the app channel, call BindAppListener after construction with a
// dedicated bind address (e.g. host:port+1) and the desired handler.
//
// advertiseAddr is what raft will hand to peers (Dial calls). Usually the
// same as the listener's resolved address; nil means use the listener's addr.
//
// appHandler is retained on the struct so callers can pre-stage a handler;
// it only takes effect once BindAppListener is also called. (The historical
// 3-arg signature is preserved for source-compat with the in-process test
// helpers — pass nil for the handler if you're not using the app channel.)
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
	go sl.raftAcceptLoop()
	return sl, nil
}

// BindAppListener opens a second TCP listener on appBindAddr and starts a
// goroutine that hands each accepted conn to handler (or the previously
// staged appHandler if handler is nil).
//
// This method MUST be called at most once per MuxedStreamLayer. Calling it
// after Close() is a no-op.
func (m *MuxedStreamLayer) BindAppListener(appBindAddr string, handler AppChannelHandler) error {
	if m.closedFlag.Load() {
		return errors.New("MuxedStreamLayer: closed")
	}
	if m.appListener != nil {
		return errors.New("MuxedStreamLayer: app listener already bound")
	}
	if handler != nil {
		m.appHandler = handler
	}
	l, err := net.Listen("tcp", appBindAddr)
	if err != nil {
		return xerrors.Errorf("listen app %s: %w", appBindAddr, err)
	}
	m.appListener = l
	m.appAdvertise = l.Addr()
	go m.appAcceptLoop()
	return nil
}

// SetAppHandler swaps the app-channel handler. Useful when the caller
// constructs the stream layer before the dependency (e.g. raft.Raft) needed
// to build the handler is available.
func (m *MuxedStreamLayer) SetAppHandler(h AppChannelHandler) {
	m.appHandler = h
}

// Addr implements net.Listener. Returns the raft listener's advertise addr.
func (m *MuxedStreamLayer) Addr() net.Addr {
	if m.advertise != nil {
		return m.advertise
	}
	return m.listener.Addr()
}

// AppAddr returns the app-channel listener address, or nil if none is bound.
func (m *MuxedStreamLayer) AppAddr() net.Addr {
	return m.appAdvertise
}

// Close implements net.Listener and raft.StreamLayer. Closes both the raft
// and (if bound) app listeners.
func (m *MuxedStreamLayer) Close() error {
	var err error
	m.closeOnce.Do(func() {
		m.closedFlag.Store(true)
		close(m.closed)
		err = m.listener.Close()
		if m.appListener != nil {
			if e := m.appListener.Close(); e != nil && err == nil {
				err = e
			}
		}
	})
	return err
}

// Accept implements net.Listener. Returns the next raft-port connection.
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

// Dial implements raft.StreamLayer. Connects to the raft port — no handshake,
// pure hashicorp/raft wire protocol from byte 0.
func (m *MuxedStreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(address), timeout)
}

// dialApp opens a plain TCP conn to the given app-channel addr. No handshake
// — both sides exchange length-prefixed frames immediately.
func dialApp(addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, timeout)
}

// errClosed signals that the StreamLayer has been Closed (returned from
// Accept). We use a sentinel here so callers can distinguish closed-listener
// from transient I/O errors. The raft library tolerates io.EOF semantics.
var errClosed = errors.New("MuxedStreamLayer: closed")

// IsClosedErr returns true if err signals the StreamLayer was closed.
func IsClosedErr(err error) bool { return errors.Is(err, errClosed) }

// raftAcceptLoop runs in a goroutine and pushes every accepted raft-port conn
// onto the raftAccepted channel for raft's Accept caller to pick up.
func (m *MuxedStreamLayer) raftAcceptLoop() {
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
		m.acceptedRaft.Add(1)
		select {
		case m.raftAccepted <- conn:
		case <-m.closed:
			_ = conn.Close()
			return
		}
	}
}

// appAcceptLoop runs in a goroutine and dispatches accepted conns on the app
// listener to m.appHandler. Each conn is handled in its own goroutine so a
// slow handler doesn't block the accept loop.
func (m *MuxedStreamLayer) appAcceptLoop() {
	for {
		conn, err := m.appListener.Accept()
		if err != nil {
			if m.closedFlag.Load() {
				return
			}
			// On any other listener error, exit the loop. Followers will
			// retry their forwards through the raft transport's app-channel
			// dial path; the listener-level error is fatal for this run.
			return
		}
		m.acceptedApp.Add(1)
		if m.appHandler != nil {
			go m.appHandler(conn)
		} else {
			_ = conn.Close()
		}
	}
}

// AcceptedRaft / AcceptedApp — atomic counters exposed for tests / metrics.
func (m *MuxedStreamLayer) AcceptedRaft() int64 { return m.acceptedRaft.Load() }
func (m *MuxedStreamLayer) AcceptedApp() int64  { return m.acceptedApp.Load() }

// stringAddr is a net.Addr whose String() returns a fixed value. It exists
// so callers can advertise a logical address (e.g. an FQDN) over raft even
// when the underlying listener is bound on a wildcard like "0.0.0.0:7000".
// The wildcard bind addr is what the OS sees; the advertise addr is what
// raft peers receive via AppendEntries and what LeaderWithID() returns to
// followers. Without an explicit advertise the wildcard leaks to followers
// and forwarder dials self-loop.
type stringAddr struct{ s string }

func (a stringAddr) Network() string { return "tcp" }
func (a stringAddr) String() string  { return a.s }

// NewStringAddr returns a net.Addr whose String() returns s verbatim. The
// Network() is hard-coded to "tcp" — the raft library doesn't inspect it.
func NewStringAddr(s string) net.Addr { return stringAddr{s: s} }

// AppPortFromRaftAddr derives the conventional app-channel address from a
// raft peer addr by incrementing the port number by 1. Used by Forwarder
// when no explicit per-peer app map has been supplied.
//
// Examples:
//
//	"10.0.0.5:7000" -> "10.0.0.5:7001"
//	"svc.cluster.local:7000" -> "svc.cluster.local:7001"
//
// Returns an error if addr is not in host:port form or the port is not a
// decimal integer in [0, 65534].
func AppPortFromRaftAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", xerrors.Errorf("split %q: %w", addr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return "", xerrors.Errorf("port %q: %w", port, err)
	}
	if p < 0 || p >= 65535 {
		return "", xerrors.Errorf("port %d out of range for +1 derivation", p)
	}
	return net.JoinHostPort(host, strconv.Itoa(p+1)), nil
}

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
