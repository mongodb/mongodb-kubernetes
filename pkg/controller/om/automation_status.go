package om

import (
	"encoding/json"

	"go.uber.org/zap"
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
		zap.S().Infow("Waiting to reach goal state",
			"currentState", p.LastGoalVersionAchieved,
			"goalState", as.GoalVersion)
		if p.LastGoalVersionAchieved != as.GoalVersion {
			return false
		}
	}

	return true
}

// WaitUntilGoalState will return after all automations agents
// have reported to reach Goal state.
func WaitUntilGoalState(om OmConnection) bool {
	var lastErr error
	wait := WaitFunction(30, 3)

	for {
		if !wait() {
			break
		}

		zap.S().Debug("Waiting for automation agents to reach Goal state")

		as, lastErr := om.ReadAutomationStatus()
		if lastErr != nil {
			continue
		}

		if checkAutomationStatusIsGoal(as) {
			return true
		}
	}

	if lastErr != nil {
		zap.S().Error(lastErr)
	}

	return false
}
