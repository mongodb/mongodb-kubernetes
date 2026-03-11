package migrate

import (
	"fmt"

	"github.com/spf13/cast"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

// buildProcessMap indexes all processes by name for O(1) lookups.
func buildProcessMap(processes []om.Process) (map[string]om.Process, error) {
	processMap := make(map[string]om.Process, len(processes))
	for i, p := range processes {
		name := cast.ToString(p["name"])
		if name == "" {
			return nil, fmt.Errorf("process at index %d has no name field", i)
		}
		if _, exists := processMap[name]; exists {
			return nil, fmt.Errorf("duplicate process name %q at index %d", name, i)
		}
		processMap[name] = p
	}
	return processMap, nil
}

// extractMemberInfo reads version, FCV, and per-member metadata from the
// automation config. Each member becomes an mdbv1.ExternalMember entry that
// can be assigned directly to the CR's spec.externalMembers.
func extractMemberInfo(members []om.ReplicaSetMember, processMap map[string]om.Process) ([]mdbv1.ExternalMember, string, string, error) {
	var externalMembers []mdbv1.ExternalMember
	var version, fcv string

	for i, m := range members {
		host := m.Name()
		if host == "" {
			return nil, "", "", fmt.Errorf("member at index %d has no host field", i)
		}

		proc, ok := processMap[host]
		if !ok {
			return nil, "", "", fmt.Errorf("process %q referenced by member at index %d not found in the automation config", host, i)
		}

		hostname := cast.ToString(proc["hostname"])
		if hostname == "" {
			return nil, "", "", fmt.Errorf("process %q has no hostname field", host)
		}

		args := proc.Args()
		port := maputil.ReadMapValueAsInt(args, "net", "port")
		if port == 0 {
			return nil, "", "", fmt.Errorf("process %q has no port configured in args2_6.net.port", host)
		}

		if version == "" {
			version = cast.ToString(proc["version"])
		}
		if fcv == "" {
			fcv = proc.FeatureCompatibilityVersion()
		}

		rsName := maputil.ReadMapValueAsString(args, "replication", "replSetName")
		externalMembers = append(externalMembers, mdbv1.ExternalMember{
			ProcessName:    host,
			Hostname:       fmt.Sprintf("%s:%d", hostname, port),
			Type:           "mongod",
			ReplicaSetName: rsName,
		})
	}

	return externalMembers, version, fcv, nil
}

func getFirstReplicaSet(d om.Deployment) (om.ReplicaSet, error) {
	replicaSets := d.GetReplicaSets()
	if len(replicaSets) == 0 {
		return nil, fmt.Errorf("no replica sets found in the automation config")
	}
	return replicaSets[0], nil
}
