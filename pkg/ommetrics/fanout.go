package ommetrics

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// fanout exports the shared aggregation state to every destination in parallel, bounded by each OM's semaphore.
type fanout struct {
	reg *registry
	cfg Config
}

func newFanout(reg *registry, cfg Config) *fanout {
	return &fanout{reg: reg, cfg: cfg}
}

func (f *fanout) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	// Cumulative: with delta and no per-destination buffer, a destination that misses a cycle loses that delta for good.
	return metricdata.CumulativeTemporality
}

func (f *fanout) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (f *fanout) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	dests := f.reg.snapshot()
	nowNanos := f.reg.now()
	var wg sync.WaitGroup
	errs := make([]error, len(dests))
	for i, d := range dests {
		if d.skipExport(nowNanos) {
			continue
		}
		wg.Add(1)
		go func(i int, d *destination) {
			defer wg.Done()
			select {
			case d.om.sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = &destinationError{dest: d.key, err: ctx.Err()}
				return
			}
			defer func() { <-d.om.sem }()

			localResource := rm.Resource
			if destinationResource := d.resource.Load(); destinationResource != nil {
				var err error
				localResource, err = resource.Merge(rm.Resource, destinationResource)
				if err != nil {
					errs[i] = &destinationError{dest: d.key, err: err}
					return
				}
			}
			local := metricdata.ResourceMetrics{Resource: localResource, ScopeMetrics: rm.ScopeMetrics}
			cctx, cancel := context.WithTimeout(withDestination(ctx, d), f.cfg.PerDestTimeout)
			defer cancel()
			err := d.exp.Export(cctx, &local)
			// A cancelled reader budget is not the destination's fault; keep it out of the breaker.
			if ctx.Err() == nil {
				d.recordResult(err, f.reg.now(), f.cfg)
			}
			if err != nil {
				errs[i] = &destinationError{dest: d.key, err: err}
			}
		}(i, d)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (f *fanout) ForceFlush(ctx context.Context) error {
	return f.each(func(d *destination) error { return d.exp.ForceFlush(ctx) })
}

func (f *fanout) Shutdown(ctx context.Context) error {
	return f.each(func(d *destination) error { return d.exp.Shutdown(ctx) })
}

func (f *fanout) each(fn func(*destination) error) error {
	dests := f.reg.snapshot()
	var wg sync.WaitGroup
	errs := make([]error, len(dests))
	for i, d := range dests {
		wg.Add(1)
		go func(i int, d *destination) {
			defer wg.Done()
			errs[i] = fn(d)
		}(i, d)
	}
	wg.Wait()
	return errors.Join(errs...)
}

type destinationError struct {
	dest destKey
	err  error
}

func (e *destinationError) Error() string {
	return "destination " + e.dest.String() + ": " + e.err.Error()
}
func (e *destinationError) Unwrap() error { return e.err }

var _ sdkmetric.Exporter = (*fanout)(nil)
