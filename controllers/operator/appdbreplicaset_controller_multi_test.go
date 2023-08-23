package operator

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const opsManagerUserPassword = "MBPYfkAj5ZM0l9uw6C7ggw"

func TestAppDB_MultiCluster(t *testing.T) {
	centralClusterName := om.DummmyCentralClusterName
	memberClusterName := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	clusters := []string{centralClusterName, memberClusterName, memberClusterName2}
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:])

	clusterSpecItems := []mdbv1.ClusterSpecItem{
		{
			ClusterName: memberClusterName,
			Members:     2,
		},
		{
			ClusterName: memberClusterName2,
			Members:     3,
		}}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(clusterSpecItems).
		SetAppDbMembers(0).
		SetAppDBTopology(om.ClusterTopologyMultiCluster).
		SetAppDBTLSConfig(mdbv1.TLSConfig{
			Enabled:                      true,
			AdditionalCertificateDomains: nil,
			CA:                           "appdb-ca",
		})

	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewManager(&opsManager)

	// prepare CA config map in central cluster
	caConfigMapName := createCAConfigMap(t, kubeManager.GetClient(), appdb)
	tlsCertSecretName, tlsSecretPemHash := createAppDBTLSCert(t, kubeManager.GetClient(), appdb)
	pemSecretName := tlsCertSecretName + "-pem"

	reconciler, err := newAppDbMultiReconciler(kubeManager, opsManager, memberClusterMap)
	require.NoError(t, err)

	err = createOpsManagerUserPasswordSecret(kubeManager.Client, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)
	reconcileResult, err := reconciler.ReconcileAppDB(&opsManager)
	require.NoError(t, err)
	// requeue is true to add monitoring
	assert.True(t, reconcileResult.Requeue)

	centralClusterChecks := newAppDBClusterChecks(t, opsManager, centralClusterName, kubeManager.Client, -1)
	// secrets and config maps created by the operator shouldn't be created in central cluster
	centralClusterChecks.checkSecretNotFound(appdb.AutomationConfigSecretName())
	centralClusterChecks.checkConfigMapNotFound(appdb.AutomationConfigConfigMapName())
	centralClusterChecks.checkSecretNotFound(appdb.MonitoringAutomationConfigSecretName())
	centralClusterChecks.checkConfigMapNotFound(appdb.MonitoringAutomationConfigConfigMapName())
	centralClusterChecks.checkSecretNotFound(pemSecretName)
	centralClusterChecks.checkCAConfigMap(caConfigMapName)
	centralClusterChecks.checkConfigMapNotFound(appdb.ProjectIDConfigMapName())

	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterClient := memberClusterMap[clusterSpecItem.ClusterName]
		memberClusterChecks := newAppDBClusterChecks(t, opsManager, clusterSpecItem.ClusterName, memberClusterClient.GetClient(), clusterIdx)
		memberClusterChecks.checkAutomationConfigSecret(appdb.AutomationConfigSecretName())
		memberClusterChecks.checkAutomationConfigConfigMap(appdb.AutomationConfigConfigMapName())
		memberClusterChecks.checkAutomationConfigSecret(appdb.MonitoringAutomationConfigSecretName())
		memberClusterChecks.checkAutomationConfigConfigMap(appdb.MonitoringAutomationConfigConfigMapName())
		memberClusterChecks.checkCAConfigMap(caConfigMapName)
		// TLS secret should not be replicated, only PEM secret
		memberClusterChecks.checkSecretNotFound(tlsCertSecretName)
		memberClusterChecks.checkPEMSecret(pemSecretName, tlsSecretPemHash)

		memberClusterChecks.checkStatefulSet(opsManager.Spec.AppDB.NameForCluster(reconciler.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
		memberClusterChecks.checkServices(opsManager.Spec.AppDB.NameForCluster(reconciler.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
	}

	// OM API Key secret is required for enabling monitoring to OM
	createOMAPIKeySecret(t, reconciler.SecretClient, opsManager)

	// reconcile to add monitoring
	reconcileResult, err = reconciler.ReconcileAppDB(&opsManager)
	require.NoError(t, err)
	require.False(t, reconcileResult.Requeue)

	// monitoring here is configured, everything should be replicated

	// we create project id and agent key resources only in member clusters
	centralClusterChecks.checkConfigMapNotFound(appdb.ProjectIDConfigMapName())
	agentAPIKey := ""
	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterClient := memberClusterMap[clusterSpecItem.ClusterName]
		memberClusterChecks := newAppDBClusterChecks(t, opsManager, clusterSpecItem.ClusterName, memberClusterClient.GetClient(), clusterIdx)
		projectID := memberClusterChecks.checkProjectIDConfigMap(appdb.ProjectIDConfigMapName())
		agentAPIKeyFromSecret := memberClusterChecks.checkAgentAPIKeySecret(projectID)
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
	centralClusterName := om.DummmyCentralClusterName
	memberClusterName := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName, memberClusterName2, memberClusterName3}
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:])

	builder := DefaultOpsManagerBuilder().
		SetName("om").
		SetNamespace("ns").
		SetAppDBClusterSpecList([]mdbv1.ClusterSpecItem{
			{
				ClusterName: memberClusterName,
				Members:     2,
			}},
		).
		SetAppDbMembers(0).
		SetAppDBTopology(om.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	//appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewManager(&opsManager)

	err := createOpsManagerUserPasswordSecret(kubeManager.Client, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)

	reconcileAppDBAndCheckExpectedProcesses := func(t *testing.T, memberClusterName string, clusterSpecItems []mdbv1.ClusterSpecItem, expectedHostnames []string, expectedProcessNames []string) {
		opsManager.Spec.AppDB.ClusterSpecList = clusterSpecItems

		reconciler, err := newAppDbMultiReconciler(kubeManager, opsManager, memberClusterMap)
		require.NoError(t, err)

		reconcileResult, err := reconciler.ReconcileAppDB(&opsManager)
		require.NoError(t, err)
		// requeue is true to add monitoring
		assert.True(t, reconcileResult.Requeue)

		ac, err := automationconfig.ReadFromSecret(reconciler.getMemberCluster(memberClusterName).SecretClient, types.NamespacedName{
			Namespace: opsManager.GetNamespace(),
			Name:      opsManager.Spec.AppDB.AutomationConfigSecretName(),
		})
		require.NoError(t, err)

		assert.Equal(t, expectedHostnames, transform(ac.Processes, func(obj automationconfig.Process) string {
			return obj.HostName
		}))
		assert.Equal(t, expectedProcessNames, transform(ac.Processes, func(obj automationconfig.Process) string {
			return obj.Name
		}))

		assert.Equal(t, expectedHostnames, reconciler.getCurrentStatefulsetHostnames(&opsManager))
	}

	t.Run("check expected hostnames", func(t *testing.T) {
		clusterSpecItems := []mdbv1.ClusterSpecItem{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName2,
				Members:     3,
			}}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-1-2-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-1-1",
			"om-db-1-2",
		}
		reconcileAppDBAndCheckExpectedProcesses(t, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames)
	})

	// TODO enable when scaling implemented
	/*t.Run("remove second cluster and add new one", func(t *testing.T) {
		clusterSpecItems := []mdbv1.ClusterSpecItem{
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

		reconcileAppDBAndCheckExpectedProcesses(t, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames)
	})

	t.Run("add second cluster back to check indexes are preserved with different clusterSpecItem order", func(t *testing.T) {
		clusterSpecItems := []mdbv1.ClusterSpecItem{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			}, {
				ClusterName: memberClusterName2,
				Members:     2,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-1-2-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-1-1",
			"om-db-1-2",
			"om-db-2-0",
		}

		reconcileAppDBAndCheckExpectedProcesses(t, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames)
	})*/
}

func transform[T any, U any](objs []T, f func(obj T) U) []U {
	result := make([]U, len(objs))
	for i := 0; i < len(objs); i++ {
		result[i] = f(objs[i])
	}
	return result
}

func TestAppDB_MultiCluster_ClusterMapping(t *testing.T) {
	centralClusterName := om.DummmyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	memberClusterName4 := "member-cluster-4"
	memberClusterName5 := "member-cluster-5"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2, memberClusterName3, memberClusterName4, memberClusterName5}
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:])

	// helper to simplify member cluster definition
	makeClusterSpecList := func(clusters ...string) []mdbv1.ClusterSpecItem {
		var clusterSpecItems []mdbv1.ClusterSpecItem
		for _, cluster := range clusters {
			clusterSpecItems = append(clusterSpecItems, mdbv1.ClusterSpecItem{ClusterName: cluster, Members: 1})
		}
		return clusterSpecItems
	}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
		SetAppDbMembers(0).
		SetAppDBTopology(om.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewManager(&opsManager)

	// prepare CA config map in central cluster
	reconciler, err := newAppDbMultiReconciler(kubeManager, opsManager, memberClusterMap)
	require.NoError(t, err)

	t.Run("check mapping cm has been created", func(t *testing.T) {
		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		})
	})

	t.Run("config map should be recreated after deletion", func(t *testing.T) {
		deleteClusterMappingConfigMap(t, kubeManager, appdb)
		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		})
	})

	t.Run("config map is updated after adding new cluster", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName2, memberClusterName3)
		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		})
	})

	t.Run("mapping is preserved if cluster is removed", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName3)

		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		})
	})

	t.Run("new cluster is assigned new index instead of the next one", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName3, memberClusterName4)
		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
		})
	})

	t.Run("empty cluster spec list does not change mapping", func(t *testing.T) {
		appdb.ClusterSpecList = []mdbv1.ClusterSpecItem{}

		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
		})
	})

	t.Run("new cluster alone will get new index", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName5)

		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
			memberClusterName5: 4,
		})
	})

	t.Run("defining clusters again will get their old indexes, order doesn't matter", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName4, memberClusterName2, memberClusterName3, memberClusterName1)

		_, err := reconciler.updateMemberClusterMapping(appdb)
		require.NoError(t, err)
		checkClusterMapping(t, reconciler, appdb, map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
			memberClusterName5: 4,
		})
	})
}

func deleteClusterMappingConfigMap(t *testing.T, kubeManager *mock.MockedManager, appdb om.AppDBSpec) {
	cm := corev1.ConfigMap{}
	err := kubeManager.GetClient().Get(context.TODO(), kube.ObjectKey(appdb.Namespace, appdb.Name()+"-cluster-mapping"), &cm)
	require.NoError(t, err)
	err = kubeManager.GetClient().Delete(context.TODO(), &cm)
	require.NoError(t, err)
}

func checkClusterMapping(t *testing.T, reconciler *ReconcileAppDbReplicaSet, appdb om.AppDBSpec, expectedMapping map[string]int) {
	cm := corev1.ConfigMap{}
	err := reconciler.centralClient.Get(context.TODO(), kube.ObjectKey(appdb.Namespace, appdb.Name()+"-cluster-mapping"), &cm)
	assert.NoError(t, err)

	expectedMappingAsStrings := map[string]string{}
	for k, v := range expectedMapping {
		expectedMappingAsStrings[k] = fmt.Sprintf("%d", v)
	}
	assert.Equal(t, expectedMappingAsStrings, cm.Data)
}

type appDBClusterChecks struct {
	t            *testing.T
	namespace    string
	clusterName  string
	kubeClient   client.Client
	clusterIndex int
}

func newAppDBClusterChecks(t *testing.T, opsManager om.MongoDBOpsManager, clusterName string, kubeClient client.Client, clusterIndex int) *appDBClusterChecks {
	result := appDBClusterChecks{
		t:            t,
		namespace:    opsManager.Namespace,
		clusterName:  clusterName,
		kubeClient:   kubeClient,
		clusterIndex: clusterIndex,
	}

	return &result
}

func (c *appDBClusterChecks) checkAutomationConfigSecret(secretName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, secretName), &sec)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, sec.Data, automationconfig.ConfigKey, "clusterName: %s", c.clusterName)
}

func (c *appDBClusterChecks) checkAgentAPIKeySecret(projectID string) string {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, agentAPIKeySecretName(projectID)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, util.OmAgentApiKey, "clusterName: %s", c.clusterName)
	return string(sec.Data[util.OmAgentApiKey])
}

func (c *appDBClusterChecks) checkSecretNotFound(secretName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, secretName), &sec)
	assert.Error(c.t, err, "clusterName: %s", c.clusterName)
	assert.True(c.t, apiErrors.IsNotFound(err))
}

func (c *appDBClusterChecks) checkConfigMapNotFound(configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, configMapName), &cm)
	assert.Error(c.t, err, "clusterName: %s", c.clusterName)
	assert.True(c.t, apiErrors.IsNotFound(err))
}

func (c *appDBClusterChecks) checkPEMSecret(secretName string, pemHash string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, secretName), &sec)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, sec.Data, pemHash, "clusterName: %s", c.clusterName)
}

func (c *appDBClusterChecks) checkAutomationConfigConfigMap(configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, configMapName), &cm)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, cm.Data, appDBACConfigMapVersionField, "clusterName: %s", c.clusterName)
}

func (c *appDBClusterChecks) checkCAConfigMap(configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, configMapName), &cm)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, cm.Data, "ca-pem", "clusterName: %s", c.clusterName)
}

func (c *appDBClusterChecks) checkProjectIDConfigMap(configMapName string) string {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, configMapName), &cm)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, cm.Data, util.AppDbProjectIdKey, "clusterName: %s", c.clusterName)
	return cm.Data[util.AppDbProjectIdKey]
}

func (c *appDBClusterChecks) checkServices(statefulSetName string, expectedMembers int) {
	for podIdx := 0; podIdx < expectedMembers; podIdx++ {
		svc := corev1.Service{}
		serviceName := fmt.Sprintf("%s-%d-svc", statefulSetName, podIdx)
		err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, serviceName), &svc)
		require.NoError(c.t, err, "clusterName: %s", c.clusterName)

		assert.Equal(c.t, map[string]string{
			"controller":                         "mongodb-enterprise-operator",
			"statefulset.kubernetes.io/pod-name": fmt.Sprintf("%s-%d", statefulSetName, podIdx)},
			svc.Spec.Selector)
	}
}

func (c *appDBClusterChecks) checkStatefulSet(statefulSetName string, expectedMembers int) {
	sts := appsv1.StatefulSet{}
	err := c.kubeClient.Get(context.TODO(), kube.ObjectKey(c.namespace, statefulSetName), &sts)
	require.NoError(c.t, err, "clusterName: %s stsName: %s", c.clusterName, statefulSetName)
	require.Equal(c.t, expectedMembers, int(*sts.Spec.Replicas))
	require.Equal(c.t, statefulSetName, sts.ObjectMeta.Name)
}

func createOMAPIKeySecret(t *testing.T, secretClient secrets.SecretClient, opsManager om.MongoDBOpsManager) {
	APIKeySecretName, err := opsManager.APIKeySecretName(secretClient, "")
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

	err = secretClient.CreateSecret(apiKeySecret)
	require.NoError(t, err)
}

func createCAConfigMap(t *testing.T, k8sClient client.Client, appDBSpec om.AppDBSpec) string {
	cert, _ := createMockCertAndKeyBytes()
	cm := configmap.Builder().
		SetName(appDBSpec.GetCAConfigMapName()).
		SetNamespace(appDBSpec.Namespace).
		SetDataField("ca-pem", string(cert)).
		Build()

	err := k8sClient.Create(context.TODO(), &cm)
	require.NoError(t, err)

	return appDBSpec.GetCAConfigMapName()
}

func createAppDBTLSCert(t *testing.T, k8sClient client.Client, appDBSpec om.AppDBSpec) (string, string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDBSpec.GetTlsCertificatesSecretName(),
			Namespace: appDBSpec.Namespace,
		},
		Type: corev1.SecretTypeTLS,
	}

	certs := map[string][]byte{}
	certs["tls.crt"], certs["tls.key"] = createMockCertAndKeyBytes()

	secret.Data = certs
	err := k8sClient.Create(context.TODO(), secret)
	require.NoError(t, err)

	pemHash := enterprisepem.ReadHashFromData(secrets.DataToStringData(secret.Data), zap.S())
	require.NotEmpty(t, pemHash)

	return secret.Name, pemHash
}

func TestAppDB_MultiCluster_ReconcilerFailsWhenThereIsNoClusterListConfigured(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList([]mdbv1.ClusterSpecItem{
			{
				ClusterName: "a",
				Members:     2,
			}}).
		SetAppDBTopology(om.ClusterTopologyMultiCluster)
	opsManager := builder.Build()
	_, err := newAppDbReconciler(mock.NewManager(&opsManager), opsManager)
	assert.Error(t, err)
}

func TestAppDB_MultiCluster_ReconcilerFailsWhenThereIsNotMatchingClusterOnSpecList(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList([]mdbv1.ClusterSpecItem{
			{
				ClusterName: "a",
				Members:     2,
			},
			{
				ClusterName: "b",
				Members:     2,
			},
			{
				ClusterName: "missing_cluster",
				Members:     2,
			}}).
		SetAppDBTopology(om.ClusterTopologyMultiCluster)
	opsManager := builder.Build()

	clusters = []string{"a", "b", "c"}
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters)

	_, err := newAppDbMultiReconciler(mock.NewManager(&opsManager), opsManager, memberClusterMap)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "missing_cluster")
}
