package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

// LicenseReconcilerHelper holds the license state and logic for a single reconcile execution.
type LicenseReconcilerHelper struct {
	reconciler *OpsManagerReconciler
	opsManager *omv1.MongoDBOpsManager
	omAdmin    api.OpsManagerAdmin
	log        *zap.SugaredLogger

	lastInstallSecretRef string
	observed             *api.LicenseView
	holderID             string
}

func (r *OpsManagerReconciler) newLicenseReconcilerHelper(opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) (*LicenseReconcilerHelper, error) {
	lastInstallSecretRef, err := opsManager.GetLastInstallSecretRef()
	if err != nil {
		return nil, xerrors.Errorf("failed to read the license secretRef of the last achieved spec: %w", err)
	}
	return &LicenseReconcilerHelper{reconciler: r, opsManager: opsManager, omAdmin: omAdmin, log: log, lastInstallSecretRef: lastInstallSecretRef}, nil
}

func (r *OpsManagerReconciler) ensureOpsManagerLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) workflow.Status {
	helper, err := r.newLicenseReconcilerHelper(opsManager, omAdmin, log)
	if err != nil {
		return licenseFailed(err)
	}
	return helper.Reconcile(ctx)
}

// reconcileLicenseDeletion releases the license the operator installed, keeping the finalizer until it succeeds.
func (r *OpsManagerReconciler) reconcileLicenseDeletion(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(opsManager, util.OpsManagerLicenseFinalizer) {
		return reconcile.Result{}, nil
	}

	log.Info("Releasing the Ops Manager license before the deletion")
	omAdmin, err := r.licenseAdminClient(ctx, opsManager, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, licenseFailed(xerrors.Errorf("failed to connect to Ops Manager to release the license: %w", err)), log, status.NewOMPartOption(status.OpsManager))
	}

	helper, err := r.newLicenseReconcilerHelper(opsManager, omAdmin, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, licenseFailed(err), log, status.NewOMPartOption(status.OpsManager))
	}

	st := helper.releaseOnDeletion(ctx)
	if !st.IsOK() {
		return r.updateStatus(ctx, opsManager, st, log, status.NewOMPartOption(status.OpsManager))
	}
	return st.ReconcileResult()
}

// licenseAdminClient builds the admin client for the release, without creating the first admin user.
func (r *OpsManagerReconciler) licenseAdminClient(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (api.OpsManagerAdmin, error) {
	ca, err := r.opsManagerCA(ctx, opsManager, log)
	if err != nil {
		return nil, xerrors.Errorf("failed to retrieve om ca certificate: %w", err)
	}
	return r.opsManagerAdmin(ctx, opsManager, opsManager.CentralURL(), ca, log)
}

// Reconcile runs the license reconcile and reports the license Ops Manager holds.
func (h *LicenseReconcilerHelper) Reconcile(ctx context.Context) workflow.Status {
	st := h.reconcileLicense(ctx)

	h.reportOpsManagerLicenseReady()
	return st
}

func (h *LicenseReconcilerHelper) reconcileLicense(ctx context.Context) workflow.Status {
	// Held while a license may be in Ops Manager, so a deletion can release it.
	held := h.opsManager.Spec.License.InstallRequested() || h.lastInstallSecretRef != ""
	if st := h.syncLicenseFinalizer(ctx, held); !st.IsOK() {
		return st
	}

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

	receipts, st := h.resolveReleaseReceipts(ctx)
	if !st.IsOK() {
		return st
	}

	if st := h.observeLicense(ctx); !st.IsOK() {
		return st
	}

	requestedID, releasedID, st := h.reconcileLicenseIntent(ctx, receipts)
	if !st.IsOK() {
		return st
	}

	if st := h.reconcileReleaseReceipts(ctx, receipts, releasedID); !st.IsOK() {
		return st
	}

	return h.reportLicenseManaged(requestedID)
}

// releaseDue reports whether the install intent was removed while the last achieved spec still had one.
func (h *LicenseReconcilerHelper) releaseDue() bool {
	return !h.opsManager.Spec.License.InstallRequested() && h.lastInstallSecretRef != ""
}

func (h *LicenseReconcilerHelper) syncLicenseFinalizer(ctx context.Context, wanted bool) workflow.Status {
	if wanted == controllerutil.ContainsFinalizer(h.opsManager, util.OpsManagerLicenseFinalizer) {
		return workflow.OK()
	}

	// A patch keeps the spec out of the request, since a full update fails CRD validation on unset required fields.
	// Like annotations.SetAnnotations it patches a copy, so the response does not overwrite the in memory spec.
	patched := h.opsManager.DeepCopy()
	patch := client.MergeFromWithOptions(h.opsManager, client.MergeFromWithOptimisticLock{})
	if wanted {
		controllerutil.AddFinalizer(patched, util.OpsManagerLicenseFinalizer)
	} else {
		controllerutil.RemoveFinalizer(patched, util.OpsManagerLicenseFinalizer)
	}
	if err := h.reconciler.client.Patch(ctx, patched, patch); err != nil {
		return licenseFailed(xerrors.Errorf("failed to update the license finalizer: %w", err))
	}
	h.opsManager.Finalizers = patched.Finalizers
	h.opsManager.ResourceVersion = patched.ResourceVersion
	return workflow.OK()
}

// releaseOnDeletion releases the license the operator installed, dropping the finalizer only on success.
func (h *LicenseReconcilerHelper) releaseOnDeletion(ctx context.Context) workflow.Status {
	receipts, st := h.resolveReleaseReceipts(ctx)
	if !st.IsOK() {
		return st
	}

	if st := h.observeLicense(ctx); !st.IsOK() {
		return st
	}

	releasedID, st := h.releaseInstalledLicense(ctx)
	if !st.IsOK() {
		return st
	}

	if st := h.reconcileReleaseReceipts(ctx, receipts, releasedID); !st.IsOK() {
		return st
	}

	return h.syncLicenseFinalizer(ctx, false)
}

// reconcileLicenseIntent applies the install or release the spec asks for and returns the artifact
// the spec requested plus the artifact that was released as a consequence.
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
		return licenseFailed(xerrors.Errorf("failed to remove the license identity rotation annotation: %w", err))
	}
	if err := h.omAdmin.RotateLicenseIdentity(ctx); err != nil {
		return licenseFailed(xerrors.Errorf("failed to rotate the Ops Manager license identity: %w", err))
	}
	return workflow.OK()
}

func (h *LicenseReconcilerHelper) reconcileLicenseRecompute(ctx context.Context) workflow.Status {
	if err := annotations.RemoveAnnotation(ctx, h.opsManager, util.LicenseRecomputeAnnotation, h.reconciler.client); err != nil {
		return licenseFailed(xerrors.Errorf("failed to remove the license recompute annotation: %w", err))
	}
	// The recomputed state is published by observeLicense, which reads it right after.
	if _, err := h.omAdmin.RecomputeLicense(ctx); err != nil {
		return licenseFailed(xerrors.Errorf("failed to recompute the Ops Manager license state: %w", err))
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
			return licenseFailed(xerrors.Errorf("failed to read license receipt secret %s: %w", secretName, err))
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
			return licenseFailed(xerrors.Errorf("failed to read Ops Manager license receipt for %s: %w", artifactID, err))
		}
		if err := h.createReleaseReceiptSecret(ctx, secretName, receipt); err != nil {
			return licenseFailed(xerrors.Errorf("failed to write license receipt secret %s: %w", secretName, err))
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
		return licenseFailed(xerrors.Errorf("failed to read the Ops Manager license state: %w", err))
	}

	h.observed = &state
	// Ops Manager versions with the license API but without the identity API are fine, the holder stays unset.
	identity, err := h.omAdmin.ReadLicenseIdentity(ctx)
	if err != nil && !apierror.NewNonNil(err).IsNotFound() {
		return licenseFailed(xerrors.Errorf("failed to read the Ops Manager license identity: %w", err))
	}
	h.holderID = identity.HolderID
	return workflow.OK()
}

// installLicenseFromSecret installs the license the spec asks for and returns the artifact it
// names plus the artifact that was replaced.
func (h *LicenseReconcilerHelper) installLicenseFromSecret(ctx context.Context, receipts []string) (requestedID, releasedID string, st workflow.Status) {
	rawArtifact, artifactID, _, err := h.readLicenseArtifact(ctx, h.opsManager.Spec.License.SecretRef.Name)
	if err != nil {
		return "", "", licenseFailed(err)
	}
	if slices.Contains(receipts, artifactID) {
		err := xerrors.Errorf("license artifact %s in secretRef is also listed in releaseReceipts", artifactID)
		return "", "", licenseFailed(err)
	}

	observed := h.observed
	if observed.HoldsArtifact(artifactID) {
		return artifactID, "", workflow.OK()
	}

	state, err := h.omAdmin.InstallLicense(ctx, json.RawMessage(rawArtifact))
	if err != nil {
		// Ops Manager keeps the license it held, reportLicenseManaged reports the artifact as rejected.
		if apierror.NewNonNil(err).IsLicenseArtifactOutdated() {
			return artifactID, "", workflow.OK()
		}
		return "", "", licenseFailed(xerrors.Errorf("failed to install Ops Manager license %s: %w", artifactID, err))
	}
	h.observed = &state
	h.log.Info("Installed Ops Manager license from secret")

	// The install replaced the license Ops Manager held before.
	if heldBefore, ok := observed.HeldArtifact(); ok && heldBefore.ArtifactID != artifactID && !state.HoldsArtifact(heldBefore.ArtifactID) {
		return artifactID, heldBefore.ArtifactID, workflow.OK()
	}
	return artifactID, "", workflow.OK()
}

// releaseInstalledLicense releases the artifact the operator installed, the one named by the
// Secret in the last achieved spec. Without it the artifact cannot be identified, and a license
// the operator did not install is never released.
func (h *LicenseReconcilerHelper) releaseInstalledLicense(ctx context.Context) (string, workflow.Status) {
	if h.lastInstallSecretRef == "" {
		return "", workflow.OK()
	}
	_, artifactID, invalid, err := h.readLicenseArtifact(ctx, h.lastInstallSecretRef)
	if invalid {
		if _, held := h.observed.HeldArtifact(); held {
			return "", licenseFailed(xerrors.Errorf("cannot release the license Ops Manager holds, the license secret %s that identifies it is gone or no longer holds a license artifact", h.lastInstallSecretRef))
		}
		return "", workflow.OK()
	}
	if err != nil {
		return "", licenseFailed(err)
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
		return "", licenseFailed(xerrors.Errorf("failed to release Ops Manager license %s: %w", artifactID, err))
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
			return nil, licenseFailed(xerrors.Errorf("releaseReceipts entries resolve to the same artifact id %s", artifactID))
		}
		receipts = append(receipts, artifactID)
	}
	return receipts, workflow.OK()
}

func (h *LicenseReconcilerHelper) receiptArtifactID(ctx context.Context, entry omv1.LicenseReleaseReceipt) (string, workflow.Status) {
	_, artifactID, _, err := h.readLicenseArtifact(ctx, entry.SecretRef.Name)
	if err != nil {
		return "", licenseFailed(err)
	}
	return artifactID, workflow.OK()
}

// readLicenseArtifact reads the license from a Secret, invalid is set when the Secret is missing or does not hold a license.
func (h *LicenseReconcilerHelper) readLicenseArtifact(ctx context.Context, secretName string) (rawArtifact, artifactID string, invalid bool, err error) {
	namespace := h.opsManager.Namespace
	data, err := h.reconciler.ReadSecret(ctx, kube.ObjectKey(namespace, secretName), h.reconciler.VaultClient.OpsManagerSecretPathOrEmpty())
	if secret.SecretNotExist(err) {
		return "", "", true, xerrors.Errorf("license secret %s/%s does not exist: %w", namespace, secretName, err)
	}
	if err != nil {
		return "", "", false, xerrors.Errorf("failed to read license secret %s/%s: %w", namespace, secretName, err)
	}
	rawArtifact = data[util.DefaultOpsManagerLicenseKey]
	if rawArtifact == "" {
		return "", "", true, xerrors.Errorf("license secret %s/%s is missing key %q", namespace, secretName, util.DefaultOpsManagerLicenseKey)
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
