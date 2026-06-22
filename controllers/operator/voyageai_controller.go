package operator

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/fields"
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

	vaiv1 "github.com/mongodb/mongodb-kubernetes/api/voyageai/v1/vai"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/deployment"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

const (
	voyageAITLSCertPath = "/etc/voyageai/tls"

	voyageAITLSCertFile = voyageAITLSCertPath + "/tls.crt"
	voyageAITLSKeyFile  = voyageAITLSCertPath + "/tls.key"

	voyageAIStartupPath   = "/health/startup"
	voyageAIReadinessPath = "/health/readiness"
	voyageAILivenessPath  = "/health/liveness"

	// voyageAITLSCertSecretIndex lets the cache find VoyageAI resources that
	// reference a given Secret, so the map-func can look up dependents in O(1)
	// when a watched Secret changes.
	voyageAITLSCertSecretIndex = ".spec.security.tls.certificateKeySecretRef.name"

	// defaultVoyageAIImageRepository is the registry path used when the operator
	// is not configured with the MDB_VOYAGEAI_REPO_URL environment variable. It
	// is credential-protected; the pull secret configured via the operator's
	// IMAGE_PULL_SECRETS environment variable is attached to VoyageAI pods.
	// Airgapped or private deployments point MDB_VOYAGEAI_REPO_URL at a mirror.
	defaultVoyageAIImageRepository = "quay.io/mongodb/voyageai"
)

type VoyageAIReconciler struct {
	kubeClient      kubernetesClient.Client
	imageRepository string
}

func newVoyageAIReconciler(client client.Client, imageRepository string) *VoyageAIReconciler {
	return &VoyageAIReconciler{
		kubeClient:      kubernetesClient.NewClient(client),
		imageRepository: imageRepository,
	}
}

// +kubebuilder:rbac:groups=ai.mongodb.com,resources={voyageais,voyageais/status,voyageais/finalizers},verbs=*,namespace=placeholder
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
func (r *VoyageAIReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("VoyageAI", request.NamespacedName)
	log.Info("-> VoyageAI.Reconcile")

	vai := &vaiv1.VoyageAI{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, vai, log); err != nil {
		return result, err
	}

	if err := vai.Validate(); err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, workflow.Failed(xerrors.Errorf("validation failed: %w", err)), log)
	}

	// When TLS is configured the Deployment mounts the referenced Secret as a
	// volume. If that Secret is missing, the pods hang in ContainerCreating with
	// no actionable signal in the resource status. Check up front and surface a
	// clear Pending message; the Secret watch re-triggers reconcile once it
	// appears.
	if vai.IsTLSConfigured() {
		secretName := vai.Spec.Security.TLS.CertificateKeySecretRef.Name
		secret := &corev1.Secret{}
		err := r.kubeClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: vai.Namespace}, secret)
		if apierrors.IsNotFound(err) {
			return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, workflow.Pending("TLS certificate secret %q not found", secretName), log)
		} else if err != nil {
			return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, workflow.Failed(xerrors.Errorf("failed to get TLS certificate secret %q: %w", secretName, err)), log)
		}
	}

	dep, err := r.ensureDeployment(ctx, vai, log)
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, workflow.Failed(xerrors.Errorf("failed to ensure Deployment: %w", err)), log)
	}

	if err := r.ensureService(ctx, vai, log); err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, workflow.Failed(xerrors.Errorf("failed to ensure Service: %w", err)), log)
	}

	log.Info("VoyageAI reconciliation complete")

	// Report status.version only once the Deployment is fully rolled out, mirroring
	// the MongoDBSearch controller, which attaches its version option after the
	// StatefulSet readiness check passes. This keeps status.version reflecting the
	// running version rather than the desired one while a rollout is in progress.
	if deploymentStatus := deployment.GetDeploymentStatus(ctx, vai.Namespace, vai.Name, dep.GetGeneration(), r.kubeClient); !deploymentStatus.IsOK() {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, deploymentStatus, log)
	}

	return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, workflow.OK(), log, vaiv1.NewVoyageAIVersionOption(vai.Spec.Version))
}

func (r *VoyageAIReconciler) ensureDeployment(ctx context.Context, vai *vaiv1.VoyageAI, log *zap.SugaredLogger) (*appsv1.Deployment, error) {
	image := r.voyageAIContainerImage(vai)
	labels := voyageAILabels(vai)
	podLabels := voyageAIPodLabels(vai)
	tlsEnabled := vai.IsTLSConfigured()

	probeScheme := corev1.URISchemeHTTP
	if tlsEnabled {
		probeScheme = corev1.URISchemeHTTPS
	}

	containerPorts := []corev1.ContainerPort{
		{Name: "http", ContainerPort: vai.Spec.Server.Port, Protocol: corev1.ProtocolTCP},
	}

	if m := vai.Spec.Metrics; m != nil {
		containerPorts = append(containerPorts, corev1.ContainerPort{
			Name:          "metrics",
			ContainerPort: m.Port,
			Protocol:      corev1.ProtocolTCP,
		})
	}

	configurePodSpecSecurityContext, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	probePort := vai.Spec.Server.Port

	containerMods := []container.Modification{
		container.WithName("voyageai"),
		configureContainerSecurityContext,
		container.WithImage(image),
		container.WithPorts(containerPorts),
		container.WithEnvs(buildEnvVars(&vai.Spec, tlsEnabled)...),
		container.WithResourceRequirements(buildResourceRequirements(vai.Spec.ResourceRequirements)),
		container.WithStartupProbe(probes.Apply(
			probes.WithHandler(buildProbeHandler(voyageAIStartupPath, probePort, probeScheme)),
			// GPU model weights can take many minutes to load into device memory.
			// 60 failures × 10s period = 10 minutes before Kubernetes gives up.
			probes.WithPeriodSeconds(10),
			probes.WithFailureThreshold(60),
			probes.WithTimeoutSeconds(5),
		)),
		container.WithReadinessProbe(probes.Apply(
			probes.WithHandler(buildProbeHandler(voyageAIReadinessPath, probePort, probeScheme)),
			probes.WithPeriodSeconds(10),
			probes.WithFailureThreshold(3),
			probes.WithTimeoutSeconds(5),
		)),
		container.WithLivenessProbe(probes.Apply(
			probes.WithHandler(buildProbeHandler(voyageAILivenessPath, probePort, probeScheme)),
			probes.WithPeriodSeconds(10),
			probes.WithFailureThreshold(3),
			probes.WithTimeoutSeconds(5),
		)),
	}

	podTemplateMods := []podtemplatespec.Modification{
		podtemplatespec.WithPodLabels(podLabels),
		configurePodSpecSecurityContext,
		podtemplatespec.WithTolerations([]corev1.Toleration{
			{
				Key:      "nvidia.com/gpu",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		}),
	}

	// VoyageAI images on quay.io/mongodb/voyageai are credential-protected;
	// attach the operator-wide pull secret to the pods when configured.
	if pullSecrets, found := env.Read(util.ImagePullSecrets); found { // nolint:forbidigo
		podTemplateMods = append(podTemplateMods, func(pts *corev1.PodTemplateSpec) {
			for _, v := range pts.Spec.ImagePullSecrets {
				if v.Name == pullSecrets {
					return
				}
			}
			pts.Spec.ImagePullSecrets = append(pts.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: pullSecrets})
		})
	}

	if tlsEnabled {
		tlsCfg := vai.Spec.Security.TLS

		tlsCertVolume := statefulset.CreateVolumeFromSecret("tls-cert", tlsCfg.CertificateKeySecretRef.Name)
		tlsCertVolumeMount := statefulset.CreateVolumeMount("tls-cert", voyageAITLSCertPath, statefulset.WithReadOnly(true))

		podTemplateMods = append(podTemplateMods, podtemplatespec.WithVolume(tlsCertVolume))
		containerMods = append(containerMods,
			container.Apply(
				container.WithEnvs(
					corev1.EnvVar{Name: "SERVER__TLS__CERTFILE", Value: voyageAITLSCertFile},
					corev1.EnvVar{Name: "SERVER__TLS__KEYFILE", Value: voyageAITLSKeyFile},
				),
				container.WithVolumeMounts([]corev1.VolumeMount{tlsCertVolumeMount}),
			))
	}

	// The default security context sets readOnlyRootFilesystem=true, so the
	// container cannot write to the root filesystem. Mount an emptyDir at /tmp
	// to provide a writable scratch space for temporary files.
	tmpVolume := statefulset.CreateVolumeFromEmptyDir("tmp")
	tmpVolumeMount := statefulset.CreateVolumeMount("tmp", "/tmp", statefulset.WithReadOnly(false))
	podTemplateMods = append(podTemplateMods, podtemplatespec.WithVolume(tmpVolume))
	containerMods = append(containerMods, container.WithVolumeMounts([]corev1.VolumeMount{tmpVolumeMount}))

	if vai.Spec.NodeAffinity != nil {
		podTemplateMods = append(podTemplateMods, func(pts *corev1.PodTemplateSpec) {
			pts.Spec.Affinity = &corev1.Affinity{NodeAffinity: vai.Spec.NodeAffinity}
		})
	}

	podTemplateMods = append(podTemplateMods,
		podtemplatespec.WithContainer("voyageai", container.Apply(containerMods...)),
	)

	modifications := []deployment.Modification{
		deployment.WithName(vai.Name),
		deployment.WithNamespace(vai.Namespace),
		deployment.WithLabels(labels),
		deployment.WithMatchLabels(podLabels),
		deployment.WithReplicas(vai.Spec.Replicas),
		// VoyageAI pods each require a dedicated GPU (nvidia.com/gpu: 1). The default
		// RollingUpdate strategy surges a new pod before terminating the old one, so
		// on GPU-constrained nodes the new pod cannot schedule (no free GPU) and the
		// rollout wedges. Recreate tears down old pods first, freeing GPUs for the
		// replacement, at the cost of brief downtime during upgrades.
		deployment.WithStrategyType(appsv1.RecreateDeploymentStrategyType),
		deployment.WithPodSpecTemplate(podtemplatespec.Apply(podTemplateMods...)),
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vai.Name,
			Namespace: vai.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, dep, func() error {
		// Reset the pod template before rebuilding it. The builder helpers merge
		// by name (WithContainer appends, WithEnvs replaces-or-appends), so
		// without this an element that is no longer desired would persist across
		// reconciles — e.g. the TLS cert volume and SERVER__TLS__* env would
		// linger after TLS is disabled. Rebuilding from empty makes the desired
		// spec authoritative.
		dep.Spec.Template = corev1.PodTemplateSpec{}
		deployment.Apply(modifications...)(dep)
		return controllerutil.SetOwnerReference(vai, dep, r.kubeClient.Scheme())
	})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure VoyageAI Deployment: %w", err)
	}

	log.Info("VoyageAI Deployment created/updated")
	return dep, nil
}

func buildEnvVars(spec *vaiv1.VoyageAISpec, tlsEnabled bool) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		// The default security context runs as UID 2000 which has no /etc/passwd
		// entry. Python's getpass.getuser() calls pwd.getpwuid(os.getuid()) and
		// raises KeyError when the UID is missing. Setting USER makes getuser()
		// return early from the environment without the passwd lookup. This fixes
		// crashes in PyTorch/vLLM code paths (both the main process and any
		// subprocesses they spawn) that call getuser() to build cache directory
		// paths.
		{Name: "USER", Value: "voyageai"},
		// Set HOME so that tools using ~/.cache (e.g. flashinfer JIT workspace,
		// HuggingFace hub) write under /tmp instead of the read-only root
		// filesystem.
		{Name: "HOME", Value: "/tmp"},
		// PyTorch's TorchInductor derives its default cache directory by resolving
		// the current UID via getpwuid(), which fails when the UID (set by the
		// default security context) has no /etc/passwd entry. Point it at /tmp
		// explicitly so it never attempts the lookup.
		{Name: "TORCHINDUCTOR_CACHE_DIR", Value: "/tmp/torchinductor_cache"},
		// Internal hardcoded paths
		{Name: "SERVER__INFO_PATH", Value: "/info"},
		{Name: "SERVER__STARTUP_PATH", Value: voyageAIStartupPath},
		{Name: "SERVER__READINESS_PATH", Value: voyageAIReadinessPath},
		{Name: "SERVER__LIVENESS_PATH", Value: voyageAILivenessPath},
		{Name: "SERVER__OPENAPI_PATH", Value: "/openapi.json"},
		{Name: "SERVER__EMBEDDINGS_PATH", Value: "/v1/embeddings"},
		{Name: "SERVER__CONTEXTUALIZED_EMBEDDINGS_PATH", Value: "/v1/contextualizedembeddings"},
		{Name: "SERVER__MULTIMODAL_EMBEDDINGS_PATH", Value: "/v1/multimodalembeddings"},
		{Name: "SERVER__RERANK_PATH", Value: "/v1/rerank"},
		{Name: "SERVER__TLS__ENABLED", Value: strconv.FormatBool(tlsEnabled)},
	}

	// Server config
	{
		s := spec.Server
		envs = append(envs,
			corev1.EnvVar{Name: "SERVER__PORT", Value: int32ToString(s.Port)},
			corev1.EnvVar{Name: "SERVER__WORKERS", Value: int32ToString(s.Workers)},
			corev1.EnvVar{Name: "SERVER__TIMEOUT", Value: int32ToString(s.Timeout)},
			corev1.EnvVar{Name: "SERVER__MAX_REQUESTS", Value: int32ToString(s.MaxRequests)},
			corev1.EnvVar{Name: "SERVER__MAX_REQUESTS_JITTER", Value: int32ToString(s.MaxRequestsJitter)},
		)
	}

	// Metrics config
	if m := spec.Metrics; m != nil {
		envs = append(envs,
			corev1.EnvVar{Name: "SERVER__METRICS__ENABLED", Value: strconv.FormatBool(m.Enabled)},
			corev1.EnvVar{Name: "SERVER__METRICS__PATH", Value: m.Path},
			corev1.EnvVar{Name: "SERVER__METRICS__PORT", Value: int32ToString(m.Port)},
		)
	}

	// DataParallel config
	if spec.DataParallel.Enabled {
		dp := spec.DataParallel
		envs = append(envs,
			corev1.EnvVar{Name: "DATA_PARALLEL__ENABLED", Value: strconv.FormatBool(true)},
			corev1.EnvVar{Name: "DATA_PARALLEL__WORKER_INIT_TIMEOUT", Value: int32ToFloatString(dp.WorkerInitTimeoutSeconds)},
			corev1.EnvVar{Name: "DATA_PARALLEL__WORKER_EXECUTION_TIMEOUT", Value: int32ToFloatString(dp.WorkerExecutionTimeoutSeconds)},
			corev1.EnvVar{Name: "DATA_PARALLEL__WORKER_QUEUE_MAXSIZE", Value: int32ToString(dp.WorkerQueueMaxSize)},
			corev1.EnvVar{Name: "DATA_PARALLEL__LOAD_BALANCING_STRATEGY", Value: dp.LoadBalancingStrategy},
			corev1.EnvVar{Name: "DATA_PARALLEL__SOCKET_PATH_PREFIX", Value: "/tmp"},
		)

		if dp.NumWorkers != nil {
			envs = append(envs, corev1.EnvVar{Name: "DATA_PARALLEL__NUM_WORKERS", Value: int32ToString(*dp.NumWorkers)})
		} else {
			envs = append(envs, corev1.EnvVar{Name: "DATA_PARALLEL__NUM_WORKERS", Value: "auto"})
		}

		if dp.Batching != nil {
			b := dp.Batching
			envs = append(envs,
				corev1.EnvVar{Name: "DATA_PARALLEL__BATCHING__STRATEGY", Value: b.Strategy},
				corev1.EnvVar{Name: "DATA_PARALLEL__BATCHING__MAX_WAIT_TIME", Value: msToSecondsFloat(b.MaxWaitTimeMs)},
				corev1.EnvVar{Name: "DATA_PARALLEL__BATCHING__MAX_QUEUE_SIZE", Value: int32ToString(b.MaxQueueSize)},
			)
		}

		if dp.HealthMonitoring != nil {
			hm := dp.HealthMonitoring
			envs = append(envs,
				corev1.EnvVar{Name: "DATA_PARALLEL__HEALTH_MONITORING__CHECK_INTERVAL", Value: int32ToFloatString(hm.CheckIntervalSeconds)},
				corev1.EnvVar{Name: "DATA_PARALLEL__HEALTH_MONITORING__MAX_CONSECUTIVE_TIMEOUTS", Value: int32ToString(hm.MaxConsecutiveTimeouts)},
				corev1.EnvVar{Name: "DATA_PARALLEL__HEALTH_MONITORING__ACTIVE_CHECK_INTERVAL", Value: int32ToString(hm.ActiveCheckIntervalSeconds)},
				corev1.EnvVar{Name: "DATA_PARALLEL__HEALTH_MONITORING__ACTIVE_CHECK_TIMEOUT", Value: int32ToString(hm.ActiveCheckTimeoutSeconds)},
				corev1.EnvVar{Name: "DATA_PARALLEL__HEALTH_MONITORING__MAX_RESTART_ATTEMPTS", Value: int32ToString(hm.MaxRestartAttempts)},
				corev1.EnvVar{Name: "DATA_PARALLEL__HEALTH_MONITORING__RESTART_COOLDOWN", Value: int32ToFloatString(hm.RestartCooldownSeconds)},
			)

			if hm.EnableActiveChecks != nil {
				envs = append(envs, corev1.EnvVar{Name: "DATA_PARALLEL__HEALTH_MONITORING__ENABLE_ACTIVE_CHECKS", Value: strconv.FormatBool(*hm.EnableActiveChecks)})
			}
		}
	}

	return envs
}

func buildResourceRequirements(userReqs *corev1.ResourceRequirements) corev1.ResourceRequirements {
	gpuQuantity := resource.MustParse("1")

	// Start with GPU floor: at least 1 GPU is always required
	result := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			"nvidia.com/gpu": gpuQuantity,
		},
		Limits: corev1.ResourceList{
			"nvidia.com/gpu": gpuQuantity,
		},
	}

	if userReqs != nil {
		// Merge user values on top; user can override GPU to a higher value
		maps.Copy(result.Requests, userReqs.Requests)
		maps.Copy(result.Limits, userReqs.Limits)
	}

	return result
}

func (r *VoyageAIReconciler) ensureService(ctx context.Context, vai *vaiv1.VoyageAI, log *zap.SugaredLogger) error {
	svcBuilder := service.Builder().
		SetName(vai.Name + "-svc").
		SetNamespace(vai.Namespace).
		SetLabels(voyageAILabels(vai)).
		SetSelector(voyageAIPodLabels(vai)).
		SetServiceType(corev1.ServiceTypeClusterIP).
		AddPort(&corev1.ServicePort{
			Name:       "http",
			Port:       vai.Spec.Server.Port,
			TargetPort: intstr.FromInt32(vai.Spec.Server.Port),
			Protocol:   corev1.ProtocolTCP,
		})
	if m := vai.Spec.Metrics; m != nil {
		svcBuilder = svcBuilder.AddPort(&corev1.ServicePort{
			Name:       "metrics",
			Port:       m.Port,
			TargetPort: intstr.FromInt32(m.Port),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	desired := svcBuilder.Build()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desired.Name,
			Namespace: desired.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, svc, func() error {
		// Surgically set only the fields the operator owns, leaving Kubernetes-managed
		// fields (ClusterIP, ResourceVersion) and any out-of-band labels/annotations
		// intact. ClusterIP is assigned by Kubernetes on creation and is immutable, so
		// we must never clobber it.
		svc.Labels = desired.Labels
		svc.Spec.Type = desired.Spec.Type
		svc.Spec.Selector = desired.Spec.Selector
		svc.Spec.Ports = desired.Spec.Ports
		return controllerutil.SetOwnerReference(vai, svc, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("failed to ensure VoyageAI Service: %w", err)
	}

	log.Info("VoyageAI Service created/updated")
	return nil
}

func (r *VoyageAIReconciler) voyageAIContainerImage(vai *vaiv1.VoyageAI) string {
	return fmt.Sprintf("%s/%s:%s", r.imageRepository, vai.Spec.Model, vai.Spec.Version)
}

func voyageAILabels(vai *vaiv1.VoyageAI) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "voyageai",
		"app.kubernetes.io/instance":   vai.Name,
		"app.kubernetes.io/managed-by": "mongodb-kubernetes-operator",
		"app.kubernetes.io/component":  string(vai.Spec.Model),
	}
}

func voyageAIPodLabels(vai *vaiv1.VoyageAI) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "voyageai",
		"app.kubernetes.io/instance": vai.Name,
	}
}

// buildProbeHandler returns an HTTP GET probe handler for the given path, port
// and scheme. With server-only TLS the kubelet's HTTPS prober reaches the
// server directly. mTLS (which the kubelet cannot satisfy) is intentionally not
// supported by the operator — use a service mesh for mutual TLS.
func buildProbeHandler(path string, port int32, scheme corev1.URIScheme) corev1.ProbeHandler {
	return corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Path:   path,
			Port:   intstr.FromInt32(port),
			Scheme: scheme,
		},
	}
}

func int32ToString(v int32) string {
	return strconv.FormatInt(int64(v), 10)
}

func int32ToFloatString(v int32) string {
	return fmt.Sprintf("%s.0", int32ToString(v))
}

func msToSecondsFloat(ms int32) string {
	return fmt.Sprintf("%.2f", float64(ms)/1000.0)
}

func AddVoyageAIController(ctx context.Context, mgr manager.Manager, imageRepository string, maxConcurrentReconciles int) error {
	r := newVoyageAIReconciler(mgr.GetClient(), imageRepository)

	// Index VoyageAI resources by the name of the TLS cert Secret they reference,
	// so the map-func can enqueue dependents when that Secret changes.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &vaiv1.VoyageAI{}, voyageAITLSCertSecretIndex, func(o client.Object) []string {
		vai := o.(*vaiv1.VoyageAI)
		if vai.Spec.Security.TLS == nil {
			return nil
		}
		return []string{vai.Spec.Security.TLS.CertificateKeySecretRef.Name}
	}); err != nil {
		return xerrors.Errorf("failed to index VoyageAI by TLS cert secret name: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		For(&vaiv1.VoyageAI{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapSecretToVoyageAI)).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// mapSecretToVoyageAI returns reconcile requests for VoyageAI resources that
// reference the given Secret via spec.security.tls.certificateKeySecretRef.
// The cache index ensures this lookup is O(1) regardless of resource count.
func (r *VoyageAIReconciler) mapSecretToVoyageAI(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.enqueueDependents(ctx, voyageAITLSCertSecretIndex, obj.GetName(), obj.GetNamespace())
}

func (r *VoyageAIReconciler) enqueueDependents(ctx context.Context, indexKey, name, namespace string) []reconcile.Request {
	var list vaiv1.VoyageAIList
	if err := r.kubeClient.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector(indexKey, name)},
	); err != nil {
		zap.S().Errorf("failed to list VoyageAI resources for %s=%s in %s: %v", indexKey, name, namespace, err)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, vai := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace},
		})
	}
	return requests
}
