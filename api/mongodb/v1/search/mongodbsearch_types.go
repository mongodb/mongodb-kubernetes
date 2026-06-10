package search

import (
	"errors"
	"fmt"
	"strconv"
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
)

const (
	// ShardNamePlaceholder is the placeholder used in endpoint templates for sharded clusters
	ShardNamePlaceholder = "{shardName}"

	// ClusterNamePlaceholder is substituted with spec.clusters[i].ClusterName when
	// resolving spec.loadBalancer.managed.externalHostname for cluster i in
	// multi-cluster MongoDBSearch deployments. The Envoy reconciler substitutes the
	// member cluster name so per-cluster SNI hostnames stay distinct.
	ClusterNamePlaceholder = "{clusterName}"

	// ClusterIndexPlaceholder is substituted with the stable cluster-index for
	// spec.clusters[i]. The index is taken verbatim from the user pin; the persisted
	// mapping retains removed clusters' entries (see cluster_index.go).
	ClusterIndexPlaceholder = "{clusterIndex}"

	// LabelResourceOwner is the label key used to identify the MongoDBSearch CR that
	// owns a resource. Used as part of GetOwnerLabels for StateStore ConfigMap selection.
	LabelResourceOwner = "mongodb.com/v1.mongodbSearchResourceOwner"

	MongotDefaultWireprotoPort      int32 = 27027
	MongotDefaultGrpcPort           int32 = 27028
	MongotDefaultPrometheusPort     int32 = 9946
	MongotDefautHealthCheckPort     int32 = 8080
	EnvoyDefaultProxyPort           int32 = 27028
	MongotDefaultSyncSourceUsername       = "search-sync-source"

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

type Prometheus struct {
	// Port where metrics endpoint will be exposed on. Defaults to 9946.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9946
	Port int `json:"port,omitempty"`
}

func (p *Prometheus) GetPort() int32 {
	//nolint:gosec
	return int32(p.Port)
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
	// Configure prometheus metrics endpoint in mongot. If not set, the metrics endpoint will be disabled.
	// +optional
	Prometheus *Prometheus `json:"prometheus,omitempty"`
	// AutoEmbedding configures MongoDB Search to generate vector embeddings automatically
	// through an embedding model service. These values populate the `embedding` section of the mongot config.
	// +optional
	AutoEmbedding *EmbeddingConfig `json:"autoEmbedding,omitempty"`
	// LoadBalancer configures how mongod/mongos connect to mongot (Managed vs Unmanaged/BYO Load Balancer).
	// Top-level spec.loadBalancer.managed.* serves as the default for every entry in spec.clusters;
	// per-cluster overrides deep-merge into this template (see spec.clusters[].loadBalancer.managed).
	// spec.loadBalancer.unmanaged is top-level only — there is no per-cluster form.
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"loadBalancer,omitempty"`
	// FeatureFlags configures mongot feature flags. When a flag is set to true in the CR,
	// it is rendered into the mongot config YAML. When omitted or false, the flag is not
	// included in mongot config and mongot uses its built-in defaults.
	// +optional
	FeatureFlags *FeatureFlags `json:"featureFlags,omitempty"`
	// Clusters configures the deployment per Kubernetes cluster: one entry for a
	// single cluster (clusterName optional), or one entry per cluster for
	// multi-cluster (clusterName required, len > 1). This is the place to set
	// replicas, resources, storage, and StatefulSet overrides.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:XValidation:rule="size(self) <= 1 || self.all(c1, has(c1.clusterName) && self.exists_one(c2, has(c2.clusterName) && c2.clusterName == c1.clusterName))",message="clusters[].clusterName must be set and unique when more than one cluster is specified"
	// +kubebuilder:validation:XValidation:rule="self.all(c1, !has(c1.clusterIndex) || self.exists_one(c2, has(c2.clusterIndex) && c2.clusterIndex == c1.clusterIndex))",message="clusters[].clusterIndex must be unique when set"
	// +kubebuilder:validation:XValidation:rule="size(self) <= 1 || self.all(c, has(c.clusterIndex))",message="clusters[].clusterIndex is required on every entry when more than one cluster is specified"
	Clusters []ClusterSpec `json:"clusters"`
}

// SyncSourceSelector picks which mongods this cluster's mongot fleet syncs from.
// At-most-one of MatchTags or Hosts may be set.
// +kubebuilder:validation:XValidation:rule="!(has(self.matchTags) && has(self.hosts))",message="syncSourceSelector.matchTags and syncSourceSelector.hosts are mutually exclusive"
type SyncSourceSelector struct {
	// MatchTags selects which sync-source mongods to read from by their replica-set tags.
	// The operator passes these to mongot as readPreferenceTags.
	// +optional
	// +kubebuilder:validation:MaxProperties=50
	MatchTags map[string]string `json:"matchTags,omitempty"`
	// Hosts is an explicit list of host:port sync-source members.
	// Mutually exclusive with MatchTags.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MaxLength=253
	Hosts []string `json:"hosts,omitempty"`
}

// ClusterSpec is one entry in spec.clusters[]. ClusterName is required and immutable
// when len(spec.clusters) > 1; optional in the single-cluster case.
// Each field, when set, applies to this cluster; when unset, the operator's
// per-field default applies.
type ClusterSpec struct {
	// ClusterName is the Kubernetes cluster name. Required and immutable
	// when len(spec.clusters) > 1; optional in the single-cluster case.
	// MaxLength is 253 — the DNS subdomain limit Kubernetes cluster names follow.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ClusterName string `json:"clusterName,omitempty"`
	// ClusterIndex is the stable integer in per-cluster resource names. Required on every entry
	// of a multi-cluster spec, and even on a single entry when each member cluster runs its own
	// operator. Changing it renames this cluster's resources, orphaning those at the old index.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=999
	ClusterIndex *int32 `json:"clusterIndex,omitempty"`
	// Replicas is the number of mongot pods for this cluster's StatefulSet.
	// For ReplicaSet sources this is the total; for sharded sources it is per shard.
	// When Replicas > 1, a load balancer (spec.loadBalancer) is required to distribute
	// traffic across mongot instances.
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
	// StatefulSetConfiguration is applied to this cluster's mongot StatefulSet at the end of the
	// reconcile loop, for customizations not exposed as first-class fields.
	// +optional
	StatefulSetConfiguration *v1.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// +optional
	SyncSourceSelector *SyncSourceSelector `json:"syncSourceSelector,omitempty"`
	// LoadBalancer per-cluster override; deep-merged into spec.loadBalancer.managed.
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"loadBalancer,omitempty"`
	// JVMFlags overrides spec.jvmFlags for this cluster's mongot pods. Replace, not merge.
	// +optional
	JVMFlags []string `json:"jvmFlags,omitempty"`
}

// ReplicasOrDefault returns the cluster's mongot replica count, defaulting to 1
// when Replicas is unset. An explicit 0 is honored (used to take mongot offline).
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
	// In multi-cluster deployments, may contain a {clusterName} placeholder so per-cluster
	// SNI hostnames stay distinct.
	// Required when MongoDB is externally managed. Ignored for operator-managed MongoDB.
	// +optional
	ExternalHostname string `json:"externalHostname,omitempty"`
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
}

// X509Auth configures x509 client certificate authentication for mongot's sync source connection.
type X509Auth struct {
	// ClientCertificateSecret is a reference to a Secret containing the x509 client
	// certificate and key for authenticating to the MongoDB sync source.
	// Expected keys: "tls.crt", "tls.key" (required), "tls.keyFilePassword" (optional).
	// +kubebuilder:validation:Required
	ClientCertificateSecret corev1.LocalObjectReference `json:"clientCertificateSecretRef"`
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
	// CA is a reference to a Secret containing the CA certificate that issued mongod's TLS certificate.
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
}

// LoadBalancerStatus reports the state of the operator-managed load balancer (Envoy).
// Phase is the worst-of phase across all per-cluster Envoy reconciles.
type LoadBalancerStatus struct {
	Phase   status.Phase `json:"phase"`
	Message string       `json:"message,omitempty"`
}

type MongoDBSearchStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
	// LoadBalancer reports the state of the operator-managed load balancer.
	// Only populated when spec.loadBalancer.managed is set.
	// +optional
	LoadBalancer *LoadBalancerStatus `json:"loadBalancer,omitempty"`
}

// +k8s:deepcopy-gen=true
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the MongoDB deployment."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",description="MongoDB Search version reconciled by the operator."
// +kubebuilder:printcolumn:name="LoadBalancer",type="string",JSONPath=".status.loadBalancer.phase",description="Current state of the managed load balancer."
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
		if partOpt.(SearchPartOption).Part == SearchPartLoadBalancer {
			return s.Status.LoadBalancer
		}
	}
	return s.Status
}

func (s *MongoDBSearch) GetStatusPath(options ...status.Option) string {
	if partOpt, exists := status.GetOption(options, SearchPartOption{}); exists {
		if partOpt.(SearchPartOption).Part == SearchPartLoadBalancer {
			return "/status/loadBalancer"
		}
	}
	return "/status"
}

func (s *MongoDBSearch) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	s.Status.Warnings = warnings
}

func (s *MongoDBSearch) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	if partOpt, exists := status.GetOption(statusOptions, SearchPartOption{}); exists {
		if partOpt.(SearchPartOption).Part == SearchPartLoadBalancer {
			s.updateLoadBalancerStatus(phase, statusOptions...)
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

func (s *MongoDBSearch) GetPrometheus() *Prometheus {
	return s.Spec.Prometheus
}

func (s *MongoDBSearch) IsLBModeUnmanaged() bool {
	return s.Spec.LoadBalancer != nil && s.Spec.LoadBalancer.Unmanaged != nil
}

// IsReplicaSetUnmanagedLB returns true if this is a ReplicaSet with unmanaged LB configuration.
// An endpoint with a template placeholder ({shardName}) is NOT considered a ReplicaSet endpoint.
func (s *MongoDBSearch) IsReplicaSetUnmanagedLB() bool {
	return s.IsLBModeUnmanaged() &&
		s.Spec.LoadBalancer.Unmanaged.Endpoint != "" &&
		!s.HasEndpointTemplate()
}

func (s *MongoDBSearch) GetReplicaSetUnmanagedLBEndpoint() string {
	if !s.IsReplicaSetUnmanagedLB() {
		return ""
	}
	return s.Spec.LoadBalancer.Unmanaged.Endpoint
}

// HasEndpointTemplate returns true if the unmanaged endpoint contains the {shardName} template placeholder.
func (s *MongoDBSearch) HasEndpointTemplate() bool {
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Unmanaged == nil {
		return false
	}
	return strings.Contains(s.Spec.LoadBalancer.Unmanaged.Endpoint, ShardNamePlaceholder)
}

// IsShardedUnmanagedLB returns true if this is a sharded unmanaged LB configuration
// identified by the presence of the {shardName} template placeholder in the endpoint.
func (s *MongoDBSearch) IsShardedUnmanagedLB() bool {
	return s.IsLBModeUnmanaged() && s.HasEndpointTemplate()
}

// IsShardedEndpoint returns true if the LB configuration uses a sharded endpoint pattern
// (either unmanaged with {shardName} template, or managed with {shardName} in hostname).
// This is a hint that the MongoDBSearch targets a sharded cluster, though the
// authoritative check is the type assertion on SearchSourceShardedDeployment at the controller level.
func (s *MongoDBSearch) IsShardedEndpoint() bool {
	if s.IsShardedUnmanagedLB() {
		return true
	}
	if s.IsLBModeManaged() && strings.Contains(s.Spec.LoadBalancer.Managed.ExternalHostname, ShardNamePlaceholder) {
		return true
	}
	return false
}

// GetEndpointForShard returns the endpoint for a specific shard by substituting
// the {shardName} placeholder in the endpoint template.
func (s *MongoDBSearch) GetEndpointForShard(shardName string) string {
	if !s.IsShardedUnmanagedLB() {
		return ""
	}
	return strings.ReplaceAll(s.Spec.LoadBalancer.Unmanaged.Endpoint, ShardNamePlaceholder, shardName)
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
		if c.ClusterName == clusterName {
			return c, nil
		}
	}
	return ClusterSpec{}, fmt.Errorf("cluster %q not found in spec.clusters", clusterName)
}

// GetReplicasForCluster returns the per-cluster mongot replica count: the
// cluster's Replicas, or the default "1" when unset. clusterName="" reads the
// first entry (single-cluster case).
//
// An explicit 0 is honored: callers (and the connectivity-tool /
// availability-tester e2e tests) take mongot offline by setting
// spec.clusters[].replicas=0 on the MongoDBSearch CR. A `> 0` guard would
// silently clamp that to 1, so the operator would never scale the mongot
// StatefulSet down and tests waiting on the scale-to-0 would time out.
func (s *MongoDBSearch) GetReplicasForCluster(clusterName string) int {
	c, err := s.EffectiveClusterFor(clusterName)
	if err != nil {
		return 1
	}
	return c.ReplicasOrDefault()
}

// HasMultipleReplicas reports whether any cluster runs more than one mongot
// replica, the trigger for requiring a load balancer.
func (s *MongoDBSearch) HasMultipleReplicas() bool {
	return s.MaxReplicasAcrossClusters() > 1
}

// MaxReplicasAcrossClusters returns the largest per-cluster mongot replica
// count (clusters default to 1 when Replicas is unset).
func (s *MongoDBSearch) MaxReplicasAcrossClusters() int {
	highest := 0
	for _, c := range s.Spec.Clusters {
		if replicas := c.ReplicasOrDefault(); replicas > highest {
			highest = replicas
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
	return s.Spec.LoadBalancer != nil && s.Spec.LoadBalancer.Managed != nil
}

// GetManagedLBDeploymentConfig returns the user-provided Envoy Deployment override, or nil.
func (s *MongoDBSearch) GetManagedLBDeploymentConfig() *v1.DeploymentConfiguration {
	if s.IsLBModeManaged() {
		return s.Spec.LoadBalancer.Managed.Deployment
	}
	return nil
}

// GetManagedLBResourceRequirements returns user-specified Envoy resource requirements, or nil.
func (s *MongoDBSearch) GetManagedLBResourceRequirements() *corev1.ResourceRequirements {
	if s.IsLBModeManaged() {
		return s.Spec.LoadBalancer.Managed.ResourceRequirements
	}
	return nil
}

// GetManagedLBRetryPolicy returns user-specified Envoy retry policy, or nil.
func (s *MongoDBSearch) GetManagedLBRetryPolicy() *EnvoyRetryPolicy {
	if s.IsLBModeManaged() {
		return s.Spec.LoadBalancer.Managed.RetryPolicy
	}
	return nil
}

// GetManagedLBEndpoint returns the external hostname from spec.loadBalancer.managed.externalHostname
// when managed LB is configured. Returns "" otherwise.
func (s *MongoDBSearch) GetManagedLBEndpoint() string {
	if !s.IsLBModeManaged() {
		return ""
	}
	return s.Spec.LoadBalancer.Managed.ExternalHostname
}

// GetManagedLBEndpointForShard returns the external hostname for a specific shard by substituting
// the {shardName} template in spec.loadBalancer.managed.externalHostname (no cluster placeholders).
// Returns "" if managed LB is not configured or externalHostname is empty.
func (s *MongoDBSearch) GetManagedLBEndpointForShard(shardName string) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return ""
	}
	return strings.ReplaceAll(s.Spec.LoadBalancer.Managed.ExternalHostname, ShardNamePlaceholder, shardName)
}

// GetManagedLBEndpointForCluster resolves {clusterName} and {clusterIndex} in the
// externalHostname template from the caller's resolved index and name. Returns "" when
// managed LB is not configured; use GetManagedLBEndpointForClusterShard to also resolve {shardName}.
func (s *MongoDBSearch) GetManagedLBEndpointForCluster(clusterIndex int, clusterName string) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return ""
	}
	out := s.Spec.LoadBalancer.Managed.ExternalHostname
	out = strings.ReplaceAll(out, ClusterNamePlaceholder, clusterName)
	out = strings.ReplaceAll(out, ClusterIndexPlaceholder, strconv.Itoa(clusterIndex))
	return out
}

// GetManagedLBEndpointForClusterShard returns the externalHostname template with
// {clusterName}, {clusterIndex}, and {shardName} all resolved for the
// (cluster, shard) pair. Used for sharded multi-cluster MongoDBSearch
// deployments. Returns "" when managed LB is not configured.
func (s *MongoDBSearch) GetManagedLBEndpointForClusterShard(clusterIndex int, clusterName, shardName string) string {
	out := s.GetManagedLBEndpointForCluster(clusterIndex, clusterName)
	if out == "" {
		return ""
	}
	return strings.ReplaceAll(out, ShardNamePlaceholder, shardName)
}

// GetManagedLBEndpointForClusterLevel returns the mongos-facing endpoint: externalHostname
// with the leading "{shardName}." stripped. Returns "" when not derivable so the caller falls back to the proxy Service FQDN.
func (s *MongoDBSearch) GetManagedLBEndpointForClusterLevel(clusterIndex int, clusterName string) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return ""
	}
	tmpl := s.Spec.LoadBalancer.Managed.ExternalHostname
	trimmed := strings.TrimPrefix(tmpl, ShardNamePlaceholder+".")
	if strings.Contains(trimmed, ShardNamePlaceholder) {
		return ""
	}
	// Empty clusterName (operator-managed sharded mongos passes "") would substitute to a
	// blank label and emit a malformed ".host"; return "" so the caller falls back to the proxy FQDN.
	if clusterName == "" && strings.Contains(trimmed, ClusterNamePlaceholder) {
		return ""
	}
	out := strings.ReplaceAll(trimmed, ClusterNamePlaceholder, clusterName)
	out = strings.ReplaceAll(out, ClusterIndexPlaceholder, strconv.Itoa(clusterIndex))
	return out
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
	return s.Name + "-search-lb-0"
}

// LoadBalancerConfigMapName returns the name of the managed Envoy ConfigMap for this resource.
func (s *MongoDBSearch) LoadBalancerConfigMapName() string {
	return s.Name + "-search-lb-0-config"
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
	return fmt.Sprintf("%s-search-lb-0-%d-config", s.Name, clusterIndex)
}

// LoadBalancerServerCert returns the namespaced name of the TLS server certificate secret for the
// managed Envoy LB (ReplicaSet). Naming pattern:
//   - With prefix: {prefix}-{name}-search-lb-0-cert
//   - Without prefix: {name}-search-lb-0-cert
func (s *MongoDBSearch) LoadBalancerServerCert() types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-0-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-0-cert", s.Name), Namespace: s.Namespace}
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
// managed Envoy LB to authenticate with mongot backends. Naming pattern:
//   - With prefix: {prefix}-{name}-search-lb-0-client-cert
//   - Without prefix: {name}-search-lb-0-client-cert
func (s *MongoDBSearch) LoadBalancerClientCert() types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-0-client-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-0-client-cert", s.Name), Namespace: s.Namespace}
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

// ValidateSimulatedMCClusterIndices requires a non-empty spec.clusters where every entry pins
// a distinct ClusterIndex. Called only by operators in simulated-MC mode (operatorClusterName set).
func (s *MongoDBSearch) ValidateSimulatedMCClusterIndices() error {
	if len(s.Spec.Clusters) == 0 {
		return fmt.Errorf("running one operator per cluster requires spec.clusters to be set")
	}
	seen := make(map[int32]string, len(s.Spec.Clusters))
	for _, c := range s.Spec.Clusters {
		if c.ClusterIndex == nil {
			return fmt.Errorf("running one operator per cluster requires clusterIndex on every spec.clusters[] entry (missing on %q)", c.ClusterName)
		}
		if prev, dup := seen[*c.ClusterIndex]; dup {
			return fmt.Errorf("clusterIndex %d is set on more than one spec.clusters[] entry (%q and %q); pinned indices must be distinct", *c.ClusterIndex, prev, c.ClusterName)
		}
		seen[*c.ClusterIndex] = c.ClusterName
	}
	return nil
}

// LocalizeToCluster narrows spec.Clusters to the entry matching name. Returns false if absent (different operator owns this CR).
func (s *MongoDBSearch) LocalizeToCluster(name string) bool {
	if len(s.Spec.Clusters) == 0 {
		return true
	}
	for _, c := range s.Spec.Clusters {
		if c.ClusterName == name {
			s.Spec.Clusters = []ClusterSpec{c}
			return true
		}
	}
	return false
}
