package status

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OpsManagerLicenseReady reports the license Ops Manager holds, whoever installed it. LicenseReconciled
// reports whether the operator applied spec.license.
const (
	ConditionOpsManagerLicenseReady = "OpsManagerLicenseReady"
	ConditionLicenseReconciled      = "LicenseReconciled"
)

// Reasons of OpsManagerLicenseReady. Ready means the license Ops Manager holds is licensed.
// NotReady covers every other license state (grace, unlicensed, expired); the message names
// the state. StateUnknown covers no state reported yet.
const (
	ReasonLicenseReady        = "LicenseReady"
	ReasonLicenseNotReady     = "LicenseNotReady"
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
