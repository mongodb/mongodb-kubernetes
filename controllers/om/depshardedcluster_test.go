package om

import (
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// TestMergeShardedClusterNoExisting that just merges the Sharded cluster into an empty deployment
func TestMergeShardedCluster_New(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard")

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	assert.NoError(t, err)

	require.Len(t, d.getProcesses(), 15)
	require.Len(t, d.getReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.getReplicaSets()[i].Members(), 3)
	}
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), createShards("myShard"))
}

func TestMergeShardedCluster_ProcessesModified(t *testing.T) {
	d := NewDeployment()

	shards := createShards("cluster")

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  createConfigSrvRs("configSrv", false),
		Shards:          shards,
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	// OM "made" some changes (should not be overriden)
	(*d.getProcessByName("pretty0"))["logRotate"] = map[string]int{"sizeThresholdMB": 1000, "timeThresholdHrs": 24}

	// These OM changes must be overriden
	(*d.getProcessByName("configSrv-1")).Args()["sharding"] = map[string]interface{}{"clusterRole": "shardsrv", "archiveMovedChunks": true}
	(*d.getProcessByName("cluster-1-1"))["hostname"] = "rubbish"
	(*d.getProcessByName("pretty2")).SetLogPath("/doesnt/exist")

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  createConfigSrvRs("configSrv", false),
		Shards:          createShards("cluster"),
		Finalizing:      false,
	}

	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	expectedMongosProcesses := createMongosProcesses(3, "pretty", "cluster")
	expectedMongosProcesses[0]["logRotate"] = map[string]int{"sizeThresholdMB": 1000, "timeThresholdHrs": 24}
	expectedConfigrs := createConfigSrvRs("configSrv", true)
	expectedConfigrs.Processes[1].Args()["sharding"] = map[string]interface{}{"clusterRole": "configsvr", "archiveMovedChunks": true}

	require.Len(t, d.getProcesses(), 15)
	checkMongoSProcesses(t, d.getProcesses(), expectedMongosProcesses)
	checkReplicaSet(t, d, expectedConfigrs)
	checkShardedCluster(t, d, NewShardedCluster("cluster", expectedConfigrs.Rs.Name(), shards), createShards("cluster"))
}

func TestMergeShardedCluster_ReplicaSetsModified(t *testing.T) {
	d := NewDeployment()

	shards := createShards("cluster")

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  createConfigSrvRs("configSrv", false),
		Shards:          shards,
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	// OM "made" some changes (should not be overriden)
	(*d.getReplicaSetByName("cluster-0"))["writeConcernMajorityJournalDefault"] = true

	// These OM changes must be overriden
	(*d.getReplicaSetByName("cluster-0"))["protocolVersion"] = util.Int32Ref(2)
	(*d.getReplicaSetByName("configSrv")).addMember(
		NewMongodProcess("foo", "bar", &mdbv1.AdditionalMongodConfig{}, mdbv1.NewStandaloneBuilder().Build().GetSpec(), "", nil, ""), "", automationconfig.MemberOptions{},
	)
	(*d.getReplicaSetByName("cluster-2")).setMembers(d.getReplicaSetByName("cluster-2").Members()[0:2])

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	configRs := createConfigSrvRs("configSrv", false)
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("cluster"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	expectedShards := createShards("cluster")
	expectedShards[0].Rs["writeConcernMajorityJournalDefault"] = true

	require.Len(t, d.getProcesses(), 15)
	require.Len(t, d.getReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.getReplicaSets()[i].Members(), 3)
	}
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), expectedShards)
}

func TestMergeShardedCluster_ShardedClusterModified(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard")

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

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
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	expectedCluster := NewShardedCluster("cluster", configRs.Rs.Name(), shards)
	expectedCluster["managedSharding"] = true
	expectedCluster["collections"] = []map[string]interface{}{{"_id": "some", "unique": true}}

	// Note, that fake replicaset and it's processes haven't disappeared as we passed 'false' to 'MergeShardedCluster'
	// which results in "draining" for redundant shards but not physical removal of replica sets
	require.Len(t, d.getProcesses(), 18)
	require.Len(t, d.getReplicaSets(), 5)
	for i := 0; i < 4; i++ {
		require.Len(t, d.getReplicaSets()[i].Members(), 3)
	}
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, expectedCluster, createShards("myShard"))
}

// TestMergeShardedCluster_ShardAdded checks the scenario of incrementing the number of shards
func TestMergeShardedCluster_ShardsAdded(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("cluster")
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	shards = createSpecificNumberOfShards(5, "cluster")
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards)
}

// TestMergeShardedCluster_ShardRemoved checks the scenario of decrementing the number of shards
// It creates a sharded cluster with 5 shards and scales down to 3
func TestMergeShardedCluster_ShardsRemoved(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createSpecificNumberOfShards(5, "cluster")

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(5, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	// On first merge the redundant replica sets and processes are not removed, but 'draining' array is populated
	shards = createSpecificNumberOfShards(3, "cluster")
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	expectedCluster := NewShardedCluster("cluster", configRs.Rs.Name(), shards)
	expectedCluster.setDraining([]string{"cluster-3", "cluster-4"})
	checkShardedClusterCheckExtraReplicaSets(t, d, expectedCluster, shards, false)

	// On second merge the redundant rses and processes will be removed ('draining' array is gone as well)
	shards = createSpecificNumberOfShards(3, "cluster")
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      true,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkShardedClusterCheckExtraReplicaSets(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards, true)
}

// TestMergeShardedCluster_MongosCountChanged checks the scenario of incrementing and decrementing the number of mongos
func TestMergeShardedCluster_MongosCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(4, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}

	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(4, "pretty", "cluster"))

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(2, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(2, "pretty", "cluster"))
}

// TestMergeShardedCluster_MongosCountChanged checks the scenario of incrementing and decrementing the number of replicas
// in config server
func TestMergeShardedCluster_ConfigSrvCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))

	configRs = createConfigSrvRsCount(6, "configSrv", false)
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(4, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkReplicaSet(t, d, createConfigSrvRsCount(6, "configSrv", true))

	configRs = createConfigSrvRsCount(2, "configSrv", false)

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(4, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkReplicaSet(t, d, createConfigSrvRsCount(2, "configSrv", true))
}

func TestMergeShardedCluster_ScaleUpShardMergeFirstProcess(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	// creating other cluster to make sure no side effects occur

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "anotherCluster",
		MongosProcesses: createMongosProcesses(3, "anotherMongos", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("anotherClusterSh"),
		Finalizing:      false,
	}

	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	// Emulating changes to current shards by OM
	for _, s := range d.getShardedClusters()[0].shards() {
		shardRs := d.getReplicaSetByName(s.rs())
		for _, m := range shardRs.Members() {
			process := d.getProcessByName(m.Name())
			process.Args()["security"] = map[string]interface{}{"clusterAuthMode": "sendX509"}
		}
	}

	// Now we "scale up" mongods from 3 to 4
	shards := createShardsSpecificNumberOfMongods(4, "myShard")
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

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
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	mergeOpts.Name = "other"
	mergeOpts.MongosProcesses = createMongosProcesses(3, "otherMongos", "")
	mergeOpts.Shards = createShards("otherSh")

	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	// Emulating changes to current mongoses by OM
	for _, m := range d.getMongosProcessesNames("cluster") {
		process := d.getProcessByName(m)
		process.Args()["security"] = map[string]interface{}{"clusterAuthMode": "sendX509"}
	}

	// Now we "scale up" mongoses from 3 to 5
	mongoses := createMongosProcesses(5, "pretty", "")

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: mongoses,
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}

	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

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
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	configRs2 := createConfigSrvRs("otherConfigSrv", false)
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "otherCluster",
		MongosProcesses: createMongosProcesses(3, "ugly", ""),
		ConfigServerRs:  configRs2,
		Shards:          createShards("otherShard"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	mergeStandalone(d, createStandalone())

	rs := mergeReplicaSet(d, "fooRs", createReplicaSetProcesses("fooRs"))

	err = d.RemoveShardedClusterByName("otherCluster", zap.S())
	assert.NoError(t, err)

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
