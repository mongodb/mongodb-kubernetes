package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLicenseReadyCondition_CarriesReasonAndMessage(t *testing.T) {
	var conditions []metav1.Condition
	c := LicenseReadyCondition(metav1.ConditionFalse, ReasonLicenseNotReady, "license state is grace")
	c.ObservedGeneration = 3
	_ = meta.SetStatusCondition(&conditions, c)

	ready := meta.FindStatusCondition(conditions, ConditionLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, ReasonLicenseNotReady, ready.Reason)
	assert.Equal(t, int64(3), ready.ObservedGeneration)
	assert.Equal(t, "license state is grace", ready.Message)
}

func TestLicenseReadyCondition_StateUnknownIsUnknownNotFalse(t *testing.T) {
	var conditions []metav1.Condition
	c := LicenseReadyCondition(metav1.ConditionUnknown, ReasonLicenseStateUnknown, "no state yet")
	_ = meta.SetStatusCondition(&conditions, c)

	ready := meta.FindStatusCondition(conditions, ConditionLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionUnknown, ready.Status)
	assert.Equal(t, ReasonLicenseStateUnknown, ready.Reason)
}
