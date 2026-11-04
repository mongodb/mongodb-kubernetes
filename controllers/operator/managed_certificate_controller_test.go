package operator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

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

// issueLeafSignedByCA returns a leaf certificate signed by the given CA certificate and key. The leaf
// carries the same organization as the mock certificates, so it also passes the membership subject check.
func issueLeafSignedByCA(t *testing.T, caCertPEM, caKeyPEM []byte, commonName string) []byte {
	t.Helper()
	block, _ := pem.Decode(caCertPEM)
	require.NotNil(t, block)
	caCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	keyBlock, _ := pem.Decode(caKeyPEM)
	require.NotNil(t, keyBlock)
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	require.NoError(t, err)
	caKey, ok := parsedKey.(*ecdsa.PrivateKey)
	require.True(t, ok, "the mock CA key must be an ECDSA key")

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{Organization: []string{"MongoDB"}, CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

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
// CA paths are the hash-named bundle files, that a CA rotation retains the old CA next to the new
// one, and that the prune annotation then drops it.
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

	// replace the CA cert-manager issued with one the test controls, so it can also sign a leaf with it
	serverCASecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-server-ca"))
	require.NoError(t, err)
	oldCA, oldCAKey := createMockCertAndKeyBytes()
	serverCASecret.Data[corev1.ServiceAccountRootCAKey] = oldCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, serverCASecret))

	reconcileAndReadTLS := func() map[string]any {
		_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
		require.NoError(t, err)
		require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
		return omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	}
	serverBundle := func() string {
		cm, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(rs.Namespace, certs.ManagedServerCABundleConfigMapName(rs.Name)))
		require.NoError(t, err)
		return cm.Data[cm.Data[util.LatestHashSecretKey]]
	}

	tlsConfig := reconcileAndReadTLS()
	hashesServer, hashesClient, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	require.NotEmpty(t, hashesServer)
	require.NotEmpty(t, hashesClient)
	assert.Equal(t, util.TLSCaMountPath+"/"+hashesServer, tlsConfig["CAFilePath"])
	assert.Equal(t, util.TLSClusterCaMountPath+"/"+hashesClient, tlsConfig["clusterCAFilePath"])

	// the issuer's CA is rotated: the old CA is retained next to the new one, and the CA path moves
	newCA, newCAKey := createMockCertAndKeyBytes()
	serverCASecret, err = kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-server-ca"))
	require.NoError(t, err)
	serverCASecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, serverCASecret))

	tlsConfig = reconcileAndReadTLS()
	newHashesServer, newHashesClient, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	assert.NotEqual(t, hashesServer, newHashesServer)
	assert.Equal(t, hashesClient, newHashesClient)
	assert.Equal(t, util.TLSCaMountPath+"/"+newHashesServer, tlsConfig["CAFilePath"])
	assert.Equal(t, util.TLSClusterCaMountPath+"/"+hashesClient, tlsConfig["clusterCAFilePath"])
	rotatedBundle := serverBundle()
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is retained after the rotation")
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(newCA)), "the new CA is published")
	assert.Equal(t, enterprisepem.HashBytes([]byte(rotatedBundle)), newHashesServer)

	// the condition tells the user that a previous CA is retained and how to prune it
	certsCondition := certificatesReadyCondition(t, rs)
	assert.Equal(t, metav1.ConditionTrue, certsCondition.Status)
	assert.Contains(t, certsCondition.Message, "Previous server CA retained in the trust bundle")
	assert.Contains(t, certsCondition.Message, util.PrunePreviousCAAnnotation+"=server (or true)")

	// the member certificate is still signed by the old CA: the prune is refused and the annotation stays
	memberSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-managed-cert"))
	require.NoError(t, err)
	memberSecret.Data[corev1.TLSCertKey] = issueLeafSignedByCA(t, oldCA, oldCAKey, "member")
	require.NoError(t, kubeClient.UpdateSecret(ctx, memberSecret))
	if rs.Annotations == nil {
		rs.Annotations = map[string]string{}
	}
	rs.Annotations[util.PrunePreviousCAAnnotation] = "true"
	require.NoError(t, kubeClient.Update(ctx, rs))

	reconcileAndReadTLS()
	assert.Contains(t, serverBundle(), strings.TrimSpace(string(oldCA)), "the previous CA is kept while a leaf is signed by it")
	assert.Equal(t, "true", rs.GetAnnotations()[util.PrunePreviousCAAnnotation], "the annotation stays while the prune is refused")
	certsCondition = certificatesReadyCondition(t, rs)
	assert.Contains(t, certsCondition.Message, "The previous server CA was not pruned: issued certificates are still signed by it.")

	// reissue the member certificate from the new CA, then the operator prunes the previous CA
	memberSecret, err = kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-managed-cert"))
	require.NoError(t, err)
	memberSecret.Data[corev1.TLSCertKey] = issueLeafSignedByCA(t, newCA, newCAKey, "member")
	require.NoError(t, kubeClient.UpdateSecret(ctx, memberSecret))

	tlsConfig = reconcileAndReadTLS()
	prunedHashesServer, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	assert.NotEqual(t, newHashesServer, prunedHashesServer)
	assert.Equal(t, util.TLSCaMountPath+"/"+prunedHashesServer, tlsConfig["CAFilePath"])
	prunedBundle := serverBundle()
	assert.NotContains(t, prunedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is pruned")
	assert.Contains(t, prunedBundle, strings.TrimSpace(string(newCA)))
	assert.Equal(t, enterprisepem.HashBytes([]byte(prunedBundle)), prunedHashesServer)

	// the operator removes the annotation once the prune has been applied
	assert.NotContains(t, rs.GetAnnotations(), util.PrunePreviousCAAnnotation)
	certsCondition = certificatesReadyCondition(t, rs)
	assert.NotContains(t, certsCondition.Message, "Previous server CA retained")
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

	hashesServer, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, sc.Name, sc.Namespace, false)
	require.NoError(t, err)
	require.NotEmpty(t, hashesServer)
	tlsConfig := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	assert.Equal(t, util.TLSCaMountPath+"/"+hashesServer, tlsConfig["CAFilePath"])
	assert.NotContains(t, tlsConfig, "clusterCAFilePath")

	serverCM, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
	require.NoError(t, err)
	assert.Equal(t, hashesServer, serverCM.Data[util.LatestHashSecretKey])
	assert.NotEmpty(t, serverCM.Data[hashesServer])

	// rotate the server CA: the previous one is retained in the bundle
	caSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(sc.Namespace, sc.Name+"-server-ca"))
	require.NoError(t, err)
	oldCA := caSecret.Data[corev1.ServiceAccountRootCAKey]
	newCA, _ := createMockCertAndKeyBytes()
	caSecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, caSecret))

	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)
	serverCM, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
	require.NoError(t, err)
	rotatedBundle := serverCM.Data[serverCM.Data[util.LatestHashSecretKey]]
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)))
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(newCA)))

	// asking to prune removes the previous CA and the annotation
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(sc), sc))
	if sc.Annotations == nil {
		sc.Annotations = map[string]string{}
	}
	sc.Annotations[util.PrunePreviousCAAnnotation] = "true"
	require.NoError(t, kubeClient.Update(ctx, sc))

	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(sc), sc))
	assert.NotContains(t, sc.GetAnnotations(), util.PrunePreviousCAAnnotation)

	serverCM, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
	require.NoError(t, err)
	prunedBundle := serverCM.Data[serverCM.Data[util.LatestHashSecretKey]]
	assert.NotContains(t, prunedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is pruned")
	assert.Contains(t, prunedBundle, strings.TrimSpace(string(newCA)))
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

	hashesServer, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, mrs.Name, mrs.Namespace, false)
	require.NoError(t, err)
	require.NotEmpty(t, hashesServer)
	tlsConfig := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	assert.Equal(t, util.TLSCaMountPath+"/"+hashesServer, tlsConfig["CAFilePath"])

	central, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
	require.NoError(t, err)
	assert.NotEmpty(t, central.Data[hashesServer])
	for clusterName, memberClient := range memberClients {
		member, err := kubernetesClient.NewClient(memberClient).GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
		require.NoError(t, err, clusterName)
		assert.Equal(t, central.Data, member.Data, clusterName)
		assert.Contains(t, member.Data, hashesServer, clusterName)
	}

	// rotate the central server CA: the previous one is retained and replicated to every member cluster
	caSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name+"-server-ca"))
	require.NoError(t, err)
	oldCA := caSecret.Data[corev1.ServiceAccountRootCAKey]
	newCA, _ := createMockCertAndKeyBytes()
	caSecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, caSecret))

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	central, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
	require.NoError(t, err)
	rotatedBundle := central.Data[central.Data[util.LatestHashSecretKey]]
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)))

	// asking to prune removes the previous CA everywhere and the annotation
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(mrs), mrs))
	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}
	mrs.Annotations[util.PrunePreviousCAAnnotation] = "true"
	require.NoError(t, kubeClient.Update(ctx, mrs))

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(mrs), mrs))
	assert.NotContains(t, mrs.GetAnnotations(), util.PrunePreviousCAAnnotation)

	central, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
	require.NoError(t, err)
	prunedBundle := central.Data[central.Data[util.LatestHashSecretKey]]
	assert.NotContains(t, prunedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is pruned")
	assert.Contains(t, prunedBundle, strings.TrimSpace(string(newCA)))
	for clusterName, memberClient := range memberClients {
		member, err := kubernetesClient.NewClient(memberClient).GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
		require.NoError(t, err, clusterName)
		assert.Equal(t, central.Data, member.Data, clusterName)
	}
}

// certificatesReadyCondition returns the CertificatesReady condition recorded on the resource.
func certificatesReadyCondition(t *testing.T, rs *mdbv1.MongoDB) metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(rs.Status.Conditions, certs.ConditionCertificatesReady)
	require.NotNil(t, condition, "CertificatesReady condition is missing")
	return *condition
}
