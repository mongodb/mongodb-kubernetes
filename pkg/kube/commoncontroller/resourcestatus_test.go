package commoncontroller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

const (
	commoncontrollerMongoDBName      = "mdb"
	commoncontrollerMongoDBNamespace = "ns"
)

func newMongoDBForStatusTests(t *testing.T) *mdbv1.MongoDB {
	t.Helper()
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName(commoncontrollerMongoDBName).
		SetNamespace(commoncontrollerMongoDBNamespace).
		SetMembers(3).
		Build()
	mdb.ResourceVersion = "2"
	mdb.UpdateStatus(status.PhaseRunning, status.ReplicaSetMembersOption{Members: 3})
	return mdb
}

func newMongoDBStatusClient(t *testing.T, obj *mdbv1.MongoDB) kubernetesClient.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(scheme))
	return kubernetesClient.NewClient(fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&mdbv1.MongoDB{}).
		WithObjects(obj).
		Build())
}

func TestStatusSubresourcePatchPaths(t *testing.T) {
	tests := []struct {
		name     string
		fullPath string
		want     []string
	}{
		{name: "status root", fullPath: "/status", want: []string{"/status"}},
		{name: "nested substatus", fullPath: "/status/loadBalancer", want: []string{"/status", "/status/loadBalancer"}},
		{name: "deeply nested substatus", fullPath: "/status/opsManager/backup", want: []string{"/status", "/status/opsManager", "/status/opsManager/backup"}},
		{name: "empty path", fullPath: "", want: nil},
		{name: "bare slash never patches the document root", fullPath: "/", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := statusSubresourcePatchPaths(tc.fullPath)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "/", "a JSON-patch add at the root replaces the entire status")
		})
	}
}

// Bootstrapping /status/loadBalancer must create only the missing members:
// JSON-patch add REPLACES an existing member, so adding an existing /status
// would wipe sibling fields written by the main controller.
func TestEnsureStatusSubresourceExists_DoesNotClobberExistingStatus(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus map[string]any
	}{
		{name: "status missing entirely", initialStatus: nil},
		{
			name:          "status exists without loadBalancer",
			initialStatus: map[string]any{"phase": "Running", "version": "1.2.3"},
		},
		{
			name: "status and loadBalancer both exist",
			initialStatus: map[string]any{
				"phase":        "Running",
				"version":      "1.2.3",
				"loadBalancer": map[string]any{"phase": "Pending", "message": "deploying"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// Seeded as unstructured: a typed object always marshals "status",
			// so only unstructured can represent a CR without it.
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "mongodb.com/v1",
				"kind":       "MongoDBSearch",
				"metadata":   map[string]any{"name": "mdbs", "namespace": "ns"},
				"spec":       map[string]any{},
			}}
			if tc.initialStatus != nil {
				obj.Object["status"] = tc.initialStatus
			}
			scheme := runtime.NewScheme()
			require.NoError(t, v1.AddToScheme(scheme))
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&searchv1.MongoDBSearch{}).
				WithObjects(obj).
				Build()

			search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdbs", Namespace: "ns"}}
			require.NoError(t, ensureStatusSubresourceExists(ctx, kubernetesClient.NewClient(c), search,
				searchv1.NewSearchPartOption(searchv1.SearchPartLoadBalancer)))

			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(obj.GroupVersionKind())
			require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mdbs", Namespace: "ns"}, got))
			gotStatus, ok := got.Object["status"].(map[string]any)
			require.True(t, ok, "/status must exist after ensure")
			assert.Contains(t, gotStatus, "loadBalancer", "/status/loadBalancer must exist after ensure")

			if tc.initialStatus == nil {
				return
			}
			assert.Equal(t, "Running", gotStatus["phase"], "sentinel status field must survive the loadBalancer bootstrap")
			assert.Equal(t, "1.2.3", gotStatus["version"], "sentinel status field must survive the loadBalancer bootstrap")
			if lb, exists := tc.initialStatus["loadBalancer"]; exists {
				assert.Equal(t, lb, gotStatus["loadBalancer"], "existing loadBalancer content must be untouched")
			}
		})
	}
}

func TestUpdateStatusOptimistic_ConflictLeavesServerStatusUntouched(t *testing.T) {
	ctx := context.Background()
	log := zap.NewNop().Sugar()
	server := newMongoDBForStatusTests(t)
	cl := newMongoDBStatusClient(t, server)
	stale := server.DeepCopy()
	stale.ResourceVersion = "1"
	stale.UpdateStatus(status.PhasePending, status.ReplicaSetMembersOption{Members: 1})

	_, err := UpdateStatusOptimistic(ctx, cl, stale, workflow.OK(), log)
	require.Error(t, err)
	assert.True(t, apiErrors.IsConflict(err))

	got := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: commoncontrollerMongoDBName, Namespace: commoncontrollerMongoDBNamespace}, got))
	assert.Equal(t, status.PhaseRunning, got.Status.Phase)
	assert.Equal(t, 3, got.Status.Members)
	assert.Equal(t, "2", got.ResourceVersion)
}

func TestUpdateStatusOptimistic_SucceedsWithCurrentVersionAndRefreshesObject(t *testing.T) {
	ctx := context.Background()
	log := zap.NewNop().Sugar()
	server := newMongoDBForStatusTests(t)
	cl := newMongoDBStatusClient(t, server)
	current := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: commoncontrollerMongoDBName, Namespace: commoncontrollerMongoDBNamespace}, current))

	current.Spec.Members = 4
	current.UpdateStatus(status.PhasePending, status.ReplicaSetMembersOption{Members: 4})

	_, err := UpdateStatusOptimistic(ctx, cl, current, workflow.Pending("scaling"), log, status.ReplicaSetMembersOption{Members: 4})
	require.NoError(t, err)

	got := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: commoncontrollerMongoDBName, Namespace: commoncontrollerMongoDBNamespace}, got))
	assert.Equal(t, current.ResourceVersion, got.ResourceVersion)
	assert.Equal(t, got.ResourceVersion, current.ResourceVersion)
	assert.Equal(t, status.PhasePending, got.Status.Phase)
	assert.Equal(t, 4, got.Status.Members)
	assert.Equal(t, got.Status.Phase, current.Status.Phase)
	assert.Equal(t, got.Status.Members, current.Status.Members)
}

func TestUpdateStatusOptimistic_SecondWriteOnSameObjectSucceeds(t *testing.T) {
	ctx := context.Background()
	log := zap.NewNop().Sugar()
	server := newMongoDBForStatusTests(t)
	cl := newMongoDBStatusClient(t, server)
	current := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: commoncontrollerMongoDBName, Namespace: commoncontrollerMongoDBNamespace}, current))

	current.Spec.Members = 4
	current.UpdateStatus(status.PhasePending, status.ReplicaSetMembersOption{Members: 4})
	_, err := UpdateStatusOptimistic(ctx, cl, current, workflow.Pending("scaling"), log, status.ReplicaSetMembersOption{Members: 4})
	require.NoError(t, err)
	firstRV := current.ResourceVersion

	current.Spec.Members = 5
	current.UpdateStatus(status.PhasePending, status.ReplicaSetMembersOption{Members: 5})
	_, err = UpdateStatusOptimistic(ctx, cl, current, workflow.Pending("scaling"), log, status.ReplicaSetMembersOption{Members: 5})
	require.NoError(t, err)
	assert.NotEqual(t, firstRV, current.ResourceVersion)

	got := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: commoncontrollerMongoDBName, Namespace: commoncontrollerMongoDBNamespace}, got))
	assert.Equal(t, 5, got.Status.Members)
	assert.Equal(t, status.PhasePending, got.Status.Phase)
	assert.Equal(t, got.ResourceVersion, current.ResourceVersion)
}

func TestUpdateStatusOptimistic_ReturnsNonConflictError(t *testing.T) {
	ctx := context.Background()
	log := zap.NewNop().Sugar()
	server := newMongoDBForStatusTests(t)
	cl := newMongoDBStatusClient(t, server)
	current := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: commoncontrollerMongoDBName, Namespace: commoncontrollerMongoDBNamespace}, current))
	current.UpdateStatus(status.PhasePending, status.ReplicaSetMembersOption{Members: 4})

	failErr := fmt.Errorf("subresource update failed")
	fakeClient := fake.NewClientBuilder().
		WithScheme(func() *runtime.Scheme {
			scheme := runtime.NewScheme()
			require.NoError(t, v1.AddToScheme(scheme))
			return scheme
		}()).
		WithStatusSubresource(&mdbv1.MongoDB{}).
		WithObjects(server.DeepCopy()).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return failErr
		}}).
		Build()
	_, err := UpdateStatusOptimistic(ctx, kubernetesClient.NewClient(fakeClient), current, workflow.Pending("scaling"), log, status.ReplicaSetMembersOption{Members: 4})
	require.EqualError(t, err, failErr.Error())
}
