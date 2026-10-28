package operator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	envtestlib "github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

const testLicenseArtifactID = "art-1"

var testLicenseArtifact = licenseArtifactJSON(testLicenseArtifactID)

type licenseEnvHarness struct {
	t          *testing.T
	ctx        context.Context
	client     client.Client
	reconciler *OpsManagerReconciler
	admin      *api.MockedOmAdmin
	namespace  string
}

func newLicenseEnvHarness(t *testing.T) *licenseEnvHarness {
	t.Helper()
	ctx := context.Background()
	k8sClient := envtestlib.Shared(t).Client

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler := NewOpsManagerReconciler(ctx, k8sClient, nil, images.ImageUrls{}, "", "", architectures.NonStatic, omConnectionFactory.GetConnectionFunc, nil, nil)
	t.Cleanup(func() { api.CurrMockedAdmin = nil })

	return &licenseEnvHarness{
		t:          t,
		ctx:        ctx,
		client:     k8sClient,
		reconciler: reconciler,
		admin:      &api.MockedOmAdmin{},
		namespace:  "default",
	}
}

func (h *licenseEnvHarness) createOM(mutate func(*omv1.MongoDBOpsManager)) *omv1.MongoDBOpsManager {
	h.t.Helper()
	om := DefaultOpsManagerBuilder().Build()
	om.Namespace = h.namespace
	om.Name = ""
	om.GenerateName = "om-lic-"
	om.ResourceVersion = ""
	om.UID = ""
	if om.Spec.AppDB != nil {
		// The CRD still requires this deprecated field to be present.
		//nolint:staticcheck
		om.Spec.AppDB.MonitoringAgent.StartupParameters = mdbv1.StartupParameters{}
	}
	if mutate != nil {
		mutate(om)
	}
	require.NoError(h.t, h.client.Create(h.ctx, om))
	h.t.Cleanup(func() { _ = h.client.Delete(h.ctx, om) })
	return om
}

func (h *licenseEnvHarness) createSecret(name, key, value string, owner *omv1.MongoDBOpsManager) {
	h.t.Helper()
	var secretObj corev1.Secret
	if owner != nil {
		secretObj = licenseSecretFor(owner, name, key, value)
	} else {
		secretObj = licenseSecretFor(&omv1.MongoDBOpsManager{ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace}}, name, key, value)
	}
	require.NoError(h.t, h.client.Create(h.ctx, &secretObj))
}

func (h *licenseEnvHarness) loadOM(name string) *omv1.MongoDBOpsManager {
	h.t.Helper()
	got := &omv1.MongoDBOpsManager{}
	require.NoError(h.t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: name}, got))
	return got
}

func (h *licenseEnvHarness) reconcile(name string) workflow.Status {
	h.t.Helper()
	om := h.loadOM(name)
	st := h.reconciler.ensureOpsManagerLicense(h.ctx, om, h.admin, zap.NewNop().Sugar())
	status := om.Status
	om = h.loadOM(name)
	om.Status = status
	require.NoError(h.t, h.client.Status().Update(h.ctx, om))
	return st
}

func (h *licenseEnvHarness) reconcileExpectOK(name string) *omv1.MongoDBOpsManager {
	h.t.Helper()
	assert.True(h.t, h.reconcile(name).IsOK())
	return h.loadOM(name)
}

func (h *licenseEnvHarness) updateSpec(name string, mutate func(*omv1.MongoDBOpsManager)) {
	h.t.Helper()
	om := h.loadOM(name)
	mutate(om)
	require.NoError(h.t, h.client.Update(h.ctx, om))
}

func (h *licenseEnvHarness) setRotateAnnotation(name, value string) {
	h.t.Helper()
	om := h.loadOM(name)
	require.NoError(h.t, annotations.SetAnnotations(h.ctx, om, map[string]string{
		util.LicenseRotateIdentityAnnotation: value,
	}, h.client))
}

func (h *licenseEnvHarness) setRecomputeAnnotation(name, value string) {
	h.t.Helper()
	om := h.loadOM(name)
	require.NoError(h.t, annotations.SetAnnotations(h.ctx, om, map[string]string{
		util.LicenseRecomputeAnnotation: value,
	}, h.client))
}

func (h *licenseEnvHarness) setInstallFromSecret(om *omv1.MongoDBOpsManager, artifact string) {
	h.t.Helper()
	secretName := om.Name + "-license"
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			LicenseSecretRef: &omv1.SecretRef{Name: secretName},
		}
	})
	h.createSecret(secretName, util.DefaultOpsManagerLicenseKey, artifact, om)
}

func (h *licenseEnvHarness) seedInstalled(name, artifactID string) {
	h.t.Helper()
	om := h.loadOM(name)
	om.Status.OpsManagerStatus.License = newLicenseStatus(h.t, omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: artifactID, Usable: true}},
	}, artifactID)
	require.NoError(h.t, h.client.Status().Update(h.ctx, om))
}

func TestLicenseE2E_InstallFromSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
	}

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)

	got := h.reconcileExpectOK(om.Name)
	require.NotNil(t, got.Status.OpsManagerStatus.License)
	assert.Equal(t, "LICENSED", licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)
	require.NotNil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_ObserveOnlyWithoutSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "ea-auto", Usable: true}},
	}

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	got := h.reconcileExpectOK(om.Name)
	require.NotNil(t, got.Status.OpsManagerStatus.License)
	assert.Equal(t, "ea-auto", licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Licenses[0].ArtifactID)
	assert.Empty(t, got.Status.OpsManagerStatus.License.InstalledArtifactID)
	assert.Nil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_DoesNotReinstallAfterOutOfBandRemoval(t *testing.T) {
	h := newLicenseEnvHarness(t)
	licensed := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}
	uncovered := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}
	h.admin.LicenseState = licensed

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)

	h.admin.LicenseReadState = &uncovered
	h.admin.LicenseState = uncovered
	h.admin.InstalledLicense = nil
	got = h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestLicenseE2E_InstallReleaseThenReceipt(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}
	h.admin.LicenseReceipt = `{"artifactId":"art-1","receipt":"ea-export"}`

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)

	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	got = h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Empty(t, got.Status.OpsManagerStatus.License.InstalledArtifactID)

	receiptName := testLicenseArtifactID + "-release-receipt"
	h.createSecret(receiptName, "note", "declared in git", nil)
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License.ReleaseReceipts = []omv1.LicenseReleaseReceipt{releaseReceipt(testLicenseArtifactID)}
	})
	h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)

	receipt := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Equal(t, h.admin.LicenseReceipt, string(receipt.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assert.NotContains(t, receipt.Data, "note")
	assert.Empty(t, receipt.OwnerReferences)

	delete(receipt.Data, util.DefaultOpsManagerReleaseReceiptKey)
	require.NoError(t, h.client.Update(h.ctx, receipt))
	h.reconcileExpectOK(om.Name)
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Equal(t, h.admin.LicenseReceipt, string(receipt.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestLicenseE2E_ReleaseWithReceiptInOneStep(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	receiptName := testLicenseArtifactID + "-release-receipt"
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			ReleaseReceipts: []omv1.LicenseReleaseReceipt{releaseReceipt(testLicenseArtifactID)},
		}
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Empty(t, got.Status.OpsManagerStatus.License.InstalledArtifactID)

	receipt := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Contains(t, receipt.Data, util.DefaultOpsManagerReleaseReceiptKey)
}

func TestLicenseE2E_ReleaseWithoutReceipt(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-1", Usable: true}},
	}

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.seedInstalled(om.Name, "art-1")

	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{"art-1"}, h.admin.ReleasedIDs)
	assert.Empty(t, got.Status.OpsManagerStatus.License.InstalledArtifactID)

	receipt := &corev1.Secret{}
	err := h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: "om-receipt"}, receipt)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestLicenseE2E_RotateIdentityViaAnnotation(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		ActiveEntitlements: map[string][]omv1.OpsManagerLicenseEntitlementStatus{
			"exemption::enforcement": {{Type: "exemption::enforcement", Active: true}},
		},
	}

	om := h.createOM(nil)
	h.setRotateAnnotation(om.Name, "true")
	got := h.reconcileExpectOK(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRotateIdentityAnnotation])
	assert.Equal(t, 1, h.admin.IdentityRotationCalls)
	require.NotNil(t, got.Status.OpsManagerStatus.License)
	assert.Equal(t, "LICENSED", licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
	require.Len(t, licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).ActiveEntitlements["exemption::enforcement"], 1)

	h.setRotateAnnotation(om.Name, "true")
	got = h.reconcileExpectOK(om.Name)
	assert.Equal(t, 2, h.admin.IdentityRotationCalls)
	assert.Empty(t, got.Annotations[util.LicenseRotateIdentityAnnotation])
}

func TestLicenseE2E_RotateFailureClearsAnnotation(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.IdentityRotationErr = fmt.Errorf("license identity rotation unavailable")

	om := h.createOM(nil)
	h.setRotateAnnotation(om.Name, "true")
	st := h.reconcile(om.Name)
	assert.False(t, st.IsOK())

	got := h.loadOM(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRotateIdentityAnnotation])
	assert.Equal(t, 1, h.admin.IdentityRotationCalls)
}

func TestLicenseE2E_RecomputeViaAnnotation(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
	}

	om := h.createOM(nil)
	h.setRecomputeAnnotation(om.Name, "true")
	got := h.reconcileExpectOK(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRecomputeAnnotation])
	assert.Equal(t, 1, h.admin.LicenseRecomputeCalls)
	require.NotNil(t, got.Status.OpsManagerStatus.License)
	assert.Equal(t, "LICENSED", licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Enforcement.EffectiveState)

	h.setRecomputeAnnotation(om.Name, "true")
	got = h.reconcileExpectOK(om.Name)
	assert.Equal(t, 2, h.admin.LicenseRecomputeCalls)
	assert.Empty(t, got.Annotations[util.LicenseRecomputeAnnotation])
}

func TestLicenseE2E_RecomputeFailureClearsAnnotation(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseRecomputeErr = fmt.Errorf("license recompute unavailable")

	om := h.createOM(nil)
	h.setRecomputeAnnotation(om.Name, "true")
	st := h.reconcile(om.Name)
	assert.False(t, st.IsOK())

	got := h.loadOM(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRecomputeAnnotation])
	assert.Equal(t, 1, h.admin.LicenseRecomputeCalls)
}

func TestLicenseE2E_InstallFails(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseInstallErr = fmt.Errorf("install rejected")

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)

	st := h.reconcile(om.Name)
	assert.False(t, st.IsOK())
	res, err := st.ReconcileResult()
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter.Seconds(), float64(0))
}

func TestLicenseE2E_MissingLicenseSecretFails(t *testing.T) {
	h := newLicenseEnvHarness(t)
	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			LicenseSecretRef: &omv1.SecretRef{Name: "missing-license"},
		}
	})
	st := h.reconcile(om.Name)
	assert.False(t, st.IsOK())
}

func TestLicenseE2E_ReceiptSecretIsCreated(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseReceipt = `{"artifactId":"art-1","receipt":"ea-export"}`

	om := h.createOM(nil)
	receiptName := "art-1-release-receipt"
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			ReleaseReceipts: []omv1.LicenseReleaseReceipt{releaseReceipt("art-1")},
		}
	})

	h.reconcileExpectOK(om.Name)
	receipt := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Equal(t, h.admin.LicenseReceipt, string(receipt.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assert.Empty(t, receipt.OwnerReferences)
}

func TestLicenseE2E_ReceiptEntryNamesArtifactByOldLicenseSecret(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-old", Status: omv1.LicenseArtifactStatusReleased}},
	}
	h.admin.LicenseReceipt = `{"artifactId":"art-old","receipt":"ea-export"}`

	om := h.createOM(nil)
	oldLicense := om.Name + "-license-old"
	h.createSecret(oldLicense, util.DefaultOpsManagerLicenseKey, licenseArtifactJSON("art-old"), om)
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			ReleaseReceipts: []omv1.LicenseReleaseReceipt{{
				LicenseSecretRef: &omv1.SecretRef{Name: oldLicense},
			}},
		}
	})

	h.reconcileExpectOK(om.Name)
	receipt := &corev1.Secret{}
	receiptName := "art-old-release-receipt"
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Equal(t, h.admin.LicenseReceipt, string(receipt.Data[util.DefaultOpsManagerReleaseReceiptKey]))
}

func TestLicenseE2E_AutoReceiptSecretOnRelease(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}
	h.admin.LicenseReceipt = `{"artifactId":"art-1","receipt":"ea-export"}`

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License.LicenseSecretRef = nil
		om.Spec.License.AutoReleaseReceipts = true
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Empty(t, got.Status.OpsManagerStatus.License.InstalledArtifactID)

	receiptName := testLicenseArtifactID + "-release-receipt"
	receipt := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Equal(t, h.admin.LicenseReceipt, string(receipt.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assert.Empty(t, receipt.OwnerReferences)
}

func TestLicenseE2E_ReleaseDoesNotReinstallWithoutSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	uncovered := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}
	h.admin.LicenseReadState = &uncovered
	h.admin.LicenseState = uncovered

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.createSecret(om.Name+"-license", util.DefaultOpsManagerLicenseKey, testLicenseArtifact, om)
	h.seedInstalled(om.Name, "art-1")

	h.reconcileExpectOK(om.Name)
	h.admin.InstalledLicense = nil
	h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_DoesNotReleaseWithoutPriorInstall(t *testing.T) {
	h := newLicenseEnvHarness(t)
	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.reconcileExpectOK(om.Name)
	assert.Empty(t, h.admin.ReleasedIDs)
}

func TestLicenseE2E_RenewWhenArtifactChanges(t *testing.T) {
	h := newLicenseEnvHarness(t)
	renewedArtifact := licenseArtifactJSON("art-renew")
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	secretName := om.Name + "-license"
	sec := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: secretName}, sec))
	sec.Data[util.DefaultOpsManagerLicenseKey] = []byte(renewedArtifact)
	require.NoError(t, h.client.Update(h.ctx, sec))
	h.admin.LicenseReadState = &omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-renew", Usable: true}},
	}
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, "art-renew", got.Status.OpsManagerStatus.License.InstalledArtifactID)
	assert.Equal(t, renewedArtifact, string(h.admin.InstalledLicense))
}

func TestLicenseE2E_ReleasesWhenOMStillHoldsInstalledArtifact(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: false},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "om-art", Usable: true}},
	}

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	om = h.loadOM(om.Name)
	om.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "om-art",
	}
	require.NoError(t, h.client.Status().Update(h.ctx, om))

	h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{"om-art"}, h.admin.ReleasedIDs)
}

func TestLicenseE2E_ObserveRetriesOnGetFailureWithSpecLicense(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseReadErr = fmt.Errorf("read failed")

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	st := h.reconcile(om.Name)
	assert.False(t, st.IsOK())
	got := h.loadOM(om.Name)
	assert.Nil(t, got.Status.OpsManagerStatus.License)
}

func TestLicenseE2E_RotateWithoutSpecLicense(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
	}

	om := h.createOM(nil)
	h.setRotateAnnotation(om.Name, "true")
	got := h.reconcileExpectOK(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRotateIdentityAnnotation])
	require.NotNil(t, got.Status.OpsManagerStatus.License)
	assert.Equal(t, "LICENSED", licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
}

func TestLicenseE2E_SkipsInstallWithoutSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
	require.NotNil(t, licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Enforcement)
	assert.Equal(t, "GRACE", licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Enforcement.EffectiveState)
}

func TestLicenseE2E_SkipsReinstallWhenOMStillLicensed(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)

	h.admin.InstalledLicense = nil
	h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_DoesNotReinstallWhenWrongArtifactInOM(t *testing.T) {
	h := newLicenseEnvHarness(t)
	wrongLicense := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-other", Usable: true}},
	}
	h.admin.LicenseState = wrongLicense

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.seedInstalled(om.Name, testLicenseArtifactID)

	got := h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)
	require.NotEmpty(t, licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Licenses)
	assert.Equal(t, "art-other", licenseViewFromStatus(t, got.Status.OpsManagerStatus.License).Licenses[0].ArtifactID)

	got = h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestLicenseE2E_SkipsReinstallWhenSameArtifactStillListed(t *testing.T) {
	h := newLicenseEnvHarness(t)
	sameArtifact := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: false},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}
	h.admin.LicenseReadState = &sameArtifact
	h.admin.LicenseState = sameArtifact

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.seedInstalled(om.Name, testLicenseArtifactID)

	h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_ReleaseLeavesAForeignArtifactAlone(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: false},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: "art-ea", Usable: true}},
	}

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	om = h.loadOM(om.Name)
	om.Status.OpsManagerStatus.License = &omv1.OpsManagerLicenseStatus{
		InstalledArtifactID: "art-1",
	}
	require.NoError(t, h.client.Status().Update(h.ctx, om))

	st := h.reconcile(om.Name)
	assert.True(t, st.IsOK())
	assert.Empty(t, h.admin.ReleasedIDs)
	om = h.loadOM(om.Name)
	assert.Empty(t, om.Status.OpsManagerStatus.License.InstalledArtifactID)
	assert.Equal(t, "art-ea", om.Status.OpsManagerStatus.License.Licenses[0].ArtifactID)
}

func TestLicenseE2E_MissingLicenseSecretKeyFails(t *testing.T) {
	h := newLicenseEnvHarness(t)
	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			LicenseSecretRef: &omv1.SecretRef{Name: om.Name + "-license"},
		}
	})
	h.createSecret(om.Name+"-license", "other-key", testLicenseArtifact, om)

	st := h.reconcile(om.Name)
	assert.False(t, st.IsOK())
}

func TestLicenseE2E_InstallSucceedsWhenStateListsExtraArtifacts(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Licenses: []omv1.OpsManagerLicenseArtifactStatus{
			{ArtifactID: testLicenseArtifactID, Usable: true},
			{ArtifactID: "art-2", Usable: true},
		},
	}

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)

	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, testLicenseArtifactID, got.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestLicenseE2E_AutoReleaseOnSecretRefRemoval(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "LICENSED", Enforced: true},
		Licenses:    []omv1.OpsManagerLicenseArtifactStatus{{ArtifactID: testLicenseArtifactID, Usable: true}},
	}

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License.LicenseSecretRef = nil
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Empty(t, got.Status.OpsManagerStatus.License.InstalledArtifactID)
}

func TestLicenseE2E_AutoReleaseClearsMarkersWhenAlreadyUncovered(t *testing.T) {
	h := newLicenseEnvHarness(t)
	uncovered := omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}
	h.admin.LicenseReadState = &uncovered
	h.admin.LicenseState = uncovered

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	om = h.loadOM(om.Name)
	om.Status.OpsManagerStatus.License = newLicenseStatus(t, omv1.OpsManagerLicenseStatus{
		Enforcement: &omv1.OpsManagerLicenseEnforcementStatus{EffectiveState: "GRACE", Enforced: false},
	}, "art-1")
	require.NoError(t, h.client.Status().Update(h.ctx, om))

	got := h.reconcileExpectOK(om.Name)
	assert.Empty(t, h.admin.ReleasedIDs)
	assert.Empty(t, got.Status.OpsManagerStatus.License.InstalledArtifactID)
}
