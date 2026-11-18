package util

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/cache"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestThrottle(t *testing.T) {
	const interval = time.Minute

	type step struct {
		key     any
		elapsed time.Duration
		allowed bool
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "only the first call within the interval is allowed",
			steps: []step{
				{key: "a", allowed: true},
				{key: "a", allowed: false},
			},
		},
		{
			name: "keys are throttled independently",
			steps: []step{
				{key: "a", allowed: true},
				{key: "b", allowed: true},
				{key: "a", allowed: false},
			},
		},
		{
			name: "key is allowed again after the interval and then throttled again",
			steps: []step{
				{key: "a", allowed: true},
				{key: "a", elapsed: interval + time.Nanosecond, allowed: true},
				{key: "a", allowed: false},
			},
		},
		{
			name: "struct keys are throttled independently",
			steps: []step{
				{key: types.NamespacedName{Namespace: "ns", Name: "rs"}, allowed: true},
				{key: types.NamespacedName{Namespace: "ns", Name: "rs"}, allowed: false},
				{key: types.NamespacedName{Namespace: "other", Name: "rs"}, allowed: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClock := clocktesting.NewFakeClock(time.Now())
			throttle := newThrottle(interval, cache.NewLRUExpireCacheWithClock(throttleMaxKeys, fakeClock))

			for _, s := range tt.steps {
				fakeClock.Step(s.elapsed)
				assert.Equal(t, s.allowed, throttle(s.key), "key %v", s.key)
			}
		})
	}
}
