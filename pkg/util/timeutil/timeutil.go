package timeutil

import "time"

// Now returns the current time formatted with RFC3339
func Now() string {
	return time.Now().Format(time.RFC3339)
}
