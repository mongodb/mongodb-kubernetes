package util

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestDoAndRetry_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	f := func() (string, bool) {
		calls++
		cancel()
		return "not yet", false
	}

	start := time.Now()
	ok, msg := DoAndRetry(ctx, f, zap.NewNop().Sugar(), 10, 5)

	assert.False(t, ok)
	assert.Equal(t, "not yet.", msg)
	assert.Equal(t, 1, calls, "no further attempts once the context is cancelled")
	assert.Less(t, time.Since(start), 5*time.Second, "must not wait out the interval once the context is cancelled")
}

func TestDoAndRetry_StopsOnCancelWithZeroInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	f := func() (string, bool) {
		calls++
		cancel()
		return "not yet", false
	}

	// With a zero interval the timer and ctx.Done() are ready at the same time, so the select after f may pick
	// the timer. The check at the top of the loop must still stop the next attempt.
	ok, msg := DoAndRetry(ctx, f, zap.NewNop().Sugar(), 10, 0)

	assert.False(t, ok)
	assert.Equal(t, "not yet.", msg)
	assert.Equal(t, 1, calls, "no further attempts once the context is cancelled, even with a zero interval")
}

func TestDoAndRetry_NoAttemptWhenContextAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	f := func() (string, bool) {
		calls++
		return "should not run", false
	}

	ok, msg := DoAndRetry(ctx, f, zap.NewNop().Sugar(), 5, 0)

	assert.False(t, ok)
	assert.Equal(t, 0, calls, "f must not be invoked when the context is already done")
	assert.Equal(t, context.Canceled.Error(), msg)
}

func TestDoAndRetry_RetriesUntilSuccess(t *testing.T) {
	calls := 0
	f := func() (string, bool) {
		calls++
		return "done", calls == 2
	}

	ok, msg := DoAndRetry(context.Background(), f, zap.NewNop().Sugar(), 5, 0)

	assert.True(t, ok)
	assert.Equal(t, "done", msg)
	assert.Equal(t, 2, calls)
}

func TestDoAndRetry_GivesUpAfterCount(t *testing.T) {
	calls := 0
	f := func() (string, bool) {
		calls++
		return "nope", false
	}

	ok, msg := DoAndRetry(context.Background(), f, zap.NewNop().Sugar(), 3, 0)

	assert.False(t, ok)
	assert.Equal(t, "nope.", msg)
	assert.Equal(t, 3, calls)
}
