package operator

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// TestMultiClusterShardedManagedCertificate_RetainsPreviousCAInEveryMemberCluster checks that a
// multi-cluster sharded deployment publishes the hash-named server CA bundle, copies the same bundle
// to every member cluster, and keeps the previous CA in all of those copies when the server CA is
// rotated.
func TestMultiClusterShardedManagedCertificate_RetainsPreviousCAInEveryMemberCluster(t *testing.T) {
	ctx := context.Background()
	memberClusters := test.NewMemberClusters(
		test.MemberClusterDetails{
			ClusterName:           "member-cluster-1",
			ShardMap:              []int{1, 1},
			NumberOfConfigServers: 1,
			NumberOfMongoses:      1,
		},
		test.MemberClusterDetails{
			ClusterName:           "member-cluster-2",
			ShardMap:              []int{1, 1},
			NumberOfConfigServers: 1,
			NumberOfMongoses:      1,
		},
	)
	sc := test.DefaultClusterBuilder().
		WithMultiClusterSetup(memberClusters).
		SetSecurity(mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}).
		Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	memberClients := getFakeMultiClusterMapWithClusters(memberClusters.ClusterNames, omConnectionFactory)
	reconciler := newShardedClusterReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, memberClients, omConnectionFactory.GetConnectionFunc, testBackupEnableDelay, certs.CertManagerReadiness{Ready: true})

	// Register the agents the way the pods would when they start, so that Ops Manager reports the
	// deployment as ready once the StatefulSets are created.
	helper, err := NewShardedClusterReconcilerHelper(ctx, reconciler.ReconcileCommonController, nil, "", "", false, false, false, "", architectures.NonStatic, reconciler.defaultImageType, sc, memberClients, omConnectionFactory.GetConnectionFunc, zap.S(), testBackupEnableDelay, certs.CertManagerReadiness{Ready: true})
	require.NoError(t, err)
	clusterMapping := helper.deploymentState.ClusterMapping
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		allHostnames, _ := generateAllHosts(sc, memberClusters.MongosDistribution, clusterMapping, memberClusters.ConfigServerDistribution, memberClusters.ShardDistribution, test.ClusterLocalDomains, test.ClusterDomains{})
		connection.(*om.MockedOmConnection).AddHosts(allHostnames)
	})

	// the first reconcile creates the Certificates, then cert-manager issues them
	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, sc.Namespace)

	reconcile := func() map[string]any {
		_, err := reconciler.Reconcile(ctx, requestFromObject(sc))
		require.NoError(t, err)
		require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(sc), sc))
		return omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	}
	centralBundle := func() (corev1.ConfigMap, string) {
		cm, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
		require.NoError(t, err)
		return cm, cm.Data[util.LatestHashSecretKey]
	}
	assertMemberBundles := func(central corev1.ConfigMap, hash string) {
		for clusterName, memberClient := range memberClients {
			member, err := kubernetesClient.NewClient(memberClient).GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
			require.NoError(t, err, clusterName)
			assert.Equal(t, central.Data, member.Data, clusterName)
			assert.Contains(t, member.Data, hash, clusterName)
		}
	}

	tlsConfig := reconcile()
	central, hashesServer := centralBundle()
	require.NotEmpty(t, hashesServer)
	assert.Equal(t, util.TLSCaMountPath+"/"+hashesServer, tlsConfig["CAFilePath"])
	assertMemberBundles(central, hashesServer)

	// rotate the central server CA: the previous one is retained and replicated to every member cluster
	caSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(sc.Namespace, sc.Name+"-server-ca"))
	require.NoError(t, err)
	oldCA := caSecret.Data[corev1.ServiceAccountRootCAKey]
	newCA, _ := createMockCertAndKeyBytes()
	caSecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, caSecret))

	tlsConfig = reconcile()
	central, rotatedHash := centralBundle()
	require.NotEmpty(t, rotatedHash)
	assert.NotEqual(t, hashesServer, rotatedHash)
	rotatedBundle := central.Data[rotatedHash]
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is retained")
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(newCA)), "the new CA is published")
	assert.Equal(t, util.TLSCaMountPath+"/"+rotatedHash, tlsConfig["CAFilePath"])
	assertMemberBundles(central, rotatedHash)
}
