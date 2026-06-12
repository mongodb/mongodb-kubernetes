package om

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

// fakeSecretStore is a minimal in-memory secret.GetUpdateCreator for testing
// SeedAgentAuthSecretFrom without pulling in the operator mock (avoids an import
// cycle: the operator mock imports this package).
type fakeSecretStore struct {
	secrets map[client.ObjectKey]corev1.Secret
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{secrets: map[client.ObjectKey]corev1.Secret{}}
}

func (f *fakeSecretStore) GetSecret(_ context.Context, key client.ObjectKey) (corev1.Secret, error) {
	if s, ok := f.secrets[key]; ok {
		return s, nil
	}
	return corev1.Secret{}, &apiErrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound}}
}

func (f *fakeSecretStore) UpdateSecret(_ context.Context, s corev1.Secret) error {
	f.secrets[client.ObjectKey{Name: s.Name, Namespace: s.Namespace}] = s
	return nil
}

func (f *fakeSecretStore) CreateSecret(ctx context.Context, s corev1.Secret) error {
	return f.UpdateSecret(ctx, s)
}

func (f *fakeSecretStore) DeleteSecret(_ context.Context, key client.ObjectKey) error {
	delete(f.secrets, key)
	return nil
}

func (f *fakeSecretStore) put(namespace, name string, data map[string]string) {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	f.secrets[client.ObjectKey{Name: name, Namespace: namespace}] = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       d,
	}
}

func (f *fakeSecretStore) password(namespace, name string) string {
	s := f.secrets[client.ObjectKey{Name: name, Namespace: namespace}]
	return string(s.Data[constants.AutomationAgentAuthSecretKey])
}

func (f *fakeSecretStore) shipperPassword(namespace, name string) string {
	s := f.secrets[client.ObjectKey{Name: name, Namespace: namespace}]
	return string(s.Data[MonarchShipperPasswordKey])
}

func TestSeedAgentAuthSecretFrom(t *testing.T) {
	ctx := context.Background()
	const ns = "ns"
	const activePwd = "active-password"

	t.Run("seeds target from source when target missing", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, "active-agent-auth-secret", map[string]string{constants.AutomationAgentAuthSecretKey: activePwd})

		err := SeedAgentAuthSecretFrom(ctx, store, ns, "active-agent-auth-secret", "standby")
		require.NoError(t, err)
		assert.Equal(t, activePwd, store.password(ns, AuthSecretName("standby")))
	})

	t.Run("overwrites a differing target password", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, "active-agent-auth-secret", map[string]string{constants.AutomationAgentAuthSecretKey: activePwd})
		store.put(ns, AuthSecretName("standby"), map[string]string{constants.AutomationAgentAuthSecretKey: "standby-own-password"})

		err := SeedAgentAuthSecretFrom(ctx, store, ns, "active-agent-auth-secret", "standby")
		require.NoError(t, err)
		assert.Equal(t, activePwd, store.password(ns, AuthSecretName("standby")))
	})

	t.Run("idempotent no-op when target already matches", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, "active-agent-auth-secret", map[string]string{constants.AutomationAgentAuthSecretKey: activePwd})
		store.put(ns, AuthSecretName("standby"), map[string]string{constants.AutomationAgentAuthSecretKey: activePwd})

		err := SeedAgentAuthSecretFrom(ctx, store, ns, "active-agent-auth-secret", "standby")
		require.NoError(t, err)
		assert.Equal(t, activePwd, store.password(ns, AuthSecretName("standby")))
	})

	t.Run("errors when source secret is missing", func(t *testing.T) {
		store := newFakeSecretStore()
		err := SeedAgentAuthSecretFrom(ctx, store, ns, "active-agent-auth-secret", "standby")
		assert.Error(t, err)
	})

	t.Run("errors when source lacks the password key", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, "active-agent-auth-secret", map[string]string{"some-other-key": "x"})
		err := SeedAgentAuthSecretFrom(ctx, store, ns, "active-agent-auth-secret", "standby")
		assert.Error(t, err)
	})
}

func TestActiveMdbNameFromAgentAuthSecretRef(t *testing.T) {
	assert.Equal(t, "active", ActiveMdbNameFromAgentAuthSecretRef("active-agent-auth-secret"))
	assert.Equal(t, "active", ActiveMdbNameFromAgentAuthSecretRef(AuthSecretName("active")))
	// No suffix → returned unchanged (defensive).
	assert.Equal(t, "active", ActiveMdbNameFromAgentAuthSecretRef("active"))
}

func TestEnsureMonarchShipperPassword(t *testing.T) {
	ctx := context.Background()
	const ns = "ns"
	const mdbName = "active"

	t.Run("generates and persists when secret missing", func(t *testing.T) {
		store := newFakeSecretStore()
		pwd, err := EnsureMonarchShipperPassword(ctx, store, ns, mdbName)
		require.NoError(t, err)
		assert.NotEmpty(t, pwd)
		assert.Equal(t, pwd, store.shipperPassword(ns, MonarchShipperSecretName(mdbName)))
	})

	t.Run("reads existing password", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, MonarchShipperSecretName(mdbName), map[string]string{MonarchShipperPasswordKey: "existing-pwd"})
		pwd, err := EnsureMonarchShipperPassword(ctx, store, ns, mdbName)
		require.NoError(t, err)
		assert.Equal(t, "existing-pwd", pwd)
	})

	t.Run("idempotent across calls", func(t *testing.T) {
		store := newFakeSecretStore()
		first, err := EnsureMonarchShipperPassword(ctx, store, ns, mdbName)
		require.NoError(t, err)
		second, err := EnsureMonarchShipperPassword(ctx, store, ns, mdbName)
		require.NoError(t, err)
		assert.Equal(t, first, second)
	})
}

func TestSeedMonarchShipperSecretFrom(t *testing.T) {
	ctx := context.Background()
	const ns = "ns"
	const activePwd = "active-shipper-pwd"
	sourceName := MonarchShipperSecretName("active")

	t.Run("seeds target from source when target missing", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, sourceName, map[string]string{MonarchShipperPasswordKey: activePwd})

		err := SeedMonarchShipperSecretFrom(ctx, store, ns, sourceName, "standby")
		require.NoError(t, err)
		assert.Equal(t, activePwd, store.shipperPassword(ns, MonarchShipperSecretName("standby")))
	})

	t.Run("idempotent no-op when target already matches", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, sourceName, map[string]string{MonarchShipperPasswordKey: activePwd})
		store.put(ns, MonarchShipperSecretName("standby"), map[string]string{MonarchShipperPasswordKey: activePwd})

		err := SeedMonarchShipperSecretFrom(ctx, store, ns, sourceName, "standby")
		require.NoError(t, err)
		assert.Equal(t, activePwd, store.shipperPassword(ns, MonarchShipperSecretName("standby")))
	})

	t.Run("overwrites a differing target password", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, sourceName, map[string]string{MonarchShipperPasswordKey: activePwd})
		store.put(ns, MonarchShipperSecretName("standby"), map[string]string{MonarchShipperPasswordKey: "standby-own"})

		err := SeedMonarchShipperSecretFrom(ctx, store, ns, sourceName, "standby")
		require.NoError(t, err)
		assert.Equal(t, activePwd, store.shipperPassword(ns, MonarchShipperSecretName("standby")))
	})

	t.Run("errors when source secret is missing", func(t *testing.T) {
		store := newFakeSecretStore()
		err := SeedMonarchShipperSecretFrom(ctx, store, ns, sourceName, "standby")
		assert.Error(t, err)
	})

	t.Run("errors when source lacks the shipper key", func(t *testing.T) {
		store := newFakeSecretStore()
		store.put(ns, sourceName, map[string]string{"some-other-key": "x"})
		err := SeedMonarchShipperSecretFrom(ctx, store, ns, sourceName, "standby")
		assert.Error(t, err)
	})
}
