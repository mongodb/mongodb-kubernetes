package ommetrics

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStalePruner(t *testing.T) {
	reg := newRegistry(NewConfig())
	mustEnsure(t, reg, Project{BaseURL: "https://om.example.com", GroupID: "g1"}, "k", TLSOptions{})
	var now atomic.Int64
	reg.now = now.Load
	now.Store(int64(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewStalePruner(&Multiplexer{reg: reg}, time.Millisecond, time.Minute).Start(ctx)
	}()

	deadline := time.After(5 * time.Second)
	for len(reg.snapshot()) != 0 {
		select {
		case <-deadline:
			t.Fatal("pruner did not remove the stale destination")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}
}
