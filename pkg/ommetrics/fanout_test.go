package ommetrics

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/protobuf/proto"

	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
)

func TestFanoutExportsDestinationMappings(t *testing.T) {
	requests := make(chan map[string]string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload collectormetrics.ExportMetricsServiceRequest
		if err := proto.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		attributes := map[string]string{"path": request.URL.Path}
		for _, metrics := range payload.GetResourceMetrics() {
			for _, attr := range metrics.GetResource().GetAttributes() {
				attributes[attr.Key] = attr.Value.GetStringValue()
			}
		}
		requests <- attributes
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := NewConfig()
	reg := newRegistry(cfg)
	want := map[string]string{}
	for _, topology := range []v1.DeploymentTopology{v1.DeploymentTopologySingleCluster, v1.DeploymentTopologyMultiCluster} {
		groupID := string(topology)
		deployment := fakeDeployment{uid: "uid-" + groupID, name: "rs-" + groupID, topology: topology}
		if err := reg.ensure(Project{BaseURL: srv.URL, GroupID: groupID}, "k", TLSOptions{}, deployment); err != nil {
			t.Fatal(err)
		}
		want["/agents/api/otlp/"+groupID+"/v1/metrics"] = `[{"mckDeploymentUid":"uid-` + groupID + `","deploymentName":"rs-` + groupID + `","topology":"` + groupID + `"}]`
	}
	rm := metricdata.ResourceMetrics{
		Resource:     resource.NewSchemaless(attribute.String("service.name", "operator")),
		ScopeMetrics: globalScopeMetrics(),
	}
	if err := newFanout(reg, cfg).Export(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("received %d requests, want 2", len(requests))
	}
	close(requests)
	got := map[string]string{}
	for attributes := range requests {
		if attributes["service.name"] != "operator" {
			t.Errorf("shared service.name = %q", attributes["service.name"])
		}
		got[attributes["path"]] = attributes[deploymentMappingsAttributeKey]
	}
	if !maps.Equal(got, want) {
		t.Fatalf("mappings by export path = %v, want %v", got, want)
	}
	if _, ok := rm.Resource.Set().Value(deploymentMappingsAttributeKey); ok {
		t.Fatal("destination mappings leaked into shared resource")
	}
}

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
