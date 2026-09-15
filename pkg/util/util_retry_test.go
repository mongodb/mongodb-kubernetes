package util

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestDoAndRetryWithContext_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	f := func() (string, bool) {
		calls++
		cancel()
		return "not yet", false
	}

	start := time.Now()
	ok, msg := DoAndRetryWithContext(ctx, f, zap.NewNop().Sugar(), 10, 5)

	assert.False(t, ok)
	assert.Equal(t, "not yet.", msg)
	assert.Equal(t, 1, calls, "no further attempts once the context is cancelled")
	assert.Less(t, time.Since(start), 5*time.Second, "must not wait out the interval once the context is cancelled")
}

func TestDoAndRetryWithContext_RetriesUntilSuccess(t *testing.T) {
	calls := 0
	f := func() (string, bool) {
		calls++
		return "done", calls == 2
	}

	ok, msg := DoAndRetryWithContext(context.Background(), f, zap.NewNop().Sugar(), 5, 0)

	assert.True(t, ok)
	assert.Equal(t, "done", msg)
	assert.Equal(t, 2, calls)
}

func TestDoAndRetryWithContext_GivesUpAfterCount(t *testing.T) {
	calls := 0
	f := func() (string, bool) {
		calls++
		return "nope", false
	}

	ok, msg := DoAndRetryWithContext(context.Background(), f, zap.NewNop().Sugar(), 3, 0)

	assert.False(t, ok)
	assert.Equal(t, "nope.", msg)
	assert.Equal(t, 3, calls)
}
