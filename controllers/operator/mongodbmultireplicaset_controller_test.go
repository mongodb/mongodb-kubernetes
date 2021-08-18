package operator

import (
	"context"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	clusters = []string{"api.kube.com", "api2.kube.com", "api3.kube.com"}
)

type multiReplicaSetBuilder struct {
	*mdbmulti.MongoDBMulti
}

func DefaultMultiReplicaSetBuilder() *multiReplicaSetBuilder {
	spec := mdbmulti.MongoDBMultiSpec{
		Version:    "5.0.0",
		Persistent: util.BooleanRef(false),
		ConnectionSpec: mdbv1.ConnectionSpec{
			OpsManagerConfig: &mdbv1.PrivateCloudConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{
					Name: mock.TestProjectConfigMapName,
				},
			},
			Credentials: mock.TestCredentialsSecretName,
		},
		ResourceType: mdbv1.ReplicaSet,
		Security: &mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{},
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
			Roles: []mdbv1.MongoDbRole{},
		},
		ClusterSpecList: mdbmulti.ClusterSpecList{
			ClusterSpecs: []mdbmulti.ClusterSpecItem{
				{
					ClusterName: clusters[0],
					Members:     3,
				},
				{
					ClusterName: clusters[1],
					Members:     2,
				},
				{
					ClusterName: clusters[2],
					Members:     5,
				},
			},
		},
	}

	mrs := &mdbmulti.MongoDBMulti{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: mock.TestNamespace}}
	return &multiReplicaSetBuilder{mrs}
}

func checkMultiReconcileSuccessful(t *testing.T, reconciler reconcile.Reconciler, m *mdbmulti.MongoDBMulti, client *mock.MockedClient) {
	result, e := reconciler.Reconcile(context.TODO(), requestFromObject(m))
	assert.NoError(t, e)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestCreateMultiReplicaSet(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()

	reconciler, client := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client)

}

func defaultMultiReplicaSetReconciler(m *mdbmulti.MongoDBMulti, t *testing.T) (*ReconcileMongoDbMultiReplicaSet, *mock.MockedClient) {
	return multiReplicaSetReconcilerWithConnection(m, om.NewEmptyMockedOmConnection, t)
}

func multiReplicaSetReconcilerWithConnection(m *mdbmulti.MongoDBMulti,
	connectionFunc func(ctx *om.OMContext) om.Connection, t *testing.T) (*ReconcileMongoDbMultiReplicaSet, *mock.MockedClient) {
	manager := mock.NewManager(m)
	manager.Client.AddDefaultMdbConfigResources()

	memberClusterMap := getFakeMultiClusterMap()
	return newMultiClusterReplicaSetReconciler(manager, connectionFunc, memberClusterMap), manager.Client
}

func (m *multiReplicaSetBuilder) Build() *mdbmulti.MongoDBMulti {
	// initialize defaults
	return m.MongoDBMulti.DeepCopy()
}

func getFakeMultiClusterMap() map[string]cluster.Cluster {
	clusterMap := make(map[string]cluster.Cluster)

	for _, e := range clusters {
		memberClient := mock.NewClient()
		memberCluster := multicluster.New(memberClient)
		clusterMap[e] = memberCluster
	}
	return clusterMap
}
