package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
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
							Plan:                    []string{"FCV", automationAgentKubeUpgradeMove},
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
			goal, msg := checkAutomationStatusIsGoal(tt.args.as, tt.args.relevantProcesses, zap.S())
			assert.Equalf(t, tt.expectedResult, goal, "checkAutomationStatusIsGoal(%v, %v)", tt.args.as, tt.args.relevantProcesses)
			assert.Contains(t, msg, tt.expectedMsg)
		})
	}
}

func TestCheckAutomationStatusIsGoal_AuthenticationTransitions(t *testing.T) {
	logger := zap.NewNop().Sugar()

	tests := []struct {
		name              string
		automationStatus  *AutomationStatus
		relevantProcesses []string
		expectedReady     bool
		expectedMessage   string
	}{
		{
			name: "should wait for UpgradeAuthModeFromAuthOffToAuthTransition move to complete",
			automationStatus: &AutomationStatus{
				GoalVersion: 5,
				Processes: []ProcessStatus{
					{
						Name:                    "rs0_0",
						LastGoalVersionAchieved: 5,
						Plan:                    []string{"UpgradeAuthModeFromAuthOffToAuthTransition"},
					},
				},
			},
			relevantProcesses: []string{"rs0_0"},
			expectedReady:     false,
			expectedMessage:   "authentication transitions in progress for 1 processes",
		},
		{
			name: "should be ready when authentication transitions are complete",
			automationStatus: &AutomationStatus{
				GoalVersion: 5,
				Processes: []ProcessStatus{
					{
						Name:                    "rs0_0",
						LastGoalVersionAchieved: 5,
						Plan:                    []string{}, // Empty plan means all moves completed
					},
				},
			},
			relevantProcesses: []string{"rs0_0"},
			expectedReady:     true,
			expectedMessage:   "processes that reached goal state: [rs0_0]",
		},
		{
			name: "should wait for multiple processes with auth transitions",
			automationStatus: &AutomationStatus{
				GoalVersion: 7,
				Processes: []ProcessStatus{
					{
						Name:                    "rs0_0",
						LastGoalVersionAchieved: 7,
						Plan:                    []string{}, // This process completed
					},
					{
						Name:                    "rs0_1",
						LastGoalVersionAchieved: 7,
						Plan:                    []string{"UpgradeAuthModeFromAuthTransitionToAuthOn"}, // Auth-related move in progress
					},
				},
			},
			relevantProcesses: []string{"rs0_0", "rs0_1"},
			expectedReady:     false,
			expectedMessage:   "authentication transitions in progress for 1 processes",
		},
		{
			name: "should ignore non-authentication moves in progress",
			automationStatus: &AutomationStatus{
				GoalVersion: 4,
				Processes: []ProcessStatus{
					{
						Name:                    "rs0_0",
						LastGoalVersionAchieved: 4,
						Plan:                    []string{"SomeOtherMove"}, // Non-auth move
					},
				},
			},
			relevantProcesses: []string{"rs0_0"},
			expectedReady:     true,
			expectedMessage:   "processes that reached goal state: [rs0_0]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, message := checkAutomationStatusIsGoal(
				tt.automationStatus,
				tt.relevantProcesses,
				logger,
			)

			assert.Equal(t, tt.expectedReady, ready, "Ready state should match expected")
			assert.Contains(t, message, tt.expectedMessage, "Message should contain expected text")

			if tt.expectedReady {
				t.Logf("✅ Process correctly marked as ready: %s", message)
			} else {
				t.Logf("⏳ Process correctly waiting for auth transition: %s", message)
			}
		})
	}
}

func TestIsAuthenticationTransitionMove(t *testing.T) {
	authMoves := []string{
		"UpgradeAuthModeFromAuthOffToAuthTransition",
		"UpgradeAuthModeFromAuthTransitionToAuthOn",
		"DowngradeAuthModeFromAuthOnToAuthTransition",
		"DowngradeAuthModeFromAuthTransitionToAuthOff",
	}

	nonAuthMoves := []string{
		"SomeOtherMove",
		"CreateIndex",
		"DropCollection",
		"BackupDatabase",
		"UpdateAuth",     // These are NOT real auth transition move names
		"WaitAuthUpdate", // These are step names, not move names
	}

	for _, move := range authMoves {
		t.Run("auth_move_"+move, func(t *testing.T) {
			assert.True(t, isAuthenticationTransitionMove(move),
				"Move %s should be recognized as authentication transition", move)
		})
	}

	for _, move := range nonAuthMoves {
		t.Run("non_auth_move_"+move, func(t *testing.T) {
			assert.False(t, isAuthenticationTransitionMove(move),
				"Move %s should not be recognized as authentication transition", move)
		})
	}
}
