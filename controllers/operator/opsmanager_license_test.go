package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

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

func newOmLicenseReconciler(t *testing.T, opsManager *omv1.MongoDBOpsManager, extra ...client.Object) *OpsManagerReconciler {
	t.Helper()
	objects := append([]client.Object{opsManager.DeepCopy()}, extra...)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory, objects...)
	return NewOpsManagerReconciler(context.Background(), kubeClient, nil, images.ImageUrls{}, "", "", architectures.NonStatic, omConnectionFactory.GetConnectionFunc, nil, nil)
}
