package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

// newShardedSourceMDB builds a minimal single-cluster sharded MongoDB named "sc" in "ns".
func newShardedSourceMDB(shardCount, mongodsPerShard, mongosCount int) *mdbv1.MongoDB {
	return &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "sc", Namespace: "ns"},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version:      "8.2.0-ent",
				ResourceType: mdbv1.ShardedCluster,
			},
			MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{
				ShardCount:           shardCount,
				MongodsPerShardCount: mongodsPerShard,
				MongosCount:          mongosCount,
				ConfigServerCount:    1,
			},
		},
	}
}

func TestShardedInternalSearchSource_HostSeeds_InternalOnly(t *testing.T) {
	sc := newShardedSourceMDB(1, 2, 1)
	seeds, err := NewShardedInternalSearchSource(sc, nil).HostSeeds("sc-0")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"sc-0-0.sc-sh.ns.svc.cluster.local:27017",
		"sc-0-1.sc-sh.ns.svc.cluster.local:27017",
	}, seeds)
}

func TestShardedInternalSearchSource_HostSeeds_ExternalOnly(t *testing.T) {
	sc := newShardedSourceMDB(1, 0, 0)
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-shard-0", Hostname: "vm-shard-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-0"},
		{ProcessName: "vm-shard-1", Hostname: "vm-shard-1.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-0"},
	}
	seeds, err := NewShardedInternalSearchSource(sc, nil).HostSeeds("sc-0")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"vm-shard-0.example.com:27017",
		"vm-shard-1.example.com:27017",
	}, seeds)
}

func TestShardedInternalSearchSource_HostSeeds_Mixed(t *testing.T) {
	sc := newShardedSourceMDB(1, 1, 1)
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-shard-0", Hostname: "vm-shard-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-0"},
	}
	seeds, err := NewShardedInternalSearchSource(sc, nil).HostSeeds("sc-0")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"sc-0-0.sc-sh.ns.svc.cluster.local:27017",
		"vm-shard-0.example.com:27017",
	}, seeds)
}

// The migration tool emits shardNameOverrides whenever the VM replica set name differs from the
// K8s shard name, so this is the shape a real migration produces. HostSeeds is called with the
// K8s name but external members are recorded under the AC replica set name.
func TestShardedInternalSearchSource_HostSeeds_HonoursShardNameOverrides(t *testing.T) {
	sc := newShardedSourceMDB(1, 0, 0)
	sc.Spec.ShardNameOverrides = []mdbv1.ShardNameOverride{
		{ShardName: "sc-0", ReplicaSetName: "vm-shard-0"},
	}
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-shard-0-0", Hostname: "vm-shard-0-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "vm-shard-0"},
	}
	seeds, err := NewShardedInternalSearchSource(sc, nil).HostSeeds("sc-0")
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-shard-0-0.example.com:27017"}, seeds)
}

// External members of a different shard must not leak into this shard's seeds.
func TestShardedInternalSearchSource_HostSeeds_IsolatesShards(t *testing.T) {
	sc := newShardedSourceMDB(2, 0, 0)
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-a", Hostname: "vm-a.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-0"},
		{ProcessName: "vm-b", Hostname: "vm-b.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-1"},
	}
	src := NewShardedInternalSearchSource(sc, nil)

	seeds0, err := src.HostSeeds("sc-0")
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-a.example.com:27017"}, seeds0)

	seeds1, err := src.HostSeeds("sc-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-b.example.com:27017"}, seeds1)
}

// Config-server external members are mongods too; mongot never syncs from them.
func TestShardedInternalSearchSource_HostSeeds_ExcludesConfigServerMembers(t *testing.T) {
	sc := newShardedSourceMDB(1, 0, 0)
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-cfg-0", Hostname: "vm-cfg-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-config"},
	}
	seeds, err := NewShardedInternalSearchSource(sc, nil).HostSeeds("sc-0")
	require.NoError(t, err)
	assert.Empty(t, seeds)
}

func TestShardedInternalSearchSource_MongosHostsAndPorts_InternalOnly(t *testing.T) {
	sc := newShardedSourceMDB(1, 2, 2)
	assert.Equal(t,
		[]string{"sc-svc.ns.svc.cluster.local:27017"},
		NewShardedInternalSearchSource(sc, nil).MongosHostsAndPorts())
}

func TestShardedInternalSearchSource_MongosHostsAndPorts_ExternalOnly(t *testing.T) {
	sc := newShardedSourceMDB(1, 0, 0)
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-mongos-0", Hostname: "vm-mongos-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongos},
		{ProcessName: "vm-shard-0", Hostname: "vm-shard-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-0"},
	}
	// The K8s mongos Service is omitted while mongosCount == 0: it would resolve to no endpoints.
	assert.Equal(t,
		[]string{"vm-mongos-0.example.com:27017"},
		NewShardedInternalSearchSource(sc, nil).MongosHostsAndPorts())
}

func TestShardedInternalSearchSource_MongosHostsAndPorts_Mixed(t *testing.T) {
	sc := newShardedSourceMDB(1, 1, 1)
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-mongos-0", Hostname: "vm-mongos-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongos},
	}
	assert.Equal(t,
		[]string{"sc-svc.ns.svc.cluster.local:27017", "vm-mongos-0.example.com:27017"},
		NewShardedInternalSearchSource(sc, nil).MongosHostsAndPorts())
}

// mongodsPerShardCount == 0 is legal mid-migration and must not fail validation.
func TestShardedInternalSearchSource_Validate_AllowsZeroMongodsPerShardDuringMigration(t *testing.T) {
	sc := newShardedSourceMDB(1, 0, 0)
	sc.Spec.ExternalMembers = []mdbv1.ExternalMember{
		{ProcessName: "vm-shard-0", Hostname: "vm-shard-0.example.com:27017", Type: mdbv1.ExternalMemberTypeMongod, ReplicaSetName: "sc-0"},
	}
	assert.NoError(t, NewShardedInternalSearchSource(sc, nil).Validate())
}
