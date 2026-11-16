package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
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
// certificates when the CA the deployment trusts today would not be in the managed trust bundle.
func TestVerifyMigrationTrustsLegacyCA(t *testing.T) {
	ctx := context.Background()
	legacyCA := newSelfSignedCA(t)
	otherCA := newSelfSignedCA(t)

	// The deployment trusts the user's CA today, and its certificates are signed by it.
	lastAchieved := &mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true, CA: legacyCAName},
	}

	t.Run("no CA configured by the user, so there is nothing that has to stay trusted", func(t *testing.T) {
		c := newMigrationCAClient(t, nil)
		res := managedCertResource()

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("the bundle is not published yet, so there is nothing to compare", func(t *testing.T) {
		c := newMigrationCAClient(t, map[string]string{legacyCAName: legacyCA})
		res := managedCertResource()

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("the CA is in both bundles", func(t *testing.T) {
		c := newMigrationCAClient(t, map[string]string{legacyCAName: legacyCA})
		res := managedCertResource()
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{
			tls.CAConfigMapKey: legacyCA + otherCA,
		})
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), map[string]string{
			"clusterca-pem": legacyCA + otherCA,
		})

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("the CA is missing from the server bundle", func(t *testing.T) {
		c := newMigrationCAClient(t, map[string]string{legacyCAName: legacyCA})
		res := managedCertResource()
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{
			tls.CAConfigMapKey: otherCA,
		})
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), map[string]string{
			"clusterca-pem": legacyCA,
		})

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, zap.S())

		require.False(t, status.IsOK(), "the migration has to be refused")
		assert.Contains(t, status.Message(), legacyCAName, "the message names the ConfigMap the CA is in")
		assert.Contains(t, status.Message(), ManagedServerCABundleConfigMapName(res.GetName()), "and the bundle it is missing from")
		assert.Contains(t, status.Message(), "managedCertificate.server.additionalTrustedCAs", "and the field to set")
	})

	t.Run("the CA is missing from the client bundle", func(t *testing.T) {
		c := newMigrationCAClient(t, map[string]string{legacyCAName: legacyCA})
		res := managedCertResource()
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{
			tls.CAConfigMapKey: legacyCA,
		})
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), map[string]string{
			"clusterca-pem": otherCA,
		})

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, zap.S())

		require.False(t, status.IsOK(), "the migration has to be refused")
		assert.Contains(t, status.Message(), "managedCertificate.client.additionalTrustedCAs")
	})

	t.Run("a bundle kept from an earlier change counts as trusted", func(t *testing.T) {
		// The pods still trust the bundle a previous change published, so the CA it holds is trusted
		// even though it is no longer the current one.
		c := newMigrationCAClient(t, map[string]string{legacyCAName: legacyCA})
		res := managedCertResource()
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{
			"abcdef":                   legacyCA,
			util.LatestHashSecretKey:   "123456",
			util.PreviousHashSecretKey: "abcdef",
			"123456":                   otherCA,
		})
		putBundle(t, c, ManagedClientCABundleConfigMapName(res.GetName()), map[string]string{
			"abcdef":                   legacyCA,
			util.LatestHashSecretKey:   "123456",
			util.PreviousHashSecretKey: "abcdef",
			"123456":                   otherCA,
		})

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, zap.S())

		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("the CA the user configured is not a certificate, so there is nothing to keep trusted", func(t *testing.T) {
		c := newMigrationCAClient(t, map[string]string{legacyCAName: "not a certificate"})
		res := managedCertResource()
		putBundle(t, c, ManagedServerCABundleConfigMapName(res.GetName()), map[string]string{
			tls.CAConfigMapKey: otherCA,
		})

		status := VerifyMigrationTrustsLegacyCA(ctx, c, res, lastAchieved, zap.S())

		assert.True(t, status.IsOK(), status.Message())
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

// newSelfSignedCA returns a self-signed CA certificate in PEM.
func newSelfSignedCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
