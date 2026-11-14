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

// Reasons of LicenseReady. StateUnknown has status Unknown.
const (
	ReasonLicenseReady        = "LicenseReady"
	ReasonLicenseNotReady     = "LicenseNotReady"
	ReasonLicenseStateUnknown = "LicenseStateUnknown"
)

// Reasons of LicenseManaged. NotManaged covers a license Ops Manager rejected as outdated and a
// failed license reconcile, and the message names which.
const (
	ReasonLicenseManaged     = "LicenseManaged"
	ReasonLicenseNotProvided = "LicenseNotProvided"
	ReasonLicenseNotManaged  = "LicenseNotManaged"
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
