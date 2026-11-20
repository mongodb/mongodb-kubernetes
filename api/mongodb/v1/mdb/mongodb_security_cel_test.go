package mdb_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
			// tls.ca is the legacy trust ConfigMap and the operator ignores it in managed mode,
			// so the combination is rejected instead of being silently ignored.
			name: "tls.ca and managedCertificate together are rejected",
			security: &mdb.Security{
				TLSConfig:          &mdb.TLSConfig{Enabled: true, CA: "my-ca"},
				ManagedCertificate: &mdb.ManagedCertificate{},
			},
			errorContains: "security.tls.ca and security.managedCertificate are mutually exclusive",
		},
		{
			// The legacy path keeps working: a user-provided CA with no managed certificates.
			name:     "tls.ca without managedCertificate is accepted",
			security: &mdb.Security{TLSConfig: &mdb.TLSConfig{Enabled: true, CA: "my-ca"}},
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

// TestManagedCertificateSubjectsAreImmutable covers the transition rule on
// security.managedCertificate.subjects. Test makes sure that the field is immutable.
//
// Setting the subjects when the managedCertificate block is added stays allowed: that is how a migration
// from user-managed TLS picks up the subjects the deployment already uses.
func TestManagedCertificateSubjectsAreImmutable(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	// The subjects the deployment uses today, read off its certificates before the migration.
	subjects := func(organization string) *mdb.CertSubjects {
		return &mdb.CertSubjects{
			Membership: &mdb.X509Subject{Organizations: []string{organization}},
			Agent:      &mdb.X509Subject{Organizations: []string{organization}, CommonName: "agent"},
		}
	}
	managedCertificate := func(subjects *mdb.CertSubjects) *mdb.ManagedCertificate {
		return &mdb.ManagedCertificate{
			Server:   &mdb.CertificateConfig{IssuerRef: &mdb.IssuerRef{Name: "my-issuer", Kind: "Issuer"}},
			Subjects: subjects,
		}
	}

	// The deployment starts on the legacy path: TLS with the certificates the user provides.
	resource := &mdb.MongoDB{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "test-subjects-immutable-", Namespace: "default"},
		Spec: mdb.MongoDbSpec{
			DbCommonSpec: mdb.DbCommonSpec{
				Version:      "7.0.0",
				ResourceType: mdb.ReplicaSet,
				ConnectionSpec: mdb.ConnectionSpec{
					Credentials: "my-credentials",
				},
				Security: &mdb.Security{TLSConfig: &mdb.TLSConfig{Enabled: true, CA: "my-ca"}},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, resource))

	// update applies the change to the stored resource, the way a user editing the spec would.
	update := func(mutate func(*mdb.Security)) error {
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(resource), resource))
		mutate(resource.Spec.Security)
		return k8sClient.Update(ctx, resource)
	}

	t.Run("setting the subjects when the managedCertificate block is added is accepted", func(t *testing.T) {
		require.NoError(t, update(func(security *mdb.Security) {
			security.TLSConfig = nil
			security.ManagedCertificate = managedCertificate(subjects("cluster.local"))
		}))
	})

	t.Run("changing the subjects is rejected", func(t *testing.T) {
		err := update(func(security *mdb.Security) {
			security.ManagedCertificate = managedCertificate(subjects("other.local"))
		})
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
		assert.Contains(t, err.Error(), "security.managedCertificate.subjects field is immutable it cannot change once the managedCertificate block is in place.")
	})

	t.Run("setting the same subjects again is accepted", func(t *testing.T) {
		require.NoError(t, update(func(security *mdb.Security) {
			security.ManagedCertificate = managedCertificate(subjects("cluster.local"))
		}))
	})

	t.Run("dropping the subjects while the block stays is rejected", func(t *testing.T) {
		err := update(func(security *mdb.Security) {
			security.ManagedCertificate = managedCertificate(nil)
		})
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
		assert.Contains(t, err.Error(), "security.managedCertificate.subjects field is immutable it cannot change once the managedCertificate block is in place.")
	})

	t.Run("adding the subjects to a block that was added without them is rejected", func(t *testing.T) {
		// The operator computed the subjects from its defaults when the block was added, so setting
		// them now is a change to the certificates in use, not a migration.
		withoutSubjects := &mdb.MongoDB{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-subjects-added-later-", Namespace: "default"},
			Spec:       *resource.Spec.DeepCopy(),
		}
		withoutSubjects.Spec.Security.ManagedCertificate = managedCertificate(nil)
		require.NoError(t, k8sClient.Create(ctx, withoutSubjects))

		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(withoutSubjects), withoutSubjects))
		withoutSubjects.Spec.Security.ManagedCertificate = managedCertificate(subjects("cluster.local"))
		err := k8sClient.Update(ctx, withoutSubjects)
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
		assert.Contains(t, err.Error(), "security.managedCertificate.subjects field is immutable it cannot change once the managedCertificate block is in place.")
	})

	t.Run("removing the whole managedCertificate block is accepted", func(t *testing.T) {
		require.NoError(t, update(func(security *mdb.Security) {
			security.ManagedCertificate = nil
			security.TLSConfig = &mdb.TLSConfig{Enabled: true, CA: "my-ca"}
		}))
	})
}
