package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// testCA is a CA with a key, so it can sign leaf certificates.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// pem is the PEM encoded certificate, newline-terminated
	pem string
}

func newTestCA(t *testing.T, commonName string) *testCA {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func randomSerial(t *testing.T) *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	require.NoError(t, err)
	return serial
}

func (ca *testCA) fingerprint() string {
	sum := sha256.Sum256(ca.cert.Raw)
	return hex.EncodeToString(sum[:])
}

// issueLeaf returns the PEM of a leaf certificate signed by the CA.
func (ca *testCA) issueLeaf(t *testing.T, commonName string) []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: randomSerial(t),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// expectedBundle is the bundle the operator publishes for the CAs: the PEM of each CA, ordered by fingerprint.
func expectedBundle(cas ...*testCA) string {
	sorted := slices.Clone(cas)
	slices.SortFunc(sorted, func(a, b *testCA) int { return strings.Compare(a.fingerprint(), b.fingerprint()) })
	var bundle strings.Builder
	for _, ca := range sorted {
		bundle.WriteString(ca.pem)
	}
	return bundle.String()
}

func hashOfBundle(bundle string) string { return enterprisepem.HashBytes([]byte(bundle)) }

func setSecret(t *testing.T, c kubernetesClient.Client, name string, data map[string][]byte) {
	secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resNamespace}, Data: data}
	if err := c.CreateSecret(context.Background(), secret); err != nil {
		require.NoError(t, c.UpdateSecret(context.Background(), secret))
	}
}

func setConfigMap(t *testing.T, c kubernetesClient.Client, name string, data map[string]string) {
	cm := configmap.Builder().SetName(name).SetNamespace(resNamespace).SetData(data).Build()
	require.NoError(t, configmap.CreateOrUpdate(context.Background(), c, cm))
}

var (
	serverCMKey = types.NamespacedName{Name: ManagedServerCABundleConfigMapName(resName), Namespace: resNamespace}
	clientCMKey = types.NamespacedName{Name: ManagedClientCABundleConfigMapName(resName), Namespace: resNamespace}
)

func ensureBundles(t *testing.T, c kubernetesClient.Client, owner CertificateOwner, opts Options, hasClientCerts bool) workflow.Status {
	t.Helper()
	status := EnsureCAConfigMapFromIssuedSecret(context.Background(), c, owner, []Options{opts}, hasClientCerts, zap.S())
	require.True(t, status.IsOK(), status.Message())
	return status
}

// latestBundle returns the bundle the ConfigMap is currently publishing under its latestHash key.
func latestBundle(t *testing.T, c kubernetesClient.Client, key types.NamespacedName) string {
	t.Helper()
	cm, err := c.GetConfigMap(context.Background(), key)
	require.NoError(t, err)
	require.NotEmpty(t, cm.Data[util.LatestHashSecretKey], "latestHash missing")
	return cm.Data[cm.Data[util.LatestHashSecretKey]]
}

// setCategoryLeaves writes the issued leaf secrets of both categories, all signed by the given CAs.
func setCategoryLeaves(t *testing.T, c kubernetesClient.Client, owner CertificateOwner, opts Options, serverCA, clientCA *testCA) {
	setSecret(t, c, opts.CertSecretName, map[string][]byte{corev1.TLSCertKey: serverCA.issueLeaf(t, "member")})
	setSecret(t, c, opts.InternalClusterSecretName, map[string][]byte{corev1.TLSCertKey: clientCA.issueLeaf(t, "clusterfile")})
	setSecret(t, c, owner.GetSecurity().AgentClientCertificateSecretName(resName), map[string][]byte{corev1.TLSCertKey: clientCA.issueLeaf(t, "agent")})
}

// rotateSelfSignedCA plays cert-manager reissuing a category's self-signed CA Certificate: both ca.crt
// and tls.crt of the CA secret are replaced with the new root.
func rotateSelfSignedCA(t *testing.T, c kubernetesClient.Client, owner CertificateOwner, cat certCategory, ca *testCA) {
	t.Helper()
	setSecret(t, c, managedCACertName(owner, cat), map[string][]byte{
		corev1.ServiceAccountRootCAKey: []byte(ca.pem),
		corev1.TLSCertKey:              []byte(ca.pem),
	})
}

// rotatedFixture is a resource whose server and client CAs were rotated once, with every leaf reissued
// by the new CAs. The published bundle therefore holds both the old and the new CAs of each category.
func rotatedFixture(t *testing.T, serverCA1, serverCA2, clientCA1, clientCA2 *testCA) (*mdbv1.MongoDB, Options, kubernetesClient.Client) {
	c, _ := mock.NewDefaultFakeClient()
	owner, opts, setCAs := managedCATestFixture(t, c)
	opts.InternalClusterSecretName = resName + "-clusterfile"

	setCAs(serverCA1.pem, clientCA1.pem)
	setCategoryLeaves(t, c, owner, opts, serverCA1, clientCA1)
	ensureBundles(t, c, owner, opts, true)

	setCAs(serverCA2.pem, clientCA2.pem)
	setCategoryLeaves(t, c, owner, opts, serverCA2, clientCA2)
	ensureBundles(t, c, owner, opts, true)

	return owner, opts, c
}

// userIssuerFixture is a resource whose server category is signed by a user issuer. The issuer emits
// its CA in ca.crt of the issued secret unless emitsCA is false, then the CA comes from the
// "user-ca" ConfigMap (server.ca). additionalCAs are read from ConfigMaps named after the index.
func userIssuerFixture(t *testing.T, emitsCA bool, additionalCAs ...*testCA) (*mdbv1.MongoDB, Options, kubernetesClient.Client, func(current *testCA)) {
	c, _ := mock.NewDefaultFakeClient()
	owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
		cfg := &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "my-issuer", Kind: "Issuer"}}
		if !emitsCA {
			cfg.CA = &mdbv1.CARef{Name: "user-ca", Kind: "ConfigMap", Key: "ca.crt"}
		}
		for i, ca := range additionalCAs {
			name := "additional-" + string(rune('a'+i))
			setConfigMap(t, c, name, map[string]string{"ca.crt": ca.pem})
			cfg.AdditionalTrustedCAs = append(cfg.AdditionalTrustedCAs, mdbv1.CARef{Name: name, Kind: "ConfigMap", Key: "ca.crt"})
		}
		m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{Server: cfg}
	})
	opts := Options{ResourceName: resName, CertSecretName: resName + "-cert", Namespace: resNamespace}

	// setCurrentCA plays the issuer: the issuer's CA changes, and the leaf is signed by it
	setCurrentCA := func(current *testCA) {
		data := map[string][]byte{corev1.TLSCertKey: current.issueLeaf(t, "leaf")}
		if emitsCA {
			data[corev1.ServiceAccountRootCAKey] = []byte(current.pem)
		} else {
			setConfigMap(t, c, "user-ca", map[string]string{"ca.crt": current.pem})
		}
		setSecret(t, c, opts.CertSecretName, data)
	}
	return owner, opts, c, setCurrentCA
}

func TestCABundleRetentionOnRotation(t *testing.T) {
	ctx := context.Background()
	serverCA1, serverCA2 := newTestCA(t, "server-ca-1"), newTestCA(t, "server-ca-2")
	clientCA1, clientCA2 := newTestCA(t, "client-ca-1"), newTestCA(t, "client-ca-2")

	t.Run("a rotated CA is retained next to the new one, with no extra ConfigMap state", func(t *testing.T) {
		_, _, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)

		hashesServer, hashesClient, err := ReadManagedCABundleHashes(ctx, c, resName, resNamespace, true)
		require.NoError(t, err)
		serverBundle, clientBundle := expectedBundle(serverCA1, serverCA2), expectedBundle(clientCA1, clientCA2)
		assert.Equal(t, hashOfBundle(serverBundle), hashesServer)
		assert.Equal(t, hashOfBundle(clientBundle), hashesClient)

		serverCM, err := c.GetConfigMap(ctx, serverCMKey)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			hashOfBundle(serverBundle):  serverBundle,
			util.LatestHashSecretKey:    hashOfBundle(serverBundle),
			util.PreviousHashSecretKey:  hashOfBundle(serverCA1.pem),
			hashOfBundle(serverCA1.pem): serverCA1.pem,
			tls.CAConfigMapKey:          serverBundle,
		}, serverCM.Data)
		assert.Empty(t, serverCM.Annotations, "the operator keeps no control state on the ConfigMap")

		clientCM, err := c.GetConfigMap(ctx, clientCMKey)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			hashOfBundle(clientBundle):  clientBundle,
			util.LatestHashSecretKey:    hashOfBundle(clientBundle),
			util.PreviousHashSecretKey:  hashOfBundle(clientCA1.pem),
			hashOfBundle(clientCA1.pem): clientCA1.pem,
		}, clientCM.Data)
		assert.Empty(t, clientCM.Annotations)
	})

	t.Run("the same two CAs give the same hash via additionalTrustedCAs or retention", func(t *testing.T) {
		caOne, caTwo := newTestCA(t, "ca-one"), newTestCA(t, "ca-two")
		both := expectedBundle(caOne, caTwo)

		// widened explicitly through additionalTrustedCAs
		ownerWidened, optsWidened, cWidened, setCurrentCAWidened := userIssuerFixture(t, true, caTwo)
		setCurrentCAWidened(caOne)
		ensureBundles(t, cWidened, ownerWidened, optsWidened, false)
		widenedHashServer, _, err := ReadManagedCABundleHashes(ctx, cWidened, resName, resNamespace, false)
		require.NoError(t, err)
		assert.Equal(t, both, latestBundle(t, cWidened, serverCMKey))

		// retained by the operator after switching the issuer from caOne to caTwo
		ownerRetained, optsRetained, cRetained, setCurrentCARetained := userIssuerFixture(t, true)
		setCurrentCARetained(caOne)
		ensureBundles(t, cRetained, ownerRetained, optsRetained, false)
		setCurrentCARetained(caTwo)
		ensureBundles(t, cRetained, ownerRetained, optsRetained, false)
		retainedHashServer, _, err := ReadManagedCABundleHashes(ctx, cRetained, resName, resNamespace, false)
		require.NoError(t, err)

		assert.Equal(t, both, latestBundle(t, cRetained, serverCMKey))
		assert.Equal(t, widenedHashServer, retainedHashServer)
		assert.Equal(t, 2, strings.Count(latestBundle(t, cRetained, serverCMKey), "BEGIN CERTIFICATE"), "the CAs are deduplicated")
	})

	t.Run("a category that did not rotate keeps its bundle", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner, opts, setCAs := managedCATestFixture(t, c)
		setCAs(serverCA1.pem, clientCA1.pem)
		ensureBundles(t, c, owner, opts, true)

		setCAs(serverCA2.pem, clientCA1.pem)
		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, expectedBundle(serverCA1, serverCA2), latestBundle(t, c, serverCMKey))
		assert.Equal(t, clientCA1.pem, latestBundle(t, c, clientCMKey))
	})
}

func TestPrunePreviousCA(t *testing.T) {
	ctx := context.Background()
	serverCA1, serverCA2 := newTestCA(t, "server-ca-1"), newTestCA(t, "server-ca-2")
	clientCA1, clientCA2 := newTestCA(t, "client-ca-1"), newTestCA(t, "client-ca-2")

	t.Run("true drops the previous CA of both categories when no leaf is signed by it", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "true"}

		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, serverCA2.pem, latestBundle(t, c, serverCMKey))
		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey))
		hashesServer, hashesClient, err := ReadManagedCABundleHashes(ctx, c, resName, resNamespace, true)
		require.NoError(t, err)
		assert.Equal(t, hashOfBundle(serverCA2.pem), hashesServer)
		assert.Equal(t, hashOfBundle(clientCA2.pem), hashesClient)
	})

	t.Run("the prune is refused while a leaf is signed by the previous CA", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "true"}
		// the clusterFile certificate has not been reissued yet, it is still signed by the previous client CA
		setSecret(t, c, opts.InternalClusterSecretName, map[string][]byte{corev1.TLSCertKey: clientCA1.issueLeaf(t, "clusterfile")})

		status := ensureBundles(t, c, owner, opts, true)
		assert.True(t, status.IsOK(), "a refused prune does not fail the reconcile")

		assert.Equal(t, serverCA2.pem, latestBundle(t, c, serverCMKey))
		assert.Equal(t, expectedBundle(clientCA1, clientCA2), latestBundle(t, c, clientCMKey), "the old client CA is kept")
	})

	t.Run("server prunes the server category only", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "server"}

		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, serverCA2.pem, latestBundle(t, c, serverCMKey))
		assert.Equal(t, expectedBundle(clientCA1, clientCA2), latestBundle(t, c, clientCMKey))
	})

	t.Run("client prunes the client category only", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "client"}

		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, expectedBundle(serverCA1, serverCA2), latestBundle(t, c, serverCMKey))
		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey))
	})

	t.Run("an unknown annotation value does not prune", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "yes"}

		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, expectedBundle(serverCA1, serverCA2), latestBundle(t, c, serverCMKey))
		assert.Equal(t, expectedBundle(clientCA1, clientCA2), latestBundle(t, c, clientCMKey))
	})

	t.Run("a leaf secret that cannot be read fails the reconcile", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "server"}
		require.NoError(t, c.DeleteSecret(ctx, types.NamespacedName{Name: opts.CertSecretName, Namespace: resNamespace}))

		status := EnsureCAConfigMapFromIssuedSecret(ctx, c, owner, []Options{opts}, true, zap.S())
		assert.False(t, status.IsOK())
		assert.Contains(t, status.Message(), "could not read certificate secret")
	})
}

// TestSelfSignedCARotationRetention covers CA rotation for a category signed by the operator's own
// self-signed CA (no issuerRef configured, the CA read from the "<name>-<cat>-ca" secret). cert-manager
// reissuing that root must go through the same retention and prune path as a user issuer CA rotation.
func TestSelfSignedCARotationRetention(t *testing.T) {
	ctx := context.Background()
	serverCA1, serverCA2, serverCA3 := newTestCA(t, "server-ca-1"), newTestCA(t, "server-ca-2"), newTestCA(t, "server-ca-3")
	clientCA1, clientCA2 := newTestCA(t, "client-ca-1"), newTestCA(t, "client-ca-2")

	t.Run("a cert-manager reissued self-signed root is bundled with the new root", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner, opts, setCAs := managedCATestFixture(t, c)
		opts.InternalClusterSecretName = resName + "-clusterfile"
		setCAs(serverCA1.pem, clientCA1.pem)
		setCategoryLeaves(t, c, owner, opts, serverCA1, clientCA1)
		ensureBundles(t, c, owner, opts, true)

		rotateSelfSignedCA(t, c, owner, categoryServer, serverCA2)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)
		setCategoryLeaves(t, c, owner, opts, serverCA2, clientCA2)
		ensureBundles(t, c, owner, opts, true)

		serverBundle, clientBundle := expectedBundle(serverCA1, serverCA2), expectedBundle(clientCA1, clientCA2)
		assert.Equal(t, serverBundle, latestBundle(t, c, serverCMKey))
		assert.Equal(t, clientBundle, latestBundle(t, c, clientCMKey))

		serverCM, err := c.GetConfigMap(ctx, serverCMKey)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			hashOfBundle(serverBundle):  serverBundle,
			util.LatestHashSecretKey:    hashOfBundle(serverBundle),
			util.PreviousHashSecretKey:  hashOfBundle(serverCA1.pem),
			hashOfBundle(serverCA1.pem): serverCA1.pem,
			tls.CAConfigMapKey:          serverBundle,
		}, serverCM.Data)
		assert.Empty(t, serverCM.Annotations, "the operator keeps no control state on the ConfigMap")

		clientCM, err := c.GetConfigMap(ctx, clientCMKey)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			hashOfBundle(clientBundle):  clientBundle,
			util.LatestHashSecretKey:    hashOfBundle(clientBundle),
			util.PreviousHashSecretKey:  hashOfBundle(clientCA1.pem),
			hashOfBundle(clientCA1.pem): clientCA1.pem,
		}, clientCM.Data)
		assert.Empty(t, clientCM.Annotations)
	})

	t.Run("true is refused while a leaf is still signed by the old self-signed root", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner, opts, setCAs := managedCATestFixture(t, c)
		opts.InternalClusterSecretName = resName + "-clusterfile"
		setCAs(serverCA1.pem, clientCA1.pem)
		setCategoryLeaves(t, c, owner, opts, serverCA1, clientCA1)
		ensureBundles(t, c, owner, opts, true)

		rotateSelfSignedCA(t, c, owner, categoryServer, serverCA2)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)
		// only the client leaves are reissued; the member cert is still signed by the old server root
		setSecret(t, c, opts.InternalClusterSecretName, map[string][]byte{corev1.TLSCertKey: clientCA2.issueLeaf(t, "clusterfile")})
		setSecret(t, c, owner.GetSecurity().AgentClientCertificateSecretName(resName), map[string][]byte{corev1.TLSCertKey: clientCA2.issueLeaf(t, "agent")})
		ensureBundles(t, c, owner, opts, true)

		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "true"}
		status := ensureBundles(t, c, owner, opts, true)
		assert.True(t, status.IsOK(), "a refused prune does not fail the reconcile")

		assert.Equal(t, expectedBundle(serverCA1, serverCA2), latestBundle(t, c, serverCMKey), "the old server root is kept")
		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey), "the client category has no leaf left on the old root")
	})

	t.Run("true prunes the old self-signed root once every leaf uses the new root", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner, opts, setCAs := managedCATestFixture(t, c)
		opts.InternalClusterSecretName = resName + "-clusterfile"
		setCAs(serverCA1.pem, clientCA1.pem)
		setCategoryLeaves(t, c, owner, opts, serverCA1, clientCA1)
		ensureBundles(t, c, owner, opts, true)

		rotateSelfSignedCA(t, c, owner, categoryServer, serverCA2)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)
		setCategoryLeaves(t, c, owner, opts, serverCA2, clientCA2)
		ensureBundles(t, c, owner, opts, true)

		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "true"}
		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, serverCA2.pem, latestBundle(t, c, serverCMKey))
		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey))
		hashesServer, hashesClient, err := ReadManagedCABundleHashes(ctx, c, resName, resNamespace, true)
		require.NoError(t, err)
		assert.Equal(t, hashOfBundle(serverCA2.pem), hashesServer)
		assert.Equal(t, hashOfBundle(clientCA2.pem), hashesClient)
	})

	t.Run("a second root rotation before pruning keeps every previously published root, not only the previous one", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner, opts, setCAs := managedCATestFixture(t, c)
		setCAs(serverCA1.pem, clientCA1.pem)
		ensureBundles(t, c, owner, opts, true)

		rotateSelfSignedCA(t, c, owner, categoryServer, serverCA2)
		ensureBundles(t, c, owner, opts, true)

		rotateSelfSignedCA(t, c, owner, categoryServer, serverCA3)
		ensureBundles(t, c, owner, opts, true)

		// previousCAs keeps everything in the bundle that is not the current CA, so
		// both earlier roots stay until a prune (the ConfigMap keeps only the two newest hashes, but
		// the latest bundle itself still carries all three roots).
		bundle := latestBundle(t, c, serverCMKey)
		assert.Equal(t, expectedBundle(serverCA1, serverCA2, serverCA3), bundle)
		assert.Equal(t, 3, strings.Count(bundle, "BEGIN CERTIFICATE"))
		assert.Equal(t, clientCA1.pem, latestBundle(t, c, clientCMKey))
	})

	t.Run("a client-only prune leaves the retained server roots in place", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner, opts, setCAs := managedCATestFixture(t, c)
		opts.InternalClusterSecretName = resName + "-clusterfile"
		setCAs(serverCA1.pem, clientCA1.pem)
		setCategoryLeaves(t, c, owner, opts, serverCA1, clientCA1)
		ensureBundles(t, c, owner, opts, true)

		rotateSelfSignedCA(t, c, owner, categoryServer, serverCA2)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)
		setCategoryLeaves(t, c, owner, opts, serverCA2, clientCA2)
		ensureBundles(t, c, owner, opts, true)
		assert.Equal(t, expectedBundle(serverCA1, serverCA2), latestBundle(t, c, serverCMKey))
		assert.Equal(t, expectedBundle(clientCA1, clientCA2), latestBundle(t, c, clientCMKey))

		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "client"}
		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, expectedBundle(serverCA1, serverCA2), latestBundle(t, c, serverCMKey), "the server bundle is untouched")
		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey))
	})

	t.Run("a removed additionalTrustedCAs entry stays in the bundle until pruned, the bundle only shrinks on prune", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner, opts, setCAs := managedCATestFixture(t, c)
		additionalCA := newTestCA(t, "additional-ca")
		setConfigMap(t, c, "additional-ca", map[string]string{"ca.crt": additionalCA.pem})
		owner.Spec.Security.ManagedCertificate.Server = &mdbv1.CertificateConfig{
			AdditionalTrustedCAs: []mdbv1.CARef{{Name: "additional-ca", Kind: "ConfigMap", Key: "ca.crt"}},
		}
		setCAs(serverCA1.pem, clientCA1.pem)
		setSecret(t, c, opts.CertSecretName, map[string][]byte{corev1.TLSCertKey: serverCA1.issueLeaf(t, "member")})
		ensureBundles(t, c, owner, opts, false)
		assert.Equal(t, expectedBundle(serverCA1, additionalCA), latestBundle(t, c, serverCMKey))

		owner.Spec.Security.ManagedCertificate.Server = nil
		ensureBundles(t, c, owner, opts, false)
		assert.Equal(t, expectedBundle(serverCA1, additionalCA), latestBundle(t, c, serverCMKey), "removing the source does not shrink the bundle")

		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "true"}
		ensureBundles(t, c, owner, opts, false)
		assert.Equal(t, serverCA1.pem, latestBundle(t, c, serverCMKey), "the removed CA is pruned")
	})
}

func TestParseAndMergeCACerts(t *testing.T) {
	caA, caB := newTestCA(t, "a"), newTestCA(t, "b")

	t.Run("a PEM with several certificates is parsed, other blocks and text are skipped", func(t *testing.T) {
		parsed, err := parseCACerts("some text\n" + caA.pem + "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n" + caB.pem)
		require.NoError(t, err)
		assert.Equal(t, []string{caA.fingerprint(), caB.fingerprint()}, []string{parsed[0].fingerprint, parsed[1].fingerprint})
	})

	t.Run("a PEM with no certificate is rejected", func(t *testing.T) {
		for _, data := range []string{
			"nothing here",
			"",
			"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		} {
			_, err := parseCACerts(data)
			assert.Error(t, err, "data: %q", data)
		}
	})

	t.Run("the bundle is deduplicated, ordered by fingerprint and independent of the input order", func(t *testing.T) {
		a, _ := parseCACerts(caA.pem)
		b, _ := parseCACerts(caB.pem)
		assert.Equal(t, expectedBundle(caA, caB), mergeCABundle(a, b, a))
		assert.Equal(t, expectedBundle(caA, caB), mergeCABundle(b, a))
	})

	t.Run("fingerprints are the SHA-256 of the DER, sorted and unique", func(t *testing.T) {
		both, _ := parseCACerts(caA.pem + caB.pem + caA.pem)
		fingerprints := []string{caA.fingerprint(), caB.fingerprint()}
		slices.Sort(fingerprints)
		assert.Equal(t, fingerprints, caFingerprints(both))
	})
}

func TestPreviousCAs(t *testing.T) {
	caA, caB, caC, caD := newTestCA(t, "a"), newTestCA(t, "b"), newTestCA(t, "c"), newTestCA(t, "d")
	parse := func(cas ...*testCA) []caCert {
		var parsed []caCert
		for _, ca := range cas {
			certs, err := parseCACerts(ca.pem)
			require.NoError(t, err)
			parsed = append(parsed, certs...)
		}
		return parsed
	}

	tests := []struct {
		name       string
		bundleCAs  []caCert
		current    []caCert
		additional []caCert
		expected   []caCert
	}{
		{name: "no bundle yet", current: parse(caA)},
		{name: "the bundle has only the current CA", bundleCAs: parse(caA), current: parse(caA)},
		{name: "the previous CA is kept", bundleCAs: parse(caA), current: parse(caB), expected: parse(caA)},
		{name: "only the bundle CA that is not current is kept", bundleCAs: parse(caA, caB), current: parse(caB), expected: parse(caA)},
		{name: "every bundle CA is kept when none is current", bundleCAs: parse(caA, caB), current: parse(caC), expected: parse(caA, caB)},
		{name: "a current CA that is not in the bundle adds nothing", bundleCAs: parse(caA), current: parse(caA, caB)},
		{name: "a configured additional CA is not a previous CA", bundleCAs: parse(caA, caD), current: parse(caA), additional: parse(caD)},
		{name: "an additional CA removed from the spec is kept", bundleCAs: parse(caA, caD), current: parse(caA), expected: parse(caD)},
		{name: "the previous CA is kept next to a configured additional CA", bundleCAs: parse(caA, caD), current: parse(caB), additional: parse(caD), expected: parse(caA)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, previousCAs(tc.bundleCAs, tc.current, tc.additional))
		})
	}
}
