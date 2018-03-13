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

func BuildAgentStateFromBytes(jsonBytes []byte) (ans *AgentState, err error) {
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
	fmt.Printf("%d agents registered in total\n", agentState.TotalCount)
	for _, result := range agentState.Results {
		fmt.Printf("Checking prefix for agent: %s\n", result.Hostname)
		if strings.HasPrefix(result.Hostname, hostname_prefix) {
			return true
		}
	}

	return false
}

// WaitUntilAgentsHaveRegistered will stop the execution with time. Sleep until the
// agents have registered in the omConnection. Or enough time has passed in which
// case it will return false.
func WaitUntilAgentsHaveRegistered(omConnection *OmConnection, agentHostnames ...string) bool {
	agentsOk := false

	// TODO: Implement exponential backoff
	for count := 0; count < 3; count++ {
		time.Sleep(3 * time.Second)

		agentResponse, err := omConnection.ReadAutomationAgents()
		if err != nil {
			fmt.Println("Unable to read from OM API, waiting...")
			fmt.Println(err)
			continue
		}

		fmt.Println("Checking if the agent have registered yet")
		agentsOk = true
		for _, hostname := range agentHostnames {
			if !CheckAgentExists(hostname, agentResponse) {
				agentsOk = false
				break
			}
		}

		if agentsOk {
			break
		}
		fmt.Println("Agents have not registered with OM, waiting...")
	}

	return agentsOk
}
