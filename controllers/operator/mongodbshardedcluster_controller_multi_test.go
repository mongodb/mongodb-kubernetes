package operator

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yudai/gojsondiff"
	"github.com/yudai/gojsondiff/formatter"
	"go.uber.org/zap"
	"golang.org/x/exp/constraints"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// Creates a list of ClusterSpecItems based on names and distribution
// The two input list must have the same size
func createClusterSpecList(memberCounts map[string]int) mdbv1.ClusterSpecList {
	specList := make(mdbv1.ClusterSpecList, 0)
	for clusterName, memberCount := range memberCounts {
		specList = append(specList, mdbv1.ClusterSpecItem{
			ClusterName: clusterName,
			Members:     memberCount,
		})
	}
	return specList
}

func newShardedClusterReconcilerForMultiCluster(ctx context.Context, sc *mdbv1.MongoDB, globalMemberClustersMap map[string]cluster.Cluster, kubeClient kubernetesClient.Client, omConnectionFactory *om.CachedOMConnectionFactory) (*ReconcileMongoDbShardedCluster, *ShardedClusterReconcileHelper, error) {
	r := &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: newReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omConnectionFactory.GetConnectionFunc,
		memberClustersMap:         globalMemberClustersMap,
	}
	reconcileHelper, err := NewShardedClusterReconcilerHelper(ctx, r.ReconcileCommonController, sc, globalMemberClustersMap, omConnectionFactory.GetConnectionFunc, zap.S())
	if err != nil {
		return nil, nil, err
	}
	return r, reconcileHelper, nil
}

func TestReconcileCreateMultiClusterShardedCluster(t *testing.T) {
	cluster1 := "member-cluster-1"
	cluster2 := "member-cluster-2"
	memberClusterNames := []string{
		cluster1,
		cluster2,
	}

	shardCount := 2
	// Two Kubernetes clusters, 2 replicaset members of each shard on the first one, 3 on the second one
	// This means a MongodPerShardCount of 5
	shardDistribution := []map[string]int{
		{cluster1: 2, cluster2: 3},
		{cluster1: 2, cluster2: 3},
	}
	shardClusterSpecList := createClusterSpecList(shardDistribution[0])

	// For Mongos and Config servers, 2 replicaset members on the first one, 1 on the second one
	mongosDistribution := map[string]int{cluster1: 2, cluster2: 1}
	mongosAndConfigSrvClusterSpecList := createClusterSpecList(mongosDistribution)

	configSrvDistribution := map[string]int{cluster1: 2, cluster2: 1}
	configSrvDistributionClusterSpecList := createClusterSpecList(configSrvDistribution)

	ctx := context.Background()
	sc := DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(shardCount).
		// The below parameters should be ignored when a clusterSpecList is configured/for multiClusterTopology
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		// Same pods repartition for
		SetShardClusterSpec(shardClusterSpecList).
		SetConfigSrvClusterSpec(configSrvDistributionClusterSpecList).
		SetMongosClusterSpec(mongosAndConfigSrvClusterSpecList).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	fakeClient := mock.NewEmptyFakeClientBuilder().WithObjects(sc).WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeClient)
	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, true)

	reconciler, reconcilerHelper, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	clusterMapping := reconcilerHelper.deploymentState.ClusterMapping
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		allHostnames, _ := generateAllHosts(sc, mongosDistribution, clusterMapping, configSrvDistribution, shardDistribution)
		connection.(*om.MockedOmConnection).AddHosts(allHostnames)
	})

	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)
	checkCorrectShardDistributionInStatus(t, sc)

	expectedHostnameOverrideMap := createExpectedHostnameOverrideMap(sc, clusterMapping, mongosDistribution, configSrvDistribution, shardDistribution)

	for clusterIdx, clusterSpecItem := range shardClusterSpecList {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, sc.Namespace, memberClusterMap[clusterSpecItem.ClusterName].GetClient())
		// Shards statefulsets should have the names shardname-0-0, shardname-0-1, shardname-1-0...
		for shardIdx := 0; shardIdx < shardCount; shardIdx++ {
			memberClusterChecks.checkStatefulSet(ctx, fmt.Sprintf("%s-%d-%d", sc.Name, shardIdx, clusterIdx), shardDistribution[shardIdx][clusterSpecItem.ClusterName])
		}
		// Config servers statefulsets should have the names mongoName-config-0, mongoName-config-1
		configSrvStsName := fmt.Sprintf("%s-config-%d", sc.Name, clusterIdx)
		memberClusterChecks.checkStatefulSet(ctx, configSrvStsName, configSrvDistribution[clusterSpecItem.ClusterName])
		memberClusterChecks.checkServices(ctx, configSrvStsName, configSrvDistribution[clusterSpecItem.ClusterName])
		// Mongos statefulsets should have the names mongoName-mongos-0, mongoName-mongos-1
		mongosStsName := fmt.Sprintf("%s-mongos-%d", sc.Name, clusterIdx)
		memberClusterChecks.checkStatefulSet(ctx, mongosStsName, mongosDistribution[clusterSpecItem.ClusterName])
		memberClusterChecks.checkServices(ctx, mongosStsName, mongosDistribution[clusterSpecItem.ClusterName])
		memberClusterChecks.checkAgentAPIKeySecret(ctx, om.TestGroupID)
		memberClusterChecks.checkHostnameOverrideConfigMap(ctx, fmt.Sprintf("%s-hostname-override", sc.Name), expectedHostnameOverrideMap)
	}
}

func createExpectedHostnameOverrideMap(sc *mdbv1.MongoDB, clusterMapping map[string]int, mongosDistribution map[string]int, configSrvDistribution map[string]int, shardDistribution []map[string]int) map[string]string {
	expectedHostnameOverrideMap := map[string]string{}
	allHostnames, allPodNames := generateAllHosts(sc, mongosDistribution, clusterMapping, configSrvDistribution, shardDistribution)
	for i := range allPodNames {
		expectedHostnameOverrideMap[allPodNames[i]] = allHostnames[i]
	}
	return expectedHostnameOverrideMap
}

type MultiClusterShardedClusterConfigList []MultiClusterShardedClusterConfigItem

type MultiClusterShardedClusterConfigItem struct {
	Name               string
	ShardsMembersArray []int
	MongosMembers      int
	ConfigSrvMembers   int
}

func (r *MultiClusterShardedClusterConfigList) AddCluster(clusterName string, shards []int, mongosCount int, configSrvCount int) {
	clusterSpec := MultiClusterShardedClusterConfigItem{
		Name:               clusterName,
		ShardsMembersArray: shards,
		MongosMembers:      mongosCount,
		ConfigSrvMembers:   configSrvCount,
	}

	*r = append(*r, clusterSpec)
}

func (r *MultiClusterShardedClusterConfigList) GetNames() []string {
	clusterNames := make([]string, len(*r))
	for i, clusterSpec := range *r {
		clusterNames[i] = clusterSpec.Name
	}

	return clusterNames
}

func (r *MultiClusterShardedClusterConfigList) GenerateAllHosts(sc *mdbv1.MongoDB, clusterMapping map[string]int) ([]string, []string) {
	var allHosts []string
	var allPodNames []string
	for _, clusterSpec := range *r {
		memberClusterName := clusterSpec.Name
		clusterIdx := clusterMapping[memberClusterName]

		for podIdx := range clusterSpec.MongosMembers {
			allHosts = append(allHosts, getMultiClusterFQDN(sc.MongosRsName(), sc.Namespace, clusterIdx, podIdx, "cluster.local"))
			allPodNames = append(allPodNames, getPodName(sc.MongosRsName(), clusterIdx, podIdx))
		}

		for podIdx := range clusterSpec.ConfigSrvMembers {
			allHosts = append(allHosts, getMultiClusterFQDN(sc.ConfigRsName(), sc.Namespace, clusterIdx, podIdx, "cluster.local"))
			allPodNames = append(allPodNames, getPodName(sc.ConfigRsName(), clusterIdx, podIdx))
		}

		for shardCount := range clusterSpec.ShardsMembersArray {
			for shardIdx := range shardCount {
				allHosts = append(allHosts, getMultiClusterFQDN(sc.ShardRsName(shardIdx), sc.Namespace, clusterIdx, shardIdx, "cluster.local"))
				allPodNames = append(allPodNames, getPodName(sc.ShardRsName(shardIdx), clusterIdx, shardIdx))
			}
		}
	}

	return allHosts, allPodNames
}

func TestReconcileMultiClusterShardedClusterCertsAndSecretsReplication(t *testing.T) {
	expectedClusterConfigList := make(MultiClusterShardedClusterConfigList, 0)
	expectedClusterConfigList.AddCluster("member-cluster-1", []int{2, 2}, 0, 2)
	expectedClusterConfigList.AddCluster("member-cluster-2", []int{3, 3}, 1, 1)
	expectedClusterConfigList.AddCluster("member-cluster-3", []int{3, 3}, 3, 0)
	expectedClusterConfigList.AddCluster("member-cluster-4", []int{0, 0}, 2, 3)

	shardCount := 2
	shardDistribution := []map[string]int{
		{expectedClusterConfigList[0].Name: 2, expectedClusterConfigList[1].Name: 3, expectedClusterConfigList[2].Name: 3},
		{expectedClusterConfigList[0].Name: 2, expectedClusterConfigList[1].Name: 3, expectedClusterConfigList[2].Name: 3},
	}
	shardClusterSpecList := createClusterSpecList(shardDistribution[0])

	mongosDistribution := map[string]int{expectedClusterConfigList[1].Name: 1, expectedClusterConfigList[2].Name: 3, expectedClusterConfigList[3].Name: 2}
	mongosAndConfigSrvClusterSpecList := createClusterSpecList(mongosDistribution)

	configSrvDistribution := map[string]int{expectedClusterConfigList[0].Name: 2, expectedClusterConfigList[1].Name: 1, expectedClusterConfigList[3].Name: 3}
	configSrvDistributionClusterSpecList := createClusterSpecList(configSrvDistribution)

	certificatesSecretsPrefix := "some-prefix"
	sc := DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(shardCount).
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		SetShardClusterSpec(shardClusterSpecList).
		SetConfigSrvClusterSpec(configSrvDistributionClusterSpecList).
		SetMongosClusterSpec(mongosAndConfigSrvClusterSpecList).
		SetSecurity(mdbv1.Security{
			TLSConfig:                 &mdbv1.TLSConfig{Enabled: true, CA: "tls-ca-config"},
			CertificatesSecretsPrefix: certificatesSecretsPrefix,
			Authentication: &mdbv1.Authentication{
				Enabled:         true,
				Modes:           []mdbv1.AuthMode{"X509"},
				InternalCluster: util.X509,
			},
		}).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	projectConfigMap := configmap.Builder().
		SetName(mock.TestProjectConfigMapName).
		SetNamespace(mock.TestNamespace).
		SetDataField(util.OmBaseUrl, "http://mycompany.example.com:8080").
		SetDataField(util.OmProjectName, om.TestGroupName).
		SetDataField(util.SSLMMSCAConfigMap, "mms-ca-config").
		SetDataField(util.OmOrgId, "").
		Build()

	mmsCAConfigMap := configmap.Builder().
		SetName("mms-ca-config").
		SetNamespace(mock.TestNamespace).
		SetDataField(util.CaCertMMS, "cert text").
		Build()

	cert, _ := createMockCertAndKeyBytes()
	tlsCAConfigMap := configmap.Builder().
		SetName("tls-ca-config").
		SetNamespace(mock.TestNamespace).
		SetDataField("ca-pem", string(cert)).
		Build()

	// create the secrets for all the shards
	shardSecrets := createSecretsForShards(sc.Name, sc.Spec.ShardCount, certificatesSecretsPrefix)

	// create secrets for mongos
	mongosSecrets := createMongosSecrets(sc.Name, certificatesSecretsPrefix)

	// create secrets for config server
	configSrvSecrets := createConfigSrvSecrets(sc.Name, certificatesSecretsPrefix)

	// create `agent-certs` secret
	agentCertsSecret := createAgentCertsSecret(sc.Name, certificatesSecretsPrefix)

	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(sc).
		WithObjects(&projectConfigMap, &mmsCAConfigMap, &tlsCAConfigMap).
		WithObjects(shardSecrets...).
		WithObjects(mongosSecrets...).
		WithObjects(configSrvSecrets...).
		WithObjects(agentCertsSecret).
		WithObjects(mock.GetCredentialsSecret(om.TestUser, om.TestApiKey)).
		Build()

	kubeClient := kubernetesClient.NewClient(fakeClient)
	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(expectedClusterConfigList.GetNames(), omConnectionFactory, true, false)

	ctx := context.Background()
	reconciler, reconcilerHelper, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	clusterMapping := reconcilerHelper.deploymentState.ClusterMapping
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		allHostnames, _ := expectedClusterConfigList.GenerateAllHosts(sc, clusterMapping)
		connection.(*om.MockedOmConnection).AddHosts(allHostnames)
	})

	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	for clusterIdx, clusterDef := range expectedClusterConfigList {
		memberClusterChecks := newClusterChecks(t, clusterDef.Name, clusterIdx, sc.Namespace, memberClusterMap[clusterDef.Name].GetClient())

		memberClusterChecks.checkMMSCAConfigMap(ctx, "mms-ca-config")
		memberClusterChecks.checkTLSCAConfigMap(ctx, "tls-ca-config")
		memberClusterChecks.checkAgentAPIKeySecret(ctx, om.TestGroupID)
		memberClusterChecks.checkAgentCertsSecret(ctx, certificatesSecretsPrefix, sc.Name)

		memberClusterChecks.checkMongosCertsSecret(ctx, certificatesSecretsPrefix, sc.Name, clusterDef.MongosMembers > 0)
		memberClusterChecks.checkConfigSrvCertsSecret(ctx, certificatesSecretsPrefix, sc.Name, clusterDef.ConfigSrvMembers > 0)

		for shardIdx, shardMembers := range clusterDef.ShardsMembersArray {
			memberClusterChecks.checkInternalClusterCertSecret(ctx, certificatesSecretsPrefix, sc.Name, shardIdx, shardMembers > 0)
			memberClusterChecks.checkMemberCertSecret(ctx, certificatesSecretsPrefix, sc.Name, shardIdx, shardMembers > 0)
		}
	}
}

func createSecretsForShards(resourceName string, shardCount int, certificatesSecretsPrefix string) []client.Object {
	var shardSecrets []client.Object
	for i := 0; i < shardCount; i++ {
		shardData := make(map[string][]byte)
		shardData["tls.crt"], shardData["tls.key"] = createMockCertAndKeyBytes()

		shardSecrets = append(shardSecrets, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-%d-cert", certificatesSecretsPrefix, resourceName, i), Namespace: mock.TestNamespace},
			Data:       shardData,
			Type:       corev1.SecretTypeTLS,
		})

		shardSecrets = append(shardSecrets, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-%d-%s", certificatesSecretsPrefix, resourceName, i, util.ClusterFileName), Namespace: mock.TestNamespace},
			Data:       shardData,
			Type:       corev1.SecretTypeTLS,
		})
	}
	return shardSecrets
}

func createMongosSecrets(resourceName string, certificatesSecretsPrefix string) []client.Object {
	mongosData := make(map[string][]byte)
	mongosData["tls.crt"], mongosData["tls.key"] = createMockCertAndKeyBytes()

	// create the mongos secret
	mongosSecretName := fmt.Sprintf("%s-%s-mongos-cert", certificatesSecretsPrefix, resourceName)
	mongosSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mongosSecretName, Namespace: mock.TestNamespace},
		Data:       mongosData,
		Type:       corev1.SecretTypeTLS,
	}

	mongosClusterFileSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-mongos-%s", certificatesSecretsPrefix, resourceName, util.ClusterFileName), Namespace: mock.TestNamespace},
		Data:       mongosData,
		Type:       corev1.SecretTypeTLS,
	}

	return []client.Object{mongosSecret, mongosClusterFileSecret}
}

func createConfigSrvSecrets(resourceName string, certificatesSecretsPrefix string) []client.Object {
	configData := make(map[string][]byte)
	configData["tls.crt"], configData["tls.key"] = createMockCertAndKeyBytes()

	configSrvSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-config-cert", certificatesSecretsPrefix, resourceName), Namespace: mock.TestNamespace},
		Data:       configData,
		Type:       corev1.SecretTypeTLS,
	}

	configSrvClusterSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-config-%s", certificatesSecretsPrefix, resourceName, util.ClusterFileName), Namespace: mock.TestNamespace},
		Data:       configData,
		Type:       corev1.SecretTypeTLS,
	}

	return []client.Object{configSrvSecret, configSrvClusterSecret}
}

func createAgentCertsSecret(resourceName string, certificatesSecretsPrefix string) *corev1.Secret {
	subjectModifier := func(cert *x509.Certificate) {
		cert.Subject.OrganizationalUnit = []string{"cloud"}
		cert.Subject.Locality = []string{"New York"}
		cert.Subject.Province = []string{"New York"}
		cert.Subject.Country = []string{"US"}
	}
	cert, key := createMockCertAndKeyBytes(subjectModifier, func(cert *x509.Certificate) { cert.Subject.CommonName = util.AutomationAgentName })

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%s", certificatesSecretsPrefix, resourceName, util.AgentSecretName),
			Namespace: mock.TestNamespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": cert,
			"tls.key": key,
		},
	}
}

func TestReconcileForComplexMultiClusterYaml(t *testing.T) {
	ctx := context.Background()
	sc, err := loadMongoDBResource("testdata/mdb-sharded-multi-cluster-complex.yaml")
	require.NoError(t, err)

	cluster0 := "cluster-0"
	cluster1 := "cluster-1"
	cluster2 := "cluster-2"
	clusterAnalytics := "cluster-analytics"
	clusterAnalytics2 := "cluster-analytics-2"
	memberClusterNames := []string{
		cluster0,
		cluster1,
		cluster2,
		clusterAnalytics,
		clusterAnalytics2,
	}

	// expected distributions of shards are copied from testdata/mdb-sharded-multi-cluster-complex-expected-shardmap.yaml
	shardDistribution := []map[string]int{
		{
			cluster0: 2,
			cluster1: 2,
			cluster2: 1,
		}, // shard 0
		{
			cluster0: 1,
			cluster1: 2,
			cluster2: 3,
		}, // shard 1
		{
			cluster0:          2,
			cluster1:          3,
			cluster2:          0,
			clusterAnalytics:  1,
			clusterAnalytics2: 2,
		}, // shard 2
		{
			cluster0: 2,
			cluster1: 2,
			cluster2: 1,
		}, // shard 3
	}

	// expected distributions of mongos and config srv are copied from testdata/mdb-sharded-multi-cluster-complex.yaml
	mongosDistribution := map[string]int{
		cluster0: 1,
		cluster1: 1,
		cluster2: 0,
	}

	configSrvDistribution := map[string]int{
		cluster0: 2,
		cluster1: 2,
		cluster2: 1,
	}

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	memberClusterMap := getFakeMultiClusterMapWithClusters(memberClusterNames, omConnectionFactory)

	reconciler, reconcilerHelper, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	clusterMapping := reconcilerHelper.deploymentState.ClusterMapping
	require.NoError(t, err)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		hosts, _ := generateAllHosts(sc, mongosDistribution, clusterMapping, configSrvDistribution, shardDistribution)
		connection.(*om.MockedOmConnection).AddHosts(hosts)
	})

	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	expectedReplicaSets, err := loadExpectedReplicaSets("testdata/mdb-sharded-multi-cluster-complex-expected-replicasets.yaml")
	require.NoError(t, err)
	normalizedExpectedReplicaSets, err := normalizeObjectToInterfaceMap(expectedReplicaSets)
	require.NoError(t, err)
	automationConfig, err := omConnectionFactory.GetConnection().ReadAutomationConfig()
	require.NoError(t, err)
	normalizedActualReplicaSets, err := normalizeObjectToInterfaceMap(map[string]any{"replicaSets": automationConfig.Deployment.ReplicaSets()})
	require.NoError(t, err)
	if !assert.Equal(t, normalizedExpectedReplicaSets, normalizedActualReplicaSets) {
		visualDiff, err := getVisualJsonDiff(normalizedExpectedReplicaSets, normalizedActualReplicaSets)
		require.NoError(t, err)
		fmt.Printf("\n%s\n", visualDiff)
	}

	for shardIdx := 0; shardIdx < sc.Spec.ShardCount; shardIdx++ {
		for clusterName, expectedMembersCount := range shardDistribution[shardIdx] {
			memberClusterChecks := newClusterChecks(t, clusterName, clusterMapping[clusterName], sc.Namespace, memberClusterMap[clusterName].GetClient())
			if expectedMembersCount > 0 {
				memberClusterChecks.checkStatefulSet(ctx, sc.MultiShardRsName(clusterMapping[clusterName], shardIdx), expectedMembersCount)
			} else {
				memberClusterChecks.checkStatefulSetDoesNotExist(ctx, sc.MultiShardRsName(clusterMapping[clusterName], shardIdx))
			}
		}
	}

	for clusterName, expectedMembersCount := range mongosDistribution {
		memberClusterChecks := newClusterChecks(t, clusterName, clusterMapping[clusterName], sc.Namespace, memberClusterMap[clusterName].GetClient())
		if expectedMembersCount > 0 {
			memberClusterChecks.checkStatefulSet(ctx, sc.MultiMongosRsName(clusterMapping[clusterName]), expectedMembersCount)
		} else {
			memberClusterChecks.checkStatefulSetDoesNotExist(ctx, sc.MultiMongosRsName(clusterMapping[clusterName]))
		}
	}

	for clusterName, expectedMembersCount := range configSrvDistribution {
		memberClusterChecks := newClusterChecks(t, clusterName, clusterMapping[clusterName], sc.Namespace, memberClusterMap[clusterName].GetClient())
		memberClusterChecks.checkStatefulSet(ctx, sc.MultiConfigRsName(clusterMapping[clusterName]), expectedMembersCount)
	}

	expectedHostnameOverrideMap := createExpectedHostnameOverrideMap(sc, clusterMapping, mongosDistribution, configSrvDistribution, shardDistribution)
	for _, clusterName := range memberClusterNames {
		memberClusterChecks := newClusterChecks(t, clusterName, clusterMapping[clusterName], sc.Namespace, memberClusterMap[clusterName].GetClient())
		memberClusterChecks.checkHostnameOverrideConfigMap(ctx, fmt.Sprintf("%s-hostname-override", sc.Name), expectedHostnameOverrideMap)
	}
}

func generateAllHosts(sc *mdbv1.MongoDB, mongosDistribution map[string]int, clusterMapping map[string]int, configSrvDistribution map[string]int, shardDistribution []map[string]int) ([]string, []string) {
	var allHosts []string
	var allPodNames []string
	podNames, hosts := generateHostsWithDistribution(sc.MongosRsName(), sc.Namespace, mongosDistribution, clusterMapping)
	allHosts = append(allHosts, hosts...)
	allPodNames = append(allPodNames, podNames...)

	podNames, hosts = generateHostsWithDistribution(sc.ConfigRsName(), sc.Namespace, configSrvDistribution, clusterMapping)
	allHosts = append(allHosts, hosts...)
	allPodNames = append(allPodNames, podNames...)

	for shardIdx := 0; shardIdx < sc.Spec.ShardCount; shardIdx++ {
		podNames, hosts = generateHostsWithDistribution(sc.ShardRsName(shardIdx), sc.Namespace, shardDistribution[shardIdx], clusterMapping)
		allHosts = append(allHosts, hosts...)
		allPodNames = append(allPodNames, podNames...)
	}
	return allHosts, allPodNames
}

func TestMigrateToNewDeploymentState(t *testing.T) {
	ctx := context.Background()

	// These annotations should be preserved, but not appear in the migrated config map
	initialAnnotations := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}
	sc := DefaultClusterBuilder().
		SetAnnotations(initialAnnotations).
		Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	memberClusterMap := getFakeMultiClusterMapWithClusters([]string{"member-cluster-1"}, omConnectionFactory)

	reconciler, _, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	require.NoError(t, err)

	// Migration is performed at reconciliation, when needed
	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	// Ensure that reconciliation generated the correct deployment state
	configMapName := fmt.Sprintf("%s-state", sc.Name)
	stateConfigMap := &corev1.ConfigMap{}
	err = kubeClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: sc.Namespace}, stateConfigMap)
	require.NoError(t, err)

	expectedDeploymentState := generateExpectedDeploymentState(t, sc)
	require.Contains(t, stateConfigMap.Data, stateKey)
	require.JSONEq(t, expectedDeploymentState, stateConfigMap.Data[stateKey])

	// Original annotations must be preserved
	updatedSc := &mdbv1.MongoDB{}
	err = kubeClient.Get(ctx, types.NamespacedName{Name: sc.Name, Namespace: sc.Namespace}, updatedSc)
	require.NoError(t, err)
	for key, value := range initialAnnotations {
		require.Equal(t, value, updatedSc.Annotations[key], "Annotation %s should be preserved", key)
	}

	// Verify that we also store the state in the annotations (everything below)
	// This way, downgrading the operator is possible without breaking the state
	require.Contains(t, updatedSc.Annotations, util.LastAchievedSpec)
	actualLastAchievedSpec := updatedSc.Annotations[util.LastAchievedSpec]

	var configMapData, actualLastAchievedSpecData map[string]interface{}
	// Deserialize the JSON data from the  annotation
	err = json.Unmarshal([]byte(actualLastAchievedSpec), &actualLastAchievedSpecData)
	require.NoError(t, err)

	// Extract lastAchievedSpec from the state Config Map
	err = json.Unmarshal([]byte(stateConfigMap.Data[stateKey]), &configMapData)
	require.NoError(t, err)
	expectedLastAchievedSpec, ok := configMapData["lastAchievedSpec"].(map[string]interface{})
	require.True(t, ok, "Expected lastAchievedSpec field is missing or invalid")

	require.Equal(t, expectedLastAchievedSpec, actualLastAchievedSpecData)
}

// Without genericity and type hinting, when unmarshalling the file in a struct, fields that should be omitted when empty
// are not, and the actual/expected configurations are not compared correctly
func testDesiredConfigurationFromYAML[T *mdbv1.ShardedClusterComponentSpec | map[int]*mdbv1.ShardedClusterComponentSpec](t *testing.T, mongoDBResourceFile string, expectedConfigurationFile string, shardedComponentType string) {
	ctx := context.Background()
	sc, err := loadMongoDBResource(mongoDBResourceFile)
	require.NoError(t, err)

	memberClusterNames := []string{"cluster-0", "cluster-1", "cluster-2", "cluster-analytics", "cluster-analytics-2"}
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	memberClusterMap := getFakeMultiClusterMapWithClusters(memberClusterNames, omConnectionFactory)

	_, reconcilerHelper, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	require.NoError(t, err)

	var actual interface{}
	// no reconcile here, we just test prepareDesiredConfiguration
	switch shardedComponentType {
	case "shard":
		actual = reconcilerHelper.prepareDesiredShardsConfiguration()
	case "config":
		actual = reconcilerHelper.prepareDesiredConfigServerConfiguration()
	case "mongos":
		actual = reconcilerHelper.prepareDesiredMongosConfiguration()
	}

	expected, err := unmarshalYamlFileInStruct[T](expectedConfigurationFile)
	require.NoError(t, err)

	normalizedActual, err := normalizeObjectToInterfaceMap(actual)
	require.NoError(t, err)
	normalizedExpected, err := normalizeObjectToInterfaceMap(expected)
	require.NoError(t, err)

	assert.Equal(t, normalizedExpected, normalizedActual)
	visualDiff, err := getVisualJsonDiff(normalizedExpected, normalizedActual)
	require.NoError(t, err)

	if !assert.Empty(t, visualDiff) {
		// it is extremely difficult to diagnose problems in IDE's console as the diff dump is very large >400 lines,
		// therefore we're saving visual diffs in ops-manager-kubernetes/tmp dir to a temp file
		tmpFile, err := os.CreateTemp(path.Join(os.Getenv("PROJECT_DIR"), "tmp"), "jsondiff")
		if err != nil {
			// ignore the error, it's not part of the actual test
			fmt.Printf("error saving diff to tmp file: %v", err)
		} else {
			if tmpFile != nil {
				defer func() { _ = tmpFile.Close() }()
			}
			_, _ = tmpFile.WriteString(visualDiff)
			if tmpFile != nil {
				fmt.Printf("Diff written to %s\n", tmpFile.Name())
			}
		}
	}
}

// Multi-Cluster
func TestShardMapForComplexMultiClusterYaml(t *testing.T) {
	testDesiredConfigurationFromYAML[map[int]*mdbv1.ShardedClusterComponentSpec](t, "testdata/mdb-sharded-multi-cluster-complex.yaml", "testdata/mdb-sharded-multi-cluster-complex-expected-shardmap.yaml", "shard")
}

// Config servers and Mongos share a lot of logic, and have the same settings in CRDs, the two below tests use the same files, and are almost identical
func TestConfigServerdExpectedConfigFromMultiClusterYaml(t *testing.T) {
	testDesiredConfigurationFromYAML[*mdbv1.ShardedClusterComponentSpec](t, "testdata/mdb-sharded-multi-cluster-configsrv-mongos.yaml", "testdata/mdb-sharded-multi-cluster-configsrv-mongos-expected-config.yaml", "config")
}

func TestMongosExpectedConfigFromMultiClusterYaml(t *testing.T) {
	testDesiredConfigurationFromYAML[*mdbv1.ShardedClusterComponentSpec](t, "testdata/mdb-sharded-multi-cluster-configsrv-mongos.yaml", "testdata/mdb-sharded-multi-cluster-configsrv-mongos-expected-config.yaml", "mongos")
}

// Single-Cluster
func TestShardMapForSingleClusterWithOverridesYaml(t *testing.T) {
	testDesiredConfigurationFromYAML[map[int]*mdbv1.ShardedClusterComponentSpec](t, "testdata/mdb-sharded-single-with-overrides.yaml", "testdata/mdb-sharded-single-with-overrides-expected-shardmap.yaml", "shard")
}

// Config servers and Mongos share a lot of logic, and have the same settings in CRDs, the two below tests use the same files, and are almost identical
func TestConfigServerdExpectedConfigFromSingleClusterYaml(t *testing.T) {
	testDesiredConfigurationFromYAML[*mdbv1.ShardedClusterComponentSpec](t, "testdata/mdb-sharded-single-cluster-configsrv-mongos.yaml", "testdata/mdb-sharded-single-cluster-configsrv-mongos-expected-config.yaml", "config")
}

func TestMongosExpectedConfigFromSingleClusterYaml(t *testing.T) {
	testDesiredConfigurationFromYAML[*mdbv1.ShardedClusterComponentSpec](t, "testdata/mdb-sharded-single-cluster-configsrv-mongos.yaml", "testdata/mdb-sharded-single-cluster-configsrv-mongos-expected-config.yaml", "mongos")
}

func TestMultiClusterShardedSetRace(t *testing.T) {
	cluster1 := "cluster-member-1"
	cluster2 := "cluster-member-2"

	memberClusterNames := []string{
		cluster1,
		cluster2,
	}

	shardCount := 2
	// Two Kubernetes clusters, 2 replicaset members of each shard on the first one, 3 on the second one
	// This means a MongodPerShardCount of 5
	shardDistribution := []map[string]int{
		{cluster1: 2, cluster2: 3},
		{cluster1: 2, cluster2: 3},
	}
	shardClusterSpecList := createClusterSpecList(shardDistribution[0])

	// For Mongos and Config servers, 2 replicaset members on the first one, 1 on the second one
	mongosDistribution := map[string]int{cluster1: 2, cluster2: 1}
	mongosAndConfigSrvClusterSpecList := createClusterSpecList(mongosDistribution)

	configSrvDistribution := map[string]int{cluster1: 2, cluster2: 1}
	configSrvDistributionClusterSpecList := createClusterSpecList(configSrvDistribution)

	sc, cfgMap, projectName := buildShardedClusterWithCustomProjectName("mc-sharded", shardCount, shardClusterSpecList, mongosAndConfigSrvClusterSpecList, configSrvDistributionClusterSpecList)
	sc1, cfgMap1, projectName1 := buildShardedClusterWithCustomProjectName("mc-sharded-1", shardCount, shardClusterSpecList, mongosAndConfigSrvClusterSpecList, configSrvDistributionClusterSpecList)
	sc2, cfgMap2, projectName2 := buildShardedClusterWithCustomProjectName("mc-sharded-2", shardCount, shardClusterSpecList, mongosAndConfigSrvClusterSpecList, configSrvDistributionClusterSpecList)

	resourceToProjectMapping := map[string]string{
		"mc-sharded":   projectName,
		"mc-sharded-1": projectName1,
		"mc-sharded-2": projectName2,
	}

	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(sc, sc1, sc2).
		WithObjects(cfgMap, cfgMap1, cfgMap2).
		WithObjects(mock.GetCredentialsSecret(om.TestUser, om.TestApiKey)).
		Build()

	kubeClient := kubernetesClient.NewClient(fakeClient)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory().WithResourceToProjectMapping(resourceToProjectMapping)
	globalMemberClustersMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, false)

	ctx := context.Background()
	reconciler := &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: newReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omConnectionFactory.GetConnectionFunc,
		memberClustersMap:         globalMemberClustersMap,
	}

	allHostnames := generateHostsForCluster(ctx, reconciler, sc, mongosDistribution, configSrvDistribution, shardDistribution)
	allHostnames1 := generateHostsForCluster(ctx, reconciler, sc1, mongosDistribution, configSrvDistribution, shardDistribution)
	allHostnames2 := generateHostsForCluster(ctx, reconciler, sc2, mongosDistribution, configSrvDistribution, shardDistribution)

	projectHostMapping := map[string][]string{
		projectName:  allHostnames,
		projectName1: allHostnames1,
		projectName2: allHostnames2,
	}

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		hostnames := projectHostMapping[connection.GroupName()]
		connection.(*om.MockedOmConnection).AddHosts(hostnames)
	})

	testConcurrentReconciles(ctx, t, fakeClient, reconciler, sc, sc1, sc2)
}

func generateHostsForCluster(ctx context.Context, reconciler *ReconcileMongoDbShardedCluster, sc *mdbv1.MongoDB, mongosDistribution map[string]int, configSrvDistribution map[string]int, shardDistribution []map[string]int) []string {
	reconcileHelper, _ := NewShardedClusterReconcilerHelper(ctx, reconciler.ReconcileCommonController, sc, reconciler.memberClustersMap, reconciler.omConnectionFactory, zap.S())
	allHostnames, _ := generateAllHosts(sc, mongosDistribution, reconcileHelper.deploymentState.ClusterMapping, configSrvDistribution, shardDistribution)

	return allHostnames
}

func buildShardedClusterWithCustomProjectName(mcShardedClusterName string, shardCount int, shardClusterSpecList mdbv1.ClusterSpecList, mongosAndConfigSrvClusterSpecList mdbv1.ClusterSpecList, configSrvDistributionClusterSpecList mdbv1.ClusterSpecList) (*mdbv1.MongoDB, *corev1.ConfigMap, string) {
	configMapName := mock.TestProjectConfigMapName + "-" + mcShardedClusterName
	projectName := om.TestGroupName + "-" + mcShardedClusterName

	return DefaultClusterBuilder().
		SetName(mcShardedClusterName).
		SetOpsManagerConfigMapName(configMapName).
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(shardCount).
		// The below parameters should be ignored when a clusterSpecList is configured/for multiClusterTopology
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		// Same pods repartition for
		SetShardClusterSpec(shardClusterSpecList).
		SetConfigSrvClusterSpec(configSrvDistributionClusterSpecList).
		SetMongosClusterSpec(mongosAndConfigSrvClusterSpecList).
		Build(), mock.GetProjectConfigMap(configMapName, projectName, ""), projectName
}

func checkCorrectShardDistributionInStatus(t *testing.T, sc *mdbv1.MongoDB) {
	clusterSpecItemToClusterNameMembers := func(clusterSpecItem mdbv1.ClusterSpecItem, _ int) (string, int) {
		return clusterSpecItem.ClusterName, clusterSpecItem.Members
	}
	expectedMongosSizeStatusInClusters := util.TransformToMap(sc.Spec.MongosSpec.ClusterSpecList, clusterSpecItemToClusterNameMembers)
	expectedConfigSrvSizeStatusInClusters := util.TransformToMap(sc.Spec.ConfigSrvSpec.ClusterSpecList, clusterSpecItemToClusterNameMembers)
	expectedShardSizeStatusInClusters := util.TransformToMap(sc.Spec.ShardSpec.ClusterSpecList, clusterSpecItemToClusterNameMembers)

	assert.Equal(t, expectedMongosSizeStatusInClusters, sc.Status.SizeStatusInClusters.MongosCountInClusters)
	assert.Equal(t, expectedShardSizeStatusInClusters, sc.Status.SizeStatusInClusters.ShardMongodsInClusters)
	assert.Equal(t, expectedConfigSrvSizeStatusInClusters, sc.Status.SizeStatusInClusters.ConfigServerMongodsInClusters)

	clusterSpecItemToMembers := func(item mdbv1.ClusterSpecItem) int {
		return item.Members
	}
	assert.Equal(t, sumSlice(util.Transform(sc.Spec.MongosSpec.ClusterSpecList, clusterSpecItemToMembers)), sc.Status.MongodbShardedClusterSizeConfig.MongosCount)
	assert.Equal(t, sumSlice(util.Transform(sc.Spec.ConfigSrvSpec.ClusterSpecList, clusterSpecItemToMembers)), sc.Status.MongodbShardedClusterSizeConfig.ConfigServerCount)
	assert.Equal(t, sumSlice(util.Transform(sc.Spec.ShardSpec.ClusterSpecList, clusterSpecItemToMembers)), sc.Status.MongodbShardedClusterSizeConfig.MongodsPerShardCount)
}

func sumSlice[T constraints.Integer](s []T) int {
	result := 0
	for i := range s {
		result += int(s[i])
	}
	return result
}

func generateHostsWithDistribution(stsName string, namespace string, distribution map[string]int, clusterIndexMapping map[string]int) ([]string, []string) {
	var hosts []string
	var podNames []string
	for memberClusterName, memberCount := range distribution {
		for podIdx := range memberCount {
			hosts = append(hosts, getMultiClusterFQDN(stsName, namespace, clusterIndexMapping[memberClusterName], podIdx, "cluster.local"))
			podNames = append(podNames, getPodName(stsName, clusterIndexMapping[memberClusterName], podIdx))
		}
	}

	return podNames, hosts
}

func getPodName(stsName string, clusterIdx int, podIdx int) string {
	return fmt.Sprintf("%s-%d-%d", stsName, clusterIdx, podIdx)
}

func getMultiClusterFQDN(stsName string, namespace string, clusterIdx int, podIdx int, clusterDomain string) string {
	return fmt.Sprintf("%s-svc.%s.svc.%s", getPodName(stsName, clusterIdx, podIdx), namespace, clusterDomain)
}

func generateExpectedDeploymentState(t *testing.T, sc *mdbv1.MongoDB) string {
	lastSpec, _ := sc.GetLastSpec()
	expectedState := ShardedClusterDeploymentState{
		CommonDeploymentState: CommonDeploymentState{
			ClusterMapping: map[string]int{},
		},
		LastAchievedSpec: lastSpec,
		Status:           &sc.Status,
	}
	lastSpecBytes, err := json.Marshal(expectedState)
	require.NoError(t, err)
	return string(lastSpecBytes)
}

func loadMongoDBResource(resourceYamlPath string) (*mdbv1.MongoDB, error) {
	mdbBytes, err := os.ReadFile(resourceYamlPath)
	if err != nil {
		return nil, err
	}

	mdb := mdbv1.MongoDB{}
	if err := yaml.Unmarshal(mdbBytes, &mdb); err != nil {
		return nil, err
	}
	return &mdb, nil
}

func unmarshalYamlFileInStruct[T *mdbv1.ShardedClusterComponentSpec | map[int]*mdbv1.ShardedClusterComponentSpec](path string) (*T, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	componentSpecStruct := new(T)
	if err := yaml.Unmarshal(bytes, &componentSpecStruct); err != nil {
		return nil, err
	}
	return componentSpecStruct, nil
}

func loadExpectedReplicaSets(path string) (map[string]any, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var ac map[string]any
	if err := yaml.Unmarshal(bytes, &ac); err != nil {
		return nil, err
	}
	return ac, nil
}

func normalizeObjectToInterfaceMap(obj any) (map[string]interface{}, error) {
	objJson, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{}
	err = json.Unmarshal(objJson, &result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func visualJsonDiffOfAnyObjects(t *testing.T, expectedObj any, actualObj any) string {
	normalizedExpectedObj, err := normalizeObjectToInterfaceMap(expectedObj)
	require.NoError(t, err)
	normalizedActualObj, err := normalizeObjectToInterfaceMap(actualObj)
	require.NoError(t, err)

	visualDiff, err := getVisualJsonDiff(normalizedExpectedObj, normalizedActualObj)
	require.NoError(t, err)

	return visualDiff
}

func getVisualJsonDiff(expectedMap map[string]interface{}, actualMap map[string]interface{}) (string, error) {
	differ := gojsondiff.New()
	diff := differ.CompareObjects(expectedMap, actualMap)
	if !diff.Modified() {
		fmt.Println("No diffs found")
		return "", nil
	}
	jsonFormatter := formatter.NewAsciiFormatter(expectedMap, formatter.AsciiFormatterConfig{
		ShowArrayIndex: false,
		Coloring:       true,
	})

	diffString, err := jsonFormatter.Format(diff)
	if err != nil {
		return "", err
	}

	return diffString, nil
}
