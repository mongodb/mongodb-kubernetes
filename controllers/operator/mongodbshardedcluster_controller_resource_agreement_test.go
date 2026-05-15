package operator

// F12b — reconcile-top resource-agreement gate tests.
//
// These tests directly drive the gate (gateOnResourceAgreement) so we can
// assert: nil coordinator → no-op; coordinator + agreed resources → OK;
// coordinator + disagreement → Pending with the diagnostic visible.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// TestGateOnResourceAgreement_NilCoordinator: non-distributed mode is a
// no-op — the gate returns OK without touching the local client or proposing
// anything.
func TestGateOnResourceAgreement_NilCoordinator(t *testing.T) {
	ctx := context.Background()
	helper, _, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	helper.coordinator = nil

	status := helper.gateOnResourceAgreement(ctx, zap.S())
	assert.True(t, status.IsOK(), "non-distributed mode: gate must always return OK")
}

// TestGateOnResourceAgreement_AgreedFlowsThrough: with a fake coordinator that
// returns ResourcesAgreed, the gate returns OK and exposes the local-hash
// reports it made.
func TestGateOnResourceAgreement_AgreedFlowsThrough(t *testing.T) {
	ctx := context.Background()
	helper, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	c := newFakeCoordinator("member-cluster-1", true)
	helper.SetCoordinator(c)

	status := helper.gateOnResourceAgreement(ctx, zap.S())
	assert.True(t, status.IsOK(), "with agreed resources the gate returns OK")

	// The fake recorded a hash for at least the project ConfigMap and the
	// credentials Secret (the two refs every sharded CR carries).
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotNil(t, c.resources, "ReportResource should have run")

	want := map[string]string{
		"ConfigMap/" + sc.GetProjectConfigMapNamespace() + "/" + sc.GetProjectConfigMapName(): "",
		"Secret/" + sc.GetCredentialsSecretNamespace() + "/" + sc.GetCredentialsSecretName(): "",
	}
	for key := range want {
		entries, ok := c.resources[key]
		require.True(t, ok, "expected report for ref %q; got resources=%v", key, c.resources)
		require.Contains(t, entries, "member-cluster-1", "expected own-cluster entry for ref %q", key)
		assert.NotEmpty(t, entries["member-cluster-1"], "hash for ref %q must not be empty", key)
	}
}

// TestGateOnResourceAgreement_DisagreementSurfacesDiagnostic: a fake
// coordinator returning ResourcesPending+diag causes the gate to surface
// the diagnostic in workflow.Pending and (downstream) update MDB status.
func TestGateOnResourceAgreement_DisagreementSurfacesDiagnostic(t *testing.T) {
	ctx := context.Background()
	helper, _, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	c := newFakeCoordinator("member-cluster-1", true)
	c.resourcesAgreedFn = func(crKey coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string) {
		return coordination.ResourcesPending, "Resource ConfigMap/ns/cm hash mismatch: cluster-a=abc1234, cluster-b=def5678 — cluster-b is out of sync."
	}
	helper.SetCoordinator(c)

	gateStatus := helper.gateOnResourceAgreement(ctx, zap.S())
	require.False(t, gateStatus.IsOK(), "disagreement: gate must return non-OK")
	msg := extractStatusMessage(t, gateStatus)
	assert.Contains(t, msg, "ResourcesNotAgreed")
	assert.Contains(t, msg, "hash mismatch")
	assert.Contains(t, msg, "out of sync")
}

// extractStatusMessage walks a workflow.Status's StatusOptions and returns
// the joined Message strings — enough to assert on the diagnostic.
func extractStatusMessage(t *testing.T, s workflow.Status) string {
	t.Helper()
	out := ""
	for _, opt := range s.StatusOptions() {
		if m, ok := opt.(status.MessageOption); ok {
			out += " " + m.Message
		}
	}
	return out
}

// TestCollectSpecReferencedResourceRefs_DefaultSharded: a vanilla three-cluster
// sharded CR yields at least {project CM, creds Secret}.
func TestCollectSpecReferencedResourceRefs_DefaultSharded(t *testing.T) {
	_, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	refs := collectSpecReferencedResourceRefs(sc)
	require.NotEmpty(t, refs)

	var sawCM, sawSecret bool
	for _, r := range refs {
		switch r.Kind {
		case "ConfigMap":
			if r.Name == sc.GetProjectConfigMapName() {
				sawCM = true
			}
		case "Secret":
			if r.Name == sc.GetCredentialsSecretName() {
				sawSecret = true
			}
		}
	}
	assert.True(t, sawCM, "must include project ConfigMap ref")
	assert.True(t, sawSecret, "must include credentials Secret ref")
}

// TestHashConfigMapData_StableAcrossKeyOrder: rebuilding the same logical CM
// with different key insertion order yields the same hash.
func TestHashConfigMapData_StableAcrossKeyOrder(t *testing.T) {
	cmA := &corev1.ConfigMap{Data: map[string]string{"alpha": "1", "beta": "2", "gamma": "3"}}
	cmB := &corev1.ConfigMap{Data: map[string]string{"gamma": "3", "alpha": "1", "beta": "2"}}
	assert.Equal(t, hashConfigMapData(cmA), hashConfigMapData(cmB))

	cmC := &corev1.ConfigMap{Data: map[string]string{"alpha": "1", "beta": "2", "gamma": "DIFFERENT"}}
	assert.NotEqual(t, hashConfigMapData(cmA), hashConfigMapData(cmC))
}

// TestHashSecretData_StableAndIncludesType: different Secret.Type with the
// same data must hash differently.
func TestHashSecretData_StableAndIncludesType(t *testing.T) {
	a := &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("v")}}
	b := &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"k": []byte("v")}}
	assert.NotEqual(t, hashSecretData(a), hashSecretData(b))

	c := &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("v")}}
	assert.Equal(t, hashSecretData(a), hashSecretData(c))
}

// TestCollectSpecReferencedResourceRefs_IncludesCRSpec — G'5 iter 14e Gate 0.
// The agreement set must include the MongoDB CR itself so divergent CR
// generations across clusters block the reconcile at the top, BEFORE the
// scaler / cross-cluster mutex gets to play. The CRSpecResourceKind value
// ("MongoDB") + CR's own namespace/name identifies the ref; the per-cluster
// content hash is computed over `.spec` only by `hashCRSpec`.
func TestCollectSpecReferencedResourceRefs_IncludesCRSpec(t *testing.T) {
	_, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	refs := collectSpecReferencedResourceRefs(sc)

	var sawCR bool
	for _, r := range refs {
		if r.Kind == CRSpecResourceKind && r.Namespace == sc.Namespace && r.Name == sc.Name {
			sawCR = true
			break
		}
	}
	require.True(t, sawCR, "iter-14e Gate 0: collectSpecReferencedResourceRefs MUST include a MongoDB CR ref (got refs=%v)", refs)
}

// TestHashCRSpec_StableIgnoresMetadata — the CR-spec hash must be stable
// across per-cluster metadata.generation / resourceVersion drift. Two
// MongoDB CRs with byte-identical `.spec` but different
// metadata.generation (the realistic case for divergent K8s apiservers)
// must produce the same hash; otherwise the gate would block on every
// reconcile.
func TestHashCRSpec_StableIgnoresMetadata(t *testing.T) {
	_, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	// Two clones differing only in metadata fields the apiserver mutates.
	a := sc.DeepCopy()
	a.Generation = 7
	a.ResourceVersion = "1234"
	b := sc.DeepCopy()
	b.Generation = 8
	b.ResourceVersion = "99999"
	assert.Equal(t, hashCRSpec(a), hashCRSpec(b),
		"CR-spec hash must be stable across metadata drift; got a=%s b=%s",
		hashCRSpec(a), hashCRSpec(b))

	// A genuine spec change (member count flip) must produce a different
	// hash. This is the bit that lets Gate 0 block on real spec drift
	// while letting metadata drift through.
	c := sc.DeepCopy()
	if len(c.Spec.ShardSpec.ClusterSpecList) > 0 {
		c.Spec.ShardSpec.ClusterSpecList[0].Members += 1
	}
	assert.NotEqual(t, hashCRSpec(a), hashCRSpec(c),
		"CR-spec hash must change when .spec changes")
}

// TestGateOnResourceAgreement_CRSpecDriftBlocks — Gate 0 integration with
// the existing reconcile-top gate. With a fake coordinator that returns
// ResourcesPending for a CR-spec hash mismatch diagnostic, the gate
// returns workflow.Pending and the diagnostic mentions the CR ref so the
// operator surface clearly identifies the gate that's blocking.
//
// This is the iter-14e Gate 0 regression pin: in the absence of Gate 0
// the operator would proceed to the per-component lease and start writing
// STSes against divergent specs. With Gate 0 in place, the diagnostic
// blocks the reconcile until ALL clusters report the same `.spec` hash.
//
// On the tip BEFORE this commit's gate addition, the diagnostic would not
// mention the CR-spec ref at all (because the ref wasn't in the agreed
// set). After the addition, the diagnostic mentions
// `MongoDB/<ns>/<name>` (the CRSpecResourceKind label) so an operator
// reading the MDB status can immediately tell that CR-spec drift is the
// blocker.
func TestGateOnResourceAgreement_CRSpecDriftBlocks(t *testing.T) {
	ctx := context.Background()
	helper, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	c := newFakeCoordinator("member-cluster-1", true)
	// Capture refs passed to WaitForResourcesAgreed and return a CR-spec
	// drift diagnostic.
	var seenCRSpecRef bool
	c.resourcesAgreedFn = func(crKey coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string) {
		for _, ref := range refs {
			if ref.Kind == CRSpecResourceKind && ref.Namespace == sc.Namespace && ref.Name == sc.Name {
				seenCRSpecRef = true
				break
			}
		}
		return coordination.ResourcesPending,
			"Resource " + CRSpecResourceKind + "/" + sc.Namespace + "/" + sc.Name +
				" hash mismatch: cluster-a=aaaa, cluster-b=bbbb — clusters disagree on .spec content."
	}
	helper.SetCoordinator(c)

	gateStatus := helper.gateOnResourceAgreement(ctx, zap.S())
	require.False(t, gateStatus.IsOK(), "iter-14e Gate 0: CR-spec drift must block reconcile (got OK)")
	require.True(t, seenCRSpecRef, "iter-14e Gate 0: WaitForResourcesAgreed MUST be called with the MongoDB CR ref in the agreed set; got refs without CR-spec ref")

	msg := extractStatusMessage(t, gateStatus)
	assert.Contains(t, msg, "ResourcesNotAgreed",
		"Gate diagnostic must use the ResourcesNotAgreed prefix")
	assert.Contains(t, msg, CRSpecResourceKind+"/"+sc.Namespace+"/"+sc.Name,
		"Gate diagnostic must identify the CR-spec ref so the operator can see CR drift is the blocker")
}

// TestGateOnResourceAgreement_CRSpecAgreesWithProjectAndCreds — the
// happy-path complement: when all three agreed refs (CR-spec, project CM,
// credentials Secret) match across clusters, the gate flows through to
// OK. Confirms that adding the CR ref doesn't break the existing
// agreed-state path.
func TestGateOnResourceAgreement_CRSpecAgreesWithProjectAndCreds(t *testing.T) {
	ctx := context.Background()
	helper, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	c := newFakeCoordinator("member-cluster-1", true)
	helper.SetCoordinator(c)

	status := helper.gateOnResourceAgreement(ctx, zap.S())
	require.True(t, status.IsOK(),
		"iter-14e Gate 0: when all refs agree, the gate must return OK; got %v", status)

	// The fake recorded a hash for the CR-spec ref too.
	c.mu.Lock()
	defer c.mu.Unlock()
	crKey := CRSpecResourceKind + "/" + sc.Namespace + "/" + sc.Name
	entries, ok := c.resources[crKey]
	require.True(t, ok, "expected ReportResource for CR-spec ref %q; got resources=%v", crKey, c.resources)
	require.Contains(t, entries, "member-cluster-1",
		"expected own-cluster entry for CR-spec ref")
	assert.NotEmpty(t, entries["member-cluster-1"], "CR-spec hash must not be empty")
}

// TestHashCRSpec_StableAcrossManagedFieldsAndStatus — G'5 iter 14f. The
// CR-spec hash must be invariant under per-cluster metadata.managedFields
// drift and any change to the Status subresource. These are the fields
// the apiserver mutates independently per cluster (managedFields tracks
// the per-cluster controller's field ownership; Status is written by
// the operator itself), so any sensitivity to them would make Gate 0
// fail under entirely legitimate per-cluster state and the operator
// would never agree.
//
// Regression pin for the iter-14e/14f drift hypothesis: even with
// identical .spec content across clusters, the previous typed-struct
// `json.Marshal(sc.Spec)` could in principle pick up pointer-vs-nil
// drift introduced by per-cluster informer state. The canonical-JSON
// of the unstructured representation must be stable.
func TestHashCRSpec_StableAcrossManagedFieldsAndStatus(t *testing.T) {
	_, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	a := sc.DeepCopy()
	a.ManagedFields = []metav1.ManagedFieldsEntry{
		{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply},
	}
	a.Status = mdbv1.MongoDbStatus{}

	b := sc.DeepCopy()
	b.ManagedFields = []metav1.ManagedFieldsEntry{
		{Manager: "mongodb-kubernetes-operator", Operation: metav1.ManagedFieldsOperationUpdate},
		{Manager: "another-controller", Operation: metav1.ManagedFieldsOperationApply},
	}
	// Force a Status difference — Status is a runtime-mutated subresource
	// and per-cluster operators write it independently.
	b.Status.Phase = status.PhasePending
	b.Status.ShardCount = 0

	c := sc.DeepCopy()
	c.Status.Phase = status.PhaseRunning
	c.Status.ShardCount = 9
	c.Generation = 42
	c.ResourceVersion = "987654321"
	c.UID = "different-uid-because-different-apiserver"

	assert.Equal(t, hashCRSpec(a), hashCRSpec(b),
		"iter-14f: managedFields / status drift must not affect CR-spec hash; got a=%s b=%s",
		hashCRSpec(a), hashCRSpec(b))
	assert.Equal(t, hashCRSpec(a), hashCRSpec(c),
		"iter-14f: all metadata drift (managedFields/status/generation/RV/UID) must be invariant; got a=%s c=%s",
		hashCRSpec(a), hashCRSpec(c))
}

// TestHashCRSpec_StableAcrossNoOpRoundTrip — G'5 iter 14f. A CR whose
// wire-side JSON is identical before and after a no-op apiserver round
// trip must hash identically. We simulate the round trip by:
//
//  1. Taking the CR.
//  2. Marshalling to JSON (apiserver wire format).
//  3. Unmarshalling back into a fresh `*MongoDB` (controller-runtime
//     decode path — invokes MongoDB.UnmarshalJSON which calls
//     InitDefaults; this is where any pointer-vs-nil drift would
//     surface).
//  4. Hashing both the original and the round-tripped copy.
//
// The canonical-JSON-of-unstructured implementation must produce the
// same bytes both before and after the round trip, even though the
// typed struct's internal representation may differ (nil pointers
// filled by InitDefaults, empty maps materialised, etc.). The iter-14e
// `json.Marshal(sc.Spec)` could fail this test if any field flipped
// nil-to-empty during InitDefaults.
func TestHashCRSpec_StableAcrossNoOpRoundTrip(t *testing.T) {
	_, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	before := hashCRSpec(sc)

	// Simulate the apiserver round-trip: marshal to JSON, unmarshal
	// into a fresh *MongoDB (UnmarshalJSON → InitDefaults).
	wire, err := json.Marshal(sc)
	require.NoError(t, err)

	roundTripped := &mdbv1.MongoDB{}
	require.NoError(t, json.Unmarshal(wire, roundTripped))

	after := hashCRSpec(roundTripped)

	assert.Equal(t, before, after,
		"iter-14f: CR-spec hash must be stable across a no-op JSON round trip; got before=%s after=%s",
		before, after)
}

// TestHashCRSpec_StableAcrossDefaultsVsExplicit — G'5 iter 14f. Two CRs
// whose wire-side JSON differs only in fields the apiserver fills with
// the same default value must hash identically. Concretely: a CR with
// `Service: ""` must hash the same as a CR whose `Service` field is
// absent from the wire JSON, because both will materialise as
// `Service: ""` after `MongoDB.UnmarshalJSON`'s `InitDefaults`. The
// canonical-JSON-of-unstructured implementation runs after
// InitDefaults (we hash the helper's `r.sc` which has already been
// decoded) so the two paths converge.
//
// This is a tighter check than TestHashCRSpec_StableIgnoresMetadata:
// here the SPEC content differs at the wire level (one has the field,
// the other doesn't), but after the apiserver / typed decoder
// canonicalises defaults, they're observationally identical and must
// agree.
func TestHashCRSpec_StableAcrossDefaultsVsExplicit(t *testing.T) {
	_, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	a := sc.DeepCopy()
	// Force Service to its zero value (explicit). InitDefaults won't
	// change a zero string back to anything.
	a.Spec.Service = ""

	b := sc.DeepCopy()
	// Round-trip b through JSON without the Service key to simulate
	// "field absent on the wire".
	wireB, err := json.Marshal(b)
	require.NoError(t, err)
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(wireB, &raw))
	if spec, ok := raw["spec"].(map[string]interface{}); ok {
		delete(spec, "service")
	}
	stripped, err := json.Marshal(raw)
	require.NoError(t, err)
	roundTripped := &mdbv1.MongoDB{}
	require.NoError(t, json.Unmarshal(stripped, roundTripped))

	assert.Equal(t, hashCRSpec(a), hashCRSpec(roundTripped),
		"iter-14f: CR with explicit-zero-default vs absent-field-with-default must hash identically; got a=%s rt=%s",
		hashCRSpec(a), hashCRSpec(roundTripped))
}

// TestCanonicalJSON_StableAcrossKeyOrderAndNesting — G'5 iter 14f.
// `canonicalJSON` is the hash-stability primitive: two map[string]interface{}
// values that JSON-compare equal must canonicalise to the same bytes
// regardless of insertion order or nesting depth. Without this, the
// CR-spec hash would still be sensitive to typed-struct marshal
// ordering inside unstructured submaps.
func TestCanonicalJSON_StableAcrossKeyOrderAndNesting(t *testing.T) {
	a := map[string]interface{}{
		"version": "7.0.18-ent",
		"shard": map[string]interface{}{
			"clusterSpecList": []interface{}{
				map[string]interface{}{"clusterName": "kind-e2e-cluster-1", "members": float64(2)},
				map[string]interface{}{"clusterName": "kind-e2e-cluster-2", "members": float64(2)},
			},
			"agent": map[string]interface{}{"logLevel": "INFO"},
		},
		"type": "ShardedCluster",
	}
	b := map[string]interface{}{
		// keys in opposite order; nested maps' keys also reordered
		"type": "ShardedCluster",
		"shard": map[string]interface{}{
			"agent":           map[string]interface{}{"logLevel": "INFO"},
			"clusterSpecList": []interface{}{
				// IMPORTANT: array order must be preserved (JSON arrays are ordered).
				// We rebuild each element's map with reversed key order.
				map[string]interface{}{"members": float64(2), "clusterName": "kind-e2e-cluster-1"},
				map[string]interface{}{"members": float64(2), "clusterName": "kind-e2e-cluster-2"},
			},
		},
		"version": "7.0.18-ent",
	}
	canonA, err := canonicalJSON(a)
	require.NoError(t, err)
	canonB, err := canonicalJSON(b)
	require.NoError(t, err)
	assert.Equal(t, string(canonA), string(canonB),
		"canonicalJSON must be byte-stable across key ordering and nested key ordering")

	// Sensitivity check: a real value change must produce different bytes.
	c := map[string]interface{}{
		"version": "7.0.18-ent",
		"shard": map[string]interface{}{
			"clusterSpecList": []interface{}{
				map[string]interface{}{"clusterName": "kind-e2e-cluster-1", "members": float64(3)},
				map[string]interface{}{"clusterName": "kind-e2e-cluster-2", "members": float64(2)},
			},
			"agent": map[string]interface{}{"logLevel": "INFO"},
		},
		"type": "ShardedCluster",
	}
	canonC, err := canonicalJSON(c)
	require.NoError(t, err)
	assert.NotEqual(t, string(canonA), string(canonC),
		"canonicalJSON must change when a leaf value changes")
}

// TestGateOnResourceAgreement_RefreshesHeldLeasesOnPending — G'5 iter 14f.
// When Gate 0 returns Pending due to ResourcesNotAgreed (the genuine
// spec-replication-lag case that the iter-14e e2e surfaced), the gate
// MUST refresh HeartbeatAt on every lease this cluster currently holds
// on the CR. Without the refresh, the leader's stuck-step detector
// revokes the lease after HeartbeatTTL (60s) and the cross-cluster cap=1
// serialisation breaks: a different cluster acquires the same component
// lease concurrently with the parked holder's real work.
//
// Test fixture:
//
//  1. Local cluster ("member-cluster-1") pre-holds two leases on the
//     CR: one on "shard-0", one on "config".
//  2. Configure the fake coordinator's WaitForResourcesAgreed to return
//     ResourcesPending so the gate returns Pending.
//  3. Run gateOnResourceAgreement.
//  4. Assert: ReportProgress was called for BOTH held leases with the
//     local cluster name. The progress report's CRSpecGeneration must
//     match the CR's current generation so the FSM merges into the
//     correct ComponentStatus entry.
//
// Pin: the gate must NOT call ReportProgress for leases held by OTHER
// clusters (the heartbeat is the holder's responsibility).
func TestGateOnResourceAgreement_RefreshesHeldLeasesOnPending(t *testing.T) {
	ctx := context.Background()
	helper, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	sc.Generation = 7 // distinguishable from zero

	c := newFakeCoordinator("member-cluster-1", true)
	// Pre-allocate two own-cluster leases and one other-cluster lease.
	c.setHolder("shard-0", "member-cluster-1", "member-cluster-1")
	c.setHolder("config", "member-cluster-1", "member-cluster-1")
	c.setHolder("shard-1", "member-cluster-2", "member-cluster-2")

	// Drive Gate 0 to Pending.
	c.resourcesAgreedFn = func(crKey coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string) {
		return coordination.ResourcesPending,
			"Resource " + CRSpecResourceKind + "/" + sc.Namespace + "/" + sc.Name +
				" hash mismatch: replication still settling."
	}
	helper.SetCoordinator(c)

	gateStatus := helper.gateOnResourceAgreement(ctx, zap.S())
	require.False(t, gateStatus.IsOK(), "Gate 0 must remain Pending in this fixture")

	// Inspect the recorded progress reports.
	c.mu.Lock()
	defer c.mu.Unlock()

	heldComponents := map[string]bool{}
	otherReports := []scopeProgress{}
	for _, p := range c.progressReports {
		if p.Cluster == "member-cluster-1" {
			heldComponents[p.Component] = true
			assert.Equal(t, int64(7), p.Progress.CRSpecGeneration,
				"refresh report must carry the local CR's current generation; got %d for component %s",
				p.Progress.CRSpecGeneration, p.Component)
		} else {
			otherReports = append(otherReports, p)
		}
	}
	assert.True(t, heldComponents["shard-0"],
		"iter-14f: Gate 0 Pending MUST refresh heartbeat on the held shard-0 lease (got reports=%v)",
		c.progressReports)
	assert.True(t, heldComponents["config"],
		"iter-14f: Gate 0 Pending MUST refresh heartbeat on the held config lease (got reports=%v)",
		c.progressReports)
	assert.Empty(t, otherReports,
		"iter-14f: keep-alive must NOT touch other clusters' leases; got %v", otherReports)
}

// TestGateOnResourceAgreement_NoRefreshWhenNoHeldLeases — G'5 iter 14f.
// When the local cluster holds zero leases for the CR (e.g. very first
// reconcile, or post-MarkReady-Release window), Gate 0 Pending should
// NOT emit any ReportProgress. This pins the no-op path so the refresh
// doesn't pollute the FSM with empty StatusReports.
func TestGateOnResourceAgreement_NoRefreshWhenNoHeldLeases(t *testing.T) {
	ctx := context.Background()
	helper, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	c := newFakeCoordinator("member-cluster-1", true)
	c.resourcesAgreedFn = func(crKey coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string) {
		return coordination.ResourcesPending,
			"Resource " + CRSpecResourceKind + "/" + sc.Namespace + "/" + sc.Name + " disagreement."
	}
	helper.SetCoordinator(c)

	gateStatus := helper.gateOnResourceAgreement(ctx, zap.S())
	require.False(t, gateStatus.IsOK())

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.progressReports,
		"iter-14f: with no held leases, keep-alive must be a no-op; got %v", c.progressReports)
}

// TestGateOnResourceAgreement_NoRefreshWhenAgreed — G'5 iter 14f.
// On the happy path (ResourcesAgreed → workflow.OK), the gate must NOT
// emit any keep-alive ReportProgress: the downstream per-component
// distReportInflightProgress / distMarkReadyAndRelease calls will do
// the real status reporting with accurate replica counts. Emitting a
// zero-count refresh here would dilute the leader's stuck-step
// signature.
func TestGateOnResourceAgreement_NoRefreshWhenAgreed(t *testing.T) {
	ctx := context.Background()
	helper, _, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	c := newFakeCoordinator("member-cluster-1", true)
	// Pre-hold a lease so the test would distinguish refresh-on-OK from
	// refresh-on-Pending if the gate had a bug.
	c.setHolder("shard-0", "member-cluster-1", "member-cluster-1")
	helper.SetCoordinator(c)

	gateStatus := helper.gateOnResourceAgreement(ctx, zap.S())
	require.True(t, gateStatus.IsOK(), "agreed-path gate must return OK")

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.progressReports,
		"iter-14f: agreed-path gate must NOT emit keep-alive ReportProgress; got %v",
		c.progressReports)
}

// TestGetLeasesHeldBy_FakeAndPerClusterView — sanity-check the new
// coordinator-interface method on both test doubles. The fake's holder
// map is the source of truth; perClusterCoordinatorView forwards.
func TestGetLeasesHeldBy_FakeAndPerClusterView(t *testing.T) {
	c := newFakeCoordinator("member-cluster-1", true)
	c.setHolder("shard-0", "member-cluster-1", "member-cluster-1")
	c.setHolder("config", "member-cluster-1", "member-cluster-1")
	c.setHolder("shard-1", "member-cluster-2", "member-cluster-2")

	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}
	held := c.GetLeasesHeldBy(crKey, "member-cluster-1")
	assert.ElementsMatch(t, []string{"shard-0", "config"}, held,
		"GetLeasesHeldBy must return only own-cluster leases")

	heldOther := c.GetLeasesHeldBy(crKey, "member-cluster-2")
	assert.ElementsMatch(t, []string{"shard-1"}, heldOther)

	heldNone := c.GetLeasesHeldBy(crKey, "member-cluster-3")
	assert.Empty(t, heldNone)

	// perClusterCoordinatorView forwards transparently.
	view := &perClusterCoordinatorView{shared: c, localCluster: "member-cluster-1"}
	viewHeld := view.GetLeasesHeldBy(crKey, "member-cluster-1")
	assert.ElementsMatch(t, []string{"shard-0", "config"}, viewHeld)
}

// TestHashCRSpec_DiagnosticOnConversionFailure — G'5 iter 14f. If the
// CR is nil, the hash returns a sentinel that won't match any real
// spec; the gate stays Pending (correct conservative behaviour). This
// pins the contract that ERR/MISSING hashes are stable, distinguishable
// from real hashes, and never silently identical to a wire-side spec.
func TestHashCRSpec_DiagnosticOnConversionFailure(t *testing.T) {
	// Nil receiver → sentinel.
	got := hashCRSpec(nil)
	assert.Equal(t, "MISSING:MongoDB", got,
		"nil CR must hash to the MISSING sentinel")

	// A real CR must produce a hex sha256 (64 chars).
	_, sc, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	realHash := hashCRSpec(sc)
	assert.Len(t, realHash, 64, "real-CR hash must be a hex sha256")
	assert.NotEqual(t, "MISSING:MongoDB", realHash)
}

