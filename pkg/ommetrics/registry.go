package ommetrics

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel/sdk/resource"

	otlpmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
)

type registry struct {
	mu     sync.RWMutex
	dests  map[destKey]*destination
	oms    map[string]*omInstance // by canonical base; evicted with its last destination
	closed bool
	cfg    Config
	now    func() int64
}

var errClosed = errors.New("ommetrics: multiplexer is shut down")

func newRegistry(cfg Config) *registry {
	return &registry{dests: map[destKey]*destination{}, oms: map[string]*omInstance{}, cfg: cfg, now: monoNanos}
}

func (r *registry) ensure(p Project, agentAPIKey string, tlsOpts TLSOptions, deployment Deployment) error {
	mappings, err := deploymentMappingsAttribute(deployment)
	if err != nil {
		return err
	}
	key, err := p.key()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	// TODO: MCK supports one deployment per project for now, so this replaces the whole mappings resource.
	// Supporting multiple deployments per project requires merging entries into one mappings array here.
	res := resource.NewSchemaless(mappings)
	if d, ok := r.dests[key]; ok {
		// Refresh before applying TLS so a bad CA neither blocks key rotation nor gets a live project pruned.
		d.agentAPIKey.Store(&agentAPIKey)
		d.resource.Store(res)
		d.lastSeenNanos.Store(r.now())
		return d.om.setTLS(r.cfg, tlsOpts)
	}
	om, err := r.omForLocked(key.base, tlsOpts)
	if err != nil {
		return err
	}
	exp, err := otlpmetrichttp.New(context.Background(),
		// Not WithEndpoint: it drops the path, and with it the group ID.
		otlpmetrichttp.WithEndpointURL(key.metricsURL()),
		otlpmetrichttp.WithHTTPClient(om.client),
		otlpmetrichttp.WithTimeout(r.cfg.PerDestTimeout),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{
			Enabled:         true,
			InitialInterval: 500 * time.Millisecond,
			MaxInterval:     2 * time.Second,
			MaxElapsedTime:  r.cfg.PerDestTimeout,
		}),
	)
	if err != nil {
		r.evictUnusedOMLocked(key.base)
		return err
	}
	d := &destination{key: key, om: om, exp: exp}
	d.agentAPIKey.Store(&agentAPIKey)
	d.resource.Store(res)
	d.lastSeenNanos.Store(r.now())
	r.dests[key] = d
	return nil
}

// omForLocked returns base's omInstance, creating it or applying changed TLS settings. Caller holds r.mu.
func (r *registry) omForLocked(base string, tlsOpts TLSOptions) (*omInstance, error) {
	if om, ok := r.oms[base]; ok {
		return om, om.setTLS(r.cfg, tlsOpts)
	}
	om, err := newOMInstance(r.cfg, base, tlsOpts)
	if err != nil {
		return nil, err
	}
	r.oms[base] = om
	return om, nil
}

// TODO: MCK supports one deployment per project for now, so releasing a deployment releases its project.
// Supporting multiple deployments per project requires removing only that deployment's mapping and
// dropping the destination once no mappings remain.
func (r *registry) release(p Project) error {
	key, err := p.key()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.dests[key]; !ok {
		return nil
	}
	// Skip exp.Shutdown: an in-flight Export may still hold the destination from a snapshot.
	delete(r.dests, key)
	r.evictUnusedOMLocked(key.base)
	return nil
}

// pruneStale removes destinations not refreshed within maxAge, catching deletes the controllers missed.
func (r *registry) pruneStale(maxAge time.Duration) int {
	cutoff := r.now() - int64(maxAge)
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	bases := map[string]struct{}{}
	for key, d := range r.dests {
		if d.lastSeenNanos.Load() < cutoff {
			delete(r.dests, key)
			bases[key.base] = struct{}{}
			removed++
		}
	}
	for base := range bases {
		r.evictUnusedOMLocked(base)
	}
	return removed
}

// evictUnusedOMLocked drops base's omInstance once no destination uses it. Caller holds r.mu.
func (r *registry) evictUnusedOMLocked(base string) {
	om, ok := r.oms[base]
	if !ok {
		return
	}
	for key := range r.dests {
		if key.base == base {
			return
		}
	}
	delete(r.oms, base)
	om.close(r.cfg)
}

func (r *registry) shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	clear(r.dests)
	for base, om := range r.oms {
		delete(r.oms, base)
		om.close(r.cfg)
	}
}

// snapshot returns the live destinations; callers may only touch their atomics.
func (r *registry) snapshot() []*destination {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*destination, 0, len(r.dests))
	for _, d := range r.dests {
		out = append(out, d)
	}
	return out
}
