package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	tlsinspectorv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/tls_inspector/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	upstreamhttpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/envvar"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TODO: in this controller, when writing code, keep in mind that we will want to re-use the "config generation" logic
//  later in a kubectl plugin, so that users can generate the appropriate config themselves with the CLI tool, when
//  they are deploying their own LB

// Some of these variables can be exposed as configuration to the user
const (
	envoyReplicas = int32(1)

	envoyProxyPort = 27029
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

// shardRoute defines the routing information for a single shard in the Envoy config.
type shardRoute struct {
	ShardName     string // e.g., "mdb-sh-0"
	ShardNameSafe string // e.g., "mdb_sh_0" (hyphens replaced with underscores for Envoy identifiers)
	SNIHostname   string // FQDN of the proxy service for SNI matching
	UpstreamHost  string // FQDN of the mongot service
	UpstreamPort  int32  // typically 27028
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
		return reconcile.Result{}, nil
	}

	// Resolve the source database (shared with the main search controller).
	searchSource, err := getSearchSource(ctx, r.kubeClient, r.watch, mdbSearch, log)
	if err != nil {
		return reconcile.Result{RequeueAfter: 10 * time.Second}, err
	}

	// Managed LB requires a sharded cluster source
	shardedSource, ok := searchSource.(searchcontroller.SearchSourceShardedDeployment)
	if !ok {
		log.Info("Managed LB for non-sharded sources not yet supported")
		return reconcile.Result{}, nil
	}

	shardNames := shardedSource.GetShardNames()
	if len(shardNames) == 0 {
		log.Warn("No shards configured, nothing to deploy")
		return reconcile.Result{}, nil
	}

	tlsCfg := searchSource.TLSConfig()
	routes := buildShardRoutesFromNames(mdbSearch, shardNames)

	// Generate Envoy config JSON
	caKeyName := caKeyNameFromTLSConfig(tlsCfg)
	envoyJSON, err := buildEnvoyConfigJSON(routes, mdbSearch.IsTLSConfigured(), caKeyName)
	if err != nil {
		log.Errorf("Failed to build Envoy config JSON: %s", err)
		return reconcile.Result{}, err
	}

	// Ensure ConfigMap
	if err := r.ensureConfigMap(ctx, mdbSearch, envoyJSON, log); err != nil {
		return reconcile.Result{}, err
	}

	// Ensure Deployment
	if err := r.ensureDeployment(ctx, mdbSearch, envoyJSON, tlsCfg, log); err != nil {
		return reconcile.Result{}, err
	}

	// Ensure per-shard proxy Services
	currentShardNames := make(map[string]bool)
	for _, route := range routes {
		currentShardNames[route.ShardName] = true
		if err := r.ensureProxyService(ctx, mdbSearch, route.ShardName, log); err != nil {
			return reconcile.Result{}, err
		}
	}

	// Clean up stale proxy Services for removed shards
	if err := r.cleanupStaleProxyServices(ctx, mdbSearch, currentShardNames, log); err != nil {
		log.Warnf("Failed to cleanup stale proxy services: %s", err)
	}

	log.Info("MongoDBSearchEnvoy reconciliation complete")
	return reconcile.Result{}, nil
}

// caKeyNameFromTLSConfig returns the CA key filename for Envoy config file paths.
func caKeyNameFromTLSConfig(tlsCfg *searchcontroller.TLSSourceConfig) string {
	if tlsCfg != nil {
		return tlsCfg.CAFileName
	}
	return envoyCAKey
}

// buildShardRoutesFromNames builds per-shard routing information from shard names.
// Works for both internal MongoDB CRs and external sources since it only
// depends on the MongoDBSearch resource and a list of shard names.
func buildShardRoutesFromNames(search *searchv1.MongoDBSearch, shardNames []string) []shardRoute {
	routes := make([]shardRoute, 0, len(shardNames))
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	for _, shardName := range shardNames {
		proxyServiceName := search.LoadBalancerProxyServiceNameForShard(shardName)
		mongotServiceName := search.MongotServiceForShard(shardName).Name

		routes = append(routes, shardRoute{
			ShardName:     shardName,
			ShardNameSafe: strings.ReplaceAll(shardName, "-", "_"),
			SNIHostname:   fmt.Sprintf("%s.%s.svc.cluster.local", proxyServiceName, namespace),
			UpstreamHost:  fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace),
			UpstreamPort:  mongotPort,
		})
	}

	return routes
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
	image := r.envoyContainerImage(search)
	resources := envoyResourceRequirements(search)
	managedSecurityContext := envvar.ReadBool(podtemplatespec.ManagedSecurityContextEnv) // nolint:forbidigo

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerDeploymentName(),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, dep, func() error {
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
					{Name: "grpc", ContainerPort: envoyProxyPort},
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

// envoyContainerImage returns the envoy image using the priority:
// CRD field > operator env var default > hardcoded constant.
func (r *MongoDBSearchEnvoyReconciler) envoyContainerImage(search *searchv1.MongoDBSearch) string {
	if search.Spec.LoadBalancer != nil &&
		search.Spec.LoadBalancer.Envoy != nil &&
		search.Spec.LoadBalancer.Envoy.Image != "" {
		return search.Spec.LoadBalancer.Envoy.Image
	}
	return r.defaultEnvoyImage
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

// ensureProxyService creates or updates a per-shard proxy Service pointing to Envoy.
func (r *MongoDBSearchEnvoyReconciler) ensureProxyService(ctx context.Context, search *searchv1.MongoDBSearch, shardName string, log *zap.SugaredLogger) error {
	serviceName := search.LoadBalancerProxyServiceNameForShard(shardName)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, svc, func() error {
		svc.Labels = map[string]string{
			"app":          search.LoadBalancerDeploymentName(),
			"component":    labelName,
			"target-shard": shardName,
		}
		svc.Spec = corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: envoyPodLabels(search),
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       envoyProxyPort,
					TargetPort: intstr.FromInt32(envoyProxyPort),
				},
			},
		}
		return controllerutil.SetOwnerReference(search, svc, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("failed to ensure proxy Service %s: %w", serviceName, err)
	}

	log.Infof("Proxy Service %s ensured", serviceName)
	return nil
}

// cleanupStaleProxyServices removes proxy Services for shards that no longer exist.
func (r *MongoDBSearchEnvoyReconciler) cleanupStaleProxyServices(ctx context.Context, search *searchv1.MongoDBSearch, currentShardNames map[string]bool, log *zap.SugaredLogger) error {
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
		targetShard := svc.Labels["target-shard"]
		if targetShard != "" && !currentShardNames[targetShard] {
			log.Infof("Deleting stale proxy Service %s (shard %s removed)", svc.Name, targetShard)
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

// ============================================================================
// Envoy Config Builder
// ============================================================================

// buildEnvoyConfigJSON builds the Envoy bootstrap configuration using
// go-control-plane protobuf types and marshals it to JSON.
func buildEnvoyConfigJSON(routes []shardRoute, tlsEnabled bool, caKeyName string) (string, error) {
	config, err := buildEnvoyBootstrapConfig(routes, tlsEnabled, caKeyName)
	if err != nil {
		return "", fmt.Errorf("failed to build Envoy bootstrap config: %w", err)
	}

	marshaler := protojson.MarshalOptions{
		UseProtoNames: true, // snake_case field names (matches Envoy expectations)
		Indent:        "  ",
	}
	data, err := marshaler.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Envoy config to JSON: %w", err)
	}

	return string(data), nil
}

// buildEnvoyBootstrapConfig constructs the full Envoy bootstrap protobuf.
func buildEnvoyBootstrapConfig(routes []shardRoute, tlsEnabled bool, caKeyName string) (*bootstrapv3.Bootstrap, error) {
	filterChains := make([]*listenerv3.FilterChain, 0, len(routes))
	clusters := make([]*clusterv3.Cluster, 0, len(routes))

	for _, route := range routes {
		fc, err := buildFilterChain(route, tlsEnabled, caKeyName)
		if err != nil {
			return nil, fmt.Errorf("failed to build filter chain for shard %s: %w", route.ShardName, err)
		}
		filterChains = append(filterChains, fc)

		cl, err := buildCluster(route, tlsEnabled, caKeyName)
		if err != nil {
			return nil, fmt.Errorf("failed to build cluster for shard %s: %w", route.ShardName, err)
		}
		clusters = append(clusters, cl)
	}

	tlsInspectorCfg, err := anypb.New(&tlsinspectorv3.TlsInspector{})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TLS inspector config: %w", err)
	}

	runtimeStruct, err := structpb.NewStruct(map[string]interface{}{
		"overload": map[string]interface{}{
			"global_downstream_max_connections": 50000,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build runtime struct: %w", err)
	}

	return &bootstrapv3.Bootstrap{
		Admin: &bootstrapv3.Admin{
			Address: socketAddress("0.0.0.0", uint32(envoyAdminPort)),
		},
		StaticResources: &bootstrapv3.Bootstrap_StaticResources{
			Listeners: []*listenerv3.Listener{
				{
					Name:    "mongod_listener",
					Address: socketAddress("0.0.0.0", uint32(envoyProxyPort)),
					ListenerFilters: []*listenerv3.ListenerFilter{
						{
							Name: wellknown.TLSInspector,
							ConfigType: &listenerv3.ListenerFilter_TypedConfig{
								TypedConfig: tlsInspectorCfg,
							},
						},
					},
					FilterChains: filterChains,
				},
			},
			Clusters: clusters,
		},
		LayeredRuntime: &bootstrapv3.LayeredRuntime{
			Layers: []*bootstrapv3.RuntimeLayer{
				{
					Name: "static_layer",
					LayerSpecifier: &bootstrapv3.RuntimeLayer_StaticLayer{
						StaticLayer: runtimeStruct,
					},
				},
			},
		},
	}, nil
}

// socketAddress builds an Envoy Address with a TCP socket.
func socketAddress(addr string, port uint32) *corev3.Address {
	return &corev3.Address{
		Address: &corev3.Address_SocketAddress{
			SocketAddress: &corev3.SocketAddress{
				Address:       addr,
				PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: port},
			},
		},
	}
}

// buildFilterChain builds a single SNI-matched filter chain for one shard.
func buildFilterChain(route shardRoute, tlsEnabled bool, caKeyName string) (*listenerv3.FilterChain, error) {
	clusterName := fmt.Sprintf("mongot_%s_cluster", route.ShardNameSafe)

	routerFilterCfg, err := anypb.New(&routerv3.Router{})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal router filter config: %w", err)
	}

	hcm := &hcmv3.HttpConnectionManager{
		StatPrefix: fmt.Sprintf("ingress_%s", route.ShardNameSafe),
		CodecType:  hcmv3.HttpConnectionManager_AUTO,
		RouteSpecifier: &hcmv3.HttpConnectionManager_RouteConfig{
			RouteConfig: &routev3.RouteConfiguration{
				Name: fmt.Sprintf("%s_route", route.ShardNameSafe),
				VirtualHosts: []*routev3.VirtualHost{
					{
						Name:    fmt.Sprintf("mongot_%s_backend", route.ShardNameSafe),
						Domains: []string{"*"},
						Routes: []*routev3.Route{
							{
								Match: &routev3.RouteMatch{
									PathSpecifier: &routev3.RouteMatch_Prefix{Prefix: "/"},
									Grpc:          &routev3.RouteMatch_GrpcRouteMatchOptions{},
								},
								Action: &routev3.Route_Route{
									Route: &routev3.RouteAction{
										ClusterSpecifier: &routev3.RouteAction_Cluster{Cluster: clusterName},
										Timeout:          durationpb.New(300 * time.Second),
									},
								},
							},
						},
					},
				},
			},
		},
		HttpFilters: []*hcmv3.HttpFilter{
			{
				Name:       wellknown.Router,
				ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: routerFilterCfg},
			},
		},
		Http2ProtocolOptions: &corev3.Http2ProtocolOptions{
			InitialConnectionWindowSize: wrapperspb.UInt32(1048576),
			InitialStreamWindowSize:     wrapperspb.UInt32(1048576),
		},
		StreamIdleTimeout: durationpb.New(300 * time.Second),
		RequestTimeout:    durationpb.New(300 * time.Second),
	}

	hcmAny, err := anypb.New(hcm)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal HCM config: %w", err)
	}

	chain := &listenerv3.FilterChain{
		FilterChainMatch: &listenerv3.FilterChainMatch{
			ServerNames: []string{route.SNIHostname},
		},
		Filters: []*listenerv3.Filter{
			{
				Name:       wellknown.HTTPConnectionManager,
				ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny},
			},
		},
	}

	if tlsEnabled {
		ts, err := buildDownstreamTLSTransportSocket(caKeyName)
		if err != nil {
			return nil, err
		}
		chain.TransportSocket = ts
	}

	return chain, nil
}

// buildCluster builds a single upstream cluster for one shard.
func buildCluster(route shardRoute, tlsEnabled bool, caKeyName string) (*clusterv3.Cluster, error) {
	clusterName := fmt.Sprintf("mongot_%s_cluster", route.ShardNameSafe)

	upstreamHTTPOpts, err := anypb.New(&upstreamhttpv3.HttpProtocolOptions{
		CommonHttpProtocolOptions: &corev3.HttpProtocolOptions{
			IdleTimeout: durationpb.New(300 * time.Second),
		},
		UpstreamProtocolOptions: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_{
			ExplicitHttpConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig{
				ProtocolConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{
					Http2ProtocolOptions: &corev3.Http2ProtocolOptions{
						InitialConnectionWindowSize: wrapperspb.UInt32(1048576),
						InitialStreamWindowSize:     wrapperspb.UInt32(1048576),
					},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upstream HTTP protocol options: %w", err)
	}

	cluster := &clusterv3.Cluster{
		Name:                 clusterName,
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STRICT_DNS},
		LbPolicy:             clusterv3.Cluster_ROUND_ROBIN,
		TypedExtensionProtocolOptions: map[string]*anypb.Any{
			"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": upstreamHTTPOpts,
		},
		LoadAssignment: &endpointv3.ClusterLoadAssignment{
			ClusterName: clusterName,
			Endpoints: []*endpointv3.LocalityLbEndpoints{
				{
					LbEndpoints: []*endpointv3.LbEndpoint{
						{
							HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
								Endpoint: &endpointv3.Endpoint{
									Address: socketAddress(route.UpstreamHost, uint32(route.UpstreamPort)),
								},
							},
						},
					},
				},
			},
		},
		CircuitBreakers: &clusterv3.CircuitBreakers{
			Thresholds: []*clusterv3.CircuitBreakers_Thresholds{
				{
					Priority:           corev3.RoutingPriority_DEFAULT,
					MaxConnections:     wrapperspb.UInt32(1024),
					MaxPendingRequests: wrapperspb.UInt32(1024),
					MaxRequests:        wrapperspb.UInt32(1024),
					MaxRetries:         wrapperspb.UInt32(3),
				},
			},
		},
		UpstreamConnectionOptions: &clusterv3.UpstreamConnectionOptions{
			TcpKeepalive: &corev3.TcpKeepalive{
				KeepaliveTime:     wrapperspb.UInt32(10),
				KeepaliveInterval: wrapperspb.UInt32(3),
				KeepaliveProbes:   wrapperspb.UInt32(3),
			},
		},
	}

	if tlsEnabled {
		ts, err := buildUpstreamTLSTransportSocket(route, caKeyName)
		if err != nil {
			return nil, err
		}
		cluster.TransportSocket = ts
	}

	return cluster, nil
}

// buildDownstreamTLSTransportSocket builds the TLS transport socket for the listener (downstream).
func buildDownstreamTLSTransportSocket(caKeyName string) (*corev3.TransportSocket, error) {
	downstreamTLS := &tlsv3.DownstreamTlsContext{
		CommonTlsContext: &tlsv3.CommonTlsContext{
			TlsCertificates: []*tlsv3.TlsCertificate{
				{
					CertificateChain: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyServerCertPath + "/tls.crt"},
					},
					PrivateKey: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyServerCertPath + "/tls.key"},
					},
				},
			},
			ValidationContextType: &tlsv3.CommonTlsContext_ValidationContext{
				ValidationContext: &tlsv3.CertificateValidationContext{
					TrustedCa: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyCACertPath + "/" + caKeyName},
					},
				},
			},
			TlsParams: &tlsv3.TlsParameters{
				TlsMinimumProtocolVersion: tlsv3.TlsParameters_TLSv1_2,
				TlsMaximumProtocolVersion: tlsv3.TlsParameters_TLSv1_2,
			},
			AlpnProtocols: []string{"h2"},
		},
		RequireClientCertificate: wrapperspb.Bool(true),
	}

	tlsAny, err := anypb.New(downstreamTLS)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal downstream TLS context: %w", err)
	}

	return &corev3.TransportSocket{
		Name:       wellknown.TransportSocketTLS,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: tlsAny},
	}, nil
}

// buildUpstreamTLSTransportSocket builds the TLS transport socket for clusters (upstream).
func buildUpstreamTLSTransportSocket(route shardRoute, caKeyName string) (*corev3.TransportSocket, error) {
	upstreamTLS := &tlsv3.UpstreamTlsContext{
		CommonTlsContext: &tlsv3.CommonTlsContext{
			TlsCertificates: []*tlsv3.TlsCertificate{
				{
					CertificateChain: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyClientCertPath + "/tls.crt"},
					},
					PrivateKey: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyClientCertPath + "/tls.key"},
					},
				},
			},
			ValidationContextType: &tlsv3.CommonTlsContext_ValidationContext{
				ValidationContext: &tlsv3.CertificateValidationContext{
					TrustedCa: &corev3.DataSource{
						Specifier: &corev3.DataSource_Filename{Filename: envoyCACertPath + "/" + caKeyName},
					},
				},
			},
			AlpnProtocols: []string{"h2"},
		},
		Sni: route.UpstreamHost,
	}

	tlsAny, err := anypb.New(upstreamTLS)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upstream TLS context: %w", err)
	}

	return &corev3.TransportSocket{
		Name:       wellknown.TransportSocketTLS,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: tlsAny},
	}, nil
}

// ============================================================================
// Controller Registration
// ============================================================================

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
