package searchcontroller

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
)

func newSecretsPresenceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return scheme
}

func newSecretObj(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

func newClientWithSecrets(t *testing.T, secrets ...*corev1.Secret) client.Client {
	t.Helper()
	objs := make([]client.Object, 0, len(secrets))
	for _, s := range secrets {
		objs = append(objs, s)
	}
	return fake.NewClientBuilder().WithScheme(newSecretsPresenceScheme(t)).WithObjects(objs...).Build()
}

func newSearchWithExternalSource(name, namespace string) *searchv1.MongoDBSearch {
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mongo-0:27017"},
				},
				PasswordSecretRef: &userv1.SecretKeyRef{Name: "search-sync-password"},
			},
		},
	}
}

// TestCheckSecretsPresence_HappyPath verifies that no gaps are reported when all
// expected secrets are present in both central and member clusters.
func TestCheckSecretsPresence_HappyPath(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	east := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	west := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	members := map[string]client.Client{"east": east, "west": west}

	got := CheckSecretsPresence(context.Background(), search, central, members)

	assert.Empty(t, got, "all secrets present in all clusters → no SecretCheckResult entries")
}

// TestCheckSecretsPresence_MissingSecret verifies that a missing secret in one member
// cluster surfaces as a single SecretCheckResult for that cluster only.
func TestCheckSecretsPresence_MissingSecret(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")

	// Central + east have the password; west does not.
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	east := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	west := newClientWithSecrets(t)
	members := map[string]client.Client{"east": east, "west": west}

	got := CheckSecretsPresence(context.Background(), search, central, members)

	assert.Len(t, got, 1)
	assert.Equal(t, "west", got[0].Cluster)
	assert.Equal(t, []string{"search-sync-password"}, got[0].Missing)
}

func mcShardedTLSSearch(t *testing.T, name, ns string, shardNames ...string) *searchv1.MongoDBSearch {
	t.Helper()
	shards := make([]searchv1.ExternalShardConfig, 0, len(shardNames))
	for _, n := range shardNames {
		shards = append(shards, searchv1.ExternalShardConfig{ShardName: n, Hosts: []string{n + ":27017"}})
	}
	s := newSearchWithExternalSource(name, ns)
	s.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
	s.Spec.Source.ExternalMongoDBSource.ShardedCluster = &searchv1.ExternalShardedClusterConfig{
		Router: searchv1.ExternalRouterConfig{Hosts: []string{"router:27017"}},
		Shards: shards,
	}
	s.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "lt"}
	return s
}

// pinClusters sets pinned spec.clusters[] entries (clusterName → index) so the
// MC secret-presence branch resolves each member cluster's index from the CRD pin.
func pinClusters(s *searchv1.MongoDBSearch, indexByCluster map[string]int32) {
	clusters := make([]searchv1.ClusterSpec, 0, len(indexByCluster))
	for name, idx := range indexByCluster {
		clusters = append(clusters, searchv1.ClusterSpec{Name: name, Index: ptr.To(idx)})
	}
	s.Spec.Clusters = clusters
}

func TestCheckSecretsPresence_MCSharded_PerClusterCertNames(t *testing.T) {
	search := mcShardedTLSSearch(t, "s", "ns", "shard-0", "shard-1")
	pinClusters(search, map[string]int32{"east": 0, "west": 1})

	central := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
	)
	east := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-1", 0), "ns"),
	)
	west := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 1), "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-1", 1), "ns"),
	)
	members := map[string]client.Client{"east": east, "west": west}

	got := CheckSecretsPresence(context.Background(), search, central, members)

	assert.Empty(t, got, "per-cluster cert names addressed via the pinned clusterIndex → no gaps")
}

func TestCheckSecretsPresence_MCSharded_MissingPerClusterCert(t *testing.T) {
	search := mcShardedTLSSearch(t, "s", "ns", "shard-0")
	pinClusters(search, map[string]int32{"east": 0, "west": 1})

	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	east := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
	)
	// west has east's cert name (index 0) but is at index 1.
	west := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
	)
	members := map[string]client.Client{"east": east, "west": west}

	got := CheckSecretsPresence(context.Background(), search, central, members)

	assert.Len(t, got, 1)
	assert.Equal(t, "west", got[0].Cluster)
	assert.Equal(t, []string{s_tlsShardNameAt("lt", "s", "shard-0", 1)}, got[0].Missing)
}

func TestCheckSecretsPresence_MCSharded_CentralSkipsPerShardCerts(t *testing.T) {
	search := mcShardedTLSSearch(t, "s", "ns", "shard-0", "shard-1")
	pinClusters(search, map[string]int32{"east": 0})

	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	east := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-1", 0), "ns"),
	)
	members := map[string]client.Client{"east": east}

	got := CheckSecretsPresence(context.Background(), search, central, members)

	assert.Empty(t, got, "central must not be probed for per-shard certs in MC mode")
}

func TestCheckSecretsPresence_MCSharded_CentralReportsInvariantGap(t *testing.T) {
	search := mcShardedTLSSearch(t, "s", "ns", "shard-0")
	pinClusters(search, map[string]int32{"east": 0})

	central := newClientWithSecrets(t)
	east := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
	)
	members := map[string]client.Client{"east": east}

	got := CheckSecretsPresence(context.Background(), search, central, members)

	assert.Len(t, got, 1)
	assert.Equal(t, "", got[0].Cluster)
	assert.Equal(t, []string{"search-sync-password"}, got[0].Missing)
}

func TestCheckSecretsPresence_SingleClusterSharded_CentralIncludesPerShardCerts(t *testing.T) {
	search := mcShardedTLSSearch(t, "s", "ns", "shard-0")

	central := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		// Missing: s_tlsShardNameAt("lt", "s", "shard-0", 0)
	)

	got := CheckSecretsPresence(context.Background(), search, central, nil)

	assert.Len(t, got, 1)
	assert.Equal(t, "", got[0].Cluster)
	assert.Equal(t, []string{s_tlsShardNameAt("lt", "s", "shard-0", 0)}, got[0].Missing)
}

// Operator-per-cluster with unified CR: per-shard cert names must be probed at the pinned index, not
// hard-coded 0 — else absent index-0 names look like phantom gaps and requeue forever.
func TestCheckSecretsPresence_OperatorPerCluster_UsesPinnedIndex(t *testing.T) {
	newPinnedSearch := func() *searchv1.MongoDBSearch {
		s := mcShardedTLSSearch(t, "s", "ns", "shard-0")
		s.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-b", Index: ptr.To(int32(7))}}
		return s
	}

	t.Run("index-7 secrets present means no gaps", func(t *testing.T) {
		central := newClientWithSecrets(t,
			newSecretObj("search-sync-password", "ns"),
			newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 7), "ns"),
		)
		got := CheckSecretsPresence(context.Background(), newPinnedSearch(), central, nil)
		assert.Empty(t, got, "presence must be probed at the pinned index 7 — no phantom index-0 gap")
	})

	t.Run("only index-0 secret present reports the index-7 name missing", func(t *testing.T) {
		central := newClientWithSecrets(t,
			newSecretObj("search-sync-password", "ns"),
			newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
		)
		got := CheckSecretsPresence(context.Background(), newPinnedSearch(), central, nil)
		assert.Len(t, got, 1)
		assert.Equal(t, []string{s_tlsShardNameAt("lt", "s", "shard-0", 7)}, got[0].Missing,
			"the gap must be reported under the index-7 name")
	})
}

func TestExpectedSecretNamesForCluster_Table(t *testing.T) {
	shardedSearch := func(keyfile string) *searchv1.MongoDBSearch {
		s := newSearchWithExternalSource("s", "ns")
		s.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
		s.Spec.Source.ExternalMongoDBSource.ShardedCluster = &searchv1.ExternalShardedClusterConfig{
			Router: searchv1.ExternalRouterConfig{Hosts: []string{"router:27017"}},
			Shards: []searchv1.ExternalShardConfig{
				{ShardName: "shard-0", Hosts: []string{"h0:27017"}},
				{ShardName: "shard-1", Hosts: []string{"h1:27017"}},
			},
		}
		if keyfile != "" {
			s.Spec.Source.ExternalMongoDBSource.KeyFileSecretKeyRef = &userv1.SecretKeyRef{Name: keyfile}
		}
		return s
	}

	tests := []struct {
		name     string
		build    func() *searchv1.MongoDBSearch
		wantKeys []string // substrings sufficient to uniquely identify expected names
	}{
		{
			name: "TLS RS source",
			build: func() *searchv1.MongoDBSearch {
				s := newSearchWithExternalSource("s", "ns")
				s.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "lt"}
				return s
			},
			wantKeys: []string{"search-sync-password", s_tlsRSName("lt", "s")},
		},
		{
			name: "TLS sharded source",
			build: func() *searchv1.MongoDBSearch {
				s := shardedSearch("")
				s.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "lt"}
				return s
			},
			wantKeys: []string{
				"search-sync-password",
				s_tlsShardName("lt", "s", "shard-0"),
				s_tlsShardName("lt", "s", "shard-1"),
			},
		},
		{
			name:     "keyfile sharded",
			build:    func() *searchv1.MongoDBSearch { return shardedSearch("mongod-keyfile") },
			wantKeys: []string{"search-sync-password", "mongod-keyfile"},
		},
		{
			name: "x509 auth",
			build: func() *searchv1.MongoDBSearch {
				s := newSearchWithExternalSource("s", "ns")
				s.Spec.Source.X509 = &searchv1.X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-client"},
				}
				return s
			},
			wantKeys: []string{"search-sync-password", "x509-client"},
		},
		{
			name: "external CA",
			build: func() *searchv1.MongoDBSearch {
				s := newSearchWithExternalSource("s", "ns")
				s.Spec.Source.ExternalMongoDBSource.TLS = &searchv1.ExternalMongodTLS{
					CA: &corev1.LocalObjectReference{Name: "external-ca"},
				}
				return s
			},
			wantKeys: []string{"search-sync-password", "external-ca"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expectedSecretNamesForCluster(tt.build(), 0)
			assert.ElementsMatch(t, tt.wantKeys, got)
		})
	}
}

// s_tlsRSName mirrors the naming logic from TLSSecretNamespacedName for test assertions.
func s_tlsRSName(prefix, resourceName string) string {
	return prefix + "-" + resourceName + "-search-cert"
}

// s_tlsShardName mirrors the naming logic from TLSSecretForClusterShard for test assertions.
func s_tlsShardName(prefix, resourceName, shardName string) string {
	return s_tlsShardNameAt(prefix, resourceName, shardName, 0)
}

// s_tlsShardNameAt mirrors TLSSecretForClusterShard for an arbitrary cluster index.
func s_tlsShardNameAt(prefix, resourceName, shardName string, clusterIndex int) string {
	return prefix + "-" + resourceName + "-search-" + strconv.Itoa(clusterIndex) + "-" + shardName + "-cert"
}
