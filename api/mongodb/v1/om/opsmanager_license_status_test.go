package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestOpsManagerStatus_LicenseManagedCondition(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.UpdateStatus(status.PhaseFailed, status.NewOMPartOption(status.OpsManager),
		status.NewLicenseStatusOptionWithCondition(
			status.LicenseManagedCondition(false, status.ReasonLicenseNotManaged, "rotation failed"),
		))

	cond := meta.FindStatusCondition(om.Status.OpsManagerStatus.Conditions, status.ConditionLicenseManaged)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, status.ReasonLicenseNotManaged, cond.Reason)
	assert.Equal(t, "rotation failed", cond.Message)
	assert.Equal(t, om.GetGeneration(), cond.ObservedGeneration)

	om.UpdateStatus(status.PhaseRunning, status.NewOMPartOption(status.OpsManager),
		status.NewLicenseStatusOptionWithCondition(
			status.LicenseManagedCondition(false, status.ReasonLicenseNotProvided, ""),
		))

	cond = meta.FindStatusCondition(om.Status.OpsManagerStatus.Conditions, status.ConditionLicenseManaged)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, status.ReasonLicenseNotProvided, cond.Reason)
}
