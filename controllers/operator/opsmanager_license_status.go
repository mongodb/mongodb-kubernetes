package operator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
)

const (
	licenseStateLicensed   = "LICENSED"
	licenseStateGrace      = "GRACE"
	licenseStateUnlicensed = "UNLICENSED"
	licenseStateExpired    = "EXPIRED"
)

// reportLicenseReady sets LicenseReady to the license Ops Manager holds, whoever installed it.
func (h *LicenseReconcilerHelper) reportLicenseReady() {
	if h.observed == nil {
		return
	}
	state := ""
	if h.observed.Enforcement != nil {
		state = h.observed.Enforcement.EffectiveState
	}
	license := "no license"
	if held, ok := h.observed.HeldArtifact(); ok {
		license = "license " + held.ArtifactID
		if held.TermEnd != "" {
			license += ", valid until " + held.TermEnd
		}
	}

	ready, reason := metav1.ConditionTrue, status.ReasonLicenseReady
	stateMessage := fmt.Sprintf("License state is %s.", state)
	if state == "" {
		// A fresh Ops Manager reports no license state until its first recompute. The operator has
		// not checked the license yet, so the condition is Unknown rather than False.
		ready, reason, stateMessage = metav1.ConditionUnknown, status.ReasonLicenseStateUnknown, "Ops Manager has not reported a license state yet."
	} else if state != licenseStateLicensed {
		ready, reason = licenseStateCondition(state)
	}

	message := fmt.Sprintf("%s Ops Manager holds %s.", stateMessage, license)
	if h.holderID != "" {
		message += fmt.Sprintf(" Holder ID %s.", h.holderID)
	}

	condition := status.LicenseReadyCondition(ready, reason, message)
	condition.ObservedGeneration = h.opsManager.GetGeneration()
	_ = meta.SetStatusCondition(&h.opsManager.Status.OpsManagerStatus.Conditions, condition)
}

// licenseStateCondition returns the status and reason of a license state other than LICENSED. The
// message names the actual state. A state the operator does not recognize is Unknown rather than
// False, since the operator cannot tell whether that state means the license is ready.
func licenseStateCondition(state string) (metav1.ConditionStatus, string) {
	if state == licenseStateGrace || state == licenseStateUnlicensed || state == licenseStateExpired {
		return metav1.ConditionFalse, status.ReasonLicenseNotReady
	}
	return metav1.ConditionUnknown, status.ReasonLicenseStateUnknown
}

// reportLicenseManaged returns LicenseManaged for the license in secretRef. Every install failure
// except an outdated artifact fails the reconcile before this point, so a requested artifact Ops
// Manager does not hold was rejected as outdated. Retrying cannot help until the Secret holds a
// newer one, so the reconcile is OK.
func (h *LicenseReconcilerHelper) reportLicenseManaged(requestedID string) workflow.Status {
	if requestedID == "" {
		return licenseManaged(false, status.ReasonLicenseNotProvided, ".spec.license.secretRef is not set, so the operator doesn't manage the license.")
	}
	if h.observed.HoldsArtifact(requestedID) {
		return licenseManaged(true, status.ReasonLicenseManaged, fmt.Sprintf("License %s from .spec.license.secretRef is managed by the operator.", requestedID))
	}
	return licenseManaged(false, status.ReasonLicenseNotManaged, fmt.Sprintf("License %s from .spec.license.secretRef is not installed, because Ops Manager already holds a license this new or newer that the operator doesn't manage.", requestedID))
}

func licenseManaged(managed bool, reason, message string) workflow.Status {
	return workflow.OK().WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseManagedCondition(managed, reason, message)))
}

func licenseFailed(err error) workflow.Status {
	return workflow.Failed(err).WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseManagedCondition(false, status.ReasonLicenseNotManaged, err.Error())))
}
