package status

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LicenseReady reports the license Ops Manager holds, whoever installed it. LicenseManaged
// reports whether the operator manages the license from .spec.license.secretRef.
const (
	ConditionLicenseReady   = "LicenseReady"
	ConditionLicenseManaged = "LicenseManaged"
)

// Reasons of LicenseReady. Unknown is used while Ops Manager has not reported a license state, or
// reports one the operator does not recognize.
const (
	ReasonLicenseActive   = "Active"
	ReasonLicenseInactive = "Inactive"
	ReasonLicenseUnknown  = "Unknown"
)

// Reasons of LicenseManaged. Rejected means Ops Manager refused the license from secretRef as
// outdated, Failed means the license reconcile failed.
const (
	ReasonLicenseManaged     = "Managed"
	ReasonLicenseNotProvided = "NotProvided"
	ReasonLicenseRejected    = "Rejected"
	ReasonLicenseFailed      = "Failed"
)

func LicenseReadyCondition(status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return licenseCondition(ConditionLicenseReady, status, reason, message)
}

func LicenseManagedCondition(managed bool, reason, message string) metav1.Condition {
	st := metav1.ConditionFalse
	if managed {
		st = metav1.ConditionTrue
	}
	return licenseCondition(ConditionLicenseManaged, st, reason, message)
}

func licenseCondition(conditionType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}
