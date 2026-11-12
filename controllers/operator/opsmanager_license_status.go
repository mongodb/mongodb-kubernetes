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

// reportLicenseReconciled returns LicenseReconciled for what happened to the license in
// secretRef. A requested artifact Ops Manager does not hold was rejected as outdated by its anti
// rollback check. The license is not applied then, but retrying cannot help until the Secret holds
// a newer one, so the reconcile is OK.
func (h *LicenseReconcilerHelper) reportLicenseReconciled(requestedID, releasedID string) workflow.Status {
	if requestedID == "" && releasedID != "" {
		return licenseReconciled(true, status.ReasonLicenseNotRequested, fmt.Sprintf("The operator released license %s after secretRef was removed. %s reports the license Ops Manager holds.", releasedID, status.ConditionOpsManagerLicenseReady))
	}
	if requestedID == "" {
		return licenseReconciled(true, status.ReasonLicenseNotRequested, fmt.Sprintf("secretRef is not set, so the operator installs no license. %s reports the license Ops Manager holds.", status.ConditionOpsManagerLicenseReady))
	}
	if h.observed.HoldsArtifact(requestedID) {
		return licenseReconciled(true, status.ReasonLicenseInstalled, fmt.Sprintf("Ops Manager holds license %s from secretRef.", requestedID))
	}
	message := fmt.Sprintf("Ops Manager rejected license %s, a license this new or newer was already installed", requestedID)
	if held, ok := h.observed.HeldArtifact(); ok {
		message += fmt.Sprintf(". It holds license %s", held.ArtifactID)
	}
	return licenseReconciled(false, status.ReasonLicenseOutdated, message+". Bring a newer artifact in the license Secret.")
}

func licenseReconciled(reconciled bool, reason, message string) workflow.Status {
	return workflow.OK().WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseReconciledCondition(reconciled, reason, message)))
}

func licenseFailed(err error) workflow.Status {
	return workflow.Failed(err).WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseReconciledCondition(false, status.ReasonLicenseReconcileFailed, err.Error())))
}
