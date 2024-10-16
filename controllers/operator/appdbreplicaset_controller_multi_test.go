package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

const opsManagerUserPassword = "MBPYfkAj5ZM0l9uw6C7ggw" //nolint

func TestAppDB_MultiCluster(t *testing.T) {
	ctx := context.Background()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	clusters := []string{centralClusterName, memberClusterName, memberClusterName2}

	clusterSpecItems := mdbv1.ClusterSpecList{
		{
			ClusterName: memberClusterName,
			Members:     2,
		},
		{
			ClusterName: memberClusterName2,
			Members:     3,
		},
	}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(clusterSpecItems).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDBTLSConfig(mdbv1.TLSConfig{
			Enabled:                      true,
			AdditionalCertificateDomains: nil,
			CA:                           "appdb-ca",
		})

	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	// prepare CA config map in central cluster
	caConfigMapName := createAppDbCAConfigMap(ctx, t, kubeClient, appdb)
	tlsCertSecretName, tlsSecretPemHash := createAppDBTLSCert(ctx, t, kubeClient, appdb)
	pemSecretName := tlsCertSecretName + "-pem"

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, zap.S(), omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	err = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)
	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	// requeue is true to add monitoring
	assert.True(t, reconcileResult.Requeue)

	centralClusterChecks := newClusterChecks(t, centralClusterName, -1, opsManager.Namespace, kubeClient)
	// secrets and config maps created by the operator shouldn't be created in central cluster
	centralClusterChecks.checkSecretNotFound(ctx, appdb.AutomationConfigSecretName())
	centralClusterChecks.checkConfigMapNotFound(ctx, appdb.AutomationConfigConfigMapName())
	centralClusterChecks.checkSecretNotFound(ctx, appdb.MonitoringAutomationConfigSecretName())
	centralClusterChecks.checkConfigMapNotFound(ctx, appdb.MonitoringAutomationConfigConfigMapName())
	centralClusterChecks.checkSecretNotFound(ctx, pemSecretName)
	centralClusterChecks.checkTLSCAConfigMap(ctx, caConfigMapName)
	centralClusterChecks.checkConfigMapNotFound(ctx, appdb.ProjectIDConfigMapName())

	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName].GetClient())
		memberClusterChecks.checkAutomationConfigSecret(ctx, appdb.AutomationConfigSecretName())
		memberClusterChecks.checkAutomationConfigConfigMap(ctx, appdb.AutomationConfigConfigMapName())
		memberClusterChecks.checkAutomationConfigSecret(ctx, appdb.MonitoringAutomationConfigSecretName())
		memberClusterChecks.checkAutomationConfigConfigMap(ctx, appdb.MonitoringAutomationConfigConfigMapName())
		memberClusterChecks.checkTLSCAConfigMap(ctx, caConfigMapName)
		// TLS secret should not be replicated, only PEM secret
		memberClusterChecks.checkSecretNotFound(ctx, tlsCertSecretName)
		memberClusterChecks.checkPEMSecret(ctx, pemSecretName, tlsSecretPemHash)

		memberClusterChecks.checkStatefulSet(ctx, opsManager.Spec.AppDB.NameForCluster(reconciler.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
		memberClusterChecks.checkServices(ctx, opsManager.Spec.AppDB.NameForCluster(reconciler.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
	}

	// OM API Key secret is required for enabling monitoring to OM
	createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

	// reconcile to add monitoring
	reconcileResult, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	require.False(t, reconcileResult.Requeue)

	// monitoring here is configured, everything should be replicated

	// we create project id and agent key resources only in member clusters
	centralClusterChecks.checkConfigMapNotFound(ctx, appdb.ProjectIDConfigMapName())
	agentAPIKey := ""
	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName].GetClient())
		projectID := memberClusterChecks.checkProjectIDConfigMap(ctx, appdb.ProjectIDConfigMapName())
		agentAPIKeyFromSecret := memberClusterChecks.checkAgentAPIKeySecret(ctx, projectID)
		assert.NotEmpty(t, agentAPIKeyFromSecret)
		if agentAPIKey == "" {
			// save the value to check if all member clusters contain the same value
			agentAPIKey = agentAPIKeyFromSecret
		}
		assert.Equal(t, agentAPIKey, agentAPIKeyFromSecret)
	}
}

func agentAPIKeySecretName(projectID string) string {
	return fmt.Sprintf("%s-group-secret", projectID)
}

func TestAppDB_MultiCluster_AutomationConfig(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName, memberClusterName2, memberClusterName3}

	builder := DefaultOpsManagerBuilder().
		SetName("om").
		SetNamespace("ns").
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
		},
		).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	globalClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	err := createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	// requeue is true to add monitoring
	assert.True(t, reconcileResult.Requeue)

	// OM API Key secret is required for enabling monitoring to OM
	createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

	// reconcile to add monitoring
	reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)
	reconcileResult, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	require.False(t, reconcileResult.Requeue)

	t.Run("check expected hostnames", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName2,
				Members:     1,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
		}
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 1, log)
	})

	t.Run("remove second cluster and add new one", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-2-0",
		}

		// 2 reconciles, remove 1 member from memberClusterName2 and add one from memberClusterName3
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 2, log)
	})

	t.Run("add second cluster back to check indexes are preserved with different clusterSpecItem order", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
			{
				ClusterName: memberClusterName2,
				Members:     2,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-1-1",
			"om-db-2-0",
		}

		// 2 reconciles to add 2 members of memberClusterName2
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 2, log)
	})

	t.Run("remove second cluster from global cluster to simulate full-cluster failure", func(t *testing.T) {
		globalMemberClusterMapWithoutCluster2 := getFakeMultiClusterMapWithClusters([]string{memberClusterName, memberClusterName3}, omConnectionFactory)
		// no changes to clusterSpecItems, nothing should be scaled, processes should be the same
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
			{
				ClusterName: memberClusterName2,
				Members:     2,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-1-1",
			"om-db-2-0",
		}

		// nothing to be scaled
		reconcileAppDBOnceAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalMemberClusterMapWithoutCluster2, memberClusterName, clusterSpecItems, false, expectedHostnames, expectedProcessNames, log)

		// memberClusterName2 is removed
		clusterSpecItems = mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
		}

		expectedHostnames = []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames = []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-2-0",
		}

		// one process from memberClusterName2 should be removed
		reconcileAppDBOnceAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalMemberClusterMapWithoutCluster2, memberClusterName, clusterSpecItems, true, expectedHostnames, expectedProcessNames, log)

		expectedHostnames = []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames = []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-2-0",
		}

		// the last process from memberClusterName2 should be removed
		// this should be final reconcile
		reconcileAppDBOnceAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalMemberClusterMapWithoutCluster2, memberClusterName, clusterSpecItems, false, expectedHostnames, expectedProcessNames, log)
	})
}

func assertExpectedProcesses(ctx context.Context, t *testing.T, memberClusterName string, reconciler *ReconcileAppDbReplicaSet, opsManager *omv1.MongoDBOpsManager, expectedHostnames []string, expectedProcessNames []string) {
	ac, err := automationconfig.ReadFromSecret(ctx, reconciler.getMemberCluster(memberClusterName).SecretClient, types.NamespacedName{
		Namespace: opsManager.GetNamespace(),
		Name:      opsManager.Spec.AppDB.AutomationConfigSecretName(),
	})
	require.NoError(t, err)

	assert.Equal(t, expectedHostnames, util.Transform(ac.Processes, func(obj automationconfig.Process) string {
		return obj.HostName
	}))
	assert.Equal(t, expectedProcessNames, util.Transform(ac.Processes, func(obj automationconfig.Process) string {
		return obj.Name
	}))

	assert.Equal(t, expectedHostnames, reconciler.getCurrentStatefulsetHostnames(opsManager))
}

func reconcileAppDBOnceAndCheckExpectedProcesses(ctx context.Context, t *testing.T, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]cluster.Cluster, memberClusterName string, clusterSpecItems mdbv1.ClusterSpecList, expectedRequeue bool, expectedHostnames []string, expectedProcessNames []string, log *zap.SugaredLogger) {
	opsManager.Spec.AppDB.ClusterSpecList = clusterSpecItems

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactoryFunc)
	require.NoError(t, err)
	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	if expectedRequeue {
		// we're expected to scale one by one for expectedReconciles count
		require.Greater(t, reconcileResult.RequeueAfter, time.Duration(0))
	} else {
		require.Equal(t, util.TWENTY_FOUR_HOURS, reconcileResult.RequeueAfter)
	}

	assertExpectedProcesses(ctx, t, memberClusterName, reconciler, opsManager, expectedHostnames, expectedProcessNames)
}

func reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx context.Context, t *testing.T, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]cluster.Cluster, memberClusterName string, clusterSpecItems mdbv1.ClusterSpecList, expectedHostnames []string, expectedProcessNames []string, expectedReconciles int, log *zap.SugaredLogger) {
	opsManager.Spec.AppDB.ClusterSpecList = clusterSpecItems

	var reconciler *ReconcileAppDbReplicaSet
	var err error
	for i := 0; i < expectedReconciles; i++ {
		reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactoryFunc)
		require.NoError(t, err)
		reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)

		// when scaling only the last final reconcile will be without requeueAfter
		if i < expectedReconciles-1 {
			// we're expected to scale one by one for expectedReconciles count
			require.Greater(t, reconcileResult.RequeueAfter, time.Duration(0), "failed in reconcile %d", i)
		} else {
			ok, _ := workflow.OK().ReconcileResult()
			require.Equal(t, ok, reconcileResult, "failed in reconcile %d", i)
		}
	}

	assertExpectedProcesses(ctx, t, memberClusterName, reconciler, opsManager, expectedHostnames, expectedProcessNames)
}

func makeClusterSpecList(clusters ...string) mdbv1.ClusterSpecList {
	var clusterSpecItems mdbv1.ClusterSpecList
	for _, clusterName := range clusters {
		clusterSpecItems = append(clusterSpecItems, mdbv1.ClusterSpecItem{ClusterName: clusterName, Members: 1})
	}
	return clusterSpecItems
}

func TestAppDB_MultiCluster_ClusterMapping(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	memberClusterName4 := "member-cluster-4"
	memberClusterName5 := "member-cluster-5"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2, memberClusterName3, memberClusterName4, memberClusterName5}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	// prepare CA config map in central cluster
	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	t.Run("check mapping cm has been created", func(t *testing.T) {
		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		})
	})

	t.Run("deployment state config map should be recreated after deletion", func(t *testing.T) {
		deleteDeploymentStateConfigMap(ctx, t, kubeClient, appdb)
		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		})
	})

	t.Run("config map is updated after adding new cluster", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName2, memberClusterName3)
		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		})
	})

	t.Run("mapping is preserved if cluster is removed", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName3)

		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		})
	})

	t.Run("new cluster is assigned new index instead of the next one", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName3, memberClusterName4)
		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
		})
	})

	t.Run("empty cluster spec list does not change mapping", func(t *testing.T) {
		appdb.ClusterSpecList = mdbv1.ClusterSpecList{}

		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
		})
	})

	t.Run("new cluster alone will get new index", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName5)

		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
			memberClusterName5: 4,
		})
	})

	t.Run("defining clusters again will get their old indexes, order doesn't matter", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName4, memberClusterName2, memberClusterName3, memberClusterName1)

		reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.stateStore.WriteState(ctx, reconciler.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
			memberClusterName5: 4,
		})
	})
}

func TestAppDB_MultiCluster_ClusterMappingMigrationToDeploymentState(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2, memberClusterName3}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	lastAppliedMongoDBVersion := "5.0"
	opsManager.Annotations = map[string]string{annotations.LastAppliedMongoDBVersion: lastAppliedMongoDBVersion}
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	legacyCM := configmap.Builder().
		SetName(appdb.Name() + "-cluster-mapping").
		SetNamespace(appdb.Namespace).
		SetData(map[string]string{
			memberClusterName3: "2",
			memberClusterName1: "1",
			memberClusterName2: "0",
		}).Build()
	require.NoError(t, kubeClient.Create(ctx, &legacyCM))

	legacyLastAppliedSpecCM := configmap.Builder().
		SetName(appdb.Name() + "-member-spec").
		SetNamespace(appdb.Namespace).
		SetData(map[string]string{
			memberClusterName3: "3",
			memberClusterName1: "1",
			memberClusterName2: "2",
		}).Build()
	require.NoError(t, kubeClient.Create(ctx, &legacyLastAppliedSpecCM))

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	t.Run("check legacy cm should be migrated to the new deployment state", func(t *testing.T) {
		checkClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 1,
			memberClusterName2: 0,
			memberClusterName3: 2,
		})
		checkLastAppliedMemberSpec(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 1,
			memberClusterName3: 3,
			memberClusterName2: 2,
		})
		checkLastAppliedMongoDBVersion(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), lastAppliedMongoDBVersion)
	})
}

// This test ensures that we update legacy Config Maps on top of the new Deployment State
func TestAppDB_MultiCluster_KeepUpdatingLegacyState(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2}

	expectedLastAppliedMongoDBVersion := "6.0.0"
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
		SetAppDbMembers(1).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDbVersion(expectedLastAppliedMongoDBVersion)

	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	t.Run("check that legacy config maps are created based on deployment state", func(t *testing.T) {
		expectedClusterMapping := map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		}
		checkLegacyClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), expectedClusterMapping)

		expectedLastAppliedMemberSpec := map[string]int{
			memberClusterName1: 1,
			memberClusterName2: 1,
		}
		checkLegacyLastAppliedMemberSpec(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), expectedLastAppliedMemberSpec)
		checkLegacyLastAppliedMongoDBVersion(ctx, t, reconciler.centralClient, opsManager.Namespace, opsManager.GetName(), expectedLastAppliedMongoDBVersion)
	})

	// Update the cluster spec lists and perform new reconcile
	opsManager.Spec.AppDB.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName3)
	reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)
	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	t.Run("check that legacy config maps are updated on reconcile", func(t *testing.T) {
		// Cluster 2 is not in cluster spec list anymore, but the reconciler should keep it in the index map
		expectedClusterMapping := map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		}
		checkLegacyClusterMapping(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), expectedClusterMapping)

		expectedLastAppliedMemberSpec := map[string]int{
			// After a full reconciliation, the final state would be [memberClusterName1: 1, memberClusterName3: 1],
			// but we only run one loop here, and we can only modify one member at a time, so we only scaled down
			// the replica on cluster 2, and end up with 1-0-0
			memberClusterName1: 1,
			memberClusterName2: 0,
			memberClusterName3: 0,
		}
		checkLegacyLastAppliedMemberSpec(ctx, t, reconciler.centralClient, appdb.Namespace, appdb.Name(), expectedLastAppliedMemberSpec)
		checkLegacyLastAppliedMongoDBVersion(ctx, t, reconciler.centralClient, opsManager.Namespace, opsManager.GetName(), expectedLastAppliedMongoDBVersion)
	})
}

func deleteDeploymentStateConfigMap(ctx context.Context, t *testing.T, kubeClient client.Client, appdb omv1.AppDBSpec) {
	cm := corev1.ConfigMap{}
	err := kubeClient.Get(ctx, kube.ObjectKey(appdb.Namespace, appdb.Name()+"-state"), &cm)
	require.NoError(t, err)
	err = kubeClient.Delete(ctx, &cm)
	require.NoError(t, err)
}

func readDeploymentState[T any](ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string) *T {
	cm := corev1.ConfigMap{}
	err := c.Get(ctx, kube.ObjectKey(namespace, resourceName+"-state"), &cm)
	require.NoError(t, err)

	stateStruct := new(T)
	require.NoError(t, json.Unmarshal([]byte(cm.Data["state"]), stateStruct))

	return stateStruct
}

func checkClusterMapping(ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string, expectedMapping map[string]int) {
	deploymentState := readDeploymentState[AppDBDeploymentState](ctx, t, c, namespace, resourceName)
	assert.Equal(t, expectedMapping, deploymentState.ClusterMapping)
}

func checkLastAppliedMemberSpec(ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string, expectedMemberSpec map[string]int) {
	deploymentState := readDeploymentState[AppDBDeploymentState](ctx, t, c, namespace, resourceName)
	assert.Equal(t, expectedMemberSpec, deploymentState.LastAppliedMemberSpec)
}

func checkLastAppliedMongoDBVersion(ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string, expectedVersion string) {
	deploymentState := readDeploymentState[AppDBDeploymentState](ctx, t, c, namespace, resourceName)
	assert.Equal(t, expectedVersion, deploymentState.LastAppliedMongoDBVersion)
}

func checkLegacyClusterMapping(ctx context.Context, t *testing.T, client client.Client, namespace, appdbName string, expectedData map[string]int) {
	cm := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: appdbName + "-cluster-mapping", Namespace: namespace}, cm)
	require.NoError(t, err)
	for k, v := range expectedData {
		assert.Equal(t, strconv.Itoa(v), cm.Data[k])
	}
}

func checkLegacyLastAppliedMemberSpec(ctx context.Context, t *testing.T, client client.Client, namespace, appdbName string, expectedData map[string]int) {
	cm := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: appdbName + "-member-spec", Namespace: namespace}, cm)
	require.NoError(t, err)
	for k, v := range expectedData {
		assert.Equal(t, strconv.Itoa(v), cm.Data[k])
	}
}

func checkLegacyLastAppliedMongoDBVersion(ctx context.Context, t *testing.T, client client.Client, namespace, omName, expectedVersion string) {
	opsManager := &omv1.MongoDBOpsManager{}
	err := client.Get(ctx, types.NamespacedName{Name: omName, Namespace: namespace}, opsManager)
	require.NoError(t, err)
	assert.Equal(t, expectedVersion, opsManager.Annotations[annotations.LastAppliedMongoDBVersion])
}

func createOMAPIKeySecret(ctx context.Context, t *testing.T, secretClient secrets.SecretClient, opsManager *omv1.MongoDBOpsManager) {
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secretClient, "")
	assert.NoError(t, err)

	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		Build()

	err = secretClient.CreateSecret(ctx, apiKeySecret)
	require.NoError(t, err)
}

func createAppDbCAConfigMap(ctx context.Context, t *testing.T, k8sClient client.Client, appDBSpec omv1.AppDBSpec) string {
	cert, _ := createMockCertAndKeyBytes()
	cm := configmap.Builder().
		SetName(appDBSpec.GetCAConfigMapName()).
		SetNamespace(appDBSpec.Namespace).
		SetDataField("ca-pem", string(cert)).
		Build()

	err := k8sClient.Create(ctx, &cm)
	require.NoError(t, err)

	return appDBSpec.GetCAConfigMapName()
}

func createAppDBTLSCert(ctx context.Context, t *testing.T, k8sClient client.Client, appDBSpec omv1.AppDBSpec) (string, string) {
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDBSpec.GetTlsCertificatesSecretName(),
			Namespace: appDBSpec.Namespace,
		},
		Type: corev1.SecretTypeTLS,
	}

	certs := map[string][]byte{}
	certs["tls.crt"], certs["tls.key"] = createMockCertAndKeyBytes()

	tlsSecret.Data = certs
	err := k8sClient.Create(ctx, tlsSecret)
	require.NoError(t, err)

	pemHash := enterprisepem.ReadHashFromData(secrets.DataToStringData(tlsSecret.Data), zap.S())
	require.NotEmpty(t, pemHash)

	return tlsSecret.Name, pemHash
}

func TestAppDB_MultiCluster_ReconcilerFailsWhenThereIsNoClusterListConfigured(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: "a",
				Members:     2,
			},
		}).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	_, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.Error(t, err)
}

func TestAppDBMultiClusterRemoveResources(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: "a",
				Members:     2,
			},
			{
				ClusterName: "b",
				Members:     2,
			},
			{
				ClusterName: "c",
				Members:     1,
			},
		}).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	clusters = []string{"a", "b", "c"}
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters, omConnectionFactory)
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, opsManager, memberClusterMap, omConnectionFactory)

	// create opsmanager reconciler
	appDBReconciler, _ := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, zap.S(), omConnectionFactory.GetConnectionFunc)

	// initially requeued as monitoring needs to be configured
	_, err := appDBReconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	// check AppDB statefulset exists in cluster "a" and cluster "b"
	for clusterIdx, clusterSpecItem := range opsManager.Spec.AppDB.ClusterSpecList {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName].GetClient())

		memberClusterChecks.checkStatefulSet(ctx, opsManager.Spec.AppDB.NameForCluster(appDBReconciler.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
	}

	// delete the OM resource
	reconciler.OnDelete(ctx, opsManager, zap.S())
	assert.Zero(t, len(reconciler.resourceWatcher.GetWatchedResources()))

	// assert STS objects in member cluster
	for clusterIdx, clusterSpecItem := range opsManager.Spec.AppDB.ClusterSpecList {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName].GetClient())

		memberClusterChecks.checkStatefulSetDoesNotExist(ctx, opsManager.Spec.AppDB.NameForCluster(appDBReconciler.getMemberClusterIndex(clusterSpecItem.ClusterName)))
	}
}
