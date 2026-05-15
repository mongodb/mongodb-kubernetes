package haraft

import "time"

// RaftEnvelope is the wire format for one Raft RPC delivered via a
// raft-inbox ConfigMap. One ConfigMap key holds one base64-encoded envelope.
type RaftEnvelope struct {
	MsgType  string `json:"msgType"`  // "AppendEntries", "RequestVote", "InstallSnapshot", "TimeoutNow", "Reply"
	From     string `json:"from"`     // sender cluster name
	ReplyKey string `json:"replyKey"` // (optional) UUID key to write the response into the sender's inbox
	Payload  []byte `json:"payload"`  // marshaled hashicorp/raft RPC args or response (msgpack via raft.NewJSONCodec equivalent)
}

// NodeConfig configures a RaftNode.
//
// All clusters are symmetric. There is no designated "bootstrap" node — every
// RaftNode calls hashicorp/raft's BootstrapCluster() at startup with the same
// voter list (NodeConfig.Peers). The API is safe to call from every node:
// subsequent calls on already-initialized state return ErrCantBootstrap, which
// the node ignores.
type NodeConfig struct {
	// ClusterName is this node's identifier (matches kubeconfig context name).
	ClusterName string
	// Peers is the full Raft membership including this cluster.
	Peers []string
	// Namespace is the operator namespace where raft-* ConfigMaps live.
	Namespace string
	// HeartbeatTimeout, ElectionTimeout, CommitTimeout, LeaderLeaseTimeout
	// override hashicorp/raft defaults to suit ConfigMap polling latencies.
	HeartbeatTimeout   time.Duration
	ElectionTimeout    time.Duration
	CommitTimeout      time.Duration
	LeaderLeaseTimeout time.Duration
	// PollInterval is how often the transport polls its raft-inbox.
	PollInterval time.Duration
}

// DefaultTimings returns timings tuned for ConfigMap polling.
// hashicorp/raft requires LeaderLeaseTimeout <= HeartbeatTimeout and
// ElectionTimeout >= HeartbeatTimeout.
func DefaultTimings() (heartbeat, election, commit, leaderLease, poll time.Duration) {
	return 2 * time.Second,
		4 * time.Second,
		500 * time.Millisecond,
		1 * time.Second,
		200 * time.Millisecond
}

// ConfigMap names used by the haraft package, all in NodeConfig.Namespace.
const (
	InboxConfigMapName    = "raft-inbox"
	IdentityConfigMapName = "raft-identity"
	PeersConfigMapName    = "raft-peers"
	StateConfigMapName    = "raft-state"
	LeaderConfigMapName   = "raft-leader"

	IdentityKeyClusterName = "clusterName"
	PeersKeyMembers        = "members" // comma-separated cluster names
	LeaderKeyClusterName   = "clusterName"
)
