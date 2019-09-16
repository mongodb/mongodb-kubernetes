package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"go.uber.org/zap"
)

const AgentHealthStatus = "/var/log/mongodb-mms-automation/agent-health-status.json"

var riskySteps []string
var logger *zap.SugaredLogger

func init() {
	riskySteps = []string{"WaitAllRsMembersUp", "WaitRsInit"}

	// By default we log to the output (convenient for tests)
	cfg := zap.NewDevelopmentConfig()
	log, err := cfg.Build()
	if err != nil {
		panic(err)
	}
	logger = log.Sugar()
}

type Health struct {
	Healthiness  map[string]processHealth     `json:"statuses"`
	ProcessPlans map[string]mmsDirectorStatus `json:"mmsStatus"`
}

type processHealth struct {
	IsInGoalState   bool  `json:"IsInGoalState"`
	LastMongoUpTime int64 `json:"LastMongoUpTime"`
	ExpectedToBeUp  bool  `json:"ExpectedToBeUp"`
}

func (h processHealth) String() string {
	return fmt.Sprintf("ExpectedToBeUp: %t, IsInGoalState: %t, LastMongoUpTime: %v", h.ExpectedToBeUp,
		h.IsInGoalState, time.Unix(h.LastMongoUpTime, 0))
}

// These structs are copied from go_planner mmsdirectorstatus.go. Some fields are pruned as not used.
type mmsDirectorStatus struct {
	Name                              string        `json:"name"`
	LastGoalStateClusterConfigVersion int64         `json:"lastGoalVersionAchieved"`
	Plans                             []*PlanStatus `json:"plans"`
}

type PlanStatus struct {
	Moves []*MoveStatus `json:"moves"`
}

type MoveStatus struct {
	Steps []*StepStatus `json:"steps"`
}
type StepStatus struct {
	Step      string     `json:"step"`
	Started   *time.Time `json:"started"`
	Completed *time.Time `json:"completed"`
	Result    string     `json:"result"`
}

func readAgentHealthStatus(file *os.File) (Health, error) {
	var health Health

	data, err := ioutil.ReadAll(file)
	if err != nil {
		return health, err
	}

	err = json.Unmarshal(data, &health)
	return health, err
}

func isPodReady(healStatusPath string) bool {
	fd, err := os.Open(healStatusPath)
	if err != nil {
		logger.Warn("No health status file exists, assuming the Automation agent is old")
		return true
	}
	defer fd.Close()

	health, err := readAgentHealthStatus(fd)
	if err != nil {
		logger.Errorf("Failed to read agent health status file: %s", err)
		// panicking allows to see the problem in the events for the pod (kubectl describe pod ..)
		panic("Failed to read agent health status file: %s")
	}

	if len(health.Healthiness) == 0 {
		logger.Info("'statuses' is empty. We assume there is no automation config for the agent yet.")
		return true
	}

	// If the agent has reached the goal state - returning true
	for _, v := range health.Healthiness {
		logger.Debug(v)
		if v.IsInGoalState {
			return true
		}
	}

	// Failback logic: the agent is not in goal state and got stuck in some steps
	if hasDeadlockedSteps(health) {
		return true
	}

	return false
}

func hasDeadlockedSteps(health Health) bool {
	for _, v := range health.ProcessPlans {
		for _, p := range v.Plans {
			for _, m := range p.Moves {
				for _, s := range m.Steps {
					if isDeadlocked(s) {
						return true
					}
				}
			}
		}
	}
	return false
}

func isDeadlocked(status *StepStatus) bool {
	// Some logic behind 15 seconds: the health status file is dumped each 10 seconds so we are sure that if the agent
	// has been in the the step for 10 seconds - this means it is waiting for the other hosts and they are not available
	fifteenSecondsAgo := time.Now().Add(time.Duration(-15) * time.Second)
	if containsString(riskySteps, status.Step) && status.Completed == nil && status.Started.Before(fifteenSecondsAgo) {
		logger.Infof("Indicated a possible deadlock, status: %s, started at %s but hasn't finished "+
			"yet. Marking the probe as ready", status.Step, status.Started)
		return true
	}
	return false
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func main() {
	cfg := zap.NewDevelopmentConfig()
	// In "production" we log to the file
	cfg.OutputPaths = []string{
		"/var/log/mongodb-mms-automation/readiness.log",
	}
	log, err := cfg.Build()
	if err != nil {
		panic(err)
	}
	logger = log.Sugar()
	if !isPodReady(AgentHealthStatus) {
		os.Exit(1)
	}
}
