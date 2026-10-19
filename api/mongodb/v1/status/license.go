package status

import (
	"k8s.io/apimachinery/pkg/api/meta"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionOpsManagerLicenseReady = "OpsManagerLicenseReady"
	ConditionLicenseReconciled      = "LicenseReconciled"

	ReasonLicenseEnforced        = "LicenseEnforced"
	ReasonLicenseReady           = "LicenseReady"
	ReasonLicenseReconciled      = "LicenseReconciled"
	ReasonIdentityRotationFailed = "IdentityRotationFailed"
	ReasonLicenseRecomputeFailed = "LicenseRecomputeFailed"
	ReasonLicenseInstallFailed   = "LicenseInstallFailed"
	ReasonLicenseReleaseFailed   = "LicenseReleaseFailed"
	ReasonLicenseReceiptFailed   = "LicenseReceiptFailed"
	ReasonLicenseSecretFailed    = "LicenseSecretFailed"
	ReasonLicenseArtifactInvalid = "LicenseArtifactInvalid"
	ReasonLicenseObserveFailed   = "LicenseObserveFailed"
)

func LicenseCondition(ready bool, reason, message string) metav1.Condition {
	st := metav1.ConditionFalse
	if ready {
		st = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type:               ConditionOpsManagerLicenseReady,
		Status:             st,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}

func LicenseEnforcedCondition(message string) metav1.Condition {
	return LicenseCondition(false, ReasonLicenseEnforced, message)
}

func OmLicenseReconciledCondition(reconciled bool, reason, message string) metav1.Condition {
	st := metav1.ConditionFalse
	if reconciled {
		st = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type:               ConditionLicenseReconciled,
		Status:             st,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}

func ClearLicenseBlockIfPresent(conditions *[]metav1.Condition, generation int64) {
	if conditions == nil {
		return
	}
	ready := meta.FindStatusCondition(*conditions, ConditionOpsManagerLicenseReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonLicenseEnforced {
		return
	}
	c := LicenseCondition(true, ReasonLicenseReady, "")
	c.ObservedGeneration = generation
	_ = meta.SetStatusCondition(conditions, c)
}
