package ommetrics

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type fakeDeployment struct{ uid, name string }

func (d fakeDeployment) TelemetryIdentifier() string      { return d.uid }
func (d fakeDeployment) AutomationDeploymentName() string { return d.name }

var testDeployment = fakeDeployment{uid: "uid-1", name: "rs"}

// ptrDeployment's promoted methods dereference the receiver, like *mdb.MongoDB's.
type ptrDeployment struct{ fakeDeployment }

func mustEnsure(t *testing.T, reg *registry, p Project, agentAPIKey string, tlsOpts TLSOptions) {
	t.Helper()
	if err := reg.ensure(p, agentAPIKey, tlsOpts, testDeployment); err != nil {
		t.Fatal(err)
	}
}

func mustKey(t *testing.T, p Project) destKey {
	t.Helper()
	k, err := p.key()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func destinationMappings(t *testing.T, reg *registry) string {
	t.Helper()
	dests := reg.snapshot()
	if len(dests) != 1 {
		t.Fatalf("snapshot has %d destinations, want 1", len(dests))
	}
	v, ok := dests[0].resource.Load().Set().Value(deploymentMappingsAttributeKey)
	if !ok {
		t.Fatalf("resource has no %s attribute", deploymentMappingsAttributeKey)
	}
	return v.AsString()
}

// globalScopeMetrics returns a minimal operator-global payload.
func globalScopeMetrics() []metricdata.ScopeMetrics {
	return []metricdata.ScopeMetrics{{
		Scope: instrumentation.Scope{Name: "test"},
		Metrics: []metricdata.Metrics{{
			Name: "mck.operator.uptime",
			Data: metricdata.Gauge[int64]{
				DataPoints: []metricdata.DataPoint[int64]{
					{Attributes: attribute.NewSet(attribute.String("service.name", "op")), Value: 1},
				},
			},
		}},
	}}
}

// ensure is idempotent: repeated calls (one per reconcile) must not change
// membership, and release removes unconditionally.
func TestEnsureIdempotentAndRelease(t *testing.T) {
	reg := newRegistry(NewConfig())
	for i := 0; i < 5; i++ {
		mustEnsure(t, reg, Project{BaseURL: "https://om.example.com", GroupID: "g1"}, "k", TLSOptions{})
	}
	if n := len(reg.snapshot()); n != 1 {
		t.Fatalf("snapshot has %d destinations, want 1 (canonicalization)", n)
	}
	if err := reg.release(Project{BaseURL: "https://om.example.com/", GroupID: "g1"}); err != nil {
		t.Fatal(err)
	}
	if n := len(reg.snapshot()); n != 0 {
		t.Fatalf("after release, snapshot has %d destinations, want 0", n)
	}
	if err := reg.release(Project{BaseURL: "https://om.example.com", GroupID: "g1"}); err != nil {
		t.Fatal(err)
	}
}

func TestPruneStale(t *testing.T) {
	reg := newRegistry(NewConfig())
	p := Project{BaseURL: "https://om.example.com", GroupID: "a"}
	mustEnsure(t, reg, p, "k", TLSOptions{})
	reg.now = func() int64 { return int64(time.Minute) }
	mustEnsure(t, reg, p, "k", TLSOptions{})
	if removed := reg.pruneStale(30 * time.Second); removed != 0 {
		t.Fatalf("pruneStale removed refreshed destination: %d", removed)
	}
	reg.now = func() int64 { return int64(2 * time.Minute) }
	if removed := reg.pruneStale(30 * time.Second); removed != 1 {
		t.Fatalf("pruneStale removed %d destinations, want 1", removed)
	}
}

func TestEnsureRefreshesKeyAndMapping(t *testing.T) {
	reg := newRegistry(NewConfig())
	p := Project{BaseURL: "https://om.example.com", GroupID: "g1"}
	mustEnsure(t, reg, p, "k1", TLSOptions{})
	renamed := fakeDeployment{uid: "uid-1", name: "rs-renamed"}
	if err := reg.ensure(p, "k2", TLSOptions{}, renamed); err != nil {
		t.Fatal(err)
	}
	if got := *reg.dests[mustKey(t, p)].agentAPIKey.Load(); got != "k2" {
		t.Fatalf("agent API key after rotation = %q, want k2", got)
	}
	if m := destinationMappings(t, reg); m != `[{"mckDeploymentUid":"uid-1","deploymentName":"rs-renamed"}]` {
		t.Fatalf("mappings after refresh = %s", m)
	}
}

func TestEnsureEncodesDeploymentMapping(t *testing.T) {
	reg := newRegistry(NewConfig())
	mustEnsure(t, reg, Project{BaseURL: "https://om.example.com", GroupID: "g1"}, "k", TLSOptions{})
	if m := destinationMappings(t, reg); m != `[{"mckDeploymentUid":"uid-1","deploymentName":"rs"}]` {
		t.Fatalf("mappings = %s", m)
	}
}

func TestEnsureRejectsIncompleteDeployment(t *testing.T) {
	for name, d := range map[string]Deployment{
		"nil":        nil,
		"typed nil":  (*ptrDeployment)(nil),
		"blank uid":  fakeDeployment{name: "rs"},
		"blank name": fakeDeployment{uid: "uid-1"},
	} {
		t.Run(name, func(t *testing.T) {
			reg := newRegistry(NewConfig())
			if err := reg.ensure(Project{BaseURL: "https://om.example.com", GroupID: "g1"}, "k", TLSOptions{}, d); err == nil {
				t.Fatal("ensure accepted an incomplete deployment")
			}
			if n := len(reg.snapshot()); n != 0 {
				t.Fatalf("snapshot has %d destinations after a rejected ensure, want 0", n)
			}
		})
	}
}

// ensure runs on every reconcile, so unchanged TLS settings must not churn the
// transport (and its connection pool).
func TestEnsureKeepsTransportWhenTLSUnchanged(t *testing.T) {
	reg := newRegistry(NewConfig())
	p := Project{BaseURL: "https://om.example.com", GroupID: "g1"}
	opts := TLSOptions{AllowInvalidSSLCertificate: true}
	mustEnsure(t, reg, p, "k", opts)
	om := reg.oms[mustKey(t, p).base]
	before := om.transport.Load()

	mustEnsure(t, reg, p, "k", opts)
	if om.transport.Load() != before {
		t.Fatal("transport replaced although TLS settings did not change")
	}

	mustEnsure(t, reg, p, "k", TLSOptions{})
	if om.transport.Load() == before {
		t.Fatal("transport not replaced after TLS settings changed")
	}
}

func TestEnsureRejectsInvalidCA(t *testing.T) {
	reg := newRegistry(NewConfig())
	err := reg.ensure(Project{BaseURL: "https://om.example.com", GroupID: "g1"}, "k", TLSOptions{CACertificate: "not-a-pem"}, testDeployment)
	if err == nil {
		t.Fatal("ensure accepted an invalid CA certificate")
	}
	if n := len(reg.snapshot()); n != 0 {
		t.Fatalf("snapshot has %d destinations after a rejected ensure, want 0", n)
	}
	if n := len(reg.oms); n != 0 {
		t.Fatalf("registry has %d OM instances after a rejected ensure, want 0", n)
	}
}

// A bad CA on a registered project must keep the old transport but still refresh the key and liveness.
func TestEnsureInvalidCAOnExistingProjectStillRefreshes(t *testing.T) {
	reg := newRegistry(NewConfig())
	p := Project{BaseURL: "https://om.example.com", GroupID: "g1"}
	mustEnsure(t, reg, p, "k1", TLSOptions{})
	d := reg.dests[mustKey(t, p)]
	before := d.om.transport.Load()

	reg.now = func() int64 { return int64(time.Minute) }
	if err := reg.ensure(p, "k2", TLSOptions{CACertificate: "not-a-pem"}, testDeployment); err == nil {
		t.Fatal("ensure accepted an invalid CA certificate")
	}
	if got := *d.agentAPIKey.Load(); got != "k2" {
		t.Fatalf("agent API key = %q, want k2", got)
	}
	if got := d.lastSeenNanos.Load(); got != int64(time.Minute) {
		t.Fatalf("lastSeenNanos = %d, want %d", got, int64(time.Minute))
	}
	if d.om.transport.Load() != before || d.om.tlsOpts != (TLSOptions{}) {
		t.Fatal("invalid CA replaced the OM's TLS settings")
	}
}

// Projects on one OM share its instance.
func TestProjectsOnOneOMShareInstance(t *testing.T) {
	reg := newRegistry(NewConfig())
	mustEnsure(t, reg, Project{BaseURL: "https://om.example.com", GroupID: "a"}, "k", TLSOptions{})
	mustEnsure(t, reg, Project{BaseURL: "https://om.example.com:443/", GroupID: "b"}, "k", TLSOptions{})
	dests := reg.snapshot()
	if len(dests) != 2 || dests[0].om != dests[1].om || len(reg.oms) != 1 {
		t.Fatalf("projects on one OM do not share an instance: %d dests, %d OMs", len(dests), len(reg.oms))
	}
}

// release drops only the destination for the given project.
func TestReleaseOnlyTargetProject(t *testing.T) {
	reg := newRegistry(NewConfig())
	for _, g := range []string{"a", "b"} {
		mustEnsure(t, reg, Project{BaseURL: "https://om.example.com", GroupID: g}, "k", TLSOptions{})
	}
	if err := reg.release(Project{BaseURL: "https://om.example.com", GroupID: "a"}); err != nil {
		t.Fatal(err)
	}
	remaining := reg.snapshot()
	if len(remaining) != 1 || remaining[0].key.groupID != "b" {
		t.Fatalf("remaining destinations = %v, want only group b", remaining)
	}
}

func TestReleaseEvictsOMWithLastProject(t *testing.T) {
	const base = "https://om.example.com"
	reg := newRegistry(NewConfig())
	for _, g := range []string{"a", "b"} {
		mustEnsure(t, reg, Project{BaseURL: base, GroupID: g}, "k", TLSOptions{})
	}
	cb := mustKey(t, Project{BaseURL: base, GroupID: "a"}).base

	if err := reg.release(Project{BaseURL: base, GroupID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.oms[cb]; !ok {
		t.Fatal("OM instance evicted while project b still uses it")
	}
	if err := reg.release(Project{BaseURL: base, GroupID: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.oms[cb]; ok {
		t.Fatal("OM instance kept after its last project was released")
	}
}

func TestPruneStaleEvictsUnusedOMs(t *testing.T) {
	reg := newRegistry(NewConfig())
	stale := Project{BaseURL: "https://stale.example.com", GroupID: "a"}
	live := Project{BaseURL: "https://live.example.com", GroupID: "b"}
	mustEnsure(t, reg, stale, "k", TLSOptions{})
	reg.now = func() int64 { return int64(time.Minute) }
	mustEnsure(t, reg, live, "k", TLSOptions{})
	if removed := reg.pruneStale(30 * time.Second); removed != 1 {
		t.Fatalf("pruneStale removed %d destinations, want 1", removed)
	}
	if _, ok := reg.oms[mustKey(t, stale).base]; ok {
		t.Fatal("OM instance for pruned project kept")
	}
	if _, ok := reg.oms[mustKey(t, live).base]; !ok {
		t.Fatal("OM instance for live project evicted")
	}
}

// An OM that returns after eviction must start from the TLS settings passed now, not the evicted ones.
func TestEnsureAfterEvictionUsesCurrentTLS(t *testing.T) {
	reg := newRegistry(NewConfig())
	p := Project{BaseURL: "https://om.example.com", GroupID: "g1"}
	mustEnsure(t, reg, p, "k", TLSOptions{AllowInvalidSSLCertificate: true})
	if err := reg.release(p); err != nil {
		t.Fatal(err)
	}
	mustEnsure(t, reg, p, "k", TLSOptions{})
	if got := reg.oms[mustKey(t, p).base].tlsOpts; got != (TLSOptions{}) {
		t.Fatalf("tlsOpts after re-registration = %+v, want zero value", got)
	}
}
