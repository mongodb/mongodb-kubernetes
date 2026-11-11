package ommetrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// A cancelled reader budget is not the destination's fault and must not trip
// its breaker.
func TestFanoutCancelledContextDoesNotTripBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := NewConfig()
	reg := newRegistry(cfg)
	mustEnsure(t, reg, Project{BaseURL: srv.URL, GroupID: "g1"}, "k", TLSOptions{})
	f := newFanout(reg, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rm := metricdata.ResourceMetrics{ScopeMetrics: globalScopeMetrics()}
	for i := 0; i < cfg.BreakerThreshold+1; i++ {
		if err := f.Export(ctx, &rm); err == nil {
			t.Fatal("export with cancelled context succeeded, want error")
		}
	}
	d := reg.snapshot()[0]
	if n := d.consecFail.Load(); n != 0 {
		t.Fatalf("consecFail = %d after cancelled exports, want 0", n)
	}
	if d.skipExport(reg.now()) {
		t.Fatal("breaker opened after cancelled exports")
	}
}

// One failing destination must not prevent exports to the others, and the
// returned error must identify it.
func TestFanoutIsolatesFailingDestination(t *testing.T) {
	var goodHit atomic.Bool
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer bad.Close()

	reg := newRegistry(NewConfig())
	mustEnsure(t, reg, Project{BaseURL: good.URL, GroupID: "good"}, "k", TLSOptions{})
	badProject := Project{BaseURL: bad.URL, GroupID: "bad"}
	mustEnsure(t, reg, badProject, "k", TLSOptions{})

	err := exportOnce(reg)
	var destErr *destinationError
	if !errors.As(err, &destErr) || destErr.dest != mustKey(t, badProject) {
		t.Fatalf("export error = %v, want destinationError for %v", err, badProject)
	}
	if !goodHit.Load() {
		t.Fatal("healthy destination was not exported to")
	}
}
