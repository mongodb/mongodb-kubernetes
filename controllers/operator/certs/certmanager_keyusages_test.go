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

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
)

var (
	serverOnlyUsages     = []certmanagerv1.KeyUsage{certmanagerv1.UsageDigitalSignature, certmanagerv1.UsageKeyEncipherment, certmanagerv1.UsageServerAuth}
	serverAndClientUsage = []certmanagerv1.KeyUsage{certmanagerv1.UsageDigitalSignature, certmanagerv1.UsageKeyEncipherment, certmanagerv1.UsageServerAuth, certmanagerv1.UsageClientAuth}
)

// putIssuedCertificate writes a TLS secret holding a self-signed certificate with the given extended
// key usages, the way cert-manager would have written it. A nil usages list leaves the extension out.
func putIssuedCertificate(ctx context.Context, t *testing.T, c kubernetesClient.Client, name string, usages []x509.ExtKeyUsage) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	s := secret.Builder().
		SetName(name).
		SetNamespace(resNamespace).
		SetDataType(corev1.SecretTypeTLS).
		SetByteData(map[string][]byte{corev1.TLSCertKey: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}).
		Build()
	require.NoError(t, secrets.SecretClient{KubeClient: c}.PutSecret(ctx, s, Unused))
}

func TestWithholdsClientCertificate(t *testing.T) {
	config := func(value string) *mdbv1.AdditionalMongodConfig {
		cfg := &mdbv1.AdditionalMongodConfig{}
		require.NoError(t, cfg.UnmarshalJSON([]byte(`{"setParameter": {"tlsWithholdClientCertificate": `+value+`}}`)))
		return cfg
	}

	for _, tc := range []struct {
		tcName         string
		parameterValue string
		want           bool
	}{
		{tcName: "true in bool", parameterValue: "true", want: true},
		{tcName: "1", parameterValue: "1", want: true},
		{tcName: "quoted true", parameterValue: `"true"`, want: true},
		{tcName: "quoted 1", parameterValue: `"1"`, want: true},
		{tcName: "false", parameterValue: "false", want: false},
		{tcName: "0", parameterValue: "0", want: false},
	} {
		t.Run(tc.tcName, func(t *testing.T) {
			assert.Equal(t, tc.want, WithholdsClientCertificate(config(tc.parameterValue)))
		})
	}

	t.Run("no configuration at all", func(t *testing.T) {
		assert.False(t, WithholdsClientCertificate(nil))
		assert.False(t, WithholdsClientCertificate(&mdbv1.AdditionalMongodConfig{}))
	})

	t.Run("other parameters are ignored", func(t *testing.T) {
		cfg := &mdbv1.AdditionalMongodConfig{}
		require.NoError(t, cfg.UnmarshalJSON([]byte(`{"net": {"port": 27017}}`)))
		assert.False(t, WithholdsClientCertificate(cfg))
	})
}

func usageTestCertificate(secretName string, requested []certmanagerv1.KeyUsage) certToEnsure {
	return certToEnsure{certName: secretName, spec: certmanagerv1.CertificateSpec{SecretName: secretName, Usages: requested}}
}

func TestVerifyIssuedKeyUsages(t *testing.T) {
	ctx := context.Background()
	res := newMongoDB(resName, func(m *mdbv1.MongoDB) {
		m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
	})

	newClient := func() kubernetesClient.Client {
		c, _ := mock.NewDefaultFakeClient()
		return c
	}

	t.Run("a certificate that has every usage asked for is accepted", func(t *testing.T) {
		c := newClient()
		// The requested usages also hold the key usages (digitalSignature, keyEncipherment), which are
		// not extended key usages, so they are not looked for in the certificate.
		putIssuedCertificate(ctx, t, c, "mdb-managed-cert", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
		status := verifyKeyUsagesInIssuedCert(ctx, c, res, []certToEnsure{usageTestCertificate("mdb-managed-cert", serverAndClientUsage)})
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("a certificate without the clientAuth usage is refused with what to do", func(t *testing.T) {
		c := newClient()
		putIssuedCertificate(ctx, t, c, "mdb-managed-cert", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
		status := verifyKeyUsagesInIssuedCert(ctx, c, res, []certToEnsure{usageTestCertificate("mdb-managed-cert", serverAndClientUsage)})
		assert.False(t, status.IsOK())
		assert.Equal(t,
			"the certificate mdb-managed-cert doesn't have the EKUs (client auth) that were requested by operator, "+
				"there are chances that configured issuer doesn't honor EKU requests. "+
				"Point that category's issuerRef (security.managedCertificate.server or .client) at an issuer that allows the usage, "+
				"or leave it unset so the operator signs the certificate with its own CA",
			status.Message())
	})

	t.Run("a certificate with no extended key usage extension is accepted", func(t *testing.T) {
		c := newClient()
		putIssuedCertificate(ctx, t, c, "mdb-managed-cert", nil)
		status := verifyKeyUsagesInIssuedCert(ctx, c, res, []certToEnsure{usageTestCertificate("mdb-managed-cert", serverAndClientUsage)})
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("a certificate with anyExtendedKeyUsage is accepted", func(t *testing.T) {
		c := newClient()
		putIssuedCertificate(ctx, t, c, "mdb-managed-cert", []x509.ExtKeyUsage{x509.ExtKeyUsageAny})
		status := verifyKeyUsagesInIssuedCert(ctx, c, res, []certToEnsure{usageTestCertificate("mdb-managed-cert", serverAndClientUsage)})
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("one bad certificate among several is refused", func(t *testing.T) {
		c := newClient()
		putIssuedCertificate(ctx, t, c, "mdb-managed-cert", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
		putIssuedCertificate(ctx, t, c, "mdb-managed-clusterfile", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
		status := verifyKeyUsagesInIssuedCert(ctx, c, res, []certToEnsure{
			usageTestCertificate("mdb-managed-cert", serverOnlyUsages),
			usageTestCertificate("mdb-managed-clusterfile", serverAndClientUsage),
		})
		assert.False(t, status.IsOK())
		assert.Contains(t, status.Message(), "mdb-managed-clusterfile")
	})

	t.Run("a certificate that is not written yet is pending", func(t *testing.T) {
		c := newClient()
		status := verifyKeyUsagesInIssuedCert(ctx, c, res, []certToEnsure{usageTestCertificate("mdb-managed-cert", serverOnlyUsages)})
		assert.Equal(t, mdbstatus.PhasePending, status.Phase())
	})
}
