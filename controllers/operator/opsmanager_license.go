package operator

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func (r *OpsManagerReconciler) ensureOpsManagerLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) workflow.Status {
	if st := r.observeLicense(ctx, opsManager, omAdmin); !st.IsOK() {
		return st
	}

	_, licenseStatus := r.reconcileLicenseIntent(ctx, opsManager, omAdmin, log)
	if !licenseStatus.IsOK() {
		return licenseStatus
	}

	return licenseReconciled()
}

func (r *OpsManagerReconciler) reconcileLicenseIntent(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) (string, workflow.Status) {
	if opsManager.Spec.License.InstallRequested() {
		return r.installLicenseFromSecret(ctx, opsManager, omAdmin, log)
	}
	return "", workflow.OK()
}

func (r *OpsManagerReconciler) observeLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin) workflow.Status {
	installedID := opsManager.GetInstalledArtifactID()
	state, err := omAdmin.ReadLicenseState(ctx)
	if err != nil {
		// Older Ops Manager versions without the license API are fine when nothing was installed.
		if installedID == "" && apierror.NewNonNil(err).IsNotFound() {
			return workflow.OK()
		}
		return licenseFailed(err, status.ReasonLicenseObserveFailed)
	}

	opsManager.SetLicenseStatusFromState(state)
	if installedID != "" && !state.HoldsArtifact(installedID) && !opsManager.LicenseReleaseDueToRemovedInstallIntent() {
		opsManager.AddOpsManagerWarningIfNotExists("Ops Manager no longer holds the license last installed by the operator")
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) installLicenseFromSecret(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) (string, workflow.Status) {
	rawArtifact, artifactID, st := r.readLicenseArtifact(ctx, opsManager, opsManager.Spec.License.LicenseSecretRef.Name, status.ReasonLicenseSecretFailed)
	if !st.IsOK() {
		return "", st
	}

	if opsManager.GetInstalledArtifactID() == artifactID {
		return "", workflow.OK()
	}

	observed := opsManager.GetLicenseStatus()
	if observed.HoldsArtifact(artifactID) {
		opsManager.SetInstalledArtifactID(artifactID)
		return "", workflow.OK()
	}

	state, err := omAdmin.InstallLicense(ctx, json.RawMessage(rawArtifact))
	if err != nil {
		msg := "failed to install Ops Manager license %s: %w"
		if apierror.NewNonNil(err).IsConflict() {
			msg = "Ops Manager rejected license %s from the license Secret: %w"
		}
		return "", licenseFailed(xerrors.Errorf(msg, artifactID, err), status.ReasonLicenseInstallFailed)
	}
	opsManager.SetLicenseStatusFromState(state)
	opsManager.SetInstalledArtifactID(artifactID)
	log.Info("Installed Ops Manager license from secret")

	// The install replaced the license Ops Manager held before.
	for _, heldBefore := range observed.HeldArtifactIDs() {
		if heldBefore != artifactID && !state.HoldsArtifact(heldBefore) {
			return heldBefore, workflow.OK()
		}
	}
	return "", workflow.OK()
}

func (r *OpsManagerReconciler) readLicenseArtifact(ctx context.Context, opsManager *omv1.MongoDBOpsManager, secretName, reason string) (rawArtifact, artifactID string, st workflow.Status) {
	rawArtifact, err := r.ReadSecretKey(ctx, kube.ObjectKey(opsManager.Namespace, secretName), r.VaultClient.OperatorSecretPathOrEmpty(), util.DefaultOpsManagerLicenseKey)
	if secret.SecretNotExist(err) {
		return "", "", licenseFailed(xerrors.Errorf("license secret %s/%s does not exist", opsManager.Namespace, secretName), reason)
	}
	if err != nil {
		return "", "", licenseFailed(err, reason)
	}
	if rawArtifact == "" {
		return "", "", licenseFailed(xerrors.Errorf("license secret %s/%s is missing key %q", opsManager.Namespace, secretName, util.DefaultOpsManagerLicenseKey), reason)
	}
	artifactID, err = api.ArtifactIDFromRaw(rawArtifact)
	if err != nil {
		return "", "", licenseFailed(err, status.ReasonLicenseArtifactInvalid)
	}
	return rawArtifact, artifactID, workflow.OK()
}

func licenseReconciled() workflow.Status {
	return workflow.OK().WithAdditionalOptions(status.NewOmLicenseReconciledStatusOptionWithCondition(status.OmLicenseReconciledCondition(true, status.ReasonLicenseReconciled, "")))
}

func licenseFailed(err error, reason string) workflow.Status {
	return workflow.Failed(err).WithAdditionalOptions(status.NewOmLicenseReconciledStatusOptionWithCondition(status.OmLicenseReconciledCondition(false, reason, err.Error())))
}
