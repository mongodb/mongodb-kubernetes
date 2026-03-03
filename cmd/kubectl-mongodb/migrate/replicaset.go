package migrate

import (
	"sort"

	"github.com/spf13/cast"
)

type ExternalMember struct {
	Hostname    string  `json:"hostname"`
	Port        int     `json:"port"`
	Votes       int     `json:"votes"`
	Priority    float32 `json:"priority"`
	ArbiterOnly bool    `json:"arbiterOnly,omitempty"`
}

func findReplicaSet(ac map[string]interface{}) map[string]interface{} {
	replicaSets, _ := discoverReplicaSets(ac)
	if len(replicaSets) == 0 {
		return nil
	}
	rsMap, ok := replicaSets[0].(map[string]interface{})
	if !ok {
		return nil
	}
	return rsMap
}

func discoverReplicaSets(ac map[string]interface{}) ([]interface{}, bool) {
	replicaSets := getSlice(ac, "replicaSets")
	if len(replicaSets) > 0 {
		return replicaSets, false
	}

	inferred := inferReplicaSetsFromProcesses(getSlice(ac, "processes"))
	if len(inferred) > 0 {
		return inferred, true
	}

	return nil, false
}

func inferReplicaSetsFromProcesses(processes []interface{}) []interface{} {
	membersByRS := map[string][]interface{}{}

	for _, p := range processes {
		proc, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		processType := cast.ToString(proc["processType"])
		if processType != "" && processType != "mongod" {
			continue
		}

		host := cast.ToString(proc["name"])
		if host == "" {
			continue
		}

		rsName := "standalone"
		if args, ok := proc["args2_6"].(map[string]interface{}); ok {
			if replication, ok := args["replication"].(map[string]interface{}); ok {
				if name := cast.ToString(replication["replSetName"]); name != "" {
					rsName = name
				}
			}
		}

		member := map[string]interface{}{
			"host":     host,
			"votes":    1,
			"priority": 1,
		}
		membersByRS[rsName] = append(membersByRS[rsName], member)
	}

	if len(membersByRS) == 0 {
		return nil
	}

	rsNames := make([]string, 0, len(membersByRS))
	for name := range membersByRS {
		rsNames = append(rsNames, name)
	}
	sort.Strings(rsNames)

	replicaSets := make([]interface{}, 0, len(rsNames))
	for _, name := range rsNames {
		replicaSets = append(replicaSets, map[string]interface{}{
			"_id":     name,
			"members": membersByRS[name],
		})
	}
	return replicaSets
}

func getSlice(m map[string]interface{}, key string) []interface{} {
	if v, ok := m[key]; ok {
		if s, ok := v.([]interface{}); ok {
			return s
		}
	}
	return nil
}

func buildProcessMap(processes []interface{}) map[string]map[string]interface{} {
	processMap := make(map[string]map[string]interface{})
	for _, p := range processes {
		proc, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		name := cast.ToString(proc["name"])
		if name != "" {
			processMap[name] = proc
		}
	}
	return processMap
}

func extractMemberInfo(members []interface{}, processMap map[string]map[string]interface{}) ([]ExternalMember, string, string) {
	var externalMembers []ExternalMember
	version := ""
	fcv := ""

	for _, m := range members {
		member, ok := m.(map[string]interface{})
		if !ok {
			continue
		}

		host := cast.ToString(member["host"])
		votes := cast.ToInt(member["votes"])
		priority := cast.ToFloat32(member["priority"])
		arbiterOnly := cast.ToBool(member["arbiterOnly"])

		port := 27017
		hostname := host

		if proc, ok := processMap[host]; ok {
			hostname = cast.ToString(proc["hostname"])
			if args, ok := proc["args2_6"].(map[string]interface{}); ok {
				if net, ok := args["net"].(map[string]interface{}); ok {
					if p := cast.ToInt(net["port"]); p != 0 {
						port = p
					}
				}
			}
			if version == "" {
				version = cast.ToString(proc["version"])
			}
			if fcv == "" {
				fcv = cast.ToString(proc["featureCompatibilityVersion"])
			}
		}

		externalMembers = append(externalMembers, ExternalMember{
			Hostname:    hostname,
			Port:        port,
			Votes:       votes,
			Priority:    priority,
			ArbiterOnly: arbiterOnly,
		})
	}

	return externalMembers, version, fcv
}
