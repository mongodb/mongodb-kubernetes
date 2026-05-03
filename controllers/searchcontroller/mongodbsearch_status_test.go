package searchcontroller

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
)

func TestBuildPerClusterStatusItems_Legacy_NoClusters(t *testing.T) {
	mdb := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec:       searchv1.MongoDBSearchSpec{},
	}
	items := buildPerClusterStatusItems(mdb, workflow.OK())
	assert.Empty(t, items, "legacy single-cluster reconcile must produce no per-cluster items")
}

func TestBuildPerClusterStatusItems_LegacyEmptySlice_NoClusters(t *testing.T) {
	emptyClusters := []searchv1.ClusterSpec{}
	mdb := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &emptyClusters,
		},
	}
	items := buildPerClusterStatusItems(mdb, workflow.OK())
	assert.Empty(t, items, "empty spec.clusters slice must also be treated as legacy")
}

func TestBuildPerClusterStatusItems_MultiCluster_OneItemPerSpec(t *testing.T) {
	clusters := []searchv1.ClusterSpec{
		{ClusterName: "us-east-k8s"},
		{ClusterName: "eu-west-k8s"},
	}
	mdb := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &clusters,
		},
	}
	items := buildPerClusterStatusItems(mdb, workflow.OK())
	require.Len(t, items, 2)
	assert.Equal(t, "us-east-k8s", items[0].ClusterName)
	assert.Equal(t, "eu-west-k8s", items[1].ClusterName)
	assert.Equal(t, status.PhaseRunning, items[0].Phase)
	assert.Equal(t, status.PhaseRunning, items[1].Phase)
}

func TestBuildPerClusterStatusItems_FailedReconcileFlowsToEachCluster(t *testing.T) {
	clusters := []searchv1.ClusterSpec{
		{ClusterName: "us-east-k8s"},
		{ClusterName: "eu-west-k8s"},
	}
	mdb := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &clusters,
		},
	}
	items := buildPerClusterStatusItems(mdb, workflow.Failed(fmt.Errorf("boom")))
	require.Len(t, items, 2)
	assert.Equal(t, status.PhaseFailed, items[0].Phase)
	assert.Equal(t, status.PhaseFailed, items[1].Phase)
	assert.Contains(t, items[0].Message, "boom")
	assert.Contains(t, items[1].Message, "boom")
}

func TestBuildPerClusterStatusItems_PendingReconcileFlows(t *testing.T) {
	clusters := []searchv1.ClusterSpec{{ClusterName: "us-east-k8s"}}
	mdb := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &clusters,
		},
	}
	items := buildPerClusterStatusItems(mdb, workflow.Pending("waiting for LB"))
	require.Len(t, items, 1)
	assert.Equal(t, status.PhasePending, items[0].Phase)
	assert.Contains(t, items[0].Message, "waiting for LB")
}
