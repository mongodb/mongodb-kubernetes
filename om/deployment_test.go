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
	mergeStandalone(d, createStandalone())

	assert.Len(t, d.getProcesses(), 1)

	d["version"] = 5
	d.getProcesses()[0]["alias"] = "alias"
	d.getProcesses()[0]["hostname"] = "foo"
	d.getProcesses()[0]["authSchemaVersion"] = 10
	d.getProcesses()[0]["featureCompatibilityVersion"] = "bla"

	mergeStandalone(d, createStandalone())

	assert.Len(t, d.getProcesses(), 1)

	expected := createStandalone()

	// fields which are not owned by OM-Kube should be left unchanged
	assert.Equal(t, d["version"], 5)
	expected["alias"] = "alias"

	assert.Equal(t, &expected, d.getProcessByName(expected.Name()))
}

// First merge results in just adding the ReplicaSet
// Second merge performs real merge operation
func TestMergeReplicaSet(t *testing.T) {
	d := NewDeployment()

	mergeReplicaSet(d, "fooRs", createReplicaSetProcesses("fooRs"))

	expectedRs := buildRsByProcesses("fooRs", createReplicaSetProcesses("fooRs"))

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets(), 1)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)
	assert.Equal(t, d.getReplicaSets()[0], expectedRs.Rs)

	// Now the deployment "gets updated" from external - new node is added and one is removed - this should be fixed
	// by merge
	d.getProcesses()[0]["processType"] = "mongos"                                             // this will be overriden
	d.getProcesses()[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"] = 20 // this will be left as-is
	d.getReplicaSets()[0]["protocolVersion"] = 10                                             // this field will be overriden by Operator
	d.getReplicaSets()[0].setMembers(d.getReplicaSets()[0].members()[0:2])                    // "removing" the last node in replicaset
	d.getReplicaSets()[0].addMember(NewMongodProcess("foo", "bar", "4.0.0"))                  // "adding" some new node
	d.getReplicaSets()[0].members()[0]["arbiterOnly"] = true                                  // changing data for first node

	mergeReplicaSet(d, "fooRs", createReplicaSetProcesses("fooRs"))

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets(), 1)

	expectedRs = buildRsByProcesses("fooRs", createReplicaSetProcesses("fooRs"))
	expectedRs.Rs.members()[0]["arbiterOnly"] = true
	expectedRs.Processes[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"] = 20
	checkReplicaSet(t, d, expectedRs)
}

// Checking that on scale down the old processes are removed
func TestMergeReplica_ScaleDown(t *testing.T) {
	d := NewDeployment()

	mergeReplicaSet(d, "someRs", createReplicaSetProcesses("someRs"))
	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)

	// "scale down"
	scaledDownRsProcesses := createReplicaSetProcesses("someRs")[0:2]
	mergeReplicaSet(d, "someRs", scaledDownRsProcesses)

	assert.Len(t, d.getProcesses(), 2)
	assert.Len(t, d.getReplicaSets()[0].members(), 2)

	// checking that the last member was removed
	rsProcesses := buildRsByProcesses("someRs", createReplicaSetProcesses("someRs")).Processes
	assert.Contains(t, d.getProcesses(), rsProcesses[0])
	assert.Contains(t, d.getProcesses(), rsProcesses[1])
	assert.NotContains(t, d.getProcesses(), rsProcesses[2])
}

// TestMergeReplicaSet_MergeFirstProcess checks that if the replica set is scaled up - then all OM changes to existing
// processes (more precisely - to the first member) are copied to new members
func TestMergeReplicaSet_MergeFirstProcess(t *testing.T) {
	d := NewDeployment()

	mergeReplicaSet(d, "fooRs", createReplicaSetProcesses("fooRs"))
	mergeReplicaSet(d, "anotherRs", createReplicaSetProcesses("anotherRs"))

	// Now the first process (and usually all others in practice) are changed by OM
	d.getProcesses()[0].Args()["net"].(map[string]interface{})["maxIncomingConnections"] = 20
	d.getProcesses()[0]["backupRestoreUrl"] = "http://localhost:7890"
	d.getProcesses()[0]["logRotate"] = map[string]int{"sizeThresholdMB": 3000, "timeThresholdHrs": 12}
	d.getProcesses()[0]["kerberos"] = map[string]string{"keytab": "123456"}

	// Now we merged the scaled up RS
	mergeReplicaSet(d, "fooRs", createReplicaSetProcessesCount(5, "fooRs"))

	assert.Len(t, d.getProcesses(), 8)
	assert.Len(t, d.getReplicaSets(), 2)

	expectedRs := buildRsByProcesses("fooRs", createReplicaSetProcessesCount(5, "fooRs"))

	// Verifying that the first process was merged with new ones
	for _, i := range []int{0, 3, 4} {
		expectedRs.Processes[i].Args()["net"].(map[string]interface{})["maxIncomingConnections"] = 20
		expectedRs.Processes[i]["backupRestoreUrl"] = "http://localhost:7890"
		expectedRs.Processes[i]["logRotate"] = map[string]int{"sizeThresholdMB": 3000, "timeThresholdHrs": 12}
		expectedRs.Processes[i]["kerberos"] = map[string]string{"keytab": "123456"}
	}

	// The other replica set must be the same
	checkReplicaSet(t, d, buildRsByProcesses("anotherRs", createReplicaSetProcesses("anotherRs")))
	checkReplicaSet(t, d, expectedRs)
}

// ************************   Methods for checking deployment units

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
		for i, e := range replicaSetWithProcesses.Processes {
			if p.ProcessType() == ProcessTypeMongod && p.Name() == e.Name() {
				assert.Equal(t, e, p, "Process %d (%s) doesn't match! \nExpected: %v, \nReal: %v", i, p.Name(), e.json(), p.json())
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

func checkMongoSProcesses(t *testing.T, allProcesses []Process, expectedMongosProcesses []Process) {
	found := 0
	totalMongoses := 0

	mongosPrefix := expectedMongosProcesses[0].Name()[0 : len(expectedMongosProcesses[0].Name())-1]

	for _, p := range allProcesses {
		for _, e := range expectedMongosProcesses {
			if p.ProcessType() == ProcessTypeMongos && p.Name() == e.Name() {
				assert.Equal(t, e, p, "Actual: %v\n, Expected: %v", p.json(), e.json())
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

func createShards(name string) []ReplicaSetWithProcesses {
	return createSpecificNumberOfShards(3, name)
}

// ********************************   Methods for creating deployment units

func createSpecificNumberOfShards(count int, name string) []ReplicaSetWithProcesses {
	return createSpecificNumberOfShardsAndMongods(count, 3, name)
}

func createShardsSpecificNumberOfMongods(count int, name string) []ReplicaSetWithProcesses {
	return createSpecificNumberOfShardsAndMongods(3, count, name)
}

func createSpecificNumberOfShardsAndMongods(countShards, countMongods int, name string) []ReplicaSetWithProcesses {
	shards := make([]ReplicaSetWithProcesses, countShards)
	for i := 0; i < countShards; i++ {
		idx := strconv.Itoa(i)
		shards[i] = NewReplicaSetWithProcesses(NewReplicaSet(name+idx, "3.6.3"), createReplicaSetProcessesCount(countMongods, name+idx))
	}
	return shards
}

func buildRsByProcesses(rsName string, processes []Process) ReplicaSetWithProcesses {
	return NewReplicaSetWithProcesses(NewReplicaSet(rsName, "3.6.3"), processes)
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
	rsMembers := make([]Process, count)

	for i := 0; i < count; i++ {
		idx := strconv.Itoa(i)
		rsMembers[i] = NewMongodProcess(rsName+idx, rsName+idx+".some.host", "3.6.3")
		// Note that we don't specify the replicaset config for process
	}
	return rsMembers
}

func createConfigSrvRs(name string, check bool) ReplicaSetWithProcesses {
	replicaSetWithProcesses := NewReplicaSetWithProcesses(NewReplicaSet(name, "3.6.3"), createReplicaSetProcesses(name))

	if check {
		for _, p := range replicaSetWithProcesses.Processes {
			p.setClusterRoleConfigSrv()
		}
	}
	return replicaSetWithProcesses
}

func createConfigSrvRsCount(count int, name string, check bool) ReplicaSetWithProcesses {
	replicaSetWithProcesses := NewReplicaSetWithProcesses(NewReplicaSet(name, "3.6.3"), createReplicaSetProcessesCount(count, name))

	if check {
		for _, p := range replicaSetWithProcesses.Processes {
			p.setClusterRoleConfigSrv()
		}
	}
	return replicaSetWithProcesses
}

func mergeReplicaSet(d Deployment, rsName string, rsProcesses []Process) ReplicaSetWithProcesses {
	rs := buildRsByProcesses(rsName, rsProcesses)
	d.MergeReplicaSet(rs, zap.S())
	return rs
}

func mergeStandalone(d Deployment, s Process) Process {
	d.MergeStandalone(s, zap.S())
	return s
}
