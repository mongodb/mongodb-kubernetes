package haraft

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubeTransport implements raft.Transport over per-cluster raft-inbox ConfigMaps.
type KubeTransport struct {
	localID      string
	namespace    string
	peerClients  map[string]client.Client // includes the local cluster
	pollInterval time.Duration

	consumer chan raft.RPC

	// pending replies keyed by ReplyKey
	pendingMu sync.Mutex
	pending   map[string]chan replyResult

	heartbeatHandler func(raft.RPC)

	stopCh chan struct{}
}

type replyResult struct {
	payload []byte
	err     error
}

// NewKubeTransport constructs a transport. The peerClients map MUST contain
// an entry for every Raft peer including the local cluster (used for the inbox
// polling loop).
func NewKubeTransport(localID, namespace string, peerClients map[string]client.Client, pollInterval time.Duration) *KubeTransport {
	return &KubeTransport{
		localID:      localID,
		namespace:    namespace,
		peerClients:  peerClients,
		pollInterval: pollInterval,
		consumer:     make(chan raft.RPC, 64),
		pending:      make(map[string]chan replyResult),
		stopCh:       make(chan struct{}),
	}
}

// start launches background goroutines (polling). Exposed for tests via
// package-private name; production callers invoke through RaftNode.
func (t *KubeTransport) start(ctx context.Context) error {
	go t.pollLoop(ctx)
	return nil
}

// --- raft.Transport interface (sending side) ---

func (t *KubeTransport) Consumer() <-chan raft.RPC                               { return t.consumer }
func (t *KubeTransport) LocalAddr() raft.ServerAddress                           { return raft.ServerAddress(t.localID) }
func (t *KubeTransport) EncodePeer(_ raft.ServerID, a raft.ServerAddress) []byte { return []byte(a) }
func (t *KubeTransport) DecodePeer(b []byte) raft.ServerAddress                  { return raft.ServerAddress(b) }
func (t *KubeTransport) SetHeartbeatHandler(cb func(raft.RPC))                   { t.heartbeatHandler = cb }
func (t *KubeTransport) Close() error                                            { close(t.stopCh); return nil }

// AppendEntriesPipeline returns ErrPipelineReplicationNotSupported. Pipelined
// replication assumes connection-oriented transport; we use one-shot writes.
func (t *KubeTransport) AppendEntriesPipeline(_ raft.ServerID, _ raft.ServerAddress) (raft.AppendPipeline, error) {
	return nil, raft.ErrPipelineReplicationNotSupported
}

func (t *KubeTransport) AppendEntries(id raft.ServerID, target raft.ServerAddress, args *raft.AppendEntriesRequest, resp *raft.AppendEntriesResponse) error {
	return t.roundTrip("AppendEntries", string(target), args, resp)
}

func (t *KubeTransport) RequestVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestVoteRequest, resp *raft.RequestVoteResponse) error {
	return t.roundTrip("RequestVote", string(target), args, resp)
}

func (t *KubeTransport) InstallSnapshot(id raft.ServerID, target raft.ServerAddress, args *raft.InstallSnapshotRequest, resp *raft.InstallSnapshotResponse, src io.Reader) error {
	// Snapshots are not used (empty FSM); return an error if Raft ever calls this.
	return fmt.Errorf("InstallSnapshot not supported by KubeTransport (empty FSM)")
}

func (t *KubeTransport) TimeoutNow(id raft.ServerID, target raft.ServerAddress, args *raft.TimeoutNowRequest, resp *raft.TimeoutNowResponse) error {
	return t.roundTrip("TimeoutNow", string(target), args, resp)
}

// roundTrip writes an envelope to target's inbox, registers a pending reply
// channel, waits for the reply (or timeout), and decodes it into resp.
func (t *KubeTransport) roundTrip(msgType, target string, args, resp interface{}) error {
	cl, ok := t.peerClients[target]
	if !ok {
		return fmt.Errorf("no client for peer %q", target)
	}

	payload, err := encodeRPC(args)
	if err != nil {
		return err
	}
	replyKey := uuid.NewString()

	replyCh := make(chan replyResult, 1)
	t.pendingMu.Lock()
	t.pending[replyKey] = replyCh
	t.pendingMu.Unlock()
	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, replyKey)
		t.pendingMu.Unlock()
	}()

	env := RaftEnvelope{MsgType: msgType, From: t.localID, ReplyKey: replyKey, Payload: payload}
	if err := t.writeEnvelope(cl, env, uuid.NewString()); err != nil {
		return err
	}

	select {
	case r := <-replyCh:
		if r.err != nil {
			return r.err
		}
		return decodeRPC(r.payload, resp)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("RPC %s to %s timed out", msgType, target)
	case <-t.stopCh:
		return fmt.Errorf("transport closed")
	}
}

func (t *KubeTransport) writeEnvelope(cl client.Client, env RaftEnvelope, key string) error {
	encoded, err := encodeEnvelope(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cm := &corev1.ConfigMap{}
	if err := cl.Get(ctx, types.NamespacedName{Name: InboxConfigMapName, Namespace: t.namespace}, cm); err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["msg-"+key] = encoded
	return cl.Update(ctx, cm)
}

func (t *KubeTransport) pollLoop(ctx context.Context) {
	tick := time.NewTicker(t.pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case <-tick.C:
			t.drainInbox(ctx)
		}
	}
}

func (t *KubeTransport) drainInbox(ctx context.Context) {
	cl := t.peerClients[t.localID]
	cm := &corev1.ConfigMap{}
	if err := cl.Get(ctx, types.NamespacedName{Name: InboxConfigMapName, Namespace: t.namespace}, cm); err != nil {
		return
	}
	if len(cm.Data) == 0 {
		return
	}

	consumed := []string{}
	for key, raw := range cm.Data {
		env, err := decodeEnvelope(raw)
		if err != nil {
			consumed = append(consumed, key)
			continue
		}
		if env.MsgType == "Reply" {
			t.pendingMu.Lock()
			ch, ok := t.pending[env.ReplyKey]
			t.pendingMu.Unlock()
			if ok {
				ch <- replyResult{payload: env.Payload}
			}
			consumed = append(consumed, key)
			continue
		}
		// Dispatch as an incoming RPC.
		t.dispatchRPC(env)
		consumed = append(consumed, key)
	}

	if len(consumed) == 0 {
		return
	}
	for _, k := range consumed {
		delete(cm.Data, k)
	}
	if err := cl.Update(ctx, cm); err != nil {
		// Conflict (409) is expected and benign — another writer added a
		// message after our Get; next poll will pick it up. Log everything
		// else: forbidden/unreachable here means we may double-process the
		// same envelopes on the next poll, so it's worth surfacing.
		zap.S().Debugf("haraft: drainInbox Update on cluster=%q failed: %v", t.localID, err)
	}
}

func (t *KubeTransport) dispatchRPC(env RaftEnvelope) {
	var cmd interface{}
	switch env.MsgType {
	case "AppendEntries":
		cmd = &raft.AppendEntriesRequest{}
	case "RequestVote":
		cmd = &raft.RequestVoteRequest{}
	case "InstallSnapshot":
		cmd = &raft.InstallSnapshotRequest{}
	case "TimeoutNow":
		cmd = &raft.TimeoutNowRequest{}
	default:
		return
	}
	if err := decodeRPC(env.Payload, cmd); err != nil {
		return
	}
	respCh := make(chan raft.RPCResponse, 1)
	t.consumer <- raft.RPC{Command: cmd, RespChan: respCh}

	go func() {
		r := <-respCh
		if r.Error != nil {
			return
		}
		t.sendReply(env.From, env.ReplyKey, r.Response)
	}()
}

func (t *KubeTransport) sendReply(target, replyKey string, resp interface{}) {
	cl, ok := t.peerClients[target]
	if !ok {
		zap.S().Warnf("haraft: sendReply: no peer client for target=%q replyKey=%s", target, replyKey)
		return
	}
	payload, err := encodeRPC(resp)
	if err != nil {
		zap.S().Warnf("haraft: sendReply: encodeRPC failed for target=%q replyKey=%s: %v", target, replyKey, err)
		return
	}
	env := RaftEnvelope{MsgType: "Reply", From: t.localID, ReplyKey: replyKey, Payload: payload}
	if err := t.writeEnvelope(cl, env, replyKey); err != nil {
		zap.S().Warnf("haraft: sendReply: writeEnvelope failed (target=%q replyKey=%s): %v", target, replyKey, err)
	}
}
