package om

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"strconv"
	"fmt"
)

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
	d.MergeStandalone(standalone)

	assert.Len(t, d.getProcesses(), 1)

	d["version"] = 5
	d.getProcesses()[0]["alias"] = "alias"
	d.getProcesses()[0].SetHostName("foo")

	d.MergeStandalone(createStandalone())

	assert.Len(t, d.getProcesses(), 1)

	// fields which are owned by OM-Kube should be overriden
	assert.Equal(t, d.getProcesses()[0].HostName(), "mongo1.some.host")

	// fields which are not owned by OM-Kube should be left unchanged
	assert.Equal(t, d.getProcesses()[0]["alias"], "alias")
	assert.Equal(t, d["version"], 5)
}

// First merge results in just adding the ReplicaSet
// Second merge performs real merge operation
func TestMergeReplicaSet(t *testing.T) {
	d := NewDeployment()

	d.MergeReplicaSet("fooRs", createReplicaSetProcesses())
	expectedRs := buildRsByProcesses("fooRs", createReplicaSetProcesses())

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets(), 1)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)
	assert.Equal(t, d.getReplicaSets()[0], expectedRs)

	// Now the deployment "gets updated" from external - new node is added and one is removed - this should be fixed
	// by merge
	d.getProcesses()[0]["processType"] = "mongos"                                             // this will be overriden
	d.getProcesses()[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"] = 20 // this will be left as-is
	d.getReplicaSets()[0].setMembers(d.getReplicaSets()[0].members()[0:2])                    // "removing" the last node in replicaset
	d.getReplicaSets()[0].addMember(NewProcess("4.0.0").SetHostName("foo").SetName("bar"))    // "adding" some new node
	d.getReplicaSets()[0].members()[0]["arbiterOnly"] = true                                  // changing data for first node

	d.MergeReplicaSet("fooRs", createReplicaSetProcesses())

	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets(), 1)
	assert.Equal(t, d.getProcesses()[0]["processType"], "mongod")
	assert.Equal(t, d.getProcesses()[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"], 20)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)

	expectedRs = buildRsByProcesses("fooRs", createReplicaSetProcesses())
	expectedRs.members()[0]["arbiterOnly"] = true
	fmt.Println(d.getReplicaSets()[0])
	fmt.Println(expectedRs)
	assert.Equal(t, d.getReplicaSets()[0], expectedRs)
}

// Checking that on scale down the old processes are removed
func TestMergeReplicaSetScaleDown(t *testing.T) {
	d := NewDeployment()

	d.MergeReplicaSet("someRs", createReplicaSetProcesses())
	assert.Len(t, d.getProcesses(), 3)
	assert.Len(t, d.getReplicaSets()[0].members(), 3)

	// "scale down"
	scaledDownRsProcesses := createReplicaSetProcesses()[0:2]
	d.MergeReplicaSet("someRs", scaledDownRsProcesses)

	assert.Len(t, d.getProcesses(), 2)
	assert.Len(t, d.getReplicaSets()[0].members(), 2)

	// checking that the last member was removed
	rsProcesses := createReplicaSetProcessesWithRsName("someRs")
	assert.Contains(t, d.getProcesses(), rsProcesses[0])
	assert.Contains(t, d.getProcesses(), rsProcesses[1])
	assert.NotContains(t, d.getProcesses(), rsProcesses[2])
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
	return NewProcess("3.6.3").SetHostName("mongo1.some.host").SetName("merchantsStandalone").
		SetDbPath("/data").SetLogPath("/data/mongodb.log")
}

func createReplicaSetProcesses() []Process {
	return createReplicaSetProcessesWithRsName("")
}

func createReplicaSetProcessesWithRsName(rsName string) []Process {
	rsMembers := make([]Process, 3)

	for i := 0; i < 3; i++ {
		idx := strconv.Itoa(i)
		rsMembers[i] = NewProcess("3.6.3").SetHostName("mongo" + idx + ".some.host").SetName("merchantsStandalone" + idx).
			SetDbPath("/data").SetLogPath("/data/mongodb.log")
		// We add replicaset member to check that replicaset name field was initialized during merge
		if rsName != "" {
			rsMembers[i].setReplicaSetName(rsName)
		}
	}
	return rsMembers
}
