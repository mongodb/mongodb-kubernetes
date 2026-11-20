package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLicenseActiveCondition_CarriesReasonAndMessage(t *testing.T) {
	var conditions []metav1.Condition
	c := LicenseActiveCondition(metav1.ConditionFalse, ReasonLicenseInactive, "license state is grace")
	c.ObservedGeneration = 3
	_ = meta.SetStatusCondition(&conditions, c)

	active := meta.FindStatusCondition(conditions, ConditionLicenseActive)
	require.NotNil(t, active)
	assert.Equal(t, metav1.ConditionFalse, active.Status)
	assert.Equal(t, ReasonLicenseInactive, active.Reason)
	assert.Equal(t, int64(3), active.ObservedGeneration)
	assert.Equal(t, "license state is grace", active.Message)
}

func TestLicenseActiveCondition_UnknownIsUnknownNotFalse(t *testing.T) {
	var conditions []metav1.Condition
	c := LicenseActiveCondition(metav1.ConditionUnknown, ReasonLicenseUnknown, "no state yet")
	_ = meta.SetStatusCondition(&conditions, c)

	active := meta.FindStatusCondition(conditions, ConditionLicenseActive)
	require.NotNil(t, active)
	assert.Equal(t, metav1.ConditionUnknown, active.Status)
	assert.Equal(t, ReasonLicenseUnknown, active.Reason)
}
