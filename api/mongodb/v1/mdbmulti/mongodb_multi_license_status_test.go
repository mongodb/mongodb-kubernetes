package mdbmulti

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestMongoDBMultiCluster_UpdateStatus_AppliesLicenseOption(t *testing.T) {
	m := &MongoDBMultiCluster{ObjectMeta: metav1.ObjectMeta{Generation: 3}}
	m.UpdateStatus(status.PhaseFailed, status.NewLicenseStatusOptionWithCondition(status.LicenseEnforcedCondition("blocked")))
	ready := meta.FindStatusCondition(m.Status.Conditions, status.ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, int64(3), ready.ObservedGeneration)
}

func TestMongoDBMultiCluster_UpdateStatus_RunningClearsLicenseEnforced(t *testing.T) {
	m := &MongoDBMultiCluster{ObjectMeta: metav1.ObjectMeta{Generation: 8}}
	meta.SetStatusCondition(&m.Status.Conditions, status.LicenseEnforcedCondition("blocked"))
	m.UpdateStatus(status.PhaseRunning)
	ready := meta.FindStatusCondition(m.Status.Conditions, status.ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, status.ReasonLicenseReady, ready.Reason)
}
