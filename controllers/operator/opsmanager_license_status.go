package operator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
)

const (
	licenseStateLicensed   = "LICENSED"
	licenseStateGrace      = "GRACE"
	licenseStateUnlicensed = "UNLICENSED"
	licenseStateExpired    = "EXPIRED"
)

// reportOpsManagerLicenseReady sets OpsManagerLicenseReady to the license Ops Manager holds,
// whoever installed it.
func (h *LicenseReconcilerHelper) reportOpsManagerLicenseReady() {
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

	ready, reason := true, status.ReasonLicenseReady
	stateMessage := fmt.Sprintf("License state is %s.", state)
	if state == "" {
		// A fresh Ops Manager reports no license state until its first recompute.
		ready, reason, stateMessage = false, status.ReasonLicenseStateUnknown, "Ops Manager has not reported a license state yet."
	} else if state != licenseStateLicensed {
		ready, reason = false, licenseStateReason(state)
	}

	message := fmt.Sprintf("%s Ops Manager holds %s.", stateMessage, license)
	if h.holderID != "" {
		message += fmt.Sprintf(" Holder ID %s.", h.holderID)
	}

	condition := status.OpsManagerLicenseReadyCondition(ready, reason, message)
	condition.ObservedGeneration = h.opsManager.GetGeneration()
	_ = meta.SetStatusCondition(&h.opsManager.Status.OpsManagerStatus.Conditions, condition)
}

// licenseStateReason returns the reason of a license state other than LICENSED. The message
// names the actual state; a state the operator does not recognize becomes LicenseStateUnknown.
func licenseStateReason(state string) string {
	if state == licenseStateGrace || state == licenseStateUnlicensed || state == licenseStateExpired {
		return status.ReasonLicenseNotReady
	}
	return status.ReasonLicenseStateUnknown
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
	return licenseManaged(false, status.ReasonLicenseNotManaged, fmt.Sprintf("License %s from .spec.license.secretRef was rejected because a license this new or newer was already installed and not managed by operator.", requestedID))
}

func licenseManaged(managed bool, reason, message string) workflow.Status {
	return workflow.OK().WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseManagedCondition(managed, reason, message)))
}

func licenseFailed(err error) workflow.Status {
	return workflow.Failed(err).WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseManagedCondition(false, status.ReasonLicenseReconcileFailed, err.Error())))
}
