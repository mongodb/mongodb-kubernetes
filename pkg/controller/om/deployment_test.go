package om

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
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
	d.getProcesses()[1].EnsureNetConfig()["MaxIncomingConnections"] = 20                      // this will be left as-is
	d.getReplicaSets()[0]["protocolVersion"] = 10                                             // this field will be overriden by Operator
	d.getReplicaSets()[0].setMembers(d.getReplicaSets()[0].members()[0:2])                    // "removing" the last node in replicaset
	d.getReplicaSets()[0].addMember(NewMongodProcess("foo", "bar", DefaultMongoDB().Build())) // "adding" some new node
	d.getReplicaSets()[0].members()[0]["arbiterOnly"] = true                                  // changing data for first node

	mergeReplicaSet(d, "fooRs", createReplicaSetProcesses("fooRs"))

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets(), 1)

	expectedRs = buildRsByProcesses("fooRs", createReplicaSetProcesses("fooRs"))
	expectedRs.Rs.members()[0]["arbiterOnly"] = true
	expectedRs.Processes[1].EnsureNetConfig()["MaxIncomingConnections"] = 20

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
	d.getProcesses()[0].EnsureNetConfig()["MaxIncomingConnections"] = 20
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
		expectedRs.Processes[i].EnsureNetConfig()["MaxIncomingConnections"] = 20
		expectedRs.Processes[i]["backupRestoreUrl"] = "http://localhost:7890"
		expectedRs.Processes[i]["logRotate"] = map[string]int{"sizeThresholdMB": 3000, "timeThresholdHrs": 12}
		expectedRs.Processes[i]["kerberos"] = map[string]string{"keytab": "123456"}
	}

	// The other replica set must be the same
	checkReplicaSet(t, d, buildRsByProcesses("anotherRs", createReplicaSetProcesses("anotherRs")))
	checkReplicaSet(t, d, expectedRs)
}

func TestConfigureSSL_Deployment(t *testing.T) {
	d := Deployment{}
	d.ConfigureTLS(&mongodb.TLSConfig{Enabled: true})
	expectedSSLConfig := map[string]interface{}{
		"CAFilePath": "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
	}
	assert.Equal(t, expectedSSLConfig, d["ssl"].(map[string]interface{}))

	d.ConfigureTLS(&mongodb.TLSConfig{})
	assert.NotEmpty(t, d["ssl"])
}

// TestMergeDeployment_BigReplicaset ensures that adding a big replica set (> 7 members) works correctly and no more than
// 7 voting members are added
func TestMergeDeployment_BigReplicaset(t *testing.T) {
	omDeployment := NewDeployment()
	rs := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(8, "my-rs"))
	checkNumberOfVotingMembers(t, rs, 8, 8)

	omDeployment.MergeReplicaSet(rs, zap.S())
	checkNumberOfVotingMembers(t, rs, 7, 8)

	// Now OM user "has changed" votes for some of the members - this must stay the same after merge
	omDeployment.getReplicaSets()[0].members()[2].setVotes(0).setPriority(0)
	omDeployment.getReplicaSets()[0].members()[4].setVotes(0).setPriority(0)

	omDeployment.MergeReplicaSet(rs, zap.S())
	checkNumberOfVotingMembers(t, rs, 5, 8)

	// Now operator scales up by one - the "OM votes" should not suffer, but total number of votes will increase by one
	omDeployment.MergeReplicaSet(buildRsByProcesses("my-rs", createReplicaSetProcessesCount(9, "my-rs")), zap.S())
	checkNumberOfVotingMembers(t, rs, 6, 9)

	// Now operator scales up by two - the "OM votes" should not suffer, but total number of votes will increase by one
	// only as 7 is the upper limit
	omDeployment.MergeReplicaSet(buildRsByProcesses("my-rs", createReplicaSetProcessesCount(11, "my-rs")), zap.S())
	checkNumberOfVotingMembers(t, rs, 7, 11)
	assert.Equal(t, 0, omDeployment.getReplicaSets()[0].members()[2].Votes())
	assert.Equal(t, 0, omDeployment.getReplicaSets()[0].members()[4].Votes())
	assert.Equal(t, 0, omDeployment.getReplicaSets()[0].members()[2].Priority())
	assert.Equal(t, 0, omDeployment.getReplicaSets()[0].members()[4].Priority())
}

func TestIsShardOf(t *testing.T) {
	clusterName := "my-shard"
	assert.True(t, isShardOfShardedCluster(clusterName, "my-shard-0"))
	assert.True(t, isShardOfShardedCluster(clusterName, "my-shard-3"))
	assert.True(t, isShardOfShardedCluster(clusterName, "my-shard-9"))
	assert.True(t, isShardOfShardedCluster(clusterName, "my-shard-10"))
	assert.True(t, isShardOfShardedCluster(clusterName, "my-shard-23"))
	assert.True(t, isShardOfShardedCluster(clusterName, "my-shard-452"))

	assert.False(t, isShardOfShardedCluster(clusterName, "my-shard"))
	assert.False(t, isShardOfShardedCluster(clusterName, "my-my-shard"))
	assert.False(t, isShardOfShardedCluster(clusterName, "my-shard-s"))
	assert.False(t, isShardOfShardedCluster(clusterName, "my-shard-1-0"))
	assert.False(t, isShardOfShardedCluster(clusterName, "mmy-shard-1"))
	assert.False(t, isShardOfShardedCluster(clusterName, "my-shard-1s"))
}

// ************************   Methods for checking deployment units

func checkShardedCluster(t *testing.T, d Deployment, expectedCluster ShardedCluster, replicaSetWithProcesses []ReplicaSetWithProcesses) {
	checkShardedClusterCheckExtraReplicaSets(t, d, expectedCluster, replicaSetWithProcesses, true)
}

func checkShardedClusterCheckExtraReplicaSets(t *testing.T, d Deployment, expectedCluster ShardedCluster,
	expectedReplicaSets []ReplicaSetWithProcesses, checkExtraReplicaSets bool) {
	cluster := d.getShardedClusterByName(expectedCluster.Name())

	require.NotNil(t, cluster)

	assert.Equal(t, expectedCluster, *cluster)

	checkReplicaSets(t, d, expectedReplicaSets, checkExtraReplicaSets)

	if checkExtraReplicaSets {
		// checking that no previous replica sets are left. For this we take the name of first shard and remove the last digit
		firstShardName := expectedReplicaSets[0].Rs.Name()
		i := 0
		for _, r := range d.getReplicaSets() {
			if strings.HasPrefix(r.Name(), firstShardName[0:len(firstShardName)-1]) {
				i++
			}
		}
		assert.Equal(t, len(expectedReplicaSets), i)
	}
}

func checkReplicaSets(t *testing.T, d Deployment, replicaSetWithProcesses []ReplicaSetWithProcesses, checkExtraProcesses bool) {
	for _, r := range replicaSetWithProcesses {
		if checkExtraProcesses {
			checkReplicaSet(t, d, r)
		} else {
			checkReplicaSetCheckExtraProcesses(t, d, r, false)
		}
	}
}

func checkReplicaSetCheckExtraProcesses(t *testing.T, d Deployment, expectedRs ReplicaSetWithProcesses, checkExtraProcesses bool) {
	rs := d.getReplicaSetByName(expectedRs.Rs.Name())

	require.NotNil(t, rs)

	assert.Equal(t, expectedRs.Rs, *rs)
	rsPrefix := expectedRs.Rs.Name()

	found := 0
	totalMongods := 0
	for _, p := range d.getProcesses() {
		for i, e := range expectedRs.Processes {
			if p.ProcessType() == ProcessTypeMongod && p.Name() == e.Name() {
				assert.Equal(t, e, p, "Process %d (%s) doesn't match! \nExpected: %v, \nReal: %v", i, p.Name(), e.json(), p.json())
				found++
			}
		}
		if p.ProcessType() == ProcessTypeMongod && strings.HasPrefix(p.Name(), rsPrefix) {
			totalMongods++
		}
	}
	assert.Equalf(t, len(expectedRs.Processes), found, "Not all  %s replicaSet processes are found!", expectedRs.Rs.Name())
	if checkExtraProcesses {
		assert.Equalf(t, len(expectedRs.Processes), totalMongods, "Some excessive mongod processes are found for %s replicaSet!", expectedRs.Rs.Name())
	}
}

func checkReplicaSet(t *testing.T, d Deployment, expectedRs ReplicaSetWithProcesses) {
	checkReplicaSetCheckExtraProcesses(t, d, expectedRs, true)
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

func checkNumberOfVotingMembers(t *testing.T, rs ReplicaSetWithProcesses, expectedNumberOfVotingMembers, totalNumberOfMembers int) {
	count := 0
	for _, m := range rs.Rs.members() {
		if m.Votes() > 0 && m.Priority() > 0 {
			count++
		}
	}
	assert.Equal(t, expectedNumberOfVotingMembers, count)
	assert.Len(t, rs.Rs.members(), totalNumberOfMembers)
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
		rsName := fmt.Sprintf("%s-%d", name, i)
		shards[i] = NewReplicaSetWithProcesses(NewReplicaSet(rsName, "3.6.3"), createReplicaSetProcessesCount(countMongods, rsName))
	}
	return shards
}

func buildRsByProcesses(rsName string, processes []Process) ReplicaSetWithProcesses {
	return NewReplicaSetWithProcesses(NewReplicaSet(rsName, "3.6.3"), processes)
}

func createStandalone() Process {
	return NewMongodProcess("merchantsStandalone", "mongo1.some.host", DefaultMongoDBVersioned("3.6.3"))
}

func createMongosProcesses(num int, name, clusterName string) []Process {
	mongosProcesses := make([]Process, num)

	for i := 0; i < num; i++ {
		idx := strconv.Itoa(i)
		mongosProcesses[i] = NewMongosProcess(name+idx, "mongoS"+idx+".some.host", DefaultMongoDBVersioned("3.6.3"))
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
		rsMembers[i] = NewMongodProcess(fmt.Sprintf("%s-%d", rsName, i), fmt.Sprintf("%s-%d.some.host", rsName, i), DefaultMongoDBVersioned("3.6.3"))
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

// Convinience builder for Mongodb object
type MongoDBBuilder struct {
	*mongodb.MongoDB
}

func DefaultMongoDB() *MongoDBBuilder {
	spec := mongodb.MongoDbSpec{
		Version: "4.0.0",
		Members: 3,
	}
	mdb := &mongodb.MongoDB{Spec: spec}
	return &MongoDBBuilder{mdb}
}

func DefaultMongoDBVersioned(version string) *mongodb.MongoDB {
	return DefaultMongoDB().SetVersion(version).Build()
}

func (b *MongoDBBuilder) SetVersion(version string) *MongoDBBuilder {
	b.Spec.Version = version
	return b
}

func (b *MongoDBBuilder) SetFCVersion(version string) *MongoDBBuilder {
	b.Spec.FeatureCompatibilityVersion = &version
	return b
}

func (b *MongoDBBuilder) SetMembers(m int) *MongoDBBuilder {
	b.Spec.Members = m
	return b
}
func (b *MongoDBBuilder) SetAdditionalConfig(c *mongodb.AdditionalMongodConfig) *MongoDBBuilder {
	b.Spec.AdditionalMongodConfig = c
	return b
}

func (b *MongoDBBuilder) Build() *mongodb.MongoDB {
	b.InitDefaults()
	return b.MongoDB
}
