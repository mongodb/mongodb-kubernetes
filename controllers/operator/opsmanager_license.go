package operator

import (
	"context"

	"go.uber.org/zap"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
)

// LicenseReconcilerHelper contains the license state and logic for a SINGLE reconcile execution.
// The decisions read it, the status only reports it.
type LicenseReconcilerHelper struct {
	reconciler *OpsManagerReconciler
	opsManager *omv1.MongoDBOpsManager
	omAdmin    api.OpsManagerAdmin
	log        *zap.SugaredLogger

	observed *api.LicenseView
}

func (r *OpsManagerReconciler) newLicenseReconcilerHelper(opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) *LicenseReconcilerHelper {
	return &LicenseReconcilerHelper{reconciler: r, opsManager: opsManager, omAdmin: omAdmin, log: log}
}

func (r *OpsManagerReconciler) ensureOpsManagerLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) workflow.Status {
	return r.newLicenseReconcilerHelper(opsManager, omAdmin, log).Reconcile(ctx)
}

func (h *LicenseReconcilerHelper) Reconcile(ctx context.Context) workflow.Status {
	st := h.reconcileLicense(ctx)

	h.setLicenseReadyCondition()
	return st
}

func (h *LicenseReconcilerHelper) reconcileLicense(ctx context.Context) workflow.Status {
	if st := h.observeLicense(ctx); !st.IsOK() {
		return st
	}

	return licenseReconciled(status.ReasonLicenseReconciled, "")
}

func (h *LicenseReconcilerHelper) observeLicense(ctx context.Context) workflow.Status {
	state, err := h.omAdmin.ReadLicenseState(ctx)
	if err != nil {
		// Older Ops Manager versions without the license API are fine, there is nothing to report.
		if apierror.NewNonNil(err).IsNotFound() {
			return workflow.OK()
		}
		return licenseFailed(err, status.ReasonLicenseObserveFailed)
	}

	h.observed = &state
	return workflow.OK()
}
