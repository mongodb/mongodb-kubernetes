package om

import (
	"encoding/json"
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
			fmt.Printf("Merged process %s into existing one\n", standaloneMongo)
			return
		}
	}
	d.setProcesses(append(d.getProcesses(), standaloneMongo))
	fmt.Printf("Added process %s as current OM deployment didn't have it\n", standaloneMongo)
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
			processesToRemove := r.MergeFrom(rs)

			// TODO replace with proper logging library
			fmt.Printf("Merged replica set %s into existing one\n", rs)

			if len(processesToRemove) > 0 {
				d.removeProcesses(processesToRemove)

				fmt.Printf("Removed processes %s as they were removed from replica set\n", processesToRemove)
			}
			return
		}
	}

	d.setReplicaSets(append(d.getReplicaSets(), rs))
	fmt.Printf("Added replica set %s as current OM deployment didn't have it\n", rs)
}

// AddMonitoring adds only one monitoring agent on the same host as the first process in the list if no monitoring
// agents are configured. Must be called after processes are added
// This is a temporary logic
func (d Deployment) AddMonitoring() {
	monitoringVersions := d["monitoringVersions"].([]interface{})

	if len(monitoringVersions) == 0 {
		monitoringVersions = append(monitoringVersions,
			map[string]string{"hostname": d.getProcesses()[0].HostName(), "name": "6.1.2.402-1"})
		d["monitoringVersions"] = monitoringVersions
	}
}

func (d Deployment) getProcesses() []Process {
	switch v := d["processes"].(type) {
	case []Process:
		return v
	case []interface{}:
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

func (d Deployment) removeProcesses(processNames []string) {
	processes := make([]Process, 0)

	for _, p := range d.getProcesses() {
		found := false
		for _, p2 := range processNames {
			if p.Name() == p2 {
				found = true
			}
		}
		if !found {
			processes = append(processes, p)
		}
	}

	d.setProcesses(processes)
}

func (d Deployment) getReplicaSets() []ReplicaSet {
	switch v := d["replicaSets"].(type) {
	case []ReplicaSet:
		return v
	case []interface{}:
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
