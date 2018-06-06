package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergeShardedClusterNoExisting that just merges the Sharded cluster into an empty deployment
func TestMergeShardedCluster_New(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard")

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)

	require.Len(t, d.getProcesses(), 15)
	require.Len(t, d.getReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.getReplicaSets()[i].members(), 3)
	}
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), createShards("myShard"))
}

func TestMergeShardedCluster_ProcessesModified(t *testing.T) {
	d := NewDeployment()

	shards := createShards("myShard")

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), createConfigSrvRs("configSrv", false), shards)

	// OM "made" some changes (should not be overriden)
	(*d.getProcessByName("pretty0"))["logRotate"] = map[string]int{"sizeThresholdMB": 1000, "timeThresholdHrs": 24}

	// These OM changes must be overriden
	(*d.getProcessByName("configSrv1")).Args()["sharding"] = map[string]interface{}{"clusterRole": "shardsrv", "archiveMovedChunks": true}
	(*d.getProcessByName("myShard11"))["hostname"] = "rubbish"
	(*d.getProcessByName("pretty2")).SetLogPath("/doesnt/exist")

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), createConfigSrvRs("configSrv", false), createShards("myShard"))

	expectedMongosProcesses := createMongosProcesses(3, "pretty", "cluster")
	expectedMongosProcesses[0]["logRotate"] = map[string]int{"sizeThresholdMB": 1000, "timeThresholdHrs": 24}
	expectedConfigrs := createConfigSrvRs("configSrv", true)
	expectedConfigrs.Processes[1].Args()["sharding"] = map[string]interface{}{"clusterRole": "configsvr", "archiveMovedChunks": true}

	require.Len(t, d.getProcesses(), 15)
	checkMongoSProcesses(t, d.getProcesses(), expectedMongosProcesses)
	checkReplicaSet(t, d, expectedConfigrs)
	checkShardedCluster(t, d, NewShardedCluster("cluster", expectedConfigrs.Rs.Name(), shards), createShards("myShard"))
}

func TestMergeShardedCluster_ReplicaSetsModified(t *testing.T) {
	d := NewDeployment()

	shards := createShards("myShard")

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), createConfigSrvRs("configSrv", false), shards)

	// OM "made" some changes (should not be overriden)
	(*d.getReplicaSetByName("myShard0"))["protocolVersion"] = 1

	// These OM changes must be overriden
	(*d.getReplicaSetByName("configSrv")).addMember(NewMongodProcess("foo", "bar", "4.0.0"))
	(*d.getReplicaSetByName("myShard2")).setMembers(d.getReplicaSetByName("myShard2").members()[0:2])

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard"))

	expectedShards := createShards("myShard")
	expectedShards[0].Rs["protocolVersion"] = 1

	require.Len(t, d.getProcesses(), 15)
	require.Len(t, d.getReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.getReplicaSets()[i].members(), 3)
	}
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), expectedShards)
}

func TestMergeShardedCluster_ShardedClusterModified(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard")

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)

	// OM "made" some changes (should not be overriden)
	(*d.getShardedClusterByName("cluster"))["managedSharding"] = true
	(*d.getShardedClusterByName("cluster"))["collections"] = []map[string]interface{}{{"_id": "some", "unique": true}}

	// These OM changes must be overriden
	(*d.getShardedClusterByName("cluster")).setConfigServerRsName("fake")
	(*d.getShardedClusterByName("cluster")).setShards(d.getShardedClusterByName("cluster").shards()[0:2])
	(*d.getShardedClusterByName("cluster")).setShards(append(d.getShardedClusterByName("cluster").shards(), newShard("fakeShard")))

	mergeReplicaSet(d, "fakeShard", createReplicaSetProcesses("fakeShard"))

	require.Len(t, d.getReplicaSets(), 5)

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	configRs = createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard"))

	expectedCluster := NewShardedCluster("cluster", configRs.Rs.Name(), shards)
	expectedCluster["managedSharding"] = true
	expectedCluster["collections"] = []map[string]interface{}{{"_id": "some", "unique": true}}

	require.Len(t, d.getProcesses(), 15)
	require.Len(t, d.getReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.getReplicaSets()[i].members(), 3)
	}
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, expectedCluster, createShards("myShard"))
}

// TestMergeShardedCluster_ShardAdded checks the scenario of incrementing and decrementing the number of shards
func TestMergeShardedCluster_ShardCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard")
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)

	shards = createSpecificNumberOfShards(5, "myShard")
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards)

	shards = createSpecificNumberOfShards(2, "myShard")
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards)
}

// TestMergeShardedCluster_MongosCountChanged checks the scenario of incrementing and decrementing the number of mongos
func TestMergeShardedCluster_MongosCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard"))
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))

	d.MergeShardedCluster("cluster", createMongosProcesses(4, "pretty", ""), configRs, createShards("myShard"))
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(4, "pretty", "cluster"))

	d.MergeShardedCluster("cluster", createMongosProcesses(2, "pretty", ""), configRs, createShards("myShard"))
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(2, "pretty", "cluster"))
}

// TestMergeShardedCluster_MongosCountChanged checks the scenario of incrementing and decrementing the number of replicas
// in config server
func TestMergeShardedCluster_ConfigSrvCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))

	configRs = createConfigSrvRsCount(6, "configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(4, "pretty", ""), configRs, createShards("myShard"))
	checkReplicaSet(t, d, createConfigSrvRsCount(6, "configSrv", true))

	configRs = createConfigSrvRsCount(2, "configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(4, "pretty", ""), configRs, createShards("myShard"))
	checkReplicaSet(t, d, createConfigSrvRsCount(2, "configSrv", true))
}

func TestMergeShardedCluster_ScaleUpShardMergeFirstProcess(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard"))
	// creating other cluster to make sure no side effects occur
	d.MergeShardedCluster("anotherCluster", createMongosProcesses(3, "anotherMongos", ""), configRs, createShards("anotherClusterSh"))

	// Emulating changes to current shards by OM
	for _, s := range d.getShardedClusters()[0].shards() {
		shardRs := d.getReplicaSetByName(s.rs())
		for _, m := range shardRs.members() {
			process := d.getProcessByName(m.Name())
			process.Args()["security"] = map[string]interface{}{"clusterAuthMode": "sendX509"}
		}
	}

	// Now we "scale up" mongods from 3 to 4
	shards := createShardsSpecificNumberOfMongods(4, "myShard")

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)

	expectedShards := createShardsSpecificNumberOfMongods(4, "myShard")

	for _, s := range expectedShards {
		for _, p := range s.Processes {
			p.Args()["security"] = map[string]interface{}{"clusterAuthMode": "sendX509"}
		}
	}

	expectedCluster := NewShardedCluster("cluster", configRs.Rs.Name(), expectedShards)
	checkShardedCluster(t, d, expectedCluster, expectedShards)

	expectedAnotherCluster := NewShardedCluster("anotherCluster", configRs.Rs.Name(), createShards("anotherClusterSh"))
	checkShardedCluster(t, d, expectedAnotherCluster, createShards("anotherClusterSh"))
}

func TestMergeShardedCluster_ScaleUpMongosMergeFirstProcess(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard"))
	d.MergeShardedCluster("other", createMongosProcesses(3, "otherMongos", ""), configRs, createShards("otherSh"))

	// Emulating changes to current mongoses by OM
	for _, m := range d.getMongosProcessesNames("cluster") {
		process := d.getProcessByName(m)
		process.Args()["security"] = map[string]interface{}{"clusterAuthMode": "sendX509"}
	}

	// Now we "scale up" mongoses from 3 to 5
	mongoses := createMongosProcesses(5, "pretty", "")
	d.MergeShardedCluster("cluster", mongoses, configRs, createShards("myShard"))

	expectedMongoses := createMongosProcesses(5, "pretty", "cluster")
	for _, p := range expectedMongoses {
		p.Args()["security"] = map[string]interface{}{"clusterAuthMode": "sendX509"}
	}

	checkMongoSProcesses(t, d.getProcesses(), expectedMongoses)

	// "other" mongoses stayed untouched
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "otherMongos", "other"))

	totalMongos := 0
	for _, p := range d.getProcesses() {
		if p.ProcessType() == ProcessTypeMongos {
			totalMongos++
		}
	}
	assert.Equal(t, 8, totalMongos)
}

// TestRemoveShardedClusterByName checks that sharded cluster and all linked artifacts are removed - but existing objects
// should stay untouched
func TestRemoveShardedClusterByName(t *testing.T) {
	d := NewDeployment()
	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard"))

	configRs2 := createConfigSrvRs("otherConfigSrv", false)
	d.MergeShardedCluster("otherCluster", createMongosProcesses(3, "ugly", ""), configRs2, createShards("otherShard"))

	mergeStandalone(d, createStandalone())

	rs := mergeReplicaSet(d, "fooRs", createReplicaSetProcesses("fooRs"))

	d.RemoveShardedClusterByName("otherCluster")

	// First check that all other entities stay untouched
	checkProcess(t, d, createStandalone())
	checkReplicaSet(t, d, rs)
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	shards := createShards("myShard")
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards)

	// Then check that the sharded cluster and all replica sets were removed
	shards2 := createShards("otherShard")
	checkShardedClusterRemoved(t, d, NewShardedCluster("otherCluster", configRs2.Rs.Name(), shards2), createConfigSrvRs("otherConfigSrv", false), shards2)
}
