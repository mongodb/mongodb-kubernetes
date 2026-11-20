package status

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LicenseActive reports the license Ops Manager holds, whoever installed it. OperatorManagedLicense
// reports whether the operator manages the license from .spec.license.secretRef.
const (
	ConditionLicenseActive          = "LicenseActive"
	ConditionOperatorManagedLicense = "OperatorManagedLicense"
)

// Reasons of LicenseActive. Unknown is used while Ops Manager has not reported a license state, or
// reports one the operator does not recognize.
const (
	ReasonLicenseActive   = "Active"
	ReasonLicenseInactive = "Inactive"
	ReasonLicenseUnknown  = "Unknown"
)

// Reasons of OperatorManagedLicense. Rejected means Ops Manager refused the license from secretRef as
// outdated, Failed means the license reconcile failed.
const (
	ReasonLicenseManaged     = "Managed"
	ReasonLicenseNotProvided = "NotProvided"
	ReasonLicenseRejected    = "Rejected"
	ReasonLicenseFailed      = "Failed"
)

func LicenseActiveCondition(status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return licenseCondition(ConditionLicenseActive, status, reason, message)
}

func OperatorManagedLicenseCondition(managed bool, reason, message string) metav1.Condition {
	st := metav1.ConditionFalse
	if managed {
		st = metav1.ConditionTrue
	}
	return licenseCondition(ConditionOperatorManagedLicense, st, reason, message)
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
