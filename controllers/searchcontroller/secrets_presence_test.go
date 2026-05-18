package searchcontroller

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
)

func newSecretsPresenceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := searchv1.AddToScheme(scheme); err != nil {
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

	got := CheckSecretsPresence(context.Background(), search, central, members, map[string]int{"east": 0, "west": 1})

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

	got := CheckSecretsPresence(context.Background(), search, central, members, map[string]int{"east": 0, "west": 1})

	assert.Len(t, got, 1)
	assert.Equal(t, "west", got[0].Cluster)
	assert.Equal(t, []string{"search-sync-password"}, got[0].Missing)
}

// TestCheckSecretsPresence_MCSharded_PerClusterCertNames verifies that per-shard
// TLS cert names are addressed using each member cluster's persisted index
// (clusterMapping), not a fixed cluster-0 form. Without this, member clusters at
// index >0 would falsely report missing *-search-0-* certs every reconcile.
func TestCheckSecretsPresence_MCSharded_PerClusterCertNames(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
	search.Spec.Source.ExternalMongoDBSource.ShardedCluster = &searchv1.ExternalShardedClusterConfig{
		Router: searchv1.ExternalRouterConfig{Hosts: []string{"router:27017"}},
		Shards: []searchv1.ExternalShardConfig{
			{ShardName: "shard-0", Hosts: []string{"h0:27017"}},
			{ShardName: "shard-1", Hosts: []string{"h1:27017"}},
		},
	}
	search.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "lt"}

	// Central is treated as index 0; east at index 0; west at index 1.
	// Each cluster's seeded per-shard certs use its own cluster index.
	central := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-1", 0), "ns"),
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

	got := CheckSecretsPresence(context.Background(), search, central, members, map[string]int{"east": 0, "west": 1})

	assert.Empty(t, got, "per-cluster cert names addressed via clusterMapping → no gaps")
}

// TestCheckSecretsPresence_MCSharded_MissingPerClusterCert verifies the failure
// path: west is at index 1 but only carries an index-0 cert, so the index-1
// cert is reported missing for west specifically.
func TestCheckSecretsPresence_MCSharded_MissingPerClusterCert(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
	search.Spec.Source.ExternalMongoDBSource.ShardedCluster = &searchv1.ExternalShardedClusterConfig{
		Router: searchv1.ExternalRouterConfig{Hosts: []string{"router:27017"}},
		Shards: []searchv1.ExternalShardConfig{
			{ShardName: "shard-0", Hosts: []string{"h0:27017"}},
		},
	}
	search.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "lt"}

	central := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
	)
	east := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
	)
	// west has east's cert name (index 0) but is at index 1 — should report missing.
	west := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(s_tlsShardNameAt("lt", "s", "shard-0", 0), "ns"),
	)
	members := map[string]client.Client{"east": east, "west": west}

	got := CheckSecretsPresence(context.Background(), search, central, members, map[string]int{"east": 0, "west": 1})

	assert.Len(t, got, 1)
	assert.Equal(t, "west", got[0].Cluster)
	assert.Equal(t, []string{s_tlsShardNameAt("lt", "s", "shard-0", 1)}, got[0].Missing)
}

// TestExpectedSecretNamesForCluster_Table covers all conditional branches inside expectedSecretNamesForCluster.
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
