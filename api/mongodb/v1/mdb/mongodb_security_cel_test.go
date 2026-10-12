package mdb_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdb "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMongoDBSecurityCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newMongoDB := func(security *mdb.Security) *mdb.MongoDB {
		return &mdb.MongoDB{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-security-cel-", Namespace: "default"},
			Spec: mdb.MongoDbSpec{
				DbCommonSpec: mdb.DbCommonSpec{
					Version:      "7.0.0",
					ResourceType: mdb.ReplicaSet,
					ConnectionSpec: mdb.ConnectionSpec{
						Credentials: "my-credentials",
					},
					Security: security,
				},
			},
		}
	}

	tlsEnabled := &mdb.TLSConfig{Enabled: true}
	issuer := &mdb.IssuerRef{Name: "my-issuer", Kind: "Issuer"}
	ca := &mdb.CARef{Name: "my-ca", Kind: "ConfigMap", Key: "ca.crt"}

	tests := []struct {
		name     string
		security *mdb.Security
		// errorContains is the expected CEL/schema validation message; empty means
		// the create must succeed.
		errorContains string
	}{
		{
			name:     "no security block is accepted",
			security: nil,
		},
		{
			name:     "certsSecretPrefix alone is accepted",
			security: &mdb.Security{TLSConfig: tlsEnabled, CertificatesSecretsPrefix: "my-prefix"},
		},
		{
			name:     "managedCertificate with tls.enabled=true is accepted",
			security: &mdb.Security{TLSConfig: tlsEnabled, ManagedCertificate: &mdb.ManagedCertificate{Server: &mdb.CertificateConfig{IssuerRef: issuer}}},
		},
		{
			name: "certsSecretPrefix and managedCertificate together are rejected",
			security: &mdb.Security{
				TLSConfig:                 tlsEnabled,
				CertificatesSecretsPrefix: "my-prefix",
				ManagedCertificate:        &mdb.ManagedCertificate{Server: &mdb.CertificateConfig{IssuerRef: issuer}},
			},
			errorContains: "security.certsSecretPrefix and security.managedCertificate are mutually exclusive",
		},
		{
			// managedCertificate enables TLS on its own (IsTLSEnabled returns true when it is set)
			name:     "managedCertificate without any tls block is accepted",
			security: &mdb.Security{ManagedCertificate: &mdb.ManagedCertificate{Server: &mdb.CertificateConfig{IssuerRef: issuer}}},
		},
		{
			name: "managedCertificate with tls.enabled=false is accepted",
			security: &mdb.Security{
				TLSConfig:          &mdb.TLSConfig{},
				ManagedCertificate: &mdb.ManagedCertificate{Server: &mdb.CertificateConfig{IssuerRef: issuer}},
			},
		},
		{
			name: "additionalTrustedCAs with an invalid kind is rejected",
			security: &mdb.Security{
				TLSConfig: tlsEnabled,
				ManagedCertificate: &mdb.ManagedCertificate{
					Server: &mdb.CertificateConfig{
						IssuerRef:            issuer,
						AdditionalTrustedCAs: []mdb.CARef{{Name: "bad", Kind: "Pod", Key: "ca.crt"}},
					},
				},
			},
			errorContains: "Unsupported value",
		},
		{
			name:     "managedCertificate with no blocks (zero-config, both self-signed) is accepted",
			security: &mdb.Security{ManagedCertificate: &mdb.ManagedCertificate{}},
		},
		{
			name:     "managedCertificate with only a server block is accepted",
			security: &mdb.Security{ManagedCertificate: &mdb.ManagedCertificate{Server: &mdb.CertificateConfig{IssuerRef: issuer}}},
		},
		{
			name: "ca together with issuerRef is accepted",
			security: &mdb.Security{ManagedCertificate: &mdb.ManagedCertificate{
				Server: &mdb.CertificateConfig{IssuerRef: issuer, CA: ca},
			}},
		},
		{
			name: "ca without issuerRef is rejected",
			security: &mdb.Security{ManagedCertificate: &mdb.ManagedCertificate{
				Server: &mdb.CertificateConfig{CA: ca},
			}},
			errorContains: "ca is only used with issuerRef",
		},
		{
			// an empty issuer name is treated as no issuer, so ca would be ignored.
			name: "ca with an empty issuer name is rejected",
			security: &mdb.Security{ManagedCertificate: &mdb.ManagedCertificate{
				Server: &mdb.CertificateConfig{IssuerRef: &mdb.IssuerRef{}, CA: ca},
			}},
			errorContains: "ca is only used with issuerRef",
		},
		{
			name: "client ca without a client issuerRef is rejected",
			security: &mdb.Security{ManagedCertificate: &mdb.ManagedCertificate{
				Client: &mdb.CertificateConfig{CA: ca},
			}},
			errorContains: "ca is only used with issuerRef",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := k8sClient.Create(ctx, newMongoDB(tc.security))

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
