package om

import (
	"encoding/json"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// AutomationStatus represents the status of automation agents registered with Ops Manager
type AutomationStatus struct {
	GoalVersion int             `json:"goalVersion"`
	Processes   []ProcessStatus `json:"processes"`
}

// ProcessStatus status of the process and what's the last version achieved
type ProcessStatus struct {
	Hostname                string   `json:"hostname"`
	Name                    string   `json:"name"`
	LastGoalVersionAchieved int      `json:"lastGoalVersionAchieved"`
	Plan                    []string `json:"plan"`
}

func buildAutomationStatusFromBytes(b []byte) (*AutomationStatus, error) {
	as := &AutomationStatus{}
	if err := json.Unmarshal(b, &as); err != nil {
		return nil, err
	}

	return as, nil
}

// CheckAutomationStatusIsGoal returns true if all the relevant processes are in Goal
// state.
func checkAutomationStatusIsGoal(as *AutomationStatus, relevantProcesses []string) bool {
	for _, p := range as.Processes {
		if !util.ContainsString(relevantProcesses, p.Name) {
			continue
		}
		if p.LastGoalVersionAchieved != as.GoalVersion {
			return false
		}
	}
	return true
}
