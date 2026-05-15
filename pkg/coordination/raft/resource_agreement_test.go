package coordraft

import (
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// F12a — ResourceObserved + WaitForResourcesAgreed.
//
// These tests cover both layers:
//   - applyResourceObserved on the FSM directly (idempotency, stale-rules).
//   - coordinator.ReportResource + WaitForResourcesAgreed over a real 3-node
//     raft cluster, exercising the same correctness gate the operator will.

// crA is reused from fsm_test.go.

func TestApplyResourceObserved_RecordsAndSupersedes(t *testing.T) {
	f := NewFSM()
	ref := ResourceRef{Kind: "ConfigMap", Namespace: "ns1", Name: "project-cm"}
	t0 := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// First report.
	applyHelper(t, f, ProposalResourceObserved, ResourceObservedPayload{
		CRKey: crA, Ref: ref, ContentHash: "abc", ObservedBy: "cluster-a", ObservedAt: t0,
	})
	cr := f.GetPerCR(crA)
	require.NotNil(t, cr.Resources)
	require.Contains(t, cr.Resources, ref.String())
	require.Contains(t, cr.Resources[ref.String()], "cluster-a")
	assert.Equal(t, "abc", cr.Resources[ref.String()]["cluster-a"].ContentHash)

	// Newer report supersedes.
	t1 := t0.Add(1 * time.Minute)
	applyHelper(t, f, ProposalResourceObserved, ResourceObservedPayload{
		CRKey: crA, Ref: ref, ContentHash: "def", ObservedBy: "cluster-a", ObservedAt: t1,
	})
	assert.Equal(t, "def", f.GetPerCR(crA).Resources[ref.String()]["cluster-a"].ContentHash)

	// Stale report does NOT supersede.
	applyHelper(t, f, ProposalResourceObserved, ResourceObservedPayload{
		CRKey: crA, Ref: ref, ContentHash: "xxx", ObservedBy: "cluster-a", ObservedAt: t0,
	})
	assert.Equal(t, "def", f.GetPerCR(crA).Resources[ref.String()]["cluster-a"].ContentHash)
}

// TestCoordinator_WaitForResourcesAgreed_AllAgree builds a 3-node cluster,
// reports identical hashes for two refs from every operator, and asserts that
// WaitForResourcesAgreed returns ResourcesAgreed on every coordinator.
func TestCoordinator_WaitForResourcesAgreed_AllAgree(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	_ = leaderIdx
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	refs := []coordination.ResourceRef{
		{Kind: "ConfigMap", Namespace: "ns", Name: "project-cm"},
		{Kind: "Secret", Namespace: "ns", Name: "creds"},
	}
	hashes := map[string]string{
		refs[0].String(): "abc-cm-hash",
		refs[1].String(): "def-secret-hash",
	}

	for _, co := range coords {
		for _, ref := range refs {
			require.NoError(t, co.ReportResource(crKey, ref, hashes[ref.String()]))
		}
	}

	// Allow the FSMs on followers to converge.
	require.Eventually(t, func() bool {
		for _, co := range coords {
			ag, _ := co.WaitForResourcesAgreed(crKey, refs)
			if ag != coordination.ResourcesAgreed {
				return false
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond)

	// Cross-check on every coordinator: returns OK with empty diagnostic.
	for i, co := range coords {
		ag, diag := co.WaitForResourcesAgreed(crKey, refs)
		assert.Equal(t, coordination.ResourcesAgreed, ag, "coord %d (%s)", i, string(nodes[i].ID))
		assert.Empty(t, diag, "coord %d", i)
	}
}

// TestCoordinator_WaitForResourcesAgreed_Disagreement: one operator reports a
// different hash. Expect ResourcesPending with diagnostic naming the
// offending cluster + ref.
func TestCoordinator_WaitForResourcesAgreed_Disagreement(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	_ = leaderIdx
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	ref := coordination.ResourceRef{Kind: "ConfigMap", Namespace: "ns", Name: "project-cm"}

	// Two clusters report hash "good", one reports "bad".
	require.NoError(t, coords[0].ReportResource(crKey, ref, "good"))
	require.NoError(t, coords[1].ReportResource(crKey, ref, "bad"))
	require.NoError(t, coords[2].ReportResource(crKey, ref, "good"))

	// Wait for everyone to learn.
	require.Eventually(t, func() bool {
		_, diag := coords[0].WaitForResourcesAgreed(crKey, []coordination.ResourceRef{ref})
		return strings.Contains(diag, "hash mismatch")
	}, 5*time.Second, 50*time.Millisecond)

	ag, diag := coords[0].WaitForResourcesAgreed(crKey, []coordination.ResourceRef{ref})
	assert.Equal(t, coordination.ResourcesPending, ag)
	assert.Contains(t, diag, "hash mismatch", "diagnostic: %s", diag)
	assert.Contains(t, diag, "ConfigMap/ns/project-cm")
	// The offender (coords[1] -> node-1) must be flagged as out of sync.
	expectedOffender := string(nodes[1].ID)
	assert.Contains(t, diag, "out of sync", "diagnostic: %s", diag)
	assert.Contains(t, diag, expectedOffender, "diagnostic must name the offending cluster: %s", diag)
}

// TestCoordinator_WaitForResourcesAgreed_MissingObservation: one operator has
// not yet reported. Expect ResourcesPending with "awaiting observation".
func TestCoordinator_WaitForResourcesAgreed_MissingObservation(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	_ = leaderIdx
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	ref := coordination.ResourceRef{Kind: "ConfigMap", Namespace: "ns", Name: "project-cm"}

	// Only two of three clusters have reported. The third is by definition
	// "known" because every coordinator counts its own cluster name as known.
	require.NoError(t, coords[0].ReportResource(crKey, ref, "good"))
	require.NoError(t, coords[1].ReportResource(crKey, ref, "good"))

	// Wait for the two reports to land on coord 2.
	require.Eventually(t, func() bool {
		cr := coords[2].fsm.GetPerCR(toRaftCRKey(crKey))
		byCluster := cr.Resources[ref.String()]
		return len(byCluster) >= 2
	}, 5*time.Second, 50*time.Millisecond)

	// Coord 2 hasn't reported yet → its known set includes itself but its
	// observation is missing.
	ag, diag := coords[2].WaitForResourcesAgreed(crKey, []coordination.ResourceRef{ref})
	assert.Equal(t, coordination.ResourcesPending, ag)
	assert.Contains(t, diag, "awaiting observation", "diagnostic: %s", diag)
	assert.Contains(t, diag, string(nodes[2].ID), "diagnostic must name absentee: %s", diag)

	// Once coord 2 reports, agreement holds.
	require.NoError(t, coords[2].ReportResource(crKey, ref, "good"))
	require.Eventually(t, func() bool {
		ag, _ := coords[2].WaitForResourcesAgreed(crKey, []coordination.ResourceRef{ref})
		return ag == coordination.ResourcesAgreed
	}, 5*time.Second, 50*time.Millisecond)
}

// TestCoordinator_ReportResource_FreshSupersedesStale: an operator's local
// resource changes, it re-reports, and WaitForResourcesAgreed reflects the
// new state. This exercises the supersedes-on-equal-timestamp path: real
// callers won't manage timestamps directly, the coordinator does.
func TestCoordinator_ReportResource_FreshSupersedesStale(t *testing.T) {
	_, coords, _ := newCoordinatorClusterForTest(t, 3)
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	ref := coordination.ResourceRef{Kind: "ConfigMap", Namespace: "ns", Name: "project-cm"}

	// All report "v1".
	for _, co := range coords {
		require.NoError(t, co.ReportResource(crKey, ref, "v1"))
	}
	require.Eventually(t, func() bool {
		ag, _ := coords[0].WaitForResourcesAgreed(crKey, []coordination.ResourceRef{ref})
		return ag == coordination.ResourcesAgreed
	}, 5*time.Second, 50*time.Millisecond)

	// Cluster 0 picks up a drifted local copy: re-reports "v2".
	require.NoError(t, coords[0].ReportResource(crKey, ref, "v2"))

	// Disagreement appears.
	require.Eventually(t, func() bool {
		ag, _ := coords[0].WaitForResourcesAgreed(crKey, []coordination.ResourceRef{ref})
		return ag == coordination.ResourcesPending
	}, 5*time.Second, 50*time.Millisecond)

	// User fixes cluster 0; it re-reports "v1" again.
	require.NoError(t, coords[0].ReportResource(crKey, ref, "v1"))
	require.Eventually(t, func() bool {
		ag, _ := coords[0].WaitForResourcesAgreed(crKey, []coordination.ResourceRef{ref})
		return ag == coordination.ResourcesAgreed
	}, 5*time.Second, 50*time.Millisecond)
}

// _silence is a no-op reference to avoid unused-import warnings in case the
// raft package above is dropped during refactors.
var _ = raft.ServerID("")
