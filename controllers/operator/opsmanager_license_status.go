package operator

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
)

const licenseStateLicensed = "LICENSED"

// setLicenseReadyCondition reports the license Ops Manager holds, whoever installed it.
func (h *LicenseReconcilerHelper) setLicenseReadyCondition() {
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
	message := fmt.Sprintf("License state is %s. Ops Manager holds %s.", state, license)

	ready, reason := true, status.ReasonLicenseReady
	if state != licenseStateLicensed {
		ready, reason = false, licenseStateReason(state)
	}

	condition := status.OpsManagerLicenseReadyCondition(ready, reason, message)
	condition.ObservedGeneration = h.opsManager.GetGeneration()
	_ = meta.SetStatusCondition(&h.opsManager.Status.OpsManagerStatus.Conditions, condition)
}

// licenseStateReason turns a license state like GRACE into a reason like LicenseGrace.
func licenseStateReason(state string) string {
	reason := "License"
	for _, word := range strings.Split(strings.ToLower(state), "_") {
		if word != "" {
			reason += strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return reason
}

// licenseReconciledStatus reports what happened to the license in secretRef.
func (h *LicenseReconcilerHelper) licenseReconciledStatus(requestedID string) workflow.Status {
	if requestedID == "" {
		return licenseReconciled(status.ReasonLicenseReconciled, "")
	}
	if h.observed.HoldsArtifact(requestedID) {
		return licenseReconciled(status.ReasonLicenseInstalled, fmt.Sprintf("Ops Manager holds license %s from secretRef.", requestedID))
	}
	return licenseReconciled(status.ReasonLicenseReconciled, "")
}

func licenseReconciled(reason, message string) workflow.Status {
	return workflow.OK().WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseReconciledCondition(true, reason, message)))
}

func licenseFailed(err error, reason string) workflow.Status {
	return workflow.Failed(err).WithAdditionalOptions(status.NewLicenseStatusOptionWithCondition(status.LicenseReconciledCondition(false, reason, err.Error())))
}
