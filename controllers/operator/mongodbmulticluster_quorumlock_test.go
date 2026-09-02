package operator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

var testDeployment = types.NamespacedName{Namespace: "testns", Name: "temple"}

// quorumLockForTest wires a lock for clusters[0] over one fake API server per cluster, with a
// zero candidacy delay and a controllable clock.
func quorumLockForTest(t *testing.T, clientsMap map[string]kubernetesClient.Client) (*quorumLock, *time.Time) {
	if clientsMap == nil {
		clientsMap = map[string]kubernetesClient.Client{}
		for _, cluster := range clusters {
			clientsMap[cluster] = kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
		}
	}
	lock := newQuorumLock(testDeployment, clusters[0], clusters, testLeaseDuration, &apiServerTransport{clients: clientsMap}, zap.S())
	lock.core.randomDelay = func() time.Duration { return 0 }
	now := time.Now()
	lock.clock = func() time.Time { return now }
	return lock, &now
}

func seedLease(t *testing.T, c kubernetesClient.Client, holder string, term int64) {
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: testDeployment.Name, Namespace: testDeployment.Namespace}}
	duration := int32(30)
	renewTime := metav1.NewMicroTime(time.Now())
	lease.Spec.HolderIdentity = &holder
	lease.Spec.LeaseDurationSeconds = &duration
	lease.Spec.RenewTime = &renewTime
	setLeaseTerm(lease, term)
	require.NoError(t, c.Create(context.Background(), lease))
}

func TestQuorumLockGetAggregationTrichotomy(t *testing.T) {
	ctx := context.Background()
	clientsMap := map[string]kubernetesClient.Client{
		clusters[0]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build()),
		clusters[1]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build()),
		clusters[2]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return xerrors.New("cluster unreachable")
			},
		}).Build()),
	}
	seedLease(t, clientsMap[clusters[0]], "somebody", 2)
	lock, _ := quorumLockForTest(t, clientsMap)

	record, raw, err := lock.Get(ctx)
	require.NoError(t, err, "Get never fails as a whole; partial visibility is a planning input")

	assert.Empty(t, record.HolderIdentity, "one lease of three is never a majority holder")
	assert.Equal(t, 30, record.LeaseDurationSeconds)
	assert.Contains(t, string(raw), clusters[2]+"=unobserved", "unreachable is not an observation")
	assert.NotNil(t, lock.core.records[clusters[1]], "NotFound IS an observation (authoritative absence)")
	assert.False(t, lock.core.records[clusters[1]].content.Exists)
	assert.Nil(t, lock.core.records[clusters[2]])
}

func TestQuorumLockCompositeStability(t *testing.T) {
	ctx := context.Background()
	clientsMap := map[string]kubernetesClient.Client{}
	for _, cluster := range clusters {
		clientsMap[cluster] = kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	}
	seedLease(t, clientsMap[clusters[0]], "somebody", 5)
	seedLease(t, clientsMap[clusters[1]], "somebody", 5)
	lock, _ := quorumLockForTest(t, clientsMap)

	record, raw1, err := lock.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "somebody", record.HolderIdentity, "two of three under one term is a majority holder")

	_, raw2, err := lock.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, raw1, raw2, "raw bytes are stable while no lease content changes — merely looking must not refresh anyone's expiry clock")

	// one holder heartbeat: only the renewTime moves
	lease := &coordinationv1.Lease{}
	require.NoError(t, clientsMap[clusters[0]].Get(ctx, testDeployment, lease))
	renewTime := metav1.NewMicroTime(time.Now().Add(time.Minute))
	lease.Spec.RenewTime = &renewTime
	require.NoError(t, clientsMap[clusters[0]].Update(ctx, lease))

	_, raw3, err := lock.Get(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, raw2, raw3, "a heartbeat is a content change and resets the composite expiry")
}

func TestQuorumLockAcquireRenewPartialFailure(t *testing.T) {
	ctx := context.Background()
	lock, now := quorumLockForTest(t, nil)
	clientsMap := lock.transport.(*apiServerTransport).clients
	self := resourceLockRecord(clusters[0])

	// turn 1: Get + Update arms the randomized delay — no CAS yet (a zero delay would proceed
	// this same turn)
	lock.core.randomDelay = func() time.Duration { return time.Second }
	_, _, err := lock.Get(ctx)
	require.NoError(t, err)
	assert.ErrorContains(t, lock.Update(ctx, self), "no candidacy this turn")
	for _, cluster := range clusters {
		getErr := clientsMap[cluster].Get(ctx, testDeployment, &coordinationv1.Lease{})
		assert.Error(t, getErr, "the delaying turn wrote nothing on %s", cluster)
	}

	// turn 2: past the delay, the fan-out creates all three leases and wins
	*now = now.Add(2 * time.Second)
	_, _, err = lock.Get(ctx)
	require.NoError(t, err)
	require.NoError(t, lock.Update(ctx, self))

	_, isLeader := lock.Current()
	assert.False(t, isLeader, "a dirty acquire serves the hold-off before Current turns true")
	*now = now.Add(testLeaseDuration)
	term, isLeader := lock.Current()
	assert.True(t, isLeader)
	assert.Equal(t, int64(1), term)

	// a usurper seizes two leases between our renewals (client-go's fast path skips Get, so the
	// stale resourceVersions are only corrected by the post-failure read-through)
	for _, cluster := range clusters[1:] {
		lease := &coordinationv1.Lease{}
		require.NoError(t, clientsMap[cluster].Get(ctx, testDeployment, lease))
		usurper := clusters[1]
		lease.Spec.HolderIdentity = &usurper
		setLeaseTerm(lease, 9)
		require.NoError(t, clientsMap[cluster].Update(ctx, lease))
	}

	assert.ErrorContains(t, lock.Update(ctx, self), "failed renewing the majority")
	_, isLeader = lock.Current()
	assert.False(t, isLeader, "1 of 3 renewed: guarded work stops immediately")
	assert.Equal(t, phaseFollower, lock.core.phase, "the read-through observed the usurper's majority")
}

func TestQuorumLockReleaseShape(t *testing.T) {
	ctx := context.Background()
	lock, _ := quorumLockForTest(t, nil)
	clientsMap := lock.transport.(*apiServerTransport).clients

	_, _, err := lock.Get(ctx)
	require.NoError(t, err)
	_ = lock.Update(ctx, resourceLockRecord(clusters[0]))
	_, _, err = lock.Get(ctx)
	require.NoError(t, err)
	require.NoError(t, lock.Update(ctx, resourceLockRecord(clusters[0])))

	// client-go's ReleaseOnCancel shape: empty holder — recognized as the release command
	require.NoError(t, lock.Update(ctx, resourceLockRecord("")))

	for _, cluster := range clusters {
		lease := &coordinationv1.Lease{}
		require.NoError(t, clientsMap[cluster].Get(ctx, testDeployment, lease))
		assert.Equal(t, "", *lease.Spec.HolderIdentity)
		assert.Equal(t, int32(1), *lease.Spec.LeaseDurationSeconds, "the release shape: empty holder, 1s duration")
		term, ok := leaseTerm(lease)
		assert.True(t, ok)
		assert.Equal(t, int64(1), term, "the term annotation survives the release for future candidacies")
	}
	_, isLeader := lock.Current()
	assert.False(t, isLeader)
}

func resourceLockRecord(holder string) resourcelock.LeaderElectionRecord {
	return resourcelock.LeaderElectionRecord{HolderIdentity: holder}
}
