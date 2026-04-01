package workflow

import "math"

const (
	defaultBaseBackoffSeconds = 10
	defaultMaxBackoffSeconds  = 300
)

// ExponentialBackoff computes an exponential backoff duration in seconds for
// the given attempt number using the default base (10s) and cap (300s).
func ExponentialBackoff(attempt int) int {
	return ExponentialBackoffWithConfig(attempt, defaultBaseBackoffSeconds, defaultMaxBackoffSeconds)
}

// ExponentialBackoffWithConfig computes an exponential backoff: baseSeconds * 2^attempt,
// capped at maxSeconds. Negative attempt values are treated as 0.
func ExponentialBackoffWithConfig(attempt, baseSeconds, maxSeconds int) int {
	if attempt < 0 {
		attempt = 0
	}
	backoff := float64(baseSeconds) * math.Pow(2, float64(attempt))
	if backoff > float64(maxSeconds) {
		return maxSeconds
	}
	return int(backoff)
}
