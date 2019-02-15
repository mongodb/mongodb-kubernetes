package om

import (
	"encoding/json"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
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

// Waits until the agents for relevant processes reach their state
func WaitForReadyState(oc Connection, processNames []string, log *zap.SugaredLogger) error {
	log.Infow("Waiting for automation config to be applied by Automation Agents...", "processes", processNames)
	reachStateFunc := func() (string, bool) {

		as, lastErr := oc.ReadAutomationStatus()
		if lastErr != nil {
			return fmt.Sprintf("Error reading Automation Agents status: %s", lastErr), false
		}

		if checkAutomationStatusIsGoal(as, processNames) {
			return "", true
		}

		return "Automation agents haven't reached READY state", false
	}
	if !util.DoAndRetry(reachStateFunc, log, 30, 3) {
		return NewAPIError(fmt.Errorf("Failed to start databases during defined interval"))
	}
	log.Info("Automation config has been successfully updated in Ops Manager and Automation Agents reached READY state")
	return nil
}

// CheckAutomationStatusIsGoal returns true if all the relevant processes are in Goal
// state.
// Note, that the function is quite tolerant to any situations except for non matching goal state, for example
// if one of the requested processes doesn't exist in the list of OM status processes - this is considered as ok
// (may be we are doing the scale down for the RS and some members were removed from OM manually - this is ok as the Operator
// will fix this later)
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
