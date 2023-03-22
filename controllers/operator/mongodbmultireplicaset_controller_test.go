package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"k8s.io/utils/pointer"

	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/failedcluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/memberwatch"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/google/uuid"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

var (
	clusters = []string{"api1.kube.com", "api2.kube.com", "api3.kube.com"}
)

func checkMultiReconcileSuccessful(t *testing.T, reconciler reconcile.Reconciler, m *mdbmulti.MongoDBMultiCluster, client *mock.MockedClient, shouldRequeue bool) {
	result, e := reconciler.Reconcile(context.TODO(), requestFromObject(m))
	assert.NoError(t, e)
	if shouldRequeue {
		assert.True(t, result.Requeue || result.RequeueAfter > 0)
	} else {
		assert.Equal(t, reconcile.Result{}, result)
	}

	// fetch the last updates as the reconciliation loop can update the mdb resource.
	err := client.Get(context.TODO(), kube.ObjectKey(m.Namespace, m.Name), m)
	assert.NoError(t, err)
}

func TestCreateMultiReplicaSet(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

}

func TestReconcileFails_WhenProjectConfig_IsNotFound(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)

	err := client.DeleteConfigMap(kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName))
	assert.NoError(t, err)

	result, err := reconciler.Reconcile(context.TODO(), requestFromObject(mrs))
	assert.Nil(t, err)
	assert.True(t, result.RequeueAfter > 0)
}

func TestServiceCreation_WithExternalName(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		SetExternalAccess(
			mdbv1.ExternalAccessConfiguration{
				ExternalDomain: pointer.String("cluster-%d.testing"),
			}, "cluster-%d.testing").
		Build()
	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	clusterSpecs := clusterSpecList
	for _, item := range clusterSpecs {
		c := memberClusterMap[item.ClusterName]
		for podNum := 0; podNum < item.Members; podNum++ {
			externalService := getExternalService(mrs, item.ClusterName, podNum)

			err = c.GetClient().Get(context.TODO(), kube.ObjectKey(externalService.Namespace, externalService.Name), &corev1.Service{})
			assert.NoError(t, err)

			// ensure that all other clusters do not have this service
			for _, otherItem := range clusterSpecs {
				if item.ClusterName == otherItem.ClusterName {
					continue
				}
				otherCluster := memberClusterMap[otherItem.ClusterName]
				err = otherCluster.GetClient().Get(context.TODO(), kube.ObjectKey(externalService.Namespace, externalService.Name), &corev1.Service{})
				assert.Error(t, err)
			}
		}
	}
}

func TestServiceCreation_WithoutDuplicates(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		Build()
	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	clusterSpecs := clusterSpecList
	for _, item := range clusterSpecs {
		c := memberClusterMap[item.ClusterName]
		for podNum := 0; podNum < item.Members; podNum++ {
			svc := getService(mrs, item.ClusterName, podNum)

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
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		Build()
	mrs.Spec.DuplicateServiceObjects = util.BooleanRef(true)

	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	for _, item := range clusterSpecs {
		for podNum := 0; podNum < item.Members; podNum++ {
			svc := getService(mrs, item.ClusterName, podNum)

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
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, memberClients := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	t.Run("Resources are created", func(t *testing.T) {
		clusterSpecs, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}
		for _, item := range clusterSpecs {
			c := memberClients[item.ClusterName]
			t.Run("Stateful Set in each member cluster has been created", func(t *testing.T) {
				sts := appsv1.StatefulSet{}
				err := c.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
				assert.NoError(t, err)
			})

			t.Run("Services in each member cluster have been created", func(t *testing.T) {
				svcList := corev1.ServiceList{}
				err := c.GetClient().List(context.TODO(), &svcList)
				assert.NoError(t, err)
				assert.Len(t, svcList.Items, item.Members+1)
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

	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	for _, item := range clusterSpecs {
		c := memberClients[item.ClusterName]
		t.Run("Stateful Set in each member cluster has been removed", func(t *testing.T) {
			sts := appsv1.StatefulSet{}
			err := c.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
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
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	for _, clusterName := range clusters {
		t.Run(fmt.Sprintf("Secret exists in cluster %s", clusterName), func(t *testing.T) {
			c, ok := memberClusterMap[clusterName]
			assert.True(t, ok)

			s := corev1.Secret{}
			err := c.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, fmt.Sprintf("%s-group-secret", om.CurrMockedConnection.GroupID())), &s)
			assert.NoError(t, err)
			assert.Equal(t, mongoDBMultiLabels(mrs.Name, mrs.Namespace), s.Labels)
		})
	}
}

func TestAuthentication_IsEnabledInOM_WhenConfiguredInCR(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetSecurity(&mdbv1.Security{
		Authentication: &mdbv1.Authentication{Enabled: true, Modes: []string{"SCRAM"}},
	}).SetClusterSpecList(clusters).Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)

	t.Run("Reconciliation is successful when configuring scram", func(t *testing.T) {
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
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
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).SetSecurity(&mdbv1.Security{
		TLSConfig:                 &mdbv1.TLSConfig{Enabled: true, CA: "some-ca"},
		CertificatesSecretsPrefix: "some-prefix",
	}).Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
	createMultiClusterReplicaSetTLSData(client, mrs, "some-ca")

	t.Run("Reconciliation is successful when configuring tls", func(t *testing.T) {
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
	})

	t.Run("Automation Config has been updated correctly", func(t *testing.T) {
		processes := om.CurrMockedConnection.GetProcesses()
		for _, p := range processes {
			assert.True(t, p.IsTLSEnabled())
			assert.Equal(t, "requireTLS", p.TLSConfig()["mode"])
		}
	})
}

func TestSpecIsSavedAsAnnotation_WhenReconciliationIsSuccessful(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	//fetch the resource after reconciliation
	err := client.Get(context.TODO(), kube.ObjectKey(mrs.Namespace, mrs.Name), mrs)
	assert.NoError(t, err)

	expected := mrs.Spec
	actual, err := mrs.ReadLastAchievedSpec()
	assert.NoError(t, err)
	assert.NotNil(t, actual)

	areEqual, err := specsAreEqual(expected, *actual)

	assert.NoError(t, err)
	assert.True(t, areEqual)
}

func TestScaling(t *testing.T) {

	t.Run("Can scale to max amount when creating the resource", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		reconciler, client, memberClusters := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		statefulSets := readStatefulSets(mrs, memberClusters)
		assert.Len(t, statefulSets, 3)

		clusterSpecs, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}
		for _, item := range clusterSpecs {
			sts := statefulSets[item.ClusterName]
			assert.Equal(t, item.Members, int(*sts.Spec.Replicas))
		}
	})

	t.Run("Scale one at a time when scaling up", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1
		reconciler, client, memberClusters := defaultMultiReplicaSetReconciler(mrs, t)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		statefulSets := readStatefulSets(mrs, memberClusters)
		clusterSpecs, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}
		for _, item := range clusterSpecs {
			sts := statefulSets[item.ClusterName]
			assert.Equal(t, 1, int(*sts.Spec.Replicas))
		}

		// scale up in two different clusters at once.
		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList[2].Members = 3

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 2, 1, 1)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 4)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 1, 1)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 5)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 1, 2)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 6)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 1, 3)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 7)
	})

	t.Run("Scale one at a time when scaling down", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList[1].Members = 2
		mrs.Spec.ClusterSpecList[2].Members = 3
		reconciler, client, memberClusters := defaultMultiReplicaSetReconciler(mrs, t)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		statefulSets := readStatefulSets(mrs, memberClusters)
		clusterSpecList, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}

		for _, item := range clusterSpecList {
			sts := statefulSets[item.ClusterName]
			assert.Equal(t, item.Members, int(*sts.Spec.Replicas))
		}

		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 8)

		// scale down in all clusters.
		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 2, 2, 3)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 7)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 2, 3)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 6)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 1, 3)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 5)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 1, 2)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 4)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 1, 1)
		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 3)
	})

	t.Run("Added members don't have overlapping replica set member Ids", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1
		reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		assert.Len(t, om.CurrMockedConnection.GetProcesses(), 3)

		dep, err := om.CurrMockedConnection.ReadDeployment()
		assert.NoError(t, err)

		replicaSets := dep.ReplicaSets()

		assert.Len(t, replicaSets, 1)
		members := replicaSets[0].Members()
		assert.Len(t, members, 3)

		assertMemberNameAndId(t, members, fmt.Sprintf("%s-0-0", mrs.Name), 0)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-1-0", mrs.Name), 1)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-2-0", mrs.Name), 2)

		assert.Equal(t, members[0].Id(), 0)
		assert.Equal(t, members[1].Id(), 1)
		assert.Equal(t, members[2].Id(), 2)

		mrs.Spec.ClusterSpecList[0].Members = 2

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		dep, err = om.CurrMockedConnection.ReadDeployment()
		assert.NoError(t, err)

		replicaSets = dep.ReplicaSets()

		assert.Len(t, replicaSets, 1)
		members = replicaSets[0].Members()
		assert.Len(t, members, 4)

		assertMemberNameAndId(t, members, fmt.Sprintf("%s-0-0", mrs.Name), 0)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-0-1", mrs.Name), 3)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-1-0", mrs.Name), 1)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-2-0", mrs.Name), 2)
	})

	t.Run("Cluster can be added", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList = mrs.Spec.ClusterSpecList[:len(mrs.Spec.ClusterSpecList)-1]

		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1

		reconciler, client, memberClusters := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 1)

		// scale one member and add a new cluster
		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList = append(mrs.Spec.ClusterSpecList, mdbmulti.ClusterSpecItem{
			ClusterName: clusters[2],
			Members:     3,
		})

		err := client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 2, 1, 0)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 1, 0)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 1, 1)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 1, 2)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 1, 3)
	})

	t.Run("Cluster can be removed", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList[1].Members = 2
		mrs.Spec.ClusterSpecList[2].Members = 3

		reconciler, client, memberClusters := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		assertStatefulSetReplicas(t, mrs, memberClusters, 3, 2, 3)

		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList = mrs.Spec.ClusterSpecList[:len(mrs.Spec.ClusterSpecList)-1]

		err := client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 2, 2, 3)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 2, 3)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 2, 2)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 2, 1)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 2)

		// can reconcile again and it succeeds.
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 2)
	})

	t.Run("Multiple clusters can be removed", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

		mrs.Spec.ClusterSpecList[0].Members = 2
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 2

		reconciler, client, memberClusters := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		assertStatefulSetReplicas(t, mrs, memberClusters, 2, 1, 2)

		// remove first and last
		mrs.Spec.ClusterSpecList = []mdbmulti.ClusterSpecItem{mrs.Spec.ClusterSpecList[1]}

		err := client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 1, 1, 2)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 0, 1, 2)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(t, mrs, memberClusters, 0, 1, 1)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(t, mrs, memberClusters, 0, 1, 0)
	})
}

func TestClusterNumbering(t *testing.T) {

	t.Run("Create MDB CR first time", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		clusterNumMap := getClusterNumMapping(mrs)
		assertClusterpresent(t, clusterNumMap, mrs.Spec.ClusterSpecList, []int{0, 1, 2})
	})

	t.Run("Add Cluster", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList = mrs.Spec.ClusterSpecList[:len(mrs.Spec.ClusterSpecList)-1]

		reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		clusterNumMap := getClusterNumMapping(mrs)
		assertClusterpresent(t, clusterNumMap, mrs.Spec.ClusterSpecList, []int{0, 1})

		// add cluster
		mrs.Spec.ClusterSpecList = append(mrs.Spec.ClusterSpecList, mdbmulti.ClusterSpecItem{
			ClusterName: clusters[2],
			Members:     1,
		})

		err := client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		clusterNumMap = getClusterNumMapping(mrs)

		assert.Equal(t, 2, clusterNumMap[clusters[2]])
	})

	t.Run("Remove and Add back cluster", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1

		reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		clusterNumMap := getClusterNumMapping(mrs)
		assertClusterpresent(t, clusterNumMap, mrs.Spec.ClusterSpecList, []int{0, 1, 2})
		clusterOneIndex := clusterNumMap[clusters[1]]

		// Remove cluster index 1 from the specs
		mrs.Spec.ClusterSpecList = []mdbmulti.ClusterSpecItem{
			{
				ClusterName: clusters[0],
				Members:     1,
			},
			{
				ClusterName: clusters[2],
				Members:     1,
			},
		}
		err := client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		// Add cluster index 1 back to the specs
		mrs.Spec.ClusterSpecList = append(mrs.Spec.ClusterSpecList, mdbmulti.ClusterSpecItem{
			ClusterName: clusters[1],
			Members:     1,
		})

		err = client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		// assert the index corresponsing to cluster 1 is still 1
		clusterNumMap = getClusterNumMapping(mrs)
		assert.Equal(t, clusterOneIndex, clusterNumMap[clusters[1]])
	})
}

func getClusterNumMapping(m *mdbmulti.MongoDBMultiCluster) map[string]int {
	clusterMapping := make(map[string]int)
	bytes := m.Annotations[mdbmulti.LastClusterNumMapping]
	json.Unmarshal([]byte(bytes), &clusterMapping)

	return clusterMapping
}

// assertMemberNameAndId makes sure that the member with the given name has the given id.
// the processes are sorted and the order in the automation config is not necessarily the order
// in which they appear in the CR.
func assertMemberNameAndId(t *testing.T, members []om.ReplicaSetMember, name string, id int) {
	for _, m := range members {
		if m.Name() == name {
			assert.Equal(t, id, m.Id())
			return
		}
	}
	t.Fatalf("Member with name %s not found in replica set members", name)
}

func TestBackupConfigurationReplicaSet(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).
		SetConnectionSpec(testConnectionSpec()).
		SetBackup(mdbv1.Backup{
			Mode: "enabled",
		}).Build()

	reconciler, client, _ := defaultMultiReplicaSetReconciler(mrs, t)
	uuidStr := uuid.New().String()

	om.CurrMockedConnection = om.NewMockedOmConnection(om.NewDeployment())
	om.CurrMockedConnection.UpdateBackupConfig(&backup.Config{
		ClusterId: uuidStr,
		Status:    backup.Inactive,
	})

	// add the Replicaset cluster to OM
	om.CurrMockedConnection.BackupHostClusters[uuidStr] = &backup.HostCluster{
		ReplicaSetName: mrs.Name,
		ClusterName:    mrs.Name,
		TypeName:       "REPLICA_SET",
	}

	t.Run("Backup can be started", func(t *testing.T) {
		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)
		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()

		assert.Len(t, configResponse.Configs, 1)
		config := configResponse.Configs[0]

		assert.Equal(t, backup.Started, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup snapshot schedule tests", backupSnapshotScheduleTests(mrs, client, reconciler, uuidStr))

	t.Run("Backup can be stopped", func(t *testing.T) {
		mrs.Spec.Backup.Mode = "disabled"
		err := client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Stopped, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup can be terminated", func(t *testing.T) {
		mrs.Spec.Backup.Mode = "terminated"
		err := client.Update(context.TODO(), mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Terminating, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})
}

func TestMultiClusterFailover(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	reconciler, client, memberClusters := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	// trigger failover by adding an annotation to the CR
	// read the first cluster from the clusterSpec list and fail it over.
	expectedNodeCount := 0
	for _, e := range mrs.Spec.ClusterSpecList {
		expectedNodeCount += e.Members
	}

	cluster := mrs.Spec.ClusterSpecList[0]
	failedClusters := []failedcluster.FailedCluster{{ClusterName: cluster.ClusterName, Members: cluster.Members}}

	clusterSpecBytes, err := json.Marshal(failedClusters)
	assert.NoError(t, err)

	mrs.SetAnnotations(map[string]string{failedcluster.FailedClusterAnnotation: string(clusterSpecBytes)})

	err = client.Update(context.TODO(), mrs)
	assert.NoError(t, err)

	os.Setenv("PERFORM_FAILOVER", "true")
	defer os.Unsetenv("PERFORM_FAILOVER")

	memberwatch.AddFailoverAnnotation(*mrs, cluster.ClusterName, client)

	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	// assert the statefulset member count in the healthy cluster is same as the initial count
	statefulSets := readStatefulSets(mrs, memberClusters)
	currentNodeCount := 0

	// only 2 clusters' statefulsets should be fetched since the first cluster has been failed-over
	assert.Equal(t, 2, len(statefulSets))

	for _, s := range statefulSets {
		currentNodeCount += int(*s.Spec.Replicas)
	}

	assert.Equal(t, expectedNodeCount, currentNodeCount)
}

func assertClusterpresent(t *testing.T, m map[string]int, specs []mdbmulti.ClusterSpecItem, arr []int) {
	tmp := make([]int, 0)
	for _, s := range specs {
		tmp = append(tmp, m[s.ClusterName])
	}

	sort.Ints(tmp)
	assert.Equal(t, arr, tmp)
}

func assertStatefulSetReplicas(t *testing.T, mrs *mdbmulti.MongoDBMultiCluster, memberClusters map[string]cluster.Cluster, expectedReplicas ...int) {
	statefulSets := readStatefulSets(mrs, memberClusters)

	for i := range expectedReplicas {
		if val, ok := statefulSets[clusters[i]]; ok {
			assert.Equal(t, expectedReplicas[i], int(*val.Spec.Replicas))
		}
	}
}

func readStatefulSets(mrs *mdbmulti.MongoDBMultiCluster, memberClusters map[string]cluster.Cluster) map[string]appsv1.StatefulSet {
	allStatefulSets := map[string]appsv1.StatefulSet{}
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		panic(err)
	}

	for _, item := range clusterSpecList {
		memberClient := memberClusters[item.ClusterName]
		sts := appsv1.StatefulSet{}
		err := memberClient.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
		if err == nil {
			allStatefulSets[item.ClusterName] = sts
		}
	}
	return allStatefulSets
}

// specsAreEqual compares two different MongoDBMultiSpec instances and returns true if they are equal.
// the specs need to be marshaled and bytes compared as this ensures that empty slices are converted to nil
// ones and gives an accurate comparison.
// We are unable to use reflect.DeepEqual for this comparision as when deserialization happens,
// some fields on spec2 are nil, while spec1 are empty collections. By converting both to bytes
// we can ensure they are equivalent for our purposes.
func specsAreEqual(spec1, spec2 mdbmulti.MongoDBMultiSpec) (bool, error) {
	spec1Bytes, err := json.Marshal(spec1)
	if err != nil {
		return false, err
	}
	spec2Bytes, err := json.Marshal(spec2)
	if err != nil {
		return false, err
	}
	return bytes.Equal(spec1Bytes, spec2Bytes), nil
}

func defaultMultiReplicaSetReconciler(m *mdbmulti.MongoDBMultiCluster, t *testing.T) (*ReconcileMongoDbMultiReplicaSet, *mock.MockedClient, map[string]cluster.Cluster) {
	connection := func(ctx *om.OMContext) om.Connection {
		ret := om.NewEmptyMockedOmConnection(ctx)
		ret.(*om.MockedOmConnection).Hostnames = calculateHostNames(m)
		return ret

	}
	return multiReplicaSetReconcilerWithConnection(m, connection, t)
}

func calculateHostNames(m *mdbmulti.MongoDBMultiCluster) []string {
	if m.Spec.ExternalAccessConfiguration == nil || m.Spec.ExternalAccessConfiguration.ExternalDomain == nil {
		return nil
	}

	var expectedHostnames []string
	for i, cl := range m.Spec.ClusterSpecList {
		for j := 0; j < cl.Members; j++ {
			expectedHostnames = append(expectedHostnames, fmt.Sprintf("%s-%d-%d.%s", m.Name, i, j, *cl.ExternalAccessConfiguration.ExternalDomain))
		}
	}
	return expectedHostnames
}

func multiReplicaSetReconcilerWithConnection(m *mdbmulti.MongoDBMultiCluster,
	connectionFunc func(ctx *om.OMContext) om.Connection, t *testing.T) (*ReconcileMongoDbMultiReplicaSet, *mock.MockedClient, map[string]cluster.Cluster) {
	manager := mock.NewManager(m)
	manager.Client.AddDefaultMdbConfigResources()
	memberClusterMap := getFakeMultiClusterMap()
	return newMultiClusterReplicaSetReconciler(manager, connectionFunc, memberClusterMap), manager.Client, memberClusterMap
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
