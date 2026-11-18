package featuregate

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

var testRequirement = RBACRequirement{Group: "mongodb.com", Resource: "clustermongodbroles", Verbs: []string{"get", "list", "watch"}}

// ssarClient returns a fake client whose SelfSubjectAccessReview creates are answered
// according to the allowed flag; probes counts the SSAR requests seen. The client is
// wrapped in a pointer type so multicluster.Entry values holding it stay comparable.
func ssarClient(allowed *atomic.Bool, probes *atomic.Int32) client.Client {
	scheme := runtime.NewScheme()
	if err := authorizationv1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			review, ok := obj.(*authorizationv1.SelfSubjectAccessReview)
			if !ok {
				return nil
			}
			probes.Add(1)
			review.Status.Allowed = allowed.Load()
			return nil
		},
	}).Build()
	return &pointerClient{Client: c}
}

type pointerClient struct {
	client.Client
}

func entryWithClient(c client.Client) multicluster.Entry {
	return multicluster.Entry{Cluster: multicluster.New(c), Client: c, ResourceName: "own-cluster"}
}

func TestRBACGateEmptyIdentityIsNeverEnabled(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	var allowed atomic.Bool
	var probes atomic.Int32
	allowed.Store(true)
	gate := NewRBACGate(ctx, "", provider, testRequirement, zap.S())

	provider.Set(ctx, "", entryWithClient(ssarClient(&allowed, &probes)))

	assert.False(t, gate.Enabled(ctx))
	assert.Zero(t, probes.Load())
}

func TestRBACGateDisabledWhenOwnClusterEntryAbsent(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	gate := NewRBACGate(ctx, "own", provider, testRequirement, zap.S())

	var allowed atomic.Bool
	var probes atomic.Int32
	allowed.Store(true)
	provider.Set(ctx, "other", entryWithClient(ssarClient(&allowed, &probes)))

	assert.False(t, gate.Enabled(ctx))
	assert.Zero(t, probes.Load())
}

func TestRBACGateEnabledWhenSSARAllows(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	gate := NewRBACGate(ctx, "own", provider, testRequirement, zap.S())

	assert.False(t, gate.Enabled(ctx))

	var allowed atomic.Bool
	var probes atomic.Int32
	allowed.Store(true)
	provider.Set(ctx, "own", entryWithClient(ssarClient(&allowed, &probes)))
	// provider.Set fired the OnAdd hook, which probed once already; reset before
	// asserting the probes an Enabled call performs.
	probes.Store(0)

	assert.True(t, gate.Enabled(ctx))
	assert.Equal(t, int32(len(testRequirement.Verbs)), probes.Load())
}

func TestRBACGateStaysDisabledWhenSSARDenies(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	gate := NewRBACGate(ctx, "own", provider, testRequirement, zap.S())

	var allowed atomic.Bool
	var probes atomic.Int32
	provider.Set(ctx, "own", entryWithClient(ssarClient(&allowed, &probes)))
	probes.Store(0)

	assert.False(t, gate.Enabled(ctx))
	// The probe stops at the first denied verb.
	assert.Equal(t, int32(1), probes.Load())
}

func TestRBACGateDisabledWhenOwnClusterEntryRemoved(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	gate := NewRBACGate(ctx, "own", provider, testRequirement, zap.S())

	var allowed atomic.Bool
	var probes atomic.Int32
	allowed.Store(true)
	provider.Set(ctx, "own", entryWithClient(ssarClient(&allowed, &probes)))
	assert.True(t, gate.Enabled(ctx))

	provider.Delete(ctx, "own", "own-cluster")
	assert.False(t, gate.Enabled(ctx))
}

func TestRBACGateProbesEveryCall(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	gate := NewRBACGate(ctx, "own", provider, testRequirement, zap.S())

	var allowed atomic.Bool
	var probes atomic.Int32
	provider.Set(ctx, "own", entryWithClient(ssarClient(&allowed, &probes)))

	// Every Enabled call reflects the current RBAC state and re-probes: there is no
	// caching or throttling.
	probes.Store(0)
	assert.False(t, gate.Enabled(ctx))
	assert.Equal(t, int32(1), probes.Load())

	probes.Store(0)
	allowed.Store(true)
	assert.True(t, gate.Enabled(ctx))
	assert.Equal(t, int32(len(testRequirement.Verbs)), probes.Load())

	probes.Store(0)
	allowed.Store(false)
	assert.False(t, gate.Enabled(ctx))
	assert.Equal(t, int32(1), probes.Load())
}

// watchCountingController implements controller.Controller, counting Watch calls.
type watchCountingController struct {
	reconcile.Reconciler
	watches atomic.Int32
}

func (f *watchCountingController) Watch(source.TypedSource[reconcile.Request]) error {
	f.watches.Add(1)
	return nil
}

func (f *watchCountingController) Start(context.Context) error { return nil }

func (f *watchCountingController) GetLogger() logr.Logger { return logr.Discard() }

func TestRBACGateEnsureWatches(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	var allowed atomic.Bool
	var probes atomic.Int32
	allowed.Store(true)
	gate := NewRBACGate(ctx, "own", provider, testRequirement, zap.S())

	ctrl := &watchCountingController{}
	gate.AddWatch(ctrl, &corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request { return nil }))

	// No own-cluster entry yet: no-op.
	gate.EnsureWatches(ctx)
	assert.Zero(t, ctrl.watches.Load())

	provider.Set(ctx, "own", entryWithClient(ssarClient(&allowed, &probes)))
	// The OnAdd hook probed (allowed) and attached the watch.
	assert.Equal(t, int32(1), ctrl.watches.Load())

	// Attach-once: a repeated EnsureWatches for the same entry does not re-attach.
	gate.EnsureWatches(ctx)
	assert.Equal(t, int32(1), ctrl.watches.Load())

	// A new entry (e.g. credential rotation) differs by value and re-attaches.
	provider.Set(ctx, "own", entryWithClient(ssarClient(&allowed, &probes)))
	assert.Equal(t, int32(2), ctrl.watches.Load())
}

func TestRBACGateEnsureWatchesNoOpWithoutIdentityOrProvider(t *testing.T) {
	ctx := context.Background()
	provider := multicluster.NewProvider()
	var allowed atomic.Bool
	var probes atomic.Int32
	allowed.Store(true)
	provider.Set(ctx, "own", entryWithClient(ssarClient(&allowed, &probes)))

	noIdentity := NewRBACGate(ctx, "", provider, testRequirement, zap.S())
	ctrl := &watchCountingController{}
	noIdentity.AddWatch(ctrl, &corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request { return nil }))
	noIdentity.EnsureWatches(ctx)
	assert.Zero(t, ctrl.watches.Load())

	noProvider := NewRBACGate(ctx, "own", nil, testRequirement, zap.S())
	noProvider.EnsureWatches(ctx)
	assert.Zero(t, ctrl.watches.Load())
}
