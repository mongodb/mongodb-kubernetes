package main

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"time"

	"github.com/spf13/cast"
	"go.uber.org/zap"
)

const AgentHealthStatus = "/var/log/mongodb-mms-automation/agent-health-status.json"

type AgentStatus map[string]interface{}

func readAgentHealthStatus(file *os.File) (AgentStatus, error) {
	var status AgentStatus

	data, err := ioutil.ReadAll(file)
	if err != nil {
		return status, err
	}

	err = json.Unmarshal(data, &status)
	return status, err
}

func isPodReady() bool {
	cfg := zap.NewDevelopmentConfig()
	cfg.OutputPaths = []string{
		"/mongodb-automation/files/readiness.log",
	}
	log, err := cfg.Build()
	if err != nil {
		os.Exit(1)
	}
	logger := log.Sugar()

	fd, err := os.Open(AgentHealthStatus)
	if err != nil {
		logger.Warn("No health status file exists, assuming the Automation agent is old")
		return true
	}
	defer fd.Close()

	status, err := readAgentHealthStatus(fd)
	if err != nil {
		logger.Errorf("Failed to read agent health status file: %s", err)
		return false
	}

	statusesValue, ok := status["statuses"]

	if !ok {
		logger.Error("The health status file has incorrect format: no 'statuses' key")
		return false
	}

	if ok && len(statusesValue.(map[string]interface{})) == 0 {
		logger.Info("'statuses' is empty.")
		return true
	}

	statuses := statusesValue.(map[string]interface{})
	for _, v := range statuses {
		v0 := v.(map[string]interface{})
		isUp := v0["ExpectedToBeUp"].(bool)
		inGoalState := v0["IsInGoalState"].(bool)
		lastMongoUp := cast.ToInt64(v0["LastMongoUpTime"])
		logger.Debug(lastMongoUp)
		logger.Debugf("ExpectedToBeUp: %t, IsInGoalState: %t, LastMongoUpTime: %v", isUp, inGoalState, time.Unix(lastMongoUp, 0))
		return inGoalState
	}

	// This is also considering a situation where "statuses" is empty
	logger.Error("We are not expected to get to this stage!")
	return false
}

func main() {
	if !isPodReady() {
		os.Exit(1)
	}
}
