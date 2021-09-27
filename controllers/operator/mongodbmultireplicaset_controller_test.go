package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

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
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
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
	clusters = []string{"api.kube.com", "api2.kube.com", "api3.kube.com"}
)

func checkMultiReconcileSuccessful(t *testing.T, reconciler reconcile.Reconciler, m *mdbmulti.MongoDBMulti, client *mock.MockedClient, shouldRequeue bool) {
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

func TestServiceCreation_WithoutDuplicates(t *testing.T) {
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	clusterSpecs := clusterSpecList
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
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	mrs.Spec.DuplicateServiceObjects = util.BooleanRef(true)

	reconciler, client, memberClusterMap := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
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
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, memberClients := defaultMultiReplicaSetReconciler(mrs, t)
	checkMultiReconcileSuccessful(t, reconciler, mrs, client, false)

	t.Run("Resources are created", func(t *testing.T) {
		clusterSpecs, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}
		for clusterNum, item := range clusterSpecs {
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

	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	for clusterNum, item := range clusterSpecs {
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
		TLSConfig: &mdbv1.TLSConfig{Enabled: true, CA: "some-ca", SecretRef: mdbv1.TLSSecretRef{Prefix: "some-prefix"}},
	}).Build()

	reconciler, client, memberClients := defaultMultiReplicaSetReconciler(mrs, t)
	createMultiClusterReplicaSetTLSData(memberClients, mrs)

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
		mrs.Spec.ClusterSpecList.ClusterSpecs[0].Members = 1
		mrs.Spec.ClusterSpecList.ClusterSpecs[1].Members = 1
		mrs.Spec.ClusterSpecList.ClusterSpecs[2].Members = 1
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
		mrs.Spec.ClusterSpecList.ClusterSpecs[0].Members = 3
		mrs.Spec.ClusterSpecList.ClusterSpecs[2].Members = 3

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
		mrs.Spec.ClusterSpecList.ClusterSpecs[0].Members = 3
		mrs.Spec.ClusterSpecList.ClusterSpecs[1].Members = 2
		mrs.Spec.ClusterSpecList.ClusterSpecs[2].Members = 3
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
		mrs.Spec.ClusterSpecList.ClusterSpecs[0].Members = 1
		mrs.Spec.ClusterSpecList.ClusterSpecs[1].Members = 1
		mrs.Spec.ClusterSpecList.ClusterSpecs[2].Members = 1

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

func assertStatefulSetReplicas(t *testing.T, mrs *mdbmulti.MongoDBMulti, memberClusters map[string]cluster.Cluster, expectedReplicas ...int) {
	if len(expectedReplicas) != len(memberClusters) {
		panic("must provide a replica count for each statefulset!")
	}
	statefulSets := readStatefulSets(mrs, memberClusters)
	assert.Equal(t, expectedReplicas[0], int(*statefulSets[clusters[0]].Spec.Replicas))
	assert.Equal(t, expectedReplicas[1], int(*statefulSets[clusters[1]].Spec.Replicas))
	assert.Equal(t, expectedReplicas[2], int(*statefulSets[clusters[2]].Spec.Replicas), "We should only scale one member at a time")
}

func readStatefulSets(mrs *mdbmulti.MongoDBMulti, memberClusters map[string]cluster.Cluster) map[string]appsv1.StatefulSet {
	allStatefulSets := map[string]appsv1.StatefulSet{}
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		panic(err)
	}
	for i, item := range clusterSpecList {
		memberClient := memberClusters[item.ClusterName]
		sts := appsv1.StatefulSet{}
		err := memberClient.GetClient().Get(context.TODO(), kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(i)), &sts)
		if err != nil {
			panic(err)
		}
		allStatefulSets[item.ClusterName] = sts
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
	return bytes.Compare(spec1Bytes, spec2Bytes) == 0, nil
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

func getFakeMultiClusterMap() map[string]cluster.Cluster {
	clusterMap := make(map[string]cluster.Cluster)

	for _, e := range clusters {
		memberClient := mock.NewClient()
		memberCluster := multicluster.New(memberClient)
		clusterMap[e] = memberCluster
	}
	return clusterMap
}
