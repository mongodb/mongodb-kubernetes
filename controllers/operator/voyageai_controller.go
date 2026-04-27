package operator

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	vaiv1 "github.com/mongodb/mongodb-kubernetes/api/voyageai/v1/vai"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/pkg/deployment"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

const (
	voyageAITLSCertPath = "/etc/voyageai/tls"
	voyageAITLSCAPath   = voyageAITLSCertPath + "/ca"

	voyageAITLSCertFile = voyageAITLSCertPath + "/tls.crt"
	voyageAITLSKeyFile  = voyageAITLSCertPath + "/tls.key"
	voyageAITLSCACerts  = voyageAITLSCAPath + "/ca.crt"

	voyageAIStartupPath   = "/health/startup"
	voyageAIReadinessPath = "/health/readiness"
	voyageAILivenessPath  = "/health/liveness"
)

type OperatorVoyageAIConfig struct {
	VoyageAIRepo    string
	VoyageAIVersion string
}

type VoyageAIReconciler struct {
	kubeClient             kubernetesClient.Client
	watch                  *watch.ResourceWatcher
	operatorVoyageAIConfig OperatorVoyageAIConfig
}

func newVoyageAIReconciler(client client.Client, config OperatorVoyageAIConfig) *VoyageAIReconciler {
	return &VoyageAIReconciler{
		kubeClient:             kubernetesClient.NewClient(client),
		watch:                  watch.NewResourceWatcher(),
		operatorVoyageAIConfig: config,
	}
}

// +kubebuilder:rbac:groups=ai.mongodb.com,resources={voyageai,voyageai/status,voyageai/finalizers},verbs=*,namespace=placeholder
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

	// Watch TLS secrets if configured
	if vai.IsTLSConfigured() {
		tlsCfg := vai.Spec.Security.TLS
		r.watch.AddWatchedResourceIfNotAdded(tlsCfg.CertificateKeySecretRef.Name, vai.Namespace, watch.Secret, vai.NamespacedName())
		if tlsCfg.CAConfigMapRef != nil {
			r.watch.AddWatchedResourceIfNotAdded(tlsCfg.CAConfigMapRef.Name, vai.Namespace, watch.ConfigMap, vai.NamespacedName())
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

	version := r.voyageAIVersion(vai)
	versionOption := vaiv1.NewVoyageAIVersionOption(version)

	if deploymentStatus := deployment.GetDeploymentStatus(ctx, vai.Namespace, vai.Name, dep.GetGeneration(), r.kubeClient); !deploymentStatus.IsOK() {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, deploymentStatus, log, versionOption)
	}

	return commoncontroller.UpdateStatus(ctx, r.kubeClient, vai, workflow.OK(), log, versionOption)
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

	containerMods := []container.Modification{
		container.WithName("voyageai"),
		container.WithImage(image),
		container.WithPorts([]corev1.ContainerPort{
			{Name: "http", ContainerPort: vai.Spec.Server.Port, Protocol: corev1.ProtocolTCP},
		}),
		container.WithEnvs(buildEnvVars(&vai.Spec, tlsEnabled)...),
		container.WithResourceRequirements(buildResourceRequirements(vai.Spec.ResourceRequirements)),
		container.WithStartupProbe(probes.Apply(
			probes.WithHandler(corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   voyageAIStartupPath,
					Port:   intstr.FromInt32(vai.Spec.Server.Port),
					Scheme: probeScheme,
				},
			}),
		)),
		container.WithReadinessProbe(probes.Apply(
			probes.WithHandler(corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   voyageAIReadinessPath,
					Port:   intstr.FromInt32(vai.Spec.Server.Port),
					Scheme: probeScheme,
				},
			}),
		)),
		container.WithLivenessProbe(probes.Apply(
			probes.WithHandler(corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   voyageAILivenessPath,
					Port:   intstr.FromInt32(vai.Spec.Server.Port),
					Scheme: probeScheme,
				},
			}),
		)),
	}

	podTemplateMods := []podtemplatespec.Modification{
		podtemplatespec.WithPodLabels(podLabels),
		podtemplatespec.WithTolerations([]corev1.Toleration{
			{
				Key:      "nvidia.com/gpu",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		}),
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

		if tlsCfg.CAConfigMapRef != nil {
			tlsCAVolume := statefulset.CreateVolumeFromConfigMap("tls-ca", tlsCfg.CAConfigMapRef.Name)
			tlsCAVolumeMount := statefulset.CreateVolumeMount("tls-ca", voyageAITLSCAPath, statefulset.WithReadOnly(true))

			podTemplateMods = append(podTemplateMods, podtemplatespec.WithVolume(tlsCAVolume))
			containerMods = append(containerMods,
				container.Apply(
					container.WithEnvs(
						corev1.EnvVar{Name: "SERVER__TLS__ENABLED", Value: strconv.FormatBool(true)},
						corev1.EnvVar{Name: "SERVER__TLS__CA_CERTS", Value: voyageAITLSCACerts},
					),
					container.WithVolumeMounts([]corev1.VolumeMount{tlsCAVolumeMount}),
				))
		}
	}

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
		deployment.WithPodSpecTemplate(podtemplatespec.Apply(podTemplateMods...)),
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vai.Name,
			Namespace: vai.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, dep, func() error {
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
		// Internal hardcoded paths
		{Name: "SERVER__INFO_PATH", Value: "/info"},
		{Name: "SERVER__STARTUP_PATH", Value: voyageAIStartupPath},
		{Name: "SERVER__READINESS_PATH", Value: voyageAIReadinessPath},
		{Name: "SERVER__LIVENESS_PATH", Value: voyageAILivenessPath},
		{Name: "SERVER__OPENAPI_PATH", Value: "/openapi.json"},
		{Name: "SERVER__EMBEDDINGS_PATH", Value: "/embeddings"},
		{Name: "SERVER__CONTEXTUALIZED_EMBEDDINGS_PATH", Value: "/contextualizedembeddings"},
		{Name: "SERVER__MULTIMODAL_EMBEDDINGS_PATH", Value: "/multimodalembeddings"},
		{Name: "SERVER__RERANK_PATH", Value: "/rerank"},
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
	desired := service.Builder().
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
		}).
		Build()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desired.Name,
			Namespace: desired.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, svc, func() error {
		resourceVersion := svc.ResourceVersion
		existingClusterIP := svc.Spec.ClusterIP
		*svc = desired
		svc.ResourceVersion = resourceVersion
		// Preserve the assigned ClusterIP for non-headless Services.
		// Kubernetes assigns a ClusterIP on creation and it is immutable.
		// For headless Services (desired.Spec.ClusterIP == "None"), this is a no-op.
		if desired.Spec.ClusterIP == "" && existingClusterIP != "" {
			svc.Spec.ClusterIP = existingClusterIP
		}
		return controllerutil.SetOwnerReference(vai, svc, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("failed to ensure VoyageAI Service: %w", err)
	}

	log.Info("VoyageAI Service created/updated")
	return nil
}

func (r *VoyageAIReconciler) voyageAIVersion(vai *vaiv1.VoyageAI) string {
	if vai.Spec.Version != "" {
		return vai.Spec.Version
	}
	return r.operatorVoyageAIConfig.VoyageAIVersion
}

func (r *VoyageAIReconciler) voyageAIContainerImage(vai *vaiv1.VoyageAI) string {
	return fmt.Sprintf("%s/voyageai/%s:%s", r.operatorVoyageAIConfig.VoyageAIRepo, vai.Spec.Model, r.voyageAIVersion(vai))
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

func int32ToString(v int32) string {
	return strconv.FormatInt(int64(v), 10)
}

func int32ToFloatString(v int32) string {
	return fmt.Sprintf("%s.0", int32ToString(v))
}

func msToSecondsFloat(ms int32) string {
	return fmt.Sprintf("%.2f", float64(ms)/1000.0)
}

func AddVoyageAIController(ctx context.Context, mgr manager.Manager, config OperatorVoyageAIConfig) error {
	r := newVoyageAIReconciler(mgr.GetClient(), config)

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		For(&vaiv1.VoyageAI{}).
		Watches(&corev1.Secret{}, &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch}).
		Watches(&corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
