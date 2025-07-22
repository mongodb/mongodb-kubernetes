package om

import (
	"go.uber.org/zap/zaptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckAutomationStatusIsGoal(t *testing.T) {
	type args struct {
		as                *AutomationStatus
		relevantProcesses []string
	}
	tests := []struct {
		name           string
		args           args
		expectedResult bool
		expectedMsg    string
	}{
		{
			name: "all in goal state",
			args: args{
				as: &AutomationStatus{
					Processes: []ProcessStatus{
						{
							Name:                    "a",
							Plan:                    []string{"FCV"},
							LastGoalVersionAchieved: 1,
						},
						{
							Name:                    "b",
							Plan:                    []string{"FCV"},
							LastGoalVersionAchieved: 1,
						},
					},
					GoalVersion: 1,
				},
				relevantProcesses: []string{"a", "b"},
			},
			expectedResult: true,
			// We can not check for the full message as the ordering of the processes won't be deterministic (stored in a map)
			expectedMsg: "processes that reached goal state: [a b]",
		}, {
			name: "one not in goal state",
			args: args{
				as: &AutomationStatus{
					Processes: []ProcessStatus{
						{
							Name:                    "a",
							Plan:                    []string{"FCV"},
							LastGoalVersionAchieved: 0,
						},
						{
							Name:                    "b",
							Plan:                    []string{"FCV"},
							LastGoalVersionAchieved: 1,
						},
					},
					GoalVersion: 1,
				},
				relevantProcesses: []string{"a", "b"},
			},
			expectedResult: false,
			expectedMsg:    "1 processes waiting to reach automation config goal state (version=1): [a@0], 1 processes reached goal state: [b]",
		}, {
			name: "one not in goal state but at least one is in kube upgrade",
			args: args{
				as: &AutomationStatus{
					Processes: []ProcessStatus{
						{
							Name:                    "a",
							Plan:                    []string{"FCV", "something-else"},
							LastGoalVersionAchieved: 0,
						},
						{
							Name:                    "b",
							Plan:                    []string{"FCV", automationAgentKubeUpgradePlan},
							LastGoalVersionAchieved: 1,
						},
					},
					GoalVersion: 1,
				},
				relevantProcesses: []string{"a", "b"},
			},
			expectedResult: true,
			// we don't return any msg for agentKubeUpgradePlan
			expectedMsg: "",
		}, {
			name: "none of the processes matched with AC",
			args: args{
				as: &AutomationStatus{
					Processes: []ProcessStatus{
						{
							Name:                    "a",
							Plan:                    []string{"X", "Y"},
							LastGoalVersionAchieved: 1,
						},
						{
							Name:                    "b",
							Plan:                    []string{"Y", "Z"},
							LastGoalVersionAchieved: 1,
						},
					},
					GoalVersion: 1,
				},
				relevantProcesses: []string{"c", "d"},
			},
			// we return true when there weren't any processes to wait for in AC
			expectedResult: true,
			expectedMsg:    "there were no processes in automation config matched with the processes to wait for",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goal, msg := checkAutomationStatusIsGoal(tt.args.as, tt.args.relevantProcesses, zaptest.NewLogger(t).Sugar())
			assert.Equalf(t, tt.expectedResult, goal, "checkAutomationStatusIsGoal(%v, %v)", tt.args.as, tt.args.relevantProcesses)
			assert.Contains(t, msg, tt.expectedMsg)
		})
	}
}
