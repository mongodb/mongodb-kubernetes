package om

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
)

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
		rs.addMember(p)
	}
	return ReplicaSetWithProcesses{rs, processes}
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
