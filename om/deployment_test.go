package om

import (
	"testing"
	"github.com/corbym/gocrest/then"
	"github.com/corbym/gocrest/is"
	"github.com/corbym/gocrest/has"
	//"fmt"
	//"encoding/json"
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

	then.AssertThat(t, d.getProcesses(), has.Length(1))

	d["version"] = 5
	d.getProcesses()[0]["alias"] = "alias"
	d.getProcesses()[0].SetHostName("foo")

	d.MergeStandalone(createStandalone())

	then.AssertThat(t, d.getProcesses(), has.Length(1))

	// fields which are owned by OM-Kube should be overriden
	then.AssertThat(t, d.getProcesses()[0].HostName(), is.EqualTo("mongo1.some.host"))

	// fields which are not owned by OM-Kube should be left unchanged
	then.AssertThat(t, d.getProcesses()[0]["alias"], is.EqualTo("alias"))
	then.AssertThat(t, d["version"], is.EqualTo(5))
}

// First merge results in just adding the ReplicaSet
// Second merge performs real merge operation
func TestMergeReplicaSet(t *testing.T) {
	d := NewDeployment()

	d.MergeReplicaSet("fooRs", createReplicaSetProcesses())
	expectedRs := buildRsByProcesses("fooRs", createReplicaSetProcesses())

	then.AssertThat(t, d.getProcesses(), has.Length(3))
	then.AssertThat(t, d.getReplicaSets(), has.Length(1))
	then.AssertThat(t, d.getReplicaSets()[0].members(), has.Length(3))
	then.AssertThat(t, d.getReplicaSets()[0], is.EqualTo(expectedRs))

	// Now the deployment "gets updated" from external - new node is added and one is removed - this should be fixed
	// by merge
	d.getProcesses()[0]["processType"] = "mongos" // this will be overriden
	d.getProcesses()[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"] = 20 // this will be left as-is
	d.getReplicaSets()[0].setMembers(d.getReplicaSets()[0].members()[0:2]) // "removing" the last node in replicaset
	d.getReplicaSets()[0].addMember(NewProcess("4.0.0").SetHostName("foo").SetName("bar")) // "adding" some new node
	d.getReplicaSets()[0].members()[0]["arbiterOnly"] = true // changing data for first node

	d.MergeReplicaSet("fooRs", createReplicaSetProcesses())

	then.AssertThat(t, d.getProcesses(), has.Length(3))
	then.AssertThat(t, d.getReplicaSets(), has.Length(1))
	then.AssertThat(t, d.getProcesses()[0]["processType"], is.EqualTo("mongod"))
	then.AssertThat(t, d.getProcesses()[1].Args()["net"].(map[string]interface{})["maxIncomingConnections"], is.EqualTo(20))
	then.AssertThat(t, d.getReplicaSets()[0].members(), has.Length(3))

	expectedRs = buildRsByProcesses("fooRs", createReplicaSetProcesses())
	expectedRs.members()[0]["arbiterOnly"] = true
	fmt.Print(d.getReplicaSets()[0])
	then.AssertThat(t, d.getReplicaSets()[0], is.EqualTo(expectedRs))
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
	rsMembers := make([]Process, 3)

	for i := 0; i < 3; i = i + 1 {
		idx := strconv.Itoa(i)
		rsMembers[i] = NewProcess("3.6.3").SetHostName("mongo" + idx + ".some.host").SetName("merchantsStandalone" + idx).
			SetDbPath("/data").SetLogPath("/data/mongodb.log")
	}
	return rsMembers
}
