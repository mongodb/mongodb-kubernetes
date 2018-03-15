package om

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
		if strings.HasPrefix(result.Hostname, hostname_prefix) {
			fmt.Printf("Agent %s is already registered\n", result.Hostname)
			return true
		}
	}

	return false
}

// WaitUntilAgentsHaveRegistered will stop the execution with time. Sleep until the
// agents have registered in the omConnection. Or enough time has passed in which
// case it will return false.
func WaitUntilAgentsHaveRegistered(omConnection *OmConnection, agentHostnames ...string) bool {
	// TODO: Implement exponential backoff
	for count := 0; count < 3; count++ {
		waitDuration := time.Duration(3)
		fmt.Printf("Waiting for %d seconds before checking if agents have registered in OM\n", waitDuration)
		time.Sleep(waitDuration * time.Second)

		agentResponse, err := omConnection.ReadAutomationAgents()
		if err != nil {
			fmt.Println("Unable to read from OM API")
			fmt.Println(err)
			continue
		}

		registeredCount := 0
		for _, hostname := range agentHostnames {
			if CheckAgentExists(hostname, agentResponse) {
				registeredCount++
			}
		}

		if registeredCount == len(agentHostnames) {
			return true;
		}
		fmt.Printf("Only %d of %d agents have registered with OM\n", registeredCount, len(agentHostnames))
	}

	return false
}
