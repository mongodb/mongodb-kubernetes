package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLicenseCondition_SetsAndClearsEnforced(t *testing.T) {
	var conditions []metav1.Condition
	c := LicenseEnforcedCondition("not licensed")
	c.ObservedGeneration = 3
	_ = meta.SetStatusCondition(&conditions, c)

	ready := meta.FindStatusCondition(conditions, ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, ReasonLicenseEnforced, ready.Reason)
	assert.Equal(t, int64(3), ready.ObservedGeneration)
	assert.Equal(t, "not licensed", ready.Message)

	ClearLicenseBlockIfPresent(&conditions, 4)
	ready = meta.FindStatusCondition(conditions, ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, ReasonLicenseReady, ready.Reason)
}

func TestClearLicenseBlockIfPresent_LeavesOtherFalseReasons(t *testing.T) {
	var conditions []metav1.Condition
	_ = meta.SetStatusCondition(&conditions, LicenseCondition(false, "SomeOtherReason", "leave me"))
	ClearLicenseBlockIfPresent(&conditions, 2)
	ready := meta.FindStatusCondition(conditions, ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, "SomeOtherReason", ready.Reason)
}
