package operator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coordinationv1 "k8s.io/api/coordination/v1"

	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// gatedTransport simulates a crash for one elector: with the gate cut, every lease write fails —
// including client-go's release-on-cancel write — so the crashed leader's leases stay held and
// the survivor has to earn the takeover through expiry.
type gatedTransport struct {
	directiveTransport
	cut atomic.Bool
}

func (g *gatedTransport) WriteLease(ctx context.Context, clusterName string, lease *coordinationv1.Lease) error {
	if g.cut.Load() {
		return xerrors.New("transport cut (simulated crash)")
	}
	return g.directiveTransport.WriteLease(ctx, clusterName, lease)
}

// zeroCandidacyDelay removes the randomized candidacy delay for one deployment's election: its
// bounds are pinned by the pure-core tests, and here it would only blur the timing assertions.
func zeroCandidacyDelay(e *QuorumElector, nsName types.NamespacedName) {
	e.mu.Lock()
	entry := e.entries[nsName]
	e.mu.Unlock()
	entry.lock.mu.Lock()
	entry.lock.core.randomDelay = func() time.Duration { return 0 }
	entry.lock.mu.Unlock()
}

// TestQuorumElectorFailoverAcrossThreeControlPlanes is the M3.7 integration deliverable: three
// real API servers (one envtest control plane per cluster), compressed durations, real Leases.
// A acquires term 1 (a dirty acquire — no released majority — so its hold-off is served); a
// clean stop of A hands leadership to a successor within one LeaseDuration at term 2 (the
// release shape skips the hold-off); a crash of that successor costs the survivor expiry PLUS
// the hold-off before it leads at term 3.
func TestQuorumElectorFailoverAcrossThreeControlPlanes(t *testing.T) {
	leaseDuration := 2 * time.Second
	nsName := types.NamespacedName{Namespace: "default", Name: "failover"}
	electorate := []string{"cluster-1", "cluster-2", "cluster-3"}

	clients := map[string]client.Client{}
	for _, name := range electorate {
		// no CRDs: the election runs on built-in coordination.k8s.io Leases only
		testEnv, err := env.Start(env.WithCRDs())
		require.NoError(t, err, "control plane for %s failed to start; run `make envtest-assets` once", name)
		t.Cleanup(func() { _ = testEnv.Stop() })
		clients[name] = testEnv.Client
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	electors := map[string]*QuorumElector{}
	gates := map[string]*gatedTransport{}
	for _, name := range electorate {
		gates[name] = &gatedTransport{directiveTransport: newAPIServerTransport(clients)}
		electors[name] = newQuorumElector(nil, gates[name], name, leaseDuration)
	}

	leaderAmong := func(wantTerm int64, timeout time.Duration, among ...string) (string, time.Duration) {
		start := time.Now()
		for time.Since(start) < timeout {
			for _, name := range among {
				if term, isLeader := electors[name].Current(nsName); isLeader {
					require.Equal(t, wantTerm, term)
					return name, time.Since(start)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		require.FailNow(t, "no leader emerged", "wanted term %d among %v within %s", wantTerm, among, timeout)
		return "", 0
	}

	// A contends alone and acquires term 1; the world had no released majority, so the acquire
	// is dirty and Current stays false until the one-duration takeover hold-off is served
	electors["cluster-1"].upsertDeployment(ctx, nsName, electorate)
	zeroCandidacyDelay(electors["cluster-1"], nsName)
	_, elapsed := leaderAmong(1, 15*time.Second, "cluster-1")
	assert.Greater(t, elapsed, leaseDuration, "a dirty acquire serves the hold-off before the first guarded write")

	// B and C join as followers under A's live majority
	for _, name := range electorate[1:] {
		electors[name].upsertDeployment(ctx, nsName, electorate)
		zeroCandidacyDelay(electors[name], nsName)
	}

	// clean stop: retiring the entry cancels its context and ReleaseOnCancel writes the release
	// shape before returning — the successor recognizes it and skips the hold-off
	electors["cluster-1"].removeDeployment(nsName)
	winner, elapsed := leaderAmong(2, 15*time.Second, "cluster-2", "cluster-3")
	assert.Less(t, elapsed, leaseDuration, "a clean release hands over within one LeaseDuration, no hold-off")

	// dirty stop: cut the winner's transport BEFORE retiring it, so the release write fails and
	// its leases stay held — the survivor pays content expiry plus the takeover hold-off
	gates[winner].cut.Store(true)
	electors[winner].removeDeployment(nsName)
	survivor := "cluster-2"
	if winner == "cluster-2" {
		survivor = "cluster-3"
	}
	_, elapsed = leaderAmong(3, 20*time.Second, survivor)
	assert.Greater(t, elapsed, 2*leaseDuration-leaseDuration/2, "a crashed leader costs expiry plus the served hold-off")

	// the terms the failovers minted are durable in the lease annotations on every cluster
	for _, name := range electorate {
		lease := &coordinationv1.Lease{}
		require.NoError(t, clients[name].Get(ctx, nsName, lease))
		term, ok := leaseTerm(lease)
		require.True(t, ok)
		assert.Equal(t, int64(3), term)
	}
}
