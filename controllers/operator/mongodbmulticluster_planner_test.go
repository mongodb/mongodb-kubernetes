package operator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
)

// The planner is pure and the snapshot serializable, so every scenario — including every
// mid-rollout crash prefix — is a hand-written snapshot: restarting from any prefix of applied
// decisions IS re-planning from that world, since plan() keeps no memory between calls.

const (
	testPlanTerm     int64 = 5
	testPlanHash           = "hash-current"
	testPlanOldHash        = "hash-old"
	testPlanProject        = "abcd1234"
	testPlanDomain         = "cluster.local"
	testPlanRSName         = "temple"
	testPlanRSNs           = "my-namespace"
	directiveGenSeen int64 = 3
)

var testPlanNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

type snapshotBuilder struct {
	s plannerSnapshot
}

// newSnapshot starts from three clusters with the given target member counts, indexes allocated
// positionally, no directives, an empty (read) AC and empty (read) OM facts.
func newSnapshot(targetCounts ...int) *snapshotBuilder {
	b := &snapshotBuilder{s: plannerSnapshot{
		Now:            testPlanNow,
		LeadershipTerm: testPlanTerm,
		Name:           testPlanRSName,
		Namespace:      testPlanRSNs,
		SpecHash:       testPlanHash,
		ProjectID:      testPlanProject,
		ClusterDomain:  testPlanDomain,
		Directives:     map[string]directiveView{},
		AC:             acView{Read: true, MemberCountsByIndex: map[int]int{}},
		OMFacts:        omFactsView{Read: true, ProcessStates: map[string]processFactView{}},
	}}
	for i, count := range targetCounts {
		b.s.Targets = append(b.s.Targets, clusterTarget{ClusterName: clusters[i], Members: count})
	}
	return b
}

func standardAllocations(b *snapshotBuilder) map[string]int {
	allocations := map[string]int{}
	for i, t := range b.s.Targets {
		allocations[t.ClusterName] = i
	}
	return allocations
}

// withGrantedDirective adds a fully current directive: at the leader's term/hash/project with the
// positional allocation map, echoed by the member, all facts true.
func (b *snapshotBuilder) withGrantedDirective(clusterName string, memberCount int) *snapshotBuilder {
	idx := standardAllocations(b)[clusterName]
	b.s.Directives[clusterName] = directiveView{
		Exists:     true,
		Generation: directiveGenSeen,
		Spec: operatorv1.MongoDBDirectiveSpec{
			ClusterName:      clusterName,
			LeadershipTerm:   testPlanTerm,
			TargetSpecHash:   testPlanHash,
			MemberCount:      memberCount,
			ClusterIndex:     idx,
			IndexAllocations: standardAllocations(b),
			ProjectID:        testPlanProject,
			AdvancedAt:       metav1.NewTime(testPlanNow.Add(-time.Minute)),
		},
		Status: operatorv1.MongoDBDirectiveStatus{
			ObservedGeneration: directiveGenSeen,
			ObservedSpecHash:   testPlanHash,
			StsApplied:         true,
			AgentRegistered:    true,
			InGoalState:        true,
		},
	}
	return b
}

func (b *snapshotBuilder) editDirective(clusterName string, edit func(d *directiveView)) *snapshotBuilder {
	d := b.s.Directives[clusterName]
	edit(&d)
	b.s.Directives[clusterName] = d
	return b
}

// withAC sets the AC counts positionally by cluster index, content stamped at the current spec
// hash (a stale-content world overrides s.AC.SpecHash explicitly).
func (b *snapshotBuilder) withAC(countsByIndex ...int) *snapshotBuilder {
	b.s.AC.MemberCountsByIndex = map[int]int{}
	for i, count := range countsByIndex {
		b.s.AC.MemberCountsByIndex[i] = count
	}
	b.s.AC.SpecHash = testPlanHash
	return b
}

// withConvergedOMFacts marks every process of every cluster at the given counts (positional by
// index) as registered and in goal state.
func (b *snapshotBuilder) withConvergedOMFacts(countsByIndex ...int) *snapshotBuilder {
	b.s.OMFacts.ProcessStates = map[string]processFactView{}
	for i, count := range countsByIndex {
		for _, hostname := range dns.GetMultiClusterProcessHostnames(testPlanRSName, testPlanRSNs, i, count, testPlanDomain, nil) {
			b.s.OMFacts.ProcessStates[hostname] = processFactView{Registered: true, GoalAchieved: true}
		}
	}
	return b
}

// converged is the steady world: directives granted at the target counts, AC matching, all facts
// true — the snapshot plan() must call Noop on.
func converged(targetCounts ...int) *snapshotBuilder {
	b := newSnapshot(targetCounts...)
	for i, count := range targetCounts {
		b.withGrantedDirective(clusters[i], count)
	}
	return b.withAC(targetCounts...).withConvergedOMFacts(targetCounts...)
}

func (b *snapshotBuilder) withSpecViolations(violations ...string) *snapshotBuilder {
	b.s.SpecViolations = violations
	return b
}

func (b *snapshotBuilder) build() plannerSnapshot {
	return b.s
}

func TestPlanNoopWhenConverged(t *testing.T) {
	decision := plan(converged(2, 2, 2).build())
	assert.Equal(t, decisionNoop, decision.Kind, decision.Reason)
}

func TestPlanStaleWorldRefusal(t *testing.T) {
	t.Run("Newer term on the AC", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.AC.LeadershipTerm = testPlanTerm + 1
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
		assert.Contains(t, decision.Reason, "newer leadership term")
	})

	t.Run("Newer term on a directive", func(t *testing.T) {
		b := converged(2, 2, 2).editDirective(clusters[1], func(d *directiveView) {
			d.Spec.LeadershipTerm = testPlanTerm + 1
		})
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
		assert.Contains(t, decision.Reason, clusters[1])
	})

	t.Run("Refusal precedes even spec judgment", func(t *testing.T) {
		// scaling both ways would be InvalidSpec, but a deposed leader must not judge the spec
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		b.s.Targets[1].Members = 1
		b.s.AC.LeadershipTerm = testPlanTerm + 1
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
	})
}

func TestPlanScalingBothWaysRefused(t *testing.T) {
	b := converged(2, 2, 2)
	b.s.Targets[0].Members = 3 // up vs granted 2
	b.s.Targets[1].Members = 1 // down vs granted 2
	decision := plan(b.build())
	assert.Equal(t, decisionInvalidSpec, decision.Kind)
	assert.Contains(t, decision.Reason, "scale up and scale down")
}

func TestDecentralizedSpecViolations(t *testing.T) {
	specWithSecurity := func(security *mdbv1.Security) mdbmultiv1.MongoDBMultiSpec {
		return mdbmultiv1.MongoDBMultiSpec{DbCommonSpec: mdbv1.DbCommonSpec{Security: security}}
	}

	assert.Empty(t, decentralizedSpecViolations(mdbmultiv1.MongoDBMultiSpec{}))

	cases := []struct {
		name string
		spec mdbmultiv1.MongoDBMultiSpec
		want string
	}{
		{"TLS enabled", specWithSecurity(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}), "spec.security.tls"},
		{"certsSecretPrefix implies TLS", specWithSecurity(&mdbv1.Security{CertificatesSecretsPrefix: "prefix"}), "spec.security.tls"},
		{"authentication enabled", specWithSecurity(&mdbv1.Security{Authentication: &mdbv1.Authentication{Enabled: true}}), "spec.security.authentication"},
		{"internal cluster auth", specWithSecurity(&mdbv1.Security{Authentication: &mdbv1.Authentication{InternalCluster: "X509"}}), "internalCluster"},
		{"roles", specWithSecurity(&mdbv1.Security{Roles: []mdbv1.MongoDBRole{{Role: "root"}}}), "roles"},
		{"roleRefs", specWithSecurity(&mdbv1.Security{RoleRefs: []mdbv1.MongoDBRoleRef{{Name: "root"}}}), "roleRefs"},
		{"backup enabled", mdbmultiv1.MongoDBMultiSpec{DbCommonSpec: mdbv1.DbCommonSpec{Backup: &mdbv1.Backup{Mode: "enabled"}}}, "spec.backup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations := decentralizedSpecViolations(tc.spec)
			require.Len(t, violations, 1)
			assert.Contains(t, violations[0], tc.want)
		})
	}

	t.Run("Features legacy MC does not support either are not refused", func(t *testing.T) {
		// prometheus no-ops in legacy MC too (nil pc); a disabled backup configures nothing
		spec := mdbmultiv1.MongoDBMultiSpec{DbCommonSpec: mdbv1.DbCommonSpec{
			Prometheus: &v1.Prometheus{},
			Backup:     &mdbv1.Backup{Mode: "disabled"},
		}}
		assert.Empty(t, decentralizedSpecViolations(spec))
	})
}

func TestClusterStatusOptionReportsGrantedCounts(t *testing.T) {
	// mid-scale, the status must show what the leader instructed (granted), not the spec's target
	b := converged(2, 2, 2)
	b.s.Targets[0].Members = 4
	b.withGrantedDirective(clusters[0], 3)
	option := clusterStatusOption(b.build()).(status.MultiReplicaSetMemberOption)
	assert.Equal(t, 7, option.Members)
	assert.Equal(t, []status.ClusterStatusItem{
		{ClusterName: clusters[0], Members: 3},
		{ClusterName: clusters[1], Members: 2},
		{ClusterName: clusters[2], Members: 2},
	}, option.ClusterStatusList)
}

func TestPlanUnsupportedSpecRefused(t *testing.T) {
	t.Run("A violation refuses even a converged world", func(t *testing.T) {
		decision := plan(converged(2, 2, 2).withSpecViolations("spec.security.tls").build())
		assert.Equal(t, decisionInvalidSpec, decision.Kind)
		assert.Contains(t, decision.Reason, "not supported in the decentralized POC")
		assert.Contains(t, decision.Reason, "spec.security.tls")
	})

	t.Run("Stale world still precedes the violation", func(t *testing.T) {
		b := converged(2, 2, 2).withSpecViolations("spec.security.tls")
		b.s.AC.LeadershipTerm = testPlanTerm + 1
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
	})
}

func TestPlanRemovalGuard(t *testing.T) {
	b := converged(2, 2, 2)
	b.s.Targets = b.s.Targets[:2] // cluster 2 leaves the spec, its directive remains
	decision := plan(b.build())
	assert.Equal(t, decisionInvalidSpec, decision.Kind)
	assert.Contains(t, decision.Reason, "cluster removal is not implemented")
	assert.Contains(t, decision.Reason, clusters[2])
}

func TestPlanAllocationAcks(t *testing.T) {
	t.Run("No directives: mint and record on the first cluster at member count 0", func(t *testing.T) {
		decision := plan(newSnapshot(3, 2, 2).build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 0, decision.DirectiveSpec.MemberCount)
		assert.Equal(t, map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}, decision.DirectiveSpec.IndexAllocations)
		assert.Equal(t, testPlanTerm, decision.DirectiveSpec.LeadershipTerm)
		assert.Equal(t, testPlanHash, decision.DirectiveSpec.TargetSpecHash)
		assert.Equal(t, testPlanProject, decision.DirectiveSpec.ProjectID)
		assert.Equal(t, metav1.NewTime(testPlanNow), decision.DirectiveSpec.AdvancedAt)
	})

	t.Run("One copy: push the map to the second cluster", func(t *testing.T) {
		b := newSnapshot(3, 2, 2).withGrantedDirective(clusters[0], 0)
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[1], decision.TargetCluster)
		assert.Equal(t, 0, decision.DirectiveSpec.MemberCount)
	})

	t.Run("Two copies: durable, advancement takes over", func(t *testing.T) {
		b := newSnapshot(3, 2, 2).withGrantedDirective(clusters[0], 0).withGrantedDirective(clusters[1], 0)
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		// first deploy: full spec count, not another map push
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 3, decision.DirectiveSpec.MemberCount)
	})
}

func TestPlanAllocationInvariant(t *testing.T) {
	t.Run("A visible ghost entry is reused, never re-minted", func(t *testing.T) {
		// a dead leader pushed cluster-2 -> 4 to a single copy; every visible entry consumes
		// its index, so the current leader adopts 4 instead of minting a conflicting one
		b := newSnapshot(3, 2, 2).withGrantedDirective(clusters[0], 0)
		b.editDirective(clusters[0], func(d *directiveView) {
			d.Spec.IndexAllocations = map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 4}
		})
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, 4, decision.DirectiveSpec.IndexAllocations[clusters[2]])
	})

	t.Run("Key conflict: the value on more copies wins", func(t *testing.T) {
		b := newSnapshot(3, 2, 2).
			withGrantedDirective(clusters[0], 0).
			withGrantedDirective(clusters[1], 0).
			withGrantedDirective(clusters[2], 0)
		b.editDirective(clusters[2], func(d *directiveView) {
			d.Spec.IndexAllocations = map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 4}
		})
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		// clusters[2]: 2 is on two copies, the ghost 4 on one — quorum-backed value overwrites
		assert.Equal(t, 2, decision.DirectiveSpec.IndexAllocations[clusters[2]])
	})

	t.Run("A ghost on a converged copy is overwritten by the next advancement", func(t *testing.T) {
		// the winning entry already has quorum, so no allocation push is due; the disagreeing
		// copy is simply behind the plan and the ordinary directive write corrects it
		b := converged(2, 2, 2)
		b.editDirective(clusters[2], func(d *directiveView) {
			d.Spec.IndexAllocations = map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 4}
		})
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[2], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.IndexAllocations[clusters[2]])
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount) // count untouched
	})

	t.Run("No minting while a cluster is unreachable", func(t *testing.T) {
		b := newSnapshot(3, 2, 2)
		b.s.Directives[clusters[2]] = directiveView{Unreachable: true}
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
		assert.Contains(t, decision.Reason, "unreachable")
	})

	t.Run("Steady-state cluster loss blocks nothing: others still advance", func(t *testing.T) {
		// allocations fully acked before the loss; the AC at the lost cluster's spec count
		// proves nothing is in flight there, so the reachable cluster's scale-up proceeds
		b := converged(2, 2, 2)
		b.s.Directives[clusters[2]] = directiveView{Unreachable: true}
		b.s.Targets[0].Members = 3
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 3, decision.DirectiveSpec.MemberCount)
	})
}

func TestPlanFirstDeployParallelism(t *testing.T) {
	t.Run("Advances at full count with allocations durable", func(t *testing.T) {
		b := newSnapshot(3, 2, 2).withGrantedDirective(clusters[0], 0).withGrantedDirective(clusters[1], 0)
		assert.Equal(t, []string{clusters[0], clusters[1], clusters[2]}, advancementCandidates(b.build()))
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, 3, decision.DirectiveSpec.MemberCount)
	})

	t.Run("Mid-first-deploy crash stays parallel", func(t *testing.T) {
		// two clusters already granted full counts, agents not yet converged, AC still empty:
		// the restarted leader must keep advancing the third at FULL count, no convergence gate
		b := newSnapshot(3, 2, 2).withGrantedDirective(clusters[0], 3).withGrantedDirective(clusters[1], 2)
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.InGoalState = false })
		b.editDirective(clusters[1], func(d *directiveView) { d.Status.AgentRegistered = false; d.Status.InGoalState = false })
		assert.Equal(t, []string{clusters[2]}, advancementCandidates(b.build()))
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[2], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
	})
}

func TestPlanExclusivity(t *testing.T) {
	t.Run("One cluster, one member step", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 4
		b.s.Targets[1].Members = 3
		assert.Equal(t, []string{clusters[0]}, advancementCandidates(b.build()))
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 3, decision.DirectiveSpec.MemberCount) // 2+1, never the full 4
	})

	t.Run("Nothing moves while a cluster is in flight", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 4
		b.s.Targets[1].Members = 3
		b.withGrantedDirective(clusters[0], 3) // granted 3, AC still 2 -> mid-step
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.InGoalState = false })
		assert.Nil(t, advancementCandidates(b.build()))
	})

	t.Run("New cluster on an existing deployment scales one at a time", func(t *testing.T) {
		b := converged(2, 2, 0)
		b.s.Targets[2].Members = 2
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[2], decision.TargetCluster)
		assert.Equal(t, 1, decision.DirectiveSpec.MemberCount) // 0 -> 1, never the jump
	})
}

func TestPlanFenceDiscipline(t *testing.T) {
	// the member's local CR copy still hashes to something older: it holds, and the planner
	// never advances its count past what it has observed
	b := converged(2, 2, 2)
	b.s.Targets[0].Members = 3
	b.editDirective(clusters[0], func(d *directiveView) { d.Status.ObservedSpecHash = testPlanOldHash })
	decision := plan(b.build())
	assert.Equal(t, decisionNotProgressing, decision.Kind)
	assert.Contains(t, decision.Reason, "GitOps")
}

// TestPlanHashOnlyChangeRefreshesDirectives walks the content-only ladder: no member moves, but
// the fences refresh one directive at a time and the deployment content republishes at the end.
func TestPlanHashOnlyChangeRefreshesDirectives(t *testing.T) {
	t.Run("Stage 1: refresh the first directive's fence at the unchanged count", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.AC.SpecHash = testPlanOldHash
		for _, cluster := range clusters {
			b.editDirective(cluster, func(d *directiveView) { d.Spec.TargetSpecHash = testPlanOldHash })
		}
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount) // count unchanged
		assert.Equal(t, testPlanHash, decision.DirectiveSpec.TargetSpecHash)
	})

	t.Run("Stage 2: a member still behind the new fence blocks the content republish", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.AC.SpecHash = testPlanOldHash
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.ObservedSpecHash = testPlanOldHash })
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
	})

	t.Run("Stage 3: fences echoed everywhere, republish the content at unchanged counts", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.AC.SpecHash = testPlanOldHash
		decision := plan(b.build())
		require.Equal(t, decisionWriteAC, decision.Kind)
		assert.Equal(t, &acPayload{LeadershipTerm: testPlanTerm, MemberCounts: map[string]int{clusters[0]: 2, clusters[1]: 2, clusters[2]: 2}}, decision.AC)
	})

	t.Run("Stage 4: content re-stamped, converged", func(t *testing.T) {
		decision := plan(converged(2, 2, 2).build())
		assert.Equal(t, decisionNoop, decision.Kind)
	})
}

// TestPlanScaleUpLadder walks the up ladder one snapshot per stage; each stage doubles as the
// crash-prefix test for the previous decision (plan is memoryless).
func TestPlanScaleUpLadder(t *testing.T) {
	t.Run("Stage 1: grant one more member", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, 3, decision.DirectiveSpec.MemberCount)
	})

	t.Run("Stage 2: agents not registered yet, no AC write", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		b.withGrantedDirective(clusters[0], 3)
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.AgentRegistered = false; d.Status.InGoalState = false })
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
		assert.Contains(t, decision.Reason, "registered")
	})

	t.Run("Stage 3: agents registered, AC write at the granted counts", func(t *testing.T) {
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		b.withGrantedDirective(clusters[0], 3)
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.InGoalState = false })
		decision := plan(b.build())
		require.Equal(t, decisionWriteAC, decision.Kind)
		assert.Equal(t, &acPayload{LeadershipTerm: testPlanTerm, MemberCounts: map[string]int{clusters[0]: 3, clusters[1]: 2, clusters[2]: 2}}, decision.AC)
	})

	t.Run("Stage 4: AC written, waiting on goal state", func(t *testing.T) {
		b := converged(2, 2, 2).withAC(3, 2, 2)
		b.s.Targets[0].Members = 3
		b.withGrantedDirective(clusters[0], 3)
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.InGoalState = false })
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
		assert.Contains(t, decision.Reason, "goal state")
	})

	t.Run("Stage 5: converged at the new count", func(t *testing.T) {
		decision := plan(converged(3, 2, 2).build())
		assert.Equal(t, decisionNoop, decision.Kind)
	})
}

// TestPlanScaleDownLadder walks the inverted ladder: the AC write comes FIRST, the member's
// grant follows only after Ops Manager witnesses the remaining processes back in goal state.
func TestPlanScaleDownLadder(t *testing.T) {
	t.Run("Stage 1: the AC write initiates the step", func(t *testing.T) {
		b := converged(3, 2, 2)
		b.s.Targets[0].Members = 2
		decision := plan(b.build())
		require.Equal(t, decisionWriteAC, decision.Kind)
		assert.Equal(t, &acPayload{LeadershipTerm: testPlanTerm, MemberCounts: map[string]int{clusters[0]: 2, clusters[1]: 2, clusters[2]: 2}}, decision.AC)
	})

	t.Run("Stage 2: witness pending, no directive advance", func(t *testing.T) {
		b := converged(3, 2, 2).withAC(2, 2, 2).withConvergedOMFacts(3, 2, 2)
		b.s.Targets[0].Members = 2
		// the shrinking member's own facts go false; they must not matter here
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.AgentRegistered = false; d.Status.InGoalState = false })
		// the witness is not converged yet: mark the surviving processes as not in goal
		for hostname, state := range b.s.OMFacts.ProcessStates {
			state.GoalAchieved = false
			b.s.OMFacts.ProcessStates[hostname] = state
		}
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
	})

	t.Run("Stage 2 blocked when the OM facts are unreadable", func(t *testing.T) {
		b := converged(3, 2, 2).withAC(2, 2, 2)
		b.s.Targets[0].Members = 2
		b.s.OMFacts = omFactsView{Read: false}
		decision := plan(b.build())
		assert.Equal(t, decisionNotProgressing, decision.Kind)
	})

	t.Run("Stage 3: witness converged, the grant follows the AC", func(t *testing.T) {
		b := converged(3, 2, 2).withAC(2, 2, 2).withConvergedOMFacts(2, 2, 2)
		b.s.Targets[0].Members = 2
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.AgentRegistered = false; d.Status.InGoalState = false })
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
	})

	t.Run("Stage 4: converged at the reduced count", func(t *testing.T) {
		decision := plan(converged(2, 2, 2).build())
		assert.Equal(t, decisionNoop, decision.Kind)
	})
}

func TestPlanStuckCluster(t *testing.T) {
	freshAndStuck := func(advancedAt time.Time) planDecision {
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		b.withGrantedDirective(clusters[0], 3)
		b.editDirective(clusters[0], func(d *directiveView) {
			d.Status.AgentRegistered = false
			d.Status.InGoalState = false
			d.Spec.AdvancedAt = metav1.NewTime(advancedAt)
		})
		return plan(b.build())
	}

	t.Run("Fresh step reads as waiting", func(t *testing.T) {
		decision := freshAndStuck(testPlanNow.Add(-time.Minute))
		require.Equal(t, decisionNotProgressing, decision.Kind)
		assert.Contains(t, decision.Reason, "waiting for cluster")
	})

	t.Run("Old step reads as not progressing", func(t *testing.T) {
		decision := freshAndStuck(testPlanNow.Add(-plannerNotProgressingAfter - time.Minute))
		require.Equal(t, decisionNotProgressing, decision.Kind)
		assert.Contains(t, decision.Reason, "has not progressed since")
	})
}

func TestPlanSpecSkew(t *testing.T) {
	// two of three members observe a common hash the leader does not have: the leader's own
	// cluster is the stale one — say so instead of silently pinning the deployment
	b := converged(2, 2, 2)
	b.editDirective(clusters[0], func(d *directiveView) { d.Status.ObservedSpecHash = "hash-newer" })
	b.editDirective(clusters[1], func(d *directiveView) { d.Status.ObservedSpecHash = "hash-newer" })
	decision := plan(b.build())
	require.Equal(t, decisionNotProgressing, decision.Kind)
	assert.Contains(t, decision.Reason, "spec skew")
}

// TestPlanStateLossSeedsFromAC pins the seed rule (Failure 7, robustness backlog T1-T3): state
// loss may cost discipline, never capacity — missing coordination state is seeded from the AC
// count at the cluster's index, never from granted()'s zero value, exactly as the legacy
// scaler's fallback is the spec.
func TestPlanStateLossSeedsFromAC(t *testing.T) {
	t.Run("T1: directive deleted while another cluster is mid-scale-up", func(t *testing.T) {
		// cluster 0 is granted +1 with agents registered (its AC write is due); cluster 1's
		// directive is deleted. The lost directive is recreated at the AC count BEFORE any AC
		// write — its live members are never published at 0
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		b.withGrantedDirective(clusters[0], 3)
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.InGoalState = false })
		delete(b.s.Directives, clusters[1])
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[1], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
	})

	t.Run("T1 variant: unreachable directive during a peer's scale-up is published at the AC count", func(t *testing.T) {
		// an unreachable view also reads Exists=false, so the seed must cover it too: a mere
		// network partition during a peer's AC write must not publish the partitioned cluster at 0
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		b.withGrantedDirective(clusters[0], 3)
		b.editDirective(clusters[0], func(d *directiveView) { d.Status.InGoalState = false })
		b.s.Directives[clusters[1]] = directiveView{Unreachable: true}
		decision := plan(b.build())
		require.Equal(t, decisionWriteAC, decision.Kind, decision.Reason)
		assert.Equal(t, &acPayload{LeadershipTerm: testPlanTerm, MemberCounts: map[string]int{clusters[0]: 3, clusters[1]: 2, clusters[2]: 2}}, decision.AC)
	})

	t.Run("T2: directive deleted in steady state (N=3) is recreated at the AC count", func(t *testing.T) {
		// the allocation map is still ack-satisfied on the two survivors, so nothing recreates
		// the directive today — a permanent management freeze; recognition must recreate it
		b := converged(2, 2, 2)
		delete(b.s.Directives, clusters[1])
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[1], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
		assert.Equal(t, map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}, decision.DirectiveSpec.IndexAllocations)
	})

	t.Run("T3: N=2 recreation via the allocation push seeds at the AC count, never 0", func(t *testing.T) {
		// with two clusters the ack threshold DOES recreate the lost directive — the seed decides
		// whether that is recognition of existing capacity or a scale-to-0 instruction
		b := converged(2, 2)
		delete(b.s.Directives, clusters[1])
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[1], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
	})

	t.Run("T4: directive lost mid-scale-up reseeds at the AC count", func(t *testing.T) {
		// the grant was 3, the AC still 2: only the never-voting extra pod is cut by the reseed;
		// the ladder then re-advances toward the spec
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 3
		delete(b.s.Directives, clusters[0])
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
	})

	t.Run("T5: directive lost mid-scale-down reseeds at the down-ladder's own next value", func(t *testing.T) {
		// the AC already dropped to 2, the shrink grant was pending when the directive was lost:
		// the seed equals what the down ladder would have granted — the shrink completes as if
		// never interrupted
		b := converged(3, 2, 2).withAC(2, 2, 2).withConvergedOMFacts(2, 2, 2)
		b.s.Targets[0].Members = 2
		delete(b.s.Directives, clusters[0])
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
	})

	t.Run("T7: a directive below the AC count is re-granted at it, spec at the AC count", func(t *testing.T) {
		// the wedge state (pre-existing damage, manual meddling): recognition of existing
		// capacity, then the normal ladder runs toward spec
		b := converged(2, 2, 2)
		b.editDirective(clusters[0], func(d *directiveView) { d.Spec.MemberCount = 1 })
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
		assert.Contains(t, decision.Reason, "recognizing existing capacity")
	})

	t.Run("T7: the wedge re-grants at the AC count even when the spec wants less", func(t *testing.T) {
		// the shrink to the spec then happens through the AC-first down ladder, never through a
		// raw low grant against live members
		b := converged(2, 2, 2)
		b.s.Targets[0].Members = 1
		b.editDirective(clusters[0], func(d *directiveView) { d.Spec.MemberCount = 1 })
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount)
	})

	t.Run("T13: recognition changes nothing physical — the paused ladder resumes after the re-ack", func(t *testing.T) {
		// beat 1 is T1's world: the deletion pauses the concurrent scale-up for exactly the
		// recognition write
		midLadder := func() *snapshotBuilder {
			b := converged(2, 2, 2)
			b.s.Targets[0].Members = 3
			b.withGrantedDirective(clusters[0], 3)
			b.editDirective(clusters[0], func(d *directiveView) { d.Status.InGoalState = false })
			return b
		}
		beat1 := midLadder()
		delete(beat1.s.Directives, clusters[1])
		recreation := plan(beat1.build())
		require.Equal(t, decisionWriteDirective, recreation.Kind, recreation.Reason)
		require.Equal(t, clusters[1], recreation.TargetCluster)

		// beat 2: the recreated directive is echoed by its member (a fully current directive at
		// the recreation's count), and the paused AC write proceeds carrying the recreated
		// cluster at its unchanged count — membership only ever changes through the AC
		beat2 := midLadder().withGrantedDirective(clusters[1], recreation.DirectiveSpec.MemberCount)
		decision := plan(beat2.build())
		require.Equal(t, decisionWriteAC, decision.Kind, decision.Reason)
		assert.Equal(t, &acPayload{LeadershipTerm: testPlanTerm, MemberCounts: map[string]int{clusters[0]: 3, clusters[1]: 2, clusters[2]: 2}}, decision.AC)
	})
}

// TestPlanStateLossFailClosed pins the seed rule's companion gates (backlog T6, T8, T12): when
// the observed world cannot be read — or contradicts every guess — the planner freezes with a
// pointed message instead of seeding a 0 or minting a colliding identity.
func TestPlanStateLossFailClosed(t *testing.T) {
	t.Run("T6: no directive is minted while the AC is unreadable", func(t *testing.T) {
		// acCount reads 0 when the AC is unread — the 0-seed must not return through that door
		b := newSnapshot(2, 2, 2)
		b.s.AC = acView{Read: false}
		decision := plan(b.build())
		require.Equal(t, decisionNotProgressing, decision.Kind, decision.Reason)
		assert.Contains(t, decision.Reason, "unreadable")
	})

	t.Run("T6: no directive is created for a visible allocation while the AC is unreadable", func(t *testing.T) {
		// creation without minting (N=2, the surviving copy carries the map): the seed count is
		// unknown, so the push holds rather than creating at 0
		b := converged(2, 2)
		delete(b.s.Directives, clusters[1])
		b.s.AC = acView{Read: false}
		decision := plan(b.build())
		require.Equal(t, decisionNotProgressing, decision.Kind, decision.Reason)
		assert.Contains(t, decision.Reason, "seed member count is unknown")
	})

	t.Run("T8: total state loss over a live AC refuses to mint colliding identities", func(t *testing.T) {
		// no surviving map copy: a mint on a guessed (spec-order) index would collide with the
		// members the AC already carries there — freeze and name the runbook, zero writes
		b := converged(2, 2, 2)
		b.s.Directives = map[string]directiveView{}
		decision := plan(b.build())
		require.Equal(t, decisionNotProgressing, decision.Kind, decision.Reason)
		assert.Contains(t, decision.Reason, "refusing to mint")
		assert.Contains(t, decision.Reason, "majority-loss runbook")
	})

	t.Run("T12: a both-ways spec edit against the live AC is refused with one directive lost", func(t *testing.T) {
		// the surviving maps make the lost cluster's index known, so its seeded granted count
		// stands in for the lost state and blockScalingBothWays still fail-closes on real capacity
		b := converged(2, 2, 2)
		delete(b.s.Directives, clusters[1])
		b.s.Targets[0].Members = 3
		b.s.Targets[1].Members = 1
		decision := plan(b.build())
		require.Equal(t, decisionInvalidSpec, decision.Kind, decision.Reason)
		assert.Contains(t, decision.Reason, "scale up and scale down")
	})

	t.Run("T12: a both-ways spec edit under TOTAL loss fail-closes behind the runbook refusal", func(t *testing.T) {
		// with no surviving map every index is a guess, so the planner withholds spec judgment
		// entirely: the mint-collision refusal freezes the world and names the runbook instead
		// of blaming a spec it cannot honestly evaluate
		b := converged(2, 2, 2)
		b.s.Directives = map[string]directiveView{}
		b.s.Targets[0].Members = 3
		b.s.Targets[1].Members = 1
		decision := plan(b.build())
		require.Equal(t, decisionNotProgressing, decision.Kind, decision.Reason)
		assert.Contains(t, decision.Reason, "majority-loss runbook")
	})

	t.Run("Total loss with a reordered spec still names the runbook, never a guessed judgment", func(t *testing.T) {
		// historically cluster 0 held index 1 (3 members) and cluster 1 held index 0 (2 members);
		// the spec — valid and untouched — lists them in the opposite order of their indexes. A
		// spec-position seed would read the peer's count and misjudge this as scaling both ways
		b := newSnapshot(3, 2).withAC(2, 3)
		decision := plan(b.build())
		require.Equal(t, decisionNotProgressing, decision.Kind, decision.Reason)
		assert.Contains(t, decision.Reason, "majority-loss runbook")
	})

	t.Run("A map push never re-publishes a below-AC grant: the wedge is floored at the AC count", func(t *testing.T) {
		// cluster 0's directive is damaged twice over — granted below the AC AND its own
		// allocation entry lost from two copies — so the allocation push (which runs before
		// recognition) rewrites it; the push must carry the AC count, not the damaged grant
		b := converged(2, 2, 2)
		b.editDirective(clusters[0], func(d *directiveView) {
			d.Spec.MemberCount = 1
			d.Spec.IndexAllocations = map[string]int{clusters[1]: 1, clusters[2]: 2}
		})
		b.editDirective(clusters[1], func(d *directiveView) {
			d.Spec.IndexAllocations = map[string]int{clusters[1]: 1, clusters[2]: 2}
		})
		decision := plan(b.build())
		require.Equal(t, decisionWriteDirective, decision.Kind, decision.Reason)
		assert.Equal(t, clusters[0], decision.TargetCluster)
		assert.Equal(t, 2, decision.DirectiveSpec.MemberCount, "the damaged grant is never re-published")
		assert.Equal(t, map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}, decision.DirectiveSpec.IndexAllocations)
	})
}

func TestPlanACUnreadable(t *testing.T) {
	b := converged(2, 2, 2)
	b.s.AC = acView{Read: false}
	decision := plan(b.build())
	require.Equal(t, decisionNotProgressing, decision.Kind)
	assert.Contains(t, decision.Reason, "automation config")
}

func TestClassifyCluster(t *testing.T) {
	current := func(edit func(d *directiveView)) directiveView {
		d := directiveView{
			Exists:     true,
			Generation: directiveGenSeen,
			Status: operatorv1.MongoDBDirectiveStatus{
				ObservedGeneration: directiveGenSeen,
				ObservedSpecHash:   testPlanHash,
				StsApplied:         true,
				AgentRegistered:    true,
				InGoalState:        true,
			},
		}
		edit(&d)
		return d
	}

	cases := []struct {
		name string
		view directiveView
		want clusterState
	}{
		{"missing", directiveView{}, awaitingDirective},
		{"not acked", current(func(d *directiveView) { d.Status.ObservedGeneration = directiveGenSeen - 1 }), awaitingDirectiveAck},
		{"spec lagging", current(func(d *directiveView) { d.Status.ObservedSpecHash = testPlanOldHash }), awaitingSpecSync},
		{"sts pending", current(func(d *directiveView) { d.Status.StsApplied = false }), applyingStatefulSet},
		{"agents pending", current(func(d *directiveView) { d.Status.AgentRegistered = false }), awaitingAgentRegistration},
		{"goal pending", current(func(d *directiveView) { d.Status.InGoalState = false }), awaitingGoalState},
		{"converged", current(func(d *directiveView) {}), inGoalState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyCluster(tc.view, testPlanHash))
		})
	}
}
