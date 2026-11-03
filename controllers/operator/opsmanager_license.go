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
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// LicenseReconcilerHelper contains the license state and logic for a SINGLE reconcile execution.
// The decisions read it, the status only reports it.
type LicenseReconcilerHelper struct {
	reconciler *OpsManagerReconciler
	opsManager *omv1.MongoDBOpsManager
	omAdmin    api.OpsManagerAdmin
	log        *zap.SugaredLogger

	lastInstallSecretRef string
	observed             *api.LicenseView
	holderID             string
}

func (r *OpsManagerReconciler) newLicenseReconcilerHelper(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) (*LicenseReconcilerHelper, error) {
	lastInstallSecretRef, err := opsManager.GetLastInstallSecretRef()
	if err != nil {
		return nil, err
	}
	return &LicenseReconcilerHelper{reconciler: r, opsManager: opsManager, omAdmin: omAdmin, log: log, lastInstallSecretRef: lastInstallSecretRef}, nil
}

func (r *OpsManagerReconciler) ensureOpsManagerLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) workflow.Status {
	helper, err := r.newLicenseReconcilerHelper(ctx, opsManager, omAdmin, log)
	if err != nil {
		return licenseFailed(err, status.ReasonLicenseStateFailed)
	}
	return helper.Reconcile(ctx)
}

func (h *LicenseReconcilerHelper) Reconcile(ctx context.Context) workflow.Status {
	st := h.reconcileLicense(ctx)

	h.setLicenseReadyCondition()
	return st
}

func (h *LicenseReconcilerHelper) reconcileLicense(ctx context.Context) workflow.Status {
	// The identity rotation requested through an annotation runs before the license state is read.
	if h.opsManager.LicenseRotateIdentityDue() {
		if st := h.reconcileLicenseIdentity(ctx); !st.IsOK() {
			return st
		}
	}

	if h.opsManager.LicenseRecomputeDue() {
		if st := h.reconcileLicenseRecompute(ctx); !st.IsOK() {
			return st
		}
	}

	// The release receipts requested in the spec resolve to the artifact ids they name.
	receipts, st := h.resolveReleaseReceipts(ctx)
	if !st.IsOK() {
		return st
	}

	if st := h.observeLicense(ctx); !st.IsOK() {
		return st
	}

	requestedID, releasedID, licenseStatus := h.reconcileLicenseIntent(ctx, receipts)
	if !licenseStatus.IsOK() {
		return licenseStatus
	}

	if st := h.reconcileReleaseReceipts(ctx, receipts, releasedID); !st.IsOK() {
		return st
	}

	return h.licenseReconciledStatus(requestedID)
}

// releaseDue reports whether the install intent was removed while the last achieved spec still had one.
func (h *LicenseReconcilerHelper) releaseDue() bool {
	return !h.opsManager.Spec.License.InstallRequested() && h.lastInstallSecretRef != ""
}

// reconcileLicenseIntent applies the install or release the spec asks for and returns the artifact
// the spec requested plus the artifact that was released as a consequence. The install asks OM:
// when it already holds the artifact from the secret the PUT is skipped, otherwise the license is
// installed or renewed.
func (h *LicenseReconcilerHelper) reconcileLicenseIntent(ctx context.Context, receipts []string) (requestedID, releasedID string, st workflow.Status) {
	if h.releaseDue() {
		releasedID, st := h.releaseInstalledLicense(ctx)
		return "", releasedID, st
	}
	if h.opsManager.Spec.License.InstallRequested() {
		return h.installLicenseFromSecret(ctx, receipts)
	}
	return "", "", workflow.OK()
}

func (h *LicenseReconcilerHelper) reconcileLicenseIdentity(ctx context.Context) workflow.Status {
	if err := annotations.RemoveAnnotation(ctx, h.opsManager, util.LicenseRotateIdentityAnnotation, h.reconciler.client); err != nil {
		return licenseFailed(err, status.ReasonIdentityRotationFailed)
	}
	if err := h.omAdmin.RotateLicenseIdentity(ctx); err != nil {
		return licenseFailed(err, status.ReasonIdentityRotationFailed)
	}
	return workflow.OK()
}

func (h *LicenseReconcilerHelper) reconcileLicenseRecompute(ctx context.Context) workflow.Status {
	if err := annotations.RemoveAnnotation(ctx, h.opsManager, util.LicenseRecomputeAnnotation, h.reconciler.client); err != nil {
		return licenseFailed(err, status.ReasonLicenseRecomputeFailed)
	}
	if _, err := h.omAdmin.RecomputeLicense(ctx); err != nil {
		return licenseFailed(err, status.ReasonLicenseRecomputeFailed)
	}
	return workflow.OK()
}

func (h *LicenseReconcilerHelper) reconcileReleaseReceipts(ctx context.Context, receipts []string, releasedID string) workflow.Status {
	autoReceipts := h.opsManager.Spec.License.AutoReleaseReceiptsRequested()
	if autoReceipts && releasedID != "" && !slices.Contains(receipts, releasedID) {
		receipts = append(receipts, releasedID)
	}
	for _, artifactID := range receipts {
		secretName := releaseReceiptSecretName(artifactID)
		h.reconciler.resourceWatcher.AddWatchedResourceIfNotAdded(secretName, h.opsManager.Namespace, watch.Secret, kube.ObjectKeyFromApiObject(h.opsManager))
		existing, err := h.reconciler.client.GetSecret(ctx, kube.ObjectKey(h.opsManager.Namespace, secretName))
		if err != nil && !secret.SecretNotExist(err) {
			return licenseFailed(err, status.ReasonLicenseReceiptFailed)
		}
		if receipt := existing.Data[util.DefaultOpsManagerReleaseReceiptKey]; len(receipt) > 0 {
			continue
		}

		if h.observed.HoldsArtifact(artifactID) {
			h.opsManager.AddOpsManagerWarningIfNotExists(status.Warning(fmt.Sprintf("Ops Manager still holds license %s, its release receipt is written once it is released", artifactID)))
			continue
		}

		receipt, err := h.omAdmin.ReadLicenseReceipt(ctx, artifactID)
		if err != nil {
			return licenseFailed(xerrors.Errorf("failed to read Ops Manager license receipt for %s: %w", artifactID, err), status.ReasonLicenseReceiptFailed)
		}
		if err := h.createReleaseReceiptSecret(ctx, secretName, receipt); err != nil {
			return licenseFailed(xerrors.Errorf("failed to write license receipt secret: %w", err), status.ReasonLicenseReceiptFailed)
		}
	}
	return workflow.OK()
}

func (h *LicenseReconcilerHelper) observeLicense(ctx context.Context) workflow.Status {
	state, err := h.omAdmin.ReadLicenseState(ctx)
	if err != nil {
		// Older Ops Manager versions without the license API are fine when the operator never recorded an install.
		if h.lastInstallSecretRef == "" && apierror.NewNonNil(err).IsNotFound() {
			return workflow.OK()
		}
		return licenseFailed(err, status.ReasonLicenseObserveFailed)
	}

	h.observed = &state
	// Ops Manager versions with the license API but without the identity API are fine, the holder stays unset.
	identity, err := h.omAdmin.ReadLicenseIdentity(ctx)
	if err != nil && !apierror.NewNonNil(err).IsNotFound() {
		return licenseFailed(err, status.ReasonLicenseObserveFailed)
	}
	h.holderID = identity.HolderID
	return workflow.OK()
}

// installLicenseFromSecret installs the license the spec asks for and returns the artifact it
// names plus the artifact that was replaced as a consequence.
func (h *LicenseReconcilerHelper) installLicenseFromSecret(ctx context.Context, receipts []string) (requestedID, releasedID string, st workflow.Status) {
	rawArtifact, artifactID, invalid, err := h.readLicenseArtifact(ctx, h.opsManager.Spec.License.SecretRef.Name)
	if invalid {
		return "", "", licenseFailed(err, status.ReasonLicenseArtifactInvalid)
	}
	if err != nil {
		return "", "", licenseFailed(err, status.ReasonLicenseSecretFailed)
	}
	if slices.Contains(receipts, artifactID) {
		err := xerrors.Errorf("license artifact %s in secretRef is also listed in releaseReceipts", artifactID)
		return "", "", licenseFailed(err, status.ReasonLicenseArtifactInvalid)
	}

	observed := h.observed
	if observed.HoldsArtifact(artifactID) {
		return artifactID, "", workflow.OK()
	}

	state, err := h.omAdmin.InstallLicense(ctx, json.RawMessage(rawArtifact))
	if err != nil {
		msg := "failed to install Ops Manager license %s: %w"
		if apierror.NewNonNil(err).IsConflict() {
			msg = "Ops Manager rejected license %s from the license Secret: %w"
		}
		return "", "", licenseFailed(xerrors.Errorf(msg, artifactID, err), status.ReasonLicenseInstallFailed)
	}
	h.observed = &state
	h.log.Info("Installed Ops Manager license from secret")

	// The install replaced the license Ops Manager held before.
	if heldBefore, ok := observed.HeldArtifact(); ok && heldBefore.ArtifactID != artifactID && !state.HoldsArtifact(heldBefore.ArtifactID) {
		return artifactID, heldBefore.ArtifactID, workflow.OK()
	}
	return artifactID, "", workflow.OK()
}

// releaseArtifactID resolves the artifact the operator installed. The last achieved spec names the
// Secret the install came from. When that Secret is gone, the license Ops Manager holds is the only
// trace left of the install.
func (h *LicenseReconcilerHelper) releaseArtifactID(ctx context.Context) string {
	if h.lastInstallSecretRef != "" {
		if _, artifactID, invalid, err := h.readLicenseArtifact(ctx, h.lastInstallSecretRef); err == nil && !invalid {
			return artifactID
		}
	}
	if held, ok := h.observed.HeldArtifact(); ok {
		return held.ArtifactID
	}
	return ""
}

func (h *LicenseReconcilerHelper) releaseInstalledLicense(ctx context.Context) (string, workflow.Status) {
	artifactID := h.releaseArtifactID(ctx)
	if artifactID == "" {
		return "", workflow.OK()
	}
	// Ops Manager holds one license at a time. When it no longer holds ours, it was already
	// released, and a license installed by someone else is left alone.
	if !h.observed.HoldsArtifact(artifactID) {
		if _, listed := h.observed.Artifact(artifactID); listed {
			return artifactID, workflow.OK()
		}
		return "", workflow.OK()
	}

	released, err := h.omAdmin.ReleaseLicense(ctx, artifactID)
	if apierror.NewNonNil(err).IsNotFound() {
		// Ops Manager has no record of it, so there is nothing released and no receipt to collect.
		return "", workflow.OK()
	}
	if err != nil {
		return "", licenseFailed(xerrors.Errorf("failed to release Ops Manager license %s: %w", artifactID, err), status.ReasonLicenseReleaseFailed)
	}
	h.observed = &released
	return artifactID, workflow.OK()
}

func (h *LicenseReconcilerHelper) resolveReleaseReceipts(ctx context.Context) ([]string, workflow.Status) {
	entries := h.opsManager.Spec.License.GetReleaseReceipts()
	receipts := make([]string, 0, len(entries))
	for _, entry := range entries {
		artifactID, st := h.receiptArtifactID(ctx, entry)
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

func (h *LicenseReconcilerHelper) receiptArtifactID(ctx context.Context, entry omv1.LicenseReleaseReceipt) (string, workflow.Status) {
	_, artifactID, invalid, err := h.readLicenseArtifact(ctx, entry.SecretRef.Name)
	if invalid {
		return "", licenseFailed(err, status.ReasonLicenseArtifactInvalid)
	}
	if err != nil {
		return "", licenseFailed(err, status.ReasonLicenseReceiptFailed)
	}
	return artifactID, workflow.OK()
}

// readLicenseArtifact reads the license from a Secret, invalid is set when the Secret does not hold a license.
func (h *LicenseReconcilerHelper) readLicenseArtifact(ctx context.Context, secretName string) (rawArtifact, artifactID string, invalid bool, err error) {
	namespace := h.opsManager.Namespace
	rawArtifact, err = h.reconciler.ReadSecretKey(ctx, kube.ObjectKey(namespace, secretName), h.reconciler.VaultClient.OpsManagerSecretPathOrEmpty(), util.DefaultOpsManagerLicenseKey)
	if secret.SecretNotExist(err) {
		return "", "", false, xerrors.Errorf("license secret %s/%s does not exist", namespace, secretName)
	}
	if err != nil {
		return "", "", false, err
	}
	if rawArtifact == "" {
		return "", "", false, xerrors.Errorf("license secret %s/%s is missing key %q", namespace, secretName, util.DefaultOpsManagerLicenseKey)
	}
	artifactID, err = api.ArtifactIDFromRaw(rawArtifact)
	if err != nil {
		return "", "", true, err
	}
	return rawArtifact, artifactID, false, nil
}

func (h *LicenseReconcilerHelper) createReleaseReceiptSecret(ctx context.Context, name, receipt string) error {
	return secret.CreateOrUpdate(ctx, h.reconciler.client, secret.Builder().
		SetName(name).
		SetNamespace(h.opsManager.Namespace).
		SetField(util.DefaultOpsManagerReleaseReceiptKey, receipt).
		Build())
}

func releaseReceiptSecretName(artifactID string) string {
	return artifactID + "-release-receipt"
}
