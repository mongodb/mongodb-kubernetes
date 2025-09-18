package main

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/agent"
)

func TestLastMongoUpTimeIgnored(t *testing.T) {
	// Create two health statuses that are identical except for LastMongoUpTime
	startTime := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	health1 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"test-pod-0": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: startTime.Unix(),
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"test-pod-0": {
				Name: "test-process",
				Plans: []*agent.PlanStatus{
					{
						Started: &startTime,
					},
				},
			},
		},
	}

	laterTime := time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC)
	health2 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"test-pod-0": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: laterTime.Unix(), // Different time
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"test-pod-0": {
				Name: "test-process",
				Plans: []*agent.PlanStatus{
					{
						Started: &startTime,
					},
				},
			},
		},
	}

	// Test 1: Without ignoring LastMongoUpTime, should detect difference
	diffWithoutIgnore := cmp.Diff(health1, health2)
	if diffWithoutIgnore == "" {
		t.Error("Expected difference when not ignoring LastMongoUpTime, but got no difference")
	}

	// Test 2: With ignoring time fields, should detect no difference
	diffWithIgnore := cmp.Diff(health1, health2,
		cmpopts.IgnoreFields(agent.ProcessHealth{}, "LastMongoUpTime"),
		cmpopts.IgnoreFields(agent.PlanStatus{}, "Started", "Completed"),
		cmpopts.IgnoreFields(agent.StepStatus{}, "Started", "Completed"),
	)
	if diffWithIgnore != "" {
		t.Errorf("Expected no difference when ignoring time fields, but got: %s", diffWithIgnore)
	}

	// Test 3: With meaningful change (IsInGoalState), should still detect difference even when ignoring LastMongoUpTime
	health3 := health2
	health3.Healthiness["test-pod-0"] = agent.ProcessHealth{
		IsInGoalState:   false, // Changed from true to false
		ExpectedToBeUp:  true,
		LastMongoUpTime: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC).Unix(), // Different time
	}

	diffMeaningfulChange := cmp.Diff(health1, health3,
		cmpopts.IgnoreFields(agent.ProcessHealth{}, "LastMongoUpTime"),
		cmpopts.IgnoreFields(agent.PlanStatus{}, "Started", "Completed"),
		cmpopts.IgnoreFields(agent.StepStatus{}, "Started", "Completed"),
	)
	if diffMeaningfulChange == "" {
		t.Error("Expected difference when IsInGoalState changes, even when ignoring LastMongoUpTime")
	}

	t.Logf("Test passed: LastMongoUpTime is properly ignored while other changes are detected")
}

func TestPlanChangesDetected(t *testing.T) {
	// Create two health statuses with different plans
	startTime := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	health1 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"test-pod-0": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: startTime.Unix(),
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"test-pod-0": {
				Name: "test-process",
				Plans: []*agent.PlanStatus{
					{
						Started: &startTime,
					},
				},
			},
		},
	}

	laterTime := time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC)
	health2 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"test-pod-0": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: laterTime.Unix(), // Different time (should be ignored)
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"test-pod-0": {
				Name: "test-process",
				Plans: []*agent.PlanStatus{
					{
						Started: &startTime,
					},
					{
						Started: &laterTime, // New plan added
					},
				},
			},
		},
	}

	// Should detect difference due to new plan, even though time fields are ignored
	diff := cmp.Diff(health1, health2,
		cmpopts.IgnoreFields(agent.ProcessHealth{}, "LastMongoUpTime"),
		cmpopts.IgnoreFields(agent.PlanStatus{}, "Started", "Completed"),
		cmpopts.IgnoreFields(agent.StepStatus{}, "Started", "Completed"),
	)
	if diff == "" {
		t.Error("Expected difference when new plan is added, even when ignoring LastMongoUpTime")
	}

	t.Logf("Test passed: Plan changes are properly detected while ignoring time fields")
}

func TestAllTimeFieldsIgnored(t *testing.T) {
	// Create two health statuses with different time fields but same meaningful content
	startTime1 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	startTime2 := time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC)
	completedTime1 := time.Date(2025, 1, 1, 10, 30, 0, 0, time.UTC)
	completedTime2 := time.Date(2025, 1, 1, 11, 30, 0, 0, time.UTC)

	health1 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"test-pod-0": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: startTime1.Unix(),
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"test-pod-0": {
				Name: "test-process",
				Plans: []*agent.PlanStatus{
					{
						Started:   &startTime1,
						Completed: &completedTime1,
						Moves: []*agent.MoveStatus{
							{
								Move: "TestMove",
								Steps: []*agent.StepStatus{
									{
										Step:      "TestStep",
										Started:   &startTime1,
										Completed: &completedTime1,
										Result:    "SUCCESS",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	health2 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"test-pod-0": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: startTime2.Unix(), // Different time
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"test-pod-0": {
				Name: "test-process",
				Plans: []*agent.PlanStatus{
					{
						Started:   &startTime2,   // Different time
						Completed: &completedTime2, // Different time
						Moves: []*agent.MoveStatus{
							{
								Move: "TestMove",
								Steps: []*agent.StepStatus{
									{
										Step:      "TestStep",
										Started:   &startTime2,   // Different time
										Completed: &completedTime2, // Different time
										Result:    "SUCCESS",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Should detect no difference when ignoring all time fields
	diff := cmp.Diff(health1, health2,
		cmpopts.IgnoreFields(agent.ProcessHealth{}, "LastMongoUpTime"),
		cmpopts.IgnoreFields(agent.PlanStatus{}, "Started", "Completed"),
		cmpopts.IgnoreFields(agent.StepStatus{}, "Started", "Completed"),
	)
	if diff != "" {
		t.Errorf("Expected no difference when ignoring all time fields, but got: %s", diff)
	}

	t.Logf("Test passed: All time fields (LastMongoUpTime, Started, Completed) are properly ignored")
}

func TestConciseDiff(t *testing.T) {
	// Create realistic health statuses based on the test data structure
	startTime := time.Date(2019, 9, 11, 14, 20, 40, 0, time.UTC)
	completedTime := time.Date(2019, 9, 11, 14, 21, 42, 0, time.UTC)

	health1 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"bar": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: 1568222195,
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"bar": {
				Name: "bar",
				LastGoalStateClusterConfigVersion: 5,
				Plans: []*agent.PlanStatus{
					{
						Started:   &startTime,
						Completed: &completedTime,
						Moves: []*agent.MoveStatus{
							{
								Move: "WaitRsInit",
								Steps: []*agent.StepStatus{
									{
										Step:      "WaitRsInit",
										Started:   &startTime,
										Completed: nil, // Currently running
										Result:    "wait",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Simulate a step completion
	stepCompletedTime := time.Date(2019, 9, 11, 14, 22, 0, 0, time.UTC)
	health2 := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"bar": {
				IsInGoalState:   true,
				ExpectedToBeUp:  true,
				LastMongoUpTime: 1568222200, // Different time (should be ignored)
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"bar": {
				Name: "bar",
				LastGoalStateClusterConfigVersion: 5,
				Plans: []*agent.PlanStatus{
					{
						Started:   &startTime,
						Completed: &completedTime,
						Moves: []*agent.MoveStatus{
							{
								Move: "WaitRsInit",
								Steps: []*agent.StepStatus{
									{
										Step:      "WaitRsInit",
										Started:   &startTime,
										Completed: &stepCompletedTime, // Now completed
										Result:    "success", // Changed from "wait" to "success"
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Test concise diff
	diff := conciseDiff(health1, health2)
	if diff == "" {
		t.Error("Expected concise diff to detect step result change")
	}

	// Verify it contains the actual change
	if !strings.Contains(diff, "Result") {
		t.Error("Expected concise diff to contain 'Result' field change")
	}

	t.Logf("Concise diff output (%d chars):\n%s", len(diff), diff)
	t.Logf("Test passed: Concise diff produces shorter, focused output with current step info")
}

func TestCurrentStepInfo(t *testing.T) {
	// Create health status with a currently running step
	startTime := time.Date(2019, 9, 11, 14, 20, 40, 0, time.UTC)

	health := agent.Health{
		Healthiness: map[string]agent.ProcessHealth{
			"bar": {
				IsInGoalState:   false,
				ExpectedToBeUp:  true,
				LastMongoUpTime: 1568222195,
			},
		},
		ProcessPlans: map[string]agent.MmsDirectorStatus{
			"bar": {
				Name: "bar",
				Plans: []*agent.PlanStatus{
					{
						Started: &startTime,
						Moves: []*agent.MoveStatus{
							{
								Move: "Start",
								Steps: []*agent.StepStatus{
									{
										Step:      "StartFresh",
										Started:   &startTime,
										Completed: nil, // Currently running
										Result:    "in_progress",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Test current step info extraction
	stepInfo := getCurrentStepInfo(health)
	if stepInfo == "" {
		t.Error("Expected getCurrentStepInfo to return current step information")
	}

	if !strings.Contains(stepInfo, "Start") || !strings.Contains(stepInfo, "StartFresh") {
		t.Errorf("Expected step info to contain move 'Start' and step 'StartFresh', got: %s", stepInfo)
	}

	t.Logf("Current step info: %s", stepInfo)
	t.Logf("Test passed: Current step info extraction works correctly")
}
