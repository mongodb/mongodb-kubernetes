package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Checks if the agents have registered.

type AgentState struct {
	Links      string         `json:"-"`
	Results    []ResultStruct `json:"results,omitempty"`
	TotalCount int            `json:"totalCount"`
}

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
