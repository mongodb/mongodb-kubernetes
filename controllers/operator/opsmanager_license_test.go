package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

func licensedView(state string) api.LicenseView {
	return api.LicenseView{Enforcement: &api.LicenseEnforcementView{EffectiveState: state}}
}

func viewWithLicenses(state string, licenses ...api.LicenseArtifactView) api.LicenseView {
	return api.LicenseView{
		Enforcement: &api.LicenseEnforcementView{EffectiveState: state},
		Licenses:    licenses,
	}
}

func artifactView(artifactID string) api.LicenseArtifactView {
	return api.LicenseArtifactView{ArtifactID: artifactID, Usable: true, Status: "ACTIVE"}
}

func releasedArtifactView(artifactID string) api.LicenseArtifactView {
	return api.LicenseArtifactView{ArtifactID: artifactID, Status: api.LicenseArtifactReleased}
}

func ensureLicense(r *OpsManagerReconciler, om *omv1.MongoDBOpsManager, admin api.OpsManagerAdmin) workflow.Status {
	return r.ensureOpsManagerLicense(context.Background(), om, admin, zap.S())
}

// seedInstalled records a prior install from the given Secret.
func seedInstalled(t *testing.T, r *OpsManagerReconciler, om *omv1.MongoDBOpsManager, licenseSecretName string) {
	t.Helper()
	require.NoError(t, r.saveLicenseSecretRef(context.Background(), om, licenseSecretName, zap.S()))
}

func licenseActiveCondition(t *testing.T, om *omv1.MongoDBOpsManager) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(om.Status.OpsManagerStatus.Conditions, status.ConditionLicenseActive)
	require.NotNil(t, c)
	return *c
}

func managedCondition(t *testing.T, options []status.Option) metav1.Condition {
	t.Helper()
	opt, found := status.GetOption(options, status.LicenseStatusOption{})
	require.True(t, found)
	return opt.(status.LicenseStatusOption).Condition
}

func assertOperatorManagedLicenseReason(t *testing.T, options []status.Option, reason string) {
	t.Helper()
	assert.Equal(t, reason, managedCondition(t, options).Reason)
}

func TestEnsureOpsManagerLicense_ShowsIdentityWithoutRotationRequest(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{LicenseIdentity: api.IdentityView{HolderID: "holder-1"}}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Contains(t, licenseActiveCondition(t, testOm).Message, "Holder ID holder-1.")
}

func TestEnsureOpsManagerLicense_IdentityReadFailure(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	setLicenseRotateIdentityAnnotation(t, context.Background(), r.client, testOm, "true")
	admin := &api.MockedOmAdmin{LicenseIdentityErr: assert.AnError}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_ProvisioningModels(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	licensed := licensedView("LICENSED")
	eaLicensed := viewWithLicenses("LICENSED", artifactView("ea-auto"))

	tests := []struct {
		name          string
		configure     func(*omv1.MongoDBOpsManager) []client.Object
		licenseState  api.LicenseView
		wantInstalled bool
		wantArtifact  string
	}{
		{
			name:         "connected EA observes OM state without spec.license",
			configure:    func(*omv1.MongoDBOpsManager) []client.Object { return nil },
			licenseState: eaLicensed,
			wantArtifact: "ea-auto",
		},
		{
			name: "spec.license without secretRef observes OM state",
			configure: func(om *omv1.MongoDBOpsManager) []client.Object {
				om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
				return nil
			},
			licenseState: eaLicensed,
			wantArtifact: "ea-auto",
		},
		{
			name: "GitOps secretRef installs from Kubernetes Secret",
			configure: func(om *omv1.MongoDBOpsManager) []client.Object {
				om.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
				s := licenseSecretFor(om, "om-license", "license", artifact)
				return []client.Object{&s}
			},
			licenseState:  licensed,
			wantInstalled: true,
			wantArtifact:  "art-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testOm := DefaultOpsManagerBuilder().Build()
			extra := tc.configure(testOm)
			r := newOmLicenseReconciler(t, testOm, extra...)
			admin := &api.MockedOmAdmin{LicenseState: tc.licenseState}

			st := ensureLicense(r, testOm, admin)
			assert.True(t, st.IsOK())

			if tc.wantInstalled {
				assert.Equal(t, json.RawMessage(artifact), admin.InstalledLicense)
			} else {
				assert.Nil(t, admin.InstalledLicense)
			}
			active := licenseActiveCondition(t, testOm)
			assert.Contains(t, active.Message, "License state is LICENSED.")
			assert.Contains(t, active.Message, tc.wantArtifact)

			wantReason, wantStatus := status.ReasonLicenseNotProvided, metav1.ConditionFalse
			if tc.wantInstalled {
				wantReason, wantStatus = status.ReasonLicenseManaged, metav1.ConditionTrue
			}
			c := managedCondition(t, st.StatusOptions())
			assert.Equal(t, status.ConditionOperatorManagedLicense, c.Type)
			assert.Equal(t, wantStatus, c.Status)
			assert.Equal(t, wantReason, c.Reason)
		})
	}
}

func TestEnsureOpsManagerLicense_ObserveGetFailurePolicy(t *testing.T) {
	tests := []struct {
		name        string
		withSpec    bool
		unsupported bool
		wantOK      bool
	}{
		{name: "transient GET failure is retried", withSpec: false, wantOK: false},
		{name: "unsupported API without spec.license is ignored", withSpec: false, unsupported: true, wantOK: true},
		{name: "unsupported API without a recorded install is ignored", withSpec: true, unsupported: true, wantOK: true},
		{name: "transient GET failure with spec.license is retried", withSpec: true, wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testOm := DefaultOpsManagerBuilder().Build()
			if tc.withSpec {
				testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
			}
			r := newOmLicenseReconciler(t, testOm)
			readErr := assert.AnError
			if tc.unsupported {
				statusCode := http.StatusNotFound
				readErr = &apierror.Error{Status: &statusCode}
			}
			admin := &api.MockedOmAdmin{LicenseReadErr: readErr}

			assert.Equal(t, tc.wantOK, ensureLicense(r, testOm, admin).IsOK())
		})
	}
}

func TestEnsureOpsManagerLicense_IdentityAPIMissingIsIgnored(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	notFound := http.StatusNotFound
	admin := &api.MockedOmAdmin{
		LicenseState:       licensedView("GRACE"),
		LicenseIdentityErr: &apierror.Error{Status: &notFound},
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	active := licenseActiveCondition(t, testOm)
	assert.Contains(t, active.Message, "License state is GRACE.")
	assert.NotContains(t, active.Message, "Holder ID")
}

func TestEnsureOpsManagerLicense_IdentityReadErrorWithoutRotationFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{LicenseIdentityErr: assert.AnError}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_WritesReadyConditionFromGet(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("GRACE", artifactView("art-1")),
	}
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	active := licenseActiveCondition(t, testOm)
	assert.Equal(t, status.ReasonLicenseInactive, active.Reason)
	assert.Contains(t, active.Message, "License state is GRACE.")
	assert.Contains(t, active.Message, "license art-1")
}

func TestEnsureOpsManagerLicense_UnknownStateBeforeFirstRecompute(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)

	assert.True(t, ensureLicense(r, testOm, &api.MockedOmAdmin{}).IsOK())
	active := licenseActiveCondition(t, testOm)
	assert.Equal(t, metav1.ConditionUnknown, active.Status)
	assert.Equal(t, status.ReasonLicenseUnknown, active.Reason)
	assert.Contains(t, active.Message, "has not reported a license state yet")
}

func TestEnsureOpsManagerLicense_InstallsWhenArtifactChanges(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{LicenseState: licensedView("LICENSED")}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, json.RawMessage(artifact), admin.InstalledLicense)

	admin.InstalledLicense = nil
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Nil(t, admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_ObservesOMChangesWhenSecretUnchanged(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{LicenseState: licensedView("LICENSED")}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	require.NotNil(t, admin.InstalledLicense)

	admin.InstalledLicense = nil
	admin.LicenseState = viewWithLicenses("GRACE", artifactView("art-1"))

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assert.Contains(t, licenseActiveCondition(t, testOm).Message, "License state is GRACE.")
}

func TestEnsureOpsManagerLicense_ReleaseFailsWhenLicenseGetFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{LicenseReadErr: assert.AnError}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	ref, err := r.readLicenseSecretRef(context.Background(), testOm)
	require.NoError(t, err)
	assert.Equal(t, "om-license", ref)
}

func TestEnsureOpsManagerLicense_ReleaseWithoutTheLicenseSecretFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("LICENSED", artifactView("art-1")),
	}

	// Without the license Secret the operator cannot tell the license it installed from a foreign
	// one, so it releases nothing and says why.
	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_ReleaseWithSecretMissingKeySkipsWhenNothingHeld(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	badSecret := licenseSecretFor(testOm, "om-license", "other-key", `{}`)
	r := newOmLicenseReconciler(t, testOm, &badSecret)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("", releasedArtifactView("art-1")),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_InstallFailed(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{LicenseInstallErr: assert.AnError}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())

	c := managedCondition(t, st.StatusOptions())
	assert.Equal(t, status.ConditionOperatorManagedLicense, c.Type)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, status.ReasonLicenseFailed, c.Reason)
}

func TestEnsureOpsManagerLicense_InstallConflictReportsRejected(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)

	conflict := http.StatusConflict
	admin := &api.MockedOmAdmin{
		LicenseReadState:  new(viewWithLicenses("LICENSED", artifactView("art-ui"))),
		LicenseInstallErr: &apierror.Error{Status: &conflict, ErrorCode: "ARTIFACT_OUTDATED", Detail: "artifact is not newer than the installed one"},
	}

	// ARTIFACT_OUTDATED means the artifact in the Secret is not newer than what was installed
	// before, the customer brings a newer one from EA.
	st := ensureLicense(r, testOm, admin)
	assert.True(t, st.IsOK())
	assert.Empty(t, admin.InstalledLicense)

	c := managedCondition(t, st.StatusOptions())
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, status.ReasonLicenseRejected, c.Reason)
	assert.Equal(t, "License art-1 from .spec.license.secretRef is not installed, because Ops Manager already holds a license this new or newer that the operator doesn't manage.", c.Message)
}

func TestEnsureOpsManagerLicense_InstallConflictWhenOMHoldsNothing(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)

	// Rejected by anti rollback with no active license left: every artifact ever installed counts,
	// released ones included.
	conflict := http.StatusConflict
	admin := &api.MockedOmAdmin{
		LicenseReadState:  new(viewWithLicenses("LICENSED", releasedArtifactView("art-1"))),
		LicenseInstallErr: &apierror.Error{Status: &conflict, ErrorCode: "ARTIFACT_OUTDATED"},
	}

	st := ensureLicense(r, testOm, admin)
	assert.True(t, st.IsOK())
	c := managedCondition(t, st.StatusOptions())
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, status.ReasonLicenseRejected, c.Reason)
	assert.Contains(t, c.Message, "Ops Manager already holds a license this new or newer that the operator doesn't manage")
	assert.NotContains(t, c.Message, "It holds license")
}

func TestEnsureOpsManagerLicense_InstallConflictWithOtherCodeFails(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)

	conflict := http.StatusConflict
	admin := &api.MockedOmAdmin{
		LicenseInstallErr: &apierror.Error{Status: &conflict, ErrorCode: "SOME_OTHER_CONFLICT"},
	}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	c := managedCondition(t, st.StatusOptions())
	assert.Equal(t, status.ReasonLicenseFailed, c.Reason)
	assert.Contains(t, c.Message, "SOME_OTHER_CONFLICT")
}

func TestEnsureOpsManagerLicense_RotateClearsFlag(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	setLicenseRotateIdentityAnnotation(t, context.Background(), r.client, testOm, "true")
	admin := &api.MockedOmAdmin{
		LicenseState:    licensedView("LICENSED"),
		LicenseIdentity: api.IdentityView{HolderID: "holder-rotated"},
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, testOm.Annotations[util.LicenseRotateIdentityAnnotation])
	active := licenseActiveCondition(t, testOm)
	assert.Contains(t, active.Message, "License state is LICENSED.")
	assert.Contains(t, active.Message, "Holder ID holder-rotated.")

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Contains(t, licenseActiveCondition(t, testOm).Message, "License state is LICENSED.")
}

func TestEnsureOpsManagerLicense_RotateOnReannotate(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	setLicenseRotateIdentityAnnotation(t, context.Background(), r.client, testOm, "true")
	admin := &api.MockedOmAdmin{LicenseState: licensedView("LICENSED")}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, testOm.Annotations[util.LicenseRotateIdentityAnnotation])

	setLicenseRotateIdentityAnnotation(t, context.Background(), r.client, testOm, "true")
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, testOm.Annotations[util.LicenseRotateIdentityAnnotation])
}

func TestEnsureOpsManagerLicense_RotateNotRotatedFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	setLicenseRotateIdentityAnnotation(t, context.Background(), r.client, testOm, "true")
	admin := &api.MockedOmAdmin{IdentityRotationErr: assert.AnError}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Empty(t, testOm.Annotations[util.LicenseRotateIdentityAnnotation])

	c := managedCondition(t, st.StatusOptions())
	assert.Equal(t, status.ConditionOperatorManagedLicense, c.Type)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, status.ReasonLicenseFailed, c.Reason)
}

func TestEnsureOpsManagerLicense_RecomputeOnAnnotation(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	setLicenseRecomputeAnnotation(t, context.Background(), r.client, testOm, "true")
	admin := &api.MockedOmAdmin{}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, testOm.Annotations[util.LicenseRecomputeAnnotation])

	setLicenseRecomputeAnnotation(t, context.Background(), r.client, testOm, "true")
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, testOm.Annotations[util.LicenseRecomputeAnnotation])
}

func TestEnsureOpsManagerLicense_RecomputePublishesReturnedState(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	setLicenseRecomputeAnnotation(t, context.Background(), r.client, testOm, "true")
	admin := &api.MockedOmAdmin{
		LicenseReadState:       new(licensedView("LICENSED")),
		LicenseRecomputedState: new(licensedView("ENFORCED")),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Contains(t, licenseActiveCondition(t, testOm).Message, "License state is ENFORCED.")
}

func TestEnsureOpsManagerLicense_WithoutSecretRefInstallsNoLicense(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{LicenseState: viewWithLicenses("LICENSED", artifactView("art-ui"))}

	st := ensureLicense(r, testOm, admin)
	assert.True(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	c := managedCondition(t, st.StatusOptions())
	assert.Equal(t, status.ReasonLicenseNotProvided, c.Reason)
	assert.Equal(t, ".spec.license.secretRef is not set, so the operator doesn't manage the license.", c.Message)
}

func TestEnsureOpsManagerLicense_ReleaseOnLicenseSpecRemoval(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = nil
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("LICENSED", artifactView("art-1")),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_ReceiptAddedAfterRelease(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("LICENSED", artifactView("art-1")),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)

	receiptSecret := emptyReceiptSecret(testOm, "om-license-art-1-release-receipt")
	require.NoError(t, r.client.Create(context.Background(), &receiptSecret))
	testOm.Spec.License.ReleasedLicenses = []omv1.ReleasedLicense{releasedLicense("om-license")}
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	got := getLicenseReceiptSecret(t, r, testOm, "om-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestEnsureOpsManagerLicense_LostReceiptIsWrittenAgain(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &oldLicense)
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("", releasedArtifactView("art-1")),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	got := getLicenseReceiptSecret(t, r, testOm, "old-license-art-1-release-receipt")
	delete(got.Data, util.DefaultOpsManagerReleaseReceiptKey)
	require.NoError(t, r.client.Update(context.Background(), &got))

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	got = getLicenseReceiptSecret(t, r, testOm, "old-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_ReceiptSecretIsRebuilt(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	receiptSecret := licenseSecretFor(testOm, "old-license-art-1-release-receipt", "note", "customer note")
	receiptSecret.Labels = map[string]string{"team": "licensing"}
	r := newOmLicenseReconciler(t, testOm, &oldLicense, &receiptSecret)

	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("", releasedArtifactView("art-1")),
	}
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	got := getLicenseReceiptSecret(t, r, testOm, "old-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assert.NotContains(t, got.Data, "note")
	assert.Empty(t, got.Labels)
}

func TestEnsureOpsManagerLicense_WrittenReceiptSkipsOpsManager(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	receipt := licenseSecretFor(testOm, "old-license-art-1-release-receipt", util.DefaultOpsManagerReleaseReceiptKey, `{"artifactId":"art-1"}`)
	r := newOmLicenseReconciler(t, testOm, &oldLicense, &receipt)
	admin := &api.MockedOmAdmin{
		LicenseState:      viewWithLicenses("LICENSED", artifactView("art-1")),
		LicenseReceiptErr: assert.AnError,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_ReceiptFetchedOnlyOnce(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &oldLicense)
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("", releasedArtifactView("art-1")),
	}

	for range 5 {
		assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	}
	assert.Contains(t, r.resourceWatcher.GetWatchedResources(), getWatch(testOm.Namespace, "old-license-art-1-release-receipt", watch.Secret))

	got := getLicenseReceiptSecret(t, r, testOm, "old-license-art-1-release-receipt")
	delete(got.Data, util.DefaultOpsManagerReleaseReceiptKey)
	require.NoError(t, r.client.Update(context.Background(), &got))
	for range 3 {
		assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	}
}

func TestEnsureOpsManagerLicense_ReleasedArtifactInSecretRefFails(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef:        &omv1.SecretRef{Name: "om-license"},
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("released-license")},
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	releasedSecret := licenseSecretFor(testOm, "released-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret, &releasedSecret)
	admin := &api.MockedOmAdmin{}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Nil(t, admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_ReleasesArtifactAlreadyReleasedAtOM(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("", releasedArtifactView("art-1")),
	}

	st := ensureLicense(r, testOm, admin)
	assert.True(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_InstallsWhenSecretRefPresent(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef: &omv1.SecretRef{Name: "om-license"},
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	unlicensed := licensedView("UNLICENSED")
	licensed := viewWithLicenses("LICENSED", artifactView("art-1"))
	admin := &api.MockedOmAdmin{
		LicenseReadState: &unlicensed,
		LicenseState:     licensed,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, json.RawMessage(artifact), admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_InstallsNewerLicenseAfterOutOfBandChange(t *testing.T) {
	newArtifact := licenseArtifactJSON("art-2")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", newArtifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	manualUpload := viewWithLicenses("LICENSED", artifactView("art-ui"))
	installed := viewWithLicenses("LICENSED", artifactView("art-2"))
	admin := &api.MockedOmAdmin{
		LicenseReadState: &manualUpload,
		LicenseState:     installed,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, json.RawMessage(newArtifact), admin.InstalledLicense)
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_RenewalWritesReceiptOfReplacedArtifact(t *testing.T) {
	newArtifact := licenseArtifactJSON("art-2")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef:        &omv1.SecretRef{Name: "om-license"},
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", newArtifact)
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &licenseSecret, &oldLicense)
	admin := &api.MockedOmAdmin{
		LicenseReadState: new(viewWithLicenses("LICENSED", artifactView("art-1"))),
		LicenseState: viewWithLicenses("LICENSED",
			artifactView("art-2"),
			artifactView("art-1"),
		),
		LicenseReceipt: `{"artifactId":"art-1"}`,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, json.RawMessage(newArtifact), admin.InstalledLicense)

	got := getLicenseReceiptSecret(t, r, testOm, "old-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestEnsureOpsManagerLicense_WritesReceiptForReleasedArtifactRow(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &oldLicense)
	admin := &api.MockedOmAdmin{
		LicenseState:   viewWithLicenses("GRACE", releasedArtifactView("art-1")),
		LicenseReceipt: `{"artifactId":"art-1"}`,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	got := getLicenseReceiptSecret(t, r, testOm, "old-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestEnsureOpsManagerLicense_ReceiptPendingWhileOpsManagerHoldsArtifact(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("ea-license")},
	}
	eaLicense := licenseSecretFor(testOm, "ea-license", "license", licenseArtifactJSON("art-ea"))
	r := newOmLicenseReconciler(t, testOm, &eaLicense)
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("LICENSED", artifactView("art-ea")),
	}

	st := ensureLicense(r, testOm, admin)
	assert.True(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Contains(t, testOm.Status.OpsManagerStatus.Warnings, status.Warning(
		"Ops Manager still holds license art-ea, its release receipt is written once it is released",
	))
	err := r.client.Get(context.Background(), client.ObjectKey{Namespace: testOm.Namespace, Name: "ea-license-art-ea-release-receipt"}, &corev1.Secret{})
	assert.True(t, apiErrors.IsNotFound(err))

	admin.LicenseReadState = &api.LicenseView{Licenses: []api.LicenseArtifactView{releasedArtifactView("art-ea")}}
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	got := getLicenseReceiptSecret(t, r, testOm, "ea-license-art-ea-release-receipt")
	assert.Contains(t, got.Data, util.DefaultOpsManagerReleaseReceiptKey)
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_RenewalWithoutReceiptSkipsRelease(t *testing.T) {
	newArtifact := licenseArtifactJSON("art-2")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", newArtifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseReadState: new(viewWithLicenses("LICENSED", artifactView("art-1"))),
		LicenseState:     viewWithLicenses("LICENSED", artifactView("art-2")),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, json.RawMessage(newArtifact), admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_InstallDoesNotReleaseUnlistedLicense(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef:        &omv1.SecretRef{Name: "om-license"},
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-old"))
	r := newOmLicenseReconciler(t, testOm, &licenseSecret, &oldLicense)
	admin := &api.MockedOmAdmin{
		LicenseReadState: new(viewWithLicenses("LICENSED",
			artifactView("art-ea"),
			releasedArtifactView("art-old"),
		)),
		LicenseState: viewWithLicenses("LICENSED",
			artifactView("art-1"),
			releasedArtifactView("art-old"),
		),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, json.RawMessage(artifact), admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_SkipsReinstallWhenSameArtifactStillListed(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	sameArtifact := viewWithLicenses("LICENSED", artifactView("art-1"))
	admin := &api.MockedOmAdmin{
		LicenseReadState: &sameArtifact,
		LicenseState:     sameArtifact,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Nil(t, admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_MalformedLicenseArtifactFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: "om-license"}}
	badSecret := licenseSecretFor(testOm, "om-license", "license", "not-a-license")
	r := newOmLicenseReconciler(t, testOm, &badSecret)
	admin := &api.MockedOmAdmin{}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_ReceiptEntryWithMissingSecretFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{{
			SecretRef: omv1.SecretRef{Name: "missing-license"},
		}},
	}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_ReceiptEntryWithMalformedSecretFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{{
			SecretRef: omv1.SecretRef{Name: "malformed-license"},
		}},
	}
	malformed := licenseSecretFor(testOm, "malformed-license", "license", "not-a-license")
	r := newOmLicenseReconciler(t, testOm, &malformed)
	admin := &api.MockedOmAdmin{}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_MissingReceiptSecretIsCreated(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")},
	}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &oldLicense)
	admin := &api.MockedOmAdmin{
		LicenseState:   viewWithLicenses("", releasedArtifactView("art-1")),
		LicenseReceipt: `{"artifactId":"art-1"}`,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	got := getLicenseReceiptSecret(t, r, testOm, "old-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assertNotOwnedByOperator(t, got)
}

func TestEnsureOpsManagerLicense_ReceiptEntryOnInstalledLicenseFails(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef: &omv1.SecretRef{Name: "om-license"},
		ReleasedLicenses: []omv1.ReleasedLicense{{
			SecretRef: omv1.SecretRef{Name: "same-artifact-different-secret"},
		}},
	}
	sameArtifact := licenseSecretFor(testOm, "same-artifact-different-secret", "license", artifact)
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret, &sameArtifact)
	admin := &api.MockedOmAdmin{}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_AutoReceiptsWriteReleaseReceipt(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef:             &omv1.SecretRef{Name: "om-license"},
		ReleaseReceiptsPolicy: omv1.ReleaseReceiptsAutomatic,
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseState:   viewWithLicenses("LICENSED", artifactView("art-1")),
		LicenseReceipt: `{"artifactId":"art-1"}`,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())

	testOm.Spec.License.SecretRef = nil
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	got := getLicenseReceiptSecret(t, r, testOm, "om-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assert.Empty(t, got.OwnerReferences)
}

func TestEnsureOpsManagerLicense_AutoReceiptFailureKeepsMarkersForRetry(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef:             &omv1.SecretRef{Name: "om-license"},
		ReleaseReceiptsPolicy: omv1.ReleaseReceiptsAutomatic,
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("LICENSED", artifactView("art-1")),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())

	testOm.Spec.License.SecretRef = nil
	admin.LicenseReceiptErr = assert.AnError
	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	ref, err := r.readLicenseSecretRef(context.Background(), testOm)
	require.NoError(t, err)
	assert.Equal(t, "om-license", ref)

	admin.LicenseReceiptErr = nil
	admin.LicenseReceipt = `{"artifactId":"art-1"}`
	admin.LicenseState = viewWithLicenses("GRACE", releasedArtifactView("art-1"))
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	got := getLicenseReceiptSecret(t, r, testOm, "om-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestEnsureOpsManagerLicense_AutoReceiptsWriteRenewalReceipt(t *testing.T) {
	newArtifact := licenseArtifactJSON("art-2")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef:             &omv1.SecretRef{Name: "om-license"},
		ReleaseReceiptsPolicy: omv1.ReleaseReceiptsAutomatic,
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", newArtifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseReadState: new(viewWithLicenses("LICENSED", artifactView("art-1"))),
		LicenseState: viewWithLicenses("LICENSED",
			artifactView("art-2"),
			artifactView("art-1"),
		),
		LicenseReceipt: `{"artifactId":"art-1"}`,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, json.RawMessage(newArtifact), admin.InstalledLicense)
	// Nothing recorded the Secret art-1 came from, so the receipt is named after the artifact alone.
	got := getLicenseReceiptSecret(t, r, testOm, "art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestEnsureOpsManagerLicense_AutoReceiptsOffByDefault(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef: &omv1.SecretRef{Name: "om-license"},
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseReadState: new(viewWithLicenses("LICENSED", artifactView("art-1"))),
		LicenseState:     viewWithLicenses("LICENSED", artifactView("art-2")),
		LicenseReceipt:   `{"artifactId":"art-1"}`,
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	err := r.client.Get(context.Background(), client.ObjectKey{Namespace: testOm.Namespace, Name: "om-license-art-1-release-receipt"}, &corev1.Secret{})
	assert.True(t, apiErrors.IsNotFound(err))
}

func TestEnsureOpsManagerLicense_ReleaseReceiptReadFailureIsRetried(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleaseReceiptsPolicy: omv1.ReleaseReceiptsAutomatic,
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{
		LicenseState:      viewWithLicenses("LICENSED", artifactView("art-1")),
		LicenseReceiptErr: assert.AnError,
	}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)

	admin.LicenseReceiptErr = nil
	admin.LicenseState = viewWithLicenses("GRACE", releasedArtifactView("art-1"))
	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	got := getLicenseReceiptSecret(t, r, testOm, "om-license-art-1-release-receipt")
	assert.Equal(t, `{"artifactId":"art-1"}`, string(got.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestEnsureOpsManagerLicense_ReleaseWhenOMHasNoLicenseClearsStaleStatus(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{
		LicenseState: licensedView("UNLICENSED"),
	}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Contains(t, licenseActiveCondition(t, testOm).Message, "no license")
}

func TestOpsManagerReconciler_watchesLicenseSecrets(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		SecretRef: &omv1.SecretRef{Name: "om-license"},
	}
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	t.Cleanup(func() { api.CurrMockedAdmin = nil })

	reconciler.watchLicenseSecrets(testOm)

	licenseKey := getWatch(testOm.Namespace, "om-license", watch.Secret)
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources(), licenseKey)
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources()[licenseKey], mock.ObjectKeyFromApiObject(testOm))
}

func TestEnsureOpsManagerLicense_AutoReceiptSkippedWhenOpsManagerLostArtifact(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{ReleaseReceiptsPolicy: omv1.ReleaseReceiptsAutomatic}
	r := newOmLicenseReconciler(t, testOm)
	seedInstalled(t, r, testOm, "om-license")
	admin := &api.MockedOmAdmin{LicenseReceiptErr: assert.AnError}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_AutoReceiptNotFoundFails(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{ReleaseReceiptsPolicy: omv1.ReleaseReceiptsAutomatic}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	seedInstalled(t, r, testOm, "om-license")
	notFound := http.StatusNotFound
	admin := &api.MockedOmAdmin{
		LicenseState:      viewWithLicenses("", releasedArtifactView("art-1")),
		LicenseReceiptErr: &apierror.Error{Status: &notFound},
	}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_ListedReceiptNotFoundStillFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")}}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	r := newOmLicenseReconciler(t, testOm, &oldLicense)
	notFound := http.StatusNotFound
	admin := &api.MockedOmAdmin{LicenseReceiptErr: &apierror.Error{Status: &notFound}}

	st := ensureLicense(r, testOm, admin)
	assert.False(t, st.IsOK())
	assertOperatorManagedLicenseReason(t, st.StatusOptions(), status.ReasonLicenseFailed)
}

func TestEnsureOpsManagerLicense_ReceiptCheckReadsKubernetes(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{ReleasedLicenses: []omv1.ReleasedLicense{releasedLicense("old-license")}}
	oldLicense := licenseSecretFor(testOm, "old-license", "license", licenseArtifactJSON("art-1"))
	written := corev1.Secret{}
	written.Name = "old-license-art-1-release-receipt"
	written.Namespace = testOm.Namespace
	written.Data = map[string][]byte{util.DefaultOpsManagerReleaseReceiptKey: []byte(`{"artifactId":"art-1"}`)}
	r := newOmLicenseReconciler(t, testOm, &oldLicense, &written)
	// A nil VaultClient keeps the shared secret client on Kubernetes, the check must use r.client either way.
	admin := &api.MockedOmAdmin{LicenseReceiptErr: assert.AnError}

	assert.True(t, ensureLicense(r, testOm, admin).IsOK())
}

func newOmLicenseReconciler(t *testing.T, opsManager *omv1.MongoDBOpsManager, extra ...client.Object) *OpsManagerReconciler {
	t.Helper()
	objects := append([]client.Object{opsManager.DeepCopy()}, extra...)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory, objects...)
	// The controller updates the resource it read, so sync the version the client holds.
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(opsManager), opsManager))
	return NewOpsManagerReconciler(context.Background(), kubeClient, nil, images.ImageUrls{}, "", "", architectures.NonStatic, "", omConnectionFactory.GetConnectionFunc, nil, nil)
}

func emptyReceiptSecret(opsManager *omv1.MongoDBOpsManager, name string) corev1.Secret {
	return corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: opsManager.Namespace}}
}

func releasedLicense(licenseSecretName string) omv1.ReleasedLicense {
	return omv1.ReleasedLicense{SecretRef: omv1.SecretRef{Name: licenseSecretName}}
}

func getLicenseReceiptSecret(t *testing.T, r *OpsManagerReconciler, opsManager *omv1.MongoDBOpsManager, name string) corev1.Secret {
	t.Helper()
	got := corev1.Secret{}
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKey{Namespace: opsManager.Namespace, Name: name}, &got))
	return got
}

func assertNotOwnedByOperator(t *testing.T, s corev1.Secret) {
	t.Helper()
	assert.Empty(t, s.OwnerReferences)
	assert.Empty(t, s.Labels)
}

func licenseSecretFor(opsManager *omv1.MongoDBOpsManager, name, key, value string) corev1.Secret {
	return secret.Builder().
		SetName(name).
		SetNamespace(opsManager.Namespace).
		SetStringMapToData(map[string]string{key: value}).
		Build()
}

// licenseArtifactJSON builds a signed-artifact envelope whose decoded claims carry artifactID,
// the shape EA issues and OM verifies.
func licenseArtifactJSON(artifactID string) string {
	claims, err := json.Marshal(map[string]string{"artifact_id": artifactID})
	if err != nil {
		panic(err)
	}
	return `{"payload":"` + base64.StdEncoding.EncodeToString(claims) + `"}`
}

func setLicenseRotateIdentityAnnotation(t *testing.T, ctx context.Context, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager, value string) {
	t.Helper()
	require.NoError(t, annotations.SetAnnotations(ctx, opsManager, map[string]string{
		util.LicenseRotateIdentityAnnotation: value,
	}, kubeClient))
}

func setLicenseRecomputeAnnotation(t *testing.T, ctx context.Context, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager, value string) {
	t.Helper()
	require.NoError(t, annotations.SetAnnotations(ctx, opsManager, map[string]string{
		util.LicenseRecomputeAnnotation: value,
	}, kubeClient))
}

func TestLicenseStateReason(t *testing.T) {
	for _, state := range []string{"GRACE", "UNLICENSED", "EXPIRED"} {
		st, reason := licenseStateCondition(state)
		assert.Equal(t, metav1.ConditionFalse, st)
		assert.Equal(t, status.ReasonLicenseInactive, reason)
	}

	st, reason := licenseStateCondition("SOMETHING_NEW")
	assert.Equal(t, metav1.ConditionUnknown, st)
	assert.Equal(t, status.ReasonLicenseUnknown, reason)
}
