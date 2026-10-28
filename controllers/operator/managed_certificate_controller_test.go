package operator

import (
	"context"
	"crypto/x509"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// issueManagedCertificates plays cert-manager: it marks every Certificate in the namespace as Ready and
// creates the secret it points at, with a fresh keypair. Existing secrets are left as they are.
func issueManagedCertificates(ctx context.Context, t *testing.T, c kubernetesClient.Client, namespace string) {
	certList := &certmanagerv1.CertificateList{}
	require.NoError(t, c.List(ctx, certList))
	for i := range certList.Items {
		cert := &certList.Items[i]
		cert.Status.Conditions = []certmanagerv1.CertificateCondition{{
			Type:               certmanagerv1.CertificateConditionReady,
			Status:             cmmeta.ConditionTrue,
			ObservedGeneration: cert.Generation,
		}}
		require.NoError(t, c.Update(ctx, cert))

		crt, key := createMockCertAndKeyBytes(func(cert *x509.Certificate) {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
		})
		ca, _ := createMockCertAndKeyBytes()
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cert.Spec.SecretName, Namespace: namespace},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key, corev1.ServiceAccountRootCAKey: ca},
		}
		if err := c.Create(ctx, secret); err != nil {
			require.True(t, apiErrors.IsAlreadyExists(err), "unexpected error: %v", err)
		}
	}
}

func TestDatabaseCAFilePaths(t *testing.T) {
	ctx := context.Background()
	const name = "mdb"
	kubeClient, _ := mock.NewDefaultFakeClient()

	serverCM := configmap.Builder().SetName(certs.ManagedServerCABundleConfigMapName(name)).SetNamespace(mock.TestNamespace).
		SetData(map[string]string{util.LatestHashSecretKey: "SERVERHASH", "SERVERHASH": "bundle"}).Build()
	clientCM := configmap.Builder().SetName(certs.ManagedClientCABundleConfigMapName(name)).SetNamespace(mock.TestNamespace).
		SetData(map[string]string{util.LatestHashSecretKey: "CLIENTHASH", "CLIENTHASH": "bundle"}).Build()
	require.NoError(t, kubeClient.CreateConfigMap(ctx, serverCM))
	require.NoError(t, kubeClient.CreateConfigMap(ctx, clientCM))

	x509 := &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}}

	t.Run("non-managed keeps the fixed ca-pem path and no cluster CA", func(t *testing.T) {
		ca, clusterCA, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, path.Join(util.TLSCaMountPath, "ca-pem"), ca)
		assert.Empty(t, clusterCA)
	})

	t.Run("non-managed honours the user caFilePath", func(t *testing.T) {
		ca, _, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true, CAFilePath: "/custom/ca"}}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, "/custom/ca", ca)
	})

	t.Run("managed uses the server bundle hash, no cluster CA without client certs", func(t *testing.T) {
		ca, clusterCA, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, util.TLSCaMountPath+"/SERVERHASH", ca)
		assert.Empty(t, clusterCA)
	})

	t.Run("managed with client certs also uses the client bundle hash", func(t *testing.T) {
		ca, clusterCA, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}, Authentication: x509}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, util.TLSCaMountPath+"/SERVERHASH", ca)
		assert.Equal(t, util.TLSClusterCaMountPath+"/CLIENTHASH", clusterCA)
	})

	t.Run("managed fails when the bundles are not published", func(t *testing.T) {
		_, _, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}, "other", mock.TestNamespace)
		assert.Error(t, err)
	})
}

// TestReplicaSetManagedCertificate_CAFilePathFollowsBundleHash checks that the automation config
// CA paths are the hash-named bundle files, and that they change when the CA bundle changes.
func TestReplicaSetManagedCertificate_CAFilePathFollowsBundleHash(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(1).SetSecurity(mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
		Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}, InternalCluster: util.X509},
	}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, omConnectionFactory.GetConnectionFunc, certs.CertManagerReadiness{Ready: true})
	addKubernetesTlsResources(ctx, kubeClient, rs)

	// the first reconcile creates the Certificates, then cert-manager issues them
	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, rs.Namespace)

	reconcileAndReadTLS := func() map[string]any {
		_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
		require.NoError(t, err)
		require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
		return omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	}

	tlsConfig := reconcileAndReadTLS()
	serverHash, clientHash, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	require.NotEmpty(t, serverHash)
	require.NotEmpty(t, clientHash)
	assert.Equal(t, util.TLSCaMountPath+"/"+serverHash, tlsConfig["CAFilePath"])
	assert.Equal(t, util.TLSClusterCaMountPath+"/"+clientHash, tlsConfig["clusterCAFilePath"])

	// the issuer's CA is rotated, the bundle gets a new hash and the automation config follows
	serverCASecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-server-ca"))
	require.NoError(t, err)
	newCA, _ := createMockCertAndKeyBytes()
	serverCASecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, serverCASecret))

	tlsConfig = reconcileAndReadTLS()
	newServerHash, newClientHash, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	assert.Equal(t, enterprisepem.HashBytes([]byte(strings.TrimRight(string(newCA), "\n")+"\n")), newServerHash)
	assert.NotEqual(t, serverHash, newServerHash)
	assert.Equal(t, clientHash, newClientHash)
	assert.Equal(t, util.TLSCaMountPath+"/"+newServerHash, tlsConfig["CAFilePath"])
	assert.Equal(t, util.TLSClusterCaMountPath+"/"+clientHash, tlsConfig["clusterCAFilePath"])
}

// TestShardedClusterManagedCertificate_CAFilePathIsBundleHash checks that the published automation
// config of a sharded cluster points at the hash-named server CA bundle.
func TestShardedClusterManagedCertificate_CAFilePathIsBundleHash(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().SetSecurity(mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	reconciler := newShardedClusterReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, nil, omConnectionFactory.GetConnectionFunc, testBackupEnableDelay, certs.CertManagerReadiness{Ready: true})

	// the first reconcile creates the Certificates, then cert-manager issues them
	_, err := reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, sc.Namespace)

	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)

	serverHash, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, sc.Name, sc.Namespace, false)
	require.NoError(t, err)
	require.NotEmpty(t, serverHash)
	tlsConfig := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	assert.Equal(t, util.TLSCaMountPath+"/"+serverHash, tlsConfig["CAFilePath"])
	assert.NotContains(t, tlsConfig, "clusterCAFilePath")
}

// TestMultiReplicaSetManagedCertificate_CAFilePathIsBundleHash checks that the published automation
// config of a multi-cluster replica set points at the hash-named server CA bundle, and that this bundle
// was replicated to the member clusters.
func TestMultiReplicaSetManagedCertificate_CAFilePathIsBundleHash(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		SetSecurity(&mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}).
		Build()

	reconciler, kubeClient, memberClients, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs, architectures.NonStatic)

	// the first reconcile creates the Certificates, then cert-manager issues them
	_, err := reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, mrs.Namespace)

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)

	serverHash, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, mrs.Name, mrs.Namespace, false)
	require.NoError(t, err)
	require.NotEmpty(t, serverHash)
	tlsConfig := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	assert.Equal(t, util.TLSCaMountPath+"/"+serverHash, tlsConfig["CAFilePath"])

	central, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
	require.NoError(t, err)
	for clusterName, memberClient := range memberClients {
		member, err := kubernetesClient.NewClient(memberClient).GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
		require.NoError(t, err, clusterName)
		assert.Equal(t, central.Data, member.Data, clusterName)
		assert.Contains(t, member.Data, serverHash, clusterName)
	}
}
