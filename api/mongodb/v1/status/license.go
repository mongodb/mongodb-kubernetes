package status

import (
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OpsManagerLicenseReady reports the license Ops Manager holds, whoever installed it. LicenseReconciled
// reports whether the operator applied spec.license.
const (
	ConditionOpsManagerLicenseReady = "OpsManagerLicenseReady"
	ConditionLicenseReconciled      = "LicenseReconciled"
)

// Reasons of OpsManagerLicenseReady. Ready, Grace, Unlicensed and Expired follow the Ops Manager
// license state. StateUnknown covers no state reported yet. Enforced marks a change Ops Manager
// rejected, set only on MongoDB and MongoDBMultiCluster.
const (
	ReasonLicenseReady        = "LicenseReady"
	ReasonLicenseGrace        = "LicenseGrace"
	ReasonLicenseUnlicensed   = "LicenseUnlicensed"
	ReasonLicenseExpired      = "LicenseExpired"
	ReasonLicenseEnforced     = "LicenseEnforced"
	ReasonLicenseStateUnknown = "LicenseStateUnknown"
)

// Reasons of LicenseReconciled. The message carries the detail of a failure.
const (
	ReasonLicenseInstalled       = "LicenseInstalled"
	ReasonLicenseNotRequested    = "LicenseNotRequested"
	ReasonLicenseOutdated        = "LicenseOutdated"
	ReasonLicenseReconcileFailed = "LicenseReconcileFailed"
)

func OpsManagerLicenseReadyCondition(ready bool, reason, message string) metav1.Condition {
	return licenseCondition(ConditionOpsManagerLicenseReady, ready, reason, message)
}

// LicenseEnforcedCondition and ClearLicenseBlockIfPresent serve the enforcement block flow, wired
// in by upcoming AC change mutations.
func LicenseEnforcedCondition(message string) metav1.Condition {
	return OpsManagerLicenseReadyCondition(false, ReasonLicenseEnforced, message)
}

func LicenseReconciledCondition(reconciled bool, reason, message string) metav1.Condition {
	return licenseCondition(ConditionLicenseReconciled, reconciled, reason, message)
}

func licenseCondition(conditionType string, ok bool, reason, message string) metav1.Condition {
	st := metav1.ConditionFalse
	if ok {
		st = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type:               conditionType,
		Status:             st,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}

// ClearLicenseBlockIfPresent removes an enforcement block once it no longer applies. The condition
// is removed rather than set to ready, since the resource never reads the license Ops Manager holds.
func ClearLicenseBlockIfPresent(conditions *[]metav1.Condition) {
	if conditions == nil {
		return
	}
	ready := meta.FindStatusCondition(*conditions, ConditionOpsManagerLicenseReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonLicenseEnforced {
		return
	}
	meta.RemoveStatusCondition(conditions, ConditionOpsManagerLicenseReady)
}
