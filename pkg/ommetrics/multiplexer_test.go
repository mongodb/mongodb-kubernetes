package ommetrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShutdownDropsStateAndRefusesEnsure(t *testing.T) {
	m, err := New(NewConfig())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	p := Project{BaseURL: srv.URL, GroupID: "g1"}
	if err := m.EnsureProject(p, "k", TLSOptions{}, testDeployment); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.reg.dests) != 0 || len(m.reg.oms) != 0 {
		t.Fatalf("after Shutdown: %d destinations, %d OMs; want 0, 0", len(m.reg.dests), len(m.reg.oms))
	}
	if err := m.EnsureProject(p, "k", TLSOptions{}, testDeployment); !errors.Is(err, errClosed) {
		t.Fatalf("EnsureProject after Shutdown = %v, want errClosed", err)
	}
}
