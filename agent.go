package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/om"
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

// CheckAgentExists will return true if any of the agents in the json document
// has `hostname_prefix` as prefix.
// This is needed to check if given agent has registered.
func CheckAgentExists(hostname_prefix string, j []byte) bool {
	var agentState AgentState

	if err := json.Unmarshal(j, &agentState); err != nil {
		fmt.Println("Unable to unmarshal")
		return false
	}

	fmt.Printf("%d agents registerd total\n", agentState.TotalCount)
	for _, result := range agentState.Results {
		fmt.Printf("Checking prefix for agent: %s\n", result.Hostname)
		if strings.HasPrefix(result.Hostname, hostname_prefix) {
			return true
		}
	}

	return false
}

// WaitUntilAgentsHaveRegistered will stop the execution with time.Sleep until the
// agents have registered in the omConnection. Or enough time has passed in which
// case it will return false.
func WaitUntilAgentsHaveRegistered(omConnection *om.OmConnection, agentHostnames []string) bool {
	agentsOk := false

	// TODO: Implement exponential backoff
	for count := 0; count < 3; count++ {
		time.Sleep(3 * time.Second)

		path := fmt.Sprintf(OpsManagerAgentsResource, omConnection.GroupId)
		agentResponse, err := omConnection.Get(path)
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
