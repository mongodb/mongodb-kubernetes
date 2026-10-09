package ommetrics

import (
	"context"
	"time"
)

// StalePruner is a manager.Runnable that periodically calls Multiplexer.PruneStale.
type StalePruner struct {
	multiplexer *Multiplexer
	interval    time.Duration
	maxAge      time.Duration
}

func NewStalePruner(multiplexer *Multiplexer, interval, maxAge time.Duration) *StalePruner {
	return &StalePruner{multiplexer: multiplexer, interval: interval, maxAge: maxAge}
}

func (p *StalePruner) Start(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.multiplexer.PruneStale(p.maxAge)
		}
	}
}
