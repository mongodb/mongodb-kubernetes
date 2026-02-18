package search

import (
	"fmt"
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

const (
	MongotDefaultWireprotoPort      int32 = 27027
	MongotDefaultGrpcPort           int32 = 27028
	MongotDefaultPrometheusPort     int32 = 9946
	MongotDefautHealthCheckPort     int32 = 8080
	MongotDefaultSyncSourceUsername       = "search-sync-source"

	ForceWireprotoAnnotation = "mongodb.com/v1.force-search-wireproto"
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
	// StatefulSetSpec which the operator will apply to the MongoDB Search StatefulSet at the end of the reconcile loop. Use to provide necessary customizations,
	// which aren't exposed as fields in the MongoDBSearch.spec.
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// Configure MongoDB Search's persistent volume. If not defined, the operator will request 10GB of storage.
	// +optional
	Persistence *common.Persistence `json:"persistence,omitempty"`
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
	// LoadBalancer configures how mongod/mongos connect to mongot (Managed vs Unmanaged/BYO LB).
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"lb,omitempty"`
}

// LBMode defines the load balancer mode for Search
// +kubebuilder:validation:Enum=Managed;Unmanaged
type LBMode string

const (
	// LBModeManaged indicates operator-managed Envoy load balancer
	LBModeManaged LBMode = "Managed"
	// LBModeUnmanaged indicates user-provided external L7 load balancer (BYO LB)
	LBModeUnmanaged LBMode = "Unmanaged"
)

// LoadBalancerConfig configures how mongod/mongos connect to mongot
type LoadBalancerConfig struct {
	// Mode specifies the load balancer mode: Managed (operator-managed) or Unmanaged (BYO L7 LB)
	// +kubebuilder:validation:Required
	Mode LBMode `json:"mode"`
	// Envoy contains configuration for operator-managed Envoy load balancer
	// +optional
	Envoy *EnvoyConfig `json:"envoy,omitempty"`
	// External contains configuration for user-provided external L7 load balancer
	// +optional
	External *ExternalLBConfig `json:"external,omitempty"`
}

// EnvoyConfig contains configuration for operator-managed Envoy load balancer
// Placeholder for future Envoy configuration options
type EnvoyConfig struct {
	// Placeholder for future Envoy configuration
}

// ExternalLBConfig contains configuration for user-provided external L7 load balancer
type ExternalLBConfig struct {
	// Endpoint is the LB endpoint for ReplicaSet or a template for sharded clusters.
	// For sharded clusters, use {shardName} as a placeholder that will be substituted
	// with the actual shard name. Example: "lb-{shardName}.example.com:27028"
	// TODO move it to spec.lb
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// DEPRECATED: Use Endpoint with {shardName} template instead.
	// Sharded contains per-shard LB endpoint configuration for sharded clusters.
	// If both Endpoint (with template) and Sharded.Endpoints are specified,
	// Endpoint template takes precedence.
	// +optional
	Sharded *ShardedExternalLBConfig `json:"sharded,omitempty"`
}

// ShardedExternalLBConfig contains per-shard LB endpoint configuration
// TODO remove it
type ShardedExternalLBConfig struct {
	// Endpoints is a list of per-shard LB endpoints
	// +kubebuilder:validation:MinItems=1
	Endpoints []ShardEndpoint `json:"endpoints"`
}

// ShardEndpoint maps a shard name to its external LB endpoint
type ShardEndpoint struct {
	// ShardName is the logical shard name (e.g., "my-cluster-0")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ShardName string `json:"shardName"`
	// Endpoint is the external LB host:port for this shard's mongot pool
	// The hostname is typically used as SNI by the external LB to route to the correct shard-local mongot
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
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
	// Replicas is the number of mongot pods to deploy.
	// For ReplicaSet source: this many mongot pods total.
	// For Sharded source: this many mongot pods per shard.
	// When Replicas > 1, a load balancer configuration (lb.mode: Unmanaged with lb.external.endpoint)
	// is required to distribute traffic across mongot instances.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int `json:"replicas,omitempty"`
}

type ExternalMongoDBSource struct {
	// HostAndPorts is the list of mongod host:port seeds for replica set sources.
	// Mutually exclusive with Sharded.
	// +optional
	HostAndPorts []string `json:"hostAndPorts,omitempty"`
	// Sharded contains configuration for external sharded MongoDB clusters.
	// Mutually exclusive with HostAndPorts.
	// +optional
	Sharded *ExternalShardedConfig `json:"sharded,omitempty"`
	// mongod keyfile used to connect to the external MongoDB deployment
	// +optional
	KeyFileSecretKeyRef *userv1.SecretKeyRef `json:"keyfileSecretRef,omitempty"`
	// TLS configuration for the external MongoDB deployment
	// +optional
	TLS *ExternalMongodTLS `json:"tls,omitempty"`
}

// ExternalShardedConfig contains configuration for external sharded MongoDB clusters
type ExternalShardedConfig struct {
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
	// TLS configuration specific to the router. If not specified, falls back to the top-level external.tls config.
	// +optional
	TLS *ExternalMongodTLS `json:"tls,omitempty"`
}

// ExternalShardConfig contains configuration for a single shard in an external sharded cluster
type ExternalShardConfig struct {
	// ShardName is the logical shard name (e.g., "shard-0").
	// This name is used to match with lb.external.sharded.endpoints[].shardName
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
	// If CertificateKeySecret.Name is also specified, it takes precedence over this field.
	// +optional
	CertsSecretPrefix string `json:"certsSecretPrefix,omitempty"`
}

type MongoDBSearchStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

// +k8s:deepcopy-gen=true
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the MongoDB deployment."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",description="MongoDB Search version reconciled by the operator."
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

func (s *MongoDBSearch) GetStatus(...status.Option) interface{} {
	return s.Status
}

func (s *MongoDBSearch) GetStatusPath(...status.Option) string {
	return "/status"
}

func (s *MongoDBSearch) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	s.Status.Warnings = warnings
}

func (s *MongoDBSearch) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	s.Status.UpdateCommonFields(phase, s.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		s.Status.Warnings = append(s.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}
	if option, exists := status.GetOption(statusOptions, MongoDBSearchVersionOption{}); exists {
		s.Status.Version = option.(MongoDBSearchVersionOption).Version
	}
}

func (s *MongoDBSearch) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name, Namespace: s.Namespace}
}

func (s *MongoDBSearch) SearchServiceNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-svc", Namespace: s.Namespace}
}

func (s *MongoDBSearch) MongotConfigConfigMapNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-config", Namespace: s.Namespace}
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

// IsSharedTLSCertificate returns true if all shards should use the same TLS certificate.
// This is the case when CertificateKeySecret.Name is explicitly set.
// When false, per-shard certificates are used (one certificate per shard).
func (s *MongoDBSearch) IsSharedTLSCertificate() bool {
	if s.Spec.Security.TLS == nil {
		return false
	}
	return s.Spec.Security.TLS.CertificateKeySecret.Name != ""
}

// TLSSecretNamespacedNameForShard returns the namespaced name of the TLS source secret for a specific shard.
// This is used in per-shard TLS mode for sharded clusters.
// Naming pattern:
//   - With prefix: {prefix}-{shardName}-search-cert
//   - Without prefix: {shardName}-search-cert
func (s *MongoDBSearch) TLSSecretNamespacedNameForShard(shardName string) types.NamespacedName {
	var secretName string
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		secretName = fmt.Sprintf("%s-%s-search-cert", s.Spec.Security.TLS.CertsSecretPrefix, shardName)
	} else {
		secretName = fmt.Sprintf("%s-search-cert", shardName)
	}
	return types.NamespacedName{Name: secretName, Namespace: s.Namespace}
}

// TLSOperatorSecretNamespacedNameForShard returns the namespaced name of the operator-managed TLS secret
// for a specific shard. This is the secret created by the operator containing the combined certificate and key.
func (s *MongoDBSearch) TLSOperatorSecretNamespacedNameForShard(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-certificate-key", shardName), Namespace: s.Namespace}
}

// IsTLSConfigured returns true if TLS is enabled (TLS struct is present)
func (s *MongoDBSearch) IsTLSConfigured() bool {
	return s.Spec.Security.TLS != nil
}

func (s *MongoDBSearch) GetMongotHealthCheckPort() int32 {
	return MongotDefautHealthCheckPort
}

func (s *MongoDBSearch) IsExternalMongoDBSource() bool {
	return s.Spec.Source != nil && s.Spec.Source.ExternalMongoDBSource != nil
}

// IsExternalShardedSource returns true if the source is an external sharded MongoDB cluster
func (s *MongoDBSearch) IsExternalShardedSource() bool {
	return s.IsExternalMongoDBSource() &&
		s.Spec.Source.ExternalMongoDBSource.Sharded != nil
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

func (s *MongoDBSearch) IsExternalLBMode() bool {
	return s.Spec.LoadBalancer != nil && s.Spec.LoadBalancer.Mode == LBModeUnmanaged
}

// IsReplicaSetExternalLB returns true if this is a ReplicaSet external LB configuration.
// An endpoint with a template placeholder ({shardName}) is NOT considered a ReplicaSet endpoint.
func (s *MongoDBSearch) IsReplicaSetExternalLB() bool {
	return s.IsExternalLBMode() &&
		s.Spec.LoadBalancer.External != nil &&
		s.Spec.LoadBalancer.External.Endpoint != "" &&
		!s.HasEndpointTemplate()
}

func (s *MongoDBSearch) GetReplicaSetExternalLBEndpoint() string {
	if !s.IsReplicaSetExternalLB() {
		return ""
	}
	return s.Spec.LoadBalancer.External.Endpoint
}

// HasEndpointTemplate returns true if the endpoint contains the {shardName} template placeholder
func (s *MongoDBSearch) HasEndpointTemplate() bool {
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.External == nil {
		return false
	}
	return strings.Contains(s.Spec.LoadBalancer.External.Endpoint, ShardNamePlaceholder)
}

// IsShardedExternalLB returns true if this is a sharded external LB configuration.
// This can be either via endpoint template or legacy per-shard endpoints array.
func (s *MongoDBSearch) IsShardedExternalLB() bool {
	if !s.IsExternalLBMode() || s.Spec.LoadBalancer.External == nil {
		return false
	}
	// Template format takes precedence
	if s.HasEndpointTemplate() {
		return true
	}
	// Legacy format: explicit per-shard endpoints
	return s.Spec.LoadBalancer.External.Sharded != nil &&
		len(s.Spec.LoadBalancer.External.Sharded.Endpoints) > 0
}

// GetEndpointForShard returns the endpoint for a specific shard.
// If using template format, substitutes {shardName} with the actual shard name.
// If using legacy format, looks up the shard in the endpoints array.
func (s *MongoDBSearch) GetEndpointForShard(shardName string) string {
	if !s.IsShardedExternalLB() {
		return ""
	}
	// Template format takes precedence
	if s.HasEndpointTemplate() {
		return strings.ReplaceAll(s.Spec.LoadBalancer.External.Endpoint, ShardNamePlaceholder, shardName)
	}
	// Legacy format: look up in endpoints array
	for _, e := range s.Spec.LoadBalancer.External.Sharded.Endpoints {
		if e.ShardName == shardName {
			return e.Endpoint
		}
	}
	return ""
}

func (s *MongoDBSearch) GetShardEndpointMap() map[string]string {
	result := make(map[string]string)
	if !s.IsShardedExternalLB() {
		return result
	}
	// Note: This only works for legacy format. For template format, use GetEndpointForShard()
	if s.Spec.LoadBalancer.External.Sharded != nil {
		for _, e := range s.Spec.LoadBalancer.External.Sharded.Endpoints {
			result[e.ShardName] = e.Endpoint
		}
	}
	return result
}

func (s *MongoDBSearch) GetReplicas() int {
	if s.Spec.Source != nil && s.Spec.Source.Replicas > 0 {
		return s.Spec.Source.Replicas
	}
	return 1
}

func (s *MongoDBSearch) HasMultipleReplicas() bool {
	return s.GetReplicas() > 1
}

func (s *MongoDBSearch) ShardMongotStatefulSetName(shardName string) string {
	return fmt.Sprintf("%s-mongot-%s", s.Name, shardName)
}

func (s *MongoDBSearch) ShardMongotStatefulSetNamespacedName(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: s.ShardMongotStatefulSetName(shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) ShardMongotServiceName(shardName string) string {
	return fmt.Sprintf("%s-mongot-%s-svc", s.Name, shardName)
}

func (s *MongoDBSearch) ShardMongotServiceNamespacedName(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: s.ShardMongotServiceName(shardName), Namespace: s.Namespace}
}

func (s *MongoDBSearch) ShardMongotConfigMapName(shardName string) string {
	return fmt.Sprintf("%s-mongot-%s-config", s.Name, shardName)
}

func (s *MongoDBSearch) ShardMongotConfigMapNamespacedName(shardName string) types.NamespacedName {
	return types.NamespacedName{Name: s.ShardMongotConfigMapName(shardName), Namespace: s.Namespace}
}
