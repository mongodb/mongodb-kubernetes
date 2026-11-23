package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const legacyCAName = "legacy-ca"

// TestVerifyMigrationTrustsLegacyCA covers the check that refuses a migration to operator-managed
// certificates when the managed trust bundle would not validate the certificates the deployment uses today.
func TestVerifyMigrationTrustsLegacyCA(t *testing.T) {
	ctx := context.Background()
	legacyCA := newTestCA(t, "legacy-ca")
	otherCA := newTestCA(t, "other-ca")
	// A certificate the user's CA file can hold besides the CA that signs the deployment's certificates,
	// such as a public CA, which the members do not need.
	unrelatedCA := newTestCA(t, "unrelated-ca")

	// The deployment trusts the user's CA today, and its certificates are signed by it.
	lastAchieved := &mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true, CA: legacyCAName},
	}
	memberSecret := lastAchieved.MemberCertificateSecretName(resName)
	clusterfileSecret := lastAchieved.InternalClusterAuthSecretName(resName)
	componentNames := []string{resName}

	// setup returns a client with the user's CA file and the user's member and clusterfile certificates.
	setup := func(t *testing.T, caFile string, member, clusterfile []byte) kubernetesClient.Client {
		c := newMigrationCAClient(t, map[string]string{legacyCAName: caFile})
		putLegacyCertificate(t, c, memberSecret, member)
		putLegacyCertificate(t, c, clusterfileSecret, clusterfile)
		return c
	}
	bothBundles := func(t *testing.T, c kubernetesClient.Client, res CertificateOwner, cas string) {
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{tls.CAConfigMapKey: cas})
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), map[string]string{"clusterca-pem": cas})
	}

	t.Run("no CA configured by the user, so there is nothing that has to stay trusted", func(t *testing.T) {
		c := setup(t, legacyCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		bothBundles(t, c, res, otherCA.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}, componentNames, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("the bundle is not published yet, so there is nothing to compare", func(t *testing.T) {
		c := setup(t, legacyCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))

		status := VerifyMigrationTrustsLegacyCA(ctx, c, managedCertResource(), lastAchieved, componentNames, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("the same CA, with more certificates in the user's CA file than the one that signs the deployment's", func(t *testing.T) {
		// The managed bundle gets only the issuer's CA, and the other certificates in the user's file are
		// not needed for the members to recognise each other.
		c := setup(t, legacyCA.pem+unrelatedCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		bothBundles(t, c, res, legacyCA.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("a new CA, with the old one added to the bundle", func(t *testing.T) {
		c := setup(t, legacyCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		bothBundles(t, c, res, otherCA.pem+legacyCA.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("a new CA, and the server bundle does not validate the member certificate", func(t *testing.T) {
		c := setup(t, legacyCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{tls.CAConfigMapKey: otherCA.pem})
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), map[string]string{"clusterca-pem": legacyCA.pem})

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		require.False(t, status.IsOK(), "the migration has to be refused")
		assert.Contains(t, status.Message(), memberSecret, "the message names the secret of the certificate")
		assert.Contains(t, status.Message(), ManagedServerCABundleConfigMapName(res.GetName()), "and the bundle that does not validate it")
		assert.Contains(t, status.Message(), "managedCertificate.server.additionalTrustedCAs", "and the field to set")
		assert.Contains(t, status.Message(), legacyCAName, "and the user's CA as an example value")
		assert.NotContains(t, status.Message(), "managedCertificate.client.additionalTrustedCAs", "the client bundle validates the clusterfile")
	})

	t.Run("a new CA, and the client bundle does not validate the clusterfile", func(t *testing.T) {
		c := setup(t, legacyCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{tls.CAConfigMapKey: legacyCA.pem})
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), map[string]string{"clusterca-pem": otherCA.pem})

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		require.False(t, status.IsOK(), "the migration has to be refused")
		assert.Contains(t, status.Message(), clusterfileSecret)
		assert.Contains(t, status.Message(), "managedCertificate.client.additionalTrustedCAs")
	})

	t.Run("the intermediate CA that signed the certificate is only in the user's CA file", func(t *testing.T) {
		// The certificate does not carry its chain, and a member on the managed certificates no longer
		// has the user's CA file, so it cannot connect the certificate to the root in the bundle.
		intermediate := newIntermediateCA(t, legacyCA, "legacy-intermediate")
		c := setup(t, legacyCA.pem+intermediate.pem, intermediate.issueLeaf(t, "member"), intermediate.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		bothBundles(t, c, res, legacyCA.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		require.False(t, status.IsOK(), "the bundle cannot validate the certificate without the intermediate")
		assert.Contains(t, status.Message(), "additionalTrustedCAs")
	})

	t.Run("the intermediate CA that signed the certificate is in the bundle", func(t *testing.T) {
		// What the user does after the refusal above: add the CA file to additionalTrustedCAs, which puts
		// the intermediate in the bundle.
		intermediate := newIntermediateCA(t, legacyCA, "legacy-intermediate")
		c := setup(t, legacyCA.pem+intermediate.pem, intermediate.issueLeaf(t, "member"), intermediate.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		bothBundles(t, c, res, legacyCA.pem+intermediate.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("the certificate is signed by an intermediate CA that follows it in tls.crt", func(t *testing.T) {
		intermediate := newIntermediateCA(t, legacyCA, "legacy-intermediate")
		withChain := func(leaf []byte) []byte { return append(leaf, intermediate.pem...) }
		c := setup(t, legacyCA.pem, withChain(intermediate.issueLeaf(t, "member")), withChain(intermediate.issueLeaf(t, "clusterfile")))
		res := managedCertResource()
		bothBundles(t, c, res, legacyCA.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		assert.True(t, status.IsOK(), "the certificate sends its chain, so the root in the bundle is enough: %s", status.Message())
	})

	t.Run("a bundle kept from an earlier change counts as trusted", func(t *testing.T) {
		// The pods still trust the bundle a previous change published, so the CA it holds is trusted
		// even though it is no longer the current one.
		c := setup(t, legacyCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))
		res := managedCertResource()
		kept := map[string]string{
			"abcdef":                   legacyCA.pem,
			util.LatestHashSecretKey:   "123456",
			util.PreviousHashSecretKey: "abcdef",
			"123456":                   otherCA.pem,
		}
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), kept)
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), kept)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("no certificate secret, so there is nothing the members could reject", func(t *testing.T) {
		c := newMigrationCAClient(t, map[string]string{legacyCAName: legacyCA.pem})
		res := managedCertResource()
		bothBundles(t, c, res, otherCA.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, componentNames, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("every component is checked", func(t *testing.T) {
		// A sharded cluster has certificates for every component: one the bundle does not validate is
		// enough to refuse the migration.
		c := setup(t, legacyCA.pem, legacyCA.issueLeaf(t, "member"), legacyCA.issueLeaf(t, "clusterfile"))
		const otherComponent = "mdb-config"
		putLegacyCertificate(t, c, lastAchieved.MemberCertificateSecretName(otherComponent), otherCA.issueLeaf(t, "config"))
		res := managedCertResource()
		bothBundles(t, c, res, legacyCA.pem)

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, []string{resName, otherComponent}, zap.S())

		require.False(t, status.IsOK(), "the other component's certificate is not validated")
		assert.Contains(t, status.Message(), lastAchieved.MemberCertificateSecretName(otherComponent))
		assert.NotContains(t, status.Message(), "secret "+memberSecret+" ", "the first component's certificate is validated")
	})
}

// TestVerifyManagedCertificateMigrationTrust covers the two directions checked for Ops Manager and
// AppDB, including intermediates carried in the PEM and older ConfigMap entries that the active
// hash does not reference.
func TestVerifyManagedCertificateMigrationTrust(t *testing.T) {
	ctx := context.Background()
	trustCheck := func(legacySecret, managedBundle, issuedSecret, legacyCA string) MigrationTrustCheck {
		return MigrationTrustCheck{
			Component:                   "component",
			LegacyCertificateSecretName: legacySecret,
			LegacyCertificateKey:        func(corev1.Secret) string { return corev1.TLSCertKey },
			ManagedCABundleName:         managedBundle,
			IssuedCertificateSecretName: issuedSecret,
			LegacyCAConfigMapName:       legacyCA,
			LegacyCAConfigMapKey:        util.CaCertMMS,
			AdditionalTrustedCAsField:   "managedCertificate.additionalTrustedCAs",
		}
	}

	t.Run("the same signer passes both directions", func(t *testing.T) {
		ca := newTestCA(t, "migration-ca")
		c := newMigrationCAClient(t, nil)
		putLegacyCertificate(t, c, "legacy", ca.issueLeaf(t, "legacy"))
		putLegacyCertificate(t, c, "issued", ca.issueLeaf(t, "issued"))
		putBundle(t, c, "managed-bundle", map[string]string{util.LatestHashSecretKey: "active", "active": ca.pem})
		putBundle(t, c, "legacy-ca", map[string]string{util.CaCertMMS: ca.pem})

		status := VerifyManagedCertificateMigrationTrust(ctx, c, resNamespace, trustCheck("legacy", "managed-bundle", "issued", "legacy-ca"))
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("intermediates after the leaf connect both directions", func(t *testing.T) {
		root := newTestCA(t, "migration-root")
		intermediate := newIntermediateCA(t, root, "migration-intermediate")
		withChain := func(leaf []byte) []byte { return append(leaf, intermediate.pem...) }
		c := newMigrationCAClient(t, nil)
		putLegacyCertificate(t, c, "legacy", withChain(intermediate.issueLeaf(t, "legacy")))
		putLegacyCertificate(t, c, "issued", withChain(intermediate.issueLeaf(t, "issued")))
		putBundle(t, c, "managed-bundle", map[string]string{util.LatestHashSecretKey: "active", "active": root.pem})
		putBundle(t, c, "legacy-ca", map[string]string{util.CaCertMMS: root.pem})

		status := VerifyManagedCertificateMigrationTrust(ctx, c, resNamespace, trustCheck("legacy", "managed-bundle", "issued", "legacy-ca"))
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("an older bundle entry the active hash does not reference is not trust", func(t *testing.T) {
		active := newTestCA(t, "active-ca")
		legacy := newTestCA(t, "legacy-ca")
		c := newMigrationCAClient(t, nil)
		putLegacyCertificate(t, c, "legacy", legacy.issueLeaf(t, "legacy"))
		putLegacyCertificate(t, c, "issued", active.issueLeaf(t, "issued"))
		putBundle(t, c, "managed-bundle", map[string]string{
			util.LatestHashSecretKey: "active",
			"active":                 active.pem,
			"stale":                  legacy.pem,
		})
		putBundle(t, c, "legacy-ca", map[string]string{util.CaCertMMS: active.pem})

		status := VerifyManagedCertificateMigrationTrust(ctx, c, resNamespace, trustCheck("legacy", "managed-bundle", "issued", "legacy-ca"))
		require.False(t, status.IsOK())
		assert.Contains(t, status.Message(), "managed-bundle")
		assert.Contains(t, status.Message(), "legacy")
	})
}

// managedCertResource is a deployment that uses operator-managed certificates with x509 internal
// cluster authentication, so it has both trust bundles.
func managedCertResource() *mdbv1.MongoDB {
	return newMongoDB(resName, func(m *mdbv1.MongoDB) {
		m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
		m.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, InternalCluster: util.X509}
	})
}

// newMigrationCAClient returns a client holding the given CA ConfigMaps, keyed by name.
func newMigrationCAClient(t *testing.T, caConfigMaps map[string]string) kubernetesClient.Client {
	t.Helper()
	c, _ := mock.NewDefaultFakeClient()
	for name, ca := range caConfigMaps {
		putBundle(t, c, name, map[string]string{tls.CAConfigMapKey: ca})
	}
	return c
}

// putBundle writes the data of a CA ConfigMap.
func putBundle(t *testing.T, c kubernetesClient.Client, name string, data map[string]string) {
	t.Helper()
	require.NoError(t, c.Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resNamespace},
		Data:       data,
	}))
}

// putLegacyCertificate writes one of the user's certificate secrets, holding the certificate PEM in
// tls.crt and a private key in tls.key.
func putLegacyCertificate(t *testing.T, c kubernetesClient.Client, secretName string, cert []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, c.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resNamespace},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       cert,
			corev1.TLSPrivateKeyKey: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
		},
	}))
}

// newIntermediateCA returns a CA signed by the parent CA.
func newIntermediateCA(t *testing.T, parent *testCA, commonName string) *testCA {
	t.Helper()
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
	der, err := x509.CreateCertificate(rand.Reader, template, parent.cert, &key.PublicKey, parent.key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}
