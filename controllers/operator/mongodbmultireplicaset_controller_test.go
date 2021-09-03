package operator

import (
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"

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

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

var (
	clusters = []string{"api.kube.com", "api2.kube.com", "api3.kube.com"}
)

type multiReplicaSetBuilder struct {
	*mdbmulti.MongoDBMulti
}

func DefaultMultiReplicaSetBuilder() *multiReplicaSetBuilder {
	spec := mdbmulti.MongoDBMultiSpec{
		Version:                 "5.0.0",
		DuplicateServiceObjects: util.BooleanRef(false),
		Persistent:              util.BooleanRef(false),
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

func (m *multiReplicaSetBuilder) SetSecurity(s *mdbv1.Security) *multiReplicaSetBuilder {
	m.Spec.Security = s
	return m
}

func checkMultiReconcileSuccessful(t *testing.T, reconciler reconcile.Reconciler, m *mdbmulti.MongoDBMulti, client *mock.MockedClient) {
	result, e := reconciler.Reconcile(context.TODO(), requestFromObject(m))
	assert.NoError(t, e)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestCreateMultiReplicaSet(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client)

}

func TestReconcileFails_WhenProjectConfig_IsNotFound(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)

	err := client.DeleteConfigMap(kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName))
	assert.NoError(t, err)

	result, err := reconciler.Reconcile(context.TODO(), requestFromObject(mrs))
	assert.Nil(t, err)
	assert.True(t, result.RequeueAfter > 0)
}

func TestServiceCreation_WithoutDuplicates(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client)

	clusterSpecs := mrs.GetOrderedClusterSpecList()
	for clusterNum, item := range clusterSpecs {
		c := memberClusterMap[item.ClusterName]
		for podNum := 0; podNum < item.Members; podNum++ {
			svc := getService(*mrs, clusterNum, podNum)

			testSvc := corev1.Service{}
			err := c.GetClient().Get(context.TODO(), kube.ObjectKey(svc.Namespace, svc.Name), &testSvc)
			assert.NoError(t, err)

			// ensure that all other clusters do not have this service
			for _, otherItem := range clusterSpecs {
				if item.ClusterName == otherItem.ClusterName {
					continue
				}
				otherCluster := memberClusterMap[otherItem.ClusterName]
				err = otherCluster.GetClient().Get(context.TODO(), kube.ObjectKey(svc.Namespace, svc.Name), &corev1.Service{})
				assert.Error(t, err)
			}
		}
	}
}

func TestServiceCreation_WithDuplicates(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.DuplicateServiceObjects = util.BooleanRef(true)

	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client)

	clusterSpecs := mrs.GetOrderedClusterSpecList()
	for clusterNum, item := range clusterSpecs {
		for podNum := 0; podNum < item.Members; podNum++ {
			svc := getService(*mrs, clusterNum, podNum)

			// ensure that all clusters have all services
			for _, otherItem := range clusterSpecs {
				otherCluster := memberClusterMap[otherItem.ClusterName]
				err := otherCluster.GetClient().Get(context.TODO(), kube.ObjectKey(svc.Namespace, svc.Name), &corev1.Service{})
				assert.NoError(t, err)
			}
		}
	}
}

func TestResourceDeletion(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	reconciler, client, memberClients := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client)

	t.Run("Resources are created", func(t *testing.T) {
		for clusterNum, item := range mrs.GetOrderedClusterSpecList() {
			c := memberClients[item.ClusterName]
			t.Run("Stateful Set in each member cluster has been created", func(t *testing.T) {
				sts := appsv1.StatefulSet{}
				err := c.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(clusterNum)), &sts)
				assert.NoError(t, err)
			})

			t.Run("Services in each member cluster have been created", func(t *testing.T) {
				svcList := corev1.ServiceList{}
				err := c.GetClient().List(context.TODO(), &svcList)
				assert.NoError(t, err)
				assert.Len(t, svcList.Items, item.Members)
			})

			t.Run("Configmaps in each member cluster have been created", func(t *testing.T) {
				configMapList := corev1.ConfigMapList{}
				err := c.GetClient().List(context.TODO(), &configMapList)
				assert.NoError(t, err)
				assert.Len(t, configMapList.Items, 1)
			})
			t.Run("Secrets in each member cluster have been created", func(t *testing.T) {
				secretList := corev1.SecretList{}
				err := c.GetClient().List(context.TODO(), &secretList)
				assert.NoError(t, err)
				assert.Len(t, secretList.Items, 1)
			})
		}
	})

	err := reconciler.deleteManagedResources(*mrs, zap.S())
	assert.NoError(t, err)

	for clusterNum, item := range mrs.GetOrderedClusterSpecList() {
		c := memberClients[item.ClusterName]
		t.Run("Stateful Set in each member cluster has been removed", func(t *testing.T) {
			sts := appsv1.StatefulSet{}
			err := c.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(clusterNum)), &sts)
			assert.Error(t, err)
		})

		t.Run("Services in each member cluster have been removed", func(t *testing.T) {
			svcList := corev1.ServiceList{}
			err := c.GetClient().List(context.TODO(), &svcList)
			assert.NoError(t, err)
			assert.Len(t, svcList.Items, 0)
		})

		t.Run("Configmaps in each member cluster have been removed", func(t *testing.T) {
			configMapList := corev1.ConfigMapList{}
			err := c.GetClient().List(context.TODO(), &configMapList)
			assert.NoError(t, err)
			assert.Len(t, configMapList.Items, 0)
		})

		t.Run("Secrets in each member cluster have been removed", func(t *testing.T) {
			secretList := corev1.SecretList{}
			err := c.GetClient().List(context.TODO(), &secretList)
			assert.NoError(t, err)
			assert.Len(t, secretList.Items, 0)
		})
	}

	t.Run("Ops Manager state has been cleaned", func(t *testing.T) {
		processes := om.CurrMockedConnection.GetProcesses()
		assert.Len(t, processes, 0)

		ac, err := om.CurrMockedConnection.ReadAutomationConfig()
		assert.NoError(t, err)

		assert.Empty(t, ac.Auth.AutoAuthMechanisms)
		assert.Empty(t, ac.Auth.DeploymentAuthMechanisms)
		assert.False(t, ac.Auth.IsEnabled())
	})

}

func TestGroupSecret_IsCopied_ToEveryMemberCluster(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client)

	for _, clusterName := range clusters {
		t.Run(fmt.Sprintf("Secret exists in cluster %s", clusterName), func(t *testing.T) {
			c, ok := memberClusterMap[clusterName]
			assert.True(t, ok)

			s := corev1.Secret{}
			err := c.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, fmt.Sprintf("%s-group-secret", om.CurrMockedConnection.GroupID())), &s)
			assert.NoError(t, err)
		})
	}
}

func TestAuthentication_IsEnabledInOM_WhenConfiguredInCR(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().SetSecurity(&mdbv1.Security{
		Authentication: &mdbv1.Authentication{Enabled: true, Modes: []string{"SCRAM"}},
	}).Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)

	t.Run("Reconciliation is successful when configuring scram", func(t *testing.T) {
		checkMultiReconcileSuccessful(t, reconciler, mrs, client)
	})

	t.Run("Automation Config has been updated correctly", func(t *testing.T) {
		ac, err := om.CurrMockedConnection.ReadAutomationConfig()
		assert.NoError(t, err)

		assert.Contains(t, ac.Auth.AutoAuthMechanism, "SCRAM-SHA-256")
		assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
		assert.True(t, ac.Auth.IsEnabled())
		assert.NotEmpty(t, ac.Auth.AutoPwd)
		assert.NotEmpty(t, ac.Auth.Key)
		assert.NotEmpty(t, ac.Auth.KeyFile)
		assert.NotEmpty(t, ac.Auth.KeyFileWindows)
		assert.NotEmpty(t, ac.Auth.AutoUser)
	})
}

func TestTls_IsEnabledInOM_WhenConfiguredInCR(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().SetSecurity(&mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true, CA: "some-ca", SecretRef: mdbv1.TLSSecretRef{Prefix: "some-prefix"}},
	}).Build()

	reconciler, client, memberClients := defaultMultiReplicaSetReconciler(mrs, t)
	createMultiClusterReplicaSetTLSData(memberClients, mrs)

	t.Run("Reconciliation is successful when configuring tls", func(t *testing.T) {
		checkMultiReconcileSuccessful(t, reconciler, mrs, client)
	})

	t.Run("Automation Config has been updated correctly", func(t *testing.T) {
		processes := om.CurrMockedConnection.GetProcesses()
		for _, p := range processes {
			assert.True(t, p.IsTLSEnabled())
			assert.Equal(t, "requireTLS", p.TLSConfig()["mode"])
		}
	})
}

func defaultMultiReplicaSetReconciler(m *mdbmulti.MongoDBMulti, t *testing.T) (*ReconcileMongoDbMultiReplicaSet, *mock.MockedClient, map[string]cluster.Cluster) {
	return multiReplicaSetReconcilerWithConnection(m, om.NewEmptyMockedOmConnection, t)
}

func multiReplicaSetReconcilerWithConnection(m *mdbmulti.MongoDBMulti,
	connectionFunc func(ctx *om.OMContext) om.Connection, t *testing.T) (*ReconcileMongoDbMultiReplicaSet, *mock.MockedClient, map[string]cluster.Cluster) {
	manager := mock.NewManager(m)
	manager.Client.AddDefaultMdbConfigResources()

	memberClusterMap := getFakeMultiClusterMap()
	return newMultiClusterReplicaSetReconciler(manager, connectionFunc, memberClusterMap), manager.Client, memberClusterMap
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
