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
	Results []ResultStruct `json:"results,omitempty"`
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
func CheckAgentExists(hostname_prefix string, agentState *AgentState, log *zap.SugaredLogger) bool {
	for _, result := range agentState.Results {
		lastPing, err := time.Parse(time.RFC3339, result.LastConf)
		if err != nil {
			log.Error("Wrong format for lastConf field: expected UTC format but the value is " + result.LastConf)
			return false
		}
		if strings.HasPrefix(result.Hostname, hostname_prefix) {
			// Any pings earlier than 1 minute ago are signs that agents are in trouble, so we cannot consider them as
			// registered (may be we should decrease this to ~5-10 seconds?)
			if lastPing.Add(time.Minute).Before(time.Now()) {
				log.Debugw("Agent is registered but its last ping was more than 1 minute ago", "ping",
					lastPing, "hostname", result.Hostname)
				return false
			}
			log.Debugw("Agent is already registered", "hostname", result.Hostname)
			return true
		}
	}

	return false
}
