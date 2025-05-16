package agents

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func TestCalculateProcessStateMap(t *testing.T) {
	sampleTimeStamp := "2023-06-03T10:00:00Z"
	testCases := []struct {
		name            string
		processStatuses []om.ProcessStatus
		agentStatuses   []om.AgentStatus
		expectError     bool
		expectedResult  map[string]ProcessState
	}{
		{
			name: "Single valid agent and single valid process",
			agentStatuses: []om.AgentStatus{
				{
					Hostname:  "host1",
					TypeName:  "AUTOMATION",
					LastConf:  "2023-01-02T15:04:05Z",
					StateName: "SomeState",
				},
			},
			processStatuses: []om.ProcessStatus{
				{
					Hostname:                "host1",
					Name:                    "shard",
					LastGoalVersionAchieved: 10,
					Plan:                    []string{"step1", "step2"},
				},
			},
			expectError: false,
			expectedResult: map[string]ProcessState{
				"host1": {
					Hostname:            "host1",
					LastAgentPing:       mustParseDate("2023-01-02T15:04:05Z"),
					GoalVersionAchieved: 10,
					ProcessName:         "shard",
					Plan:                []string{"step1", "step2"},
				},
			},
		},
		{
			name: "Multiple agents, single process (same host)",
			agentStatuses: []om.AgentStatus{
				{
					Hostname: "host1",
					TypeName: "AUTOMATION",
					LastConf: "2023-05-02T15:04:05Z",
				},
				{
					Hostname: "host1", // same host but repeated agent status
					TypeName: "AUTOMATION",
					LastConf: sampleTimeStamp,
				},
			},
			processStatuses: []om.ProcessStatus{
				{
					Hostname:                "host1",
					Name:                    "shard",
					LastGoalVersionAchieved: 3,
					Plan:                    []string{"planA"},
				},
			},
			expectError: false,
			// LastConf from the second agent status should overwrite the LastAgentPing
			expectedResult: map[string]ProcessState{
				"host1": {
					Hostname:            "host1",
					LastAgentPing:       mustParseDate(sampleTimeStamp),
					GoalVersionAchieved: 3,
					ProcessName:         "shard",
					Plan:                []string{"planA"},
				},
			},
		},
		{
			name: "Multiple agents, multiple processes",
			agentStatuses: []om.AgentStatus{
				{
					Hostname: "host1",
					TypeName: "AUTOMATION",
					LastConf: sampleTimeStamp,
				},
				{
					Hostname: "host1",
					TypeName: "AUTOMATION",
					LastConf: "2023-05-02T15:04:05Z", // Will overwrite above LastConf for host1
				},
				{
					Hostname: "host2",
					TypeName: "AUTOMATION",
					LastConf: sampleTimeStamp,
				},
				{
					Hostname: "host3", // This host is not in process statuses
					TypeName: "AUTOMATION",
					LastConf: sampleTimeStamp,
				},
			},
			processStatuses: []om.ProcessStatus{
				{
					Hostname:                "host1",
					Name:                    "shard",
					LastGoalVersionAchieved: 5,
					Plan:                    []string{"planZ"},
				},
				{
					Hostname:                "host1", // These values will overwrite the ones above
					Name:                    "mongos",
					LastGoalVersionAchieved: 1,
					Plan:                    []string{"planA, planB"},
				},
				{
					Hostname:                "host2",
					Name:                    "configserver",
					LastGoalVersionAchieved: 3,
					Plan:                    []string{"planC"},
				},
				{
					Hostname:                "host4", // This host is not in agentStatuses
					Name:                    "shard",
					LastGoalVersionAchieved: 5,
					Plan:                    []string{"planD"},
				},
			},
			expectError: false,
			expectedResult: map[string]ProcessState{
				"host1": {
					Hostname:            "host1",
					LastAgentPing:       mustParseDate("2023-05-02T15:04:05Z"),
					GoalVersionAchieved: 1,
					ProcessName:         "mongos",
					Plan:                []string{"planA, planB"},
				},
				"host2": {
					Hostname:            "host2",
					LastAgentPing:       mustParseDate(sampleTimeStamp),
					GoalVersionAchieved: 3,
					ProcessName:         "configserver",
					Plan:                []string{"planC"},
				},
				"host3": {
					Hostname:            "host3",
					LastAgentPing:       mustParseDate(sampleTimeStamp),
					GoalVersionAchieved: -1,
					ProcessName:         "",
					Plan:                nil,
				},
				"host4": {
					Hostname:            "host4",
					LastAgentPing:       time.Time{},
					GoalVersionAchieved: 5,
					ProcessName:         "shard",
					Plan:                []string{"planD"},
				},
			},
		},
		{
			name: "No overlapping values",
			agentStatuses: []om.AgentStatus{
				{
					Hostname: "host1",
					TypeName: "AUTOMATION",
					LastConf: sampleTimeStamp,
				},
				{
					Hostname: "host2",
					TypeName: "AUTOMATION",
					LastConf: "2023-05-02T15:04:05Z",
				},
			},
			processStatuses: []om.ProcessStatus{
				{
					Hostname:                "host3",
					Name:                    "configserver",
					LastGoalVersionAchieved: 1,
					Plan:                    []string{"planA"},
				},
				{
					Hostname:                "host4",
					Name:                    "shard",
					LastGoalVersionAchieved: 2,
					Plan:                    []string{"planB"},
				},
			},
			expectError: false,
			expectedResult: map[string]ProcessState{
				"host1": {
					Hostname:            "host1",
					LastAgentPing:       mustParseDate(sampleTimeStamp),
					GoalVersionAchieved: -1,
					ProcessName:         "",
					Plan:                nil,
				},
				"host2": {
					Hostname:            "host2",
					LastAgentPing:       mustParseDate("2023-05-02T15:04:05Z"),
					GoalVersionAchieved: -1,
					ProcessName:         "",
					Plan:                nil,
				},
				"host3": {
					Hostname:            "host3",
					LastAgentPing:       time.Time{},
					GoalVersionAchieved: 1,
					ProcessName:         "configserver",
					Plan:                []string{"planA"},
				},
				"host4": {
					Hostname:            "host4",
					LastAgentPing:       time.Time{},
					GoalVersionAchieved: 2,
					ProcessName:         "shard",
					Plan:                []string{"planB"},
				},
			},
		},
		{
			name:          "No agents, only processes",
			agentStatuses: nil,
			processStatuses: []om.ProcessStatus{
				{
					Hostname:                "host2",
					Name:                    "mongos",
					LastGoalVersionAchieved: 7,
					Plan:                    []string{"stepX", "stepY"},
				},
				{
					Hostname:                "host3",
					Name:                    "config",
					LastGoalVersionAchieved: 1,
					Plan:                    []string{"planC"},
				},
			},
			expectError: false,
			expectedResult: map[string]ProcessState{
				"host2": {
					Hostname:            "host2",
					LastAgentPing:       time.Time{},
					GoalVersionAchieved: 7,
					ProcessName:         "mongos",
					Plan:                []string{"stepX", "stepY"},
				},
				"host3": {
					Hostname:            "host3",
					LastAgentPing:       time.Time{},
					GoalVersionAchieved: 1,
					ProcessName:         "config",
					Plan:                []string{"planC"},
				},
			},
		},
		{
			name:            "No processes, only agents",
			processStatuses: nil,
			agentStatuses: []om.AgentStatus{
				{
					Hostname: "host4",
					TypeName: "AUTOMATION",
					LastConf: sampleTimeStamp,
				},
			},
			expectError: false,
			expectedResult: map[string]ProcessState{
				"host4": {
					Hostname:            "host4",
					LastAgentPing:       mustParseDate(sampleTimeStamp),
					GoalVersionAchieved: -1,
					Plan:                nil,
					ProcessName:         "",
				},
			},
		},
		{
			name: "Agent with invalid TypeName",
			agentStatuses: []om.AgentStatus{
				{
					Hostname: "host5",
					TypeName: "NOT_AUTOMATION",
					LastConf: sampleTimeStamp,
				},
			},
			processStatuses: nil,
			expectError:     true,
			expectedResult:  nil,
		},
		{
			name: "Agent with invalid LastConf format",
			agentStatuses: []om.AgentStatus{
				{
					Hostname: "host6",
					TypeName: "AUTOMATION",
					// Missing 'Z' or doesn't follow RFC3339
					LastConf: "2023-01-02 15:04:05",
				},
			},
			processStatuses: nil,
			expectError:     true,
			expectedResult:  nil,
		},
		{
			name:            "Empty slices",
			agentStatuses:   []om.AgentStatus{},
			processStatuses: []om.ProcessStatus{},
			expectError:     false,
			expectedResult:  map[string]ProcessState{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := calculateProcessStateMap(tc.processStatuses, tc.agentStatuses)
			if tc.expectError {
				require.Error(t, err, "Expected an error but got none")
			} else {
				require.NoError(t, err, "Did not expect an error but got one")
				require.Equal(t, tc.expectedResult, result)
			}
		})
	}
}

// mustParseDate is a helper that must successfully parse an RFC3339 date.
func mustParseDate(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestGetClusterState(t *testing.T) {
	testCases := []struct {
		name string
		// Mock ReadAutomationStatus func
		mockAutomationStatus    *om.AutomationStatus
		mockAutomationStatusErr error
		// Mock ReadAutomationAgents func
		mockAgentStatusResponse om.Paginated
		mockAgentStatusErr      error

		expectErr               bool
		expectedGoalVersion     int
		expectedProcessStateMap map[string]ProcessState
	}{
		{
			name: "Happy path with one agent and one process",
			mockAutomationStatus: &om.AutomationStatus{
				GoalVersion: 42,
				Processes: []om.ProcessStatus{
					{
						Hostname:                "host1",
						Name:                    "shard",
						LastGoalVersionAchieved: 7,
						Plan:                    []string{"step1", "step2"},
					},
				},
			},
			mockAutomationStatusErr: nil,
			mockAgentStatusResponse: om.AutomationAgentStatusResponse{
				OMPaginated: om.OMPaginated{TotalCount: 1},
				AutomationAgents: []om.AgentStatus{
					{
						Hostname: "host1",
						TypeName: "AUTOMATION",
						LastConf: "2024-01-01T00:00:00Z",
					},
				},
			},
			mockAgentStatusErr:  nil,
			expectErr:           false,
			expectedGoalVersion: 42,
			expectedProcessStateMap: map[string]ProcessState{
				"host1": {
					Hostname:            "host1",
					ProcessName:         "shard",
					LastAgentPing:       mustParseDate("2024-01-01T00:00:00Z"),
					GoalVersionAchieved: 7,
					Plan:                []string{"step1", "step2"},
				},
			},
		},
		{
			name:                    "Error when reading automation status",
			mockAutomationStatus:    nil,
			mockAutomationStatusErr: errors.New("cannot read automation status"),
			mockAgentStatusResponse: om.AutomationAgentStatusResponse{},
			mockAgentStatusErr:      nil,
			expectErr:               true,
		},
		{
			name: "Error when reading agent pages",
			mockAutomationStatus: &om.AutomationStatus{
				GoalVersion: 1,
				Processes:   nil,
			},
			mockAutomationStatusErr: nil,
			mockAgentStatusResponse: nil, // Not useful here
			mockAgentStatusErr:      errors.New("agent pages error"),
			expectErr:               true,
		},
		{
			name: "Invalid agent type triggers error in calculateProcessStateMap",
			mockAutomationStatus: &om.AutomationStatus{
				GoalVersion: 10,
				Processes: []om.ProcessStatus{
					{
						Hostname:                "host2",
						Name:                    "shard",
						LastGoalVersionAchieved: 2,
						Plan:                    []string{"planA"},
					},
				},
			},
			mockAutomationStatusErr: nil,
			mockAgentStatusResponse: om.AutomationAgentStatusResponse{
				OMPaginated: om.OMPaginated{TotalCount: 1},
				AutomationAgents: []om.AgentStatus{
					{
						Hostname: "host2",
						TypeName: "NOT_AUTOMATION",
						LastConf: "2023-06-03T10:00:00Z",
					},
				},
			},
			mockAgentStatusErr: nil,
			expectErr:          true,
		},
	}

	// For each iteration, we create a mocked OM connection and override the default behaviour of status methods
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockConn := &om.MockedOmConnection{}

			// Override the behavior of OM connection methods
			mockConn.ReadAutomationStatusFunc = func() (*om.AutomationStatus, error) {
				return tc.mockAutomationStatus, tc.mockAutomationStatusErr
			}
			mockConn.ReadAutomationAgentsFunc = func(_ int) (om.Paginated, error) {
				return tc.mockAgentStatusResponse, tc.mockAgentStatusErr
			}

			clusterState, err := GetMongoDBClusterState(mockConn)
			if tc.expectErr {
				require.Error(t, err, "Expected an error but got none")
				return
			}
			require.NoError(t, err, "Did not expect an error but got one")

			require.Equal(t, tc.expectedGoalVersion, clusterState.GoalVersion)
			require.Equal(t, tc.expectedProcessStateMap, clusterState.ProcessStateMap)
		})
	}
}
