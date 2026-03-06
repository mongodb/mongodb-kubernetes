package migrate

import (
	"fmt"

	"github.com/spf13/cast"
)

func getSlice(m map[string]interface{}, key string) []interface{} {
	if v, ok := m[key]; ok {
		if s, ok := v.([]interface{}); ok {
			return s
		}
	}
	return nil
}

// buildProcessMap indexes all processes by name for O(1) lookups.
func buildProcessMap(processes []interface{}) (map[string]map[string]interface{}, error) {
	processMap := make(map[string]map[string]interface{}, len(processes))
	for i, p := range processes {
		proc, ok := p.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("process at index %d is not a valid map", i)
		}
		name := cast.ToString(proc["name"])
		if name == "" {
			return nil, fmt.Errorf("process at index %d has no name field", i)
		}
		processMap[name] = proc
	}
	return processMap, nil
}

// extractMemberInfo reads version, FCV, and per-member metadata from the
// automation config. Each member becomes an ExternalMember entry.
func extractMemberInfo(members []interface{}, processMap map[string]map[string]interface{}) ([]ExternalMember, string, string, error) {
	var externalMembers []ExternalMember
	var version, fcv string

	for i, m := range members {
		member, ok := m.(map[string]interface{})
		if !ok {
			return nil, "", "", fmt.Errorf("member at index %d is not a valid map", i)
		}

		host := cast.ToString(member["host"])
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

		port := 0
		if args, ok := proc["args2_6"].(map[string]interface{}); ok {
			if net, ok := args["net"].(map[string]interface{}); ok {
				port = cast.ToInt(net["port"])
			}
		}
		if port == 0 {
			return nil, "", "", fmt.Errorf("process %q has no port configured in args2_6.net.port", host)
		}

		if version == "" {
			version = cast.ToString(proc["version"])
		}
		if fcv == "" {
			fcv = cast.ToString(proc["featureCompatibilityVersion"])
		}

		externalMembers = append(externalMembers, ExternalMember{
			ProcessID:   host,
			Hostname:    hostname,
			Port:        port,
			Votes:       cast.ToInt(member["votes"]),
			Priority:    cast.ToFloat32(member["priority"]),
			ArbiterOnly: cast.ToBool(member["arbiterOnly"]),
		})
	}

	return externalMembers, version, fcv, nil
}

func getFirstReplicaSet(d map[string]interface{}) (map[string]interface{}, error) {
	replicaSets := getReplicaSets(d)
	if len(replicaSets) == 0 {
		return nil, fmt.Errorf("no replica sets found in the automation config")
	}
	rsMap, ok := replicaSets[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("first replica set entry is not a valid map")
	}
	return rsMap, nil
}

func getReplicaSets(d map[string]interface{}) []interface{} {
	return getSlice(d, "replicaSets")
}

// ExternalMember holds the identifying information for a replica set member
// that is still running on a VM during the migration.
type ExternalMember struct {
	ProcessID   string  `json:"processId"`
	Hostname    string  `json:"hostname"`
	Port        int     `json:"port"`
	Votes       int     `json:"votes"`
	Priority    float32 `json:"priority"`
	ArbiterOnly bool    `json:"arbiterOnly,omitempty"`
}
