package main

import (
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

	// Test 2: With ignoring LastMongoUpTime, should detect no difference
	diffWithIgnore := cmp.Diff(health1, health2, cmpopts.IgnoreFields(agent.ProcessHealth{}, "LastMongoUpTime"))
	if diffWithIgnore != "" {
		t.Errorf("Expected no difference when ignoring LastMongoUpTime, but got: %s", diffWithIgnore)
	}

	// Test 3: With meaningful change (IsInGoalState), should still detect difference even when ignoring LastMongoUpTime
	health3 := health2
	health3.Healthiness["test-pod-0"] = agent.ProcessHealth{
		IsInGoalState:   false, // Changed from true to false
		ExpectedToBeUp:  true,
		LastMongoUpTime: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC).Unix(), // Different time
	}

	diffMeaningfulChange := cmp.Diff(health1, health3, cmpopts.IgnoreFields(agent.ProcessHealth{}, "LastMongoUpTime"))
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

	// Should detect difference due to new plan, even though LastMongoUpTime is ignored
	diff := cmp.Diff(health1, health2, cmpopts.IgnoreFields(agent.ProcessHealth{}, "LastMongoUpTime"))
	if diff == "" {
		t.Error("Expected difference when new plan is added, even when ignoring LastMongoUpTime")
	}

	t.Logf("Test passed: Plan changes are properly detected while ignoring LastMongoUpTime")
}
