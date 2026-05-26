package haraft

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
)

func crPullerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(s))
	return s
}

func newPuller(t *testing.T, localClient client.Client) *CRPuller {
	t.Helper()
	return NewCRPuller("B", "ns", localClient, map[string]client.Client{"B": localClient}, nil)
}

func sampleCR(name string) *mdbmultiv1.MongoDBMultiCluster {
	return &mdbmultiv1.MongoDBMultiCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "ns",
		},
		Spec: mdbmultiv1.MongoDBMultiSpec{
			ClusterSpecList: mdb.ClusterSpecList{
				{ClusterName: "A", Members: 1},
				{ClusterName: "B", Members: 1},
			},
		},
	}
}

func TestCRPuller_Upsert_CreatesReplicaSpecOnly(t *testing.T) {
	scheme := crPullerTestScheme(t)
	local := fake.NewClientBuilder().WithScheme(scheme).Build()
	p := newPuller(t, local)

	cr := sampleCR("mdb")
	cr.ResourceVersion = "123"
	cr.UID = "should-be-dropped"
	cr.Generation = 7
	cr.Status.Phase = "Running"
	cr.Annotations = map[string]string{"user-anno": "x"}

	require.NoError(t, p.upsertLocalReplica(context.Background(), "A", cr))

	got := &mdbmultiv1.MongoDBMultiCluster{}
	require.NoError(t, local.Get(context.Background(), types.NamespacedName{Name: "mdb", Namespace: "ns"}, got))
	assert.Equal(t, cr.Spec.ClusterSpecList, got.Spec.ClusterSpecList)
	assert.Equal(t, "A", got.Annotations[ReplicaSourceAnnotation])
	assert.Equal(t, "x", got.Annotations["user-anno"])
	assert.Empty(t, got.Status.Phase, "status should not be replicated")
}

func TestCRPuller_Upsert_UpdatesExistingReplica(t *testing.T) {
	scheme := crPullerTestScheme(t)
	existing := sampleCR("mdb")
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	p := newPuller(t, local)

	updated := sampleCR("mdb")
	updated.Spec.ClusterSpecList[1].Members = 3

	require.NoError(t, p.upsertLocalReplica(context.Background(), "A", updated))

	got := &mdbmultiv1.MongoDBMultiCluster{}
	require.NoError(t, local.Get(context.Background(), types.NamespacedName{Name: "mdb", Namespace: "ns"}, got))
	assert.Equal(t, 3, got.Spec.ClusterSpecList[1].Members)
}

func TestCRPuller_Delete_RemovesReplica(t *testing.T) {
	scheme := crPullerTestScheme(t)
	existing := sampleCR("mdb")
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	p := newPuller(t, local)

	require.NoError(t, p.deleteLocalReplica(context.Background(), types.NamespacedName{Name: "mdb", Namespace: "ns"}))

	got := &mdbmultiv1.MongoDBMultiCluster{}
	err := local.Get(context.Background(), types.NamespacedName{Name: "mdb", Namespace: "ns"}, got)
	assert.True(t, apiErrors.IsNotFound(err))
}

func TestCRPuller_Delete_NoOpOnMissing(t *testing.T) {
	scheme := crPullerTestScheme(t)
	local := fake.NewClientBuilder().WithScheme(scheme).Build()
	p := newPuller(t, local)

	require.NoError(t, p.deleteLocalReplica(context.Background(), types.NamespacedName{Name: "missing", Namespace: "ns"}))
}

func TestCRPuller_Sync_MirrorsAllLeaderCRs(t *testing.T) {
	scheme := crPullerTestScheme(t)

	cr1 := sampleCR("one")
	cr2 := sampleCR("two")

	leader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr1, cr2).Build()
	local := fake.NewClientBuilder().WithScheme(scheme).Build()

	p := NewCRPuller("B", "ns", local, map[string]client.Client{"A": leader, "B": local}, nil)

	require.NoError(t, p.syncOnce(context.Background(), "A"))

	list := &mdbmultiv1.MongoDBMultiClusterList{}
	require.NoError(t, local.List(context.Background(), list))
	assert.Len(t, list.Items, 2)
}

func TestCRPuller_Sync_DeletesLocalReplicasNoLongerOnLeader(t *testing.T) {
	scheme := crPullerTestScheme(t)

	keep := sampleCR("keep")

	stale := sampleCR("stale")
	stale.Annotations = map[string]string{ReplicaSourceAnnotation: "A"}

	leader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(keep).Build()
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()

	p := NewCRPuller("B", "ns", local, map[string]client.Client{"A": leader, "B": local}, nil)

	require.NoError(t, p.syncOnce(context.Background(), "A"))

	got := &mdbmultiv1.MongoDBMultiCluster{}
	err := local.Get(context.Background(), types.NamespacedName{Name: "stale", Namespace: "ns"}, got)
	assert.True(t, apiErrors.IsNotFound(err), "stale replica should be deleted")
	require.NoError(t, local.Get(context.Background(), types.NamespacedName{Name: "keep", Namespace: "ns"}, got))
}

func TestCRPuller_Start_NoOpWhenLocalIsLeader(t *testing.T) {
	scheme := crPullerTestScheme(t)
	local := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Test the early return for leaderClusterName == localClusterName.
	p := NewCRPuller("B", "ns", local, map[string]client.Client{"B": local}, nil)
	p.Start(context.Background(), "B")
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Nil(t, p.cancelWatch, "Start with leader==local should not start a goroutine")
	assert.Empty(t, p.currentLeader)
}

func TestCRPuller_Start_Idempotent(t *testing.T) {
	scheme := crPullerTestScheme(t)
	leader := fake.NewClientBuilder().WithScheme(scheme).Build()
	local := fake.NewClientBuilder().WithScheme(scheme).Build()

	p := NewCRPuller("B", "ns", local, map[string]client.Client{"A": leader, "B": local}, nil)
	p.Start(context.Background(), "A")
	// Allow the goroutine to enter run() and increment its counter.
	require.Eventually(t, func() bool { return p.runCount() == 1 }, time.Second, 10*time.Millisecond)

	p.Start(context.Background(), "A") // should be a no-op
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), p.runCount(), "Start with same leader must not spawn another goroutine")

	p.Stop()
}

func TestCRPuller_Restart_SwitchesLeader(t *testing.T) {
	scheme := crPullerTestScheme(t)
	leaderA := fake.NewClientBuilder().WithScheme(scheme).Build()
	leaderC := fake.NewClientBuilder().WithScheme(scheme).Build()
	local := fake.NewClientBuilder().WithScheme(scheme).Build()

	p := NewCRPuller("B", "ns", local,
		map[string]client.Client{"A": leaderA, "B": local, "C": leaderC}, nil)

	p.Start(context.Background(), "A")
	p.mu.Lock()
	assert.Equal(t, "A", p.currentLeader)
	p.mu.Unlock()

	p.Restart(context.Background(), "C")
	p.mu.Lock()
	assert.Equal(t, "C", p.currentLeader)
	p.mu.Unlock()

	p.Stop()
	p.mu.Lock()
	assert.Empty(t, p.currentLeader)
	assert.Nil(t, p.cancelWatch)
	p.mu.Unlock()
}
