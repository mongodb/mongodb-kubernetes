package ommetrics

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func exportOnce(reg *registry) error {
	rm := metricdata.ResourceMetrics{ScopeMetrics: globalScopeMetrics()}
	return newFanout(reg, reg.cfg).Export(context.Background(), &rm)
}

// Exports must hit {base}/agents/api/otlp/{groupId}/v1/metrics with Basic auth
// username = group ID and password = agent API key.
func TestExportURLAndBasicAuth(t *testing.T) {
	var gotPath, gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := newRegistry(NewConfig())
	mustEnsure(t, reg, Project{BaseURL: srv.URL, GroupID: "g1"}, "sekrit", TLSOptions{})
	if err := exportOnce(reg); err != nil {
		t.Fatalf("export: %v", err)
	}
	if want := "/agents/api/otlp/g1/v1/metrics"; gotPath != want {
		t.Fatalf("export hit %q, want %q", gotPath, want)
	}
	if gotUser != "g1" || gotPass != "sekrit" {
		t.Fatalf("basic auth = %q/%q, want g1/sekrit", gotUser, gotPass)
	}
}

// TLS settings rotate like agent API keys: a later ensure with a new CA takes
// effect on the next export without re-registering the destination.
func TestTLSRotation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))

	reg := newRegistry(NewConfig())
	p := Project{BaseURL: srv.URL, GroupID: "g1"}
	mustEnsure(t, reg, p, "k", TLSOptions{})
	if err := exportOnce(reg); err == nil {
		t.Fatal("export to a self-signed server succeeded with system roots")
	}
	mustEnsure(t, reg, p, "k", TLSOptions{CACertificate: caPEM})
	if err := exportOnce(reg); err != nil {
		t.Fatalf("export after CA rotation: %v", err)
	}
}

// The agent API key must never follow a redirect to another host.
func TestRedirectNotFollowed(t *testing.T) {
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	reg := newRegistry(NewConfig())
	mustEnsure(t, reg, Project{BaseURL: origin.URL, GroupID: "g1"}, "k", TLSOptions{})
	if err := exportOnce(reg); err == nil {
		t.Fatal("export succeeded through a redirect, want error")
	}
	if targetHit.Load() {
		t.Fatal("redirect was followed; credentials may have leaked")
	}
}

func TestOMInstanceRoundTrip(t *testing.T) {
	var gotUser, gotPass string
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		gotUser, gotPass, _ = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	om, err := newOMInstance(NewConfig(), srv.URL, TLSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := &destination{key: destKey{base: srv.URL, groupID: "g1"}, om: om}
	key := "k"
	d.agentAPIKey.Store(&key)

	t.Run("does not mutate the caller's request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, srv.URL+"/x", nil).WithContext(withDestination(context.Background(), d))
		req.RequestURI = ""
		resp, err := om.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if req.Header.Get("Authorization") != "" {
			t.Fatal("RoundTrip mutated the caller's request headers")
		}
		if gotUser != "g1" || gotPass != "k" {
			t.Fatalf("forwarded basic auth = %q/%q, want g1/k", gotUser, gotPass)
		}
	})

	t.Run("rejects requests without destination context", func(t *testing.T) {
		hit.Store(false)
		req := httptest.NewRequest(http.MethodPost, srv.URL+"/x", nil)
		req.RequestURI = ""
		if _, err := om.RoundTrip(req); err == nil {
			t.Fatal("RoundTrip accepted a request without destination context")
		}
		if hit.Load() {
			t.Fatal("request without destination context was sent")
		}
	})
}
