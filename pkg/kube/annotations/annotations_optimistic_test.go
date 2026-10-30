package annotations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
)

func newMongoDBForAnnotationTests(t *testing.T) *mdbv1.MongoDB {
	t.Helper()
	mdb := mdbv1.NewReplicaSetBuilder().SetName("mdb").SetNamespace("ns").SetMembers(3).Build()
	mdb.ResourceVersion = "2"
	mdb.UpdateStatus(status.PhaseRunning, status.ReplicaSetMembersOption{Members: 3})
	return mdb
}

func newMongoDBAnnotationClient(t *testing.T, obj *mdbv1.MongoDB) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&mdbv1.MongoDB{}).WithObjects(obj).Build()
}

func TestSetAnnotationsOptimistic_SucceedsAndRefreshesObject(t *testing.T) {
	ctx := context.Background()
	obj := newMongoDBForAnnotationTests(t)
	cl := newMongoDBAnnotationClient(t, obj)

	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: "mdb", Namespace: "ns"}, obj))
	require.NoError(t, annotations.SetAnnotationsOptimistic(ctx, obj, map[string]string{annotations.LastAppliedMongoDBVersion: "8.0.0"}, cl))

	assert.Equal(t, "8.0.0", obj.Annotations[annotations.LastAppliedMongoDBVersion])
	assert.NotEqual(t, "2", obj.ResourceVersion)

	obj.UpdateStatus(status.PhaseRunning, status.ReplicaSetMembersOption{Members: 3})
	require.NoError(t, cl.Status().Update(ctx, obj))

	got := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: "mdb", Namespace: "ns"}, got))
	assert.Equal(t, "8.0.0", got.Annotations[annotations.LastAppliedMongoDBVersion])
	assert.Equal(t, status.PhaseRunning, got.Status.Phase)
	assert.Equal(t, 3, got.Status.Members)
}

func TestSetAnnotationsOptimistic_ConflictLeavesServerAndObjectUntouched(t *testing.T) {
	ctx := context.Background()
	obj := newMongoDBForAnnotationTests(t)
	cl := newMongoDBAnnotationClient(t, obj)

	stale := obj.DeepCopy()
	stale.ResourceVersion = "1"
	stale.Annotations = map[string]string{"keep": "me"}

	err := annotations.SetAnnotationsOptimistic(ctx, stale, map[string]string{annotations.LastAppliedMongoDBVersion: "8.0.0"}, cl)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err))
	assert.Equal(t, map[string]string{"keep": "me"}, stale.Annotations)
	assert.Equal(t, "1", stale.ResourceVersion)

	got := &mdbv1.MongoDB{}
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: "mdb", Namespace: "ns"}, got))
	assert.Nil(t, got.Annotations)
	assert.Equal(t, status.PhaseRunning, got.Status.Phase)
	assert.Equal(t, 3, got.Status.Members)
	assert.Equal(t, "2", got.ResourceVersion)
}

func TestSetAnnotationsOptimistic_PreservesInMemoryStatusEdits(t *testing.T) {
	ctx := context.Background()
	obj := newMongoDBForAnnotationTests(t)
	cl := newMongoDBAnnotationClient(t, obj)

	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: "mdb", Namespace: "ns"}, obj))
	obj.Status.Warnings = []status.Warning{"preexisting"}

	require.NoError(t, annotations.SetAnnotationsOptimistic(ctx, obj, map[string]string{annotations.LastAppliedMongoDBVersion: "8.0.0"}, cl))
	assert.Equal(t, []status.Warning{"preexisting"}, obj.Status.Warnings)
	assert.Equal(t, "8.0.0", obj.Annotations[annotations.LastAppliedMongoDBVersion])
}

func TestSetAnnotations_LeavesResourceVersionStale(t *testing.T) {
	ctx := context.Background()
	obj := newMongoDBForAnnotationTests(t)
	cl := newMongoDBAnnotationClient(t, obj)

	require.NoError(t, annotations.SetAnnotations(ctx, obj, map[string]string{annotations.LastAppliedMongoDBVersion: "8.0.0"}, cl))

	obj.UpdateStatus(status.PhaseRunning, status.ReplicaSetMembersOption{Members: 3})
	err := cl.Status().Update(ctx, obj)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err))
}
