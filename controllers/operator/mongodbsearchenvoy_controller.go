package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/envvar"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// Some of these variables can be exposed as configuration to the user
const (
	envoyReplicas = int32(1)

	envoyAdminPort = 9901

	envoyServerCertPath = "/etc/envoy/tls/server"
	envoyClientCertPath = "/etc/envoy/tls/client"
	envoyCACertPath     = "/etc/envoy/tls/ca"
	envoyConfigPath     = "/etc/envoy"

	// CA key in the MongoDB CA ConfigMap
	envoyCAKey = "ca-pem"

	envoyConfigHashAnnotation = "mongodb.com/envoy-config-hash"

	labelName = "search-proxy"
)

// envoyRoute defines routing information for one Envoy entrypoint (one per shard, or one for RS).
type envoyRoute struct {
	Name             string // identifier: shard name (e.g., "mdb-sh-0") or "rs" for ReplicaSets
	NameSafe         string // identifier safe for Envoy (hyphens replaced with underscores)
	SNIHostname      string // FQDN of the proxy service for SNI matching
	UpstreamHost     string // FQDN of the mongot headless service
	UpstreamPort     int32  // typically 27028
	ProxyServiceName string // name of the ClusterIP proxy Service for this route
}

type MongoDBSearchEnvoyReconciler struct {
	kubeClient        kubernetesClient.Client
	watch             *watch.ResourceWatcher
	defaultEnvoyImage string
}

func newMongoDBSearchEnvoyReconciler(client client.Client, defaultEnvoyImage string) *MongoDBSearchEnvoyReconciler {
	return &MongoDBSearchEnvoyReconciler{
		kubeClient:        kubernetesClient.NewClient(client),
		watch:             watch.NewResourceWatcher(),
		defaultEnvoyImage: defaultEnvoyImage,
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status,mongodbsearch/finalizers},verbs=*,namespace=placeholder
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
func (r *MongoDBSearchEnvoyReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("MongoDBSearchEnvoy", request.NamespacedName)
	log.Info("-> MongoDBSearchEnvoy.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, mdbSearch, log); err != nil {
		return result, err
	}

	// Only act when lb.mode == Managed
	if !mdbSearch.IsLBModeManaged() {
		if mdbSearch.Status.LoadBalancer != nil {
			r.clearLBStatus(ctx, mdbSearch, log)
		}
		return reconcile.Result{}, nil
	}

	// Fail fast if the envoy image is not configured, this is a terminal config error and should not be re-enqueued
	if _, err := r.envoyContainerImage(); err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Invalid("%s", err), log)
	}

	// Resolve the source database (shared with the main search controller).
	searchSource, err := getSearchSource(ctx, r.kubeClient, r.watch, mdbSearch, log)
	if err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("Waiting for search source: %s", err), log)
	}

	tlsCfg := searchSource.TLSConfig()
	tlsEnabled := mdbSearch.IsTLSConfigured()

	routes := buildRoutes(mdbSearch, searchSource)
	if len(routes) == 0 {
		log.Warn("No routes to configure, nothing to deploy")
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("No routes to configure for load balancer"), log)
	}

	// Generate Envoy config JSON
	caKeyName := caKeyNameFromTLSConfig(tlsCfg)
	envoyJSON, err := buildEnvoyConfigJSON(routes, tlsEnabled, caKeyName)
	if err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Failed(err), log)
	}

	// Ensure ConfigMap
	if err := r.ensureConfigMap(ctx, mdbSearch, envoyJSON, log); err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Failed(err), log)
	}

	// Ensure Deployment
	if err := r.ensureDeployment(ctx, mdbSearch, envoyJSON, tlsCfg, log); err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Failed(err), log)
	}

	// Ensure proxy Services (one per route)
	currentServiceNames := make(map[string]bool, len(routes))
	for _, route := range routes {
		currentServiceNames[route.ProxyServiceName] = true
		if err := r.ensureProxyService(ctx, mdbSearch, route, log); err != nil {
			return r.updateLBStatus(ctx, mdbSearch, workflow.Failed(err), log)
		}
	}

	// Clean up stale proxy Services for removed routes
	if err := r.cleanupStaleProxyServices(ctx, mdbSearch, currentServiceNames, log); err != nil {
		log.Warnf("Failed to cleanup stale proxy services: %s", err)
	}

	log.Info("MongoDBSearchEnvoy reconciliation complete")
	return r.updateLBStatus(ctx, mdbSearch, workflow.OK(), log)
}

// updateLBStatus patches the loadBalancer sub-status on the MongoDBSearch CR
// and returns the reconcile result derived from the workflow status.
func (r *MongoDBSearchEnvoyReconciler) updateLBStatus(ctx context.Context, search *searchv1.MongoDBSearch, st workflow.Status, log *zap.SugaredLogger) (reconcile.Result, error) {
	partOption := searchv1.NewSearchPartOption(searchv1.SearchPartLoadBalancer)
	return commoncontroller.UpdateStatus(ctx, r.kubeClient, search, st, log, partOption)
}

// clearLBStatus removes the loadBalancer substatus when LB is no longer configured.
// This works because UpdateStatus uses a JSON Patch targeting only /status/loadBalancer,
// so it won't conflict with the main controller patching /status.
func (r *MongoDBSearchEnvoyReconciler) clearLBStatus(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) {
	search.Status.LoadBalancer = nil
	partOption := searchv1.NewSearchPartOption(searchv1.SearchPartLoadBalancer)
	// GetStatus with LB part will return nil, which patches null into /status/loadBalancer
	if _, err := commoncontroller.UpdateStatus(ctx, r.kubeClient, search, workflow.OK(), log, partOption); err != nil {
		log.Warnf("Failed to clear loadBalancer status: %s", err)
	}
}

// caKeyNameFromTLSConfig returns the CA key filename for Envoy config file paths.
func caKeyNameFromTLSConfig(tlsCfg *searchcontroller.TLSSourceConfig) string {
	if tlsCfg != nil {
		return tlsCfg.CAFileName
	}
	return envoyCAKey
}

// buildRoutes returns the Envoy routes for the given topology.
// It is the single topology-aware path in the controller. Everything downstream (config generation,
// Service creation, cleanup) is topology-agnostic, using the envoyRoute data structure only.
func buildRoutes(search *searchv1.MongoDBSearch, source searchcontroller.SearchSourceDBResource) []envoyRoute {
	if shardedSource, ok := source.(searchcontroller.SearchSourceShardedDeployment); ok {
		return buildShardRoutes(search, shardedSource.GetShardNames())
	}
	return []envoyRoute{buildReplicaSetRoute(search)}
}

// buildShardRoutes builds per-shard routing information from shard names.
func buildShardRoutes(search *searchv1.MongoDBSearch, shardNames []string) []envoyRoute {
	routes := make([]envoyRoute, 0, len(shardNames))
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	for _, shardName := range shardNames {
		lbServiceName := search.LoadBalancerProxyServiceNameForShard(shardName)
		sniServiceName := search.ProxyServiceNameForShard(shardName).Name
		mongotServiceName := search.MongotServiceForShard(shardName).Name

		sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", proxyServiceName, namespace)
		if endpoint := search.GetManagedLBEndpointForShard(shardName); endpoint != "" {
			sniHostname = endpoint
		}

		routes = append(routes, envoyRoute{
			Name:             shardName,
			NameSafe:         strings.ReplaceAll(shardName, "-", "_"),
			SNIHostname:      sniHostname,
			UpstreamHost:     fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace),
			UpstreamPort:     mongotPort,
			ProxyServiceName: lbServiceName,
		})
	}

	return routes
}

// buildReplicaSetRoute returns the single route for a ReplicaSet.
func buildReplicaSetRoute(search *searchv1.MongoDBSearch) envoyRoute {
	lbServiceName := search.LoadBalancerServiceName()
	sniServiceName := search.ProxyServiceNamespacedName().Name
	mongotServiceName := search.SearchServiceNamespacedName().Name
	namespace := search.Namespace

	sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", proxyServiceName, namespace)
	if endpoint := search.GetManagedLBEndpoint(); endpoint != "" {
		sniHostname = endpoint
	}

	return envoyRoute{
		Name:             "rs",
		NameSafe:         "rs",
		SNIHostname:      sniHostname,
		UpstreamHost:     fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace),
		UpstreamPort:     search.GetMongotGrpcPort(),
		ProxyServiceName: lbServiceName,
	}
}

// ensureConfigMap creates or updates the Envoy ConfigMap.
func (r *MongoDBSearchEnvoyReconciler) ensureConfigMap(ctx context.Context, search *searchv1.MongoDBSearch, envoyJSON string, log *zap.SugaredLogger) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerConfigMapName(),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, cm, func() error {
		cm.Labels = envoyLabels(search)
		cm.Data = map[string]string{"envoy.json": envoyJSON}
		return controllerutil.SetOwnerReference(search, cm, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("failed to ensure Envoy ConfigMap: %w", err)
	}

	log.Info("Envoy ConfigMap ensured")
	return nil
}

// ensureDeployment creates or updates the Envoy Deployment.
func (r *MongoDBSearchEnvoyReconciler) ensureDeployment(ctx context.Context, search *searchv1.MongoDBSearch, envoyJSON string, tlsCfg *searchcontroller.TLSSourceConfig, log *zap.SugaredLogger) error {
	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(envoyJSON)))
	replicas := envoyReplicas
	labels := envoyLabels(search)
	tlsEnabled := search.IsTLSConfigured()
	image, err := r.envoyContainerImage()
	if err != nil {
		return err
	}
	resources := envoyResourceRequirements(search)
	managedSecurityContext := envvar.ReadBool(podtemplatespec.ManagedSecurityContextEnv) // nolint:forbidigo

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerDeploymentName(),
			Namespace: search.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.kubeClient, dep, func() error {
		dep.Labels = labels

		dep.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: envoyPodLabels(search),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: envoyPodLabels(search),
					Annotations: map[string]string{
						envoyConfigHashAnnotation: configHash,
					},
				},
				Spec: buildEnvoyPodSpec(search, tlsCfg, tlsEnabled, image, resources, managedSecurityContext),
			},
		}

		// Apply user deployment configuration override
		if search.Spec.LoadBalancer != nil &&
			search.Spec.LoadBalancer.Envoy != nil &&
			search.Spec.LoadBalancer.Envoy.DeploymentConfiguration != nil {
			depCfg := search.Spec.LoadBalancer.Envoy.DeploymentConfiguration
			dep.Spec = merge.DeploymentSpecs(dep.Spec, depCfg.SpecWrapper.Spec)
			dep.Labels = merge.StringToStringMap(dep.Labels, depCfg.MetadataWrapper.Labels)
			dep.Annotations = merge.StringToStringMap(dep.Annotations, depCfg.MetadataWrapper.Annotations)
		}

		return controllerutil.SetOwnerReference(search, dep, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("failed to ensure Envoy Deployment: %w", err)
	}

	log.Info("Envoy Deployment ensured")
	return nil
}

// buildEnvoyPodSpec builds the PodSpec for the Envoy Deployment.
// tlsCfg may be nil if TLS is not configured on the source.
func buildEnvoyPodSpec(search *searchv1.MongoDBSearch, tlsCfg *searchcontroller.TLSSourceConfig, tlsEnabled bool, image string, resources corev1.ResourceRequirements, managedSecurityContext bool) corev1.PodSpec {
	volumes := []corev1.Volume{
		{
			Name: "envoy-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: search.LoadBalancerConfigMapName()},
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "envoy-config", MountPath: envoyConfigPath, ReadOnly: true},
	}

	if tlsEnabled && tlsCfg != nil {
		// Use the CA volume from TLSSourceConfig directly (already ConfigMap or Secret).
		// Add Items to project only the CA key into the mount path.
		caVolume := tlsCfg.CAVolume
		caVolume.Name = "ca-cert"
		if caVolume.Secret != nil {
			caVolume.Secret.Items = []corev1.KeyToPath{{Key: tlsCfg.CAFileName, Path: tlsCfg.CAFileName}}
		} else if caVolume.ConfigMap != nil {
			caVolume.ConfigMap.Items = []corev1.KeyToPath{{Key: tlsCfg.CAFileName, Path: tlsCfg.CAFileName}}
		}

		volumes = append(volumes,
			corev1.Volume{
				Name: "envoy-server-cert",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: search.LoadBalancerServerCert().Name},
				},
			},
			corev1.Volume{
				Name: "envoy-client-cert",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: search.LoadBalancerClientCert().Name},
				},
			},
			caVolume,
		)

		volumeMounts = append(volumeMounts,
			corev1.VolumeMount{Name: "envoy-server-cert", MountPath: envoyServerCertPath, ReadOnly: true},
			corev1.VolumeMount{Name: "envoy-client-cert", MountPath: envoyClientCertPath, ReadOnly: true},
			corev1.VolumeMount{Name: "ca-cert", MountPath: envoyCACertPath, ReadOnly: true},
		)
	}

	var podSecurityContext *corev1.PodSecurityContext
	var containerSecurityContext *corev1.SecurityContext
	if !managedSecurityContext {
		psc := podtemplatespec.DefaultPodSecurityContext()
		podSecurityContext = &psc
		csc := container.DefaultSecurityContext()
		containerSecurityContext = &csc
	}

	return corev1.PodSpec{
		SecurityContext: podSecurityContext,
		Containers: []corev1.Container{
			{
				Name:    "envoy",
				Image:   image,
				Command: []string{"/usr/local/bin/envoy"},
				Args:    []string{"-c", "/etc/envoy/envoy.json", "--log-level", "info"},
				Ports: []corev1.ContainerPort{
					{Name: "grpc", ContainerPort: searchv1.EnvoyDefaultProxyPort},
					{Name: "admin", ContainerPort: envoyAdminPort},
				},
				Resources:       resources,
				SecurityContext: containerSecurityContext,
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/ready",
							Port: intstr.FromInt32(envoyAdminPort),
						},
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       5,
				},
				VolumeMounts: volumeMounts,
			},
		},
		Volumes: volumes,
	}
}

// envoyContainerImage returns the envoy image from the MDB_ENVOY_IMAGE env var.
// Returns an error if the env var is not set.
func (r *MongoDBSearchEnvoyReconciler) envoyContainerImage() (string, error) {
	if r.defaultEnvoyImage == "" {
		return "", fmt.Errorf("%s environment variable must be set on the operator to use managed load balancer", util.EnvoyImageEnv)
	}
	return r.defaultEnvoyImage, nil
}

// envoyResourceRequirements returns user-specified resource requirements
// or the defaults (100m/128Mi requests, 500m/512Mi limits).
func envoyResourceRequirements(search *searchv1.MongoDBSearch) corev1.ResourceRequirements {
	if search.Spec.LoadBalancer != nil &&
		search.Spec.LoadBalancer.Envoy != nil &&
		search.Spec.LoadBalancer.Envoy.ResourceRequirements != nil {
		return *search.Spec.LoadBalancer.Envoy.ResourceRequirements
	}
	return defaultEnvoyResourceRequirements()
}

func defaultEnvoyResourceRequirements() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
}

// ensureProxyService creates or updates a proxy Service pointing to Envoy for a given route.
func (r *MongoDBSearchEnvoyReconciler) ensureProxyService(ctx context.Context, search *searchv1.MongoDBSearch, route envoyRoute, log *zap.SugaredLogger) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      route.ProxyServiceName,
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, svc, func() error {
		svc.Labels = map[string]string{
			"app":       search.LoadBalancerDeploymentName(),
			"component": labelName,
		}
		svc.Spec = corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: envoyPodLabels(search),
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       searchv1.EnvoyDefaultProxyPort,
					TargetPort: intstr.FromInt32(searchv1.EnvoyDefaultProxyPort),
				},
			},
		}
		return controllerutil.SetOwnerReference(search, svc, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("failed to ensure proxy Service %s: %w", route.ProxyServiceName, err)
	}

	log.Infof("Proxy Service %s ensured", route.ProxyServiceName)
	return nil
}

// cleanupStaleProxyServices removes proxy Services that no longer correspond to any current route.
func (r *MongoDBSearchEnvoyReconciler) cleanupStaleProxyServices(ctx context.Context, search *searchv1.MongoDBSearch, currentServiceNames map[string]bool, log *zap.SugaredLogger) error {
	serviceList := &corev1.ServiceList{}
	err := r.kubeClient.List(ctx, serviceList,
		client.InNamespace(search.Namespace),
		client.MatchingLabels{
			"app":       search.LoadBalancerDeploymentName(),
			"component": labelName,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to list proxy services: %w", err)
	}

	for i := range serviceList.Items {
		svc := &serviceList.Items[i]
		if !currentServiceNames[svc.Name] {
			log.Infof("Deleting stale proxy Service %s", svc.Name)
			if err := r.kubeClient.Delete(ctx, svc); err != nil && !apiErrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete stale proxy Service %s: %w", svc.Name, err)
			}
		}
	}

	return nil
}

// envoyLabels returns standard labels for Envoy resources.
func envoyLabels(search *searchv1.MongoDBSearch) map[string]string {
	return map[string]string{
		"app":       search.LoadBalancerDeploymentName(),
		"component": labelName,
	}
}

// envoyPodLabels returns labels for Envoy pod selection.
func envoyPodLabels(search *searchv1.MongoDBSearch) map[string]string {
	return map[string]string{
		"app": search.LoadBalancerDeploymentName(),
	}
}

// Controller Registration

func AddMongoDBSearchEnvoyController(ctx context.Context, mgr manager.Manager, defaultEnvoyImage string) error {
	// NOTE: The field index for MongoDBSearchIndexFieldName is already registered
	// by AddMongoDBSearchController. Do not register it again here.

	r := newMongoDBSearchEnvoyReconciler(mgr.GetClient(), defaultEnvoyImage)

	return ctrl.NewControllerManagedBy(mgr).
		Named("mongodbsearchenvoy").
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		For(&searchv1.MongoDBSearch{}).
		Watches(&mdbv1.MongoDB{}, &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch}).
		Watches(&mdbcv1.MongoDBCommunity{}, &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
