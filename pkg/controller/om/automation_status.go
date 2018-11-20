package om

import (
	"encoding/json"
)

type AutomationStatus struct {
	GoalVersion int             `json:"goalVersion"`
	Processes   []ProcessStatus `json:"processes"`
}

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

// CheckAutomationStatusIsGoal returns true if all the processes are in Goal
// state.
func checkAutomationStatusIsGoal(as *AutomationStatus) bool {
	for _, p := range as.Processes {
		if p.LastGoalVersionAchieved != as.GoalVersion {
			return false
		}
	}

	return true
}
