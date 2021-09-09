package om

import (
	"sort"
	"strconv"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
)

const maxMembersInReplicaSet = 50

// ReplicaSetWithProcesses is a wrapper for replica set and processes that match to it
// The contract for class is that both processes and replica set are guaranteed to match to each other
// Note, that the type modifies the entities directly and doesn't create copies! (seems not a big deal for clients)
type ReplicaSetWithProcesses struct {
	Rs        ReplicaSet
	Processes []Process
}

// NewReplicaSetWithProcesses is the only correct function for creation ReplicaSetWithProcesses
func NewReplicaSetWithProcesses(
	rs ReplicaSet,
	processes []Process,
) ReplicaSetWithProcesses {
	rs.clearMembers()

	for _, p := range processes {
		p.setReplicaSetName(rs.Name())
		rs.addMember(p, "")
	}
	return ReplicaSetWithProcesses{rs, processes}
}

// NewMultiClusterReplicaSetWithProcesses Creates processes for a multi cluster deployment.
// The only difference from NewReplicaSetWithProcesses is that we ensure that there can be no overlapping values for _id.
// E.g. if we have a multi cluster resource 1-1-1, if we scale the first cluster to 2, the _id value would overlap (1)
// and the resource would not get into the ready state. This function offsets each cluster by 50 which is the maximum
// number of replicaset members in any given replicasets. This ensures that there will be no overlapping values.
func NewMultiClusterReplicaSetWithProcesses(
	rs ReplicaSet,
	processMap map[string][]Process,

) ReplicaSetWithProcesses {
	rs.clearMembers()

	var clusterNames []string
	for k := range processMap {
		clusterNames = append(clusterNames, k)
	}

	// TODO: this particular approach will break if a new cluster is added that does not becomes the last member.
	// One possible solution is to ensure order of cluster names once a resource has been deployed.
	sort.SliceStable(clusterNames, func(i, j int) bool {
		return clusterNames[i] < clusterNames[j]
	})

	var allProcesses []Process
	for clusterNum, clusterName := range clusterNames {
		processList := processMap[clusterName]
		for i, p := range processList {
			p.setReplicaSetName(rs.Name())
			rs.addMember(p, strconv.Itoa(i+maxMembersInReplicaSet*clusterNum))
		}
		allProcesses = append(allProcesses, processList...)
	}

	return ReplicaSetWithProcesses{rs, allProcesses}
}

func (r ReplicaSetWithProcesses) GetProcessNames() []string {
	processNames := make([]string, len(r.Processes))
	for i, p := range r.Processes {
		processNames[i] = p.Name()
	}
	return processNames
}

func (r ReplicaSetWithProcesses) SetHorizons(replicaSetHorizons []mdbv1.MongoDBHorizonConfig) {
	if len(replicaSetHorizons) >= len(r.Rs.members()) {
		for i, m := range r.Rs.members() {
			m.setHorizonConfig(replicaSetHorizons[i])
		}
	}
}
