package om

import (
	"k8s.io/apimachinery/pkg/util/json"
	"fmt"
)

type Deployment map[string]interface{}

func BuildDeploymentFromBytes(jsonBytes []byte) (ans *Deployment, err error) {
	cc := &Deployment{}
	if err := json.Unmarshal(jsonBytes, &cc); err != nil {
		return nil, err
	}
	return cc, nil
}

func NewDeployment() Deployment {
	ans := Deployment{}
	ans.setProcesses(make([]Process, 0))
	ans.setReplicaSets(make([]ReplicaSet, 0))
	return ans
}

// merge Standalone. If we found the process with the same name - update some fields there. Otherwise add the new one
func (d Deployment) MergeStandalone(standaloneMongo Process) {
	// merging process in case exists, otherwise adding it
	for _, pr := range d.getProcesses() {
		if pr.Name() == standaloneMongo.Name() {
			pr.MergeFrom(standaloneMongo)
			return
		}
	}
	d.setProcesses(append(d.getProcesses(), standaloneMongo))
}

// Merges the replica set and its members to the deployment. Note that if "wrong" RS members are removed after merge -
// corresponding processes are not removed.
// So far we don't configure anything for RS except it's name (though the API supports many other parameters
// and we may change this in future)
func (d Deployment) MergeReplicaSet(rsName string, processes []Process) {
	rs := NewReplicaSet(rsName)
	for _, p := range processes {
		p.setReplicaSetName(rsName)
		d.MergeStandalone(p)
		rs.addMember(p)
	}

	// merging replicaset in case it exists, otherwise adding it
	for _, r := range d.getReplicaSets() {
		if r.Name() == rsName {
			r.MergeFrom(rs)
			return
		}
	}
	d.setReplicaSets(append(d.getReplicaSets(), rs))
}

func (d Deployment) getProcesses() []Process {
	switch v := d["processes"].(type) {
	case []Process:
		return v
	case [] interface{}:
		// seems we cannot directly cast the array of interfaces to array of Processes - have to manually copy references
		ans := make([]Process, len(v))
		for i, val := range v {
			ans[i] = NewProcessFromInterface(val)
		}
		return ans
	default:
		panic(fmt.Sprintf("Unexpected type of processes variable: %T", v))
	}
}

func (d Deployment) setProcesses(processes []Process) {
	d["processes"] = processes
}

func (d Deployment) getReplicaSets() []ReplicaSet {
	switch v := d["replicaSets"].(type) {
	case []ReplicaSet:
		return v
	case [] interface{}:
		ans := make([]ReplicaSet, len(v))
		for i, val := range v {
			ans[i] = NewReplicaSetFromInterface(val)
		}
		return ans
	default:
		panic(fmt.Sprintf("Unexpected type of replicasets variable: %T", v))
	}
}

func (d Deployment) setReplicaSets(replicaSets []ReplicaSet) {
	d["replicaSets"] = replicaSets
}

// merge sharded cluster
