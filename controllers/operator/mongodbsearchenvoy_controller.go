package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Some of these variables can be exposed as configuration to the user
const (
	envoyImage    = "envoyproxy/envoy:v1.31-latest"
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
)

// shardRoute defines the routing information for a single shard in the Envoy config.
type shardRoute struct {
	ShardName     string // e.g., "mdb-sh-0"
	ShardNameSafe string // e.g., "mdb_sh_0" (hyphens replaced with underscores for Envoy identifiers)
	SNIHostname   string // FQDN of the proxy service for SNI matching
	UpstreamHost  string // FQDN of the mongot service
	UpstreamPort  int32  // typically 27028
}

// caConfig holds CA certificate reference info.
type caConfig struct {
	ConfigMapName string // name of the ConfigMap containing the CA
	Key           string // key within the ConfigMap (e.g., "ca-pem")
}

type MongoDBSearchEnvoyReconciler struct {
	kubeClient kubernetesClient.Client
	watch      *watch.ResourceWatcher
}

func newMongoDBSearchEnvoyReconciler(client client.Client) *MongoDBSearchEnvoyReconciler {
	return &MongoDBSearchEnvoyReconciler{
		kubeClient: kubernetesClient.NewClient(client),
		watch:      watch.NewResourceWatcher(),
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

	// Fetch the referenced MongoDB resource
	mdb, err := r.getSourceMongoDB(ctx, mdbSearch, log)
	if err != nil {
		return reconcile.Result{RequeueAfter: 10 * time.Second}, err
	}

	// Validate: must be a sharded cluster for the scope of this POC
	if mdb.Spec.GetResourceType() != mdbv1.ShardedCluster {
		log.Warnf("Managed LB requires a ShardedCluster source, got %s", mdb.Spec.GetResourceType())
		return reconcile.Result{}, nil
	}

	if mdb.Spec.ShardCount <= 0 {
		log.Warn("ShardCount is 0, nothing to deploy")
		return reconcile.Result{}, nil
	}

	// Resolve CA config from MongoDB TLS settings
	ca := r.resolveCAConfig(mdbSearch, mdb)

	// Build per-shard routing information
	routes := r.buildShardRoutes(mdbSearch, mdb)

	// Generate Envoy config YAML
	envoyYAML, err := buildEnvoyConfigYAML(routes, mdbSearch.IsTLSConfigured(), ca)
	if err != nil {
		log.Errorf("Failed to build Envoy config YAML: %s", err)
		return reconcile.Result{}, err
	}

	// Ensure ConfigMap
	if err := r.ensureConfigMap(ctx, mdbSearch, envoyYAML, log); err != nil {
		return reconcile.Result{}, err
	}

	// Ensure Deployment
	if err := r.ensureDeployment(ctx, mdbSearch, envoyYAML, ca, log); err != nil {
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

// getSourceMongoDB fetches the MongoDB CR referenced by the MongoDBSearch resource.
func (r *MongoDBSearchEnvoyReconciler) getSourceMongoDB(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (*mdbv1.MongoDB, error) {
	resourceRef := search.GetMongoDBResourceRef()
	if resourceRef == nil {
		return nil, fmt.Errorf("MongoDBSearch source MongoDB resource reference is not set")
	}

	sourceName := types.NamespacedName{Namespace: search.GetNamespace(), Name: resourceRef.Name}
	log.Infof("Looking up MongoDB source %s for Envoy LB", sourceName)

	mdb := &mdbv1.MongoDB{}
	if err := r.kubeClient.Get(ctx, sourceName, mdb); err != nil {
		return nil, fmt.Errorf("error getting MongoDB %s: %w", sourceName, err)
	}

	r.watch.AddWatchedResourceIfNotAdded(resourceRef.Name, resourceRef.Namespace, watch.MongoDB, search.NamespacedName())
	return mdb, nil
}

// resolveCAConfig determines the CA ConfigMap name and key from the MongoDB TLS configuration.
func (r *MongoDBSearchEnvoyReconciler) resolveCAConfig(search *searchv1.MongoDBSearch, mdb *mdbv1.MongoDB) caConfig {
	tlsConfig := mdb.Spec.GetTLSConfig()
	if tlsConfig != nil && tlsConfig.CA != "" {
		return caConfig{
			ConfigMapName: tlsConfig.CA,
			Key:           envoyCAKey,
		}
	}

	// Fallback: convention-based CA name
	return caConfig{
		ConfigMapName: search.Name + "-ca",
		Key:           envoyCAKey,
	}
}

// buildShardRoutes builds per-shard routing information from the MongoDB and MongoDBSearch resources.
func (r *MongoDBSearchEnvoyReconciler) buildShardRoutes(search *searchv1.MongoDBSearch, mdb *mdbv1.MongoDB) []shardRoute {
	routes := make([]shardRoute, 0, mdb.Spec.ShardCount)
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	for i := 0; i < mdb.Spec.ShardCount; i++ {
		shardName := mdb.ShardRsName(i)
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
func (r *MongoDBSearchEnvoyReconciler) ensureConfigMap(ctx context.Context, search *searchv1.MongoDBSearch, envoyYAML string, log *zap.SugaredLogger) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerConfigMapName(),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.kubeClient, cm, func() error {
		cm.Labels = envoyLabels(search)
		cm.Data = map[string]string{"envoy.yaml": envoyYAML}
		return controllerutil.SetOwnerReference(search, cm, r.kubeClient.Scheme())
	})
	if err != nil {
		return fmt.Errorf("failed to ensure Envoy ConfigMap: %w", err)
	}

	log.Info("Envoy ConfigMap ensured")
	return nil
}

// ensureDeployment creates or updates the Envoy Deployment.
func (r *MongoDBSearchEnvoyReconciler) ensureDeployment(ctx context.Context, search *searchv1.MongoDBSearch, envoyYAML string, ca caConfig, log *zap.SugaredLogger) error {
	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(envoyYAML)))
	replicas := envoyReplicas
	labels := envoyLabels(search)
	tlsEnabled := search.IsTLSConfigured()

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
				Spec: buildEnvoyPodSpec(search, ca, tlsEnabled),
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
func buildEnvoyPodSpec(search *searchv1.MongoDBSearch, ca caConfig, tlsEnabled bool) corev1.PodSpec {
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

	if tlsEnabled {
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
			corev1.Volume{
				Name: "ca-cert",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: ca.ConfigMapName},
						Items:                []corev1.KeyToPath{{Key: ca.Key, Path: ca.Key}},
					},
				},
			},
		)

		volumeMounts = append(volumeMounts,
			corev1.VolumeMount{Name: "envoy-server-cert", MountPath: envoyServerCertPath, ReadOnly: true},
			corev1.VolumeMount{Name: "envoy-client-cert", MountPath: envoyClientCertPath, ReadOnly: true},
			corev1.VolumeMount{Name: "ca-cert", MountPath: envoyCACertPath, ReadOnly: true},
		)
	}

	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:    "envoy",
				Image:   envoyImage,
				Command: []string{"/usr/local/bin/envoy"},
				Args:    []string{"-c", "/etc/envoy/envoy.yaml", "--log-level", "info"},
				Ports: []corev1.ContainerPort{
					{Name: "grpc", ContainerPort: envoyProxyPort},
					{Name: "admin", ContainerPort: envoyAdminPort},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
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
			"component":    "search-proxy",
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
			"component": "search-proxy",
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
		"component": "search-proxy",
	}
}

// envoyPodLabels returns labels for Envoy pod selection.
func envoyPodLabels(search *searchv1.MongoDBSearch) map[string]string {
	return map[string]string{
		"app": search.LoadBalancerDeploymentName(),
	}
}

// ============================================================================
// Envoy Config Structs
// ============================================================================
//
// These types model the Envoy v3 bootstrap configuration as Go structs.
// They are marshaled to YAML via github.com/ghodss/yaml (already used in
// this codebase) to produce the envoy.yaml stored in the ConfigMap.
//
// Only the subset of the Envoy API needed for SNI-based L7 gRPC routing
// is modeled here. The canonical reference is:
// https://www.envoyproxy.io/docs/envoy/v1.31.0/api-v3/config/bootstrap/v3/bootstrap.proto

// envoyBootstrapConfig is the top-level Envoy bootstrap configuration.
type envoyBootstrapConfig struct {
	Admin           envoyAdmin           `json:"admin"`
	StaticResources envoyStaticResources `json:"static_resources"`
	LayeredRuntime  envoyLayeredRuntime  `json:"layered_runtime"`
}

type envoyAdmin struct {
	Address envoyAddress `json:"address"`
}

type envoyStaticResources struct {
	Listeners []envoyListener `json:"listeners"`
	Clusters  []envoyCluster  `json:"clusters"`
}

// --- Listener types ---

type envoyListener struct {
	Name            string                `json:"name"`
	Address         envoyAddress          `json:"address"`
	ListenerFilters []envoyListenerFilter `json:"listener_filters"`
	FilterChains    []envoyFilterChain    `json:"filter_chains"`
}

type envoyAddress struct {
	SocketAddress envoySocketAddress `json:"socket_address"`
}

type envoySocketAddress struct {
	Address   string `json:"address"`
	PortValue int32  `json:"port_value"`
}

type envoyListenerFilter struct {
	Name        string                 `json:"name"`
	TypedConfig map[string]interface{} `json:"typed_config"`
}

type envoyFilterChain struct {
	FilterChainMatch *envoyFilterChainMatch `json:"filter_chain_match,omitempty"`
	Filters          []envoyNetworkFilter   `json:"filters"`
	TransportSocket  *envoyTransportSocket  `json:"transport_socket,omitempty"`
}

type envoyFilterChainMatch struct {
	ServerNames []string `json:"server_names"`
}

type envoyNetworkFilter struct {
	Name        string      `json:"name"`
	TypedConfig interface{} `json:"typed_config"`
}

// envoyHCMConfig is the HttpConnectionManager typed_config.
type envoyHCMConfig struct {
	Type                 string             `json:"@type"`
	StatPrefix           string             `json:"stat_prefix"`
	CodecType            string             `json:"codec_type"`
	RouteConfig          envoyRouteConfig   `json:"route_config"`
	HTTPFilters          []envoyHTTPFilter  `json:"http_filters"`
	HTTP2ProtocolOptions *envoyHTTP2Options `json:"http2_protocol_options,omitempty"`
	StreamIdleTimeout    string             `json:"stream_idle_timeout,omitempty"`
	RequestTimeout       string             `json:"request_timeout,omitempty"`
}

type envoyRouteConfig struct {
	Name         string             `json:"name"`
	VirtualHosts []envoyVirtualHost `json:"virtual_hosts"`
}

type envoyVirtualHost struct {
	Name    string       `json:"name"`
	Domains []string     `json:"domains"`
	Routes  []envoyRoute `json:"routes"`
}

type envoyRoute struct {
	Match envoyRouteMatch  `json:"match"`
	Route envoyRouteAction `json:"route"`
}

type envoyRouteMatch struct {
	Prefix string                 `json:"prefix"`
	GRPC   map[string]interface{} `json:"grpc,omitempty"`
}

type envoyRouteAction struct {
	Cluster string `json:"cluster"`
	Timeout string `json:"timeout,omitempty"`
}

type envoyHTTPFilter struct {
	Name        string                 `json:"name"`
	TypedConfig map[string]interface{} `json:"typed_config"`
}

type envoyHTTP2Options struct {
	InitialConnectionWindowSize int `json:"initial_connection_window_size,omitempty"`
	InitialStreamWindowSize     int `json:"initial_stream_window_size,omitempty"`
}

// --- TLS types ---

type envoyTransportSocket struct {
	Name        string      `json:"name"`
	TypedConfig interface{} `json:"typed_config"`
}

type envoyDownstreamTLSContext struct {
	Type                     string                `json:"@type"`
	CommonTLSContext         envoyCommonTLSContext `json:"common_tls_context"`
	RequireClientCertificate bool                  `json:"require_client_certificate"`
}

type envoyUpstreamTLSContext struct {
	Type             string                `json:"@type"`
	CommonTLSContext envoyCommonTLSContext `json:"common_tls_context"`
	SNI              string                `json:"sni,omitempty"`
}

type envoyCommonTLSContext struct {
	TLSCertificates   []envoyTLSCertificate   `json:"tls_certificates"`
	ValidationContext *envoyValidationContext `json:"validation_context,omitempty"`
	TLSParams         *envoyTLSParams         `json:"tls_params,omitempty"`
	ALPNProtocols     []string                `json:"alpn_protocols,omitempty"`
}

type envoyTLSCertificate struct {
	CertificateChain envoyDataSource `json:"certificate_chain"`
	PrivateKey       envoyDataSource `json:"private_key"`
}

type envoyValidationContext struct {
	TrustedCA envoyDataSource `json:"trusted_ca"`
}

type envoyTLSParams struct {
	TLSMinimumProtocolVersion string `json:"tls_minimum_protocol_version,omitempty"`
	TLSMaximumProtocolVersion string `json:"tls_maximum_protocol_version,omitempty"`
}

type envoyDataSource struct {
	Filename string `json:"filename"`
}

// --- Cluster types ---

type envoyCluster struct {
	Name                      string                    `json:"name"`
	Type                      string                    `json:"type"`
	LBPolicy                  string                    `json:"lb_policy"`
	HTTP2ProtocolOptions      *envoyHTTP2Options        `json:"http2_protocol_options,omitempty"`
	LoadAssignment            envoyLoadAssignment       `json:"load_assignment"`
	CircuitBreakers           *envoyCircuitBreakers     `json:"circuit_breakers,omitempty"`
	TransportSocket           *envoyTransportSocket     `json:"transport_socket,omitempty"`
	UpstreamConnectionOptions *envoyUpstreamConnOptions `json:"upstream_connection_options,omitempty"`
	CommonHTTPProtocolOptions *envoyCommonHTTPOptions   `json:"common_http_protocol_options,omitempty"`
}

type envoyLoadAssignment struct {
	ClusterName string               `json:"cluster_name"`
	Endpoints   []envoyEndpointGroup `json:"endpoints"`
}

type envoyEndpointGroup struct {
	LBEndpoints []envoyLBEndpoint `json:"lb_endpoints"`
}

type envoyLBEndpoint struct {
	Endpoint envoyEndpoint `json:"endpoint"`
}

type envoyEndpoint struct {
	Address envoyAddress `json:"address"`
}

type envoyCircuitBreakers struct {
	Thresholds []envoyCircuitBreakerThreshold `json:"thresholds"`
}

type envoyCircuitBreakerThreshold struct {
	Priority           string `json:"priority"`
	MaxConnections     int    `json:"max_connections"`
	MaxPendingRequests int    `json:"max_pending_requests"`
	MaxRequests        int    `json:"max_requests"`
	MaxRetries         int    `json:"max_retries"`
}

type envoyUpstreamConnOptions struct {
	TCPKeepalive envoyTCPKeepalive `json:"tcp_keepalive"`
}

type envoyTCPKeepalive struct {
	KeepaliveTime     int `json:"keepalive_time"`
	KeepaliveInterval int `json:"keepalive_interval"`
	KeepaliveProbes   int `json:"keepalive_probes"`
}

type envoyCommonHTTPOptions struct {
	IdleTimeout string `json:"idle_timeout"`
}

// --- Runtime types ---

type envoyLayeredRuntime struct {
	Layers []envoyRuntimeLayer `json:"layers"`
}

type envoyRuntimeLayer struct {
	Name        string                 `json:"name"`
	StaticLayer map[string]interface{} `json:"static_layer"`
}

// ============================================================================
// Envoy Config Builder
// ============================================================================

// buildEnvoyConfigYAML builds the Envoy bootstrap configuration from structured
// types and marshals it to YAML.
func buildEnvoyConfigYAML(routes []shardRoute, tlsEnabled bool, ca caConfig) (string, error) {
	config := buildEnvoyBootstrapConfig(routes, tlsEnabled, ca)

	data, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Envoy config to YAML: %w", err)
	}

	return string(data), nil
}

// buildEnvoyBootstrapConfig constructs the full Envoy bootstrap configuration struct.
func buildEnvoyBootstrapConfig(routes []shardRoute, tlsEnabled bool, ca caConfig) envoyBootstrapConfig {
	filterChains := make([]envoyFilterChain, 0, len(routes))
	clusters := make([]envoyCluster, 0, len(routes))

	for _, route := range routes {
		filterChains = append(filterChains, buildFilterChain(route, tlsEnabled, ca))
		clusters = append(clusters, buildCluster(route, tlsEnabled, ca))
	}

	return envoyBootstrapConfig{
		Admin: envoyAdmin{
			Address: envoyAddress{
				SocketAddress: envoySocketAddress{
					Address:   "0.0.0.0",
					PortValue: envoyAdminPort,
				},
			},
		},
		StaticResources: envoyStaticResources{
			Listeners: []envoyListener{
				{
					Name: "mongod_listener",
					Address: envoyAddress{
						SocketAddress: envoySocketAddress{
							Address:   "0.0.0.0",
							PortValue: envoyProxyPort,
						},
					},
					ListenerFilters: []envoyListenerFilter{
						{
							Name: "envoy.filters.listener.tls_inspector",
							TypedConfig: map[string]interface{}{
								"@type": "type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector",
							},
						},
					},
					FilterChains: filterChains,
				},
			},
			Clusters: clusters,
		},
		LayeredRuntime: envoyLayeredRuntime{
			Layers: []envoyRuntimeLayer{
				{
					Name: "static_layer",
					StaticLayer: map[string]interface{}{
						"overload": map[string]interface{}{
							"global_downstream_max_connections": 50000,
						},
					},
				},
			},
		},
	}
}

// buildFilterChain builds a single SNI-matched filter chain for one shard.
func buildFilterChain(route shardRoute, tlsEnabled bool, ca caConfig) envoyFilterChain {
	clusterName := fmt.Sprintf("mongot_%s_cluster", route.ShardNameSafe)

	hcm := envoyHCMConfig{
		Type:       "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
		StatPrefix: fmt.Sprintf("ingress_%s", route.ShardNameSafe),
		CodecType:  "AUTO",
		RouteConfig: envoyRouteConfig{
			Name: fmt.Sprintf("%s_route", route.ShardNameSafe),
			VirtualHosts: []envoyVirtualHost{
				{
					Name:    fmt.Sprintf("mongot_%s_backend", route.ShardNameSafe),
					Domains: []string{"*"},
					Routes: []envoyRoute{
						{
							Match: envoyRouteMatch{
								Prefix: "/",
								GRPC:   map[string]interface{}{},
							},
							Route: envoyRouteAction{
								Cluster: clusterName,
								Timeout: "300s",
							},
						},
					},
				},
			},
		},
		HTTPFilters: []envoyHTTPFilter{
			{
				Name: "envoy.filters.http.router",
				TypedConfig: map[string]interface{}{
					"@type": "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router",
				},
			},
		},
		HTTP2ProtocolOptions: &envoyHTTP2Options{
			InitialConnectionWindowSize: 1048576,
			InitialStreamWindowSize:     1048576,
		},
		StreamIdleTimeout: "300s",
		RequestTimeout:    "300s",
	}

	chain := envoyFilterChain{
		FilterChainMatch: &envoyFilterChainMatch{
			ServerNames: []string{route.SNIHostname},
		},
		Filters: []envoyNetworkFilter{
			{
				Name:        "envoy.filters.network.http_connection_manager",
				TypedConfig: hcm,
			},
		},
	}

	if tlsEnabled {
		chain.TransportSocket = &envoyTransportSocket{
			Name: "envoy.transport_sockets.tls",
			TypedConfig: envoyDownstreamTLSContext{
				Type: "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext",
				CommonTLSContext: envoyCommonTLSContext{
					TLSCertificates: []envoyTLSCertificate{
						{
							CertificateChain: envoyDataSource{Filename: envoyServerCertPath + "/tls.crt"},
							PrivateKey:       envoyDataSource{Filename: envoyServerCertPath + "/tls.key"},
						},
					},
					ValidationContext: &envoyValidationContext{
						TrustedCA: envoyDataSource{Filename: envoyCACertPath + "/" + ca.Key},
					},
					TLSParams: &envoyTLSParams{
						TLSMinimumProtocolVersion: "TLSv1_2",
						TLSMaximumProtocolVersion: "TLSv1_2",
					},
					ALPNProtocols: []string{"h2"},
				},
				RequireClientCertificate: true,
			},
		}
	}

	return chain
}

// buildCluster builds a single upstream cluster for one shard.
func buildCluster(route shardRoute, tlsEnabled bool, ca caConfig) envoyCluster {
	clusterName := fmt.Sprintf("mongot_%s_cluster", route.ShardNameSafe)

	cluster := envoyCluster{
		Name:     clusterName,
		Type:     "STRICT_DNS",
		LBPolicy: "ROUND_ROBIN",
		HTTP2ProtocolOptions: &envoyHTTP2Options{
			InitialConnectionWindowSize: 1048576,
			InitialStreamWindowSize:     1048576,
		},
		LoadAssignment: envoyLoadAssignment{
			ClusterName: clusterName,
			Endpoints: []envoyEndpointGroup{
				{
					LBEndpoints: []envoyLBEndpoint{
						{
							Endpoint: envoyEndpoint{
								Address: envoyAddress{
									SocketAddress: envoySocketAddress{
										Address:   route.UpstreamHost,
										PortValue: route.UpstreamPort,
									},
								},
							},
						},
					},
				},
			},
		},
		CircuitBreakers: &envoyCircuitBreakers{
			Thresholds: []envoyCircuitBreakerThreshold{
				{
					Priority:           "DEFAULT",
					MaxConnections:     1024,
					MaxPendingRequests: 1024,
					MaxRequests:        1024,
					MaxRetries:         3,
				},
			},
		},
		UpstreamConnectionOptions: &envoyUpstreamConnOptions{
			TCPKeepalive: envoyTCPKeepalive{
				KeepaliveTime:     10,
				KeepaliveInterval: 3,
				KeepaliveProbes:   3,
			},
		},
		CommonHTTPProtocolOptions: &envoyCommonHTTPOptions{
			IdleTimeout: "300s",
		},
	}

	if tlsEnabled {
		cluster.TransportSocket = &envoyTransportSocket{
			Name: "envoy.transport_sockets.tls",
			TypedConfig: envoyUpstreamTLSContext{
				Type: "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
				CommonTLSContext: envoyCommonTLSContext{
					TLSCertificates: []envoyTLSCertificate{
						{
							CertificateChain: envoyDataSource{Filename: envoyClientCertPath + "/tls.crt"},
							PrivateKey:       envoyDataSource{Filename: envoyClientCertPath + "/tls.key"},
						},
					},
					ValidationContext: &envoyValidationContext{
						TrustedCA: envoyDataSource{Filename: envoyCACertPath + "/" + ca.Key},
					},
					ALPNProtocols: []string{"h2"},
				},
				SNI: route.UpstreamHost,
			},
		}
	}

	return cluster
}

// ============================================================================
// Controller Registration
// ============================================================================

func AddMongoDBSearchEnvoyController(ctx context.Context, mgr manager.Manager) error {
	// NOTE: The field index for MongoDBSearchIndexFieldName is already registered
	// by AddMongoDBSearchController. Do not register it again here.

	r := newMongoDBSearchEnvoyReconciler(kubernetesClient.NewClient(mgr.GetClient()))

	return ctrl.NewControllerManagedBy(mgr).
		Named("mongodbsearchenvoy").
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		For(&searchv1.MongoDBSearch{}).
		Watches(&mdbv1.MongoDB{}, &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch}).
		Watches(&mdbcv1.MongoDBCommunity{}, &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch}).
		Watches(&corev1.Secret{}, &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch}).
		Watches(&corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
