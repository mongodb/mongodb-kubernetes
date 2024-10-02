package om

import (
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

const automationAgentKubeUpgradePlan = "ChangeVersionKube"

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

// WaitForReadyState waits until the agents for relevant processes reach their state
func WaitForReadyState(oc Connection, processNames []string, supressErrors bool, log *zap.SugaredLogger) error {
	log.Infow("Waiting for MongoDB agents to reach READY state...", "processes", processNames)

	reachStateFunc := func() (string, bool) {
		as, lastErr := oc.ReadAutomationStatus()
		if lastErr != nil {
			return fmt.Sprintf("Error reading Automation Agents status: %s", lastErr), false
		}

		if checkAutomationStatusIsGoal(as, processNames) {
			return "", true
		}

		return "MongoDB agents haven't reached READY state", false
	}
	if !util.DoAndRetry(reachStateFunc, log, 30, 3) {
		if supressErrors {
			log.Warnf("automation agents haven't reached READY state but the error is supressed")
			return nil
		}
		return apierror.New(xerrors.Errorf("automation agents haven't reached READY state during defined interval"))
	}
	log.Info("MongoDB agents have reached READY state")
	return nil
}

// CheckAutomationStatusIsGoal returns true if all the relevant processes are in Goal
// state.
// Note, that the function is quite tolerant to any situations except for non-matching goal state, for example
// if one of the requested processes doesn't exist in the list of OM status processes - this is considered as ok
// (maybe we are doing the scale down for the RS and some members were removed from OM manually - this is ok as the Operator
// will fix this later)
func checkAutomationStatusIsGoal(as *AutomationStatus, relevantProcesses []string) bool {
	for _, p := range as.Processes {
		if !stringutil.Contains(relevantProcesses, p.Name) {
			continue
		}
		for _, plan := range p.Plan {
			// This means the following:
			// - the cluster is in static architecture
			// - the agents are in a dedicated upgrade process, waiting for their binaries to be replaced by kubernetes
			// - this can only happen if the statefulset is ready, therefore we are returning ready here
			if plan == automationAgentKubeUpgradePlan {
				zap.S().Debug("cluster is in changeVersionKube mode, returning the agent is ready.")
				return true
			}
		}
	}

	for _, p := range as.Processes {
		if !stringutil.Contains(relevantProcesses, p.Name) {
			continue
		}

		if p.LastGoalVersionAchieved != as.GoalVersion {
			return false
		}
	}
	return true
}
