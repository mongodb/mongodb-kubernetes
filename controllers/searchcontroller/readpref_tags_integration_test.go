package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
)

// newQ2MCSearch builds a Q2-MC RS MongoDBSearch with two clusters and matchTags-driven
// syncSourceSelector, modelling the §4.1 spec example.
func newQ2MCSearch() *searchv1.MongoDBSearch {
	clusters := []searchv1.ClusterSpec{
		{
			ClusterName: "us-east-k8s",
			SyncSourceSelector: &searchv1.SyncSourceSelector{
				MatchTags: map[string]string{"region": "us-east"},
			},
		},
		{
			ClusterName: "eu-west-k8s",
			SyncSourceSelector: &searchv1.SyncSourceSelector{
				MatchTags: map[string]string{"region": "eu-west"},
			},
		},
	}
	user := "search-sync-source"
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "lt-search", Namespace: "mongodb"},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{
						"mongod-east-1.lt.example.com:27017",
						"mongod-west-1.lt.example.com:27017",
					},
				},
				PasswordSecretRef: &userv1.SecretKeyRef{Name: "search-sync-password"},
				Username:          &user,
			},
			Clusters: &clusters,
		},
	}
}

// TestModifierChain_BackCompat_NoClusterName confirms that when clusterName=="" (the
// single-cluster path before per-cluster expansion lands), readPreferenceTags is not
// emitted regardless of spec.clusters[i].syncSourceSelector contents.
func TestModifierChain_BackCompat_NoClusterName(t *testing.T) {
	search := newQ2MCSearch()
	hostSeeds := []string{"mongod-east-1.lt.example.com:27017", "mongod-west-1.lt.example.com:27017"}

	cfg := mongot.Config{}
	mongot.Apply(
		baseMongotConfig(search, hostSeeds),
		readPreferenceTagsMod(search, ""),
		syncSourceHostsMod(search, ""),
	)(&cfg)

	assert.Nil(t, cfg.SyncSource.ReplicaSet.ReadPreferenceTags, "no clusterName should not emit readPreferenceTags")
	assert.Equal(t, hostSeeds, cfg.SyncSource.ReplicaSet.HostAndPort, "no clusterName should not override hostAndPort")
}

// TestModifierChain_RendersReadPrefTags_ForCluster proves the integration: when a unit's
// clusterName matches a spec.clusters[i] entry with matchTags set, the rendered mongot
// config carries readPreferenceTags == [{matchTags}].
func TestModifierChain_RendersReadPrefTags_ForCluster(t *testing.T) {
	search := newQ2MCSearch()
	hostSeeds := []string{"mongod-east-1.lt.example.com:27017", "mongod-west-1.lt.example.com:27017"}

	cfg := mongot.Config{}
	mongot.Apply(
		baseMongotConfig(search, hostSeeds),
		readPreferenceTagsMod(search, "us-east-k8s"),
		syncSourceHostsMod(search, "us-east-k8s"),
	)(&cfg)

	assert.Equal(
		t,
		[]map[string]string{{"region": "us-east"}},
		cfg.SyncSource.ReplicaSet.ReadPreferenceTags,
		"matchTags should render into readPreferenceTags",
	)
	assert.Equal(t, hostSeeds, cfg.SyncSource.ReplicaSet.HostAndPort, "matchTags should not override hostAndPort")
}

// TestModifierChain_HostsOverridesSeeds proves syncSourceSelector.hosts replaces the seed list.
func TestModifierChain_HostsOverridesSeeds(t *testing.T) {
	hosts := []string{"mongod-explicit-1:27017", "mongod-explicit-2:27017"}
	clusters := []searchv1.ClusterSpec{
		{
			ClusterName: "us-east-k8s",
			SyncSourceSelector: &searchv1.SyncSourceSelector{
				Hosts: hosts,
			},
		},
	}
	user := "search-sync-source"
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{HostAndPorts: []string{"seed:27017"}},
				PasswordSecretRef:     &userv1.SecretKeyRef{Name: "p"},
				Username:              &user,
			},
			Clusters: &clusters,
		},
	}

	cfg := mongot.Config{}
	mongot.Apply(
		baseMongotConfig(search, []string{"seed:27017"}),
		readPreferenceTagsMod(search, "us-east-k8s"),
		syncSourceHostsMod(search, "us-east-k8s"),
	)(&cfg)

	assert.Nil(t, cfg.SyncSource.ReplicaSet.ReadPreferenceTags)
	assert.Equal(t, hosts, cfg.SyncSource.ReplicaSet.HostAndPort)
}

