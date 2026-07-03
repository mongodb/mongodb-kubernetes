package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
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

	require.Len(t, d.GetProcesses(), 15)
	require.Len(t, d.GetReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.GetReplicaSets()[i].Members(), 3)
	}
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards, nil), createShards("myShard"))
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

	require.Len(t, d.GetProcesses(), 15)
	checkMongoSProcesses(t, d.GetProcesses(), expectedMongosProcesses)
	checkReplicaSet(t, d, expectedConfigrs)
	checkShardedCluster(t, d, NewShardedCluster("cluster", expectedConfigrs.Rs.Name(), shards, nil), createShards("cluster"))
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
		NewMongodProcess("foo", "bar", "fake-mongoDBImage", false, &mdbv1.AdditionalMongodConfig{}, mdbv1.NewStandaloneBuilder().Build().GetSpec(), "", nil, ""), nil, automationconfig.MemberOptions{},
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

	require.Len(t, d.GetProcesses(), 15)
	require.Len(t, d.GetReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.GetReplicaSets()[i].Members(), 3)
	}
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards, nil), expectedShards)
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
	(*d.getShardedClusterByName("cluster")).setShards(d.getShardedClusterByName("cluster").Shards()[0:2])
	(*d.getShardedClusterByName("cluster")).setShards(append(d.getShardedClusterByName("cluster").Shards(), newShard("fakeShard", "fakeShard")))

	mergeReplicaSet(d, "fakeShard", createReplicaSetProcesses("fakeShard"))

	require.Len(t, d.GetReplicaSets(), 5)

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

	expectedCluster := NewShardedCluster("cluster", configRs.Rs.Name(), shards, nil)
	expectedCluster["managedSharding"] = true
	expectedCluster["collections"] = []map[string]interface{}{{"_id": "some", "unique": true}}
	// The redundant shard is scheduled for draining even though its name does not match the pattern.
	expectedCluster.setDraining([]string{"fakeShard"})

	// Note, that fake replicaset and it's processes haven't disappeared as we passed 'false' to 'MergeShardedCluster'
	// which results in "draining" for redundant shards but not physical removal of replica sets
	require.Len(t, d.GetProcesses(), 18)
	require.Len(t, d.GetReplicaSets(), 5)
	for i := 0; i < 4; i++ {
		require.Len(t, d.GetReplicaSets()[i].Members(), 3)
	}
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(3, "pretty", "cluster"))
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
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards, nil), shards)
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

	expectedCluster := NewShardedCluster("cluster", configRs.Rs.Name(), shards, nil)
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
	checkShardedClusterCheckExtraReplicaSets(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards, nil), shards, true)
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
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(3, "pretty", "cluster"))

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(4, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}

	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(4, "pretty", "cluster"))

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(2, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("myShard"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(2, "pretty", "cluster"))
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
	for _, s := range d.getShardedClusters()[0].Shards() {
		shardRs := d.getReplicaSetByName(s.Rs())
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

	expectedCluster := NewShardedCluster("cluster", configRs.Rs.Name(), expectedShards, nil)
	checkShardedCluster(t, d, expectedCluster, expectedShards)

	expectedAnotherCluster := NewShardedCluster("anotherCluster", configRs.Rs.Name(), createShards("anotherClusterSh"), nil)
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

	checkMongoSProcesses(t, d.GetProcesses(), expectedMongoses)

	// "other" mongoses stayed untouched
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(3, "otherMongos", "other"))

	totalMongos := 0
	for _, p := range d.GetProcesses() {
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
	checkMongoSProcesses(t, d.GetProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	shards := createShards("myShard")
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards, nil), shards)

	// Then check that the sharded cluster and all replica sets were removed
	shards2 := createShards("otherShard")
	checkShardedClusterRemoved(t, d, NewShardedCluster("otherCluster", configRs2.Rs.Name(), shards2, nil), createConfigSrvRs("otherConfigSrv", false), shards2)
}

// TestMergeShardedCluster_ConfigServerExternalMembersPreserved verifies that an external member
// already present in the config server replica set is not removed during a merge when it is
// declared via ConfigServerExternalMembers.
func TestMergeShardedCluster_ConfigServerExternalMembersPreserved(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard")

	// Perform an initial merge to populate the deployment with the config server RS, shards, and mongos.
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)

	// Simulate an external member that was added directly in Ops Manager (not managed by the operator).
	extMember := ReplicaSetMember{}
	extMember["host"] = "external-cfg-0:27017"
	extMember["_id"] = 10
	extMember["votes"] = 1
	extMember["priority"] = float32(1)
	extMember["tags"] = map[string]string{}
	cfgRs := d.getReplicaSetByName("configSrv")
	require.NotNil(t, cfgRs)
	cfgRs.setMembers(append(cfgRs.Members(), extMember))

	// Second merge: operator still only knows the original 3 members.
	// Declare the external member so it is preserved rather than removed.
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  createConfigSrvRs("configSrv", false),
		Shards:          createShards("myShard"),
		Finalizing:      false,
		ExternalCluster: ExternalShardedCluster{ConfigServerMembers: []string{"external-cfg-0:27017"}},
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)

	// The config server RS should retain the external member.
	resultCfgRs := d.getReplicaSetByName("configSrv")
	require.NotNil(t, resultCfgRs)
	memberHosts := make([]string, 0, len(resultCfgRs.Members()))
	for _, m := range resultCfgRs.Members() {
		memberHosts = append(memberHosts, m.Name())
	}
	assert.Contains(t, memberHosts, "external-cfg-0:27017")
	assert.Len(t, resultCfgRs.Members(), 4)
}

// TestMergeShardedCluster_ShardExternalMembersPreserved verifies that an external member
// already present in a shard replica set is not removed during a merge when it is declared
// via ShardExternalMembers for that shard index.
func TestMergeShardedCluster_ShardExternalMembersPreserved(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard")

	// Perform an initial merge to populate the deployment with the config server RS, shards, and mongos.
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)

	// Simulate an external member that was added directly in Ops Manager to shard 0.
	extMember := ReplicaSetMember{}
	extMember["host"] = "external-shard-0:27017"
	extMember["_id"] = 10
	extMember["votes"] = 1
	extMember["priority"] = float32(1)
	extMember["tags"] = map[string]string{}
	shard0Rs := d.getReplicaSetByName("myShard-0")
	require.NotNil(t, shard0Rs)
	shard0Rs.setMembers(append(shard0Rs.Members(), extMember))

	// Second merge: operator still only knows the original 3 members per shard.
	// Declare the external member for shard 0 so it is preserved rather than removed.
	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "pretty", ""),
		ConfigServerRs:  createConfigSrvRs("configSrv", false),
		Shards:          createShards("myShard"),
		Finalizing:      false,
		ExternalCluster: ExternalShardedCluster{ShardMembers: [][]string{{"external-shard-0:27017"}}},
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)

	// Shard 0 should retain the external member.
	resultShard0Rs := d.getReplicaSetByName("myShard-0")
	require.NotNil(t, resultShard0Rs)
	memberHosts := make([]string, 0, len(resultShard0Rs.Members()))
	for _, m := range resultShard0Rs.Members() {
		memberHosts = append(memberHosts, m.Name())
	}
	assert.Contains(t, memberHosts, "external-shard-0:27017")
	assert.Len(t, resultShard0Rs.Members(), 4)
}

// TestMergeShardedCluster_ScaleDownOverriddenShardRsName verifies the two phase removal of a shard
// whose replica set name does not match the <prefix>-<idx> pattern (full form ShardNameOverride).
func TestMergeShardedCluster_ScaleDownOverriddenShardRsName(t *testing.T) {
	d := NewDeployment()
	configRs := createConfigSrvRs("configSrv", false)
	shards := append(createSpecificNumberOfShards(1, "sc"), createSpecificNumberOfShards(1, "vmshard")...)

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "sc",
		MongosProcesses: createMongosProcesses(3, "mongos", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	shardsRemoving, err := d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)
	assert.False(t, shardsRemoving)
	require.Len(t, d.getShardedClusterByName("sc").Shards(), 2)

	// Scale down removing the shard whose rs name does not match the "sc" prefix.
	mergeOpts.Shards = shards[0:1]
	shardsRemoving, err = d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)
	assert.True(t, shardsRemoving)
	assert.Equal(t, []string{"vmshard-0"}, d.getShardedClusterByName("sc").draining())
	// The orphaned replica set is not counted as excess while draining.
	assert.Equal(t, 0, d.GetNumberOfExcessProcesses("sc", nil))

	// Finalizing pass physically removes the drained replica set and its processes.
	mergeOpts.Finalizing = true
	shardsRemoving, err = d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)
	assert.False(t, shardsRemoving)
	assert.Nil(t, d.GetReplicaSetByName("vmshard-0"))
	assert.Empty(t, d.getShardedClusterByName("sc").draining())
	assert.Equal(t, 0, d.GetNumberOfExcessProcesses("sc", nil))
}

// TestMergeShardedCluster_UnrelatedReplicaSetMatchingShardPattern verifies that a replica set which
// was never a shard of the cluster is left untouched even when its name looks like a shard name.
// Removed shards are tracked through the draining array, ownership is never guessed from names.
func TestMergeShardedCluster_UnrelatedReplicaSetMatchingShardPattern(t *testing.T) {
	d := NewDeployment()
	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("cluster")

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "cluster",
		MongosProcesses: createMongosProcesses(3, "mongos", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)

	// A replica set matching the shard naming pattern which never belonged to the sharding section.
	mergeReplicaSet(d, "cluster-5", createReplicaSetProcesses("cluster-5"))

	shardsRemoving, err := d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)
	assert.False(t, shardsRemoving)
	assert.Empty(t, d.getShardedClusterByName("cluster").draining())
	assert.NotNil(t, d.GetReplicaSetByName("cluster-5"))

	// The unrelated replica set does not belong to the sharded cluster so its processes are excess.
	assert.Equal(t, 3, d.GetNumberOfExcessProcesses("cluster", nil))

	// A finalizing merge does not remove it either.
	mergeOpts.Finalizing = true
	shardsRemoving, err = d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)
	assert.False(t, shardsRemoving)
	assert.NotNil(t, d.GetReplicaSetByName("cluster-5"))
}

// TestGetShardedClusterShardProcessNamesByRs verifies the lookup by replica set name stays correct
// when the AC shards array order (sorted by _id) differs from the shard index order.
func TestGetShardedClusterShardProcessNamesByRs(t *testing.T) {
	d := NewDeployment()
	configRs := createConfigSrvRs("configSrv", false)
	shards := createSpecificNumberOfShards(2, "foo")
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "foo",
		MongosProcesses: createMongosProcesses(1, "mongos", ""),
		ConfigServerRs:  configRs,
		Shards:          shards,
		ExternalCluster: ExternalShardedCluster{ShardIds: []string{"zebra", "alpha"}},
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)
	// A second merge sorts the shards array by _id: [alpha (foo-1), zebra (foo-0)].
	_, err = d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)
	require.Equal(t, "foo-1", d.getShardedClusterByName("foo").Shards()[0].Rs())

	// Positional access would return foo-1 processes for index 0. Lookup by rs name is unaffected.
	processNames := d.GetShardedClusterShardProcessNamesByRs("foo", "foo-0")
	require.NotEmpty(t, processNames)
	for _, p := range processNames {
		assert.Contains(t, p, "foo-0-")
	}
	assert.Nil(t, d.GetShardedClusterShardProcessNamesByRs("foo", "unknown"))
	assert.Nil(t, d.GetShardedClusterShardProcessNamesByRs("unknown", "foo-0"))
}

// TestRemoveShardedClusterByName_OverriddenShardIds verifies shard replica sets are removed by replica
// set name even when the shard _id differs from it, as is the case with full form ShardNameOverrides.
func TestRemoveShardedClusterByName_OverriddenShardIds(t *testing.T) {
	d := NewDeployment()
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "foo",
		MongosProcesses: createMongosProcesses(2, "mongos", ""),
		ConfigServerRs:  createConfigSrvRs("configSrv", false),
		Shards:          createSpecificNumberOfShards(2, "foo"),
		ExternalCluster: ExternalShardedCluster{ShardIds: []string{"vm-id-0", "vm-id-1"}},
	}
	_, err := d.MergeShardedCluster(mergeOpts)
	require.NoError(t, err)

	err = d.RemoveShardedClusterByName("foo", zap.S())
	require.NoError(t, err)

	assert.Empty(t, d.getShardedClusters())
	assert.Empty(t, d.GetReplicaSets())
	assert.Empty(t, d.GetProcesses())
}
