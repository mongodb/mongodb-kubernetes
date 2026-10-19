package mdb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestMongoDB_UpdateStatus_AppliesLicenseOption(t *testing.T) {
	m := &MongoDB{
		ObjectMeta: metav1.ObjectMeta{Generation: 4},
		Spec: MongoDbSpec{
			DbCommonSpec: DbCommonSpec{ResourceType: ReplicaSet},
		},
	}
	m.UpdateStatus(status.PhaseFailed, status.NewLicenseStatusOptionWithCondition(status.LicenseEnforcedCondition("blocked")))
	ready := meta.FindStatusCondition(m.Status.Conditions, status.ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, status.ReasonLicenseEnforced, ready.Reason)
	assert.Equal(t, int64(4), ready.ObservedGeneration)
}

func TestMongoDB_UpdateStatus_RunningClearsLicenseEnforced(t *testing.T) {
	m := &MongoDB{
		ObjectMeta: metav1.ObjectMeta{Generation: 5},
		Spec: MongoDbSpec{
			DbCommonSpec: DbCommonSpec{ResourceType: ReplicaSet, Version: "6.0.0"},
		},
	}
	meta.SetStatusCondition(&m.Status.Conditions, status.LicenseEnforcedCondition("blocked"))
	m.UpdateStatus(status.PhaseRunning)
	ready := meta.FindStatusCondition(m.Status.Conditions, status.ConditionOpsManagerLicenseReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, status.ReasonLicenseReady, ready.Reason)
}
