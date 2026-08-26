package operator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coordinationv1 "k8s.io/api/coordination/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// TestQuorumElectorLifecycle drives one entry end to end over fake API servers with compressed
// durations: contention to leadership (hold-off included), wake-up events, electorate updates
// without a new election, and retirement writing the clean-release shape.
func TestQuorumElectorLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientsMap := map[string]kubernetesClient.Client{}
	for _, cluster := range clusters {
		clientsMap[cluster] = kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	}
	elector := newQuorumElector(nil, &apiServerTransport{clients: clientsMap}, clusters[0], 300*time.Millisecond)

	_, isLeader := elector.Current(kube.ObjectKey("testns", "unknown"))
	assert.False(t, isLeader, "an unknown deployment is never leader")
	elector.ObserveTermFloor(kube.ObjectKey("testns", "unknown"), 5) // must not panic

	elector.upsertDeployment(ctx, testDeployment, clusters)
	require.Eventually(t, func() bool {
		_, isLeader := elector.Current(testDeployment)
		return isLeader
	}, 10*time.Second, 20*time.Millisecond, "the single contender acquires and serves the hold-off")
	term, _ := elector.Current(testDeployment)
	assert.Equal(t, int64(1), term)

	select {
	case wakeUp := <-elector.Events():
		assert.Equal(t, testDeployment, kube.ObjectKeyFromApiObject(wakeUp.Object))
	default:
		t.Fatal("acquiring leadership must push a wake-up event")
	}

	// an electorate change follows the CR in place: same entry, no second election
	elector.upsertDeployment(ctx, testDeployment, clusters[:2])
	elector.mu.Lock()
	assert.Len(t, elector.entries, 1)
	elector.mu.Unlock()

	// retirement cancels the entry; ReleaseOnCancel writes the clean-release shape everywhere
	elector.removeDeployment(testDeployment)
	_, isLeader = elector.Current(testDeployment)
	assert.False(t, isLeader)
	for _, cluster := range clusters {
		lease := &coordinationv1.Lease{}
		require.NoError(t, clientsMap[cluster].Get(ctx, testDeployment, lease))
		assert.Equal(t, "", *lease.Spec.HolderIdentity, "cluster %s holds the release shape", cluster)
		assert.Equal(t, int32(1), *lease.Spec.LeaseDurationSeconds)
	}
}
