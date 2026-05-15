package operator

// F12b — reconcile-top resource-agreement gate tests.
//
// These tests directly drive the gate (gateOnResourceAgreement) so we can
// assert: nil coordinator → no-op; coordinator + agreed resources → OK;
// coordinator + disagreement → Pending with the diagnostic visible.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"

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

