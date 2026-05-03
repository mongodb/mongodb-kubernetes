package searchcontroller

import (
	"context"
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

func TestCheckSecretsPresence_AllPresent_NoResults(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)

	assert.Empty(t, got, "all secrets present in central → no SecretCheckResult entries")
}

func TestCheckSecretsPresence_TLSPrefix_SingleRS(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "lt"}

	// Only the password is present; the TLS cert is missing → expect it in Missing.
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)

	assert.Len(t, got, 1)
	assert.Equal(t, "", got[0].Cluster)
	assert.Contains(t, got[0].Missing, search.TLSSecretNamespacedName().Name)
}

func TestCheckSecretsPresence_MissingPasswordInOneMember(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")

	// Central + east have the password; west does not.
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	east := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))
	west := newClientWithSecrets(t)
	members := map[string]client.Client{"east": east, "west": west}

	got := CheckSecretsPresence(context.Background(), search, central, members)

	// Only `west` should appear; `central` and `east` are silent.
	assert.Len(t, got, 1)
	assert.Equal(t, "west", got[0].Cluster)
	assert.Equal(t, []string{"search-sync-password"}, got[0].Missing)
}

func TestCheckSecretsPresence_TLSPrefix_PerShard(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
	search.Spec.Source.ExternalMongoDBSource.ShardedCluster = &searchv1.ExternalShardedClusterConfig{
		Router: searchv1.ExternalRouterConfig{Hosts: []string{"router:27017"}},
		Shards: []searchv1.ExternalShardConfig{
			{ShardName: "shard-0", Hosts: []string{"shard-0-mongo:27017"}},
			{ShardName: "shard-1", Hosts: []string{"shard-1-mongo:27017"}},
		},
	}
	search.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "lt"}

	// Provide all secrets except shard-1's TLS cert.
	central := newClientWithSecrets(t,
		newSecretObj("search-sync-password", "ns"),
		newSecretObj(search.TLSSecretForShard("shard-0").Name, "ns"),
	)

	got := CheckSecretsPresence(context.Background(), search, central, nil)

	assert.Len(t, got, 1)
	assert.Equal(t, []string{search.TLSSecretForShard("shard-1").Name}, got[0].Missing)
}

func TestCheckSecretsPresence_KeyfileSharded_RequiredOnlyWhenSet(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
	search.Spec.Source.ExternalMongoDBSource.ShardedCluster = &searchv1.ExternalShardedClusterConfig{
		Router: searchv1.ExternalRouterConfig{Hosts: []string{"router:27017"}},
		Shards: []searchv1.ExternalShardConfig{{ShardName: "shard-0", Hosts: []string{"h:27017"}}},
	}
	// No KeyFileSecretKeyRef set — must not be checked.
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)
	assert.Empty(t, got, "keyfile must not be required when KeyFileSecretKeyRef is unset")
}

func TestCheckSecretsPresence_KeyfileSharded_MissingWhenSet(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
	search.Spec.Source.ExternalMongoDBSource.ShardedCluster = &searchv1.ExternalShardedClusterConfig{
		Router: searchv1.ExternalRouterConfig{Hosts: []string{"router:27017"}},
		Shards: []searchv1.ExternalShardConfig{{ShardName: "shard-0", Hosts: []string{"h:27017"}}},
	}
	search.Spec.Source.ExternalMongoDBSource.KeyFileSecretKeyRef = &userv1.SecretKeyRef{Name: "mongod-keyfile"}
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)
	assert.Len(t, got, 1)
	assert.Contains(t, got[0].Missing, "mongod-keyfile")
}

func TestCheckSecretsPresence_KeyfileNotRequiredForRS(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	// RS source with KeyFileSecretKeyRef set on the spec → should still NOT be checked
	// because the keyfile secret is sharded-only.
	search.Spec.Source.ExternalMongoDBSource.KeyFileSecretKeyRef = &userv1.SecretKeyRef{Name: "should-not-check"}
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)
	assert.Empty(t, got, "keyfile is sharded-only; do not check it for RS sources")
}

func TestCheckSecretsPresence_X509_NotRequired_WhenAbsent(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)
	assert.Empty(t, got)
}

func TestCheckSecretsPresence_X509_Required_WhenConfigured(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Source.X509 = &searchv1.X509Auth{
		ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-client"},
	}
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)
	assert.Len(t, got, 1)
	assert.Contains(t, got[0].Missing, "x509-client")
}

func TestCheckSecretsPresence_SingleClusterFallback(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	central := newClientWithSecrets(t) // empty — password is missing

	got := CheckSecretsPresence(context.Background(), search, central, nil)
	assert.Len(t, got, 1)
	assert.Equal(t, "", got[0].Cluster, "single-cluster gap must surface as empty cluster name (central)")
	assert.Equal(t, []string{"search-sync-password"}, got[0].Missing)
}

func TestCheckSecretsPresence_ExternalCA_RequiredWhenSet(t *testing.T) {
	search := newSearchWithExternalSource("s", "ns")
	search.Spec.Source.ExternalMongoDBSource.TLS = &searchv1.ExternalMongodTLS{
		CA: &corev1.LocalObjectReference{Name: "external-ca"},
	}
	central := newClientWithSecrets(t, newSecretObj("search-sync-password", "ns"))

	got := CheckSecretsPresence(context.Background(), search, central, nil)
	assert.Len(t, got, 1)
	assert.Contains(t, got[0].Missing, "external-ca")
}
