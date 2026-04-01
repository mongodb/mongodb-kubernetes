package workflow

import (
	"testing"
)

func TestExponentialBackoff(t *testing.T) {
	tests := []struct {
		attempt  int
		expected int
	}{
		{0, 10},   // 10 * 2^0 = 10
		{1, 20},   // 10 * 2^1 = 20
		{2, 40},   // 10 * 2^2 = 40
		{3, 80},   // 10 * 2^3 = 80
		{4, 160},  // 10 * 2^4 = 160
		{5, 300},  // 10 * 2^5 = 320, capped at 300
		{10, 300}, // capped at 300
	}

	for _, tt := range tests {
		result := ExponentialBackoff(tt.attempt)
		if result != tt.expected {
			t.Errorf("ExponentialBackoff(%d) = %d, want %d", tt.attempt, result, tt.expected)
		}
	}
}

func TestExponentialBackoffNegativeAttempt(t *testing.T) {
	result := ExponentialBackoff(-1)
	if result != 10 {
		t.Errorf("ExponentialBackoff(-1) = %d, want 10", result)
	}
}

func TestExponentialBackoffWithConfig(t *testing.T) {
	tests := []struct {
		attempt     int
		base        int
		max         int
		expected    int
		description string
	}{
		{0, 5, 100, 5, "base case"},
		{1, 5, 100, 10, "first retry"},
		{2, 5, 100, 20, "second retry"},
		{3, 5, 100, 40, "third retry"},
		{4, 5, 100, 80, "fourth retry"},
		{5, 5, 100, 100, "capped at max"},
		{10, 5, 100, 100, "well beyond cap"},
		{0, 1, 5, 1, "small base"},
		{3, 1, 5, 5, "small base capped"},
		{-1, 5, 100, 5, "negative attempt treated as 0"},
	}

	for _, tt := range tests {
		result := ExponentialBackoffWithConfig(tt.attempt, tt.base, tt.max)
		if result != tt.expected {
			t.Errorf("ExponentialBackoffWithConfig(%d, %d, %d) [%s] = %d, want %d",
				tt.attempt, tt.base, tt.max, tt.description, result, tt.expected)
		}
	}
}
