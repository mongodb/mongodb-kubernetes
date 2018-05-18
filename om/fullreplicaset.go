package om

// ReplicaSetWithProcesses is a wrapper for replica set and processes that match to it
// The contract for class is that both processes and replica set are guaranteed to match to each other
// Note, that the type modifies the entities directly and doesn't create copies! (seems not a big deal for clients)
type ReplicaSetWithProcesses struct {
	Rs        ReplicaSet
	Processes []Process
}

// NewReplicaSetWithProcesses is the only correct function for creation ReplicaSetWithProcesses
func NewReplicaSetWithProcesses(rs ReplicaSet, processes []Process) ReplicaSetWithProcesses {
	rs.clearMembers()

	for _, p := range processes {
		p.setReplicaSetName(rs.Name())
		rs.addMember(p)
	}
	return ReplicaSetWithProcesses{rs, processes}
}
