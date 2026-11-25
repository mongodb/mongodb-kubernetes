package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	vaiv1 "github.com/mongodb/mongodb-kubernetes/api/voyageai/v1/vai"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
)

func newVoyageRouter(name, namespace string) *vaiv1.VoyageRouter {
	return &vaiv1.VoyageRouter{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

func newVoyageRouterReconcilerForTest(objects ...client.Object) (*VoyageRouterReconciler, client.Client) {
	builder := mock.NewEmptyFakeClientBuilder().
		WithIndex(&vaiv1.VoyageRouter{}, voyageRouterTLSCertSecretIndex, func(object client.Object) []string {
			router := object.(*vaiv1.VoyageRouter)
			if !router.IsTLSConfigured() {
				return nil
			}
			return []string{router.InboundTLSSecretNamespacedName().Name}
		}).
		WithIndex(&vaiv1.VoyageRouter{}, voyageRouterUpstreamCAIndex, func(object client.Object) []string {
			router := object.(*vaiv1.VoyageRouter)
			if router.Spec.Security.UpstreamTLS == nil {
				return nil
			}
			return []string{router.UpstreamCAConfigMapNamespacedName().Name}
		})
	if len(objects) > 0 {
		builder.WithObjects(objects...)
	}
	builder.WithStatusSubresource(&vaiv1.VoyageRouter{})
	fakeClient := builder.Build()
	return newVoyageRouterReconciler(fakeClient, "envoy:test", "router.test"), fakeClient
}

func newRouterVoyageAI(name, namespace string, model vaiv1.VoyageAIModel, phase status.Phase, labels map[string]string) *vaiv1.VoyageAI {
	return &vaiv1.VoyageAI{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       vaiv1.VoyageAISpec{Model: model, Version: "1.0.0", Server: vaiv1.ServerConfig{Port: 8088}},
		Status:     vaiv1.VoyageAIStatus{Common: status.Common{Phase: phase}},
	}
}

func reconcileVoyageRouter(ctx context.Context, reconciler *VoyageRouterReconciler, router *vaiv1.VoyageRouter) (reconcile.Result, error) {
	return reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: router.Name, Namespace: router.Namespace}})
}

func TestVoyageRouterSelectedModelsHonorsSelectorsAndAvailability(t *testing.T) {
	ctx := context.Background()
	router := newVoyageRouter("router", mock.TestNamespace)
	router.Spec.Selector = &metav1.LabelSelector{
		MatchLabels:      map[string]string{"tier": "selected"},
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "region", Operator: metav1.LabelSelectorOpIn, Values: []string{"west"}}},
	}
	running := newRouterVoyageAI("running", router.Namespace, vaiv1.VoyageAIModelVoyage4, status.PhaseRunning, map[string]string{"tier": "selected", "region": "west"})
	pending := newRouterVoyageAI("pending", router.Namespace, vaiv1.VoyageAIModelRerank25, status.PhasePending, map[string]string{"tier": "selected", "region": "west"})
	excluded := newRouterVoyageAI("excluded", router.Namespace, vaiv1.VoyageAIModelVoyage4Large, status.PhaseRunning, map[string]string{"tier": "other", "region": "west"})
	reconciler, _ := newVoyageRouterReconcilerForTest(router, running, pending, excluded)

	models, needsUpstreamCA, err := reconciler.selectedModels(ctx, router)
	require.NoError(t, err)
	assert.False(t, needsUpstreamCA)
	byModel := make(map[vaiv1.VoyageAIModel]voyageRouterModel, len(models))
	for _, model := range models {
		byModel[model.Model] = model
	}
	assert.True(t, byModel[vaiv1.VoyageAIModelVoyage4].Selected)
	require.Len(t, byModel[vaiv1.VoyageAIModelVoyage4].Backends, 1)
	assert.Equal(t, "running-svc.my-namespace.svc.router.test", byModel[vaiv1.VoyageAIModelVoyage4].Backends[0].ServiceFQDN)
	assert.True(t, byModel[vaiv1.VoyageAIModelRerank25].Selected)
	assert.Empty(t, byModel[vaiv1.VoyageAIModelRerank25].Backends)
	assert.False(t, byModel[vaiv1.VoyageAIModelVoyage4Large].Selected)
}

func TestVoyageRouterSelectedModelsRequireCAForPendingTLSBackend(t *testing.T) {
	ctx := context.Background()
	router := newVoyageRouter("router", mock.TestNamespace)
	vai := newRouterVoyageAI("tls", router.Namespace, vaiv1.VoyageAIModelVoyage4, status.PhasePending, nil)
	vai.Spec.Security.TLS = &vaiv1.TLS{CertificateKeySecretRef: corev1.LocalObjectReference{Name: "tls"}}
	reconciler, _ := newVoyageRouterReconcilerForTest(router, vai)

	models, needsUpstreamCA, err := reconciler.selectedModels(ctx, router)
	require.NoError(t, err)
	assert.True(t, needsUpstreamCA)
	assert.Empty(t, models[1].Backends)
}

func TestVoyageRouterReconcileMissingUpstreamCAWritesBlockedConfig(t *testing.T) {
	ctx := context.Background()
	router := newVoyageRouter("router", mock.TestNamespace)
	vai := newRouterVoyageAI("tls", router.Namespace, vaiv1.VoyageAIModelVoyage4, status.PhaseRunning, nil)
	vai.Spec.Security.TLS = &vaiv1.TLS{CertificateKeySecretRef: corev1.LocalObjectReference{Name: "tls"}}
	reconciler, fakeClient := newVoyageRouterReconcilerForTest(router, vai)

	_, err := reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)
	configMap := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.ConfigMapName(), Namespace: router.Namespace}, configMap))
	configMapOwner := metav1.GetControllerOf(configMap)
	require.NotNil(t, configMapOwner)
	assert.Equal(t, router.UID, configMapOwner.UID)
	assert.True(t, *configMapOwner.Controller)
	assert.Contains(t, configMap.Data["cds.json"], voyageRouterClusterName(vaiv1.VoyageAIModelVoyage4))
	assert.NotContains(t, configMap.Data["cds.json"], "tls-svc")
	deployment := &appsv1.Deployment{}
	assert.True(t, apierrors.IsNotFound(fakeClient.Get(ctx, types.NamespacedName{Name: router.DeploymentName(), Namespace: router.Namespace}, deployment)))
	updated := &vaiv1.VoyageRouter{}
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.Name, Namespace: router.Namespace}, updated))
	assert.Equal(t, status.PhasePending, updated.Status.Phase)
}

func TestVoyageRouterReconcileMissingEnvoyImageFails(t *testing.T) {
	ctx := context.Background()
	router := newVoyageRouter("router", mock.TestNamespace)
	_, fakeClient := newVoyageRouterReconcilerForTest(router)
	reconciler := newVoyageRouterReconciler(fakeClient, "", "")

	_, err := reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)
	updated := &vaiv1.VoyageRouter{}
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.Name, Namespace: router.Namespace}, updated))
	assert.Equal(t, status.PhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "MDB_ENVOY_IMAGE")
}

func TestVoyageRouterReconcileHotUpdatesCDSWithoutChangingPodTemplate(t *testing.T) {
	ctx := context.Background()
	router := newVoyageRouter("router", mock.TestNamespace)
	vai := newRouterVoyageAI("backend", router.Namespace, vaiv1.VoyageAIModelVoyage4, status.PhaseRunning, nil)
	reconciler, fakeClient := newVoyageRouterReconcilerForTest(router, vai)

	_, err := reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)
	deployment := &appsv1.Deployment{}
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.DeploymentName(), Namespace: router.Namespace}, deployment))
	originalTemplate := deployment.Spec.Template.DeepCopy()
	deploymentOwner := metav1.GetControllerOf(deployment)
	require.NotNil(t, deploymentOwner)
	assert.Equal(t, router.UID, deploymentOwner.UID)
	assert.True(t, *deploymentOwner.Controller)
	configMap := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.ConfigMapName(), Namespace: router.Namespace}, configMap))
	originalCDS := configMap.Data["cds.json"]
	service := &corev1.Service{}
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.ServiceName(), Namespace: router.Namespace}, service))
	serviceOwner := metav1.GetControllerOf(service)
	require.NotNil(t, serviceOwner)
	assert.Equal(t, router.UID, serviceOwner.UID)
	assert.True(t, *serviceOwner.Controller)

	vai.Status.Phase = status.PhasePending
	require.NoError(t, fakeClient.Status().Update(ctx, vai))
	_, err = reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.DeploymentName(), Namespace: router.Namespace}, deployment))
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: router.ConfigMapName(), Namespace: router.Namespace}, configMap))
	assert.Equal(t, *originalTemplate, deployment.Spec.Template)
	assert.NotEqual(t, originalCDS, configMap.Data["cds.json"])
}

func TestVoyageRouterReconcileTLSRotationChangesPodTemplate(t *testing.T) {
	ctx := context.Background()
	router := newVoyageRouter("router", mock.TestNamespace)
	router.Spec.Security.TLS = &vaiv1.TLS{CertificateKeySecretRef: corev1.LocalObjectReference{Name: "router-tls"}}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "router-tls", Namespace: router.Namespace},
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("cert-v1"), corev1.TLSPrivateKeyKey: []byte("key-v1")},
	}
	reconciler, fakeClient := newVoyageRouterReconcilerForTest(router, secret)
	_, err := reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Name: router.DeploymentName(), Namespace: router.Namespace}
	require.NoError(t, fakeClient.Get(ctx, key, deployment))
	firstHash := deployment.Spec.Template.Annotations[envoyConfigHashAnnotation]

	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, secret))
	secret.Data[corev1.TLSCertKey] = []byte("cert-v2")
	require.NoError(t, fakeClient.Update(ctx, secret))
	_, err = reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)
	require.NoError(t, fakeClient.Get(ctx, key, deployment))
	assert.NotEqual(t, firstHash, deployment.Spec.Template.Annotations[envoyConfigHashAnnotation])
}

func TestVoyageRouterReconcileUpstreamCARotationChangesPodTemplate(t *testing.T) {
	ctx := context.Background()
	router := newVoyageRouter("router", mock.TestNamespace)
	router.Spec.Security.UpstreamTLS = &vaiv1.VoyageRouterUpstreamTLS{CA: corev1.LocalObjectReference{Name: "upstream-ca"}}
	vai := newRouterVoyageAI("tls", router.Namespace, vaiv1.VoyageAIModelVoyage4, status.PhaseRunning, nil)
	vai.Spec.Security.TLS = &vaiv1.TLS{CertificateKeySecretRef: corev1.LocalObjectReference{Name: "backend-tls"}}
	ca := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "upstream-ca", Namespace: router.Namespace}, Data: map[string]string{voyageRouterUpstreamCAFile: "ca-v1"}}
	reconciler, fakeClient := newVoyageRouterReconcilerForTest(router, vai, ca)
	_, err := reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Name: router.DeploymentName(), Namespace: router.Namespace}
	require.NoError(t, fakeClient.Get(ctx, key, deployment))
	firstHash := deployment.Spec.Template.Annotations[envoyConfigHashAnnotation]

	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: ca.Name, Namespace: ca.Namespace}, ca))
	ca.Data[voyageRouterUpstreamCAFile] = "ca-v2"
	require.NoError(t, fakeClient.Update(ctx, ca))
	_, err = reconcileVoyageRouter(ctx, reconciler, router)
	require.NoError(t, err)
	require.NoError(t, fakeClient.Get(ctx, key, deployment))
	assert.NotEqual(t, firstHash, deployment.Spec.Template.Annotations[envoyConfigHashAnnotation])
}

func TestVoyageRouterMapVoyageAIToRoutersFansOutWithinNamespace(t *testing.T) {
	ctx := context.Background()
	routerA := newVoyageRouter("router-a", mock.TestNamespace)
	routerB := newVoyageRouter("router-b", mock.TestNamespace)
	otherNamespace := newVoyageRouter("router-c", "other")
	vai := newRouterVoyageAI("backend", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, status.PhaseRunning, nil)
	reconciler, _ := newVoyageRouterReconcilerForTest(routerA, routerB, otherNamespace, vai)

	requests := reconciler.mapVoyageAIToRouters(ctx, vai)
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: routerA.Name, Namespace: routerA.Namespace}},
		{NamespacedName: types.NamespacedName{Name: routerB.Name, Namespace: routerB.Namespace}},
	}, requests)
}
