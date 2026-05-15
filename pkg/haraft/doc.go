// Package haraft provides multi-cluster leader election for the MCK operator
// using hashicorp/raft over a custom Kubernetes ConfigMap transport.
//
// Application state is not replicated through Raft — the FSM is intentionally
// empty. The leader uses normal Kubernetes writes (via the existing
// cluster-client map) to propagate the MongoDBMultiCluster CR to peers.
//
// See docs/superpowers/specs/2026-05-11-ha-multicluster-operator-design.md
package haraft
