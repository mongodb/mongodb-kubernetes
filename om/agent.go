package om

import (
	"encoding/json"
	"strings"

	"time"

	"go.uber.org/zap"
)

// Checks if the agents have registered.

// AgentState represents the json document returned by the agents API.
type AgentState struct {
	Links      string         `json:"-"`
	Results    []ResultStruct `json:"results,omitempty"`
	TotalCount int            `json:"totalCount"`
}

// ResultStruct represents the json document pointed by the "results" key
// in the agents API response.
type ResultStruct struct {
	ConfCount int    `json:"confCount"`
	Hostname  string `json:"hostname"`
	LastConf  string `json:"lastConf"`
	StateName string `json:"stateName"`
	TypeName  string `json:"typeName"`
}

func BuildAgentStateFromBytes(jsonBytes []byte) (*AgentState, error) {
	cc := &AgentState{}
	if err := json.Unmarshal(jsonBytes, &cc); err != nil {
		return nil, err
	}
	return cc, nil
}

// CheckAgentExists will return true if any of the agents in the json document
// has `hostname_prefix` as prefix.
// This is needed to check if given agent has registered.
func CheckAgentExists(hostname_prefix string, agentState *AgentState) bool {
	for _, result := range agentState.Results {
		lastPing, err := time.Parse(time.RFC3339, result.LastConf)
		if err != nil {
			zap.S().Error("Wrong format for lastConf field: expected UTC format but the value is " + result.LastConf)
			return false
		}
		if strings.HasPrefix(result.Hostname, hostname_prefix) {
			// Any pings earlier than 1 minute ago are signs that agents are in trouble, so we cannot consider them as
			// registered (may be we should decrease this to ~5-10 seconds?)
			if lastPing.Add(time.Minute).Before(time.Now()) {
				zap.S().Debugw("Agent is registered but its last ping was more than 1 minute ago", "ping",
					lastPing, "hostname", result.Hostname)
				return false
			}
			zap.S().Debugw("Agent is already registered", "hostname", result.Hostname)
			return true
		}
	}

	return false
}

// WaitUntilAgentsHaveRegistered will stop the execution with time. Sleep until the
// agents have registered in the omConnection. Or enough time has passed in which
// case it will return false.
func WaitUntilAgentsHaveRegistered(omConnection *OmConnection, agentHostnames ...string) bool {
	wait := WaitFunction(10, 10)
	for {
		if !wait() {
			break
		}
		zap.S().Debug("Waiting for agents to register with OM")
		agentResponse, err := omConnection.ReadAutomationAgents()
		if err != nil {
			zap.S().Error("Unable to read from OM API: ", err)
			continue
		}

		registeredCount := 0
		for _, hostname := range agentHostnames {
			if CheckAgentExists(hostname, agentResponse) {
				registeredCount++
			}
		}

		if registeredCount == len(agentHostnames) {
			return true
		}
		zap.S().Infof("Only %d of %d agents have registered with OM\n", registeredCount, len(agentHostnames))
	}

	return false
}
