package coordraft

import (
	"errors"
	"net"
	"time"

	"github.com/hashicorp/raft"
	"go.uber.org/zap"
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
// The conn is then closed by the server. As of G'5 iter 7 the app channel
// uses a dedicated TCP port (no handshake byte) so Istio's
// metadata_exchange / tcp_proxy filters fall through cleanly on every
// cross-cluster connection. Address derivation: forwarder maps the raft
// leader's address to the app port via AppPortFromRaftAddr (port+1) by
// default; explicit address resolution is provided by the AppAddrResolver
// hook for tests and future per-peer app port discovery.

// AppRespStatusOK / AppRespStatusErr are the 1-byte response status codes.
const (
	AppRespStatusOK  byte = 0
	AppRespStatusErr byte = 1
)

// AppDialTimeout is the default dial+handshake timeout for follower→leader
// proposal-forwarding connections.
var AppDialTimeout = 3 * time.Second

// AppApplyTimeout is the default raft.Apply timeout the leader enforces
// when applying a forwarded proposal. G iter 10 raised the default from
// 5s to 10s so a freshly-booted follower has room to latch the leader
// while the Istio sidecar warms up.
var AppApplyTimeout = 10 * time.Second

// Default backoffs used by Submit when retrying. Tunable per-Forwarder via
// LeaderBackoff / DialBackoff. G iter 10:
//   - leaderBackoff = 200ms (was hard-coded 50ms inline) — sleeps when
//     LeaderWithID() returns "" so the follower has time to receive a
//     heartbeat from the new leader.
//   - dialBackoff   = 100ms base; grows 2^attempt up to dialBackoffCap on
//     consecutive dial failures (was no sleep — tight CPU loop on a
//     cross-cluster outage).
//   - DefaultMaxAttempts = 30 — paired with leaderBackoff yields ~6s of
//     "no known leader" budget before Submit returns.
var (
	defaultLeaderBackoff = 200 * time.Millisecond
	defaultDialBackoff   = 100 * time.Millisecond
	dialBackoffCap       = 1 * time.Second
	DefaultMaxAttempts   = 30
)

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

// AppAddrResolver maps a raft peer's advertised TCP address (host:port) to
// the app-channel address used for proposal forwarding. Default
// implementation is AppPortFromRaftAddr (port+1).
type AppAddrResolver func(raftAddr string) (string, error)

// Forwarder forwards proposals from a follower to the current leader via the
// app-channel TCP listener. Construction binds it to a Manager so it can
// discover the leader's address through r.LeaderWithID().
type Forwarder struct {
	mgr         *Manager
	streamLayer *MuxedStreamLayer

	// ResolveAppAddr translates the leader's raft address to its app-channel
	// address. Defaults to AppPortFromRaftAddr (port+1) when unset.
	ResolveAppAddr AppAddrResolver

	// PeerAddrs maps raft.ServerID → "host:port" of that peer's RAFT bind
	// addr. Used to translate a leader's ServerID into a routable peer
	// address; the value reported by raft.LeaderWithID() can be a wildcard
	// ([::]:7000) when the bind addr was 0.0.0.0:7000, which is not
	// dial-able cross-host. When PeerAddrs[leaderID] is set Submit prefers
	// it over LeaderWithID()'s self-advertised addr. Production callers
	// populate this from cfg.Peers in BuildProductionCoordinator; tests
	// that already inject ResolveAppAddr can leave it nil and fall back to
	// the historical (advertised-addr → resolver) path.
	PeerAddrs map[raft.ServerID]string

	// MaxAttempts is the bound on retries before Submit gives up. Default
	// DefaultMaxAttempts (30). Each attempt may itself sleep up to
	// LeaderBackoff (no-leader) or DialBackoff*2^attempt (dial failure)
	// before reattempting, so an attempts count of 30 typically corresponds
	// to several seconds of real wall-clock budget. Submit also honours the
	// per-call deadline (timeout parameter); the loop exits early when the
	// deadline passes.
	MaxAttempts int

	// LeaderBackoff is the sleep between attempts when LeaderWithID()
	// returns "". Defaults to defaultLeaderBackoff (200ms).
	LeaderBackoff time.Duration

	// DialBackoff is the base sleep applied after a failed dial. Grows
	// 2^attempt up to dialBackoffCap. Defaults to defaultDialBackoff
	// (100ms).
	DialBackoff time.Duration

	// Logger, if non-nil, receives WARN-level per-attempt diagnostics. When
	// nil Submit logs through zap.S() (the operator's package-level sugared
	// logger). Tests can pass a no-op sugared logger to silence output.
	Logger *zap.SugaredLogger
}

// NewForwarder constructs a Forwarder bound to mgr and the local stream layer
// (sl is retained for parity with the historical signature; the forwarder
// dials peers via net.DialTimeout on the app-channel port).
func NewForwarder(mgr *Manager, sl *MuxedStreamLayer) *Forwarder {
	return &Forwarder{
		mgr:            mgr,
		streamLayer:    sl,
		ResolveAppAddr: AppPortFromRaftAddr,
		MaxAttempts:    DefaultMaxAttempts,
		LeaderBackoff:  defaultLeaderBackoff,
		DialBackoff:    defaultDialBackoff,
	}
}

// logger returns the configured logger or falls back to zap.S(). The fallback
// is fine in tests too because zap.S() is a no-op until ReplaceGlobals is
// called by the operator main; tests that need silence can install a
// dedicated logger via f.Logger.
func (f *Forwarder) logger() *zap.SugaredLogger {
	if f.Logger != nil {
		return f.Logger
	}
	return zap.S()
}

// ErrNoLeader is returned when LeaderWithID returns an empty address.
var ErrNoLeader = errors.New("forwarder: no known leader")

// Submit serialises payload, sends it to the current leader via the app
// channel, and blocks until the leader reports apply success or an error. On
// raft.ErrNotLeader the leader is re-looked-up and the call retries up to
// MaxAttempts times.
//
// G iter 10: every continue point now logs the underlying cause at WARN
// level (attempt number, deadline-remaining, branch, error, resolved app
// addr, raft LeaderWithID() raw return values) so production failure modes
// stop hiding behind the terminal "exhausted N attempts" message. The
// terminal error itself now includes the last observed cause.
func (f *Forwarder) Submit(payload []byte, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = AppApplyTimeout
	}
	deadline := time.Now().Add(timeout)
	attempts := f.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	leaderBackoff := f.LeaderBackoff
	if leaderBackoff <= 0 {
		leaderBackoff = defaultLeaderBackoff
	}
	dialBackoff := f.DialBackoff
	if dialBackoff <= 0 {
		dialBackoff = defaultDialBackoff
	}
	log := f.logger()
	var lastErr error
	dialFailures := 0

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
			lastErr = err
			log.Warnw("forwarder: local Apply returned ErrNotLeader, will dial new leader",
				"attempt", attempt, "remaining", time.Until(deadline))
		}

		rawAddr, rawID := f.mgr.Raft().LeaderWithID()
		addr := rawAddr
		// Prefer the configured routable peer address over LeaderWithID()'s
		// self-advertised bind addr. In pod-mode the leader's bind addr is
		// wildcard "[::]:7000" (because the helm chart binds 0.0.0.0:7000),
		// which is NOT dial-able from a follower pod — port+1 resolves to
		// the FOLLOWER'S OWN localhost app handler and burns the retry
		// budget on self-dials. The peer map carries the routable per-peer
		// Service DNS injected at construction by BuildProductionCoordinator.
		if rawID != "" && len(f.PeerAddrs) > 0 {
			if peerAddr, ok := f.PeerAddrs[rawID]; ok && peerAddr != "" {
				if peerAddr != string(rawAddr) {
					log.Infow("forwarder: overriding LeaderWithID() addr via PeerAddrs",
						"attempt", attempt,
						"leader_id_raw", string(rawID),
						"leader_addr_raw", string(rawAddr),
						"leader_addr_peer_map", peerAddr,
					)
				}
				addr = raft.ServerAddress(peerAddr)
			}
		}
		if addr == "" {
			// No known leader yet — sleep briefly then retry.
			lastErr = ErrNoLeader
			log.Warnw("forwarder: LeaderWithID() returned empty addr",
				"attempt", attempt,
				"remaining", time.Until(deadline),
				"branch", "no-leader",
				"raft_state", f.mgr.Raft().State().String(),
				"leader_id_raw", string(rawID),
			)
			if time.Now().After(deadline) {
				return wrapForwarderTerminal(lastErr, attempt+1)
			}
			time.Sleep(leaderBackoff)
			continue
		}

		// Dial leader via app channel.
		dialTimeout := time.Until(deadline)
		if dialTimeout > AppDialTimeout {
			dialTimeout = AppDialTimeout
		}
		if dialTimeout <= 0 {
			lastErr = xerrors.Errorf("forwarder: timeout reached before dial")
			return wrapForwarderTerminal(lastErr, attempt+1)
		}
		resolve := f.ResolveAppAddr
		if resolve == nil {
			resolve = AppPortFromRaftAddr
		}
		appAddr, err := resolve(string(addr))
		if err != nil {
			lastErr = xerrors.Errorf("forwarder: resolve app addr from raft %q: %w", addr, err)
			log.Warnw("forwarder: resolve app addr failed",
				"attempt", attempt, "branch", "resolve",
				"leader_addr_raw", string(addr),
				"leader_id_raw", string(rawID),
				"error", err)
			// resolve errors are not transient (mapping config bug) — return.
			return lastErr
		}
		conn, err := dialApp(appAddr, dialTimeout)
		if err != nil {
			lastErr = xerrors.Errorf("forwarder: dial %s: %w", appAddr, err)
			log.Warnw("forwarder: dial leader failed",
				"attempt", attempt,
				"remaining", time.Until(deadline),
				"branch", "dial",
				"app_addr", appAddr,
				"leader_addr_raw", string(addr),
				"leader_id_raw", string(rawID),
				"error", err)
			if time.Now().After(deadline) {
				return wrapForwarderTerminal(lastErr, attempt+1)
			}
			// Exponential backoff on consecutive dial failures: dialBackoff*2^k
			// up to dialBackoffCap. dialFailures resets on a clean dial.
			sleep := dialBackoff << uint(dialFailures)
			if sleep > dialBackoffCap {
				sleep = dialBackoffCap
			}
			dialFailures++
			time.Sleep(sleep)
			continue
		}
		dialFailures = 0

		// Write the proposal payload.
		_ = conn.SetWriteDeadline(time.Now().Add(AppDialTimeout))
		if err := WriteFramed(conn, payload); err != nil {
			_ = conn.Close()
			lastErr = xerrors.Errorf("forwarder: write payload: %w", err)
			log.Warnw("forwarder: write payload failed",
				"attempt", attempt,
				"remaining", time.Until(deadline),
				"branch", "write",
				"app_addr", appAddr,
				"leader_addr_raw", string(addr),
				"leader_id_raw", string(rawID),
				"error", err)
			if time.Now().After(deadline) {
				return wrapForwarderTerminal(lastErr, attempt+1)
			}
			continue
		}
		_ = conn.SetWriteDeadline(time.Time{})

		// Read the response: 1-byte status + framed err.
		_ = conn.SetReadDeadline(time.Now().Add(time.Until(deadline) + time.Second))
		var status [1]byte
		if _, err := readN(conn, status[:]); err != nil {
			_ = conn.Close()
			lastErr = xerrors.Errorf("forwarder: read status: %w", err)
			log.Warnw("forwarder: read status failed",
				"attempt", attempt,
				"remaining", time.Until(deadline),
				"branch", "read-status",
				"app_addr", appAddr,
				"leader_addr_raw", string(addr),
				"leader_id_raw", string(rawID),
				"error", err)
			if time.Now().After(deadline) {
				return wrapForwarderTerminal(lastErr, attempt+1)
			}
			continue
		}
		errBody, err := ReadFramed(conn)
		_ = conn.Close()
		if err != nil {
			lastErr = xerrors.Errorf("forwarder: read err body: %w", err)
			log.Warnw("forwarder: read err body failed",
				"attempt", attempt,
				"remaining", time.Until(deadline),
				"branch", "read-body",
				"app_addr", appAddr,
				"leader_addr_raw", string(addr),
				"leader_id_raw", string(rawID),
				"error", err)
			return lastErr
		}
		if status[0] == AppRespStatusOK {
			return nil
		}
		msg := string(errBody)
		// Detect ErrNotLeader by message contents and retry.
		if msg == raft.ErrNotLeader.Error() {
			lastErr = raft.ErrNotLeader
			log.Warnw("forwarder: leader reported ErrNotLeader, will re-resolve",
				"attempt", attempt,
				"remaining", time.Until(deadline),
				"branch", "not-leader",
				"app_addr", appAddr,
				"leader_addr_raw", string(addr),
				"leader_id_raw", string(rawID))
			continue
		}
		return xerrors.New(msg)
	}
	return wrapForwarderTerminal(lastErr, attempts)
}

// wrapForwarderTerminal builds the terminal error message returned by
// Submit when MaxAttempts is reached or the deadline elapsed. The message
// embeds the LAST observed per-attempt cause so the operator log surfaces
// the actual failure (no-leader / dial / write / read) instead of the
// opaque pre-iter-10 "exhausted N attempts" string.
func wrapForwarderTerminal(lastErr error, attempts int) error {
	if lastErr == nil {
		return xerrors.Errorf("forwarder: exhausted %d attempts (no error captured)", attempts)
	}
	return xerrors.Errorf("forwarder: exhausted %d attempts; last error: %w", attempts, lastErr)
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

