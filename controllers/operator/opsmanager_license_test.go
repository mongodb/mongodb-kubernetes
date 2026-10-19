package operator

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

func viewWithLicenses(state string, licenses ...api.LicenseArtifactView) api.LicenseView {
	return api.LicenseView{
		Enforcement: &api.LicenseEnforcementView{EffectiveState: state},
		Licenses:    licenses,
	}
}

func artifactView(artifactID string) api.LicenseArtifactView {
	return api.LicenseArtifactView{ArtifactID: artifactID, Usable: true, Status: "ACTIVE"}
}

func licenseReadyCondition(t *testing.T, om *omv1.MongoDBOpsManager) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(om.Status.OpsManagerStatus.Conditions, status.ConditionOpsManagerLicenseReady)
	require.NotNil(t, c)
	return *c
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

			assert.Equal(t, tc.wantOK, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
		})
	}
}

func TestEnsureOpsManagerLicense_WritesReadyConditionFromGet(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	r := newOmLicenseReconciler(t, testOm)
	admin := &api.MockedOmAdmin{
		LicenseState: viewWithLicenses("GRACE", artifactView("art-1")),
	}
	assert.True(t, r.ensureOpsManagerLicense(context.Background(), testOm, admin, zap.S()).IsOK())
	ready := licenseReadyCondition(t, testOm)
	assert.Equal(t, "LicenseGrace", ready.Reason)
	assert.Contains(t, ready.Message, "License state is GRACE.")
	assert.Contains(t, ready.Message, "license art-1")
}

func newOmLicenseReconciler(t *testing.T, opsManager *omv1.MongoDBOpsManager, extra ...client.Object) *OpsManagerReconciler {
	t.Helper()
	objects := append([]client.Object{opsManager.DeepCopy()}, extra...)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory, objects...)
	return NewOpsManagerReconciler(context.Background(), kubeClient, nil, images.ImageUrls{}, "", "", architectures.NonStatic, "", omConnectionFactory.GetConnectionFunc, nil, nil)
}
