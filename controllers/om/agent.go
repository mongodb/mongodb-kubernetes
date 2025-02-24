package om

import (
	"strings"
	"time"

	"go.uber.org/zap"
)

// Checks if the agents have registered.

type AutomationAgentStatusResponse struct {
	OMPaginated
	AutomationAgents []AgentStatus `json:"results"`
}

type AgentStatus struct {
	ConfCount int    `json:"confCount"`
	Hostname  string `json:"hostname"`
	LastConf  string `json:"lastConf"`
	StateName string `json:"stateName"`
	TypeName  string `json:"typeName"`
}

var _ Paginated = AutomationAgentStatusResponse{}

// IsRegistered will return true if this given agent has `hostname_prefix` as a
// prefix. This is needed to check if the given agent has registered.
func (agent AgentStatus) IsRegistered(hostnamePrefix string, log *zap.SugaredLogger) bool {
	lastPing, err := time.Parse(time.RFC3339, agent.LastConf)
	if err != nil {
		log.Error("Wrong format for lastConf field: expected UTC format but the value is " + agent.LastConf)
		return false
	}
	if strings.HasPrefix(agent.Hostname, hostnamePrefix) {
		// Any pings earlier than 1 minute ago are signs that agents are in trouble, so we cannot consider them as
		// registered (maybe we should decrease this to ~5-10 seconds?)
		if lastPing.Add(time.Minute).Before(time.Now()) {
			log.Debugw("Agent is registered but its last ping was more than 1 minute ago", "ping",
				lastPing, "hostname", agent.Hostname)
			return false
		}
		log.Debugw("Agent is already registered", "hostname", agent.Hostname)
		return true
	}

	return false
}

// Results are needed to fulfil the Paginated interface
func (aar AutomationAgentStatusResponse) Results() []interface{} {
	ans := make([]interface{}, len(aar.AutomationAgents))
	for i, aa := range aar.AutomationAgents {
		ans[i] = aa
	}
	return ans
}

type Status interface {
	IsRegistered(hostnamePrefix string, log *zap.SugaredLogger) bool
}
