package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	vaiv1 "github.com/mongodb/mongodb-kubernetes/api/voyageai/v1/vai"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/deployment"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/lifecycle"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
)

const (
	voyageRouterTLSCertSecretIndex = ".spec.security.tls.certificateKeySecretRef.name"
	voyageRouterUpstreamCAIndex    = ".spec.security.upstreamTLS.ca.name"
	voyageRouterComponent          = "voyage-router"
)

type VoyageRouterReconciler struct {
	kubeClient        kubernetesClient.Client
	defaultEnvoyImage string
	clusterDomain     string
}

func newVoyageRouterReconciler(c client.Client, defaultEnvoyImage, clusterDomain string) *VoyageRouterReconciler {
	return &VoyageRouterReconciler{
		kubeClient:        kubernetesClient.NewClient(c),
		defaultEnvoyImage: defaultEnvoyImage,
		clusterDomain:     clusterDomain,
	}
}

// +kubebuilder:rbac:groups=ai.mongodb.com,resources={voyagerouters,voyagerouters/status,voyagerouters/finalizers},verbs=*,namespace=placeholder
// +kubebuilder:rbac:groups=ai.mongodb.com,resources=voyageais,verbs=get;list;watch,namespace=placeholder
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
// +kubebuilder:rbac:groups="",resources=services;configmaps;secrets,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
func (r *VoyageRouterReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("VoyageRouter", request.NamespacedName)
	router := &vaiv1.VoyageRouter{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, router, log); err != nil {
		return result, err
	}
	if !router.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	if r.defaultEnvoyImage == "" {
		return r.updateStatus(ctx, router, workflow.Failed(fmt.Errorf("%s environment variable must be set to use VoyageRouter", util.EnvoyImageEnv)), false, false, log)
	}

	// Hash TLS material into the Deployment pod template so certificate changes trigger a rollout.
	tlsMaterial := map[string]map[string][]byte{}
	inboundTLSData, err := r.validateInboundTLS(ctx, router)
	if err != nil {
		return r.updateStatus(ctx, router, workflow.Pending("%s", err), false, false, log)
	}
	if len(inboundTLSData) > 0 {
		tlsMaterial["inbound"] = inboundTLSData
	}

	models, needsUpstreamCA, err := r.selectedModels(ctx, router)
	if err != nil {
		return r.updateStatus(ctx, router, workflow.Failed(err), false, false, log)
	}
	if needsUpstreamCA {
		upstreamCA, err := r.validateUpstreamCA(ctx, router)
		if err != nil {
			blocked := slices.Clone(models)
			for index := range blocked {
				blocked[index].Backends = nil
			}
			return r.reconcileBlockedCA(ctx, router, blocked, err, log)
		}
		tlsMaterial["upstream"] = map[string][]byte{voyageRouterUpstreamCAFile: upstreamCA}
	}
	tlsMaterialJSON, err := json.Marshal(tlsMaterial)
	if err != nil {
		return r.updateStatus(ctx, router, workflow.Failed(fmt.Errorf("marshal Envoy TLS material hash input: %w", err)), false, false, log)
	}

	bootstrapJSON, cdsJSON, ldsJSON, err := r.buildConfig(router, models)
	if err != nil {
		return r.updateStatus(ctx, router, workflow.Failed(fmt.Errorf("build Envoy configuration: %w", err)), false, false, log)
	}
	if err := r.ensureConfigMap(ctx, router, bootstrapJSON, cdsJSON, ldsJSON); err != nil {
		return r.updateStatus(ctx, router, workflow.Failed(err), false, false, log)
	}
	deploymentResource, err := r.ensureDeployment(ctx, router, bootstrapJSON, tlsMaterialJSON, needsUpstreamCA)
	if err != nil {
		return r.updateStatus(ctx, router, workflow.Failed(err), false, false, log)
	}
	if err := r.ensureService(ctx, router); err != nil {
		return r.updateStatus(ctx, router, workflow.Failed(err), false, false, log)
	}

	if deploymentStatus := deployment.GetDeploymentStatus(ctx, router.Namespace, router.DeploymentName(), deploymentResource.Generation, r.kubeClient); !deploymentStatus.IsOK() {
		return r.updateStatus(ctx, router, deploymentStatus, false, true, log)
	}
	return r.updateStatus(ctx, router, workflow.OK(), true, true, log)
}

func (r *VoyageRouterReconciler) selectedModels(ctx context.Context, router *vaiv1.VoyageRouter) ([]voyageRouterModel, bool, error) {
	selector := labels.Everything()
	if router.Spec.Selector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(router.Spec.Selector)
		if err != nil {
			return nil, false, fmt.Errorf("invalid VoyageAI selector: %w", err)
		}
	}
	var resources vaiv1.VoyageAIList
	if err := r.kubeClient.List(ctx, &resources, client.InNamespace(router.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, false, fmt.Errorf("list VoyageAI resources: %w", err)
	}

	byModel := make(map[vaiv1.VoyageAIModel]*voyageRouterModel)
	for _, model := range vaiv1.SupportedVoyageAIModels() {
		byModel[model] = &voyageRouterModel{Model: model}
	}
	needsUpstreamCA := false
	for _, vai := range resources.Items {
		if !vai.DeletionTimestamp.IsZero() {
			continue
		}
		model, exists := byModel[vai.Spec.Model]
		if !exists {
			continue
		}
		model.Selected = true
		tlsEnabled := vai.IsTLSConfigured()
		needsUpstreamCA = needsUpstreamCA || tlsEnabled
		if vai.Status.Phase != status.PhaseRunning {
			continue
		}
		model.Backends = append(model.Backends, voyageRouterBackend{
			ServiceFQDN: dns.GetServiceFQDN(vai.Name+"-svc", router.Namespace, r.clusterDomain),
			Port:        vai.Spec.Server.Port,
			TLS:         tlsEnabled,
		})
	}
	models := make([]voyageRouterModel, 0, len(byModel))
	for _, model := range vaiv1.SupportedVoyageAIModels() {
		models = append(models, *byModel[model])
	}
	return models, needsUpstreamCA, nil
}

func (r *VoyageRouterReconciler) validateInboundTLS(ctx context.Context, router *vaiv1.VoyageRouter) (map[string][]byte, error) {
	if !router.IsTLSConfigured() {
		return nil, nil
	}
	secret := &corev1.Secret{}
	key := router.InboundTLSSecretNamespacedName()
	if err := r.kubeClient.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("inbound TLS certificate Secret %q not found", key.Name)
		}
		return nil, fmt.Errorf("get inbound TLS certificate Secret %q: %w", key.Name, err)
	}
	if len(secret.Data[corev1.TLSCertKey]) == 0 || len(secret.Data[corev1.TLSPrivateKeyKey]) == 0 {
		return nil, fmt.Errorf("inbound TLS certificate Secret %q must contain %q and %q", key.Name, corev1.TLSCertKey, corev1.TLSPrivateKeyKey)
	}
	return map[string][]byte{
		corev1.TLSCertKey:       secret.Data[corev1.TLSCertKey],
		corev1.TLSPrivateKeyKey: secret.Data[corev1.TLSPrivateKeyKey],
	}, nil
}

func (r *VoyageRouterReconciler) validateUpstreamCA(ctx context.Context, router *vaiv1.VoyageRouter) ([]byte, error) {
	if router.Spec.Security.UpstreamTLS == nil || router.Spec.Security.UpstreamTLS.CA.Name == "" {
		return nil, fmt.Errorf("security.upstreamTLS.ca is required when a selected VoyageAI uses TLS")
	}
	configMap := &corev1.ConfigMap{}
	key := router.UpstreamCAConfigMapNamespacedName()
	if err := r.kubeClient.Get(ctx, key, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("upstream TLS CA ConfigMap %q not found", key.Name)
		}
		return nil, fmt.Errorf("get upstream TLS CA ConfigMap %q: %w", key.Name, err)
	}
	if configMap.Data[voyageRouterUpstreamCAFile] == "" {
		return nil, fmt.Errorf("upstream TLS CA ConfigMap %q must contain %q", key.Name, voyageRouterUpstreamCAFile)
	}
	return []byte(configMap.Data[voyageRouterUpstreamCAFile]), nil
}

func (r *VoyageRouterReconciler) reconcileBlockedCA(ctx context.Context, router *vaiv1.VoyageRouter, models []voyageRouterModel, cause error, log *zap.SugaredLogger) (reconcile.Result, error) {
	bootstrapJSON, configErr := buildVoyageRouterBootstrapJSON()
	if configErr != nil {
		return r.updateStatus(ctx, router, workflow.Failed(configErr), false, false, log)
	}
	cdsJSON, configErr := buildVoyageRouterCDSJSON(models)
	if configErr != nil {
		return r.updateStatus(ctx, router, workflow.Failed(configErr), false, false, log)
	}
	ldsJSON, configErr := buildVoyageRouterLDSJSON(models, router.RequestBodyLimitBytesOrDefault(), router.IsTLSConfigured())
	if configErr != nil {
		return r.updateStatus(ctx, router, workflow.Failed(configErr), false, false, log)
	}
	if err := r.ensureConfigMap(ctx, router, bootstrapJSON, cdsJSON, ldsJSON); err != nil {
		return r.updateStatus(ctx, router, workflow.Failed(err), false, false, log)
	}
	return r.updateStatus(ctx, router, workflow.Pending("%s", cause), false, false, log)
}

func (r *VoyageRouterReconciler) buildConfig(router *vaiv1.VoyageRouter, models []voyageRouterModel) (string, string, string, error) {
	bootstrapJSON, err := buildVoyageRouterBootstrapJSON()
	if err != nil {
		return "", "", "", err
	}
	cdsJSON, err := buildVoyageRouterCDSJSON(models)
	if err != nil {
		return "", "", "", err
	}
	ldsJSON, err := buildVoyageRouterLDSJSON(models, router.RequestBodyLimitBytesOrDefault(), router.IsTLSConfigured())
	if err != nil {
		return "", "", "", err
	}
	return bootstrapJSON, cdsJSON, ldsJSON, nil
}

func (r *VoyageRouterReconciler) ensureConfigMap(ctx context.Context, router *vaiv1.VoyageRouter, bootstrapJSON, cdsJSON, ldsJSON string) error {
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: router.ConfigMapName(), Namespace: router.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, configMap, func() error {
		configMap.Labels = voyageRouterLabels(router)
		configMap.Data = map[string]string{"bootstrap.json": bootstrapJSON, "cds.json": cdsJSON, "lds.json": ldsJSON}
		return controllerutil.SetControllerReference(router, configMap, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("ensure Router ConfigMap: %w", err)
	}
	return nil
}

func (r *VoyageRouterReconciler) ensureDeployment(ctx context.Context, router *vaiv1.VoyageRouter, bootstrapJSON string, tlsMaterial []byte, mountUpstreamCA bool) (*appsv1.Deployment, error) {
	configHash, err := envoyConfigHash(bootstrapJSON, tlsMaterial)
	if err != nil {
		return nil, err
	}
	configurePodSecurity, configureContainerSecurity := podtemplatespec.WithDefaultSecurityContextsModifications()
	volumes := []corev1.Volume{statefulset.CreateVolumeFromConfigMap("router-config", router.ConfigMapName())}
	volumeMounts := []corev1.VolumeMount{statefulset.CreateVolumeMount("router-config", voyageRouterConfigPath, statefulset.WithReadOnly(true))}
	if router.IsTLSConfigured() {
		volumes = append(volumes, statefulset.CreateVolumeFromSecret("inbound-tls", router.InboundTLSSecretNamespacedName().Name))
		volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount("inbound-tls", voyageRouterInboundTLSPath, statefulset.WithReadOnly(true)))
	}
	if mountUpstreamCA {
		volumes = append(volumes, statefulset.CreateVolumeFromConfigMap("upstream-ca", router.UpstreamCAConfigMapNamespacedName().Name))
		volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount("upstream-ca", voyageRouterUpstreamTLSPath, statefulset.WithReadOnly(true)))
	}
	podLabels := voyageRouterPodLabels(router)
	podTemplateMods := []podtemplatespec.Modification{
		podtemplatespec.WithPodLabels(podLabels),
		podtemplatespec.WithAnnotations(map[string]string{envoyConfigHashAnnotation: configHash}),
		configurePodSecurity,
		podtemplatespec.WithVolumes(volumes),
	}
	containerMods := []container.Modification{
		container.WithName("envoy"),
		configureContainerSecurity,
		container.WithImage(r.defaultEnvoyImage),
		container.WithCommand([]string{"/usr/local/bin/envoy"}),
		container.WithArgs([]string{"-c", voyageRouterConfigPath + "/bootstrap.json", "--log-level", "info", "--drain-time-s", "30", "--drain-strategy", "immediate"}),
		container.WithPorts([]corev1.ContainerPort{{Name: "http", ContainerPort: voyageRouterListenerPort}, {Name: "admin", ContainerPort: EnvoyAdminPort}}),
		container.WithResourceRequirements(voyageRouterResourceRequirements(router.Spec.ResourceRequirements)),
		container.WithVolumeMounts(volumeMounts),
		container.WithReadinessProbe(probes.Apply(probes.WithHandler(voyageRouterAdminProbe()), probes.WithInitialDelaySeconds(5), probes.WithPeriodSeconds(5))),
		container.WithLivenessProbe(probes.Apply(probes.WithHandler(voyageRouterAdminProbe()), probes.WithInitialDelaySeconds(10), probes.WithPeriodSeconds(10), probes.WithFailureThreshold(3))),
		container.WithLifecycle(lifecycle.Apply(lifecycle.WithPrestopCommand([]string{"/usr/bin/bash", "-c", fmt.Sprintf("( exec 3<>/dev/tcp/127.0.0.1/%d; printf 'POST /drain_listeners?graceful=true HTTP/1.1\\r\\nHost: localhost\\r\\nContent-Length: 0\\r\\nConnection: close\\r\\n\\r\\n' >&3; cat <&3 >/dev/null ) || true; sleep 30", EnvoyAdminPort)}))),
	}
	podTemplateMods = append(podTemplateMods, podtemplatespec.WithContainer("envoy", container.Apply(containerMods...)))
	modifications := []deployment.Modification{
		deployment.WithName(router.DeploymentName()),
		deployment.WithNamespace(router.Namespace),
		deployment.WithLabels(voyageRouterLabels(router)),
		deployment.WithMatchLabels(podLabels),
		deployment.WithReplicas(router.ReplicasOrDefault()),
		deployment.WithPodSpecTemplate(podtemplatespec.Apply(podTemplateMods...)),
	}
	deploymentResource := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: router.DeploymentName(), Namespace: router.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.kubeClient, deploymentResource, func() error {
		deploymentResource.Spec.Template = corev1.PodTemplateSpec{}
		deployment.Apply(modifications...)(deploymentResource)
		if router.Spec.Deployment != nil {
			deploymentResource.Spec = merge.DeploymentSpecs(deploymentResource.Spec, router.Spec.Deployment.SpecWrapper.Spec)
			deploymentResource.Labels = merge.StringToStringMap(deploymentResource.Labels, router.Spec.Deployment.MetadataWrapper.Labels)
			deploymentResource.Annotations = merge.StringToStringMap(deploymentResource.Annotations, router.Spec.Deployment.MetadataWrapper.Annotations)
		}
		deploymentResource.Labels = merge.StringToStringMap(deploymentResource.Labels, voyageRouterLabels(router))
		return controllerutil.SetControllerReference(router, deploymentResource, r.kubeClient.Scheme())
	})
	if err != nil {
		return nil, fmt.Errorf("ensure Router Deployment: %w", err)
	}
	return deploymentResource, nil
}

func (r *VoyageRouterReconciler) ensureService(ctx context.Context, router *vaiv1.VoyageRouter) error {
	desired := service.Builder().
		SetName(router.ServiceName()).
		SetNamespace(router.Namespace).
		SetLabels(voyageRouterLabels(router)).
		SetSelector(voyageRouterPodLabels(router)).
		SetServiceType(corev1.ServiceTypeClusterIP).
		AddPort(&corev1.ServicePort{Name: "http", Port: voyageRouterListenerPort, TargetPort: intstr.FromInt32(voyageRouterListenerPort), Protocol: corev1.ProtocolTCP}).
		Build()
	serviceResource := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, serviceResource, func() error {
		resourceVersion, clusterIP := serviceResource.ResourceVersion, serviceResource.Spec.ClusterIP
		*serviceResource = desired
		serviceResource.ResourceVersion = resourceVersion
		if clusterIP != "" {
			serviceResource.Spec.ClusterIP = clusterIP
		}
		return controllerutil.SetControllerReference(router, serviceResource, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("ensure Router Service: %w", err)
	}
	return nil
}

func (r *VoyageRouterReconciler) updateStatus(ctx context.Context, router *vaiv1.VoyageRouter, reconcileStatus workflow.Status, ready, reconciled bool, log *zap.SugaredLogger) (reconcile.Result, error) {
	if !ready && !reconciled {
		for _, condition := range router.Status.Conditions {
			if condition.Type == vaiv1.VoyageRouterConditionReady && condition.Status == metav1.ConditionTrue {
				ready = true
				break
			}
		}
	}
	conditionStatus := metav1.ConditionFalse
	if ready {
		conditionStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&router.Status.Conditions, metav1.Condition{Type: vaiv1.VoyageRouterConditionReady, Status: conditionStatus, Reason: string(reconcileStatus.Phase()), Message: voyageRouterStatusMessage(reconcileStatus), ObservedGeneration: router.Generation})
	conditionStatus = metav1.ConditionFalse
	if reconciled {
		conditionStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&router.Status.Conditions, metav1.Condition{Type: vaiv1.VoyageRouterConditionReconciled, Status: conditionStatus, Reason: string(reconcileStatus.Phase()), Message: voyageRouterStatusMessage(reconcileStatus), ObservedGeneration: router.Generation})
	return commoncontroller.UpdateStatus(ctx, r.kubeClient, router, reconcileStatus, log)
}

func voyageRouterStatusMessage(reconcileStatus workflow.Status) string {
	if reconcileStatus.IsOK() {
		return "Router resources are current"
	}
	return searchcontroller.MessageFromStatus(reconcileStatus)
}

func voyageRouterResourceRequirements(requirements *corev1.ResourceRequirements) corev1.ResourceRequirements {
	if requirements != nil {
		return *requirements
	}
	return defaultEnvoyResourceRequirements()
}

func voyageRouterAdminProbe() corev1.ProbeHandler {
	return corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(EnvoyAdminPort)}}
}

func voyageRouterLabels(router *vaiv1.VoyageRouter) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       voyageRouterComponent,
		"app.kubernetes.io/instance":   router.Name,
		"app.kubernetes.io/managed-by": "mongodb-kubernetes-operator",
	}
}

func voyageRouterPodLabels(router *vaiv1.VoyageRouter) map[string]string {
	return map[string]string{"app": router.DeploymentName(), "app.kubernetes.io/name": voyageRouterComponent, "app.kubernetes.io/instance": router.Name}
}

func AddVoyageRouterController(ctx context.Context, mgr manager.Manager, defaultEnvoyImage, clusterDomain string) error {
	r := newVoyageRouterReconciler(mgr.GetClient(), defaultEnvoyImage, clusterDomain)
	if err := mgr.GetFieldIndexer().IndexField(ctx, &vaiv1.VoyageRouter{}, voyageRouterTLSCertSecretIndex, func(object client.Object) []string {
		router := object.(*vaiv1.VoyageRouter)
		if !router.IsTLSConfigured() {
			return nil
		}
		return []string{router.InboundTLSSecretNamespacedName().Name}
	}); err != nil {
		return xerrors.Errorf("index VoyageRouter inbound TLS Secret: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &vaiv1.VoyageRouter{}, voyageRouterUpstreamCAIndex, func(object client.Object) []string {
		router := object.(*vaiv1.VoyageRouter)
		if router.Spec.Security.UpstreamTLS == nil {
			return nil
		}
		return []string{router.UpstreamCAConfigMapNamespacedName().Name}
	}); err != nil {
		return xerrors.Errorf("index VoyageRouter upstream CA ConfigMap: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		For(&vaiv1.VoyageRouter{}).
		Watches(&vaiv1.VoyageAI{}, handler.EnqueueRequestsFromMapFunc(r.mapVoyageAIToRouters)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapSecretToRouters)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.mapConfigMapToRouters)).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *VoyageRouterReconciler) mapVoyageAIToRouters(ctx context.Context, object client.Object) []reconcile.Request {
	var routers vaiv1.VoyageRouterList
	if err := r.kubeClient.List(ctx, &routers, client.InNamespace(object.GetNamespace())); err != nil {
		zap.S().Errorf("list VoyageRouters for VoyageAI %s/%s: %v", object.GetNamespace(), object.GetName(), err)
		return nil
	}
	return routerRequests(routers.Items)
}

func (r *VoyageRouterReconciler) mapSecretToRouters(ctx context.Context, object client.Object) []reconcile.Request {
	return r.indexedRouterRequests(ctx, voyageRouterTLSCertSecretIndex, object.GetName(), object.GetNamespace())
}

func (r *VoyageRouterReconciler) mapConfigMapToRouters(ctx context.Context, object client.Object) []reconcile.Request {
	return r.indexedRouterRequests(ctx, voyageRouterUpstreamCAIndex, object.GetName(), object.GetNamespace())
}

func (r *VoyageRouterReconciler) indexedRouterRequests(ctx context.Context, index, name, namespace string) []reconcile.Request {
	var routers vaiv1.VoyageRouterList
	if err := r.kubeClient.List(ctx, &routers, client.InNamespace(namespace), client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector(index, name)}); err != nil {
		zap.S().Errorf("list VoyageRouters for %s=%s in %s: %v", index, name, namespace, err)
		return nil
	}
	return routerRequests(routers.Items)
}

func routerRequests(routers []vaiv1.VoyageRouter) []reconcile.Request {
	requests := make([]reconcile.Request, 0, len(routers))
	for _, router := range routers {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: router.Name, Namespace: router.Namespace}})
	}
	return requests
}
