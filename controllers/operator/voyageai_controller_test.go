package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	vaiv1 "github.com/mongodb/mongodb-kubernetes/api/voyageai/v1/vai"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// --- Test helpers ---

func newVoyageAI(name, namespace string, model vaiv1.VoyageAIModel, version string) *vaiv1.VoyageAI {
	return &vaiv1.VoyageAI{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: vaiv1.VoyageAISpec{
			Model:   model,
			Version: version,
		},
	}
}

func newVoyageAIReconcilerWithConfig(
	config OperatorVoyageAIConfig,
	objects ...client.Object,
) (*VoyageAIReconciler, client.Client) {
	builder := mock.NewEmptyFakeClientBuilder()
	if len(objects) > 0 {
		builder.WithObjects(objects...)
	}
	fakeClient := builder.Build()
	return newVoyageAIReconciler(fakeClient, config), fakeClient
}

func defaultVoyageAIConfig() OperatorVoyageAIConfig {
	return OperatorVoyageAIConfig{
		VoyageAIRepo:    "testrepo",
		VoyageAIVersion: "1.0.0",
	}
}

func markDeploymentReady(ctx context.Context, t *testing.T, c client.Client, name, namespace string) {
	t.Helper()
	dep := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dep))

	wantedReplicas := int32(1)
	if dep.Spec.Replicas != nil {
		wantedReplicas = *dep.Spec.Replicas
	}

	dep.Status.ReadyReplicas = wantedReplicas
	dep.Status.UpdatedReplicas = wantedReplicas
	dep.Status.Replicas = wantedReplicas
	dep.Status.ObservedGeneration = dep.Generation
	require.NoError(t, c.Status().Update(ctx, dep))
}

func reconcileVoyageAI(ctx context.Context, t *testing.T, reconciler *VoyageAIReconciler, name, namespace string) (reconcile.Result, error) {
	t.Helper()
	return reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
}

func reconcileVoyageAISuccessful(
	ctx context.Context,
	t *testing.T,
	reconciler *VoyageAIReconciler,
	c client.Client,
	vai *vaiv1.VoyageAI,
) {
	t.Helper()
	namespacedName := types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace}

	res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
	require.NoError(t, err)

	updated := &vaiv1.VoyageAI{}
	require.NoError(t, c.Get(ctx, namespacedName, updated))

	if updated.Status.Phase == status.PhasePending {
		markDeploymentReady(ctx, t, c, vai.Name, vai.Namespace)
		res, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		require.NoError(t, err)
		require.NoError(t, c.Get(ctx, namespacedName, updated))
	}

	require.Equal(t, util.TWENTY_FOUR_HOURS, res.RequeueAfter)
	require.Equal(t, status.PhaseRunning, updated.Status.Phase)
}

func getVoyageAIDeployment(ctx context.Context, t *testing.T, c client.Client, vai *vaiv1.VoyageAI) *appsv1.Deployment {
	t.Helper()
	dep := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace}, dep))
	return dep
}

func getVoyageAIService(ctx context.Context, t *testing.T, c client.Client, vai *vaiv1.VoyageAI) *corev1.Service {
	t.Helper()
	svc := &corev1.Service{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: vai.Name + "-svc", Namespace: vai.Namespace}, svc))
	return svc
}

func getVoyageAIContainer(dep *appsv1.Deployment) corev1.Container {
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == "voyageai" {
			return c
		}
	}
	return corev1.Container{}
}

// --- Reconcile lifecycle tests ---

func TestVoyageAIReconcile_NotFound(t *testing.T) {
	ctx := context.Background()
	reconciler, _ := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig())

	res, err := reconcileVoyageAI(ctx, t, reconciler, "missing", mock.TestNamespace)

	assert.Error(t, err)
	assert.True(t, apiErrors.IsNotFound(err))
	assert.Equal(t, reconcile.Result{}, res)
}

func TestVoyageAIReconcile_ValidationFailed_EmptyModel(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, "", "1.0.0")
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	assert.NoError(t, err)

	updated := &vaiv1.VoyageAI{}
	assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace}, updated))
	assert.Equal(t, status.PhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "spec.model must be set")
}

func TestVoyageAIReconcile_Success(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "2.0.0")
	config := OperatorVoyageAIConfig{VoyageAIRepo: "testrepo", VoyageAIVersion: "1.0.0"}
	reconciler, c := newVoyageAIReconcilerWithConfig(config, vai)

	reconcileVoyageAISuccessful(ctx, t, reconciler, c, vai)

	// Verify status version is set from spec
	updated := &vaiv1.VoyageAI{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace}, updated))
	assert.Equal(t, "2.0.0", updated.Status.Version)

	// Verify Deployment exists
	dep := getVoyageAIDeployment(ctx, t, c, vai)
	assert.Equal(t, vai.Name, dep.Name)
	assert.Equal(t, vai.Namespace, dep.Namespace)

	// Verify Service exists
	svc := getVoyageAIService(ctx, t, c, vai)
	assert.Equal(t, vai.Name+"-svc", svc.Name)
}

func TestVoyageAIReconcile_Pending(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Replicas = 1
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	// Reconcile without marking deployment ready
	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	assert.NoError(t, err)

	updated := &vaiv1.VoyageAI{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace}, updated))
	assert.Equal(t, status.PhasePending, updated.Status.Phase)
}

// --- Deployment spec tests ---

func TestVoyageAI_DeploymentSpec(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "2.0.0")
	vai.Spec.Replicas = 3
	config := OperatorVoyageAIConfig{VoyageAIRepo: "myrepo", VoyageAIVersion: "1.0.0"}
	reconciler, c := newVoyageAIReconcilerWithConfig(config, vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)

	// Replicas
	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(3), *dep.Spec.Replicas)

	// Container image
	container := getVoyageAIContainer(dep)
	assert.Equal(t, "myrepo/voyageai/voyage-4:2.0.0", container.Image)

	// Container port (default 8080 from zero-value ServerConfig; kubebuilder defaults don't apply in unit tests)
	// With zero-value ServerConfig, Port=0 so the container port will be 0.
	// Let's test with an explicit port below.

	// Labels
	assert.Equal(t, "voyageai", dep.Labels["app.kubernetes.io/name"])
	assert.Equal(t, "vai", dep.Labels["app.kubernetes.io/instance"])
	assert.Equal(t, "mongodb-kubernetes-operator", dep.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "voyage-4", dep.Labels["app.kubernetes.io/component"])

	// Pod labels
	podLabels := dep.Spec.Template.Labels
	assert.Equal(t, "voyageai", podLabels["app.kubernetes.io/name"])
	assert.Equal(t, "vai", podLabels["app.kubernetes.io/instance"])

	// GPU resources
	gpuReq := container.Resources.Requests["nvidia.com/gpu"]
	gpuLim := container.Resources.Limits["nvidia.com/gpu"]
	assert.Equal(t, resource.MustParse("1"), gpuReq)
	assert.Equal(t, resource.MustParse("1"), gpuLim)

	// GPU toleration
	tolerations := dep.Spec.Template.Spec.Tolerations
	require.Len(t, tolerations, 1)
	assert.Equal(t, "nvidia.com/gpu", tolerations[0].Key)
	assert.Equal(t, corev1.TolerationOpExists, tolerations[0].Operator)
	assert.Equal(t, corev1.TaintEffectNoSchedule, tolerations[0].Effect)

	// Owner reference
	require.Len(t, dep.OwnerReferences, 1)
	assert.Equal(t, "VoyageAI", dep.OwnerReferences[0].Kind)
	assert.Equal(t, vai.Name, dep.OwnerReferences[0].Name)

	// Match labels selector
	require.NotNil(t, dep.Spec.Selector)
	assert.Equal(t, "voyageai", dep.Spec.Selector.MatchLabels["app.kubernetes.io/name"])
	assert.Equal(t, "vai", dep.Spec.Selector.MatchLabels["app.kubernetes.io/instance"])
}

func TestVoyageAI_DeploymentSpec_ExplicitPort(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Server.Port = 9090
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)
	container := getVoyageAIContainer(dep)

	require.Len(t, container.Ports, 1)
	assert.Equal(t, int32(9090), container.Ports[0].ContainerPort)
	assert.Equal(t, corev1.ProtocolTCP, container.Ports[0].Protocol)
}

func TestVoyageAI_HealthProbes(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Server.Port = 8080
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)
	container := getVoyageAIContainer(dep)

	// Startup probe
	require.NotNil(t, container.StartupProbe)
	require.NotNil(t, container.StartupProbe.HTTPGet)
	assert.Equal(t, voyageAIStartupPath, container.StartupProbe.HTTPGet.Path)
	assert.Equal(t, int32(8080), container.StartupProbe.HTTPGet.Port.IntVal)
	assert.Equal(t, corev1.URISchemeHTTP, container.StartupProbe.HTTPGet.Scheme)

	// Readiness probe
	require.NotNil(t, container.ReadinessProbe)
	require.NotNil(t, container.ReadinessProbe.HTTPGet)
	assert.Equal(t, voyageAIReadinessPath, container.ReadinessProbe.HTTPGet.Path)
	assert.Equal(t, int32(8080), container.ReadinessProbe.HTTPGet.Port.IntVal)
	assert.Equal(t, corev1.URISchemeHTTP, container.ReadinessProbe.HTTPGet.Scheme)

	// Liveness probe
	require.NotNil(t, container.LivenessProbe)
	require.NotNil(t, container.LivenessProbe.HTTPGet)
	assert.Equal(t, voyageAILivenessPath, container.LivenessProbe.HTTPGet.Path)
	assert.Equal(t, int32(8080), container.LivenessProbe.HTTPGet.Port.IntVal)
	assert.Equal(t, corev1.URISchemeHTTP, container.LivenessProbe.HTTPGet.Scheme)
}

func TestVoyageAI_HealthProbes_TLS(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Server.Port = 8443
	vai.Spec.Security.TLS = &vaiv1.TLS{
		CertificateKeySecretRef: corev1.LocalObjectReference{Name: "tls-secret"},
	}
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)
	container := getVoyageAIContainer(dep)

	assert.Equal(t, corev1.URISchemeHTTPS, container.StartupProbe.HTTPGet.Scheme)
	assert.Equal(t, corev1.URISchemeHTTPS, container.ReadinessProbe.HTTPGet.Scheme)
	assert.Equal(t, corev1.URISchemeHTTPS, container.LivenessProbe.HTTPGet.Scheme)
}

// --- Environment variable tests ---

func TestBuildEnvVars_Defaults(t *testing.T) {
	// Simulate what the API server delivers after applying +kubebuilder:default values.
	spec := &vaiv1.VoyageAISpec{
		Replicas: 1,
		Server: vaiv1.ServerConfig{
			Port:              8080,
			Workers:           1,
			Timeout:           120,
			MaxRequests:       1000,
			MaxRequestsJitter: 50,
		},
	}
	envs := buildEnvVars(spec, false)

	// Internal hardcoded paths
	envMap := make(map[string]string)
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	assert.Equal(t, "/info", envMap["SERVER__INFO_PATH"])
	assert.Equal(t, "/health/startup", envMap["SERVER__STARTUP_PATH"])
	assert.Equal(t, "/health/readiness", envMap["SERVER__READINESS_PATH"])
	assert.Equal(t, "/health/liveness", envMap["SERVER__LIVENESS_PATH"])
	assert.Equal(t, "/openapi.json", envMap["SERVER__OPENAPI_PATH"])
	assert.Equal(t, "/embeddings", envMap["SERVER__EMBEDDINGS_PATH"])
	assert.Equal(t, "/contextualizedembeddings", envMap["SERVER__CONTEXTUALIZED_EMBEDDINGS_PATH"])
	assert.Equal(t, "/multimodalembeddings", envMap["SERVER__MULTIMODAL_EMBEDDINGS_PATH"])
	assert.Equal(t, "/rerank", envMap["SERVER__RERANK_PATH"])

	// TLS disabled
	assert.Equal(t, "false", envMap["SERVER__TLS__ENABLED"])
	_, hasCert := envMap["SERVER__TLS__CERTFILE"]
	assert.False(t, hasCert)
	_, hasKey := envMap["SERVER__TLS__KEYFILE"]
	assert.False(t, hasKey)
	_, hasCA := envMap["SERVER__TLS__CA_CERTS"]
	assert.False(t, hasCA)

	// Fields with kubebuilder defaults are populated by the API server and always present
	assert.Equal(t, "120", envMap["SERVER__TIMEOUT"])
	assert.Equal(t, "1000", envMap["SERVER__MAX_REQUESTS"])
	assert.Equal(t, "50", envMap["SERVER__MAX_REQUESTS_JITTER"])
}

func TestBuildEnvVars_ServerConfig(t *testing.T) {
	spec := &vaiv1.VoyageAISpec{
		Server: vaiv1.ServerConfig{
			Port:              9090,
			Workers:           4,
			Timeout:           60,
			MaxRequests:       100,
			MaxRequestsJitter: 10,
		},
	}
	envs := buildEnvVars(spec, false)
	envMap := make(map[string]string)
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	assert.Equal(t, "9090", envMap["SERVER__PORT"])
	assert.Equal(t, "4", envMap["SERVER__WORKERS"])
	assert.Equal(t, "60", envMap["SERVER__TIMEOUT"])
	assert.Equal(t, "100", envMap["SERVER__MAX_REQUESTS"])
	assert.Equal(t, "10", envMap["SERVER__MAX_REQUESTS_JITTER"])
}

func TestBuildEnvVars_WithDataParallel(t *testing.T) {
	numWorkers := int32(4)
	enableActive := true
	spec := &vaiv1.VoyageAISpec{
		DataParallel: vaiv1.DataParallelConfig{
			Enabled:                       true,
			NumWorkers:                    &numWorkers,
			LoadBalancingStrategy:         "round_robin",
			WorkerInitTimeoutSeconds:      600,
			WorkerExecutionTimeoutSeconds: 30,
			WorkerQueueMaxSize:            100,
			Batching: &vaiv1.BatchingConfig{
				Strategy:      "time_window",
				MaxWaitTimeMs: 10,
				MaxQueueSize:  2000,
			},
			HealthMonitoring: &vaiv1.HealthMonitoringConfig{
				CheckIntervalSeconds:       5,
				MaxConsecutiveTimeouts:     3,
				EnableActiveChecks:         &enableActive,
				ActiveCheckIntervalSeconds: 60,
				ActiveCheckTimeoutSeconds:  5,
				MaxRestartAttempts:         3,
				RestartCooldownSeconds:     30,
			},
		},
	}
	envs := buildEnvVars(spec, false)
	envMap := make(map[string]string)
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	// DataParallel core
	assert.Equal(t, "true", envMap["DATA_PARALLEL__ENABLED"])
	assert.Equal(t, "/tmp", envMap["DATA_PARALLEL__SOCKET_PATH_PREFIX"])
	assert.Equal(t, "4", envMap["DATA_PARALLEL__NUM_WORKERS"])
	assert.Equal(t, "round_robin", envMap["DATA_PARALLEL__LOAD_BALANCING_STRATEGY"])
	assert.Equal(t, "600.0", envMap["DATA_PARALLEL__WORKER_INIT_TIMEOUT"])
	assert.Equal(t, "30.0", envMap["DATA_PARALLEL__WORKER_EXECUTION_TIMEOUT"])
	assert.Equal(t, "100", envMap["DATA_PARALLEL__WORKER_QUEUE_MAXSIZE"])

	// Batching
	assert.Equal(t, "time_window", envMap["DATA_PARALLEL__BATCHING__STRATEGY"])
	assert.Equal(t, "0.01", envMap["DATA_PARALLEL__BATCHING__MAX_WAIT_TIME"])
	assert.Equal(t, "2000", envMap["DATA_PARALLEL__BATCHING__MAX_QUEUE_SIZE"])

	// Health monitoring
	assert.Equal(t, "5.0", envMap["DATA_PARALLEL__HEALTH_MONITORING__CHECK_INTERVAL"])
	assert.Equal(t, "3", envMap["DATA_PARALLEL__HEALTH_MONITORING__MAX_CONSECUTIVE_TIMEOUTS"])
	assert.Equal(t, "true", envMap["DATA_PARALLEL__HEALTH_MONITORING__ENABLE_ACTIVE_CHECKS"])
	assert.Equal(t, "60", envMap["DATA_PARALLEL__HEALTH_MONITORING__ACTIVE_CHECK_INTERVAL"])
	assert.Equal(t, "5", envMap["DATA_PARALLEL__HEALTH_MONITORING__ACTIVE_CHECK_TIMEOUT"])
	assert.Equal(t, "3", envMap["DATA_PARALLEL__HEALTH_MONITORING__MAX_RESTART_ATTEMPTS"])
	assert.Equal(t, "30.0", envMap["DATA_PARALLEL__HEALTH_MONITORING__RESTART_COOLDOWN"])
}

func TestBuildEnvVars_HealthMonitoring_EnableActiveChecks_Nil(t *testing.T) {
	spec := &vaiv1.VoyageAISpec{
		DataParallel: vaiv1.DataParallelConfig{
			HealthMonitoring: &vaiv1.HealthMonitoringConfig{
				EnableActiveChecks: nil,
			},
		},
	}
	envs := buildEnvVars(spec, false)
	envMap := make(map[string]string)
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	_, found := envMap["DATA_PARALLEL__HEALTH_MONITORING__ENABLE_ACTIVE_CHECKS"]
	assert.False(t, found, "ENABLE_ACTIVE_CHECKS should be omitted when EnableActiveChecks is nil")
}

// --- Resource requirements tests ---

func TestBuildResourceRequirements_Default(t *testing.T) {
	result := buildResourceRequirements(nil)

	gpuReq := result.Requests["nvidia.com/gpu"]
	gpuLim := result.Limits["nvidia.com/gpu"]
	assert.Equal(t, resource.MustParse("1"), gpuReq)
	assert.Equal(t, resource.MustParse("1"), gpuLim)
}

func TestBuildResourceRequirements_WithUserRequirements(t *testing.T) {
	userReqs := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}
	result := buildResourceRequirements(userReqs)

	// User resources merged
	assert.Equal(t, resource.MustParse("500m"), result.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("1Gi"), result.Requests[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("1"), result.Limits[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("2Gi"), result.Limits[corev1.ResourceMemory])

	// GPU floor still present
	assert.Equal(t, resource.MustParse("1"), result.Requests["nvidia.com/gpu"])
	assert.Equal(t, resource.MustParse("1"), result.Limits["nvidia.com/gpu"])
}

func TestBuildResourceRequirements_UserOverridesGPU(t *testing.T) {
	userReqs := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			"nvidia.com/gpu": resource.MustParse("2"),
		},
		Limits: corev1.ResourceList{
			"nvidia.com/gpu": resource.MustParse("2"),
		},
	}
	result := buildResourceRequirements(userReqs)

	assert.Equal(t, resource.MustParse("2"), result.Requests["nvidia.com/gpu"])
	assert.Equal(t, resource.MustParse("2"), result.Limits["nvidia.com/gpu"])
}

// --- TLS volume mount tests ---

func TestVoyageAI_TLS(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Security.TLS = &vaiv1.TLS{
		CertificateKeySecretRef: corev1.LocalObjectReference{Name: "tls-secret"},
		CAConfigMapRef:          &corev1.LocalObjectReference{Name: "ca-configmap"},
	}
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)
	container := getVoyageAIContainer(dep)

	// Verify volumes exist
	volumes := dep.Spec.Template.Spec.Volumes
	volumeNames := make(map[string]bool)
	for _, v := range volumes {
		volumeNames[v.Name] = true
	}
	assert.True(t, volumeNames["tls-cert"], "tls-cert volume should exist")
	assert.True(t, volumeNames["tls-ca"], "tls-ca volume should exist")

	// Verify volume sources
	for _, v := range volumes {
		if v.Name == "tls-cert" {
			require.NotNil(t, v.Secret)
			assert.Equal(t, "tls-secret", v.Secret.SecretName)
		}
		if v.Name == "tls-ca" {
			require.NotNil(t, v.ConfigMap)
			assert.Equal(t, "ca-configmap", v.ConfigMap.Name)
		}
	}

	// Verify volume mounts
	mountPaths := make(map[string]corev1.VolumeMount)
	for _, vm := range container.VolumeMounts {
		mountPaths[vm.Name] = vm
	}

	certMount, ok := mountPaths["tls-cert"]
	require.True(t, ok, "tls-cert volume mount should exist")
	assert.Equal(t, "/etc/voyageai/tls", certMount.MountPath)
	assert.True(t, certMount.ReadOnly)

	caMount, ok := mountPaths["tls-ca"]
	require.True(t, ok, "tls-ca volume mount should exist")
	assert.Equal(t, "/etc/voyageai/tls/ca", caMount.MountPath)
	assert.True(t, caMount.ReadOnly)

	// Verify TLS environment variables
	envMap := make(map[string]string)
	for _, e := range container.Env {
		envMap[e.Name] = e.Value
	}
	assert.Equal(t, "true", envMap["SERVER__TLS__ENABLED"])
	assert.Equal(t, voyageAITLSCertFile, envMap["SERVER__TLS__CERTFILE"])
	assert.Equal(t, voyageAITLSKeyFile, envMap["SERVER__TLS__KEYFILE"])
	assert.Equal(t, voyageAITLSCACerts, envMap["SERVER__TLS__CA_CERTS"])
}

func TestVoyageAI_TLS_NoCA(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Security.TLS = &vaiv1.TLS{
		CertificateKeySecretRef: corev1.LocalObjectReference{Name: "tls-secret"},
	}
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)

	volumeNames := make(map[string]bool)
	for _, v := range dep.Spec.Template.Spec.Volumes {
		volumeNames[v.Name] = true
	}
	assert.True(t, volumeNames["tls-cert"], "tls-cert volume should exist")
	assert.False(t, volumeNames["tls-ca"], "tls-ca volume should not exist when CAConfigMapRef is nil")

	// Verify TLS environment variables
	container := getVoyageAIContainer(dep)
	envMap := make(map[string]string)
	for _, e := range container.Env {
		envMap[e.Name] = e.Value
	}
	assert.Equal(t, "true", envMap["SERVER__TLS__ENABLED"])
	assert.Equal(t, voyageAITLSCertFile, envMap["SERVER__TLS__CERTFILE"])
	assert.Equal(t, voyageAITLSKeyFile, envMap["SERVER__TLS__KEYFILE"])
	_, hasCA := envMap["SERVER__TLS__CA_CERTS"]
	assert.False(t, hasCA, "SERVER__TLS__CA_CERTS should not be set when CAConfigMapRef is nil")
}

// --- Service tests ---

func TestVoyageAI_Service(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Server.Port = 8080
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	svc := getVoyageAIService(ctx, t, c, vai)

	// Service type
	assert.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)

	// Port
	require.Len(t, svc.Spec.Ports, 1)
	assert.Equal(t, int32(8080), svc.Spec.Ports[0].Port)
	assert.Equal(t, int32(8080), svc.Spec.Ports[0].TargetPort.IntVal)
	assert.Equal(t, corev1.ProtocolTCP, svc.Spec.Ports[0].Protocol)

	// Selector matches pod labels
	assert.Equal(t, "voyageai", svc.Spec.Selector["app.kubernetes.io/name"])
	assert.Equal(t, "vai", svc.Spec.Selector["app.kubernetes.io/instance"])

	// Labels
	assert.Equal(t, "voyageai", svc.Labels["app.kubernetes.io/name"])
	assert.Equal(t, "vai", svc.Labels["app.kubernetes.io/instance"])
	assert.Equal(t, "mongodb-kubernetes-operator", svc.Labels["app.kubernetes.io/managed-by"])

	// Owner reference
	require.Len(t, svc.OwnerReferences, 1)
	assert.Equal(t, "VoyageAI", svc.OwnerReferences[0].Kind)
	assert.Equal(t, vai.Name, svc.OwnerReferences[0].Name)
}

// --- Node affinity test ---

func TestVoyageAI_NodeAffinity(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.NodeAffinity = &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "gpu-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"a100", "h100"},
						},
					},
				},
			},
		},
	}
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)
	require.NotNil(t, dep.Spec.Template.Spec.Affinity)
	require.NotNil(t, dep.Spec.Template.Spec.Affinity.NodeAffinity)
	require.NotNil(t, dep.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution)

	terms := dep.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, 1)
	require.Len(t, terms[0].MatchExpressions, 1)
	assert.Equal(t, "gpu-type", terms[0].MatchExpressions[0].Key)
	assert.Equal(t, []string{"a100", "h100"}, terms[0].MatchExpressions[0].Values)
}

func TestVoyageAI_NoNodeAffinity(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	dep := getVoyageAIDeployment(ctx, t, c, vai)
	assert.Nil(t, dep.Spec.Template.Spec.Affinity, "Affinity should be nil when NodeAffinity is not set")
}

// --- Version resolution tests ---

func TestVoyageAI_VersionFromSpec(t *testing.T) {
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelRerank25, "3.0.0")
	config := OperatorVoyageAIConfig{VoyageAIRepo: "myrepo", VoyageAIVersion: "2.0.0"}
	r := &VoyageAIReconciler{operatorVoyageAIConfig: config}

	assert.Equal(t, "3.0.0", r.voyageAIVersion(vai))
	assert.Equal(t, "myrepo/voyageai/rerank-2.5:3.0.0", r.voyageAIContainerImage(vai))
}

func TestVoyageAI_VersionFallback(t *testing.T) {
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4Lite, "")
	config := OperatorVoyageAIConfig{VoyageAIRepo: "myrepo", VoyageAIVersion: "2.0.0"}
	r := &VoyageAIReconciler{operatorVoyageAIConfig: config}

	assert.Equal(t, "2.0.0", r.voyageAIVersion(vai))
	assert.Equal(t, "myrepo/voyageai/voyage-4-lite:2.0.0", r.voyageAIContainerImage(vai))
}

// --- Model variants test ---

func TestVoyageAI_AllModelVariants(t *testing.T) {
	tests := []struct {
		model         vaiv1.VoyageAIModel
		expectedImage string
	}{
		{vaiv1.VoyageAIModelVoyage4, "testrepo/voyageai/voyage-4:1.0.0"},
		{vaiv1.VoyageAIModelVoyage4Lite, "testrepo/voyageai/voyage-4-lite:1.0.0"},
		{vaiv1.VoyageAIModelRerank25, "testrepo/voyageai/rerank-2.5:1.0.0"},
		{vaiv1.VoyageAIModelRerank25Lite, "testrepo/voyageai/rerank-2.5-lite:1.0.0"},
	}

	for _, tc := range tests {
		t.Run(string(tc.model), func(t *testing.T) {
			ctx := context.Background()
			vai := newVoyageAI("vai", mock.TestNamespace, tc.model, "1.0.0")
			reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

			_, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
			require.NoError(t, err)

			dep := getVoyageAIDeployment(ctx, t, c, vai)
			container := getVoyageAIContainer(dep)
			assert.Equal(t, tc.expectedImage, container.Image)

			// Component label should match model
			assert.Equal(t, string(tc.model), dep.Labels["app.kubernetes.io/component"])
		})
	}
}

// --- Labels helper tests ---

func TestVoyageAILabels(t *testing.T) {
	vai := newVoyageAI("my-vai", mock.TestNamespace, vaiv1.VoyageAIModelRerank25Lite, "1.0.0")
	labels := voyageAILabels(vai)

	assert.Equal(t, map[string]string{
		"app.kubernetes.io/name":       "voyageai",
		"app.kubernetes.io/instance":   "my-vai",
		"app.kubernetes.io/managed-by": "mongodb-kubernetes-operator",
		"app.kubernetes.io/component":  "rerank-2.5-lite",
	}, labels)
}

func TestVoyageAIPodLabels(t *testing.T) {
	vai := newVoyageAI("my-vai", mock.TestNamespace, vaiv1.VoyageAIModelRerank25Lite, "1.0.0")
	labels := voyageAIPodLabels(vai)

	assert.Equal(t, map[string]string{
		"app.kubernetes.io/name":     "voyageai",
		"app.kubernetes.io/instance": "my-vai",
	}, labels)
}

// --- Conversion helper tests ---

func TestInt32ToString(t *testing.T) {
	assert.Equal(t, "0", int32ToString(0))
	assert.Equal(t, "8080", int32ToString(8080))
	assert.Equal(t, "-1", int32ToString(-1))
}

func TestInt32ToFloatString(t *testing.T) {
	assert.Equal(t, "0.0", int32ToFloatString(0))
	assert.Equal(t, "600.0", int32ToFloatString(600))
	assert.Equal(t, "30.0", int32ToFloatString(30))
}

func TestMsToSecondsFloat(t *testing.T) {
	assert.Equal(t, "0.01", msToSecondsFloat(10))
	assert.Equal(t, "1.00", msToSecondsFloat(1000))
	assert.Equal(t, "0.50", msToSecondsFloat(500))
	assert.Equal(t, "0.00", msToSecondsFloat(0))
}

// --- Status version option test ---

func TestVoyageAIReconcile_StatusVersionFromOperatorConfig(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "2.5.0")
	config := OperatorVoyageAIConfig{VoyageAIRepo: "testrepo", VoyageAIVersion: "1.0.0"}
	reconciler, c := newVoyageAIReconcilerWithConfig(config, vai)

	reconcileVoyageAISuccessful(ctx, t, reconciler, c, vai)

	updated := &vaiv1.VoyageAI{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace}, updated))
	// Version from spec takes precedence
	assert.Equal(t, "2.5.0", updated.Status.Version)
}

// --- Idempotent reconciliation test ---

func TestVoyageAIReconcile_Idempotent(t *testing.T) {
	ctx := context.Background()
	vai := newVoyageAI("vai", mock.TestNamespace, vaiv1.VoyageAIModelVoyage4, "1.0.0")
	vai.Spec.Server.Port = 8080
	reconciler, c := newVoyageAIReconcilerWithConfig(defaultVoyageAIConfig(), vai)

	// First reconcile
	reconcileVoyageAISuccessful(ctx, t, reconciler, c, vai)

	// Second reconcile should also succeed without error
	res, err := reconcileVoyageAI(ctx, t, reconciler, vai.Name, vai.Namespace)
	require.NoError(t, err)

	updated := &vaiv1.VoyageAI{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: vai.Name, Namespace: vai.Namespace}, updated))
	assert.Equal(t, status.PhaseRunning, updated.Status.Phase)
	assert.Equal(t, util.TWENTY_FOUR_HOURS, res.RequeueAfter)
}
