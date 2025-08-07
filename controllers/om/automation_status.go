package om

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
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
	if len(processNames) == 0 {
		log.Infow("Not waiting for MongoDB agents to reach READY state (no expected processes to wait for)")
		return nil
	}

	log.Infow("Waiting for MongoDB agents to reach READY state...", "processes", processNames)
	reachStateFunc := func() (string, bool) {
		as, lastErr := oc.ReadAutomationStatus()
		if lastErr != nil {
			return fmt.Sprintf("Error reading Automation Agents status: %s", lastErr), false
		}

		if allReachedGoalState, msg := checkAutomationStatusIsGoal(as, processNames, log); allReachedGoalState {
			return msg, true
		} else {
			return fmt.Sprintf("MongoDB agents haven't reached READY state; %s", msg), false
		}
	}
	ok, msg := util.DoAndRetry(reachStateFunc, log, 30, 3)
	if !ok {
		if supressErrors {
			log.Warnf("automation agents haven't reached READY state but the error is supressed")
			return nil
		}
		return apierror.New(xerrors.Errorf("automation agents haven't reached READY state during defined interval: %s", msg))
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
func checkAutomationStatusIsGoal(as *AutomationStatus, relevantProcesses []string, log *zap.SugaredLogger) (bool, string) {
	if areAnyAgentsInKubeUpgradeMode(as, relevantProcesses, log) {
		return true, ""
	}

	goalsNotAchievedMap := map[string]int{}
	goalsAchievedMap := map[string]int{}
	authTransitionInProgress := map[string]string{}

	for _, p := range as.Processes {
		if !stringutil.Contains(relevantProcesses, p.Name) {
			continue
		}
		if p.LastGoalVersionAchieved == as.GoalVersion {
			goalsAchievedMap[p.Name] = p.LastGoalVersionAchieved

			// Check if authentication transitions are in the current plan
			// If a process has reached goal version but still has auth-related moves in plan,
			// it means authentication transition is likely in progress
			// The plan contains non-completed move names from the API
			for _, move := range p.Plan {
				if isAuthenticationTransitionMove(move) {
					authTransitionInProgress[p.Name] = move
					break
				}
			}
		} else {
			goalsNotAchievedMap[p.Name] = p.LastGoalVersionAchieved
		}
	}

	var goalsNotAchievedMsgList []string
	for processName, goalAchieved := range goalsNotAchievedMap {
		goalsNotAchievedMsgList = append(goalsNotAchievedMsgList, fmt.Sprintf("%s@%d", processName, goalAchieved))
	}
	goalsAchievedMsgList := slices.Collect(maps.Keys(goalsAchievedMap))
	sort.Strings(goalsAchievedMsgList)

	// Check if any authentication transitions are in progress
	if len(authTransitionInProgress) > 0 {
		var authTransitionMsgList []string
		for processName, step := range authTransitionInProgress {
			authTransitionMsgList = append(authTransitionMsgList, fmt.Sprintf("%s:%s", processName, step))
		}
		log.Infow("Authentication transitions still in progress, waiting for completion",
			"processes", authTransitionMsgList)
		return false, fmt.Sprintf("authentication transitions in progress for %d processes: %s",
			len(authTransitionInProgress), authTransitionMsgList)
	}

	if len(goalsNotAchievedMap) > 0 {
		return false, fmt.Sprintf("%d processes waiting to reach automation config goal state (version=%d): %s, %d processes reached goal state: %s",
			len(goalsNotAchievedMap), as.GoalVersion, goalsNotAchievedMsgList, len(goalsAchievedMsgList), goalsAchievedMsgList)
	} else if len(goalsAchievedMap) == 0 {
		return true, "there were no processes in automation config matched with the processes to wait for"
	} else {
		return true, fmt.Sprintf("processes that reached goal state: %s", goalsAchievedMsgList)
	}
}

// isAuthenticationTransitionMove returns true if the given move is related to authentication transitions
func isAuthenticationTransitionMove(move string) bool {
	authMoves := map[string]struct{}{
		"RestartMongod":   {},
		"UpdateAuth":      {},
		"UpdateConfig":    {},
		"WaitForHealthy":  {},
		"InitiateReplSet": {},
	}

	 _, ok := authMoves[move]

	 return ok
}

func areAnyAgentsInKubeUpgradeMode(as *AutomationStatus, relevantProcesses []string, log *zap.SugaredLogger) bool {
	for _, p := range as.Processes {
		if !stringutil.Contains(relevantProcesses, p.Name) {
			continue
		}
		for _, planStep := range p.Plan {
			// This means the following:
			// - the cluster is in static architecture
			// - the agents are in a dedicated upgrade process, waiting for their binaries to be replaced by kubernetes
			// - this can only happen if the statefulset is ready, therefore we are returning ready here
			if planStep == automationAgentKubeUpgradePlan {
				log.Debug("cluster is in changeVersionKube mode, returning the agent is ready.")
				return true
			}
		}
	}
	return false
}
