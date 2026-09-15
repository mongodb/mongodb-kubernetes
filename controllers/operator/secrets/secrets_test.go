package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

const testBasePath = "/secret/data/mongodbenterprise/operator"

// fakeVault is a minimal Vault KV v2 stand-in: it serves the secrets it was seeded with
// and records every write it receives.
type fakeVault struct {
	server *httptest.Server
	// data maps a vault path to its stored key/value pairs.
	data   map[string]map[string]string
	writes []string
}

func newFakeVault(t *testing.T, data map[string]map[string]string) *fakeVault {
	t.Helper()
	fv := &fakeVault{data: data}
	if fv.data == nil {
		fv.data = map[string]map[string]string{}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[len("/v1"):]

		if r.Method != http.MethodGet {
			fv.writes = append(fv.writes, path)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		secretData, ok := fv.data[path]
		if !ok {
			// Mirrors Vault's behaviour for a missing secret: 404 with no body, which the
			// api client surfaces as a nil secret.
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"data": secretData},
		}))
	})

	fv.server = httptest.NewServer(mux)
	t.Cleanup(fv.server.Close)
	return fv
}

func testSecret() corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tls-cert", Namespace: "my-namespace"},
		Data:       map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("private-key")},
	}
}

func vaultPathFor(s corev1.Secret) string {
	return fmt.Sprintf("%s/%s/%s", testBasePath, s.Namespace, s.Name)
}

func newSecretClient(t *testing.T, fv *fakeVault) SecretClient {
	t.Helper()
	vaultClient, err := vault.NewVaultClientForTesting(fv.server.URL, "test-token", vault.VaultConfiguration{})
	require.NoError(t, err)

	return SecretClient{
		VaultClient: vaultClient,
		KubeClient:  kubernetesClient.NewClient(fake.NewClientBuilder().Build()),
	}
}

func secretExistsInKubernetes(t *testing.T, r SecretClient, s corev1.Secret) bool {
	t.Helper()
	_, err := r.KubeClient.GetSecret(context.Background(), types.NamespacedName{Name: s.Name, Namespace: s.Namespace})
	return err == nil
}

// TestPutSecretIfChanged_VaultBackendNeverWritesToKubernetes is the regression test for
// KUBE-310: on a Vault backend the Kubernetes API must never receive the secret, in
// particular in the steady state where the Vault copy is already up to date.
func TestPutSecretIfChanged_VaultBackendNeverWritesToKubernetes(t *testing.T) {
	ctx := context.Background()
	s := testSecret()

	tests := map[string]struct {
		vaultData        map[string]map[string]string
		expectVaultWrite bool
	}{
		"vault copy is up to date": {
			vaultData:        map[string]map[string]string{vaultPathFor(s): {"tls.crt": "cert", "tls.key": "private-key"}},
			expectVaultWrite: false,
		},
		"vault copy is stale": {
			vaultData:        map[string]map[string]string{vaultPathFor(s): {"tls.crt": "cert", "tls.key": "old-private-key"}},
			expectVaultWrite: true,
		},
		"vault copy does not exist": {
			vaultData:        nil,
			expectVaultWrite: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SECRET_BACKEND", vault.VaultBackend)

			fv := newFakeVault(t, tc.vaultData)
			r := newSecretClient(t, fv)

			require.NoError(t, r.PutSecretIfChanged(ctx, s, testBasePath))

			assert.False(t, secretExistsInKubernetes(t, r, s), "secret must not be persisted to Kubernetes on a Vault backend")
			if tc.expectVaultWrite {
				assert.Equal(t, []string{vaultPathFor(s)}, fv.writes)
			} else {
				assert.Empty(t, fv.writes)
			}
		})
	}
}

func TestPutSecretIfChanged_KubernetesBackendWritesToKubernetes(t *testing.T) {
	ctx := context.Background()
	s := testSecret()

	t.Setenv("SECRET_BACKEND", vault.K8sSecretBackend)

	fv := newFakeVault(t, nil)
	r := newSecretClient(t, fv)

	require.NoError(t, r.PutSecretIfChanged(ctx, s, testBasePath))

	assert.True(t, secretExistsInKubernetes(t, r, s))
	assert.Empty(t, fv.writes)
}
