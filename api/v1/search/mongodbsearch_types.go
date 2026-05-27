package search

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ShardNamePlaceholder is the placeholder used in endpoint templates for sharded clusters
const ShardNamePlaceholder = "{shardName}"

// ClusterNamePlaceholder is substituted with spec.clusters[i].ClusterName in spec.loadBalancer.managed.externalHostname.
const ClusterNamePlaceholder = "{clusterName}"

// ClusterIndexPlaceholder is substituted with the stable cluster-index; indices are never reused on remove/re-add.
const ClusterIndexPlaceholder = "{clusterIndex}"

// LabelResourceOwner is the label key used to identify the MongoDBSearch CR that
// owns a resource. Used as part of GetOwnerLabels for StateStore ConfigMap selection.
const LabelResourceOwner = "mongodb.com/v1.mongodbSearchResourceOwner"

const (
	MongotDefaultWireprotoPort      int32 = 27027
	MongotDefaultGrpcPort           int32 = 27028
	MongotDefaultPrometheusPort     int32 = 9946
	MongotDefautHealthCheckPort     int32 = 8080
	EnvoyDefaultProxyPort           int32 = 27028
	MongotDefaultSyncSourceUsername       = "search-sync-source"

	ForceWireprotoAnnotation = "mongodb.com/v1.force-search-wireproto"

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
	// Deprecated: prefer spec.clusters[].replicas. With spec.clusters omitted, this auto-promotes into spec.clusters[0].replicas.
	// Replicas is the number of mongot pods to deploy (per-shard for sharded source). When Replicas > 1, spec.loadBalancer must be configured.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`
	// Deprecated: prefer spec.clusters[].statefulSet. With spec.clusters omitted, this auto-promotes into spec.clusters[0].statefulSet.
	// StatefulSetSpec applied to the mongot StatefulSet for customizations not exposed as MongoDBSearch.spec fields.
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// Deprecated: prefer spec.clusters[].persistence. With spec.clusters omitted, this auto-promotes into spec.clusters[0].persistence.
	// Configures the mongot persistent volume; defaults to 10Gi if unset.
	// +optional
	Persistence *common.Persistence `json:"persistence,omitempty"`
	// Deprecated: prefer spec.clusters[].resourceRequirements. With spec.clusters omitted, this auto-promotes into spec.clusters[0].resourceRequirements.
	// Resource requests and limits for the mongot pods.
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
	// entry built from the top-level fields. Pointer-of-slice so omitted vs.
	// empty is distinguishable.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:XValidation:rule="self.all(c1, self.exists_one(c2, c2.clusterName == c1.clusterName))",message="clusters[].clusterName must be unique"
	// +kubebuilder:validation:XValidation:rule="self.all(c1, !has(c1.clusterIndex) || self.exists_one(c2, has(c2.clusterIndex) && c2.clusterIndex == c1.clusterIndex))",message="clusters[].clusterIndex must be unique when set"
	Clusters *[]ClusterSpec `json:"clusters,omitempty"`
}

// SyncSourceSelector picks which mongods this cluster's mongot fleet syncs from.
// At-most-one of MatchTags or Hosts may be set.
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

// ClusterSpec is one entry in spec.clusters[]. ClusterName is required and immutable
// when len(spec.clusters) > 1; optional in the single-cluster degenerate case.
// All other fields override the corresponding top-level value when set; nil/omitted inherits.
type ClusterSpec struct {
	// ClusterName is the Kubernetes cluster name. Required when len(spec.clusters) > 1.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ClusterName string `json:"clusterName,omitempty"`
	// ClusterIndex is the stable integer in per-cluster resource names. Immutable once set; required in simulated multi-cluster mode.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ClusterIndex *int32 `json:"clusterIndex,omitempty"`
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
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"loadBalancer,omitempty"`
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
	Deployment *common.DeploymentConfiguration `json:"deployment,omitempty"`
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

// EffectiveClusters returns the per-cluster slice with top-level fields cascaded as defaults.
// REPLACE-if-nil for scalars/structs, REPLACE-if-empty for slices; cluster-set wins. Pure — no mutation.
func (s *MongoDBSearch) EffectiveClusters() []ClusterSpec {
	//nolint:staticcheck // SA1019: deprecated top-level fields are the documented default for the cascade.
	topReplicas := s.Spec.Replicas
	//nolint:staticcheck // SA1019
	topResReq := s.Spec.ResourceRequirements
	//nolint:staticcheck // SA1019
	topPersistence := s.Spec.Persistence
	//nolint:staticcheck // SA1019
	topSTSConfig := s.Spec.StatefulSetConfiguration
	//nolint:staticcheck // SA1019
	topJVMFlags := s.Spec.JVMFlags

	if s.Spec.Clusters == nil {
		return []ClusterSpec{{
			Replicas:                 topReplicas,
			ResourceRequirements:     topResReq,
			Persistence:              topPersistence,
			StatefulSetConfiguration: topSTSConfig,
			JVMFlags:                 topJVMFlags,
		}}
	}

	clusters := *s.Spec.Clusters
	out := make([]ClusterSpec, 0, len(clusters))
	for _, c := range clusters {
		resolved := c
		if resolved.Replicas == nil {
			resolved.Replicas = topReplicas
		}
		if resolved.ResourceRequirements == nil {
			resolved.ResourceRequirements = topResReq
		}
		if resolved.Persistence == nil {
			resolved.Persistence = topPersistence
		}
		// TODO: whole-struct REPLACE-if-nil here; sharded MC deep-merges the inner
		// PodTemplateSpec via merge.PodTemplateSpecs. Gated on shardOverrides API
		// redesign — revisit before GA.
		if resolved.StatefulSetConfiguration == nil {
			resolved.StatefulSetConfiguration = topSTSConfig
		}
		if len(resolved.JVMFlags) == 0 {
			resolved.JVMFlags = topJVMFlags
		}
		out = append(out, resolved)
	}
	return out
}

// EffectiveClusterFor returns the cascaded ClusterSpec for the named cluster.
// Empty clusterName returns the auto-promoted single-cluster entry (legacy path).
// Returns an error if the named cluster is not found in spec.clusters[].
func (s *MongoDBSearch) EffectiveClusterFor(clusterName string) (ClusterSpec, error) {
	effective := s.EffectiveClusters()
	if clusterName == "" {
		if len(effective) > 0 {
			return effective[0], nil
		}
		return ClusterSpec{}, fmt.Errorf("cluster %q not found in spec.clusters", clusterName)
	}
	for _, c := range effective {
		if c.ClusterName == clusterName {
			return c, nil
		}
	}
	return ClusterSpec{}, fmt.Errorf("cluster %q not found in spec.clusters", clusterName)
}

func (s *MongoDBSearch) GetReplicas() int {
	//nolint:staticcheck // SA1019: legacy single-cluster fallback; MC readers use GetReplicasForCluster.
	if s.Spec.Replicas != nil && *s.Spec.Replicas > 0 {
		return int(*s.Spec.Replicas)
	}
	return 1
}

// GetReplicasForCluster returns the cascaded replica count; clusterName="" returns the single-cluster value.
func (s *MongoDBSearch) GetReplicasForCluster(clusterName string) int {
	c, err := s.EffectiveClusterFor(clusterName)
	if err != nil {
		return 1
	}
	if r := c.Replicas; r != nil && *r > 0 {
		return int(*r)
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
// the {shardName} template in spec.loadBalancer.managed.externalHostname (no cluster placeholders).
// Returns "" if managed LB is not configured or externalHostname is empty.
func (s *MongoDBSearch) GetManagedLBEndpointForShard(shardName string) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return ""
	}
	return strings.ReplaceAll(s.Spec.LoadBalancer.Managed.ExternalHostname, ShardNamePlaceholder, shardName)
}

// GetManagedLBEndpointForCluster resolves {clusterName} and {clusterIndex} in
// the externalHostname template for spec.clusters[i]. Returns "" when managed
// LB is not configured. Use GetManagedLBEndpointForClusterShard when {shardName}
// also needs resolving.
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
	out = strings.ReplaceAll(out, ClusterIndexPlaceholder, strconv.Itoa(clusterIndexFor(clusters, i)))
	return out
}

// clusterIndexFor returns the user-pinned ClusterIndex for clusters[i] when
// set, falling back to the array position. The pinned value is what makes
// per-cluster resource names stable across spec.clusters[] reorders.
func clusterIndexFor(clusters []ClusterSpec, i int) int {
	if clusters[i].ClusterIndex != nil {
		return int(*clusters[i].ClusterIndex)
	}
	return i
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

// GetManagedLBEndpointForClusterLevel derives the mongos-facing endpoint by stripping
// the leading "{shardName}." from externalHostname. Returns "" when not derivable;
// callers fall back to the cluster-level proxy Service FQDN.
func (s *MongoDBSearch) GetManagedLBEndpointForClusterLevel(i int) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return ""
	}
	tmpl := s.Spec.LoadBalancer.Managed.ExternalHostname
	trimmed := strings.TrimPrefix(tmpl, ShardNamePlaceholder+".")
	if strings.Contains(trimmed, ShardNamePlaceholder) {
		return ""
	}
	hasClusterPlaceholder := strings.Contains(trimmed, ClusterNamePlaceholder) ||
		strings.Contains(trimmed, ClusterIndexPlaceholder)
	if s.Spec.Clusters == nil {
		if hasClusterPlaceholder {
			return ""
		}
		return trimmed
	}
	clusters := *s.Spec.Clusters
	if i < 0 || i >= len(clusters) {
		if hasClusterPlaceholder {
			return ""
		}
		return trimmed
	}
	out := strings.ReplaceAll(trimmed, ClusterNamePlaceholder, clusters[i].ClusterName)
	out = strings.ReplaceAll(out, ClusterIndexPlaceholder, strconv.Itoa(clusterIndexFor(clusters, i)))
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

// LocalizeToCluster narrows spec.Clusters to the entry matching name. Returns false if absent (different operator owns this CR).
func (s *MongoDBSearch) LocalizeToCluster(name string) bool {
	if s.Spec.Clusters == nil || len(*s.Spec.Clusters) == 0 {
		return true
	}
	for _, c := range *s.Spec.Clusters {
		if c.ClusterName == name {
			only := []ClusterSpec{c}
			s.Spec.Clusters = &only
			return true
		}
	}
	return false
}

// AssignClusterIndices merges existing+current into a clusterName->index map.
// Existing entries are preserved (indices never reused on remove/re-add); user-set ClusterIndex
// overrides; unset names append at max(existing)+1. existing is not mutated.
func AssignClusterIndices(existing map[string]int, current []ClusterSpec) map[string]int {
	result := make(map[string]int, len(existing)+len(current))
	next := 0
	for k, v := range existing {
		result[k] = v
		if v >= next {
			next = v + 1
		}
	}
	for _, c := range current {
		if c.ClusterIndex == nil {
			continue
		}
		idx := int(*c.ClusterIndex)
		result[c.ClusterName] = idx
		if idx >= next {
			next = idx + 1
		}
	}
	for _, c := range current {
		if c.ClusterIndex != nil {
			continue
		}
		if _, ok := result[c.ClusterName]; ok {
			continue
		}
		result[c.ClusterName] = next
		next++
	}
	return result
}
