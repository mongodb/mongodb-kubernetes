package om

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/xerrors"
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

func TestCheckAutomationStatusIsGoal_ParkedAgents(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name              string
		processes         []ProcessStatus
		relevantProcesses []string
		expectedReady     bool
	}{
		{
			name: "agent parked waiting for the operator makes the agents ready",
			processes: []ProcessStatus{
				{Hostname: "host-0", Name: "rs-0", LastGoalVersionAchieved: 41, Execution: &ProcessExecution{AutomationConfigVersion: 42, Move: "ExternalNodeRestart", Step: ExecutionStepWaitNodeReplacement, ReportedAt: now}},
				{Hostname: "host-1", Name: "rs-1", LastGoalVersionAchieved: 42},
			},
			relevantProcesses: []string{"rs-0", "rs-1"},
			expectedReady:     true,
		},
		{
			name: "stale parked report does not make the agents ready",
			processes: []ProcessStatus{
				{Hostname: "host-0", Name: "rs-0", LastGoalVersionAchieved: 41, Execution: &ProcessExecution{AutomationConfigVersion: 42, Move: "ExternalNodeRestart", Step: ExecutionStepWaitNodeReplacement, ReportedAt: now.Add(-StaleProcessDuration - time.Second)}},
				{Hostname: "host-1", Name: "rs-1", LastGoalVersionAchieved: 42},
			},
			relevantProcesses: []string{"rs-0", "rs-1"},
			expectedReady:     false,
		},
		{
			name: "parked report for another goal version does not make the agents ready",
			processes: []ProcessStatus{
				{Hostname: "host-0", Name: "rs-0", LastGoalVersionAchieved: 41, Execution: &ProcessExecution{AutomationConfigVersion: 41, Move: "ExternalNodeRestart", Step: ExecutionStepWaitNodeReplacement, ReportedAt: now}},
				{Hostname: "host-1", Name: "rs-1", LastGoalVersionAchieved: 42},
			},
			relevantProcesses: []string{"rs-0", "rs-1"},
			expectedReady:     false,
		},
		{
			name: "parked process outside the relevant processes does not make the agents ready",
			processes: []ProcessStatus{
				{Hostname: "host-0", Name: "rs-0", LastGoalVersionAchieved: 41, Execution: &ProcessExecution{AutomationConfigVersion: 42, Move: "ExternalNodeRestart", Step: ExecutionStepWaitNodeReplacement, ReportedAt: now}},
				{Hostname: "host-1", Name: "rs-1", LastGoalVersionAchieved: 41},
			},
			relevantProcesses: []string{"rs-1"},
			expectedReady:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as := &AutomationStatus{GoalVersion: 42, Processes: tt.processes}

			ready, _ := checkAutomationStatusIsGoal(as, tt.relevantProcesses, zap.NewNop().Sugar())

			assert.Equal(t, tt.expectedReady, ready)
		})
	}
}

func TestProcessExecution_IsParked(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name                                string
		execution                           *ProcessExecution
		goalVersion                         int
		expectedIsParked                    bool
		expectedIsWaitingForNodeReplacement bool
	}{
		{
			name: "nil execution",
		},
		{
			name:        "wrong goal version",
			execution:   &ProcessExecution{AutomationConfigVersion: 41, Step: ExecutionStepWaitCanUpdate, ReportedAt: now},
			goalVersion: 42,
		},
		{
			name:        "stale report",
			execution:   &ProcessExecution{AutomationConfigVersion: 42, Step: ExecutionStepWaitCanUpdate, ReportedAt: now.Add(-StaleProcessDuration - time.Second)},
			goalVersion: 42,
		},
		{
			name:             "fresh WaitCanUpdate",
			execution:        &ProcessExecution{AutomationConfigVersion: 42, Step: ExecutionStepWaitCanUpdate, ReportedAt: now},
			goalVersion:      42,
			expectedIsParked: true,
		},
		{
			name:                                "fresh WaitNodeReplacement",
			execution:                           &ProcessExecution{AutomationConfigVersion: 42, Step: ExecutionStepWaitNodeReplacement, ReportedAt: now},
			goalVersion:                         42,
			expectedIsParked:                    true,
			expectedIsWaitingForNodeReplacement: true,
		},
		{
			name:        "other step",
			execution:   &ProcessExecution{AutomationConfigVersion: 42, Step: "StepDownIfPrimary", ReportedAt: now},
			goalVersion: 42,
		},
		{
			name:                                "report exactly at the staleness boundary is fresh",
			execution:                           &ProcessExecution{AutomationConfigVersion: 42, Step: ExecutionStepWaitNodeReplacement, ReportedAt: now.Add(-StaleProcessDuration)},
			goalVersion:                         42,
			expectedIsParked:                    true,
			expectedIsWaitingForNodeReplacement: true,
		},
		{
			name:        "report just past the staleness boundary",
			execution:   &ProcessExecution{AutomationConfigVersion: 42, Step: ExecutionStepWaitNodeReplacement, ReportedAt: now.Add(-StaleProcessDuration - time.Nanosecond)},
			goalVersion: 42,
		},
		{
			name:                                "report from the future is fresh",
			execution:                           &ProcessExecution{AutomationConfigVersion: 42, Step: ExecutionStepWaitNodeReplacement, ReportedAt: now.Add(time.Minute)},
			goalVersion:                         42,
			expectedIsParked:                    true,
			expectedIsWaitingForNodeReplacement: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedIsParked, tt.execution.IsParked(tt.goalVersion, now))
			assert.Equal(t, tt.expectedIsWaitingForNodeReplacement, tt.execution.IsWaitingForNodeReplacement(tt.goalVersion, now))
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
			name: "should wait for UpdateAuth move to complete",
			automationStatus: &AutomationStatus{
				GoalVersion: 5,
				Processes: []ProcessStatus{
					{
						Name:                    "rs0_0",
						LastGoalVersionAchieved: 5,
						Plan:                    []string{"UpdateAuth"},
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
						Plan:                    []string{"WaitAuthUpdate"}, // Auth-related move in progress
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
		"UpdateAuth",
		"WaitAuthUpdate",
	}

	nonAuthMoves := []string{
		"SomeOtherMove",
		"CreateIndex",
		"DropCollection",
		"BackupDatabase",
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

func TestWaitForReadyState_GivesUpWhenContextIsDone(t *testing.T) {
	conn := NewMockedOmConnection(NewDeployment())
	calls := 0
	conn.ReadAutomationStatusFunc = func() (*AutomationStatus, error) {
		calls++
		return &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{{Name: "rs-0", LastGoalVersionAchieved: 0}}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := WaitForReadyState(ctx, conn, []string{"rs-0"}, false, zap.S())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "haven't reached READY state")
	assert.Contains(t, err.Error(), "context canceled")
	assert.Equal(t, 0, calls, "f must not be invoked when the context is already done")
}

func TestWaitForReadyState_StopsRetryingWhenContextIsCancelled(t *testing.T) {
	conn := NewMockedOmConnection(NewDeployment())
	calls := 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn.ReadAutomationStatusFunc = func() (*AutomationStatus, error) {
		calls++
		// the reconcile is cancelled while the wait is in progress, after the first status check
		cancel()
		return &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{{Name: "rs-0", LastGoalVersionAchieved: 0}}}, nil
	}

	start := time.Now()
	err := WaitForReadyState(ctx, conn, []string{"rs-0"}, false, zap.S())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "haven't reached READY state")
	assert.Equal(t, 1, calls, "the wait must stop at the first check instead of completing all 30 retries")
	assert.Less(t, time.Since(start), 3*time.Second, "must not wait out the retry interval once the context is cancelled")
}

func TestGetAgentErrors(t *testing.T) {
	// Only the tail of a long error is kept, so the last 500 runes lie entirely within the trailing run.
	longAgentError := strings.Repeat("é", 600) + strings.Repeat("ü", 500)
	trimmedLongAgentError := "…" + strings.Repeat("ü", 500)

	tests := []struct {
		name             string
		automationStatus *AutomationStatus
		processNames     []string
		expectedErrors   []AgentError
	}{
		{
			name: "error of a process below goal is returned",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: "could not connect to member"},
			}},
			processNames: []string{"rs-0"},
			expectedErrors: []AgentError{
				{Process: "rs-0", Code: 106, Message: "could not connect to member"},
			},
		},
		{
			name: "errors of two processes below goal are returned in status order",
			automationStatus: &AutomationStatus{GoalVersion: 3, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 2, ErrorCode: 106, ErrorString: "first"},
				{Name: "rs-1", LastGoalVersionAchieved: 2, ErrorCode: 5, ErrorString: "second"},
			}},
			processNames: []string{"rs-0", "rs-1"},
			expectedErrors: []AgentError{
				{Process: "rs-0", Code: 106, Message: "first"},
				{Process: "rs-1", Code: 5, Message: "second"},
			},
		},
		{
			name: "whitespace in the error is collapsed",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: "could not connect\n\tto   member"},
			}},
			processNames: []string{"rs-0"},
			expectedErrors: []AgentError{
				{Process: "rs-0", Code: 106, Message: "could not connect to member"},
			},
		},
		{
			name: "long error is trimmed keeping only the tail",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: longAgentError},
			}},
			processNames: []string{"rs-0"},
			expectedErrors: []AgentError{
				{Process: "rs-0", Code: 106, Message: trimmedLongAgentError},
			},
		},
		{
			name: "error of exactly the trimming threshold is not trimmed",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: strings.Repeat("é", 500)},
			}},
			processNames: []string{"rs-0"},
			expectedErrors: []AgentError{
				{Process: "rs-0", Code: 106, Message: strings.Repeat("é", 500)},
			},
		},
		{
			name: "error one rune above the trimming threshold is trimmed",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: "é" + strings.Repeat("ü", 500)},
			}},
			processNames: []string{"rs-0"},
			expectedErrors: []AgentError{
				{Process: "rs-0", Code: 106, Message: "…" + strings.Repeat("ü", 500)},
			},
		},
		{
			name: "error of a process at goal is ignored",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0},
				{Name: "rs-1", LastGoalVersionAchieved: 1, ErrorCode: 5, ErrorString: "boom"},
			}},
			processNames: []string{"rs-0", "rs-1"},
		},
		{
			name: "error of a process not waited for is ignored",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0},
				{Name: "rs-1", LastGoalVersionAchieved: 0, ErrorCode: 5, ErrorString: "boom"},
			}},
			processNames: []string{"rs-0"},
		},
		{
			name: "blank error is ignored",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 5, ErrorString: " \n\t "},
			}},
			processNames: []string{"rs-0"},
		},
		{
			name:             "no processes",
			automationStatus: &AutomationStatus{GoalVersion: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentErrors := GetAgentErrors(tt.automationStatus, func(name string) bool {
				return slices.Contains(tt.processNames, name)
			})

			assert.Equal(t, tt.expectedErrors, agentErrors)
		})
	}
}

func TestWaitForReadyState_LogsAgentErrors(t *testing.T) {
	tests := []struct {
		name             string
		automationStatus *AutomationStatus
		readError        error
		processNames     []string
		supressErrors    bool
		expectedLogs     []map[string]any
	}{
		{
			name: "error of a process below goal is logged",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: "could not connect to member"},
			}},
			processNames: []string{"rs-0"},
			expectedLogs: []map[string]any{
				{"process": "rs-0", "errorCode": int64(106), "error": "could not connect to member"},
			},
		},
		{
			name: "errors of two processes below goal are logged in status order",
			automationStatus: &AutomationStatus{GoalVersion: 3, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 2, ErrorCode: 106, ErrorString: "first"},
				{Name: "rs-1", LastGoalVersionAchieved: 2, ErrorCode: 5, ErrorString: "second"},
			}},
			processNames: []string{"rs-0", "rs-1"},
			expectedLogs: []map[string]any{
				{"process": "rs-0", "errorCode": int64(106), "error": "first"},
				{"process": "rs-1", "errorCode": int64(5), "error": "second"},
			},
		},
		{
			name: "error of a process not waited for is ignored",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0},
				{Name: "rs-1", LastGoalVersionAchieved: 0, ErrorCode: 5, ErrorString: "boom"},
			}},
			processNames: []string{"rs-0"},
		},
		{
			name: "error of a process below goal is logged during an auth transition",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 1, Plan: []string{"WaitAuthUpdate"}},
				{Name: "rs-1", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: "could not connect to member"},
			}},
			processNames: []string{"rs-0", "rs-1"},
			expectedLogs: []map[string]any{
				{"process": "rs-1", "errorCode": int64(106), "error": "could not connect to member"},
			},
		},
		{
			name:         "no error is logged when the automation status cannot be read",
			readError:    xerrors.New("boom"),
			processNames: []string{"rs-0"},
		},
		{
			name: "no error is logged when the agents report none",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0},
			}},
			processNames: []string{"rs-0"},
		},
		{
			name: "error is logged when errors are supressed",
			automationStatus: &AutomationStatus{GoalVersion: 1, Processes: []ProcessStatus{
				{Name: "rs-0", LastGoalVersionAchieved: 0, ErrorCode: 106, ErrorString: "could not connect to member"},
			}},
			processNames:  []string{"rs-0"},
			supressErrors: true,
			expectedLogs: []map[string]any{
				{"process": "rs-0", "errorCode": int64(106), "error": "could not connect to member"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := NewMockedOmConnection(NewDeployment())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			conn.ReadAutomationStatusFunc = func() (*AutomationStatus, error) {
				// stop the retry loop after the first status check
				cancel()
				return tt.automationStatus, tt.readError
			}
			core, logs := observer.New(zap.WarnLevel)

			err := WaitForReadyState(ctx, conn, tt.processNames, tt.supressErrors, zap.New(core).Sugar())

			entries := logs.FilterMessage("MongoDB agent reported an error").All()
			require.Len(t, entries, len(tt.expectedLogs))
			for i, expectedFields := range tt.expectedLogs {
				assert.Equal(t, zapcore.WarnLevel, entries[i].Level)
				assert.Equal(t, expectedFields, entries[i].ContextMap())
			}

			if tt.supressErrors {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "haven't reached READY state")
			if tt.automationStatus == nil {
				return
			}
			for _, p := range tt.automationStatus.Processes {
				if msg := strings.Join(strings.Fields(p.ErrorString), " "); msg != "" {
					assert.NotContains(t, err.Error(), msg)
				}
			}
		})
	}
}

func TestBuildAutomationStatusFromBytes_Execution(t *testing.T) {
	tests := []struct {
		name              string
		payload           string
		expectedExecution *ProcessExecution
	}{
		{
			name: "execution present",
			payload: `{"goalVersion":42,"processes":[{"name":"my-rs-0","plan":["ExternalNodeRestart"],` +
				`"execution":{"automationConfigVersion":42,"move":"ExternalNodeRestart","step":"WaitNodeReplacement","reportedAt":"2026-10-13T12:00:00Z"}}]}`,
			expectedExecution: &ProcessExecution{
				AutomationConfigVersion: 42,
				Move:                    "ExternalNodeRestart",
				Step:                    ExecutionStepWaitNodeReplacement,
				ReportedAt:              time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
			},
		},
		{
			name:    "execution absent",
			payload: `{"processes":[{"name":"my-rs-0"}]}`,
		},
		{
			name:    "execution null",
			payload: `{"processes":[{"name":"my-rs-0","execution":null}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as, err := buildAutomationStatusFromBytes([]byte(tt.payload))

			require.NoError(t, err)
			require.Len(t, as.Processes, 1)
			assert.Equal(t, tt.expectedExecution, as.Processes[0].Execution)
		})
	}
}

func TestBuildAutomationStatusFromBytes_ErrorFields(t *testing.T) {
	tests := []struct {
		name                string
		payload             string
		expectedErrorCode   int
		expectedErrorString string
	}{
		{
			name:    "error fields null",
			payload: `{"processes":[{"name":"rs-0","errorCode":null,"errorString":null}]}`,
		},
		{
			name:    "error fields absent",
			payload: `{"processes":[{"name":"rs-0"}]}`,
		},
		{
			name:    "error string empty",
			payload: `{"processes":[{"name":"rs-0","errorString":""}]}`,
		},
		{
			name:                "error fields populated",
			payload:             `{"processes":[{"name":"rs-0","errorCode":106,"errorString":"could not connect to member"}]}`,
			expectedErrorCode:   106,
			expectedErrorString: "could not connect to member",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as, err := buildAutomationStatusFromBytes([]byte(tt.payload))

			require.NoError(t, err)
			require.Len(t, as.Processes, 1)
			assert.Equal(t, tt.expectedErrorCode, as.Processes[0].ErrorCode)
			assert.Equal(t, tt.expectedErrorString, as.Processes[0].ErrorString)
		})
	}
}
