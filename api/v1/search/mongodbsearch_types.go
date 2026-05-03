package search

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
)

// ShardNamePlaceholder is the placeholder used in endpoint templates for sharded clusters
const ShardNamePlaceholder = "{shardName}"

// ClusterNamePlaceholder is substituted with spec.clusters[i].ClusterName when
// resolving spec.loadBalancer.managed.externalHostname for cluster i in
// multi-cluster MongoDBSearch deployments. The Envoy reconciler substitutes the
// member cluster name so per-cluster SNI hostnames stay distinct.
const ClusterNamePlaceholder = "{clusterName}"

// ClusterIndexPlaceholder is substituted with the stable cluster-index assigned
// by the LastClusterNumMapping annotation for spec.clusters[i]. The index is
// monotonic and never reused on remove/re-add (see api/v1/search/cluster_index.go).
const ClusterIndexPlaceholder = "{clusterIndex}"

const (
	MongotDefaultWireprotoPort      int32 = 27027
	MongotDefaultGrpcPort           int32 = 27028
	MongotDefaultPrometheusPort     int32 = 9946
	MongotDefautHealthCheckPort     int32 = 8080
	EnvoyDefaultProxyPort           int32 = 27028
	MongotDefaultSyncSourceUsername       = "search-sync-source"

	ForceWireprotoAnnotation = "mongodb.com/v1.force-search-wireproto"

	// LastClusterNumMapping holds the JSON-encoded clusterName -> clusterIndex
	// mapping for spec.clusters[]. Mirrors mdbmulti.LastClusterNumMapping; the
	// string value is intentionally identical so a future shared util can collapse
	// the two without breaking existing CR annotations. Index never reused on
	// remove/re-add (see api/v1/search/cluster_index.go).
	LastClusterNumMapping = "mongodb.com/v1.lastClusterNumMapping"

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
	// Optional version of MongoDB Search component (mongot). If not set, then the operator will set the most appropriate version of MongoDB Search.
	// +optional
	Version string `json:"version"`
	// MongoDB database connection details from which MongoDB Search will synchronize data to build indexes.
	// +optional
	Source *MongoDBSource `json:"source"`
	// Deprecated: In multi-cluster deployments, prefer spec.clusters[].replicas. When
	// spec.clusters is omitted, this value auto-promotes into spec.clusters[0].replicas.
	// Setting both spec.replicas and spec.clusters at the same time is rejected by admission.
	// Replicas is the number of mongot pods to deploy.
	// For ReplicaSet source: the number of mongot pods in total.
	// For Sharded source: the number mongot pods per shard.
	// When Replicas > 1, a load balancer configuration (spec.loadBalancer)
	// is required to distribute traffic across mongot instances.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`
	// Deprecated: In multi-cluster deployments, prefer spec.clusters[].statefulSet. When
	// spec.clusters is omitted, this value auto-promotes into spec.clusters[0].statefulSet.
	// Setting both spec.statefulSet and spec.clusters at the same time is rejected by admission.
	// StatefulSetSpec which the operator will apply to the MongoDB Search StatefulSet at the end of the reconcile loop. Use to provide necessary customizations,
	// which aren't exposed as fields in the MongoDBSearch.spec.
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// Deprecated: In multi-cluster deployments, prefer spec.clusters[].persistence. When
	// spec.clusters is omitted, this value auto-promotes into spec.clusters[0].persistence.
	// Setting both spec.persistence and spec.clusters at the same time is rejected by admission.
	// Configure MongoDB Search's persistent volume. If not defined, the operator will request 10GB of storage.
	// +optional
	Persistence *common.Persistence `json:"persistence,omitempty"`
	// Deprecated: In multi-cluster deployments, prefer spec.clusters[].resourceRequirements. When
	// spec.clusters is omitted, this value auto-promotes into spec.clusters[0].resourceRequirements.
	// Setting both spec.resourceRequirements and spec.clusters at the same time is rejected by admission.
	// Configure resource requests and limits for the MongoDB Search pods.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// Configure security settings of the MongoDB Search server that MongoDB database is connecting to when performing search queries.
	// +optional
	Security Security `json:"security"`
	// Configure verbosity of mongot logs. Defaults to INFO if not set.
	// +kubebuilder:validation:Enum=TRACE;DEBUG;INFO;WARN;ERROR
	// +optional
	LogLevel mdb.LogLevel `json:"logLevel,omitempty"`
	// Configure prometheus metrics endpoint in mongot. If not set, the metrics endpoint will be disabled.
	// +optional
	Prometheus *Prometheus `json:"prometheus,omitempty"`
	// Configure MongoDB Search's automatic generation of vector embeddings using an embedding model service.
	// `embedding` field of mongot config is generated using the values provided here.
	// +optional
	AutoEmbedding *EmbeddingConfig `json:"autoEmbedding,omitempty"`
	// LoadBalancer configures how mongod/mongos connect to mongot (Managed vs Unmanaged/BYO Load Balancer).
	// Top-level spec.loadBalancer.managed.* serves as the default for every entry in spec.clusters;
	// per-cluster overrides deep-merge into this template (see spec.clusters[].loadBalancer.managed).
	// spec.loadBalancer.unmanaged is top-level only — there is no per-cluster form.
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"loadBalancer,omitempty"`
	// JVMFlags can be used to set the `--jvm-flags` option for the search (mongot) processes.
	// Top-level spec.jvmFlags serves as the default; spec.clusters[].jvmFlags replaces (not merges) it for that cluster.
	// https://www.mongodb.com/docs/manual/tutorial/mongot-sizing/advanced-guidance/hardware/#jvm-heap-sizing
	// +optional
	JVMFlags []string `json:"jvmFlags,omitempty"`
	// Clusters is the per-cluster distribution shape. Required for multi-cluster
	// deployments (len > 1); when omitted, the reconciler defaults it to a single
	// entry built from the top-level fields (B18 — not yet implemented). Pointer-of-slice
	// so omitted vs. empty is distinguishable.
	// MaxItems is set so the apiserver can bound the cost of the clusterName
	// uniqueness CEL rule below; 50 is well above any realistic multi-cluster
	// deployment.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:XValidation:rule="self.all(c1, self.exists_one(c2, c2.clusterName == c1.clusterName))",message="clusters[].clusterName must be unique"
	Clusters *[]ClusterSpec `json:"clusters,omitempty"`
}

// SyncSourceSelector picks which mongods this cluster's mongot fleet syncs from.
// At-most-one of MatchTags or Hosts may be set; the cross-field rule that
// requires exactly one when len(spec.clusters) > 1 lives in B13.
// MaxProperties / MaxItems / MaxLength on the children are required so the
// apiserver can bound the schema-cost contribution that the XValidation rule
// reads via has().
// +kubebuilder:validation:XValidation:rule="!(has(self.matchTags) && has(self.hosts))",message="syncSourceSelector.matchTags and syncSourceSelector.hosts are mutually exclusive"
type SyncSourceSelector struct {
	// MatchTags renders into mongot's readPreferenceTags; the operator picks
	// sync-source members whose replSetConfig tags match.
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

// PerClusterLoadBalancerConfig narrows LoadBalancerConfig to the subset that
// is overridable per-cluster: only Managed sub-fields. Unmanaged is top-level only.
type PerClusterLoadBalancerConfig struct {
	// Managed deep-merges into the top-level spec.loadBalancer.managed for this cluster.
	// +optional
	Managed *ManagedLBConfig `json:"managed,omitempty"`
}

// ShardOverride lets sharded MongoDBSearch deployments tune one or more shards
// beyond the per-cluster defaults. Only valid for sharded sources (the source-aware
// admission rule lives in B13).
type ShardOverride struct {
	// ShardNames is the set of shard names this override applies to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	ShardNames []string `json:"shardNames"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// +optional
	Persistence *common.Persistence `json:"persistence,omitempty"`
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
}

// ClusterSpec is one entry in spec.clusters[]. ClusterName is required and immutable
// when len(spec.clusters) > 1 (B13); optional in the single-cluster degenerate case.
// All other fields override the corresponding top-level value when set; nil/omitted inherits.
type ClusterSpec struct {
	// ClusterName is the Kubernetes cluster name. Required and immutable
	// when len(spec.clusters) > 1; optional in the single-cluster degenerate case.
	// MaxLength bounds the per-element cost contributed to the parent
	// clusters[] uniqueness CEL rule. 253 matches the DNS subdomain limit
	// that K8s cluster names obey.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ClusterName string `json:"clusterName,omitempty"`
	// Replicas overrides spec.replicas for this cluster's mongot StatefulSet.
	// For sharded sources, this is mongot pods per shard, not total.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// +optional
	Persistence *common.Persistence `json:"persistence,omitempty"`
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// +optional
	SyncSourceSelector *SyncSourceSelector `json:"syncSourceSelector,omitempty"`
	// LoadBalancer per-cluster override; deep-merged into spec.loadBalancer.managed.
	// Only managed sub-fields are overridable per-cluster.
	// +optional
	LoadBalancer *PerClusterLoadBalancerConfig `json:"loadBalancer,omitempty"`
	// ShardOverrides applies only to sharded sources.
	// +optional
	ShardOverrides []ShardOverride `json:"shardOverrides,omitempty"`
	// JVMFlags overrides spec.jvmFlags for this cluster's mongot pods. Replace, not merge.
	// +optional
	JVMFlags []string `json:"jvmFlags,omitempty"`
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
	// ResourceRequirements for the Envoy container.
	// When not set, defaults to requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: 500m, memory: 512Mi}.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// Deployment holds optional overrides merged into the operator-created Envoy Deployment.
	// Follows the same convention as spec.statefulSet on MongoDB resources.
	// +optional
	Deployment *common.DeploymentConfiguration `json:"deployment,omitempty"`
	// Replicas is the number of Envoy pods the operator deploys.
	// In multi-cluster deployments, top-level spec.loadBalancer.managed.replicas is the
	// default; per-cluster spec.clusters[].loadBalancer.managed.replicas overrides for
	// that cluster. Default 1 when unset.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`
}

// UnmanagedLBConfig configures a user-provided (BYO) L7 load balancer.
type UnmanagedLBConfig struct {
	// Endpoint is the full host:port of the BYO load balancer written into mongod config.
	// For sharded clusters, must contain a {shardName} placeholder.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

type EmbeddingConfig struct {
	ProviderEndpoint string `json:"providerEndpoint,omitempty"`
	// EmbeddingModelAPIKeySecret would have the name of the secret that has two keys
	// query-key and indexing-key for embedding model's API keys.
	// +kubebuilder:validation:Required
	EmbeddingModelAPIKeySecret corev1.LocalObjectReference `json:"embeddingModelAPIKeySecret"`
}

type MongoDBSource struct {
	// +optional
	MongoDBResourceRef *userv1.MongoDBResourceRef `json:"mongodbResourceRef,omitempty"`
	// +optional
	ExternalMongoDBSource *ExternalMongoDBSource `json:"external,omitempty"`
	// +optional
	PasswordSecretRef *userv1.SecretKeyRef `json:"passwordSecretRef,omitempty"`
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
type LoadBalancerStatus struct {
	Phase   status.Phase `json:"phase"`
	Message string       `json:"message,omitempty"`
	// Clusters is the per-cluster managed-LB phase. Only populated when
	// len(spec.clusters) > 0 — single-cluster keeps the existing top-level Phase.
	// B16 writes a placeholder schema; B9 will formalize it (sharded sub-statuses,
	// observedGeneration, etc.).
	// +optional
	Clusters []ClusterLoadBalancerStatus `json:"clusters,omitempty"`
}

// ClusterLoadBalancerStatus reports per-cluster managed LB state.
// Placeholder schema written by B16; B9 formalizes the per-cluster status surface.
type ClusterLoadBalancerStatus struct {
	// ClusterName is the member cluster this status entry belongs to.
	ClusterName string `json:"clusterName"`
	// Phase mirrors LoadBalancerStatus.Phase but for one cluster.
	Phase status.Phase `json:"phase"`
	// Message is an optional explanation for the per-cluster phase.
	// +optional
	Message string `json:"message,omitempty"`
}

// ShardLoadBalancerStatus reports the state of a single (cluster, shard)'s
// managed Envoy load balancer. Used in the flat per-cluster-per-shard map on
// MongoDBSearchStatus for sharded multi-cluster deployments. Mirrors
// LoadBalancerStatus but lives keyed by (clusterName, shardName) at the
// top level rather than nested under the per-cluster item, matching the
// MongoDB-sharded *InClusters precedent (api/v1/status/scaling_status.go).
type ShardLoadBalancerStatus struct {
	Phase   status.Phase `json:"phase"`
	Message string       `json:"message,omitempty"`
}

// SearchClusterStatusItem is the per-Kubernetes-cluster status entry, one
// entry per spec.clusters[i]. Mirrors the MongoDBMultiCluster
// ClusterStatusItem precedent (api/v1/mdbmulti/mongodb_multi_types.go) and
// adds an optional per-cluster managed-LB phase. Search is the first MCK
// CRD to surface a load-balancer phase per cluster.
type SearchClusterStatusItem struct {
	// ClusterName is the Kubernetes cluster name from spec.clusters[i].clusterName.
	// Empty in the single-cluster degenerate case (no spec.clusters entry).
	// +optional
	ClusterName string `json:"clusterName,omitempty"`
	// Common carries the per-cluster phase, message, lastTransition, and observedGeneration.
	status.Common `json:",inline"`
	// ObservedReplicas is the number of mongot pods this cluster currently has Ready.
	// Used by the load-test harness as the readiness gate.
	// +optional
	ObservedReplicas int32 `json:"observedReplicas,omitempty"`
	// Warnings is the per-cluster warnings list (e.g. customer-replicated
	// secret missing in this cluster). Populated by the per-cluster presence
	// check in B5; nil until that integration lands.
	// +optional
	Warnings []status.Warning `json:"warnings,omitempty"`
	// LoadBalancer reports the per-cluster managed-LB phase for ReplicaSet
	// topologies. Sharded per-(cluster, shard) LB phase lives on the
	// top-level ShardLoadBalancerStatusInClusters map.
	// +optional
	LoadBalancer *LoadBalancerStatus `json:"loadBalancer,omitempty"`
}

// SearchClusterStatusList is the per-cluster status surface for MongoDBSearch.
// Mirrors the MongoDBMultiCluster pattern.
type SearchClusterStatusList struct {
	// +optional
	ClusterStatuses []SearchClusterStatusItem `json:"clusterStatuses,omitempty"`
}

type MongoDBSearchStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
	// LoadBalancer reports the state of the operator-managed load balancer.
	// Only populated when spec.loadBalancer.managed is set.
	// In multi-cluster (len(spec.clusters) > 1) deployments, the per-cluster
	// LB phase lives on status.clusterStatusList.clusterStatuses[i].loadBalancer.
	// This top-level field stays for single-cluster back-compat.
	// +optional
	LoadBalancer *LoadBalancerStatus `json:"loadBalancer,omitempty"`
	// ClusterStatusList is the per-cluster status surface, one entry per
	// spec.clusters[i]. Empty in single-cluster (legacy) deployments.
	// +optional
	ClusterStatusList SearchClusterStatusList `json:"clusterStatusList,omitempty"`
	// ShardLoadBalancerStatusInClusters reports per-(cluster, shard) managed-LB
	// phase for sharded multi-cluster deployments. Flat map-of-map keyed by
	// clusterName then shardName, matching MongoDB sharded's *InClusters
	// precedent (api/v1/status/scaling_status.go).
	// +optional
	ShardLoadBalancerStatusInClusters map[string]map[string]*ShardLoadBalancerStatus `json:"shardLoadBalancerStatusInClusters,omitempty"`
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

// ProxyServiceNamespacedNameForCluster returns the proxy Service name for one
// member cluster identified by its cluster index. clusterIndex 0 matches the
// legacy single-cluster ProxyServiceNamespacedName for backward compatibility.
//
// Each cluster's proxy Service has a distinct name with the cluster index as a
// suffix; this avoids relying on per-cluster ClusterIP DNS scoping for
// disambiguation. mongod's `mongotHost` should be set to this name's FQDN
// per cluster (via `clusterSpecList[i].additionalMongodConfig` on the
// MongoDBMulti source).
func (s *MongoDBSearch) ProxyServiceNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-%s", s.Name, clusterIndex, ProxyServiceSuffix),
		Namespace: s.Namespace,
	}
}

// ProxyServiceNameForShard returns the stable proxy Service name for a specific shard.
func (s *MongoDBSearch) ProxyServiceNameForShard(shardName string) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-0-%s-%s", s.Name, shardName, ProxyServiceSuffix),
		Namespace: s.Namespace,
	}
}

func (s *MongoDBSearch) MongotConfigConfigMapNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-config", Namespace: s.Namespace}
}

// MongotConfigConfigMapNameForCluster returns the per-cluster mongot ConfigMap
// name. Index 0 matches the legacy single-cluster name for back-compat.
func (s *MongoDBSearch) MongotConfigConfigMapNameForCluster(clusterIndex int) types.NamespacedName {
	if clusterIndex == 0 {
		return s.MongotConfigConfigMapNamespacedName()
	}
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-config", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}

// StatefulSetNamespacedNameForCluster returns the StatefulSet name for one
// member cluster identified by its cluster index. The per-cluster path always
// uses index-suffixed names (`<name>-search-<idx>`); the legacy unindexed
// `<name>-search` from StatefulSetNamespacedName is reserved for the
// single-cluster degenerate path (Spec.Clusters nil/empty), which calls that
// helper directly.
func (s *MongoDBSearch) StatefulSetNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}

// SearchServiceNamespacedNameForCluster returns the headless Service name for
// one member cluster. Like StatefulSetNamespacedNameForCluster, it always
// produces an index-suffixed name; the legacy unindexed name comes from
// SearchServiceNamespacedName on the single-cluster path.
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
		Group:   GroupVersion.Group,
		Version: GroupVersion.Version,
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

// TLSSecretForShard returns the namespaced name of the TLS source secret for a specific shard.
// This is used in per-shard TLS mode for sharded clusters.
// Naming pattern:
//   - With prefix: {prefix}-{name}-search-0-{shardName}-cert
//   - Without prefix: {name}-search-0-{shardName}-cert
func (s *MongoDBSearch) TLSSecretForShard(shardName string) types.NamespacedName {
	var secretName string
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		secretName = fmt.Sprintf("%s-%s-search-0-%s-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name, shardName)
	} else {
		secretName = fmt.Sprintf("%s-search-0-%s-cert", s.Name, shardName)
	}
	return types.NamespacedName{Name: secretName, Namespace: s.Namespace}
}

// TLSOperatorSecretForShard returns the namespaced name of the operator-managed TLS secret
// for a specific shard. This is the secret created by the operator containing the combined certificate and key.
func (s *MongoDBSearch) TLSOperatorSecretForShard(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-certificate-key", shardName), Namespace: s.Namespace}
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
// loop should iterate over.
//
//   - When spec.clusters is non-nil (including the explicitly-empty slice),
//     it is returned as-is. The empty-slice case is reached only when admission
//     allows it; readers that index [0] must guard.
//   - When spec.clusters is nil, it auto-promotes the top-level
//     Replicas/ResourceRequirements/Persistence/StatefulSetConfiguration into
//     a one-element ClusterSpec for the legacy single-cluster path.
//
// The function is pure — no mutation of s, no side effects.
func EffectiveClusters(s *MongoDBSearch) []ClusterSpec {
	if s.Spec.Clusters != nil {
		return *s.Spec.Clusters
	}
	// Single legitimate read of the deprecated top-level distribution fields:
	// the auto-promotion fallback. Per-cluster readers go through this function.
	//nolint:staticcheck // SA1019: deprecated fields are the documented fallback path.
	return []ClusterSpec{{
		Replicas:                 s.Spec.Replicas,
		ResourceRequirements:     s.Spec.ResourceRequirements,
		Persistence:              s.Spec.Persistence,
		StatefulSetConfiguration: s.Spec.StatefulSetConfiguration,
	}}
}

func (s *MongoDBSearch) GetReplicas() int {
	// Single legitimate read of the deprecated top-level field — this is the
	// operator-side default ("1 when unset") for the legacy single-cluster path.
	// Multi-cluster readers go through EffectiveClusters() instead.
	//nolint:staticcheck // SA1019: deprecated field is the documented fallback.
	if s.Spec.Replicas != nil && *s.Spec.Replicas > 0 {
		return int(*s.Spec.Replicas)
	}
	return 1
}

func (s *MongoDBSearch) HasMultipleReplicas() bool {
	return s.GetReplicas() > 1
}

// HasAutoEmbedding returns true when auto-embedding is configured.
func (s *MongoDBSearch) HasAutoEmbedding() bool {
	return s.Spec.AutoEmbedding != nil
}

func (s *MongoDBSearch) MongotStatefulSetForShard(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-0-%s", s.Name, shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) MongotServiceForShard(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-0-%s-svc", s.Name, shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) MongotConfigMapForShard(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-0-%s-config", s.Name, shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) IsLBModeManaged() bool {
	return s.Spec.LoadBalancer != nil && s.Spec.LoadBalancer.Managed != nil
}

// GetManagedLBDeploymentConfig returns the user-provided Envoy Deployment override, or nil.
func (s *MongoDBSearch) GetManagedLBDeploymentConfig() *common.DeploymentConfiguration {
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

// GetManagedLBEndpoint returns the external hostname from spec.loadBalancer.managed.externalHostname
// when managed LB is configured. Returns "" otherwise.
func (s *MongoDBSearch) GetManagedLBEndpoint() string {
	if !s.IsLBModeManaged() {
		return ""
	}
	return s.Spec.LoadBalancer.Managed.ExternalHostname
}

// GetManagedLBEndpointForShard returns the external hostname for a specific shard by substituting
// the {shardName} template in spec.loadBalancer.managed.externalHostname.
// Returns "" if managed LB is not configured or externalHostname is empty.
func (s *MongoDBSearch) GetManagedLBEndpointForShard(shardName string) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return ""
	}
	return strings.ReplaceAll(s.Spec.LoadBalancer.Managed.ExternalHostname, ShardNamePlaceholder, shardName)
}

// GetManagedLBEndpointForCluster returns the externalHostname template with
// {clusterName} and {clusterIndex} resolved for spec.clusters[i]. Returns "" when
// managed LB is not configured. Use GetManagedLBEndpointForClusterShard for
// sharded sources where {shardName} also needs resolving.
//
// Behaviour:
//   - Legacy single-cluster (spec.clusters nil): placeholders are left untouched.
//     Admission rejects MC specs missing the required placeholders, so reaching
//     this path with a placeholder-bearing legacy template is malformed.
//   - Out-of-range i: placeholders are left untouched (defensive — call sites
//     iterate over len(*spec.clusters)).
func (s *MongoDBSearch) GetManagedLBEndpointForCluster(i int) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return ""
	}
	out := s.Spec.LoadBalancer.Managed.ExternalHostname
	if s.Spec.Clusters == nil {
		return out
	}
	clusters := *s.Spec.Clusters
	if i < 0 || i >= len(clusters) {
		return out
	}
	out = strings.ReplaceAll(out, ClusterNamePlaceholder, clusters[i].ClusterName)
	// {clusterIndex} resolves to the persisted monotonic index from the
	// LastClusterNumMapping annotation when available; otherwise it falls back
	// to the slice index. The fallback covers legacy specs that have not yet
	// been re-reconciled to populate the annotation.
	idx := i
	if persisted, ok := ClusterIndex(s, clusters[i].ClusterName); ok {
		idx = persisted
	}
	out = strings.ReplaceAll(out, ClusterIndexPlaceholder, strconv.Itoa(idx))
	return out
}

// GetManagedLBEndpointForClusterShard returns the externalHostname template with
// {clusterName}, {clusterIndex}, and {shardName} all resolved for the
// (spec.clusters[i], shardName) pair. Used for sharded multi-cluster
// MongoDBSearch deployments. Returns "" when managed LB is not configured.
func (s *MongoDBSearch) GetManagedLBEndpointForClusterShard(i int, shardName string) string {
	out := s.GetManagedLBEndpointForCluster(i)
	if out == "" {
		return ""
	}
	return strings.ReplaceAll(out, ShardNamePlaceholder, shardName)
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
// Deployment for one member cluster (B16). When clusterName is empty the result
// matches LoadBalancerDeploymentName for back-compat with single-cluster installs.
// In multi-cluster, the cluster identifier is appended so per-cluster Deployments
// in the same namespace stay distinct (resource-name length is checked at
// admission via validateClustersEnvoyResourceNames).
func (s *MongoDBSearch) LoadBalancerDeploymentNameForCluster(clusterName string) string {
	if clusterName == "" {
		return s.LoadBalancerDeploymentName()
	}
	return s.LoadBalancerDeploymentName() + "-" + clusterName
}

// LoadBalancerConfigMapNameForCluster returns the name of the managed Envoy
// ConfigMap for one member cluster (B16). See LoadBalancerDeploymentNameForCluster.
func (s *MongoDBSearch) LoadBalancerConfigMapNameForCluster(clusterName string) string {
	if clusterName == "" {
		return s.LoadBalancerConfigMapName()
	}
	return s.Name + "-search-lb-0-" + clusterName + "-config"
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

// LoadBalancerServerCertForShard returns the namespaced name of the TLS server certificate secret for
// a specific shard of the managed Envoy LB (sharded cluster). Naming pattern:
//   - With prefix: {prefix}-{name}-search-lb-0-{shardName}-cert
//   - Without prefix: {name}-search-lb-0-{shardName}-cert
func (s *MongoDBSearch) LoadBalancerServerCertForShard(shardName string) types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-0-%s-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name, shardName),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-0-%s-cert", s.Name, shardName), Namespace: s.Namespace}
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
