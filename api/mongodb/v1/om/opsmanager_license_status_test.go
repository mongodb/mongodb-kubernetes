package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestOpsManagerStatus_LicenseReconciledCondition(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.UpdateStatus(status.PhaseFailed, status.NewOMPartOption(status.OpsManager),
		status.NewOmLicenseReconciledStatusOptionWithCondition(
			status.OmLicenseReconciledCondition(false, status.ReasonIdentityRotationFailed, "rotation failed"),
		))

	cond := meta.FindStatusCondition(om.Status.OpsManagerStatus.Conditions, status.ConditionLicenseReconciled)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, status.ReasonIdentityRotationFailed, cond.Reason)
	assert.Equal(t, "rotation failed", cond.Message)
	assert.Equal(t, om.GetGeneration(), cond.ObservedGeneration)

	om.UpdateStatus(status.PhaseRunning, status.NewOMPartOption(status.OpsManager),
		status.NewOmLicenseReconciledStatusOptionWithCondition(
			status.OmLicenseReconciledCondition(true, status.ReasonLicenseReconciled, ""),
		))

	cond = meta.FindStatusCondition(om.Status.OpsManagerStatus.Conditions, status.ConditionLicenseReconciled)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, status.ReasonLicenseReconciled, cond.Reason)
}

func TestOpsManagerLicenseStatus_ArtifactQuestions(t *testing.T) {
	view := OpsManagerLicenseStatus{
		Licenses: []OpsManagerLicenseArtifactStatus{
			{ArtifactID: "held", Status: "ACTIVE"},
			{ArtifactID: "unstated"},
			{ArtifactID: "released", Status: LicenseArtifactStatusReleased},
		},
	}

	assert.True(t, view.HoldsArtifact("held"))
	assert.True(t, view.HoldsArtifact("unstated"))
	assert.False(t, view.HoldsArtifact("released"))
	assert.False(t, view.HoldsArtifact("missing"))

	assert.True(t, view.ListsArtifact("released"))
	assert.False(t, view.ListsArtifact("missing"))
	assert.Equal(t, []string{"held", "unstated"}, view.HeldArtifactIDs())
	assert.Equal(t, []string{"held", "unstated", "released"}, view.ArtifactIDs())
}
