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
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

func newLicenseStatus(t testing.TB, view omv1.OpsManagerLicenseStatus, installedArtifactID string) *omv1.OpsManagerLicenseStatus {
	t.Helper()
	status, err := licenseStatusFromView(view, nil)
	require.NoError(t, err)
	status.InstalledArtifactID = installedArtifactID
	return &status
}

func licenseStatusFromView(view omv1.OpsManagerLicenseStatus, prev *omv1.OpsManagerLicenseStatus) (omv1.OpsManagerLicenseStatus, error) {
	raw, err := json.Marshal(view)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	var state omv1.OpsManagerLicenseStatus
	if err := json.Unmarshal(raw, &state); err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	return omv1.LicenseStatusFromState(state, prev), nil
}

func licenseViewFromStatus(t testing.TB, status *omv1.OpsManagerLicenseStatus) omv1.OpsManagerLicenseStatus {
	t.Helper()
	require.NotNil(t, status)
	return *status
}

func TestMongoDBOpsManagerLicense_InstallRequested(t *testing.T) {
	assert.False(t, (*omv1.MongoDBOpsManagerLicense)(nil).InstallRequested())
	assert.False(t, (&omv1.MongoDBOpsManagerLicense{}).InstallRequested())
	assert.True(t, (&omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}).InstallRequested())
	assert.False(t, (&omv1.MongoDBOpsManagerLicense{}).ReceiptsRequested())
	assert.False(t, (*omv1.MongoDBOpsManagerLicense)(nil).AutoReleaseReceiptsRequested())
	assert.False(t, (&omv1.MongoDBOpsManagerLicense{}).AutoReleaseReceiptsRequested())
	listed := &omv1.MongoDBOpsManagerLicense{ReleaseReceipts: []omv1.LicenseReleaseReceipt{releaseReceipt("art-1")}}
	assert.True(t, listed.ReceiptsRequested())
	listed.AutoReleaseReceipts = true
	assert.True(t, listed.AutoReleaseReceiptsRequested())
	assert.True(t, listed.HasReleaseReceipt("art-1"))
	assert.False(t, listed.HasReleaseReceipt("art-2"))
	assert.False(t, (*omv1.MongoDBOpsManagerLicense)(nil).HasReleaseReceipt("art-1"))
}

func TestMongoDBOpsManager_LicenseReleaseDueToRemovedInstallIntent(t *testing.T) {
	om := DefaultOpsManagerBuilder().Build()
	assert.False(t, om.LicenseReleaseDueToRemovedInstallIntent())

	om.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	om.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{InstalledArtifactID: "art-1"}
	assert.False(t, om.LicenseReleaseDueToRemovedInstallIntent())

	om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	assert.True(t, om.LicenseReleaseDueToRemovedInstallIntent())

	om.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{}
	assert.False(t, om.LicenseReleaseDueToRemovedInstallIntent())
}

func TestLicenseStatusFromView(t *testing.T) {
	view := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{
			EffectiveState: "UNLICENSED",
			Enforced:       true,
			Cause:          "no-coverage:UNLICENSED",
			Transition: &omv1.OpsManagerLicenseEnforcementTransitionStatus{
				EvaluatorID:  "enforcement-evaluator",
				InStateUntil: "2026-06-01T00:00:00Z",
				NextState:    "UNLICENSED",
			},
			Verdicts: []omv1.OpsManagerLicenseVerdictStatus{{
				EvaluatorID:  "verdict-evaluator",
				State:        "LICENSED",
				WarningStart: "2026-04-01T00:00:00Z",
				Transition: &omv1.OpsManagerLicenseVerdictTransitionStatus{
					InStateUntil: "2026-05-01T00:00:00Z",
					NextState:    "GRACE",
				},
			}},
		},
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{
			ArtifactID:   "a1",
			Usable:       false,
			GeneratedAt:  "2026-01-01T00:00:00Z",
			InstalledAt:  "2026-01-02T00:00:00Z",
			Status:       "DENIED",
			StatusCause:  "CLAIM_CONFLICT",
			StatusReason: "the claim for this artifact is held by another deployment",
		}},
		ActiveEntitlements: map[string][]omv1.OpsManagerLicenseEntitlementStatus{
			"exemption::enforcement": {{
				Type:   "exemption::enforcement",
				Active: true,
				Window: &omv1.OpsManagerLicenseEntitlementWindowStatus{Start: "2026-01-01T00:00:00Z", End: "2026-12-31T23:59:59Z"},
			}},
		},
	}
	got, err := licenseStatusFromView(view, &omv1.OpsManagerLicenseStatus{InstalledArtifactID: "a1"})
	require.NoError(t, err)
	gotView := licenseViewFromStatus(t, &got)
	require.NotNil(t, gotView.Enforcement)
	assert.Equal(t, "UNLICENSED", gotView.Enforcement.EffectiveState)
	assert.True(t, gotView.Enforcement.Enforced)
	assert.Equal(t, "no-coverage:UNLICENSED", gotView.Enforcement.Cause)
	require.NotNil(t, gotView.Enforcement.Transition)
	assert.Equal(t, "enforcement-evaluator", gotView.Enforcement.Transition.EvaluatorID)
	assert.Equal(t, "2026-06-01T00:00:00Z", gotView.Enforcement.Transition.InStateUntil)
	assert.Equal(t, "UNLICENSED", gotView.Enforcement.Transition.NextState)
	require.Len(t, gotView.Enforcement.Verdicts, 1)
	require.NotNil(t, gotView.Enforcement.Verdicts[0].Transition)
	assert.Equal(t, "2026-05-01T00:00:00Z", gotView.Enforcement.Verdicts[0].Transition.InStateUntil)
	assert.Equal(t, "GRACE", gotView.Enforcement.Verdicts[0].Transition.NextState)
	require.Len(t, gotView.Licenses, 1)
	assert.Equal(t, "a1", gotView.Licenses[0].ArtifactID)
	assert.False(t, gotView.Licenses[0].Usable)
	assert.Equal(t, "2026-01-01T00:00:00Z", gotView.Licenses[0].GeneratedAt)
	assert.Equal(t, "2026-01-02T00:00:00Z", gotView.Licenses[0].InstalledAt)
	assert.Equal(t, "DENIED", gotView.Licenses[0].Status)
	assert.Equal(t, "CLAIM_CONFLICT", gotView.Licenses[0].StatusCause)
	assert.Equal(t, "the claim for this artifact is held by another deployment", gotView.Licenses[0].StatusReason)
	assert.Equal(t, "a1", got.InstalledArtifactID)
	require.Len(t, gotView.ActiveEntitlements["exemption::enforcement"], 1)
	assert.Equal(t, "exemption::enforcement", gotView.ActiveEntitlements["exemption::enforcement"][0].Type)
	assert.True(t, gotView.ActiveEntitlements["exemption::enforcement"][0].Active)
	assert.Equal(t, "2026-01-01T00:00:00Z", gotView.ActiveEntitlements["exemption::enforcement"][0].Window.Start)
}

func TestEnsureOpsManagerLicense_PersistsTypedAPIState(t *testing.T) {
	const apiResponse = `{
		"enforcement": {
			"effectiveState": "GRACE",
			"cause": "term:GRACE",
			"enforced": false,
			"transition": {
				"evaluatorId": "enforcement-evaluator",
				"inStateUntil": "2026-06-01T00:00:00Z",
				"nextState": "EXPIRED"
			},
			"verdicts": [{
				"evaluatorId": "verdict-evaluator",
				"state": "LICENSED",
				"warningStart": "2026-04-01T00:00:00Z",
				"transition": {
					"inStateUntil": "2026-05-01T00:00:00Z",
					"nextState": "GRACE"
				}
			}]
		},
		"licenses": [{
			"artifactId": "art-1",
			"usable": true,
			"generatedAt": "2026-01-01T00:00:00Z",
			"installedAt": "2026-01-02T00:00:00Z",
			"status": "ACTIVE",
			"termEnd": "2026-12-31T23:59:59Z"
		}],
		"activeEntitlements": {
			"exemption::enforcement": [{
				"type": "exemption::enforcement",
				"productName": "Ops Manager",
				"active": true
			}]
		},
		"futureField": {
			"preserved": true
		}
	}`
	testOm := DefaultOpsManagerBuilder().Build()
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{LicenseState: json.RawMessage(apiResponse)}

	require.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	require.NotNil(t, testOm.Status.OpsManagerStatus.License)
	require.NotNil(t, testOm.Status.OpsManagerStatus.License.Enforcement)
	assert.Equal(t, "GRACE", testOm.Status.OpsManagerStatus.License.Enforcement.EffectiveState)
	assert.False(t, testOm.Status.OpsManagerStatus.License.Enforcement.Enforced)
	require.Len(t, testOm.Status.OpsManagerStatus.License.Licenses, 1)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.Licenses[0].ArtifactID)
	require.Len(t, testOm.Status.OpsManagerStatus.License.ActiveEntitlements["exemption::enforcement"], 1)
}

func TestEnsureOpsManagerLicense_ProvisioningModels(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	licensed := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
	}
	eaLicensed := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "ea-auto", Usable: true}},
	}

	tests := []struct {
		name            string
		configure       func(*omv1.MongoDBOpsManager) []client.Object
		licenseState    omv1.OpsManagerLicenseStatus
		wantInstalled   bool
		wantInstallMark bool
		wantArtifact    string
	}{
		{
			name:         "connected EA observes OM state without spec.license",
			configure:    func(*omv1.MongoDBOpsManager) []client.Object { return nil },
			licenseState: eaLicensed,
			wantArtifact: "ea-auto",
		},
		{
			name: "spec.license without licenseSecretRef observes OM state",
			configure: func(om *omv1.MongoDBOpsManager) []client.Object {
				om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
				return nil
			},
			licenseState: eaLicensed,
			wantArtifact: "ea-auto",
		},
		{
			name: "GitOps licenseSecretRef installs from Kubernetes Secret",
			configure: func(om *omv1.MongoDBOpsManager) []client.Object {
				om.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
				s := licenseSecretFor(om, "om-license", "license", artifact)
				return []client.Object{&s}
			},
			licenseState:    licensed,
			wantInstalled:   true,
			wantInstallMark: true,
			wantArtifact:    "art-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testOm := DefaultOpsManagerBuilder().Build()
			extra := tc.configure(testOm)
			r := newOmLicenseReconciler(t, testOm, extra...)
			admin := &api.MockedOmAdmin{LicenseState: tc.licenseState}

			st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
			assert.True(t, st.IsOK())

			if tc.wantInstalled {
				assert.Equal(t, json.RawMessage(artifact), admin.InstalledLicense)
			} else {
				assert.Nil(t, admin.InstalledLicense)
			}
			require.NotNil(t, testOm.Status.OpsManagerStatus.License)
			assert.Equal(t, "LICENSED", licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
			require.Len(t, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Licenses, 1)
			assert.Equal(t, tc.wantArtifact, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Licenses[0].ArtifactID)
			opt, found := status.GetOption(st.StatusOptions(), status.OmLicenseReconciledStatusOption{})
			require.True(t, found)
			c := opt.(status.OmLicenseReconciledStatusOption).Condition
			assert.Equal(t, status.ConditionLicenseReconciled, c.Type)
			assert.Equal(t, metav1.ConditionTrue, c.Status)
			assert.Equal(t, status.ReasonLicenseReconciled, c.Reason)
			if tc.wantInstallMark {
				assert.Equal(t, tc.wantArtifact, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
			} else {
				assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
			}
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

			assert.Equal(t, tc.wantOK, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
			assert.Nil(t, testOm.Status.OpsManagerStatus.License)
		})
	}
}

func TestEnsureOpsManagerLicense_WritesStatusFromGet(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false, Cause: "expiring"},
			Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1"}},
		},
	}
	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	require.NotNil(t, testOm.Status.OpsManagerStatus.License)
	require.NotNil(t, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement)
	assert.Equal(t, "GRACE", licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
	assert.False(t, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement.Enforced)
	assert.Equal(t, []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1"}}, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Licenses)
}

func TestEnsureOpsManagerLicense_InstallsWhenArtifactChanges(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, json.RawMessage(artifact), admin.InstalledLicense)
	require.NotNil(t, testOm.Status.OpsManagerStatus.License)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
	require.Len(t, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Licenses, 1)
	assert.Equal(t, "art-1", licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Licenses[0].ArtifactID)

	admin.InstalledLicense = nil
	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Nil(t, admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_ObservesOMChangesWhenSecretUnchanged(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	require.NotNil(t, admin.InstalledLicense)

	admin.InstalledLicense = nil
	admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false, Cause: "expiring"},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
		ActiveEntitlements: map[string][]omv1.OpsManagerLicenseEntitlementStatus{
			"exemption::enforcement": {{Type: "exemption::enforcement", Active: true}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assert.Equal(t, "GRACE", licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
	require.Len(t, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).ActiveEntitlements["exemption::enforcement"], 1)
}

func TestEnsureOpsManagerLicense_DoesNotReinstallAfterOutOfBandRemoval(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}, "art-1")
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	uncovered := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}
	admin := &api.MockedOmAdmin{LicenseState: uncovered}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.True(t, st.IsOK())
	assert.Nil(t, admin.InstalledLicense)
	require.NotNil(t, licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement)
	assert.Equal(t, "GRACE", licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
	assert.Contains(t, testOm.Status.OpsManagerStatus.Warnings, status.Warning(
		"Ops Manager no longer holds the license last installed by the operator",
	))
}

func TestEnsureOpsManagerLicense_SkipsReinstallWhenOMStillLicensed(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}, "art-1")
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
			Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Nil(t, admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_ReleaseLeavesAForeignArtifactAlone(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{}, "art-1")
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: false},
			Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-ea", Usable: true}},
		},
	}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.True(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, "art-ea", admin.LicenseState.(omv1.OpsManagerLicenseStatus).Licenses[0].ArtifactID)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_ReleaseFailsWhenLicenseGetFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "art-1",
	}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{LicenseReadErr: assert.AnError}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.False(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_InstallFailed(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{LicenseInstallErr: assert.AnError}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.False(t, st.IsOK())

	opt, found := status.GetOption(st.StatusOptions(), status.OmLicenseReconciledStatusOption{})
	require.True(t, found)
	c := opt.(status.OmLicenseReconciledStatusOption).Condition
	assert.Equal(t, status.ConditionLicenseReconciled, c.Type)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, status.ReasonLicenseInstallFailed, c.Reason)
}

func TestEnsureOpsManagerLicense_InstallConflictWithArtifactHeld(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)

	conflict := http.StatusConflict
	admin := &api.MockedOmAdmin{
		LicenseInstallErr: &apierror.Error{Status: &conflict},
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1"}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, "art-1", testOm.GetInstalledArtifactID())
}

func TestEnsureOpsManagerLicense_InstallConflictWithNewerArtifactHeld(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)

	conflict := http.StatusConflict
	admin := &api.MockedOmAdmin{
		LicenseInstallErr: &apierror.Error{Status: &conflict, ErrorCode: "ARTIFACT_OUTDATED", Detail: "artifact is not newer than the installed one"},
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-newer"}},
		},
	}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.False(t, st.IsOK())
	assert.Empty(t, testOm.GetInstalledArtifactID())

	opt, found := status.GetOption(st.StatusOptions(), status.OmLicenseReconciledStatusOption{})
	require.True(t, found)
	c := opt.(status.OmLicenseReconciledStatusOption).Condition
	assert.Equal(t, status.ReasonLicenseInstallFailed, c.Reason)
	assert.Contains(t, c.Message, "rejected license art-1")
	assert.Contains(t, c.Message, "ErrorCode: ARTIFACT_OUTDATED")
	assert.Contains(t, c.Message, "artifact is not newer than the installed one")
}

func TestEnsureOpsManagerLicense_AutoReleaseOnSecretRefRemoval(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}, "art-1")
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
	assertReleasedArtifact(t, testOm, "art-1")
}

func TestEnsureOpsManagerLicense_ReleaseOnLicenseSpecRemoval(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = nil
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}, "art-1")
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_AutoReleaseClearsMarkersWhenAlreadyUncovered(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}, "art-1")
	r := newOmLicenseReconciler(t, testOm)
	uncovered := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}
	admin := &api.MockedOmAdmin{
		LicenseReadState: &uncovered,
		LicenseState:     uncovered,
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_DoesNotReleaseWithoutPriorInstall(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_ReleaseWithoutReceipt(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}, "art-1")
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, []string{"art-1"}, admin.ReleasedIDs)
	assertReleasedArtifact(t, testOm, "art-1")
}

func TestEnsureOpsManagerLicense_ReleasesWhenOMStillHoldsInstalledArtifact(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "om-art",
	}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "om-art", Usable: true}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, []string{"om-art"}, admin.ReleasedIDs)
}

func TestEnsureOpsManagerLicense_ReleasesArtifactAlreadyReleasedAtOM(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Status: omv1.LicenseArtifactStatusReleased}},
	}, "art-1")
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Status: omv1.LicenseArtifactStatusReleased}},
		},
	}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.True(t, st.IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_ReleaseDoesNotReinstallWithoutSecretRef(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		ReleaseReceipts: []omv1.LicenseReleaseReceipt{releaseReceipt("art-1")},
	}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}, "art-1")
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	uncovered := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Status: omv1.LicenseArtifactStatusReleased}},
	}
	admin := &api.MockedOmAdmin{
		LicenseReadState: &uncovered,
		LicenseState:     uncovered,
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)

	admin.InstalledLicense = nil
	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Nil(t, admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_SkipsInstallWithoutSecretRef(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "UNLICENSED", Enforced: true},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assert.Equal(t, "UNLICENSED", licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
}

func TestEnsureOpsManagerLicense_InstallsWhenSecretRefPresent(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{
		LicenseSecretRef: &omv1.SecretRef{Name: "om-license"},
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	unlicensed := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "UNLICENSED", Enforced: true},
	}
	licensed := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}
	admin := &api.MockedOmAdmin{
		LicenseReadState: &unlicensed,
		LicenseState:     licensed,
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, json.RawMessage(artifact), admin.InstalledLicense)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_DoesNotReinstallWhenWrongArtifactInOM(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}, "art-1")
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	wrongLicense := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-other", Usable: true}},
	}
	admin := &api.MockedOmAdmin{LicenseState: wrongLicense}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.True(t, st.IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assert.Equal(t, "art-other", licenseViewFromStatus(t, testOm.Status.OpsManagerStatus.License).Licenses[0].ArtifactID)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
	assert.Contains(t, testOm.Status.OpsManagerStatus.Warnings, status.Warning(
		"Ops Manager no longer holds the license last installed by the operator",
	))
}

func TestEnsureOpsManagerLicense_InstallsNewerLicenseAfterOutOfBandChange(t *testing.T) {
	newArtifact := licenseArtifactJSON("art-2")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	testOm.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "art-1",
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", newArtifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	manualUpload := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-ui", Usable: true}},
	}
	installed := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-2", Usable: true}},
	}
	admin := &api.MockedOmAdmin{
		LicenseReadState: &manualUpload,
		LicenseState:     installed,
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, json.RawMessage(newArtifact), admin.InstalledLicense)
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, "art-2", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_RenewalWithoutReceiptSkipsRelease(t *testing.T) {
	newArtifact := licenseArtifactJSON("art-2")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	testOm.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "art-1",
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", newArtifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseReadState: &omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
		},
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
			Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-2", Usable: true}},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Equal(t, json.RawMessage(newArtifact), admin.InstalledLicense)
	assert.Equal(t, "art-2", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_DriftDoesNotReinstallOnLaterPasses(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	testOm.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "art-1",
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	unlicensed := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "UNLICENSED", Enforced: true},
	}
	admin := &api.MockedOmAdmin{LicenseState: unlicensed}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_SkipsReinstallWhenSameArtifactStillListed(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	testOm.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "art-1",
	}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	sameArtifact := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: false},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}
	admin := &api.MockedOmAdmin{
		LicenseReadState: &sameArtifact,
		LicenseState:     sameArtifact,
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Nil(t, admin.InstalledLicense)
}

func TestEnsureOpsManagerLicense_MissingLicenseSecretFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	r := newOmLicenseReconciler(t, testOm)
	st := r.ensureOpsManagerLicense(context.Background(), testOm, &api.MockedOmAdmin{}, zap.S())
	assert.False(t, st.IsOK())
}

func TestEnsureOpsManagerLicense_MissingLicenseSecretKeyFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	badSecret := licenseSecretFor(testOm, "om-license", "other-key", `{}`)
	r := newOmLicenseReconciler(t, testOm, &badSecret)
	st := r.ensureOpsManagerLicense(context.Background(), testOm, &api.MockedOmAdmin{}, zap.S())
	assert.False(t, st.IsOK())
}

func TestEnsureOpsManagerLicense_MalformedLicenseArtifactFails(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	badSecret := licenseSecretFor(testOm, "om-license", "license", "not-a-license")
	r := newOmLicenseReconciler(t, testOm, &badSecret)
	admin := &api.MockedOmAdmin{}

	st := r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S())
	assert.False(t, st.IsOK())
	assert.Nil(t, admin.InstalledLicense)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
	assertLicenseReconcileReason(t, st.StatusOptions(), status.ReasonLicenseArtifactInvalid)
}

func TestEnsureOpsManagerLicense_InstallSucceedsWhenStateListsExtraArtifacts(t *testing.T) {
	artifact := licenseArtifactJSON("art-1")
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{LicenseSecretRef: &omv1.SecretRef{Name: "om-license"}}
	licenseSecret := licenseSecretFor(testOm, "om-license", "license", artifact)
	r := newOmLicenseReconciler(t, testOm, &licenseSecret)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Licenses: []omv1.OpsManagerLicenseArtifactStatus{
				{ArtifactID: "art-1", Usable: true},
				{ArtifactID: "art-2", Usable: true},
			},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Equal(t, "art-1", testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestEnsureOpsManagerLicense_ReleaseWhenOMHasNoLicenseClearsStaleStatus(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	testOm.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{
			{ArtifactID: "art-1", Usable: true},
			{ArtifactID: "art-2", Usable: true},
		},
	}, "art-1")
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: omv1.OpsManagerLicenseStatus{
			Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "UNLICENSED", Enforced: true},
		},
	}

	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	assert.Empty(t, admin.ReleasedIDs)
	assert.Empty(t, testOm.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func newOmLicenseReconciler(t *testing.T, opsManager *omv1.MongoDBOpsManager, extra ...client.Object) *OpsManagerReconciler {
	t.Helper()
	objects := append([]client.Object{opsManager.DeepCopy()}, extra...)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory, objects...)
	return NewOpsManagerReconciler(context.Background(), kubeClient, nil, images.ImageUrls{}, "", "", architectures.NonStatic, omConnectionFactory.GetConnectionFunc, nil, nil)
}

func releaseReceipt(artifactID string) omv1.LicenseReleaseReceipt {
	return omv1.LicenseReleaseReceipt{ArtifactID: artifactID}
}

func assertReleasedArtifact(t *testing.T, opsManager *omv1.MongoDBOpsManager, artifactID string) {
	t.Helper()
	licenses := licenseViewFromStatus(t, opsManager.Status.OpsManagerStatus.License).Licenses
	require.Len(t, licenses, 1)
	assert.Equal(t, artifactID, licenses[0].ArtifactID)
	assert.Equal(t, omv1.LicenseArtifactStatusReleased, licenses[0].Status)
	assert.False(t, licenses[0].Usable)
}

func assertLicenseReconcileReason(t *testing.T, options []status.Option, reason string) {
	t.Helper()
	opt, found := status.GetOption(options, status.OmLicenseReconciledStatusOption{})
	require.True(t, found)
	assert.Equal(t, reason, opt.(status.OmLicenseReconciledStatusOption).Condition.Reason)
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
