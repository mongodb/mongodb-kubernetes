package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func (r *OpsManagerReconciler) ensureOpsManagerLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) workflow.Status {
	// The identity rotation requested through an annotation runs before the license state is read.
	if opsManager.LicenseRotateIdentityDue() {
		if st := r.reconcileLicenseIdentity(ctx, opsManager, omAdmin); !st.IsOK() {
			return st
		}
	}

	if opsManager.LicenseRecomputeDue() {
		if st := r.reconcileLicenseRecompute(ctx, opsManager, omAdmin); !st.IsOK() {
			return st
		}
	}

	// The release receipts requested in the spec resolve to the artifact ids they name.
	receipts, st := r.resolveReleaseReceipts(ctx, opsManager)
	if !st.IsOK() {
		return st
	}

	if st := r.observeLicense(ctx, opsManager, omAdmin); !st.IsOK() {
		return st
	}

	releasedID, licenseStatus := r.reconcileLicenseIntent(ctx, opsManager, omAdmin, receipts, log)
	if !licenseStatus.IsOK() {
		return licenseStatus
	}

	if st := r.reconcileReleaseReceipts(ctx, opsManager, omAdmin, receipts, releasedID); !st.IsOK() {
		return st
	}

	if opsManager.LicenseReleaseDueToRemovedInstallIntent() {
		opsManager.SetInstalledArtifactID("")
	}

	return licenseReconciled()
}

func (r *OpsManagerReconciler) reconcileLicenseIntent(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, receipts []string, log *zap.SugaredLogger) (string, workflow.Status) {
	if opsManager.LicenseReleaseDueToRemovedInstallIntent() {
		return r.releaseInstalledLicense(ctx, opsManager, omAdmin)
	}
	if opsManager.Spec.License.InstallRequested() {
		return r.installLicenseFromSecret(ctx, opsManager, omAdmin, receipts, log)
	}
	return "", workflow.OK()
}

func (r *OpsManagerReconciler) reconcileLicenseIdentity(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin) workflow.Status {
	if err := annotations.RemoveAnnotation(ctx, opsManager, util.LicenseRotateIdentityAnnotation, r.client); err != nil {
		return licenseFailed(err, status.ReasonIdentityRotationFailed)
	}
	if err := omAdmin.RotateLicenseIdentity(ctx); err != nil {
		return licenseFailed(err, status.ReasonIdentityRotationFailed)
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) reconcileLicenseRecompute(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin) workflow.Status {
	if err := annotations.RemoveAnnotation(ctx, opsManager, util.LicenseRecomputeAnnotation, r.client); err != nil {
		return licenseFailed(err, status.ReasonLicenseRecomputeFailed)
	}
	if err := omAdmin.RecomputeLicense(ctx); err != nil {
		return licenseFailed(err, status.ReasonLicenseRecomputeFailed)
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) reconcileReleaseReceipts(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, receipts []string, releasedID string) workflow.Status {
	autoReceipts := opsManager.Spec.License.AutoReleaseReceiptsRequested()
	if autoReceipts && releasedID != "" && !slices.Contains(receipts, releasedID) {
		receipts = append(receipts, releasedID)
	}
	state := opsManager.GetLicenseStatus()
	for _, artifactID := range receipts {
		secretName := releaseReceiptSecretName(artifactID)
		existing, err := r.client.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, secretName))
		if err != nil && !secret.SecretNotExist(err) {
			return licenseFailed(err, status.ReasonLicenseReceiptFailed)
		}
		if _, written := existing.Data[util.DefaultOpsManagerReleaseReceiptKey]; written {
			continue
		}

		if state.HoldsArtifact(artifactID) {
			opsManager.AddOpsManagerWarningIfNotExists(status.Warning(fmt.Sprintf("Ops Manager still holds license %s, its release receipt is written once it is released", artifactID)))
			continue
		}

		receipt, err := omAdmin.ReadLicenseReceipt(ctx, artifactID)
		if err != nil {
			return licenseFailed(xerrors.Errorf("failed to read Ops Manager license receipt for %s: %w", artifactID, err), status.ReasonLicenseReceiptFailed)
		}
		if err := r.createReleaseReceiptSecret(ctx, opsManager.Namespace, secretName, receipt); err != nil {
			return licenseFailed(xerrors.Errorf("failed to write license receipt secret: %w", err), status.ReasonLicenseReceiptFailed)
		}
	}
	return workflow.OK()
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
	// Ops Manager versions with the license API but without the identity API are fine, the identity stays unset.
	identity, err := omAdmin.ReadLicenseIdentity(ctx)
	if err != nil && !apierror.NewNonNil(err).IsNotFound() {
		return licenseFailed(err, status.ReasonLicenseObserveFailed)
	}
	if err == nil {
		opsManager.SetLicenseIdentity(identity.HolderID)
	}
	if installedID != "" && !state.HoldsArtifact(installedID) && !opsManager.LicenseReleaseDueToRemovedInstallIntent() {
		opsManager.AddOpsManagerWarningIfNotExists("Ops Manager no longer holds the license last installed by the operator")
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) installLicenseFromSecret(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, receipts []string, log *zap.SugaredLogger) (string, workflow.Status) {
	rawArtifact, artifactID, st := r.readLicenseArtifact(ctx, opsManager, opsManager.Spec.License.LicenseSecretRef.Name, status.ReasonLicenseSecretFailed)
	if !st.IsOK() {
		return "", st
	}
	if slices.Contains(receipts, artifactID) {
		return "", licenseFailed(xerrors.Errorf("license artifact %s in licenseSecretRef is also listed in releaseReceipts", artifactID), status.ReasonLicenseArtifactInvalid)
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

func (r *OpsManagerReconciler) releaseInstalledLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin) (string, workflow.Status) {
	installedID := opsManager.GetInstalledArtifactID()
	// Ops Manager holds one license at a time. When it no longer holds ours, it was already
	// released, and a license installed by someone else is left alone.
	state := opsManager.GetLicenseStatus()
	if !state.HoldsArtifact(installedID) {
		if _, listed := state.ArtifactStatus(installedID); listed {
			return installedID, workflow.OK()
		}
		return "", workflow.OK()
	}

	released, err := omAdmin.ReleaseLicense(ctx, installedID)
	if apierror.NewNonNil(err).IsNotFound() {
		// Ops Manager has no record of it, so there is nothing released and no receipt to collect.
		return "", workflow.OK()
	}
	if err != nil {
		return "", licenseFailed(xerrors.Errorf("failed to release Ops Manager license %s: %w", installedID, err), status.ReasonLicenseReleaseFailed)
	}
	opsManager.SetLicenseStatusFromState(released)
	return installedID, workflow.OK()
}

func (r *OpsManagerReconciler) resolveReleaseReceipts(ctx context.Context, opsManager *omv1.MongoDBOpsManager) ([]string, workflow.Status) {
	entries := opsManager.Spec.License.GetReleaseReceipts()
	receipts := make([]string, 0, len(entries))
	for _, entry := range entries {
		artifactID, st := r.receiptArtifactID(ctx, opsManager, entry)
		if !st.IsOK() {
			return nil, st
		}
		if slices.Contains(receipts, artifactID) {
			return nil, licenseFailed(xerrors.Errorf("releaseReceipts entries resolve to the same artifact id %s", artifactID), status.ReasonLicenseArtifactInvalid)
		}
		receipts = append(receipts, artifactID)
	}
	return receipts, workflow.OK()
}

func (r *OpsManagerReconciler) receiptArtifactID(ctx context.Context, opsManager *omv1.MongoDBOpsManager, entry omv1.LicenseReleaseReceipt) (string, workflow.Status) {
	if entry.LicenseSecretRef == nil {
		return entry.ArtifactID, workflow.OK()
	}
	_, artifactID, st := r.readLicenseArtifact(ctx, opsManager, entry.LicenseSecretRef.Name, status.ReasonLicenseReceiptFailed)
	return artifactID, st
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

func (r *OpsManagerReconciler) createReleaseReceiptSecret(ctx context.Context, namespace, name, receipt string) error {
	return secret.CreateOrUpdate(ctx, r.client, secret.Builder().
		SetName(name).
		SetNamespace(namespace).
		SetField(util.DefaultOpsManagerReleaseReceiptKey, receipt).
		Build())
}

func releaseReceiptSecretName(artifactID string) string {
	return artifactID + "-release-receipt"
}

func licenseReconciled() workflow.Status {
	return workflow.OK().WithAdditionalOptions(status.NewOmLicenseReconciledStatusOptionWithCondition(status.OmLicenseReconciledCondition(true, status.ReasonLicenseReconciled, "")))
}

func licenseFailed(err error, reason string) workflow.Status {
	return workflow.Failed(err).WithAdditionalOptions(status.NewOmLicenseReconciledStatusOptionWithCondition(status.OmLicenseReconciledCondition(false, reason, err.Error())))
}
