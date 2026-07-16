package search

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
)

const (
	// ShardNamePlaceholder is the placeholder used in endpoint templates for sharded clusters
	ShardNamePlaceholder = "{shardName}"

	// LabelResourceOwner is the label key used to identify the MongoDBSearch CR that
	// owns a resource. Used as part of GetOwnerLabels for StateStore ConfigMap selection.
	LabelResourceOwner = "mongodb.com/v1.mongodbSearchResourceOwner"

	MongotDefaultWireprotoPort      int32 = 27027
	MongotDefaultGrpcPort           int32 = 27028
	MongotDefaultPrometheusPort     int32 = 9946
	MongotDefautHealthCheckPort     int32 = 8080
	EnvoyDefaultProxyPort           int32 = 27028
	MongotDefaultSyncSourceUsername       = "search-sync-source"

	// MongotTerminationGracePeriodSeconds matches mongot's internal 60s gRPC
	// awaitTermination drain window. Without it the pod default (30s) SIGKILLs
	// mongot mid-drain, abandoning in-flight $search cursor streams on a rolling
	// restart. EnvoyTerminationGracePeriodSeconds outlives it by 10s so Envoy
	// keeps serving those in-flight streams until mongot has drained.
	MongotTerminationGracePeriodSeconds int64 = 60
	EnvoyTerminationGracePeriodSeconds  int64 = MongotTerminationGracePeriodSeconds + 10

	// EnvoyPreStopDrainSleepSeconds is how long the preStop hook sleeps after
	// POSTing a graceful /drain_listeners. The sleep blocks SIGTERM so in-flight
	// mongod->Envoy->mongot getMore streams complete on the draining Envoy
	// instead of being severed. Kept below the grace period so SIGKILL never
	// preempts it.
	EnvoyPreStopDrainSleepSeconds int64 = EnvoyTerminationGracePeriodSeconds - 10

	ForceWireprotoAnnotation = "mongodb.com/v1.force-search-wireproto"

	// DisableReconciliationAnnotation, when set to "true" on a MongoDBSearch CR,
	// short-circuits the reconciler: it returns Result{} + nil without
	// mutating any owned objects. Useful for tests that need to mutate
	// owned StatefulSets directly without the operator reverting them.
	DisableReconciliationAnnotation = "mongodb.com/disable-reconciliation"

	MongoDBSearchIndexFieldName = "mdbsearch-for-mongodbresourceref-index"

	// ProxyServiceSuffix is the suffix used for the stable proxy Service that mongod connects to.
	// This Service always exists (except for unmanaged LB) and its selector flips between
	// mongot pods (no LB) and Envoy pods (managed LB), keeping mongotHost stable.
	ProxyServiceSuffix = "proxy-svc"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBSearch{}, &MongoDBSearchList{})
}

// PrometheusMode controls whether the Prometheus metrics endpoint in mongot is enabled.
// +kubebuilder:validation:Enum=enabled;disabled
type PrometheusMode string

const (
	// PrometheusModeEnabled enables the Prometheus metrics endpoint in mongot.
	PrometheusModeEnabled PrometheusMode = "enabled"
	// PrometheusModeDisabled disables the Prometheus metrics endpoint in mongot.
	PrometheusModeDisabled PrometheusMode = "disabled"
)

type Prometheus struct {
	// Mode controls whether the Prometheus metrics endpoint in mongot is enabled.
	// Defaults to enabled.
	// +optional
	// +kubebuilder:default=enabled
	// +kubebuilder:validation:Enum=enabled;disabled
	Mode PrometheusMode `json:"mode,omitempty"`

	// Port where metrics endpoint will be exposed on. Defaults to 9946.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9946
	Port int `json:"port,omitempty"`
}

func (p Prometheus) IsEnabled() bool {
	return p.Mode != PrometheusModeDisabled
}

func (p Prometheus) GetPort() int32 {
	if p.Port == 0 {
		return MongotDefaultPrometheusPort
	}
	//nolint:gosec
	return int32(p.Port)
}

// MetricsForwarderMode controls when the metrics forwarder Deployment is created.
// +kubebuilder:validation:Enum=auto;enabled;disabled
type MetricsForwarderMode string

const (
	// MetricsForwarderModeAuto enables the metrics forwarder automatically for internal MongoDB sources,
	// and for external sources when OpsManager is set.
	MetricsForwarderModeAuto MetricsForwarderMode = "auto"
	// MetricsForwarderModeEnabled always enables the metrics forwarder.
	MetricsForwarderModeEnabled MetricsForwarderMode = "enabled"
	// MetricsForwarderModeDisabled always disables the metrics forwarder.
	MetricsForwarderModeDisabled MetricsForwarderMode = "disabled"
)

// MetricsForwarderOpsManagerConfig holds Ops Manager connection details for the metrics forwarder.
type MetricsForwarderOpsManagerConfig struct {
	// AgentCredentials is a reference to a Secret containing the Ops Manager Agent API key.
	// +kubebuilder:validation:Required
	AgentCredentials corev1.LocalObjectReference `json:"agentCredentials"`
	// ProjectConfigMapRef is a reference to a ConfigMap containing OM Project configuration.
	// +kubebuilder:validation:Required
	ProjectConfigMapRef corev1.LocalObjectReference `json:"projectConfigMapRef"`
}

// MetricsForwarderConfig configures the Ops Manager metrics forwarder.
type MetricsForwarderConfig struct {
	// Mode controls whether the metrics forwarder Deployment is created.
	// Auto (default): enabled for internal MongoDB sources, and for external sources when OpsManager is set.
	// Enabled: always create the metrics forwarder.
	// Disabled: never create the metrics forwarder.
	// +optional
	// +kubebuilder:default=auto
	Mode MetricsForwarderMode `json:"mode,omitempty"`
	// ResourceRequirements for the metrics forwarder container.
	// +optional
	// +kubebuilder:default={requests: {cpu: "100m", memory: "128Mi"}, limits: {cpu: "250m", memory: "256Mi"}}
	ResourceRequirements corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// Deployment holds optional overrides merged into the operator-created metrics forwarder Deployment.
	// +optional
	Deployment *v1.DeploymentConfiguration `json:"deployment,omitempty"`
	// OpsManager holds the Ops Manager project and credentials configuration for the metrics forwarder.
	// If not set, the operator derives the connection details from the source MongoDB resource's connection spec.
	// +optional
	OpsManager *MetricsForwarderOpsManagerConfig `json:"opsManager,omitempty"`
}

// ObservabilityConfig holds observability-related configuration for MongoDBSearch.
type ObservabilityConfig struct {
	// Prometheus configures the Prometheus metrics endpoint in mongot.
	// By default the endpoint is enabled on port 9946.
	// +optional
	// +kubebuilder:default={}
	Prometheus Prometheus `json:"prometheus,omitempty"`
	// MetricsForwarder configures a metrics forwarder Deployment that scrapes
	// mongot Prometheus metrics and forwards them to Ops Manager.
	// +optional
	// +kubebuilder:default={}
	MetricsForwarder MetricsForwarderConfig `json:"metricsForwarder,omitempty"`
}
type MongoDBSearchSpec struct {
	// Version of MongoDB Search (mongot) to run. If unset, the operator picks the most appropriate version.
	// +optional
	Version string `json:"version"`
	// Source is the MongoDB database that MongoDB Search syncs from to build its indexes.
	// +optional
	Source *MongoDBSource `json:"source"`
	// Security holds the TLS settings for the MongoDB Search server.
	// +optional
	Security Security `json:"security"`
	// Configure verbosity of mongot logs. Defaults to INFO if not set.
	// +kubebuilder:validation:Enum=TRACE;DEBUG;INFO;WARN;ERROR
	// +optional
	LogLevel mdb.LogLevel `json:"logLevel,omitempty"`
	// Observability configures observability features (e.g. Prometheus endpoint and metrics forwarding to Ops Manager).
	// +optional
	// +kubebuilder:default={}
	Observability ObservabilityConfig `json:"observability,omitempty"`
	// AutoEmbedding configures MongoDB Search to generate vector embeddings automatically
	// through an embedding model service. These values populate the `embedding` section of the mongot config.
	// +optional
	AutoEmbedding *EmbeddingConfig `json:"autoEmbedding,omitempty"`
	// FeatureFlags configures mongot feature flags. When a flag is set to true in the CR,
	// it is rendered into the mongot config YAML. When omitted or false, the flag is not
	// included in mongot config and mongot uses its built-in defaults.
	// +optional
	FeatureFlags *FeatureFlags `json:"featureFlags,omitempty"`
	// Clusters configures the deployment per Kubernetes cluster: one entry for a
	// single cluster (name optional), or one entry per cluster for
	// multi-cluster (name required, len > 1). This is the place to set
	// replicas, resources, storage, and StatefulSet overrides.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:XValidation:rule="size(self) <= 1 || self.all(c1, has(c1.name) && self.exists_one(c2, has(c2.name) && c2.name == c1.name))",message="clusters[].name must be set and unique when more than one cluster is specified"
	// +kubebuilder:validation:XValidation:rule="self.all(c1, !has(c1.index) || self.exists_one(c2, has(c2.index) && c2.index == c1.index))",message="clusters[].index must be unique when set"
	// +kubebuilder:validation:XValidation:rule="size(self) <= 1 || self.all(c, has(c.index))",message="clusters[].index is required on every entry when more than one cluster is specified"
	Clusters []ClusterSpec `json:"clusters"`
}

// AdvancedMongotConfigs wraps free-form mongot configuration. The CRD generator
// does not support map[string]interface{} directly, hence the MapWrapper indirection.
type AdvancedMongotConfigs struct {
	v1.MapWrapper `json:"-"`
}

// ToMap returns a deep copy of the wrapped config, or nil when unset.
func (a *AdvancedMongotConfigs) ToMap() map[string]interface{} {
	if a == nil || a.Object == nil {
		return nil
	}
	return a.MapWrapper.DeepCopy().Object
}

// SyncSourceSelector picks which mongods this cluster's mongot fleet syncs from.
type SyncSourceSelector struct {
	// MatchTagSets selects sync-source mongods by replica-set tags: an ordered list of
	// tag sets passed to mongot's syncSource.replicationReader (secondaryPreferred).
	// mongot syncs from the first set with matches; a trailing {} is a match-any fallback.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	MatchTagSets []map[string]string `json:"matchTagSets,omitempty"`
}

// ClusterSpec is one entry in spec.clusters[]. The cluster name (spec.clusters[].name)
// is required and immutable when len(spec.clusters) > 1; optional in the single-cluster case.
// Each field, when set, applies to this cluster; when unset, the operator's
// per-field default applies.
type ClusterSpec struct {
	// Name is the Kubernetes cluster name. Required and immutable
	// when len(spec.clusters) > 1; optional in the single-cluster case.
	// MaxLength is 253 — the DNS subdomain limit Kubernetes cluster names follow.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
	// Index is the stable integer in per-cluster resource names. Required on every entry
	// of a multi-cluster spec, and even on a single entry when each member cluster runs its own
	// operator. Changing it renames this cluster's resources, orphaning those at the old index.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=999
	Index *int32 `json:"index,omitempty"`
	// Replicas is the number of mongot pods for this cluster's StatefulSet.
	// For ReplicaSet sources this is the total; for sharded sources it is per shard.
	// When Replicas > 1, a load balancer (spec.clusters[].loadBalancer) is required to
	// distribute traffic across mongot instances.
	// Set to 0 to take mongot offline: the StatefulSet scales to 0 while the MongoDBSearch CR stays.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`
	// ResourceRequirements configures resource requests and limits for this cluster's mongot pods.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// Persistence configures this cluster's mongot persistent volume. Defaults to 10GB if unset.
	// +optional
	Persistence *v1.Persistence `json:"persistence,omitempty"`
	// NodeAffinity can be used to configure mongot pod's node affinity or pod's spec.affinity.nodeAffinity field.
	// A spec.clusters[].statefulSet override of nodeAffinity, takes precedence over this field.
	// +optional
	NodeAffinity *corev1.NodeAffinity `json:"nodeAffinity,omitempty"`
	// StatefulSetConfiguration is applied to this cluster's mongot StatefulSet at the end of the
	// reconcile loop, for customizations not exposed as first-class fields.
	// +optional
	StatefulSetConfiguration *v1.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// +optional
	SyncSourceSelector *SyncSourceSelector `json:"syncSourceSelector,omitempty"`
	// LoadBalancer configures how mongod/mongos connect to this cluster's mongot
	// (managed Envoy or user-provided). Every cluster must agree on the mode:
	// either all clusters set managed, all set unmanaged, or none set loadBalancer.
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"loadBalancer,omitempty"`
	// JVMFlags sets the `--jvm-flags` option for this cluster's mongot pods.
	// +optional
	JVMFlags []string `json:"jvmFlags,omitempty"`
	// ShardOverrides applies per-shard sizing exceptions within this cluster,
	// for external sharded sources only. Each entry layers its set sizing fields
	// onto this cluster's resolved values for the named shards.
	// +optional
	ShardOverrides []ShardOverride `json:"shardOverrides,omitempty"`
	// AdvancedMongotConfigs is an opaque block of mongot settings rendered verbatim
	// under the advancedConfigs key of this cluster's mongot config file.
	// The operator neither reads nor modifies it; operator-generated settings are unaffected.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	AdvancedMongotConfigs *AdvancedMongotConfigs `json:"advancedMongotConfigs,omitempty"`
}

// ResolveIndex resolves this cluster's per-cluster resource index: the pinned
// Index, else 0.
func (c ClusterSpec) ResolveIndex() int {
	if c.Index == nil {
		return 0
	}
	return int(*c.Index)
}

// ShardOverride sizes specific shards within the enclosing cluster differently
// from the cluster default. Replicas, ResourceRequirements, Persistence,
// JVMFlags and NodeAffinity replace the cluster value for the named shards when set;
// StatefulSetConfiguration is deep-merged onto the cluster value. Unset fields
// inherit the cluster value.
type ShardOverride struct {
	// ShardNames are the shards (within this cluster) this override applies to.
	// Each must exist in spec.source.external.shardedCluster.shards[].
	// +kubebuilder:validation:MinItems=1
	ShardNames []string `json:"shardNames"`
	// Replicas replaces the cluster's mongot replica count for these shards.
	// Set to 0 to take these shards' mongot offline.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`
	// ResourceRequirements replaces the cluster's mongot resource requests/limits for these shards.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// Persistence replaces the cluster's mongot persistent volume config for these shards.
	// +optional
	Persistence *v1.Persistence `json:"persistence,omitempty"`
	// NodeAffinity replaces the cluster's mongot node affinity for these shards.
	// +optional
	NodeAffinity *corev1.NodeAffinity `json:"nodeAffinity,omitempty"`
	// StatefulSetConfiguration is deep-merged onto the cluster's StatefulSet override for these shards.
	// +optional
	StatefulSetConfiguration *v1.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// JVMFlags replaces the cluster's jvmFlags for these shards when non-empty.
	// +optional
	JVMFlags []string `json:"jvmFlags,omitempty"`
}

// ReplicasOrDefault returns the cluster's mongot replica count, defaulting to 1
// when Replicas is unset.
//
// An explicit 0 is honored: callers (and the connectivity-tool /
// availability-tester e2e tests) take mongot offline by setting
// spec.clusters[].replicas=0 on the MongoDBSearch CR. A `> 0` guard would
// silently clamp that to 1, so the operator would never scale the mongot
// StatefulSet down and tests waiting on the scale-to-0 would time out.
func (c ClusterSpec) ReplicasOrDefault() int {
	if c.Replicas != nil {
		return int(*c.Replicas)
	}
	return 1
}

// LoadBalancerConfig configures L7 load balancing between mongod/mongos and mongot.
// Exactly one of Managed or Unmanaged must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.managed) && !has(self.unmanaged)) || (!has(self.managed) && has(self.unmanaged))",message="exactly one of managed or unmanaged must be set"
type LoadBalancerConfig struct {
	// Managed configures an operator-managed Envoy load balancer.
	// +optional
	Managed *ManagedLBConfig `json:"managed,omitempty"`
	// Unmanaged configures a user-provided (BYO) L7 load balancer.
	// +optional
	Unmanaged *UnmanagedLBConfig `json:"unmanaged,omitempty"`
}

// ManagedLBConfig configures the operator-deployed Envoy proxy.
type ManagedLBConfig struct {
	// ExternalHostname is the hostname Envoy expects for SNI matching on incoming requests.
	// For sharded clusters, may contain a {shardName} placeholder.
	// In multi-cluster deployments, every cluster's hostname must be distinct.
	// Required when MongoDB is externally managed. Ignored for operator-managed MongoDB.
	// +optional
	ExternalHostname string `json:"externalHostname,omitempty"`
	// RouterHostname is the host:port a sharded cluster's mongos (router) uses to reach this cluster's
	// mongot via the managed Envoy LB, and the SNI hostname Envoy matches for the cluster-level filter
	// chain. Unlike ExternalHostname it is shard-agnostic and must NOT contain a {shardName}
	// placeholder. This value is per-cluster: in multi-cluster deployments each cluster has its own
	// Envoy LB and its mongos routes to it, so every cluster's RouterHostname must be distinct.
	// Required for an external sharded MongoDB source with managed LB; ignored for ReplicaSet sources
	// and operator-managed MongoDB. Must be covered by the Envoy LB server certificate SANs.
	// +optional
	RouterHostname string `json:"routerHostname,omitempty"`
	// Replicas is the number of Envoy proxy pods to deploy.
	// Defaults to 1 if not specified.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`
	// ResourceRequirements for the Envoy container.
	// When not set, defaults to requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: 500m, memory: 512Mi}.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// Deployment holds optional overrides merged into the operator-created Envoy Deployment.
	// Follows the same convention as spec.statefulSet on MongoDB resources.
	// +optional
	Deployment *v1.DeploymentConfiguration `json:"deployment,omitempty"`
	// RetryPolicy configures Envoy retry behavior for individual gRPC streams to upstream mongot clusters.
	// When not set, retries are enabled with sensible defaults (2 retries, 60s per-try timeout).
	// +optional
	RetryPolicy *EnvoyRetryPolicy `json:"retryPolicy,omitempty"`
	// MinMongotReadyReplicas is the minimum number of ready mongot replicas in a group
	// before this mongot group can be considered ready and envoy routes real traffic to it.
	// Until this threshold is met, traffic for that shard is forwarded to a healthy mongot group with the
	// routed_from_another_shard header (returning empty results).
	// Defaults to 1 if not specified.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinMongotReadyReplicas *int32 `json:"minMongotReadyReplicas,omitempty"`
}

// EnvoyRetryPolicy configures retry behavior for individual gRPC streams to upstream mongot clusters.
// Retries are always attempted on a different host than the one that failed.
// All fields are optional; when nil, operator defaults are used.
type EnvoyRetryPolicy struct {
	// NumRetries is the maximum number of retries per request.
	// Defaults to 2 if not specified (3 total attempts including the original).
	// +optional
	// +kubebuilder:validation:Minimum=1
	NumRetries *uint32 `json:"numRetries,omitempty"`
	// PerTryTimeout is the timeout for each retry attempt (including the original).
	// Specified as a Go duration string (e.g., "60s", "30s").
	// Defaults to "60s" if not specified.
	// +optional
	PerTryTimeout *string `json:"perTryTimeout,omitempty"`
}

// UnmanagedLBConfig configures a user-provided (BYO) L7 load balancer.
type UnmanagedLBConfig struct {
	// Endpoint is the full host:port of the BYO load balancer written into mongod config.
	// For sharded clusters, must contain a {shardName} placeholder.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

type EmbeddingConfig struct {
	// ProviderEndpoint is the URL of the embedding model service.
	ProviderEndpoint string `json:"providerEndpoint,omitempty"`
	// EmbeddingModelAPIKeySecret references a Secret holding the embedding model's API keys.
	// The Secret must contain two keys: query-key and indexing-key.
	// It may be omitted only when ProviderEndpoint points to the Kubernetes Service
	// exposing the self-hosted Voyage AI embedding model managed by this operator. For any
	// other endpoint the secret remains required; the operator validates this at
	// reconcile time.
	// +optional
	EmbeddingModelAPIKeySecret corev1.LocalObjectReference `json:"embeddingModelAPIKeySecret,omitempty"`
}

// FeatureFlags configures mongot feature flags. Each field maps to a named
// feature flag in the mongot config.
type FeatureFlags struct {
	// EnableOverloadRetrySignal enables the OVERLOAD_RETRY_SIGNAL feature in mongot,
	// allowing it to signal load shedding to upstream proxies (e.g., Envoy) via
	// gRPC RESOURCE_EXHAUSTED status codes. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	EnableOverloadRetrySignal *bool `json:"enableOverloadRetrySignal,omitempty"`
}

type MongoDBSource struct {
	// MongoDBResourceRef points to an operator-managed MongoDB resource to sync from.
	// Mutually exclusive with External.
	// +optional
	MongoDBResourceRef *userv1.MongoDBResourceRef `json:"mongodbResourceRef,omitempty"`
	// ExternalMongoDBSource describes a MongoDB deployment the operator does not manage.
	// Mutually exclusive with MongoDBResourceRef.
	// +optional
	ExternalMongoDBSource *ExternalMongoDBSource `json:"external,omitempty"`
	// PasswordSecretRef references the Secret holding the sync-source user's password.
	// +optional
	PasswordSecretRef *userv1.SecretKeyRef `json:"passwordSecretRef,omitempty"`
	// Username is the sync-source user mongot authenticates as. Defaults to search-sync-source.
	// +optional
	Username *string `json:"username,omitempty"`
	// X509 configures x509 client certificate authentication for the sync source connection.
	// When set, mongot authenticates to MongoDB using x509 instead of username/password.
	// Mutually exclusive with PasswordSecretRef and Username.
	// +optional
	X509 *X509Auth `json:"x509,omitempty"`
	// TLS configures TLS transport settings for the sync source SCRAM connection.
	// When set, mongot presents the specified client certificate during the TLS handshake.
	// Only applicable when using SCRAM auth (passwordSecretRef). Mutually exclusive with X509.
	// +optional
	TLS *SourceTLS `json:"tls,omitempty"`
}

// X509Auth configures x509 client certificate authentication for mongot's sync source connection.
type X509Auth struct {
	// ClientCertificateSecret is a reference to a Secret containing the x509 client
	// certificate and key for authenticating to the MongoDB sync source.
	// Expected keys: "tls.crt", "tls.key".
	// +kubebuilder:validation:Required
	ClientCertificateSecret corev1.LocalObjectReference `json:"clientCertificateSecretRef"`
	// KeyFilePasswordSecret references a Secret with the password (key: "keyFilePassword") that
	// decrypts the password-encrypted client private key. Omit when the key is not encrypted.
	// +optional
	KeyFilePasswordSecret corev1.LocalObjectReference `json:"keyFilePasswordSecretRef,omitempty"`
}

// SourceTLS configures TLS transport settings for the sync source connection.
// This is separate from authentication — when using SCRAM auth with mTLS,
// mongot presents this client certificate during the TLS handshake while
// still authenticating via username/password.
type SourceTLS struct {
	// ClientCertificateSecret is a reference to a Secret containing a TLS client
	// certificate and key to present during the TLS handshake with the source MongoDB.
	// Expected keys: "tls.crt", "tls.key".
	// +kubebuilder:validation:Required
	ClientCertificateSecret corev1.LocalObjectReference `json:"clientCertificateSecretRef"`
	// KeyFilePasswordSecret references a Secret with the password (key: "keyFilePassword") that
	// decrypts the password-encrypted client private key. Omit when the key is not encrypted.
	// +optional
	KeyFilePasswordSecret corev1.LocalObjectReference `json:"keyFilePasswordSecretRef,omitempty"`
}

type ExternalMongoDBSource struct {
	// HostAndPorts is the list of mongod host:port seeds for replica set sources.
	// Mutually exclusive with Sharded.
	// +optional
	HostAndPorts []string `json:"hostAndPorts,omitempty"`
	// ShardedCluster contains configuration for external sharded MongoDB clusters.
	// Mutually exclusive with HostAndPorts.
	// +optional
	ShardedCluster *ExternalShardedClusterConfig `json:"shardedCluster,omitempty"`
	// mongod keyfile used to connect to the external MongoDB deployment
	// +optional
	KeyFileSecretKeyRef *userv1.SecretKeyRef `json:"keyfileSecretRef,omitempty"`
	// TLS configuration for the external MongoDB deployment
	// +optional
	TLS *ExternalMongodTLS `json:"tls,omitempty"`
}

// ExternalShardedClusterConfig contains configuration for external sharded MongoDB clusters
type ExternalShardedClusterConfig struct {
	// Router contains the mongos router configuration
	// +kubebuilder:validation:Required
	Router ExternalRouterConfig `json:"router"`
	// Shards is the list of shard configurations
	// +kubebuilder:validation:MinItems=1
	Shards []ExternalShardConfig `json:"shards"`
}

// ExternalRouterConfig contains configuration for mongos routers in an external sharded cluster
type ExternalRouterConfig struct {
	// Hosts is the list of mongos router endpoints (host:port)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Hosts []string `json:"hosts"`
}

// ExternalShardConfig contains configuration for a single shard in an external sharded cluster
type ExternalShardConfig struct {
	// ShardName is the logical shard name (e.g., "shard-0").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ShardName string `json:"shardName"`
	// Hosts is the list of mongod host:port seeds for this shard's replica set
	// +kubebuilder:validation:MinItems=1
	Hosts []string `json:"hosts"`
}

type ExternalMongodTLS struct {
	// CA is a reference to a ConfigMap containing the CA certificate that issued mongod's TLS certificate.
	// The CA certificate is expected to be PEM encoded and available at the "ca.crt" key.
	CA *corev1.LocalObjectReference `json:"ca"`
}

type Security struct {
	// TLS configures TLS for the MongoDB Search server.
	// +optional
	TLS *TLS `json:"tls,omitempty"`
}

type TLS struct {
	// CertificateKeySecret is a reference to a Secret containing a private key and certificate to use for TLS.
	// The key and cert are expected to be PEM encoded and available at "tls.key" and "tls.crt".
	// This is the same format used for the standard "kubernetes.io/tls" Secret type, but no specific type is required.
	// If both CertificateKeySecret.Name and CertsSecretPrefix are specified, CertificateKeySecret.Name takes precedence.
	// +optional
	CertificateKeySecret corev1.LocalObjectReference `json:"certificateKeySecretRef,omitempty"`
	// CertsSecretPrefix is a prefix used to derive the TLS secret name.
	// When set, the operator will look for a secret named "{prefix}-{resourceName}-search-cert".
	// If CertificateKeySecret.Name is also specified, that takes precedence over this field.
	// +optional
	CertsSecretPrefix string `json:"certsSecretPrefix,omitempty"`
	// KeyFilePasswordSecret references a Secret with the password (key: "keyFilePassword") that
	// decrypts the password-encrypted server private key. Omit when the key is not encrypted.
	// +optional
	KeyFilePasswordSecret corev1.LocalObjectReference `json:"keyFilePasswordSecretRef,omitempty"`
}

// LoadBalancerStatus reports the state of the operator-managed load balancer (Envoy).
// Phase is the worst-of phase across all per-cluster Envoy reconciles.
type LoadBalancerStatus struct {
	Phase   status.Phase `json:"phase"`
	Message string       `json:"message,omitempty"`
}

// MetricsForwarderStatus reports the state of the metrics forwarder.
type MetricsForwarderStatus struct {
	Phase   status.Phase `json:"phase"`
	Message string       `json:"message,omitempty"`
}

// ClusterStatus reports one member cluster's search + LB + metrics forwarder state.
// +k8s:deepcopy-gen=true
type ClusterStatus struct {
	// Name is the member cluster name; empty in single-cluster deployments.
	// +optional
	Name string `json:"name,omitempty"`
	// Index is the spec.clusters[] pinned index so the status entries map back to their spec
	// entry independently of list order.
	Index int `json:"index"`
	// Search is the worst-of phase of this cluster's mongot StatefulSet(s).
	// +optional
	Search status.Phase `json:"search,omitempty"`
	// SearchMessage contains the reason when Search is not Running.
	// +optional
	SearchMessage string `json:"searchMessage,omitempty"`
	// LoadBalancer is this cluster's managed Envoy load balancer phase; empty when no managed LB.
	// +optional
	LoadBalancer status.Phase `json:"loadBalancer,omitempty"`
	// LoadBalancerMessage contains reason when LoadBalancer is not Running.
	// +optional
	LoadBalancerMessage string `json:"loadBalancerMessage,omitempty"`
	// MetricsForwarder is this cluster's Ops Manager metrics-forwarder Deployment phase;
	// empty when the metrics forwarder is not enabled.
	// +optional
	MetricsForwarder status.Phase `json:"metricsForwarder,omitempty"`
	// MetricsForwarderMessage contians the reason when metrics-forwarder is not Running.
	// +optional
	MetricsForwarderMessage string `json:"metricsForwarderMessage,omitempty"`
}

// Top level Phase field is considered `Running` only when Search STSs and LoadBalancer (status.LoadBalancer) is running.
type MongoDBSearchStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
	// LoadBalancer reports the state of the operator-managed load balancer.
	// Only populated when spec.clusters[].loadBalancer.managed is set.
	// +optional
	LoadBalancer *LoadBalancerStatus `json:"loadBalancer,omitempty"`
	// MetricsForwarder reports the state of the Ops Manager metrics forwarder.
	// +optional
	MetricsForwarder *MetricsForwarderStatus `json:"metricsForwarder,omitempty"`
	// Clusters reports per-cluster search + load balancer + metrics forwarder state across the topology. In
	// single-cluster and operator per cluster deployments the list has exactly one entry.
	// +optional
	// +listType=map
	// +listMapKey=index
	Clusters []ClusterStatus `json:"clusters,omitempty"`
}

// +k8s:deepcopy-gen=true
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the MongoDB deployment."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",description="MongoDB Search version reconciled by the operator."
// +kubebuilder:printcolumn:name="LoadBalancer",type="string",JSONPath=".status.loadBalancer.phase",description="Current state of the managed load balancer."
// +kubebuilder:printcolumn:name="MetricsForwarder",type="string",JSONPath=".status.metricsForwarder.phase",description="Current state of the Ops Manager metrics forwarder."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDB resource was created."
// +kubebuilder:resource:path=mongodbsearch,scope=Namespaced,shortName=mdbs
type MongoDBSearch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MongoDBSearchSpec `json:"spec"`
	// +optional
	Status MongoDBSearchStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBSearchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBSearch `json:"items"`
}

func (s *MongoDBSearch) GetCommonStatus(options ...status.Option) *status.Common {
	return &s.Status.Common
}

func (s *MongoDBSearch) GetStatus(options ...status.Option) interface{} {
	if partOpt, exists := status.GetOption(options, SearchPartOption{}); exists {
		switch partOpt.(SearchPartOption).Part {
		case SearchPartLoadBalancer:
			return s.Status.LoadBalancer
		case SearchPartMetricsForwarder:
			return s.Status.MetricsForwarder
		}
	}
	return s.Status
}

func (s *MongoDBSearch) GetStatusPath(options ...status.Option) string {
	if partOpt, exists := status.GetOption(options, SearchPartOption{}); exists {
		switch partOpt.(SearchPartOption).Part {
		case SearchPartLoadBalancer:
			return "/status/loadBalancer"
		case SearchPartMetricsForwarder:
			return "/status/metricsForwarder"
		}
	}
	return "/status"
}

func (s *MongoDBSearch) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	s.Status.Warnings = warnings
}

func (s *MongoDBSearch) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	if partOpt, exists := status.GetOption(statusOptions, SearchPartOption{}); exists {
		switch partOpt.(SearchPartOption).Part {
		case SearchPartLoadBalancer:
			s.updateLoadBalancerStatus(phase, statusOptions...)
			return
		case SearchPartMetricsForwarder:
			s.updateMetricsForwarderStatus(phase, statusOptions...)
			return
		}
	}

	s.Status.UpdateCommonFields(phase, s.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		s.Status.Warnings = append(s.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}
	if option, exists := status.GetOption(statusOptions, MongoDBSearchVersionOption{}); exists {
		s.Status.Version = option.(MongoDBSearchVersionOption).Version
	}
	// The search controller is the sole writer of status.clusters. Every reconcile it
	// rebuilds the whole list by reading the current StatefulSet/Deployment objects and
	// replaces status.clusters wholesale — it never read-modify-writes the previously
	// persisted status. A nil slice is valid and clears stale entries.
	if option, exists := status.GetOption(statusOptions, MongoDBSearchClusterStatusesOption{}); exists {
		s.Status.Clusters = option.(MongoDBSearchClusterStatusesOption).Statuses
	}
}

func (s *MongoDBSearch) updateLoadBalancerStatus(phase status.Phase, statusOptions ...status.Option) {
	if s.Status.LoadBalancer == nil {
		s.Status.LoadBalancer = &LoadBalancerStatus{}
	}
	s.Status.LoadBalancer.Phase = phase
	s.Status.LoadBalancer.Message = ""
	if option, exists := status.GetOption(statusOptions, status.MessageOption{}); exists {
		s.Status.LoadBalancer.Message = option.(status.MessageOption).Message
	}
}

func (s *MongoDBSearch) updateMetricsForwarderStatus(phase status.Phase, statusOptions ...status.Option) {
	if s.Status.MetricsForwarder == nil {
		s.Status.MetricsForwarder = &MetricsForwarderStatus{}
	}
	s.Status.MetricsForwarder.Phase = phase
	s.Status.MetricsForwarder.Message = ""
	if option, exists := status.GetOption(statusOptions, status.MessageOption{}); exists {
		s.Status.MetricsForwarder.Message = option.(status.MessageOption).Message
	}
}

// GetMinMongotReadyReplicasForRouting returns the minimum number of ready mongot
// replicas required to consider a mongot group ready for envoy to route real traffic to a shard's mongot group.
func (s *MongoDBSearch) GetMinMongotReadyReplicasForRouting() int32 {
	if lb := s.firstClusterLB(); lb != nil && lb.Managed != nil && lb.Managed.MinMongotReadyReplicas != nil {
		return *lb.Managed.MinMongotReadyReplicas
	}
	return 1
}

func (s *MongoDBSearch) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name, Namespace: s.Namespace}
}

func (s *MongoDBSearch) SearchServiceNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-svc", Namespace: s.Namespace}
}

// ProxyServiceNamespacedName returns the stable proxy Service name for ReplicaSet topologies.
func (s *MongoDBSearch) ProxyServiceNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-0-" + ProxyServiceSuffix, Namespace: s.Namespace}
}

// Per-cluster proxy Service. Targeted by mongod in RS-MC, by mongos in sharded-MC
// (shard-scoped traffic flows through ProxyServiceNameForClusterShard).
func (s *MongoDBSearch) ProxyServiceNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-%s", s.Name, clusterIndex, ProxyServiceSuffix),
		Namespace: s.Namespace,
	}
}

// ProxyServiceNameForClusterShard returns the proxy Service name for a specific (cluster, shard) pair.
func (s *MongoDBSearch) ProxyServiceNameForClusterShard(clusterIndex int, shardName string) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-%s-%s", s.Name, clusterIndex, shardName, ProxyServiceSuffix),
		Namespace: s.Namespace,
	}
}

func (s *MongoDBSearch) MongotConfigConfigMapNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-config", Namespace: s.Namespace}
}

// MongotConfigConfigMapNameForCluster returns the per-cluster mongot ConfigMap name.
func (s *MongoDBSearch) MongotConfigConfigMapNameForCluster(clusterIndex int) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-config", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}

// StatefulSetNamespacedNameForCluster returns the index-suffixed StatefulSet name for one member cluster.
func (s *MongoDBSearch) StatefulSetNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}

// SearchServiceNamespacedNameForCluster returns the index-suffixed headless
// Service name; the unindexed name is single-cluster-only.
func (s *MongoDBSearch) SearchServiceNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-svc", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}

func (s *MongoDBSearch) SourceUserPasswordSecretRef() *userv1.SecretKeyRef {
	var syncUserPasswordSecretKey *userv1.SecretKeyRef
	if s.Spec.Source != nil && s.Spec.Source.PasswordSecretRef != nil {
		syncUserPasswordSecretKey = s.Spec.Source.PasswordSecretRef
	} else {
		syncUserPasswordSecretKey = &userv1.SecretKeyRef{
			Name: fmt.Sprintf("%s-%s-password", s.Name, s.SourceUsername()),
		}
	}

	if syncUserPasswordSecretKey.Key == "" {
		syncUserPasswordSecretKey.Key = "password"
	}

	return syncUserPasswordSecretKey
}

func (s *MongoDBSearch) SourceUsername() string {
	if s.Spec.Source != nil && s.Spec.Source.Username != nil {
		return *s.Spec.Source.Username
	}

	return MongotDefaultSyncSourceUsername
}

func (s *MongoDBSearch) StatefulSetNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search", Namespace: s.Namespace}
}

func (s *MongoDBSearch) GetOwnerReferences() []metav1.OwnerReference {
	ownerReference := *metav1.NewControllerRef(s, schema.GroupVersionKind{
		Group:   v1.SchemeGroupVersion.Group,
		Version: v1.SchemeGroupVersion.Version,
		Kind:    s.Kind,
	})
	return []metav1.OwnerReference{ownerReference}
}

func (s *MongoDBSearch) GetMongoDBResourceRef() *userv1.MongoDBResourceRef {
	if s.IsExternalMongoDBSource() {
		return nil
	}

	mdbResourceRef := userv1.MongoDBResourceRef{Namespace: s.Namespace, Name: s.Name}
	if s.Spec.Source != nil && s.Spec.Source.MongoDBResourceRef != nil && s.Spec.Source.MongoDBResourceRef.Name != "" {
		mdbResourceRef.Name = s.Spec.Source.MongoDBResourceRef.Name
	}

	return &mdbResourceRef
}

func (s *MongoDBSearch) GetMongotWireprotoPort() int32 {
	return MongotDefaultWireprotoPort
}

func (s *MongoDBSearch) GetMongotGrpcPort() int32 {
	return MongotDefaultGrpcPort
}

// TLSSecretNamespacedName will get the namespaced name of the Secret containing the server certificate and key.
// Precedence:
//  1. CertificateKeySecret.Name - explicit secret name
//  2. CertsSecretPrefix - uses pattern {prefix}-{resourceName}-search-cert
//  3. Default - uses pattern {resourceName}-search-cert
func (s *MongoDBSearch) TLSSecretNamespacedName() types.NamespacedName {
	// Explicit name takes precedence
	if s.Spec.Security.TLS.CertificateKeySecret.Name != "" {
		return types.NamespacedName{Name: s.Spec.Security.TLS.CertificateKeySecret.Name, Namespace: s.Namespace}
	}

	// Prefix-based naming: {prefix}-{resourceName}-search-cert
	if s.Spec.Security.TLS.CertsSecretPrefix != "" {
		secretName := fmt.Sprintf("%s-%s-search-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name)
		return types.NamespacedName{Name: secretName, Namespace: s.Namespace}
	}

	// Default naming: {resourceName}-search-cert
	secretName := fmt.Sprintf("%s-search-cert", s.Name)
	return types.NamespacedName{Name: secretName, Namespace: s.Namespace}
}

// TLSOperatorSecretNamespacedName will get the namespaced name of the Secret created by the operator
// containing the combined certificate and key.
func (s *MongoDBSearch) TLSOperatorSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-certificate-key", Namespace: s.Namespace}
}

func (s *MongoDBSearch) CertificateKeySecretName() bool {
	if s.Spec.Security.TLS == nil {
		return false
	}
	return s.Spec.Security.TLS.CertificateKeySecret.Name != ""
}

// TLSSecretForClusterShard returns the namespaced name of the TLS source secret for a specific (cluster, shard) pair.
// Naming pattern:
//   - With prefix: {prefix}-{name}-search-{clusterIndex}-{shardName}-cert
//   - Without prefix: {name}-search-{clusterIndex}-{shardName}-cert
func (s *MongoDBSearch) TLSSecretForClusterShard(clusterIndex int, shardName string) types.NamespacedName {
	var secretName string
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		secretName = fmt.Sprintf("%s-%s-search-%d-%s-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name, clusterIndex, shardName)
	} else {
		secretName = fmt.Sprintf("%s-search-%d-%s-cert", s.Name, clusterIndex, shardName)
	}
	return types.NamespacedName{Name: secretName, Namespace: s.Namespace}
}

// TLSOperatorSecretForClusterShard returns the operator-managed combined-cert+key
// Secret name for a (cluster, shard) pair: {name}-search-{clusterIndex}-{shardName}-certificate-key.
func (s *MongoDBSearch) TLSOperatorSecretForClusterShard(clusterIndex int, shardName string) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-%s-certificate-key", s.Name, clusterIndex, shardName),
		Namespace: s.Namespace,
	}
}

// IsTLSConfigured returns true if TLS is enabled (TLS struct is present)
func (s *MongoDBSearch) IsTLSConfigured() bool {
	return s.Spec.Security.TLS != nil
}

// IsX509Auth returns true if x509 client certificate authentication is configured for the sync source.
func (s *MongoDBSearch) IsX509Auth() bool {
	return s.Spec.Source != nil && s.Spec.Source.X509 != nil
}

// X509ClientCertSecret returns the namespaced name of the user-provided x509 client certificate secret.
func (s *MongoDBSearch) X509ClientCertSecret() types.NamespacedName {
	if !s.IsX509Auth() {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: s.Spec.Source.X509.ClientCertificateSecret.Name, Namespace: s.Namespace}
}

// X509OperatorManagedSecret returns the namespaced name of the operator-managed secret
// containing the combined x509 client certificate and key.
func (s *MongoDBSearch) X509OperatorManagedSecret() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-x509-client-cert", Namespace: s.Namespace}
}

// HasScramClientCert returns true if a TLS client certificate is configured for the SCRAM connection.
func (s *MongoDBSearch) HasScramClientCert() bool {
	return s.Spec.Source != nil && s.Spec.Source.TLS != nil &&
		s.Spec.Source.TLS.ClientCertificateSecret.Name != ""
}

// ScramClientCertSecret returns the namespaced name of the user-provided SCRAM TLS client certificate secret.
func (s *MongoDBSearch) ScramClientCertSecret() types.NamespacedName {
	if !s.HasScramClientCert() {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: s.Spec.Source.TLS.ClientCertificateSecret.Name, Namespace: s.Namespace}
}

// GrpcKeyFilePasswordSecret returns the dedicated secret holding the password that decrypts the
// gRPC server private key, or an empty name when unset.
func (s *MongoDBSearch) GrpcKeyFilePasswordSecret() types.NamespacedName {
	if s.Spec.Security.TLS == nil || s.Spec.Security.TLS.KeyFilePasswordSecret.Name == "" {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: s.Spec.Security.TLS.KeyFilePasswordSecret.Name, Namespace: s.Namespace}
}

// X509KeyFilePasswordSecret returns the dedicated secret holding the password that decrypts the
// x509 client private key, or an empty name when unset.
func (s *MongoDBSearch) X509KeyFilePasswordSecret() types.NamespacedName {
	if !s.IsX509Auth() || s.Spec.Source.X509.KeyFilePasswordSecret.Name == "" {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: s.Spec.Source.X509.KeyFilePasswordSecret.Name, Namespace: s.Namespace}
}

// ScramKeyFilePasswordSecret returns the dedicated secret holding the password that decrypts the
// scram client private key, or an empty name when unset.
func (s *MongoDBSearch) ScramKeyFilePasswordSecret() types.NamespacedName {
	if !s.HasScramClientCert() || s.Spec.Source.TLS.KeyFilePasswordSecret.Name == "" {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: s.Spec.Source.TLS.KeyFilePasswordSecret.Name, Namespace: s.Namespace}
}

// ScramClientCertOperatorManagedSecret returns the namespaced name of the operator-managed secret
// containing the combined SCRAM TLS client certificate and key.
func (s *MongoDBSearch) ScramClientCertOperatorManagedSecret() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-scram-client-cert", Namespace: s.Namespace}
}

func (s *MongoDBSearch) GetMongotHealthCheckPort() int32 {
	return MongotDefautHealthCheckPort
}

func (s *MongoDBSearch) IsExternalMongoDBSource() bool {
	return s.Spec.Source != nil && s.Spec.Source.ExternalMongoDBSource != nil
}

// IsExternalSourceSharded returns true if the source is an external sharded MongoDB cluster
func (s *MongoDBSearch) IsExternalSourceSharded() bool {
	return s.IsExternalMongoDBSource() &&
		s.Spec.Source.ExternalMongoDBSource.ShardedCluster != nil
}

func (s *MongoDBSearch) GetLogLevel() mdb.LogLevel {
	if s.Spec.LogLevel == "" {
		return "INFO"
	}

	return s.Spec.LogLevel
}

// mongot configuration defaults to the gRPC server. on rare occasions we might advise users to enable the legacy
// wireproto server. Once the deprecated wireproto server is removed, this function, annotation, and all code guarded
// by this check should be removed.
func (s *MongoDBSearch) IsWireprotoEnabled() bool {
	val, ok := s.Annotations[ForceWireprotoAnnotation]
	return ok && val == "true"
}

func (s *MongoDBSearch) GetEffectiveMongotPort() int32 {
	if s.IsWireprotoEnabled() {
		return s.GetMongotWireprotoPort()
	}
	return s.GetMongotGrpcPort()
}

// firstClusterLB returns the first cluster's loadBalancer. Validation enforces
// that every cluster sets the same mode (or none sets one), so the first entry
// answers deployment-wide mode questions.
func (s *MongoDBSearch) firstClusterLB() *LoadBalancerConfig {
	if len(s.Spec.Clusters) == 0 {
		return nil
	}
	return s.Spec.Clusters[0].LoadBalancer
}

func (s *MongoDBSearch) IsLBModeUnmanaged() bool {
	lb := s.firstClusterLB()
	return lb != nil && lb.Unmanaged != nil
}

// IsReplicaSetUnmanagedLB returns true if this is a ReplicaSet with unmanaged LB configuration.
// An endpoint with a template placeholder ({shardName}) is NOT considered a ReplicaSet endpoint.
func (s *MongoDBSearch) IsReplicaSetUnmanagedLB() bool {
	return s.IsLBModeUnmanaged() &&
		s.firstClusterLB().Unmanaged.Endpoint != "" &&
		!s.HasEndpointTemplate()
}

// GetUnmanagedLBEndpoint returns the unmanaged endpoint, or "" when no
// unmanaged LB is configured. Unmanaged LB is single-cluster-only
// (validateMCRequiresManagedLB), so the first cluster is the only one.
func (s *MongoDBSearch) GetUnmanagedLBEndpoint() string {
	lb := s.firstClusterLB()
	if lb == nil || lb.Unmanaged == nil {
		return ""
	}
	return lb.Unmanaged.Endpoint
}

// HasEndpointTemplate returns true if the unmanaged endpoint contains the
// {shardName} template placeholder.
func (s *MongoDBSearch) HasEndpointTemplate() bool {
	return strings.Contains(s.GetUnmanagedLBEndpoint(), ShardNamePlaceholder)
}

// IsShardedUnmanagedLB returns true if this is a sharded unmanaged LB configuration
// identified by the presence of the {shardName} template placeholder in the endpoint.
func (s *MongoDBSearch) IsShardedUnmanagedLB() bool {
	return s.IsLBModeUnmanaged() && s.HasEndpointTemplate()
}

// GetEndpointForShard returns the unmanaged endpoint for a specific shard by
// substituting the {shardName} placeholder.
func (s *MongoDBSearch) GetEndpointForShard(shardName string) string {
	if !s.IsShardedUnmanagedLB() {
		return ""
	}
	return strings.ReplaceAll(s.GetUnmanagedLBEndpoint(), ShardNamePlaceholder, shardName)
}

// EffectiveClusters returns the per-cluster distribution slice the reconcile
// loop iterates over: spec.clusters[] as authored. Sizing has a single home (the
// per-cluster entry) with no cross-tier cascade, so this is an identity accessor.
func (s *MongoDBSearch) EffectiveClusters() []ClusterSpec {
	return s.Spec.Clusters
}

// EffectiveClusterFor returns the ClusterSpec for the named cluster.
// Empty clusterName returns the first entry (single-cluster case, where
// clusterName may be omitted). Returns an error if the named cluster is not
// found in spec.clusters[].
func (s *MongoDBSearch) EffectiveClusterFor(clusterName string) (ClusterSpec, error) {
	clusters := s.Spec.Clusters
	if clusterName == "" {
		if len(clusters) > 0 {
			return clusters[0], nil
		}
		return ClusterSpec{}, errors.New("no clusters are configured in spec.clusters")
	}
	for _, c := range clusters {
		if c.Name == clusterName {
			return c, nil
		}
	}
	return ClusterSpec{}, fmt.Errorf("cluster %q not found in spec.clusters", clusterName)
}

// ResolveSizingForClusterShard returns the effective sizing for one
// (cluster, shard) cell: the named cluster's ClusterSpec with the matching
// shardOverride (if any) layered on top. Replicas, ResourceRequirements,
// Persistence, NodeAffinity and JVMFlags are replaced when the override sets them;
// StatefulSetConfiguration is deep-merged onto the cluster value. An empty
// shardName (replica-set sources) or a cluster without a matching override
// returns the cluster spec.
func (s *MongoDBSearch) ResolveSizingForClusterShard(clusterName, shardName string) (ClusterSpec, error) {
	resolved, err := s.EffectiveClusterFor(clusterName)
	if err != nil {
		return ClusterSpec{}, err
	}
	// The returned spec is a flat per-(cluster,shard) sizing; drop the override
	// list so callers never re-layer it.
	overrides := resolved.ShardOverrides
	resolved.ShardOverrides = nil
	if shardName == "" {
		return resolved, nil
	}
	override := findShardOverride(overrides, shardName)
	if override == nil {
		return resolved, nil
	}
	if override.Replicas != nil {
		resolved.Replicas = override.Replicas
	}
	if override.ResourceRequirements != nil {
		resolved.ResourceRequirements = override.ResourceRequirements
	}
	if override.Persistence != nil {
		resolved.Persistence = override.Persistence
	}
	if override.NodeAffinity != nil {
		resolved.NodeAffinity = override.NodeAffinity
	}
	if len(override.JVMFlags) > 0 {
		resolved.JVMFlags = override.JVMFlags
	}
	if override.StatefulSetConfiguration != nil {
		resolved.StatefulSetConfiguration = mergeStatefulSetConfiguration(resolved.StatefulSetConfiguration, override.StatefulSetConfiguration)
	}
	return resolved, nil
}

// findShardOverride returns the override whose ShardNames contains shardName, or nil.
func findShardOverride(overrides []ShardOverride, shardName string) *ShardOverride {
	for i := range overrides {
		for _, name := range overrides[i].ShardNames {
			if name == shardName {
				return &overrides[i]
			}
		}
	}
	return nil
}

// mergeStatefulSetConfiguration deep-merges the override StatefulSetConfiguration
// onto base (override wins per field). The result never aliases the inputs, so
// callers may mutate it without touching the CR spec.
func mergeStatefulSetConfiguration(base, override *v1.StatefulSetConfiguration) *v1.StatefulSetConfiguration {
	if base == nil {
		return override.DeepCopy()
	}
	merged := base.DeepCopy()
	merged.SpecWrapper.Spec = merge.StatefulSetSpecs(merged.SpecWrapper.Spec, override.SpecWrapper.Spec)
	merged.MetadataWrapper.Labels = merge.StringToStringMap(merged.MetadataWrapper.Labels, override.MetadataWrapper.Labels)
	merged.MetadataWrapper.Annotations = merge.StringToStringMap(merged.MetadataWrapper.Annotations, override.MetadataWrapper.Annotations)
	return merged
}

// HasMultipleReplicas reports whether any cluster runs more than one mongot
// replica, the trigger for requiring a load balancer.
func (s *MongoDBSearch) HasMultipleReplicas() bool {
	return s.MaxReplicasAcrossClusters() > 1
}

// MaxReplicasAcrossClusters returns the largest resolved mongot replica count
// across every (cluster, shard) cell, accounting for per-shard overrides
// (clusters default to 1 when Replicas is unset).
func (s *MongoDBSearch) MaxReplicasAcrossClusters() int {
	highest := 0
	consider := func(replicas int) {
		if replicas > highest {
			highest = replicas
		}
	}
	for _, c := range s.Spec.Clusters {
		consider(c.ReplicasOrDefault())
		for _, o := range c.ShardOverrides {
			if o.Replicas != nil {
				consider(int(*o.Replicas))
			}
		}
	}
	return highest
}

// HasAutoEmbedding returns true when auto-embedding is configured.
func (s *MongoDBSearch) HasAutoEmbedding() bool {
	return s.Spec.AutoEmbedding != nil
}

func (s *MongoDBSearch) MongotStatefulSetForClusterShard(clusterIndex int, shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-%d-%s", s.Name, clusterIndex, shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) MongotServiceForClusterShard(clusterIndex int, shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-%d-%s-svc", s.Name, clusterIndex, shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) MongotConfigMapForClusterShard(clusterIndex int, shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-%d-%s-config", s.Name, clusterIndex, shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) IsLBModeManaged() bool {
	lb := s.firstClusterLB()
	return lb != nil && lb.Managed != nil
}

// GetManagedLBForCluster returns the named cluster's loadBalancer.managed, or
// nil when the cluster is unknown or has no managed LB. An empty clusterName
// means the first cluster (single-cluster installs). Resolution is by name,
// never by index: the pin is for resource naming only and spec entries
// are looked up by name.
func (s *MongoDBSearch) GetManagedLBForCluster(clusterName string) *ManagedLBConfig {
	c, err := s.EffectiveClusterFor(clusterName)
	if err != nil || c.LoadBalancer == nil {
		return nil
	}
	return c.LoadBalancer.Managed
}

// GetManagedLBEndpointForCluster returns the named cluster's externalHostname.
// {shardName} may remain in the result; use GetManagedLBEndpointForClusterShard
// when it needs resolving. Returns "" when the cluster has no managed LB.
func (s *MongoDBSearch) GetManagedLBEndpointForCluster(clusterName string) string {
	cfg := s.GetManagedLBForCluster(clusterName)
	if cfg == nil {
		return ""
	}
	return cfg.ExternalHostname
}

// GetManagedLBEndpointForClusterShard returns the named cluster's externalHostname
// with {shardName} resolved. Used for sharded MongoDBSearch deployments.
func (s *MongoDBSearch) GetManagedLBEndpointForClusterShard(clusterName, shardName string) string {
	out := s.GetManagedLBEndpointForCluster(clusterName)
	if out == "" {
		return ""
	}
	return strings.ReplaceAll(out, ShardNamePlaceholder, shardName)
}

// GetRouterHostnameForCluster returns the named cluster's shard-agnostic cluster-level (mongos)
// hostname from loadBalancer.managed.routerHostname. Used verbatim (no {shardName} trimming).
// Returns "" when the cluster has no managed LB or the field is unset.
func (s *MongoDBSearch) GetRouterHostnameForCluster(clusterName string) string {
	cfg := s.GetManagedLBForCluster(clusterName)
	if cfg == nil {
		return ""
	}
	return cfg.RouterHostname
}

// IsLoadBalancerReady returns true if managed LB is not configured,
// or if it is configured and its status phase is Running.
func (s *MongoDBSearch) IsLoadBalancerReady() bool {
	if !s.IsLBModeManaged() {
		return true
	}
	return s.Status.LoadBalancer != nil && s.Status.LoadBalancer.Phase == status.PhaseRunning
}

// LoadBalancerDeploymentName returns the name of the managed Envoy Deployment for this resource.
func (s *MongoDBSearch) LoadBalancerDeploymentName() string {
	return s.Name + "-search-lb"
}

// LoadBalancerConfigMapName returns the name of the managed Envoy ConfigMap for this resource.
func (s *MongoDBSearch) LoadBalancerConfigMapName() string {
	return s.Name + "-search-lb-config"
}

// LoadBalancerDeploymentNameForCluster returns the name of the managed Envoy
// Deployment for one member cluster. The cluster index (from the persisted
// StateStore mapping) is appended so per-cluster Deployments in the same
// namespace stay distinct without encoding user-supplied cluster names into
// resource names (name length is checked at admission via
// validateClustersEnvoyResourceNames).
func (s *MongoDBSearch) LoadBalancerDeploymentNameForCluster(clusterIndex int) string {
	return fmt.Sprintf("%s-%d", s.LoadBalancerDeploymentName(), clusterIndex)
}

// LoadBalancerConfigMapNameForCluster returns the name of the managed Envoy
// ConfigMap for one member cluster. See LoadBalancerDeploymentNameForCluster.
func (s *MongoDBSearch) LoadBalancerConfigMapNameForCluster(clusterIndex int) string {
	return fmt.Sprintf("%s-%d-config", s.LoadBalancerDeploymentName(), clusterIndex)
}

// LoadBalancerServerCert returns the namespaced name of the TLS server certificate secret for the
// managed Envoy LB of one member cluster. The cluster index matches the Envoy
// Deployment name so the cert that fronts a cluster's Envoy is unambiguous.
// Naming pattern:
//   - With prefix: {prefix}-{name}-search-lb-{clusterIndex}-cert
//   - Without prefix: {name}-search-lb-{clusterIndex}-cert
func (s *MongoDBSearch) LoadBalancerServerCert(clusterIndex int) types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-%d-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name, clusterIndex),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-%d-cert", s.Name, clusterIndex), Namespace: s.Namespace}
}

// LoadBalancerServerCertForClusterShard returns the namespaced name of the TLS server certificate secret for
// a specific (cluster, shard) pair of the managed Envoy LB. Naming pattern:
//   - With prefix: {prefix}-{name}-search-lb-{clusterIndex}-{shardName}-cert
//   - Without prefix: {name}-search-lb-{clusterIndex}-{shardName}-cert
func (s *MongoDBSearch) LoadBalancerServerCertForClusterShard(clusterIndex int, shardName string) types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-%d-%s-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name, clusterIndex, shardName),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-%d-%s-cert", s.Name, clusterIndex, shardName), Namespace: s.Namespace}
}

// LoadBalancerClientCert returns the namespaced name of the TLS client certificate secret used by the
// managed Envoy LB of one member cluster to authenticate with mongot backends.
// The cluster index matches the Envoy Deployment name. Naming pattern:
//   - With prefix: {prefix}-{name}-search-lb-{clusterIndex}-client-cert
//   - Without prefix: {name}-search-lb-{clusterIndex}-client-cert
func (s *MongoDBSearch) LoadBalancerClientCert(clusterIndex int) types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-%d-client-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name, clusterIndex),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-%d-client-cert", s.Name, clusterIndex), Namespace: s.Namespace}
}

// ObjectKey implements v1.ResourceOwner.
func (s *MongoDBSearch) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(s.Namespace, s.Name)
}

// GetOwnerLabels implements v1.ResourceOwner. Returns labels used to identify
// the state ConfigMap owned by this MongoDBSearch.
func (s *MongoDBSearch) GetOwnerLabels() map[string]string {
	return map[string]string{
		util.OperatorLabelName: util.OperatorLabelValue,
		LabelResourceOwner:     s.Name,
	}
}

// GetKind implements v1.ObjectOwner.
func (s *MongoDBSearch) GetKind() string {
	return "MongoDBSearch"
}

// ValidateOperatorPerClusterIndices requires a non-empty spec.clusters where every entry pins
// a distinct Index. Called only by operators in operator-per-cluster with unified CR mode (operatorClusterName set).
func (s *MongoDBSearch) ValidateOperatorPerClusterIndices() error {
	if len(s.Spec.Clusters) == 0 {
		return fmt.Errorf("running one operator per cluster requires spec.clusters to be set")
	}
	seen := make(map[int32]string, len(s.Spec.Clusters))
	for _, c := range s.Spec.Clusters {
		if c.Index == nil {
			return fmt.Errorf("running one operator per cluster requires index on every spec.clusters[] entry (missing on %q)", c.Name)
		}
		if prev, dup := seen[*c.Index]; dup {
			return fmt.Errorf("index %d is set on more than one spec.clusters[] entry (%q and %q); pinned indices must be distinct", *c.Index, prev, c.Name)
		}
		seen[*c.Index] = c.Name
	}
	return nil
}

// LocalizeToCluster narrows spec.Clusters to the entry matching name. Returns false if absent (different operator owns this CR).
func (s *MongoDBSearch) LocalizeToCluster(name string) bool {
	if len(s.Spec.Clusters) == 0 {
		return true
	}
	for _, c := range s.Spec.Clusters {
		if c.Name == name {
			s.Spec.Clusters = []ClusterSpec{c}
			return true
		}
	}
	return false
}

// IsMetricsForwarderEnabled returns true if the metrics forwarder should be created.
// Mode Enabled: always returns true.
// Mode Disabled: always returns false.
// Mode Auto (default): returns true for internal MongoDB sources,
// or for external sources when both AgentCredentials and ProjectConfigMapRef are populated.
func (s *MongoDBSearch) IsMetricsForwarderEnabled() bool {
	switch s.Spec.Observability.MetricsForwarder.Mode {
	case MetricsForwarderModeEnabled:
		return true
	case MetricsForwarderModeDisabled:
		return false
	default: // Auto
		if !s.IsExternalMongoDBSource() {
			return true
		}
		return s.Spec.Observability.MetricsForwarder.OpsManager != nil
	}
}

func (s *MongoDBSearch) MetricsForwarderHasExplicitProjectConfig() bool {
	return s.Spec.Observability.MetricsForwarder.OpsManager != nil
}

// MetricsForwarderDeploymentName returns the name of the metrics forwarder Deployment.
func (s *MongoDBSearch) MetricsForwarderDeploymentName() string {
	return s.Name + "-search-metrics-forwarder"
}

// MetricsForwarderConfigMapName returns the name of the metrics forwarder config ConfigMap.
func (s *MongoDBSearch) MetricsForwarderConfigMapName() string {
	return s.Name + "-search-metrics-forwarder-config"
}

// MetricsForwarderDeploymentNameForCluster returns the per-cluster metrics forwarder Deployment name.
func (s *MongoDBSearch) MetricsForwarderDeploymentNameForCluster(clusterIndex int) string {
	return fmt.Sprintf("%s-search-metrics-forwarder-%d", s.Name, clusterIndex)
}

// MetricsForwarderConfigMapNameForCluster returns the per-cluster metrics forwarder config ConfigMap name.
func (s *MongoDBSearch) MetricsForwarderConfigMapNameForCluster(clusterIndex int) string {
	return fmt.Sprintf("%s-search-metrics-forwarder-%d-config", s.Name, clusterIndex)
}

// MetricsForwarderAgentKeySecretNameForCluster returns the per-cluster agent-key Secret name
// for the forwarder-owned replicated copy.
func (s *MongoDBSearch) MetricsForwarderAgentKeySecretNameForCluster(clusterIndex int) string {
	return fmt.Sprintf("%s-search-metrics-forwarder-%d-agent-key", s.Name, clusterIndex)
}

// MetricsForwarderCACertConfigMapNameForCluster returns the per-cluster OM CA ConfigMap name
// for the forwarder-owned replicated copy.
func (s *MongoDBSearch) MetricsForwarderCACertConfigMapNameForCluster(clusterIndex int) string {
	return fmt.Sprintf("%s-search-metrics-forwarder-%d-ca-cert", s.Name, clusterIndex)
}
