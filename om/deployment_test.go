package om

import (
	"strconv"
	"testing"

	"strings"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestSerialize(t *testing.T) {
	//deployment := newDeployment("3.6.3")
	//standalone := (NewProcess("3.6.3")).HostPort("mongo1.some.host").Name("merchantsStandalone").
	//	DbPath("/data/mongodb").LogPath("/data/mongodb/mongodb.log")
	//deployment.mergeStandalone(standalone)
	//
	//data, _ := json.Marshal(deployment)
	//// todo check against serialized content
	//fmt.Printf("%s", string(data))
}

// First time merge adds the new standalone
// second invocation doesn't add new node as the existing standalone is found (by name) and the data is merged
func TestMergeStandalone(t *testing.T) {
	d := NewDeployment()
	standalone := createStandalone()
	d.MergeStandalone(standalone, nil)

	assert.Len(t, d.getProcesses(), 1)

	d["version"] = 5
	d.getProcesses()[0]["alias"] = "alias"
	d.getProcesses()[0]["hostname"] = "foo"

	d.MergeStandalone(createStandalone(), nil)

	assert.Len(t, d.getProcesses(), 1)

	// fields which are owned by OM-Kube should be overriden back
	assert.Equal(t, d.getProcesses()[0].HostName(), "mongo1.some.host")

	// fields which are not owned by OM-Kube should be left unchanged
	assert.Equal(t, d.getProcesses()[0]["alias"], "alias")
	assert.Equal(t, d["version"], 5)
}

// First merge results in just adding the ReplicaSet
// Second merge performs real merge operation
func TestMergeReplicaSet(t *testing.T) {
	d := NewDeployment()

	d.MergeReplicaSet(NewReplicaSetWithProcesses(NewReplicaSet("fooRs"), createReplicaSetProcesses("fooRs")), nil)
	expectedRs := buildRsByProcesses("fooRs", createReplicaSetProcesses("fooRs"))

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets(), 1)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)
	assert.Equal(t, d.getReplicaSets()[0], expectedRs)

	// Now the deployment "gets updated" from external - new node is added and one is removed - this should be fixed
	// by merge
	d.getProcesses()[0]["processType"] = "mongos"                                             // this will be overriden
	d.getProcesses()[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"] = 20 // this will be left as-is
	d.getReplicaSets()[0].setMembers(d.getReplicaSets()[0].members()[0:2])                    // "removing" the last node in replicaset
	d.getReplicaSets()[0].addMember(NewMongodProcess("foo", "bar", "4.0.0"))                  // "adding" some new node
	d.getReplicaSets()[0].members()[0]["arbiterOnly"] = true                                  // changing data for first node

	d.MergeReplicaSet(NewReplicaSetWithProcesses(NewReplicaSet("fooRs"), createReplicaSetProcesses("fooRs")), nil)

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets(), 1)
	assert.Equal(t, d.getProcesses()[0]["processType"], ProcessTypeMongod)
	assert.Equal(t, d.getProcesses()[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"], 20)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)

	expectedRs = buildRsByProcesses("fooRs", createReplicaSetProcesses("fooRs"))
	expectedRs.members()[0]["arbiterOnly"] = true
	assert.Equal(t, d.getReplicaSets()[0], expectedRs)
}

// Checking that on scale down the old processes are removed
func TestMergeReplicaSetScaleDown(t *testing.T) {
	d := NewDeployment()

	d.MergeReplicaSet(NewReplicaSetWithProcesses(NewReplicaSet("someRs"), createReplicaSetProcesses("someRs")), nil)
	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)

	// "scale down"
	scaledDownRsProcesses := createReplicaSetProcesses("someRs")[0:2]
	d.MergeReplicaSet(NewReplicaSetWithProcesses(NewReplicaSet("someRs"), scaledDownRsProcesses), nil)

	assert.Len(t, d.getProcesses(), 2)
	assert.Len(t, d.getReplicaSets()[0].members(), 2)

	// checking that the last member was removed
	rsProcesses := createReplicaSetProcessesCheck("someRs", true)
	assert.Contains(t, d.getProcesses(), rsProcesses[0])
	assert.Contains(t, d.getProcesses(), rsProcesses[1])
	assert.NotContains(t, d.getProcesses(), rsProcesses[2])
}

// TestMergeShardedClusterNoExisting that just merges the Sharded cluster into an empty deployment
func TestMergeShardedCluster_New(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard", false)

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)

	require.Len(t, d.getProcesses(), 15)
	require.Len(t, d.getReplicaSets(), 4)
	for i := 0; i < 4; i++ {
		require.Len(t, d.getReplicaSets()[i].members(), 3)
	}
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), createShards("myShard", true))
}

func TestMergeShardedCluster_ProcessesModified(t *testing.T) {
	d := NewDeployment()

	shards := createShards("myShard", false)

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), createConfigSrvRs("configSrv", false), shards)

	// OM "made" some changes (should not be overriden)
	(*d.getProcessByName("pretty0"))["logRotate"] = map[string]int{"sizeThresholdMB": 1000, "timeThresholdHrs": 24}

	// These OM changes must be overriden
	(*d.getProcessByName("configSrv1")).Args()["sharding"] = map[string]interface{}{"clusterRole": "shardsrv", "archiveMovedChunks": true}
	(*d.getProcessByName("myShard11"))["hostname"] = "rubbish"
	(*d.getProcessByName("pretty2")).SetLogPath("/doesnt/exist")

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), createConfigSrvRs("configSrv", false), createShards("myShard", false))

	expectedMongosProcesses := createMongosProcesses(3, "pretty", "cluster")
	expectedMongosProcesses[0]["logRotate"] = map[string]int{"sizeThresholdMB": 1000, "timeThresholdHrs": 24}
	expectedConfigrs := createConfigSrvRs("configSrv", true)
	expectedConfigrs.Processes[1].Args()["sharding"] = map[string]interface{}{"clusterRole": "configsvr", "archiveMovedChunks": true}

	require.Len(t, d.getProcesses(), 15)
	checkMongoSProcesses(t, d.getProcesses(), expectedMongosProcesses)
	checkReplicaSet(t, d, expectedConfigrs)
	checkShardedCluster(t, d, NewShardedCluster("cluster", expectedConfigrs.Rs.Name(), shards), createShards("myShard", false))
}

func TestMergeShardedCluster_ReplicaSetsModified(t *testing.T) {
	d := NewDeployment()

	shards := createShards("myShard", false)

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), createConfigSrvRs("configSrv", false), shards)

	// OM "made" some changes (should not be overriden)
	(*d.getReplicaSetByName("myShard0"))["protocolVersion"] = 1

	// These OM changes must be overriden
	(*d.getReplicaSetByName("configSrv")).addMember(NewMongodProcess("foo", "bar", "4.0.0"))
	(*d.getReplicaSetByName("myShard2")).setMembers(d.getReplicaSetByName("myShard2").members()[0:2])

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard", false))

	expectedShards := createShards("myShard", false)
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
	shards := createShards("myShard", false)

	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)

	// OM "made" some changes (should not be overriden)
	(*d.getShardedClusterByName("cluster"))["managedSharding"] = true
	(*d.getShardedClusterByName("cluster"))["collections"] = []map[string]interface{}{{"_id": "some", "unique": true}}

	// These OM changes must be overriden
	(*d.getShardedClusterByName("cluster")).setConfigServerRsName("fake")
	(*d.getShardedClusterByName("cluster")).setShards(d.getShardedClusterByName("cluster").shards()[0:2])
	(*d.getShardedClusterByName("cluster")).setShards(append(d.getShardedClusterByName("cluster").shards(), newShard("fakeShard")))
	d.MergeReplicaSet(NewReplicaSetWithProcesses(NewReplicaSet("fakeShard"), createReplicaSetProcesses("fakeShard")), nil)

	require.Len(t, d.getReplicaSets(), 5)

	// Final check - we create the expected configuration, add there correct OM changes and check for equality with merge
	// result
	configRs = createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard", false))

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
	checkShardedCluster(t, d, expectedCluster, createShards("myShard", false))
}

// TestMergeShardedCluster_ShardAdded checks the scenario of incrementing and decrementing the number of shards
func TestMergeShardedCluster_ShardCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	shards := createShards("myShard", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)

	shards = createSpecificNumberOfShards(5, "myShard", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards)

	shards = createSpecificNumberOfShards(2, "myShard", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, shards)
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards)
}

// TestMergeShardedCluster_MongosCountChanged checks the scenario of incrementing and decrementing the number of mongos
func TestMergeShardedCluster_MongosCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard", false))
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))

	d.MergeShardedCluster("cluster", createMongosProcesses(4, "pretty", ""), configRs, createShards("myShard", false))
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(4, "pretty", "cluster"))

	d.MergeShardedCluster("cluster", createMongosProcesses(2, "pretty", ""), configRs, createShards("myShard", false))
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(2, "pretty", "cluster"))
}

// TestMergeShardedCluster_MongosCountChanged checks the scenario of incrementing and decrementing the number of replicas
// in config server
func TestMergeShardedCluster_ConfigSrvCountChanged(t *testing.T) {
	d := NewDeployment()

	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard", false))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))

	configRs = createConfigSrvRsCount(6, "configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(4, "pretty", ""), configRs, createShards("myShard", false))
	checkReplicaSet(t, d, createConfigSrvRsCount(6, "configSrv", true))

	configRs = createConfigSrvRsCount(2, "configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(4, "pretty", ""), configRs, createShards("myShard", false))
	checkReplicaSet(t, d, createConfigSrvRsCount(2, "configSrv", true))
}

// TestRemoveShardedClusterByName checks that sharded cluster and all linked artifacts are removed - but existing objects
// should stay untouched
func TestRemoveShardedClusterByName(t *testing.T) {
	d := NewDeployment()
	configRs := createConfigSrvRs("configSrv", false)
	d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty", ""), configRs, createShards("myShard", false))

	configRs2 := createConfigSrvRs("otherConfigSrv", false)
	d.MergeShardedCluster("otherCluster", createMongosProcesses(3, "ugly", ""), configRs2, createShards("otherShard", false))

	d.MergeStandalone(createStandalone(), nil)
	rs := NewReplicaSetWithProcesses(NewReplicaSet("fooRs"), createReplicaSetProcesses("fooRs"))
	d.MergeReplicaSet(rs, nil)

	d.RemoveShardedClusterByName("otherCluster")

	// First check that all other entities stay untouched
	checkProcess(t, d, createStandalone())
	checkReplicaSet(t, d, rs)
	checkMongoSProcesses(t, d.getProcesses(), createMongosProcesses(3, "pretty", "cluster"))
	checkReplicaSet(t, d, createConfigSrvRs("configSrv", true))
	shards := createShards("myShard", false)
	checkShardedCluster(t, d, NewShardedCluster("cluster", configRs.Rs.Name(), shards), shards)

	// Then check that the sharded cluster and all replica sets were removed
	shards2 := createShards("otherShard", false)
	checkShardedClusterRemoved(t, d, NewShardedCluster("otherCluster", configRs2.Rs.Name(), shards2), createConfigSrvRs("otherConfigSrv", false), shards2)
}

// Methods for checking deployment units

func checkShardedCluster(t *testing.T, d Deployment, expectedCluster ShardedCluster, replicaSetWithProcesses []ReplicaSetWithProcesses) {
	cluster := d.getShardedClusterByName(expectedCluster.Name())

	require.NotNil(t, cluster)

	assert.Equal(t, expectedCluster, *cluster)

	checkReplicaSets(t, d, replicaSetWithProcesses)

	// checking that no previous replica sets are left. For this we take the name of first shard and remove the last digit
	firstShardName := replicaSetWithProcesses[0].Rs.Name()
	i := 0
	for _, r := range d.getReplicaSets() {
		if strings.HasPrefix(r.Name(), firstShardName[0:len(firstShardName)-1]) {
			i++
		}
	}
	assert.Equal(t, len(replicaSetWithProcesses), i)
}

func checkReplicaSets(t *testing.T, d Deployment, replicaSetWithProcesses []ReplicaSetWithProcesses) {
	for _, r := range replicaSetWithProcesses {
		checkReplicaSet(t, d, r)
	}
}

func checkReplicaSet(t *testing.T, d Deployment, replicaSetWithProcesses ReplicaSetWithProcesses) {
	rs := d.getReplicaSetByName(replicaSetWithProcesses.Rs.Name())

	require.NotNil(t, rs)

	assert.Equal(t, replicaSetWithProcesses.Rs, *rs)
	rsPrefix := replicaSetWithProcesses.Rs.Name()

	found := 0
	totalMongods := 0
	for _, p := range d.getProcesses() {
		for _, e := range replicaSetWithProcesses.Processes {
			if p.ProcessType() == ProcessTypeMongod && p.Name() == e.Name() {
				assert.Equal(t, e, p)
				found++
			}
		}
		if p.ProcessType() == ProcessTypeMongod && strings.HasPrefix(p.Name(), rsPrefix) {
			totalMongods++
		}
	}
	assert.Equalf(t, len(replicaSetWithProcesses.Processes), found, "Not all  %s replicaSet processes are found!", replicaSetWithProcesses.Rs.Name())
	assert.Equalf(t, len(replicaSetWithProcesses.Processes), totalMongods, "Some excessive mongod processes are found for %s replicaSet!", replicaSetWithProcesses.Rs.Name())
}

func checkProcess(t *testing.T, d Deployment, expectedProcess Process) {
	assert.NotNil(t, d.getProcessByName(expectedProcess.Name()))

	for _, p := range d.getProcesses() {
		if p.Name() == expectedProcess.Name() {
			assert.Equal(t, expectedProcess, p)
			break
		}
	}
}

func checkMongoSProcesses(t *testing.T, processes []Process, expectedMongosProcesses []Process) {
	found := 0
	totalMongoses := 0

	mongosPrefix := expectedMongosProcesses[0].Name()[0 : len(expectedMongosProcesses[0].Name())-1]

	for _, p := range processes {
		for _, e := range expectedMongosProcesses {
			if p.ProcessType() == ProcessTypeMongos && p.Name() == e.Name() {
				assert.Equal(t, e, p)
				found++
			}
		}
		if p.ProcessType() == ProcessTypeMongos && strings.HasPrefix(p.Name(), mongosPrefix) {
			totalMongoses++
		}
	}
	assert.Equal(t, len(expectedMongosProcesses), found, "Not all mongos processes are found!")
	assert.Equal(t, len(expectedMongosProcesses), totalMongoses, "Some excessive mongos processes are found!")
}

func checkShardedClusterRemoved(t *testing.T, d Deployment, sc ShardedCluster, configRs ReplicaSetWithProcesses, shards []ReplicaSetWithProcesses) {
	assert.Nil(t, d.getShardedClusterByName(sc.Name()))

	checkReplicaSetRemoved(t, d, configRs)

	for _, s := range shards {
		checkReplicaSetRemoved(t, d, s)
	}

	assert.Len(t, d.getMongosProcessesNames(sc.Name()), 0)
}

func checkReplicaSetRemoved(t *testing.T, d Deployment, rs ReplicaSetWithProcesses) {
	assert.Nil(t, d.getReplicaSetByName(rs.Rs.Name()))

	for _, p := range rs.Processes {
		checkProcessRemoved(t, d, p.Name())
	}
}

func checkProcessRemoved(t *testing.T, d Deployment, p string) {
	assert.Nil(t, d.getProcessByName(p))
}

// Methods for creating deployment units

func createShards(name string, check bool) []ReplicaSetWithProcesses {
	return createSpecificNumberOfShards(3, name, check)
}

func createSpecificNumberOfShards(count int, name string, check bool) []ReplicaSetWithProcesses {
	shards := make([]ReplicaSetWithProcesses, count)
	for i := 0; i < count; i++ {
		idx := strconv.Itoa(i)
		if check {
			shards[i] = NewReplicaSetWithProcesses(NewReplicaSet(name+idx), createReplicaSetProcessesCheck(name+idx, check))
		} else {
			shards[i] = NewReplicaSetWithProcesses(NewReplicaSet(name+idx), createReplicaSetProcesses(name+idx))
		}
	}
	return shards
}

func buildRsByProcesses(rsName string, processes []Process) ReplicaSet {
	rs := NewReplicaSet(rsName)
	members := make([]ReplicaSetMember, len(processes))
	for i, v := range processes {
		members[i] = ReplicaSetMember{}
		members[i]["host"] = v.Name()
		members[i]["_id"] = i
	}
	rs.setMembers(members)
	return rs
}

func createStandalone() Process {
	return NewMongodProcess("merchantsStandalone", "mongo1.some.host", "3.6.3").
		SetDbPath("/data").SetLogPath("/data/mongodb.log")
}

func createMongosProcesses(num int, name, clusterName string) []Process {
	mongosProcesses := make([]Process, num)

	for i := 0; i < num; i++ {
		idx := strconv.Itoa(i)
		mongosProcesses[i] = NewMongosProcess(name+idx, "mongoS"+idx+".some.host", "3.6.3")
		if clusterName != "" {
			mongosProcesses[i].setCluster(clusterName)
		}
	}
	return mongosProcesses
}

func createReplicaSetProcesses(rsName string) []Process {
	return createReplicaSetProcessesCount(3, rsName)
}

func createReplicaSetProcessesCount(count int, rsName string) []Process {
	return createReplicaSetProcessesCheckCount(count, rsName, false)
}

func createReplicaSetProcessesCheck(rsName string, check bool) []Process {
	return createReplicaSetProcessesCheckCount(3, rsName, check)
}

func createReplicaSetProcessesCheckCount(count int, rsName string, check bool) []Process {
	rsMembers := make([]Process, count)

	for i := 0; i < count; i++ {
		idx := strconv.Itoa(i)
		rsMembers[i] = NewMongodProcess(rsName+idx, rsName+idx+".some.host", "3.6.3")
		// We add replicaset member to check that replicaset name field was initialized during merge
		if check {
			rsMembers[i].setReplicaSetName(rsName)
		}
	}
	return rsMembers
}

func createConfigSrvRs(name string, check bool) ReplicaSetWithProcesses {
	replicaSetWithProcesses := NewReplicaSetWithProcesses(NewReplicaSet(name), createReplicaSetProcesses(name))

	if check {
		for _, p := range replicaSetWithProcesses.Processes {
			p.setClusterRoleConfigSrv()
		}
	}
	return replicaSetWithProcesses
}
func createConfigSrvRsCount(count int, name string, check bool) ReplicaSetWithProcesses {
	replicaSetWithProcesses := NewReplicaSetWithProcesses(NewReplicaSet(name), createReplicaSetProcessesCount(count, name))

	if check {
		for _, p := range replicaSetWithProcesses.Processes {
			p.setClusterRoleConfigSrv()
		}
	}
	return replicaSetWithProcesses
}
