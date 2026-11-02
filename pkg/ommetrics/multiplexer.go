// Package ommetrics multiplexes the operator's OpenTelemetry metrics to the OTLP
// ingestion endpoint of every Ops Manager project the operator manages.
package ommetrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/sdk/resource"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Multiplexer exports the operator's metrics to every registered Ops Manager project through a single MeterProvider.
type Multiplexer struct {
	mp  *sdkmetric.MeterProvider
	reg *registry
}

func New(cfg Config) (*Multiplexer, error) {
	if err := cfg.validateTimeoutBudget(); err != nil {
		return nil, err
	}
	reg := newRegistry(cfg)
	reader := sdkmetric.NewPeriodicReader(
		newFanout(reg, cfg),
		sdkmetric.WithInterval(cfg.CollectInterval),
		sdkmetric.WithTimeout(cfg.ReaderTimeout),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource.NewSchemaless(cfg.resourceAttributes()...)),
		sdkmetric.WithReader(reader),
	)

	// The reader only exports when there is at least one data point; for now
	// only the resource attributes are significant.
	up, err := mp.Meter("mongodb.kubernetes.operator").Int64Gauge("mongodb.kubernetes.operator.up")
	if err != nil {
		return nil, err
	}
	up.Record(context.Background(), 1)

	return &Multiplexer{mp: mp, reg: reg}, nil
}

// EnsureProject registers a project as an export destination, or refreshes its agent API key,
// deployment mapping and its OM's TLS settings. All projects on one OM must share TLS settings.
func (f *Multiplexer) EnsureProject(p Project, agentAPIKey string, tlsOpts TLSOptions, deployment Deployment) error {
	return f.reg.ensure(p, agentAPIKey, tlsOpts, deployment)
}

// ReleaseProject removes a project's destination; a no-op when absent.
func (f *Multiplexer) ReleaseProject(p Project) error {
	return f.reg.release(p)
}

// PruneStale removes destinations that no EnsureProject call has refreshed
// within maxAge and returns how many it removed.
func (f *Multiplexer) PruneStale(maxAge time.Duration) int {
	return f.reg.pruneStale(maxAge)
}

// Shutdown runs a final export; later EnsureProject calls fail.
func (f *Multiplexer) Shutdown(ctx context.Context) error {
	err := f.mp.Shutdown(ctx)
	f.reg.shutdown()
	return err
}
