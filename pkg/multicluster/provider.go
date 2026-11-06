package multicluster

import (
	"context"
	"maps"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
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

// Hooks are callbacks the Provider invokes when a member-cluster entry is added or removed.
// Controllers register them at setup time: OnAdd attaches the controller's per-cluster
// watches to the new cluster (controller-runtime supports Watch after start) and enqueues
// the controller's CRs, so that expanding to a cluster where a CR owns no resources yet
// still reconciles it. OnRemove releases per-cluster state (e.g. health checkers); watched
// informers on the removed cluster stop with the entry's context, and resources there are
// abandoned by design.
type Hooks struct {
	OnAdd    func(ctx context.Context, clusterName string, entry Entry)
	OnRemove func(ctx context.Context, clusterName string, entry Entry)
}

// Provider is the operator's registry of member clusters, keyed by the MemberCluster CR's
// spec.clusterName (the logical name referenced by workload CRs' clusterSpecList[].clusterName).
// Controllers snapshot it once per reconcile via Entries and thread the snapshot through.
type Provider struct {
	mu      sync.RWMutex
	entries map[string]Entry
	hooks   []Hooks
}

func NewProvider() *Provider {
	return &Provider{entries: map[string]Entry{}}
}

// RegisterHooks adds hooks invoked on every subsequent Set/Delete, and immediately engages
// them for entries already present, so registration order relative to Set does not matter.
func (p *Provider) RegisterHooks(ctx context.Context, hooks Hooks) {
	p.mu.Lock()
	p.hooks = append(p.hooks, hooks)
	entries := maps.Clone(p.entries)
	p.mu.Unlock()

	if hooks.OnAdd != nil {
		for clusterName, entry := range entries {
			hooks.OnAdd(ctx, clusterName, entry)
		}
	}
}

func (p *Provider) Set(ctx context.Context, clusterName string, entry Entry) {
	p.mu.Lock()
	p.entries[clusterName] = entry
	hooks := slices.Clone(p.hooks)
	p.mu.Unlock()

	for _, h := range hooks {
		if h.OnAdd != nil {
			h.OnAdd(ctx, clusterName, entry)
		}
	}
}

// Delete removes the entry for clusterName only if it is still owned by the MemberCluster
// CR named resourceName, and fires OnRemove hooks only when a deletion actually happened.
// Entry ownership can transfer between the caller's state check and the delete, so the
// comparison must be atomic with it.
func (p *Provider) Delete(ctx context.Context, clusterName, resourceName string) {
	p.mu.Lock()
	entry, ok := p.entries[clusterName]
	if ok && entry.ResourceName == resourceName {
		delete(p.entries, clusterName)
	} else {
		ok = false
	}
	hooks := slices.Clone(p.hooks)
	p.mu.Unlock()

	if !ok {
		return
	}
	for _, h := range hooks {
		if h.OnRemove != nil {
			h.OnRemove(ctx, clusterName, entry)
		}
	}
}

// Entries returns a copy of the registry; callers may mutate the copy freely.
func (p *Provider) Entries() map[string]Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entries := make(map[string]Entry, len(p.entries))
	maps.Copy(entries, p.entries)
	return entries
}

// EnqueueAll lists all objects of the given type on the central cluster and pushes a generic
// event for each onto ch. Controllers call it from Hooks.OnAdd so that adding a cluster
// reconciles CRs that own no resources on that cluster yet (watch replay alone cannot reach
// those).
func EnqueueAll(ctx context.Context, c client.Client, list client.ObjectList, ch chan<- event.GenericEvent) error {
	if err := c.List(ctx, list); err != nil {
		return err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return err
	}
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok {
			continue
		}
		select {
		case ch <- event.GenericEvent{Object: obj}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
