package multicluster

import (
	"maps"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// Entry holds the runtime objects the operator maintains for one member cluster.
type Entry struct {
	Cluster cluster.Cluster
	Client  client.Client
	// ResourceName is the MemberCluster CR's metadata.name. Member-scoped resource names are
	// derived from it rather than from the logical cluster name, which is not necessarily
	// RFC 1123.
	ResourceName string
}

// Provider is the operator's registry of member clusters, keyed by the MemberCluster CR's
// spec.clusterName (the logical name referenced by workload CRs' clusterSpecList[].clusterName).
// Controllers snapshot it once per reconcile via Entries and thread the snapshot through,
// instead of holding a startup-built map.
type Provider struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

func NewProvider() *Provider {
	return &Provider{entries: map[string]Entry{}}
}

func (p *Provider) Set(clusterName string, entry Entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[clusterName] = entry
}

// Entries returns a copy of the registry; callers may mutate the copy freely.
func (p *Provider) Entries() map[string]Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entries := make(map[string]Entry, len(p.entries))
	maps.Copy(entries, p.entries)
	return entries
}
