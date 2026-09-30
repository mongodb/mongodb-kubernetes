package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubeClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

func TestReplicaSetReconcilerCleanupScramSecrets(t *testing.T) {
	lastApplied := newScramReplicaSet(mdbv1.MongoDBUser{
		Name: "testUser",
		PasswordSecretRef: v1.SecretKeyReference{
			Name: "password-secret-name",
		},
		ScramCredentialsSecretName: "scram-credentials",
	})

	t.Run("no change same resource", func(t *testing.T) {
		actual := getScramSecretsToDelete(lastApplied.Spec, lastApplied.Spec)

		assert.Equal(t, []string(nil), actual)
	})

	t.Run("new user new secret", func(t *testing.T) {
		current := newScramReplicaSet(
			mdbv1.MongoDBUser{
				Name: "testUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ScramCredentialsSecretName: "scram-credentials",
			},
			mdbv1.MongoDBUser{
				Name: "newUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ScramCredentialsSecretName: "scram-credentials-2",
			},
		)

		actual := getScramSecretsToDelete(current.Spec, lastApplied.Spec)

		assert.Equal(t, []string(nil), actual)
	})

	t.Run("old user new secret", func(t *testing.T) {
		current := newScramReplicaSet(mdbv1.MongoDBUser{
			Name: "testUser",
			PasswordSecretRef: v1.SecretKeyReference{
				Name: "password-secret-name",
			},
			ScramCredentialsSecretName: "scram-credentials-2",
		})

		expected := []string{"scram-credentials-scram-credentials"}
		actual := getScramSecretsToDelete(current.Spec, lastApplied.Spec)

		assert.Equal(t, expected, actual)
	})

	t.Run("removed one user and changed secret of the other", func(t *testing.T) {
		lastApplied = newScramReplicaSet(
			mdbv1.MongoDBUser{
				Name: "testUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ScramCredentialsSecretName: "scram-credentials",
			},
			mdbv1.MongoDBUser{
				Name: "anotherUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ScramCredentialsSecretName: "another-scram-credentials",
			},
		)

		current := newScramReplicaSet(mdbv1.MongoDBUser{
			Name: "testUser",
			PasswordSecretRef: v1.SecretKeyReference{
				Name: "password-secret-name",
			},
			ScramCredentialsSecretName: "scram-credentials-2",
		})

		expected := []string{"scram-credentials-scram-credentials", "another-scram-credentials-scram-credentials"}
		actual := getScramSecretsToDelete(current.Spec, lastApplied.Spec)

		assert.Equal(t, expected, actual)
	})
}

func TestReplicaSetReconcilerCleanupPemSecret(t *testing.T) {
	ctx := context.Background()
	lastAppliedSpec := mdbv1.MongoDBCommunitySpec{
		Security: mdbv1.Security{
			Authentication: mdbv1.Authentication{
				Modes: []mdbv1.AuthMode{"X509"},
			},
		},
	}
	mdb := mdbv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "my-rs",
			Namespace:   "my-ns",
			Annotations: map[string]string{},
		},
		Spec: mdbv1.MongoDBCommunitySpec{
			Members: 3,
			Version: "4.2.2",
			Security: mdbv1.Security{
				Authentication: mdbv1.Authentication{
					Modes: []mdbv1.AuthMode{"SCRAM"},
				},
				TLS: mdbv1.TLS{
					Enabled: true,
					CaConfigMap: &corev1.LocalObjectReference{
						Name: "caConfigMap",
					},
					CaCertificateSecret: &corev1.LocalObjectReference{
						Name: "certificateKeySecret",
					},
					CertificateKeySecret: corev1.LocalObjectReference{
						Name: "certificateKeySecret",
					},
				},
			},
		},
	}

	mgr := kubeClient.NewManager(ctx, &mdb)

	client := kubeClient.NewClient(mgr.GetClient())
	err := createAgentCertPemSecret(ctx, client, mdb, "CERT", "KEY", "")
	assert.NoError(t, err)

	r := NewReconciler(mgr, "fake-mongodbRepoUrl", "fake-mongodbImage", "ubi8", "fake-agentImage", "fake-versionUpgradeHookImage", "fake-readinessProbeImage")

	secret, err := r.client.GetSecret(ctx, mdb.AgentCertificatePemSecretNamespacedName())
	assert.NoError(t, err)
	assert.Equal(t, "CERT", string(secret.Data["tls.crt"]))
	assert.Equal(t, "KEY", string(secret.Data["tls.key"]))

	r.cleanupPemSecret(ctx, mdb.Spec, lastAppliedSpec, "my-ns")

	_, err = r.client.GetSecret(ctx, mdb.AgentCertificatePemSecretNamespacedName())
	assert.Error(t, err)
}

func TestReplicaSetReconcilerCleanupConnectionStringSecrets(t *testing.T) {
	lastApplied := newScramReplicaSet(mdbv1.MongoDBUser{
		Name: "testUser",
		PasswordSecretRef: v1.SecretKeyReference{
			Name: "password-secret-name",
		},
		ConnectionStringSecretName: "connection-string-secret",
	})

	t.Run("no change same resource", func(t *testing.T) {
		actual := getConnectionStringSecretsToDelete(lastApplied.Spec, lastApplied.Spec, "my-rs")

		assert.Equal(t, []string(nil), actual)
	})

	t.Run("new user does not require existing user cleanup", func(t *testing.T) {
		current := newScramReplicaSet(
			mdbv1.MongoDBUser{
				Name: "testUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ConnectionStringSecretName: "connection-string-secret",
			},
			mdbv1.MongoDBUser{
				Name: "newUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ConnectionStringSecretName: "connection-string-secret-2",
			},
		)

		actual := getConnectionStringSecretsToDelete(current.Spec, lastApplied.Spec, "my-rs")

		assert.Equal(t, []string(nil), actual)
	})

	t.Run("old user new secret", func(t *testing.T) {
		current := newScramReplicaSet(mdbv1.MongoDBUser{
			Name: "testUser",
			PasswordSecretRef: v1.SecretKeyReference{
				Name: "password-secret-name",
			},
			ConnectionStringSecretName: "connection-string-secret-2",
		})

		expected := []string{"connection-string-secret"}
		actual := getConnectionStringSecretsToDelete(current.Spec, lastApplied.Spec, "my-rs")

		assert.Equal(t, expected, actual)
	})

	t.Run("removed one user and changed secret of the other", func(t *testing.T) {
		lastApplied = newScramReplicaSet(
			mdbv1.MongoDBUser{
				Name: "testUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ConnectionStringSecretName: "connection-string-secret",
			},
			mdbv1.MongoDBUser{
				Name: "anotherUser",
				PasswordSecretRef: v1.SecretKeyReference{
					Name: "password-secret-name",
				},
				ConnectionStringSecretName: "connection-string-secret-2",
			},
		)

		current := newScramReplicaSet(mdbv1.MongoDBUser{
			Name: "testUser",
			PasswordSecretRef: v1.SecretKeyReference{
				Name: "password-secret-name",
			},
			ConnectionStringSecretName: "connection-string-secret-1",
		})

		expected := []string{"connection-string-secret", "connection-string-secret-2"}
		actual := getConnectionStringSecretsToDelete(current.Spec, lastApplied.Spec, "my-rs")

		assert.Equal(t, expected, actual)
	})
}

func TestReplicaSetReconcilerCleanupConnectionStringSecrets_OnlyDeletesOwnedSecrets(t *testing.T) {
	ctx := context.Background()
	const victimSecretName = "victim-app-secret"

	newMdb := func() mdbv1.MongoDBCommunity {
		mdb := newScramReplicaSet()
		mdb.UID = "mdb-uid"
		return mdb
	}
	lastApplied := newScramReplicaSet(mdbv1.MongoDBUser{
		Name: "testUser",
		PasswordSecretRef: v1.SecretKeyReference{
			Name: "password-secret-name",
		},
		ConnectionStringSecretName: victimSecretName,
	})
	// the diff between lastApplied and an empty user list queues victimSecretName for deletion
	require.Equal(t, []string{victimSecretName}, getConnectionStringSecretsToDelete(newMdb().Spec, lastApplied.Spec, "my-rs"))

	runCleanup := func(t *testing.T, ownerRefs []metav1.OwnerReference) (kubeClient.Client, mdbv1.MongoDBCommunity) {
		mdb := newMdb()
		mgr := kubeClient.NewManager(ctx, &mdb)
		victim := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            victimSecretName,
				Namespace:       mdb.Namespace,
				OwnerReferences: ownerRefs,
			},
			Data: map[string][]byte{"original-data": []byte("do-not-delete")},
		}
		require.NoError(t, mgr.Client.Create(ctx, &victim))

		r := NewReconciler(mgr, "fake-mongodbRepoUrl", "fake-mongodbImage", "ubi8", "fake-agentImage", "fake-versionUpgradeHookImage", "fake-readinessProbeImage")
		r.cleanupConnectionStringSecrets(ctx, mdb, lastApplied.Spec)
		return r.client, mdb
	}

	assertVictimSurvives := func(t *testing.T, c kubeClient.Client, mdb mdbv1.MongoDBCommunity) {
		s, err := c.GetSecret(ctx, types.NamespacedName{Name: victimSecretName, Namespace: mdb.Namespace})
		require.NoError(t, err, "secret not owned by this MongoDBCommunity must not be deleted")
		assert.Equal(t, []byte("do-not-delete"), s.Data["original-data"])
	}

	t.Run("secret with no owner is kept", func(t *testing.T) {
		c, mdb := runCleanup(t, nil)
		assertVictimSurvives(t, c, mdb)
	})

	t.Run("secret controlled by another resource is kept", func(t *testing.T) {
		other := newMdb()
		other.Name = "other-rs"
		other.UID = "other-uid"
		c, mdb := runCleanup(t, other.GetOwnerReferences())
		assertVictimSurvives(t, c, mdb)
	})

	t.Run("secret controlled by this resource is deleted", func(t *testing.T) {
		owner := newMdb()
		c, mdb := runCleanup(t, owner.GetOwnerReferences())
		_, err := c.GetSecret(ctx, types.NamespacedName{Name: victimSecretName, Namespace: mdb.Namespace})
		assert.True(t, apiErrors.IsNotFound(err), "secret owned by this MongoDBCommunity should be cleaned up, got: %v", err)
	})
}
