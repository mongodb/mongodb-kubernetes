package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

const vaultAgentAPIKeyAnnotation = "vault.hashicorp.com/agent-inject-secret-agentApiKey"

// inMemoryVault is a Vault KV v2 stand-in for reconcilers: it serves what was put into it and answers 404 otherwise.
type inMemoryVault struct {
	mu     sync.Mutex
	secret map[string]map[string]any
	client *vault.VaultClient
}

func newInMemoryVault(t *testing.T) *inMemoryVault {
	t.Helper()
	v := &inMemoryVault{secret: map[string]map[string]any{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/v1")
		if r.Method != http.MethodGet {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var payload struct{ Data map[string]any }
			require.NoError(t, json.Unmarshal(body, &payload))
			v.secret[path] = payload.Data
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if dataPath, isMetadata := strings.CutPrefix(path, "/secret/metadata/"); isMetadata {
			if _, ok := v.secret["/secret/data/"+dataPath]; ok {
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"current_version": 1}}))
				return
			}
		} else if data, ok := v.secret[path]; ok {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data}}))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client, err := vault.NewVaultClientForTesting(server.URL, "test-token", vault.VaultConfiguration{OperatorSecretPath: "operator", DatabaseSecretPath: "database"})
	require.NoError(t, err)
	v.client = client
	return v
}

func (v *inMemoryVault) put(path string, data map[string]string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	stored := map[string]any{}
	for key, value := range data {
		stored[key] = value
	}
	v.secret[path] = stored
}

func (v *inMemoryVault) has(path string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.secret[path]
	return ok
}

func (v *inMemoryVault) databaseSecretPath(namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", v.client.DatabaseSecretPath(), namespace, name)
}

// useVaultBackend switches a reconciler built on the Kubernetes backend to Vault, seeded with what an
// otherwise empty reconcile reads. The constructors panic with the Vault backend set, so this runs after them.
func useVaultBackend(t *testing.T, common *ReconcileCommonController, namespace string) *inMemoryVault {
	t.Helper()
	store := newInMemoryVault(t)
	t.Setenv("SECRET_BACKEND", vault.VaultBackend)
	common.VaultClient = store.client

	credentials := mock.GetCredentialsSecret(om.TestUser, om.TestApiKey)
	store.put(fmt.Sprintf("%s/%s/%s", store.client.OperatorSecretPath(), namespace, credentials.Name), map[string]string{
		util.OmPublicApiKey: string(credentials.Data[util.OmPublicApiKey]),
		util.OmPrivateKey:   string(credentials.Data[util.OmPrivateKey]),
	})
	cert, key := createMockCertAndKeyBytes()
	store.put(store.databaseSecretPath(namespace, util.AgentSecretName), map[string]string{
		corev1.TLSCertKey:       string(cert),
		corev1.TLSPrivateKeyKey: string(key),
	})
	return store
}

func listStatefulSets(ctx context.Context, t *testing.T, kubeClient kubernetesClient.Client) []appsv1.StatefulSet {
	t.Helper()
	sets := &appsv1.StatefulSetList{}
	require.NoError(t, kubeClient.List(ctx, sets))
	return sets.Items
}

type reconciledDeployment struct {
	namespace    string
	statefulSets []appsv1.StatefulSet
	kubeClient   kubernetesClient.Client
	vault        *inMemoryVault
}

// With the Kubernetes backend the agent reads the key from a mounted Secret. With Vault the key is
// injected into the pod and written to Vault only, so no Secret volume may be mounted.
func TestAgentAPIKeyProvisioning(t *testing.T) {
	ctx := context.Background()
	tests := map[string]struct {
		reconcile                func(t *testing.T, useVault bool) reconciledDeployment
		expectedStatefulSetCount int
	}{
		"replica set": {
			reconcile: func(t *testing.T, useVault bool) reconciledDeployment {
				rs := DefaultReplicaSetBuilder().Build()
				reconciler, kubeClient, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs, architectures.NonStatic)
				var store *inMemoryVault
				if useVault {
					store = useVaultBackend(t, reconciler.ReconcileCommonController, rs.Namespace)
				}
				checkReconcileSuccessful(ctx, t, reconciler, rs, kubeClient)
				return reconciledDeployment{rs.Namespace, listStatefulSets(ctx, t, kubeClient), kubeClient, store}
			},
			expectedStatefulSetCount: 1,
		},
		"standalone": {
			reconcile: func(t *testing.T, useVault bool) reconciledDeployment {
				st := DefaultStandaloneBuilder().Build()
				reconciler, kubeClient, _ := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, st, architectures.NonStatic)
				var store *inMemoryVault
				if useVault {
					store = useVaultBackend(t, reconciler.ReconcileCommonController, st.Namespace)
				}
				checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)
				return reconciledDeployment{st.Namespace, listStatefulSets(ctx, t, kubeClient), kubeClient, store}
			},
			expectedStatefulSetCount: 1,
		},
		"sharded cluster": {
			reconcile: func(t *testing.T, useVault bool) reconciledDeployment {
				sc := test.DefaultClusterBuilder().Build()
				reconciler, _, kubeClient, _, err := defaultShardedClusterReconciler(ctx, nil, "", "", sc, nil, testBackupEnableDelay, architectures.NonStatic)
				require.NoError(t, err)
				var store *inMemoryVault
				if useVault {
					store = useVaultBackend(t, reconciler.ReconcileCommonController, sc.Namespace)
				}
				checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)
				return reconciledDeployment{sc.Namespace, listStatefulSets(ctx, t, kubeClient), kubeClient, store}
			},
			// config servers, mongos and two shards
			expectedStatefulSetCount: 4,
		},
	}

	for name, tt := range tests {
		for _, useVault := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s with vault=%t", name, useVault), func(t *testing.T) {
				deployment := tt.reconcile(t, useVault)
				require.Len(t, deployment.statefulSets, tt.expectedStatefulSetCount)

				keySecretName := mdb.AgentAPIKeySecretName(om.TestGroupID)
				for _, sts := range deployment.statefulSets {
					annotations := sts.Spec.Template.Annotations
					if useVault {
						assert.Empty(t, construct.MountedAgentAPIKeySecretName(sts), sts.Name)
						assert.Equal(t, deployment.vault.databaseSecretPath(deployment.namespace, keySecretName), annotations[vaultAgentAPIKeyAnnotation], sts.Name)
					} else {
						assert.Equal(t, keySecretName, construct.MountedAgentAPIKeySecretName(sts), sts.Name)
						assert.NotContains(t, annotations, vaultAgentAPIKeyAnnotation, sts.Name)
					}
				}

				_, err := deployment.kubeClient.GetSecret(ctx, kube.ObjectKey(deployment.namespace, keySecretName))
				if useVault {
					assert.True(t, deployment.vault.has(deployment.vault.databaseSecretPath(deployment.namespace, keySecretName)), "the key must be written to Vault")
					assert.Error(t, err, "the key must not be written to Kubernetes")
				} else {
					assert.NoError(t, err)
				}
			})
		}
	}
}

// Vault is not supported for multi-cluster databases, so every member cluster mounts the key Secret.
func TestMultiClusterMountsAgentAPIKeySecretInEveryMemberCluster(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, kubeClient, memberClusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs, architectures.NonStatic)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, kubeClient, false)

	require.Len(t, memberClusterMap, len(clusters))
	for clusterName, memberClient := range memberClusterMap {
		sets := &appsv1.StatefulSetList{}
		require.NoError(t, memberClient.List(ctx, sets))
		require.Len(t, sets.Items, 1, clusterName)
		assert.Equal(t, mdb.AgentAPIKeySecretName(om.TestGroupID), construct.MountedAgentAPIKeySecretName(sets.Items[0]), clusterName)
		assert.NotContains(t, sets.Items[0].Spec.Template.Annotations, vaultAgentAPIKeyAnnotation, clusterName)
	}
}
