package util

import (
	"time"

	"k8s.io/apimachinery/pkg/util/cache"
)

const throttleMaxKeys = 1000

// NewThrottle returns a function that allows an action for a key at most once per interval; the first call for a key is always allowed.
func NewThrottle(interval time.Duration) func(key any) bool {
	return newThrottle(interval, cache.NewLRUExpireCache(throttleMaxKeys))
}

func newThrottle(interval time.Duration, c *cache.LRUExpireCache) func(key any) bool {
	return func(key any) bool {
		if _, ok := c.Get(key); ok {
			return false
		}
		c.Add(key, struct{}{}, interval)
		return true
	}
}
