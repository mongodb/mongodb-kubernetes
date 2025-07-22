package om

import (
	"fmt"
	"go.uber.org/zap/zaptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// First time merge adds the new standalone
// second invocation doesn't add new node as the existing standalone is found (by name) and the data is merged
func TestMergeStandalone(t *testing.T) {
	d := NewDeployment()
	mergeStandalone(t, d, createStandalone())

	assert.Len(t, d.getProcesses(), 1)

	d["version"] = 5
	d.getProcesses()[0]["alias"] = "alias"
	d.getProcesses()[0]["hostname"] = "foo"
	d.getProcesses()[0]["authSchemaVersion"] = 10
	d.getProcesses()[0]["featureCompatibilityVersion"] = "bla"

	mergeStandalone(t, d, createStandalone())

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
	mergeReplicaSet(t, d, "fooRs", createReplicaSetProcesses("fooRs"))
	expectedRs := buildRsByProcesses("fooRs", createReplicaSetProcesses("fooRs"))

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.GetReplicaSets(), 1)
	assert.Len(t, d.GetReplicaSets()[0].Members(), 3)
	assert.Equal(t, d.GetReplicaSets()[0], expectedRs.Rs)

	// Now the deployment "gets updated" from external - new node is added and one is removed - this should be fixed
	// by merge
	newProcess := NewMongodProcess("foo", "bar", "fake-mongoDBImage", false, &mdbv1.AdditionalMongodConfig{}, &mdbv1.NewStandaloneBuilder().Build().Spec, "", nil, "")

	d.getProcesses()[0]["processType"] = ProcessTypeMongos                            // this will be overriden
	d.getProcesses()[1].EnsureNetConfig()["MaxIncomingConnections"] = 20              // this will be left as-is
	d.GetReplicaSets()[0]["protocolVersion"] = 10                                     // this field will be overriden by Operator
	d.GetReplicaSets()[0].setMembers(d.GetReplicaSets()[0].Members()[0:2])            // "removing" the last node in replicaset
	d.GetReplicaSets()[0].addMember(newProcess, "", automationconfig.MemberOptions{}) // "adding" some new node
	d.GetReplicaSets()[0].Members()[0]["arbiterOnly"] = true                          // changing data for first node

	mergeReplicaSet(t, d, "fooRs", createReplicaSetProcesses("fooRs"))

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.GetReplicaSets(), 1)

	expectedRs = buildRsByProcesses("fooRs", createReplicaSetProcesses("fooRs"))
	expectedRs.Rs.Members()[0]["arbiterOnly"] = true
	expectedRs.Processes[1].EnsureNetConfig()["MaxIncomingConnections"] = 20

	checkReplicaSet(t, d, expectedRs)
}

// Checking that on scale down the old processes are removed
func TestMergeReplica_ScaleDown(t *testing.T) {
	d := NewDeployment()

	mergeReplicaSet(t, d, "someRs", createReplicaSetProcesses("someRs"))
	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.GetReplicaSets()[0].Members(), 3)

	// "scale down"
	scaledDownRsProcesses := createReplicaSetProcesses("someRs")[0:2]
	mergeReplicaSet(t, d, "someRs", scaledDownRsProcesses)

	assert.Len(t, d.getProcesses(), 2)
	assert.Len(t, d.GetReplicaSets()[0].Members(), 2)

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

	mergeReplicaSet(t, d, "fooRs", createReplicaSetProcesses("fooRs"))
	mergeReplicaSet(t, d, "anotherRs", createReplicaSetProcesses("anotherRs"))

	// Now the first process (and usually all others in practice) are changed by OM
	d.getProcesses()[0].EnsureNetConfig()["MaxIncomingConnections"] = 20
	d.getProcesses()[0]["backupRestoreUrl"] = "http://localhost:7890"
	d.getProcesses()[0]["logRotate"] = map[string]int{"sizeThresholdMB": 3000, "timeThresholdHrs": 12}
	d.getProcesses()[0]["kerberos"] = map[string]string{"keytab": "123456"}

	// Now we merged the scaled up RS
	mergeReplicaSet(t, d, "fooRs", createReplicaSetProcessesCount(5, "fooRs"))

	assert.Len(t, d.getProcesses(), 8)
	assert.Len(t, d.GetReplicaSets(), 2)

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
	d.ConfigureTLS(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}, util.CAFilePathInContainer)
	expectedSSLConfig := map[string]interface{}{
		"CAFilePath": "/mongodb-automation/ca.pem",
	}
	assert.Equal(t, expectedSSLConfig, d["tls"].(map[string]interface{}))

	d.ConfigureTLS(&mdbv1.Security{}, util.CAFilePathInContainer)
	assert.Equal(t, d["tls"], map[string]any{"clientCertificateMode": string(automationconfig.ClientCertificateModeOptional)})
}

func TestTLSConfigurationWillBeDisabled(t *testing.T) {
	d := Deployment{}
	d.ConfigureTLS(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: false}}, util.CAFilePathInContainer)

	assert.False(t, d.TLSConfigurationWillBeDisabled(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: false}}))
	assert.False(t, d.TLSConfigurationWillBeDisabled(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}))

	d = Deployment{}
	d.ConfigureTLS(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}, util.CAFilePathInContainer)

	assert.False(t, d.TLSConfigurationWillBeDisabled(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}))
	assert.True(t, d.TLSConfigurationWillBeDisabled(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: false}}))
}

// TestMergeDeployment_BigReplicaset ensures that adding a big replica set (> 7 members) works correctly and no more than
// 7 voting members are added
func TestMergeDeployment_BigReplicaset(t *testing.T) {
	omDeployment := NewDeployment()
	rs := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(8, "my-rs"))
	checkNumberOfVotingMembers(t, rs, 8, 8)

	omDeployment.MergeReplicaSet(rs, nil, nil, zaptest.NewLogger(t).Sugar())
	checkNumberOfVotingMembers(t, rs, 7, 8)

	// Now OM user "has changed" votes for some of the members - this must stay the same after merge
	omDeployment.GetReplicaSets()[0].Members()[2].setVotes(0).setPriority(0)
	omDeployment.GetReplicaSets()[0].Members()[4].setVotes(0).setPriority(0)

	omDeployment.MergeReplicaSet(rs, nil, nil, zaptest.NewLogger(t).Sugar())
	checkNumberOfVotingMembers(t, rs, 5, 8)

	// Now operator scales up by one - the "OM votes" should not suffer, but total number of votes will increase by one
	rsToMerge := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(9, "my-rs"))
	rsToMerge.Rs.Members()[2].setVotes(0).setPriority(0)
	rsToMerge.Rs.Members()[4].setVotes(0).setPriority(0)
	rsToMerge.Rs.Members()[7].setVotes(0).setPriority(0)
	omDeployment.MergeReplicaSet(rsToMerge, nil, nil, zaptest.NewLogger(t).Sugar())
	checkNumberOfVotingMembers(t, rs, 6, 9)

	// Now operator scales up by two - the "OM votes" should not suffer, but total number of votes will increase by one
	// only as 7 is the upper limit
	rsToMerge = buildRsByProcesses("my-rs", createReplicaSetProcessesCount(11, "my-rs"))
	rsToMerge.Rs.Members()[2].setVotes(0).setPriority(0)
	rsToMerge.Rs.Members()[4].setVotes(0).setPriority(0)

	omDeployment.MergeReplicaSet(rsToMerge, nil, nil, zaptest.NewLogger(t).Sugar())
	checkNumberOfVotingMembers(t, rs, 7, 11)
	assert.Equal(t, 0, omDeployment.GetReplicaSets()[0].Members()[2].Votes())
	assert.Equal(t, 0, omDeployment.GetReplicaSets()[0].Members()[4].Votes())
	assert.Equal(t, float32(0), omDeployment.GetReplicaSets()[0].Members()[2].Priority())
	assert.Equal(t, float32(0), omDeployment.GetReplicaSets()[0].Members()[4].Priority())
}

func TestGetAllProcessNames_MergedReplicaSetsAndShardedClusters(t *testing.T) {
	d := NewDeployment()
	rs0 := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(3, "my-rs"))

	d.MergeReplicaSet(rs0, nil, nil, zaptest.NewLogger(t).Sugar())
	assert.Equal(t, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, d.GetAllProcessNames())

	rs1 := buildRsByProcesses("another-rs", createReplicaSetProcessesCount(5, "another-rs"))
	d.MergeReplicaSet(rs1, nil, nil, zaptest.NewLogger(t).Sugar())

	assert.Equal(
		t,
		[]string{
			"my-rs-0", "my-rs-1", "my-rs-2",
			"another-rs-0", "another-rs-1", "another-rs-2", "another-rs-3", "another-rs-4",
		},
		d.GetAllProcessNames())

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

	assert.Equal(
		t,
		[]string{
			"my-rs-0", "my-rs-1", "my-rs-2",
			"another-rs-0", "another-rs-1", "another-rs-2", "another-rs-3", "another-rs-4",
			"pretty0", "pretty1", "pretty2",
			"configSrv-0", "configSrv-1", "configSrv-2",
			"myShard-0-0", "myShard-0-1", "myShard-0-2",
			"myShard-1-0", "myShard-1-1", "myShard-1-2",
			"myShard-2-0", "myShard-2-1", "myShard-2-2",
		},
		d.GetAllProcessNames())
}

func TestGetAllProcessNames_MergedShardedClusters(t *testing.T) {
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
	assert.Equal(
		t,
		[]string{
			"pretty0", "pretty1", "pretty2",
			"configSrv-0", "configSrv-1", "configSrv-2",
			"myShard-0-0", "myShard-0-1", "myShard-0-2",
			"myShard-1-0", "myShard-1-1", "myShard-1-2",
			"myShard-2-0", "myShard-2-1", "myShard-2-2",
		},
		d.GetAllProcessNames(),
	)

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "anotherCluster",
		MongosProcesses: createMongosProcesses(3, "anotherMongos", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("anotherClusterSh"),
		Finalizing:      false,
	}
	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	assert.Equal(
		t,
		[]string{
			"pretty0", "pretty1", "pretty2",
			"configSrv-0", "configSrv-1", "configSrv-2",
			"myShard-0-0", "myShard-0-1", "myShard-0-2",
			"myShard-1-0", "myShard-1-1", "myShard-1-2",
			"myShard-2-0", "myShard-2-1", "myShard-2-2",
			"anotherMongos0", "anotherMongos1", "anotherMongos2",
			"anotherClusterSh-0-0", "anotherClusterSh-0-1", "anotherClusterSh-0-2",
			"anotherClusterSh-1-0", "anotherClusterSh-1-1", "anotherClusterSh-1-2",
			"anotherClusterSh-2-0", "anotherClusterSh-2-1", "anotherClusterSh-2-2",
		},
		d.GetAllProcessNames(),
	)
}

func TestDeploymentCountIsCorrect(t *testing.T) {
	d := NewDeployment()

	rs0 := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(3, "my-rs"))
	d.MergeReplicaSet(rs0, nil, nil, zaptest.NewLogger(t).Sugar())

	excessProcesses := d.GetNumberOfExcessProcesses("my-rs")
	// There's only one resource in this deployment
	assert.Equal(t, 0, excessProcesses)

	rs1 := buildRsByProcesses("my-rs-second", createReplicaSetProcessesCount(3, "my-rs-second"))
	d.MergeReplicaSet(rs1, nil, nil, zaptest.NewLogger(t).Sugar())
	excessProcesses = d.GetNumberOfExcessProcesses("my-rs")

	// another replica set was added to the deployment. 3 processes do not belong to this one
	assert.Equal(t, 3, excessProcesses)

	configRs := createConfigSrvRs("config", false)

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "sc001",
		MongosProcesses: createMongosProcesses(3, "mongos", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("sc001"),
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	excessProcesses = d.GetNumberOfExcessProcesses("my-rs")

	// a Sharded Cluster was added, plenty of processes do not belong to "my-rs" anymore
	assert.Equal(t, 18, excessProcesses)

	// This unknown process does not belong in here
	excessProcesses = d.GetNumberOfExcessProcesses("some-unknown-name")

	// a Sharded Cluster was added, plenty of processes do not belong to "my-rs" anymore
	assert.Equal(t, 21, excessProcesses)

	excessProcesses = d.GetNumberOfExcessProcesses("sc001")
	// There are 6 processes that do not belong to the sc001 sharded cluster
	assert.Equal(t, 6, excessProcesses)
}

func TestGetNumberOfExcessProcesses_ShardedClusterScaleDown(t *testing.T) {
	d := NewDeployment()
	configRs := createConfigSrvRs("config", false)

	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "sc001",
		MongosProcesses: createMongosProcesses(3, "mongos", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("sc001"),
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	assert.Len(t, d.getShardedClusterByName("sc001").shards(), 3)
	assert.Len(t, d.GetReplicaSets(), 4)
	assert.Equal(t, 0, d.GetNumberOfExcessProcesses("sc001"))

	// Now we are "scaling down" the sharded cluster - so junk replica sets will appear - this is still ok
	twoShards := createShards("sc001")[0:2]

	mergeOpts = DeploymentShardedClusterMergeOptions{
		Name:            "sc001",
		MongosProcesses: createMongosProcesses(3, "mongos", ""),
		ConfigServerRs:  configRs,
		Shards:          twoShards,
		Finalizing:      false,
	}

	_, err = d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)
	assert.Len(t, d.getShardedClusterByName("sc001").shards(), 2)
	assert.Len(t, d.GetReplicaSets(), 4)

	assert.Equal(t, 0, d.GetNumberOfExcessProcesses("sc001"))
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

func TestProcessBelongsToReplicaSet(t *testing.T) {
	d := NewDeployment()
	rs0 := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(3, "my-rs"))
	d.MergeReplicaSet(rs0, nil, nil, zaptest.NewLogger(t).Sugar())

	assert.True(t, d.ProcessBelongsToResource("my-rs-0", "my-rs"))
	assert.True(t, d.ProcessBelongsToResource("my-rs-1", "my-rs"))
	assert.True(t, d.ProcessBelongsToResource("my-rs-2", "my-rs"))

	// Process does not belong if resource does not exist
	assert.False(t, d.ProcessBelongsToResource("unknown-0", "unknown"))
}

func TestProcessBelongsToShardedCluster(t *testing.T) {
	d := NewDeployment()
	configRs := createConfigSrvRs("config", false)
	mergeOpts := DeploymentShardedClusterMergeOptions{
		Name:            "sh001",
		MongosProcesses: createMongosProcesses(3, "mongos", ""),
		ConfigServerRs:  configRs,
		Shards:          createShards("shards"),
		Finalizing:      false,
	}

	_, err := d.MergeShardedCluster(mergeOpts)
	assert.NoError(t, err)

	// Config Servers
	assert.True(t, d.ProcessBelongsToResource("config-0", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("config-1", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("config-2", "sh001"))

	// Does not belong!
	assert.False(t, d.ProcessBelongsToResource("config-3", "sh001"))

	// Mongos
	assert.True(t, d.ProcessBelongsToResource("mongos0", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("mongos1", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("mongos2", "sh001"))
	// Does not belong!
	assert.False(t, d.ProcessBelongsToResource("mongos3", "sh001"))

	// Shard members
	assert.True(t, d.ProcessBelongsToResource("shards-0-0", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("shards-0-1", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("shards-0-2", "sh001"))

	// Does not belong!
	assert.False(t, d.ProcessBelongsToResource("shards-0-3", "sh001"))
	// Shard members
	assert.True(t, d.ProcessBelongsToResource("shards-1-0", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("shards-1-1", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("shards-1-2", "sh001"))

	// Does not belong!
	assert.False(t, d.ProcessBelongsToResource("shards-1-3", "sh001"))
	// Shard members
	assert.True(t, d.ProcessBelongsToResource("shards-2-0", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("shards-2-1", "sh001"))
	assert.True(t, d.ProcessBelongsToResource("shards-2-2", "sh001"))

	// Does not belong!
	assert.False(t, d.ProcessBelongsToResource("shards-2-3", "sh001"))
}

func TestDeploymentMinimumMajorVersion(t *testing.T) {
	d0 := NewDeployment()
	rs0Processes := createReplicaSetProcessesCount(3, "my-rs")
	rs0 := buildRsByProcesses("my-rs", rs0Processes)
	d0.MergeReplicaSet(rs0, nil, nil, zaptest.NewLogger(t).Sugar())

	assert.Equal(t, uint64(3), d0.MinimumMajorVersion())

	d1 := NewDeployment()
	rs1Processes := createReplicaSetProcessesCount(3, "my-rs")
	rs1Processes[0]["featureCompatibilityVersion"] = "2.4"
	rs1 := buildRsByProcesses("my-rs", rs1Processes)
	d1.MergeReplicaSet(rs1, nil, nil, zaptest.NewLogger(t).Sugar())

	assert.Equal(t, uint64(2), d1.MinimumMajorVersion())

	d2 := NewDeployment()
	rs2Processes := createReplicaSetProcessesCountEnt(3, "my-rs")
	rs2 := buildRsByProcesses("my-rs", rs2Processes)
	d2.MergeReplicaSet(rs2, nil, nil, zaptest.NewLogger(t).Sugar())

	assert.Equal(t, uint64(3), d2.MinimumMajorVersion())
}

// TestConfiguringTlsProcessFromOpsManager ensures that if OM sends 'tls' fields for processes and deployments -
// they are moved to 'ssl'
func TestConfiguringTlsProcessFromOpsManager(t *testing.T) {
	data, err := os.ReadFile("testdata/deployment_tls.json")
	assert.NoError(t, err)
	deployment, err := BuildDeploymentFromBytes(data)
	assert.NoError(t, err)

	assert.Contains(t, deployment, "tls")

	for _, p := range deployment.getProcesses() {
		assert.Contains(t, p.EnsureNetConfig(), "tls")
	}
}

func TestAddMonitoring(t *testing.T) {
	d := NewDeployment()

	rs0 := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(3, "my-rs"))
	d.MergeReplicaSet(rs0, nil, nil, zaptest.NewLogger(t).Sugar())
	d.AddMonitoring(zaptest.NewLogger(t).Sugar(), false, util.CAFilePathInContainer)

	expectedMonitoringVersions := []interface{}{
		map[string]interface{}{"hostname": "my-rs-0.some.host", "name": MonitoringAgentDefaultVersion},
		map[string]interface{}{"hostname": "my-rs-1.some.host", "name": MonitoringAgentDefaultVersion},
		map[string]interface{}{"hostname": "my-rs-2.some.host", "name": MonitoringAgentDefaultVersion},
	}
	assert.Equal(t, expectedMonitoringVersions, d.getMonitoringVersions())

	// adding again - nothing changes
	d.AddMonitoring(zaptest.NewLogger(t).Sugar(), false, util.CAFilePathInContainer)
	assert.Equal(t, expectedMonitoringVersions, d.getMonitoringVersions())
}

func TestAddMonitoringTls(t *testing.T) {
	d := NewDeployment()

	rs0 := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(3, "my-rs"))
	d.MergeReplicaSet(rs0, nil, nil, zaptest.NewLogger(t).Sugar())
	d.AddMonitoring(zaptest.NewLogger(t).Sugar(), true, util.CAFilePathInContainer)

	expectedAdditionalParams := map[string]string{
		"useSslForAllConnections":      "true",
		"sslTrustedServerCertificates": util.CAFilePathInContainer,
	}

	expectedMonitoringVersions := []interface{}{
		map[string]interface{}{"hostname": "my-rs-0.some.host", "name": MonitoringAgentDefaultVersion, "additionalParams": expectedAdditionalParams},
		map[string]interface{}{"hostname": "my-rs-1.some.host", "name": MonitoringAgentDefaultVersion, "additionalParams": expectedAdditionalParams},
		map[string]interface{}{"hostname": "my-rs-2.some.host", "name": MonitoringAgentDefaultVersion, "additionalParams": expectedAdditionalParams},
	}
	assert.Equal(t, expectedMonitoringVersions, d.getMonitoringVersions())

	// adding again - nothing changes
	d.AddMonitoring(zaptest.NewLogger(t).Sugar(), false, util.CAFilePathInContainer)
	assert.Equal(t, expectedMonitoringVersions, d.getMonitoringVersions())
}

func TestAddBackup(t *testing.T) {
	d := NewDeployment()

	rs0 := buildRsByProcesses("my-rs", createReplicaSetProcessesCount(3, "my-rs"))
	d.MergeReplicaSet(rs0, nil, nil, zaptest.NewLogger(t).Sugar())
	d.addBackup(zaptest.NewLogger(t).Sugar())

	expectedBackupVersions := []interface{}{
		map[string]interface{}{"hostname": "my-rs-0.some.host", "name": BackupAgentDefaultVersion},
		map[string]interface{}{"hostname": "my-rs-1.some.host", "name": BackupAgentDefaultVersion},
		map[string]interface{}{"hostname": "my-rs-2.some.host", "name": BackupAgentDefaultVersion},
	}
	assert.Equal(t, expectedBackupVersions, d.getBackupVersions())

	// adding again - nothing changes
	d.addBackup(zaptest.NewLogger(t).Sugar())
	assert.Equal(t, expectedBackupVersions, d.getBackupVersions())
}

// ************************   Methods for checking deployment units

func checkShardedCluster(t *testing.T, d Deployment, expectedCluster ShardedCluster, replicaSetWithProcesses []ReplicaSetWithProcesses) {
	checkShardedClusterCheckExtraReplicaSets(t, d, expectedCluster, replicaSetWithProcesses, true)
}

func checkShardedClusterCheckExtraReplicaSets(t *testing.T, d Deployment, expectedCluster ShardedCluster,
	expectedReplicaSets []ReplicaSetWithProcesses, checkExtraReplicaSets bool,
) {
	cluster := d.getShardedClusterByName(expectedCluster.Name())

	require.NotNil(t, cluster)

	assert.Equal(t, expectedCluster, *cluster)

	checkReplicaSets(t, d, expectedReplicaSets, checkExtraReplicaSets)

	if checkExtraReplicaSets {
		// checking that no previous replica sets are left. For this we take the name of first shard and remove the last digit
		firstShardName := expectedReplicaSets[0].Rs.Name()
		i := 0
		for _, r := range d.GetReplicaSets() {
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
	for _, m := range rs.Rs.Members() {
		if m.Votes() > 0 && m.Priority() > 0 {
			count++
		}
	}
	assert.Equal(t, expectedNumberOfVotingMembers, count)
	assert.Len(t, rs.Rs.Members(), totalNumberOfMembers)
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
		options := make([]automationconfig.MemberOptions, countMongods)
		shards[i] = NewReplicaSetWithProcesses(
			NewReplicaSet(rsName, "3.6.3"),
			createReplicaSetProcessesCount(countMongods, rsName),
			options,
		)
	}
	return shards
}

func buildRsByProcesses(rsName string, processes []Process) ReplicaSetWithProcesses {
	options := make([]automationconfig.MemberOptions, len(processes))
	return NewReplicaSetWithProcesses(
		NewReplicaSet(rsName, "3.6.3"),
		processes,
		options,
	)
}

func createStandalone() Process {
	return NewMongodProcess("merchantsStandalone", "mongo1.some.host", "fake-mongoDBImage", false, &mdbv1.AdditionalMongodConfig{}, defaultMongoDBVersioned("3.6.3"), "", nil, "")
}

func createMongosProcesses(num int, name, clusterName string) []Process {
	mongosProcesses := make([]Process, num)

	for i := 0; i < num; i++ {
		idx := strconv.Itoa(i)
		mongosProcesses[i] = NewMongosProcess(name+idx, "mongoS"+idx+".some.host", "fake-mongoDBImage", false, &mdbv1.AdditionalMongodConfig{}, defaultMongoDBVersioned("3.6.3"), "", nil, "")
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
		rsMembers[i] = NewMongodProcess(fmt.Sprintf("%s-%d", rsName, i), fmt.Sprintf("%s-%d.some.host", rsName, i), "fake-mongoDBImage", false, &mdbv1.AdditionalMongodConfig{}, defaultMongoDBVersioned("3.6.3"), "", nil, "")
		// Note that we don't specify the replicaset config for process
	}
	return rsMembers
}

func createReplicaSetProcessesCountEnt(count int, rsName string) []Process {
	rsMembers := make([]Process, count)

	for i := 0; i < count; i++ {
		rsMembers[i] = NewMongodProcess(fmt.Sprintf("%s-%d", rsName, i), fmt.Sprintf("%s-%d.some.host", rsName, i), "fake-mongoDBImage", false, &mdbv1.AdditionalMongodConfig{}, defaultMongoDBVersioned("3.6.3-ent"), "", nil, "")
		// Note that we don't specify the replicaset config for process
	}
	return rsMembers
}

func createConfigSrvRs(name string, check bool) ReplicaSetWithProcesses {
	options := make([]automationconfig.MemberOptions, 3)
	replicaSetWithProcesses := NewReplicaSetWithProcesses(
		NewReplicaSet(name, "3.6.3"),
		createReplicaSetProcesses(name),
		options,
	)

	if check {
		for _, p := range replicaSetWithProcesses.Processes {
			p.setClusterRoleConfigSrv()
		}
	}
	return replicaSetWithProcesses
}

func createConfigSrvRsCount(count int, name string, check bool) ReplicaSetWithProcesses {
	options := make([]automationconfig.MemberOptions, count)
	replicaSetWithProcesses := NewReplicaSetWithProcesses(
		NewReplicaSet(name, "3.6.3"),
		createReplicaSetProcessesCount(count, name),
		options,
	)

	if check {
		for _, p := range replicaSetWithProcesses.Processes {
			p.setClusterRoleConfigSrv()
		}
	}
	return replicaSetWithProcesses
}

func mergeReplicaSet(t *testing.T, d Deployment, rsName string, rsProcesses []Process) ReplicaSetWithProcesses {
	rs := buildRsByProcesses(rsName, rsProcesses)
	d.MergeReplicaSet(rs, nil, nil, zaptest.NewLogger(t).Sugar())
	return rs
}

func mergeStandalone(t *testing.T, d Deployment, s Process) Process {
	d.MergeStandalone(s, nil, nil, zaptest.NewLogger(t).Sugar())
	return s
}

func defaultMongoDBVersioned(version string) mdbv1.DbSpec {
	spec := mdbv1.NewReplicaSetBuilder().SetVersion(version).Build().Spec
	return &spec
}
