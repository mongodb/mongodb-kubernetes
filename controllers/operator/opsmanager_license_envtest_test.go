package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
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
	reconciler := NewOpsManagerReconciler(ctx, k8sClient, nil, images.ImageUrls{}, "", "", architectures.NonStatic, "", omConnectionFactory.GetConnectionFunc, nil, nil)
	t.Cleanup(func() { api.CurrMockedAdmin = nil })

	return &licenseEnvHarness{
		t:          t,
		ctx:        ctx,
		client:     k8sClient,
		reconciler: reconciler,
		admin:      &api.MockedOmAdmin{},
		namespace:  newLicenseNamespace(t, ctx, k8sClient),
	}
}

// newLicenseNamespace gives each harness its own namespace so tests never share Secrets.
func newLicenseNamespace(t *testing.T, ctx context.Context, k8sClient client.Client) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "om-lic-"}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	return ns.Name
}

func (h *licenseEnvHarness) createOM(mutate func(*omv1.MongoDBOpsManager)) *omv1.MongoDBOpsManager {
	h.t.Helper()
	om := DefaultOpsManagerBuilder().Build()
	om.Namespace = h.namespace
	om.Name = ""
	om.GenerateName = "om-lic-"
	om.ResourceVersion = ""
	om.UID = ""
	if mutate != nil {
		mutate(om)
	}
	// Create it without spec.applicationDatabase.monitoringAgent like a user manifest, so the operator
	// writes back an object the CRD validates the same way as in a real cluster.
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(om)
	require.NoError(h.t, err)
	unstructured.RemoveNestedField(content, "spec", "applicationDatabase", "monitoringAgent")
	gvk, err := apiutil.GVKForObject(om, h.client.Scheme())
	require.NoError(h.t, err)
	obj := &unstructured.Unstructured{Object: content}
	obj.SetGroupVersionKind(gvk)
	require.NoError(h.t, h.client.Create(h.ctx, obj))
	require.NoError(h.t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, om))
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

// reconcile runs the license reconcile and mirrors what the controller does at the end of a
// reconciliation: the LastAchievedSpec annotation is refreshed with the spec as it was seen.
func (h *licenseEnvHarness) reconcile(name string) workflow.Status {
	h.t.Helper()
	om := h.loadOM(name)
	st := h.reconciler.ensureOpsManagerLicense(h.ctx, om, h.admin, zap.NewNop().Sugar())

	reconciledStatus := om.Status
	specBytes, err := json.Marshal(om.Spec)
	require.NoError(h.t, err)
	om = h.loadOM(name)
	require.NoError(h.t, annotations.SetAnnotations(h.ctx, om, map[string]string{
		util.LastAchievedSpec: string(specBytes),
	}, h.client))

	om = h.loadOM(name)
	om.Status = reconciledStatus
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
	patch := client.MergeFrom(om.DeepCopy())
	mutate(om)
	require.NoError(h.t, h.client.Patch(h.ctx, om, patch))
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
			SecretRef: &omv1.SecretRef{Name: secretName},
		}
	})
	h.createSecret(secretName, util.DefaultOpsManagerLicenseKey, artifact, om)
}

// seedInstalled records a prior install intent from the license Secret the last reconciliation achieved.
func (h *licenseEnvHarness) seedInstalled(name, licenseSecretName string) {
	h.t.Helper()
	om := h.loadOM(name)
	require.NoError(h.t, annotations.SetAnnotations(h.ctx, om, map[string]string{
		util.LastAchievedSpec: achievedSpecJSON(h.t, licenseSecretName),
	}, h.client))
}

// achievedSpecJSON builds the LastAchievedSpec annotation value for a spec whose license came from
// the named Secret. An empty name stands for a spec without an install intent.
func achievedSpecJSON(t *testing.T, licenseSecretName string) string {
	t.Helper()
	spec := omv1.MongoDBOpsManagerSpec{}
	if licenseSecretName != "" {
		spec.License = &omv1.MongoDBOpsManagerLicense{SecretRef: &omv1.SecretRef{Name: licenseSecretName}}
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	return string(raw)
}

func TestLicenseE2E_InstallFromSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)

	got := h.reconcileExpectOK(om.Name)
	ready := licenseReadyCondition(t, got)
	assert.Contains(t, ready.Message, "License state is LICENSED.")
	assert.Contains(t, ready.Message, testLicenseArtifactID)
	require.NotNil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_ObserveOnlyWithoutSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("ea-auto"))

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "license ea-auto")
	assert.Nil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_ReinstallsWhenOMLostTheLicense(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)
	require.NotNil(t, h.admin.InstalledLicense)

	// OM lost the license out of band, the spec still asks for it and is installed again.
	uncovered := licensedView("GRACE")
	h.admin.LicenseReadState = &uncovered
	h.admin.LicenseState = uncovered
	h.admin.InstalledLicense = nil
	h.reconcileExpectOK(om.Name)
	assert.NotNil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_InstallReleaseThenReceipt(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView(testLicenseArtifactID))
	h.admin.LicenseReceipt = `{"artifactId":"art-1","receipt":"ea-export"}`

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "no license")

	receiptName := testLicenseArtifactID + "-release-receipt"
	h.createSecret(receiptName, "note", "declared in git", nil)
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License.ReleaseReceipts = []omv1.LicenseReleaseReceipt{releaseReceipt(om.Name + "-license")}
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
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView(testLicenseArtifactID))

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	receiptName := testLicenseArtifactID + "-release-receipt"
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			ReleaseReceipts: []omv1.LicenseReleaseReceipt{releaseReceipt(om.Name + "-license")},
		}
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "no license")

	receipt := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Contains(t, receipt.Data, util.DefaultOpsManagerReleaseReceiptKey)
}

func TestLicenseE2E_ReleaseWithoutReceipt(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("art-1"))

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.seedInstalled(om.Name, om.Name+"-license")
	h.createSecret(om.Name+"-license", util.DefaultOpsManagerLicenseKey, licenseArtifactJSON("art-1"), om)

	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{"art-1"}, h.admin.ReleasedIDs)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "no license")

	receipt := &corev1.Secret{}
	err := h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: "art-1-release-receipt"}, receipt)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestLicenseE2E_RotateIdentityViaAnnotation(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setRotateAnnotation(om.Name, "true")
	got := h.reconcileExpectOK(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRotateIdentityAnnotation])
	assert.Contains(t, licenseReadyCondition(t, got).Message, "License state is LICENSED.")

	h.setRotateAnnotation(om.Name, "true")
	got = h.reconcileExpectOK(om.Name)
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
}

func TestLicenseE2E_RecomputeViaAnnotation(t *testing.T) {
	h := newLicenseEnvHarness(t)
	// Reads return the state of the last evaluation until a recompute evaluates a new one.
	h.admin.LicenseReadState = licensedView("GRACE")
	h.admin.LicenseRecomputedState = licensedView("LICENSED")

	om := h.createOM(nil)
	got := h.reconcileExpectOK(om.Name)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "License state is GRACE.")

	h.setRecomputeAnnotation(om.Name, "true")
	got = h.reconcileExpectOK(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRecomputeAnnotation])
	assert.Contains(t, licenseReadyCondition(t, got).Message, "License state is LICENSED.")

	h.setRecomputeAnnotation(om.Name, "true")
	got = h.reconcileExpectOK(om.Name)
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

// deleteAndRelease deletes the resource and runs the release the controller runs while the finalizer holds it.
func (h *licenseEnvHarness) deleteAndRelease(name string) {
	h.t.Helper()
	om := h.loadOM(name)
	require.NoError(h.t, h.client.Delete(h.ctx, om))

	om = h.loadOM(name)
	log := zap.NewNop().Sugar()
	helper, err := h.reconciler.newLicenseReconcilerHelper(om, h.admin, log)
	require.NoError(h.t, err)
	_, _ = h.reconciler.updateStatus(h.ctx, om, helper.releaseOnDeletion(h.ctx), log, status.NewOMPartOption(status.OpsManager))
}

func (h *licenseEnvHarness) omGone(name string) bool {
	h.t.Helper()
	err := h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: name}, &omv1.MongoDBOpsManager{})
	return apierrors.IsNotFound(err)
}

func licenseManagedCondition(t *testing.T, om *omv1.MongoDBOpsManager) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(om.Status.OpsManagerStatus.Conditions, status.ConditionLicenseManaged)
	require.NotNil(t, c)
	return *c
}

func TestLicenseE2E_ReleasesOnDeletion(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	got := h.reconcileExpectOK(om.Name)
	require.Contains(t, got.Finalizers, util.OpsManagerLicenseFinalizer)

	h.deleteAndRelease(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.True(t, h.omGone(om.Name))
}

func TestLicenseE2E_DeletionWaitsForTheRelease(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.admin.LicenseReadErr = fmt.Errorf("ops manager is unreachable")
	h.deleteAndRelease(om.Name)

	held := h.loadOM(om.Name)
	assert.Contains(t, held.Finalizers, util.OpsManagerLicenseFinalizer)
	assert.Empty(t, h.admin.ReleasedIDs)
	assert.Contains(t, licenseManagedCondition(t, held).Message, "unreachable")

	// Ops Manager is back, the next attempt releases the license and lets the deletion finish.
	h.admin.LicenseReadErr = nil
	h.deleteAndRelease(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.True(t, h.omGone(om.Name))
}

func TestLicenseE2E_DeletionLeavesAForeignLicenseAlone(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	// A top up from EA replaced the license, so Ops Manager no longer holds the one the Secret names.
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("art-ea"))
	h.deleteAndRelease(om.Name)

	assert.Empty(t, h.admin.ReleasedIDs)
	assert.True(t, h.omGone(om.Name))
}

func TestLicenseE2E_NoFinalizerWithoutInstallIntent(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("ea-auto"))

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	got := h.reconcileExpectOK(om.Name)
	assert.NotContains(t, got.Finalizers, util.OpsManagerLicenseFinalizer)
}

func TestLicenseE2E_FinalizerDropsAfterTheRelease(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.reconcileExpectOK(om.Name)

	// The reconcile that released still saw the install intent in the achieved spec, so the finalizer
	// comes off on the next one.
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.NotContains(t, got.Finalizers, util.OpsManagerLicenseFinalizer)
}

func TestLicenseE2E_MissingLicenseSecretFails(t *testing.T) {
	h := newLicenseEnvHarness(t)
	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			SecretRef: &omv1.SecretRef{Name: "missing-license"},
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
	oldLicense := om.Name + "-license-old"
	h.createSecret(oldLicense, util.DefaultOpsManagerLicenseKey, licenseArtifactJSON("art-1"), om)
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			ReleaseReceipts: []omv1.LicenseReleaseReceipt{releaseReceipt(oldLicense)},
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
	h.admin.LicenseState = viewWithLicenses("", releasedArtifactView("art-old"))
	h.admin.LicenseReceipt = `{"artifactId":"art-old","receipt":"ea-export"}`

	om := h.createOM(nil)
	oldLicense := om.Name + "-license-old"
	h.createSecret(oldLicense, util.DefaultOpsManagerLicenseKey, licenseArtifactJSON("art-old"), om)
	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			ReleaseReceipts: []omv1.LicenseReleaseReceipt{{
				SecretRef: omv1.SecretRef{Name: oldLicense},
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
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView(testLicenseArtifactID))
	h.admin.LicenseReceipt = `{"artifactId":"art-1","receipt":"ea-export"}`

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License.SecretRef = nil
		om.Spec.License.ReleaseReceiptsPolicy = omv1.ReleaseReceiptsAutomatic
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "no license")

	receiptName := testLicenseArtifactID + "-release-receipt"
	receipt := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: receiptName}, receipt))
	assert.Equal(t, h.admin.LicenseReceipt, string(receipt.Data[util.DefaultOpsManagerReleaseReceiptKey]))
	assert.Empty(t, receipt.OwnerReferences)
}

func TestLicenseE2E_ReleaseDoesNotReinstallWithoutSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	uncovered := viewWithLicenses("GRACE", releasedArtifactView(testLicenseArtifactID))
	h.admin.LicenseReadState = &uncovered
	h.admin.LicenseState = uncovered

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.createSecret(om.Name+"-license", util.DefaultOpsManagerLicenseKey, testLicenseArtifact, om)
	h.seedInstalled(om.Name, om.Name+"-license")

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
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView(testLicenseArtifactID))

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	// A renewal updates the license in the same Secret, OM no longer holds the new artifact and gets the PUT.
	secretName := om.Name + "-license"
	sec := &corev1.Secret{}
	require.NoError(t, h.client.Get(h.ctx, client.ObjectKey{Namespace: h.namespace, Name: secretName}, sec))
	sec.Data[util.DefaultOpsManagerLicenseKey] = []byte(renewedArtifact)
	require.NoError(t, h.client.Update(h.ctx, sec))
	h.admin.LicenseReadState = &api.LicenseView{
		Licenses: []api.LicenseArtifactView{artifactView(testLicenseArtifactID)},
	}
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("art-renew"))
	got := h.reconcileExpectOK(om.Name)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "license art-renew")
	assert.Equal(t, renewedArtifact, string(h.admin.InstalledLicense))
}

func TestLicenseE2E_ReleasesWhenOMStillHoldsInstalledArtifact(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("om-art"))

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.seedInstalled(om.Name, om.Name+"-license")
	h.createSecret(om.Name+"-license", util.DefaultOpsManagerLicenseKey, licenseArtifactJSON("om-art"), om)

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
}

func TestLicenseE2E_RotateWithoutSpecLicense(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("LICENSED")

	om := h.createOM(nil)
	h.setRotateAnnotation(om.Name, "true")
	got := h.reconcileExpectOK(om.Name)
	assert.Empty(t, got.Annotations[util.LicenseRotateIdentityAnnotation])
	assert.Contains(t, licenseReadyCondition(t, got).Message, "License state is LICENSED.")
}

func TestLicenseE2E_SkipsInstallWithoutSecretRef(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = licensedView("GRACE")

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "License state is GRACE.")
}

func TestLicenseE2E_SkipsReinstallWhenOMStillLicensed(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView(testLicenseArtifactID))

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.admin.InstalledLicense = nil
	h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_ReplacesAForeignArtifact(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("art-other"))

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)

	got := h.reconcileExpectOK(om.Name)
	assert.NotNil(t, h.admin.InstalledLicense)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "license art-1")

	h.admin.InstalledLicense = nil
	h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
}

func TestLicenseE2E_ReleaseLeavesAForeignArtifactAlone(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView("art-ea"))

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.createSecret(om.Name+"-license", util.DefaultOpsManagerLicenseKey, testLicenseArtifact, om)
	h.seedInstalled(om.Name, om.Name+"-license")

	st := h.reconcile(om.Name)
	assert.True(t, st.IsOK())
	assert.Empty(t, h.admin.ReleasedIDs)
	got := h.loadOM(om.Name)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "license art-ea")
}

func TestLicenseE2E_MissingLicenseSecretKeyFails(t *testing.T) {
	h := newLicenseEnvHarness(t)
	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{
			SecretRef: &omv1.SecretRef{Name: "om-license"},
		}
	})
	h.createSecret("om-license", "other-key", testLicenseArtifact, om)

	st := h.reconcile(om.Name)
	assert.False(t, st.IsOK())
}

func TestLicenseE2E_InstallSucceedsWhenStateListsExtraArtifacts(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("",
		artifactView(testLicenseArtifactID),
		artifactView("art-2"),
	)

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)

	got := h.reconcileExpectOK(om.Name)
	assert.Nil(t, h.admin.InstalledLicense)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "license art-1")
}

func TestLicenseE2E_AutoReleaseOnSecretRefRemoval(t *testing.T) {
	h := newLicenseEnvHarness(t)
	h.admin.LicenseState = viewWithLicenses("LICENSED", artifactView(testLicenseArtifactID))

	om := h.createOM(nil)
	h.setInstallFromSecret(om, testLicenseArtifact)
	h.reconcileExpectOK(om.Name)

	h.updateSpec(om.Name, func(om *omv1.MongoDBOpsManager) {
		om.Spec.License.SecretRef = nil
	})
	got := h.reconcileExpectOK(om.Name)
	assert.Equal(t, []string{testLicenseArtifactID}, h.admin.ReleasedIDs)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "no license")
}

func TestLicenseE2E_AutoReleaseClearsMarkersWhenAlreadyUncovered(t *testing.T) {
	h := newLicenseEnvHarness(t)
	uncovered := licensedView("GRACE")
	h.admin.LicenseReadState = &uncovered
	h.admin.LicenseState = uncovered

	om := h.createOM(func(om *omv1.MongoDBOpsManager) {
		om.Spec.License = &omv1.MongoDBOpsManagerLicense{}
	})
	h.seedInstalled(om.Name, om.Name+"-license")

	got := h.reconcileExpectOK(om.Name)
	assert.Empty(t, h.admin.ReleasedIDs)
	assert.Contains(t, licenseReadyCondition(t, got).Message, "no license")
}
