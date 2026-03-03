package migrate

import (
	"fmt"

	"github.com/spf13/cast"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

type Severity string

const (
	SeverityError   Severity = "ERROR"
	SeverityWarning Severity = "WARNING"
)

type Blocker struct {
	Severity Severity
	Message  string
}

func ValidateMigrationBlockers(ac *om.AutomationConfig) []Blocker {
	var blockers []Blocker
	blockers = append(blockers, checkOneDeploymentPerProject(ac.Deployment)...)
	return blockers
}

func countDeployments(ac map[string]interface{}) int {
	sharding := getSlice(ac, "sharding")
	shardedCount := len(sharding)

	shardRSNames := map[string]bool{}
	for _, s := range sharding {
		sMap, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		for _, sh := range getSlice(sMap, "shards") {
			if shMap, ok := sh.(map[string]interface{}); ok {
				shardRSNames[cast.ToString(shMap["_id"])] = true
			}
		}
	}

	replicaSets, _ := discoverReplicaSets(ac)
	independentRSCount := 0
	for _, rs := range replicaSets {
		rsMap, ok := rs.(map[string]interface{})
		if !ok {
			continue
		}
		rsID := cast.ToString(rsMap["_id"])
		if !shardRSNames[rsID] {
			independentRSCount++
		}
	}

	return shardedCount + independentRSCount
}

func checkOneDeploymentPerProject(ac map[string]interface{}) []Blocker {
	count := countDeployments(ac)
	if count <= 1 {
		return nil
	}
	return []Blocker{{
		Severity: SeverityError,
		Message:  fmt.Sprintf("project contains %d deployments but the operator requires exactly one deployment per Ops Manager project; split the project before migrating", count),
	}}
}
