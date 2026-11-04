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

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// newFakeClients returns the secret client the tests write with and the Kubernetes client the code
// under test reads with. They share one fake cluster.
func newFakeClients() (secrets.SecretClient, kubernetesClient.Client) {
	fakeClient, _ := mock.NewDefaultFakeClient(mdbv1.NewReplicaSetBuilder().Build())
	return secrets.SecretClient{KubeClient: fakeClient}, fakeClient
}

// putCertificateSecret writes a secret holding a self-signed certificate with the given subject, the
// way a certificate the user created looks to the operator.
func putCertificateSecret(ctx context.Context, t *testing.T, client secrets.SecretClient, name string, subject pkix.Name) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      subject,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	s := secret.Builder().
		SetName(name).
		SetNamespace(mock.TestNamespace).
		SetDataType(corev1.SecretTypeTLS).
		SetByteData(map[string][]byte{corev1.TLSCertKey: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}).
		Build()
	require.NoError(t, client.PutSecret(ctx, s, Unused))
}

// The subject of a legacy certificate, as the user would have created it.
func legacyCertificateSubject(organization, namespace string) pkix.Name {
	return pkix.Name{Organization: []string{organization}, OrganizationalUnit: []string{namespace}}
}

// The subject of a legacy agent certificate, as the user would have created it.
func legacyAgentCertificateSubject(commonName, organization, namespace string) pkix.Name {
	return pkix.Name{
		CommonName:         commonName,
		Organization:       []string{organization},
		OrganizationalUnit: []string{namespace},
		Country:            []string{"US"},
	}
}

func migrationTestResource(managedCertificates *mdbv1.ManagedCertificate) *mdbv1.MongoDB {
	rs := mdbv1.NewReplicaSetBuilder().SetName("my-rs").SetNamespace(mock.TestNamespace).Build()
	rs.Spec.Security = &mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true},
		Authentication: &mdbv1.Authentication{
			Enabled:         true,
			Modes:           []mdbv1.AuthMode{util.X509},
			InternalCluster: util.X509,
		},
		ManagedCertificate: managedCertificates,
	}
	return rs
}

// The security of the last applied spec: TLS with the user's certificates and x509 for both the
// members and the agent, which is the state the migration starts from.
func legacySecurity() *mdbv1.Security {
	return &mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true},
		Authentication: &mdbv1.Authentication{
			Enabled:         true,
			Modes:           []mdbv1.AuthMode{util.X509},
			InternalCluster: util.X509,
		},
	}
}

func TestVerifyMigrationSubjects(t *testing.T) {
	ctx := context.Background()
	legacy := legacySecurity()

	t.Run("subjects that match the managed defaults are accepted", func(t *testing.T) {
		// The legacy certificates carry the subject the operator gives the managed ones by default:
		// O=<name>-server, OU=<namespace> for the members, and CN=<name>-agent, O=<name>-agent,
		// OU=<namespace>, C=US for the agent.
		client, kubeClient := newFakeClients()
		putCertificateSecret(ctx, t, client, "my-rs-cert", legacyCertificateSubject("my-rs-server", mock.TestNamespace))
		putCertificateSecret(ctx, t, client, "agent-certs", legacyAgentCertificateSubject("my-rs-agent", "my-rs-agent", mock.TestNamespace))

		status := VerifyMigrationSubjects(ctx, kubeClient, migrationTestResource(&mdbv1.ManagedCertificate{}), legacy, "my-rs", zap.S())
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("a different membership subject is refused with the values to set", func(t *testing.T) {
		client, kubeClient := newFakeClients()
		putCertificateSecret(ctx, t, client, "my-rs-cert", legacyCertificateSubject("cluster.local-server", mock.TestNamespace))
		putCertificateSecret(ctx, t, client, "agent-certs", legacyAgentCertificateSubject("my-rs-agent", "my-rs-agent", mock.TestNamespace))

		status := VerifyMigrationSubjects(ctx, kubeClient, migrationTestResource(&mdbv1.ManagedCertificate{}), legacy, "my-rs", zap.S())
		assert.False(t, status.IsOK())
		assert.Equal(t,
			"this migration would change the subjects MongoDB uses to identify the deployment, so its members would stop recognising each other while they restart: "+
				"the member certificate's subject would change from O=[cluster.local-server], OU=[my-namespace] to O=[my-rs-server], OU=[my-namespace], "+
				"and MongoDB compares O and OU to recognise the members of a deployment. "+
				"Set spec.security.managedCertificate.subjects.membership to organizations: [cluster.local-server], organizationalUnits: [my-namespace] to keep the subject in use today",
			status.Message())
	})

	t.Run("keeping the legacy membership subject in the spec is accepted", func(t *testing.T) {
		client, kubeClient := newFakeClients()
		putCertificateSecret(ctx, t, client, "my-rs-cert", legacyCertificateSubject("cluster.local-server", mock.TestNamespace))
		putCertificateSecret(ctx, t, client, "agent-certs", legacyAgentCertificateSubject("my-rs-agent", "my-rs-agent", mock.TestNamespace))

		managed := &mdbv1.ManagedCertificate{Subjects: &mdbv1.CertSubjects{
			Membership: &mdbv1.X509Subject{
				Organizations:       []string{"cluster.local-server"},
				OrganizationalUnits: []string{mock.TestNamespace},
			},
		}}
		status := VerifyMigrationSubjects(ctx, kubeClient, migrationTestResource(managed), legacy, "my-rs", zap.S())
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("keeping the legacy agent subject in the spec is accepted", func(t *testing.T) {
		client, kubeClient := newFakeClients()
		putCertificateSecret(ctx, t, client, "my-rs-cert", legacyCertificateSubject("my-rs-server", mock.TestNamespace))
		putCertificateSecret(ctx, t, client, "agent-certs", legacyAgentCertificateSubject("cluster.local-agent", "cluster.local-agent", mock.TestNamespace))

		managed := &mdbv1.ManagedCertificate{Subjects: &mdbv1.CertSubjects{
			Agent: &mdbv1.X509Subject{
				CommonName:          "cluster.local-agent",
				Organizations:       []string{"cluster.local-agent"},
				OrganizationalUnits: []string{mock.TestNamespace},
				Countries:           []string{"US"},
			},
		}}
		status := VerifyMigrationSubjects(ctx, kubeClient, migrationTestResource(managed), legacy, "my-rs", zap.S())
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("a different agent subject is refused with the values to set", func(t *testing.T) {
		client, kubeClient := newFakeClients()
		putCertificateSecret(ctx, t, client, "my-rs-cert", legacyCertificateSubject("my-rs-server", mock.TestNamespace))
		putCertificateSecret(ctx, t, client, "agent-certs", legacyAgentCertificateSubject("cluster.local-agent", "cluster.local-agent", mock.TestNamespace))

		status := VerifyMigrationSubjects(ctx, kubeClient, migrationTestResource(&mdbv1.ManagedCertificate{}), legacy, "my-rs", zap.S())
		assert.False(t, status.IsOK())
		assert.Equal(t,
			"this migration would change the subjects MongoDB uses to identify the deployment, so its members would stop recognising each other while they restart: "+
				"the agent certificate's subject would change from CN=cluster.local-agent, O=[cluster.local-agent], OU=[my-namespace], C=[US] to CN=my-rs-agent, O=[my-rs-agent], OU=[my-namespace], C=[US], "+
				"and the agent's MongoDB user is registered under its subject. "+
				"Set spec.security.managedCertificate.subjects.agent to commonName: cluster.local-agent, organizations: [cluster.local-agent], organizationalUnits: [my-namespace], countries: [US] to keep the subject in use today",
			status.Message())
	})

	t.Run("the membership subject is not checked without x509 internal cluster auth", func(t *testing.T) {
		client, kubeClient := newFakeClients()
		putCertificateSecret(ctx, t, client, "my-rs-cert", legacyCertificateSubject("cluster.local-server", mock.TestNamespace))
		putCertificateSecret(ctx, t, client, "agent-certs", legacyAgentCertificateSubject("my-rs-agent", "my-rs-agent", mock.TestNamespace))

		rs := migrationTestResource(&mdbv1.ManagedCertificate{})
		rs.Spec.Security.Authentication.InternalCluster = ""
		status := VerifyMigrationSubjects(ctx, kubeClient, rs, legacy, "my-rs", zap.S())
		assert.True(t, status.IsOK(), status.Message())
	})

	t.Run("a certificate the deployment does not use is skipped", func(t *testing.T) {
		client, kubeClient := newFakeClients()
		putCertificateSecret(ctx, t, client, "my-rs-cert", legacyCertificateSubject("my-rs-server", mock.TestNamespace))

		status := VerifyMigrationSubjects(ctx, kubeClient, migrationTestResource(&mdbv1.ManagedCertificate{}), legacy, "my-rs", zap.S())
		assert.True(t, status.IsOK(), status.Message())
	})
}
