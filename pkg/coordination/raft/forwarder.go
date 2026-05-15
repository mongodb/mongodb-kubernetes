package coordraft

import (
	"errors"
	"net"
	"time"

	"github.com/hashicorp/raft"
	"golang.org/x/xerrors"
)

// AppChannelProtocol — over the wire format:
//
//	client → server (the leader):
//	  WriteFramed(serializedProposal bytes)
//	server → client:
//	  status byte (0 = ok, 1 = err) + optional error string framed.
//
// Specifically, the server reads ONE proposal framed payload, calls
// raft.Apply locally, then writes:
//   - status byte (0 = applied; 1 = error)
//   - WriteFramed(errorMessage)  // empty bytes if status=0
//
// The conn is then closed by the server.

// AppRespStatusOK / AppRespStatusErr are the 1-byte response status codes.
const (
	AppRespStatusOK  byte = 0
	AppRespStatusErr byte = 1
)

// AppDialTimeout is the default dial+handshake timeout for follower→leader
// proposal-forwarding connections.
var AppDialTimeout = 3 * time.Second

// AppApplyTimeout is the default raft.Apply timeout the leader enforces
// when applying a forwarded proposal.
var AppApplyTimeout = 5 * time.Second

// makeAppChannelHandler returns an AppChannelHandler bound to the given
// raft.Raft. The handler:
//  1. Reads one framed proposal payload from the conn.
//  2. Calls r.Apply(payload, AppApplyTimeout).
//  3. Writes a 1-byte status + framed error string back.
//  4. Closes the conn.
//
// The handler runs in its own goroutine (spawned by MuxedStreamLayer); it
// must therefore be safe to call concurrently from many goroutines. raft.Apply
// is itself thread-safe.
func makeAppChannelHandler(r *raft.Raft) AppChannelHandler {
	return func(conn net.Conn) {
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(AppDialTimeout))
		payload, err := ReadFramed(conn)
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil {
			return
		}
		fut := r.Apply(payload, AppApplyTimeout)
		applyErr := fut.Error()
		_ = conn.SetWriteDeadline(time.Now().Add(AppDialTimeout))
		defer conn.SetWriteDeadline(time.Time{})
		if applyErr == nil {
			_, _ = conn.Write([]byte{AppRespStatusOK})
			_ = WriteFramed(conn, nil)
			return
		}
		_, _ = conn.Write([]byte{AppRespStatusErr})
		_ = WriteFramed(conn, []byte(applyErr.Error()))
	}
}

// Forwarder forwards proposals from a follower to the current leader via the
// muxed transport's app channel. Construction binds it to a Manager so it can
// discover the leader's address through r.LeaderWithID().
type Forwarder struct {
	mgr         *Manager
	streamLayer *MuxedStreamLayer

	// MaxAttempts is the bound on ErrNotLeader retries before Submit gives up.
	// Default 3.
	MaxAttempts int
}

// NewForwarder constructs a Forwarder bound to mgr and the local stream layer
// (the layer is only used for the constants — Forwarder dials raw via
// net.DialTimeout + DialAppChannel-style handshake).
func NewForwarder(mgr *Manager, sl *MuxedStreamLayer) *Forwarder {
	return &Forwarder{mgr: mgr, streamLayer: sl, MaxAttempts: 3}
}

// ErrNoLeader is returned when LeaderWithID returns an empty address.
var ErrNoLeader = errors.New("forwarder: no known leader")

// Submit serialises payload, sends it to the current leader via the app
// channel, and blocks until the leader reports apply success or an error. On
// raft.ErrNotLeader the leader is re-looked-up and the call retries up to
// MaxAttempts times.
func (f *Forwarder) Submit(payload []byte, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = AppApplyTimeout
	}
	deadline := time.Now().Add(timeout)
	attempts := f.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}

	for attempt := 0; attempt < attempts; attempt++ {
		// Short-circuit: if we're the leader, apply locally.
		if f.mgr.IsLeader() {
			fut := f.mgr.Apply(payload, AppApplyTimeout)
			err := fut.Error()
			if err == nil {
				return nil
			}
			if !errors.Is(err, raft.ErrNotLeader) {
				return err
			}
			// Fall through to forward to current leader.
		}

		addr, _ := f.mgr.Raft().LeaderWithID()
		if addr == "" {
			// No known leader yet — sleep briefly then retry.
			if time.Now().After(deadline) {
				return ErrNoLeader
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}

		// Dial leader via app channel.
		dialTimeout := time.Until(deadline)
		if dialTimeout > AppDialTimeout {
			dialTimeout = AppDialTimeout
		}
		if dialTimeout <= 0 {
			return xerrors.Errorf("forwarder: timeout reached before dial")
		}
		conn, err := dialWithHandshake(string(addr), dialTimeout, HandshakeApp)
		if err != nil {
			// Transient dial error — retry.
			if time.Now().After(deadline) {
				return xerrors.Errorf("forwarder: dial %s: %w", addr, err)
			}
			continue
		}

		// Write the proposal payload.
		_ = conn.SetWriteDeadline(time.Now().Add(AppDialTimeout))
		if err := WriteFramed(conn, payload); err != nil {
			_ = conn.Close()
			return xerrors.Errorf("forwarder: write payload: %w", err)
		}
		_ = conn.SetWriteDeadline(time.Time{})

		// Read the response: 1-byte status + framed err.
		_ = conn.SetReadDeadline(time.Now().Add(time.Until(deadline) + time.Second))
		var status [1]byte
		if _, err := readN(conn, status[:]); err != nil {
			_ = conn.Close()
			if time.Now().After(deadline) {
				return xerrors.Errorf("forwarder: read status: %w", err)
			}
			continue
		}
		errBody, err := ReadFramed(conn)
		_ = conn.Close()
		if err != nil {
			return xerrors.Errorf("forwarder: read err body: %w", err)
		}
		if status[0] == AppRespStatusOK {
			return nil
		}
		msg := string(errBody)
		// Detect ErrNotLeader by message contents and retry.
		if msg == raft.ErrNotLeader.Error() {
			continue
		}
		return xerrors.New(msg)
	}
	return xerrors.Errorf("forwarder: exhausted %d attempts", attempts)
}

// readN is a tiny convenience to read exactly len(buf) bytes from conn.
func readN(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		if err != nil {
			return total + n, err
		}
		total += n
	}
	return total, nil
}

