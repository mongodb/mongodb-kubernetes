package ommetrics

import (
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/sdk/resource"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

var procStart = time.Now()

// monoNanos is monotonic nanos since process start, so the breaker is immune to wall-clock steps.
func monoNanos() int64 { return int64(time.Since(procStart)) }

// destination is one Ops Manager project receiving metrics. key, om and exp are immutable; the rest are atomics.
type destination struct {
	key            destKey
	om             *omInstance
	exp            sdkmetric.Exporter
	resource       atomic.Pointer[resource.Resource]
	agentAPIKey    atomic.Pointer[string] // Basic auth password; the username is key.groupID
	consecFail     atomic.Int32
	skipUntilNanos atomic.Int64 // 0 == circuit closed
	lastSeenNanos  atomic.Int64
}

// skipExport reports whether the circuit breaker is open at nowNanos.
func (d *destination) skipExport(nowNanos int64) bool {
	return nowNanos < d.skipUntilNanos.Load()
}

func (d *destination) recordResult(err error, nowNanos int64, cfg Config) {
	if err == nil {
		d.skipUntilNanos.Store(0) // close the circuit before clearing the counter
		d.consecFail.Store(0)
		return
	}
	n := d.consecFail.Add(1)
	if int(n) < cfg.BreakerThreshold {
		return
	}
	d.skipUntilNanos.Store(nowNanos + int64(breakerBackoff(n, cfg)))
}

// breakerBackoff doubles the backoff per consecutive failure beyond the
// threshold, capped at 10 doublings and BreakerMax.
func breakerBackoff(consecFail int32, cfg Config) time.Duration {
	shift := min(int(consecFail)-cfg.BreakerThreshold, 10)
	return min(cfg.BreakerBase<<shift, cfg.BreakerMax)
}
