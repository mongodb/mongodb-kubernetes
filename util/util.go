package util

import (
	"time"

	"go.uber.org/zap"
)

// ************** This is a file containing any general "algorithmic" or "util" functions used by other packages

// FindLeftDifference finds the difference between arrays of string - the elements that are present in "left" but absent
// in "right" array
func FindLeftDifference(left, right []string) []string {
	ans := make([]string, 0)
	for _, v := range left {
		skip := false
		for _, p := range right {
			if p == v {
				skip = true
				break
			}
		}
		if !skip {
			ans = append(ans, v)
		}
	}
	return ans
}

// Int32Ref is required to return a *int32, which can't be declared as a literal.
func Int32Ref(i int32) *int32 {
	return &i
}

// BooleanRef is required to return a *bool, which can't be declared as a literal.
func BooleanRef(b bool) *bool {
	return &b
}

// DoAndRetry performs the task 'f' until it returns true or 'count' retrials are executed. Sleeps for 'interval' seconds
// between retries
func DoAndRetry(f func() bool, log *zap.SugaredLogger, count, interval int) bool {
	for i := 0; i < count; i++ {
		if f() {
			return true
		}
		// if we are on the last iteration - returning as there's no need to wait and retry again
		if i != count-1 {
			log.Debugf("Retrial attempt %d of %d (waiting for %d more seconds)", i+2, count, interval)
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}
	return false
}
