// Package featuregate is the home for day-2 optional-feature gates: features the
// operator enables based on day-2 state (such as RBAC presence on a member cluster)
// rather than install-time values.
package featuregate

import (
	"context"
	"sync"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// RBACRequirement describes the permissions a member ServiceAccount must hold for the
// gated feature to be enabled.
type RBACRequirement struct {
	Group    string
	Resource string
	Verbs    []string
}

type registeredWatch struct {
	ctrl controller.Controller
	obj  client.Object
	h    handler.TypedEventHandler[client.Object, reconcile.Request]
}

// RBACGate gates an optional feature on the operator's own member-cluster entry being
// registered in the multicluster.Provider and its ServiceAccount holding the required
// RBAC there. Enabled is a pure predicate: it verifies the requirement via
// SelfSubjectAccessReview on every call, so granting or revoking the RBAC takes effect
// on the next probe.
type RBACGate struct {
	identity string
	provider *multicluster.Provider
	req      RBACRequirement
	log      *zap.SugaredLogger

	mu            sync.Mutex
	watches       []registeredWatch
	attachedEntry *multicluster.Entry
}

// NewRBACGate returns a gate for the operator's own cluster (identified by identity) and
// registers an OnAdd hook on provider that attaches the registered watches as soon as the
// own-cluster entry appears with the required RBAC. An empty identity means the gate can
// never be enabled.
func NewRBACGate(ctx context.Context, identity string, provider *multicluster.Provider, req RBACRequirement, log *zap.SugaredLogger) *RBACGate {
	g := &RBACGate{identity: identity, provider: provider, req: req, log: log}
	if provider != nil && identity != "" {
		provider.RegisterHooks(ctx, multicluster.Hooks{
			OnAdd: func(ctx context.Context, clusterName string, _ multicluster.Entry) {
				if clusterName == g.identity && g.Enabled(ctx) {
					g.EnsureWatches(ctx)
				}
			},
		})
	}
	return g
}

// Enabled reports whether the operator's own member-cluster ServiceAccount currently
// holds the required RBAC, verified via SelfSubjectAccessReview on every call. It is a
// pure predicate: it mutates no state.
func (g *RBACGate) Enabled(ctx context.Context) bool {
	if g.provider == nil || g.identity == "" {
		return false
	}
	entry, ok := g.provider.Get(g.identity)
	if !ok {
		return false
	}
	for _, verb := range g.req.Verbs {
		review := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Group:    g.req.Group,
					Resource: g.req.Resource,
					Verb:     verb,
				},
			},
		}
		if err := entry.Client.Create(ctx, review); err != nil {
			g.log.Debugf("RBAC probe for %s.%s verb %q on the operator's own cluster failed: %s", g.req.Resource, g.req.Group, verb, err)
			return false
		}
		if !review.Status.Allowed {
			g.log.Debugf("RBAC probe for %s.%s verb %q on the operator's own cluster was denied", g.req.Resource, g.req.Group, verb)
			return false
		}
	}
	return true
}

// AddWatch registers a watch for obj with the given controller; the watch is attached to
// the own-cluster member entry's cache by EnsureWatches. Registration usually happens at
// controller setup, before any member entry exists.
func (g *RBACGate) AddWatch(ctrl controller.Controller, obj client.Object, h handler.TypedEventHandler[client.Object, reconcile.Request]) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.watches = append(g.watches, registeredWatch{ctrl: ctrl, obj: obj, h: h})
}

// EnsureWatches attaches the registered watches to the own-cluster entry's cache if not
// already attached for that entry. Attachment happens once per entry value: a new entry
// (e.g. after credential rotation) differs by value and re-attaches, while dead sources
// from the old entry are harmless.
func (g *RBACGate) EnsureWatches(ctx context.Context) {
	if g.provider == nil || g.identity == "" {
		return
	}
	entry, ok := g.provider.Get(g.identity)
	if !ok {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.attachedEntry != nil && *g.attachedEntry == entry {
		return
	}
	for _, w := range g.watches {
		if err := w.ctrl.Watch(source.Kind(entry.Cluster.GetCache(), w.obj, w.h)); err != nil {
			g.log.Errorf("failed to attach watch for %T on the operator's own cluster: %s", w.obj, err)
		}
	}
	g.attachedEntry = &entry
}
