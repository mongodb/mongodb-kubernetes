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

func (r *OpsManagerReconciler) ensureOpsManagerLicense(ctx context.Context, opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) workflow.Status {
	if st := r.observeLicense(ctx, opsManager, omAdmin); !st.IsOK() {
		return st
	}

	return licenseReconciled()
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
	return workflow.OK()
}

func licenseReconciled() workflow.Status {
	return workflow.OK().WithAdditionalOptions(status.NewOmLicenseReconciledStatusOptionWithCondition(status.OmLicenseReconciledCondition(true, status.ReasonLicenseReconciled, "")))
}

func licenseFailed(err error, reason string) workflow.Status {
	return workflow.Failed(err).WithAdditionalOptions(status.NewOmLicenseReconciledStatusOptionWithCondition(status.OmLicenseReconciledCondition(false, reason, err.Error())))
}
