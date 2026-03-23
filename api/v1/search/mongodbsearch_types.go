package search

import (
	"fmt"
	"net"
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
	EnvoyDefaultProxyPort           int32 = 27028
	MongotDefaultSyncSourceUsername       = "search-sync-source"

	ForceWireprotoAnnotation = "mongodb.com/v1.force-search-wireproto"

	MongoDBSearchIndexFieldName = "mdbsearch-for-mongodbresourceref-index"
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
	// Replicas is the number of mongot pods to deploy.
	// For ReplicaSet source: the number of mongot pods in total.
	// For Sharded source: the number mongot pods per shard.
	// When Replicas > 1, a load balancer configuration (spec.lb)
	// is required to distribute traffic across mongot instances.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int `json:"replicas,omitempty"`
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
	// LoadBalancer configures how mongod/mongos connect to mongot (Managed vs Unmanaged/BYO Load Balancer).
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"lb,omitempty"`
	// JVMFlags can be used to set the `--jvm-flags` option for the search (mongot) processes.
	// https://www.mongodb.com/docs/manual/tutorial/mongot-sizing/advanced-guidance/hardware/#jvm-heap-sizing
	// +optional
	JVMFlags []string `json:"jvmFlags,omitempty"`
}

// LBMode defines the load balancer mode for Search
// +kubebuilder:validation:Enum=Managed;Unmanaged
type LBMode string

const (
	// LBModeManaged indicates operator-managed load balancer
	LBModeManaged LBMode = "Managed"
	// LBModeUnmanaged indicates user-provided L7 load balancer (BYO LB)
	LBModeUnmanaged LBMode = "Unmanaged"
)

// LoadBalancerConfig configures how mongod/mongos connect to mongot
type LoadBalancerConfig struct {
	// Mode specifies the load balancer mode: Managed (operator-managed) or Unmanaged (BYO L7 Load Balancer)
	// +kubebuilder:validation:Required
	Mode LBMode `json:"mode"`
	// Endpoint is the LB endpoint for ReplicaSet, or a template for sharded clusters.
	// For sharded clusters, at least one {shardName} placeholder must be present and will be substituted with the actual shard name.
	// Each shard must connect to their mongots using a shard-unique endpoint to allow LoadBalancer to
	// route the traffic to the mongot instances for that shard.
	// Example: "lb-{shardName}.example.com:27028"
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Envoy contains configuration for operator-managed Envoy load balancer
	// +optional
	Envoy *EnvoyConfig `json:"envoy,omitempty"`
}

// EnvoyConfig contains configuration for the operator-managed Envoy load balancer.
type EnvoyConfig struct {
	// ResourceRequirements for the Envoy container.
	// When not set, defaults to requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: 500m, memory: 512Mi}.
	// When set, replaces the defaults entirely.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
	// DeploymentConfiguration holds the optional custom Deployment configuration
	// that will be merged into the operator-created Envoy Deployment.
	// Use to provide customizations (tolerations, affinity, nodeSelector, annotations, etc.)
	// which aren't exposed as fields in spec.lb.envoy.
	// +optional
	DeploymentConfiguration *common.DeploymentConfiguration `json:"deploymentConfiguration,omitempty"`
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
	// TLS configuration specific to the router. If not specified, falls back to the top-level external.tls config.
	// +optional
	TLS *ExternalMongodTLS `json:"tls,omitempty"`
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
}

type MongoDBSearchStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
	// LoadBalancer reports the state of the operator-managed load balancer.
	// Only populated when spec.lb.mode == Managed.
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
	return s.Spec.LoadBalancer != nil && s.Spec.LoadBalancer.Mode == LBModeUnmanaged
}

// IsReplicaSetUnmanagedLB returns true if this is a ReplicaSet with unmanaged LB configuration.
// An endpoint with a template placeholder ({shardName}) is NOT considered a ReplicaSet endpoint.
func (s *MongoDBSearch) IsReplicaSetUnmanagedLB() bool {
	return s.IsLBModeUnmanaged() &&
		s.Spec.LoadBalancer.Endpoint != "" &&
		!s.HasEndpointTemplate()
}

func (s *MongoDBSearch) GetReplicaSetUnmanagedLBEndpoint() string {
	if !s.IsReplicaSetUnmanagedLB() {
		return ""
	}
	return s.Spec.LoadBalancer.Endpoint
}

// HasEndpointTemplate returns true if the endpoint contains the {shardName} template placeholder
func (s *MongoDBSearch) HasEndpointTemplate() bool {
	if s.Spec.LoadBalancer == nil {
		return false
	}
	return strings.Contains(s.Spec.LoadBalancer.Endpoint, ShardNamePlaceholder)
}

// IsShardedUnmanagedLB returns true if this is a sharded unmanaged LB configuration
// identified by the presence of the {shardName} template placeholder in the endpoint.
func (s *MongoDBSearch) IsShardedUnmanagedLB() bool {
	return s.IsLBModeUnmanaged() && s.HasEndpointTemplate()
}

// GetEndpointForShard returns the endpoint for a specific shard by substituting
// the {shardName} placeholder in the endpoint template.
func (s *MongoDBSearch) GetEndpointForShard(shardName string) string {
	if !s.IsShardedUnmanagedLB() {
		return ""
	}
	return strings.ReplaceAll(s.Spec.LoadBalancer.Endpoint, ShardNamePlaceholder, shardName)
}

func (s *MongoDBSearch) GetReplicas() int {
	if s.Spec.Replicas > 0 {
		return s.Spec.Replicas
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
	return s.Spec.LoadBalancer != nil && s.Spec.LoadBalancer.Mode == LBModeManaged
}

// GetManagedLBEndpoint returns the hostname (port stripped) from spec.lb.endpoint
// when mode is Managed and an endpoint is configured. Returns "" otherwise.
func (s *MongoDBSearch) GetManagedLBEndpoint() string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Endpoint == "" {
		return ""
	}
	return stripPort(s.Spec.LoadBalancer.Endpoint)
}

// GetManagedLBEndpointForShard returns the hostname for a specific shard by resolving
// the {shardName} template in spec.lb.endpoint and stripping the port.
// Returns "" if mode is not Managed or no endpoint is configured.
func (s *MongoDBSearch) GetManagedLBEndpointForShard(shardName string) string {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Endpoint == "" {
		return ""
	}
	endpoint := strings.ReplaceAll(s.Spec.LoadBalancer.Endpoint, ShardNamePlaceholder, shardName)
	return stripPort(endpoint)
}

// stripPort extracts the hostname from a host:port string. If no port is present, returns as-is.
func stripPort(endpoint string) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return host
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

// LoadBalancerServiceName returns the name of the managed Envoy ClusterIP Service for this resource.
func (s *MongoDBSearch) LoadBalancerServiceName() string {
	return s.Name + "-search-lb-svc"
}

// LoadBalancerServerCert returns the namespaced name of the TLS server certificate secret for the
// managed Envoy LB (ReplicaSet). Naming pattern:
//   - With prefix: {prefix}-{name}-search-lb-cert
//   - Without prefix: {name}-search-lb-cert
func (s *MongoDBSearch) LoadBalancerServerCert() types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-cert", s.Name), Namespace: s.Namespace}
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
//   - With prefix: {prefix}-{name}-search-lb-client-cert
//   - Without prefix: {name}-search-lb-client-cert
func (s *MongoDBSearch) LoadBalancerClientCert() types.NamespacedName {
	if s.Spec.Security.TLS != nil && s.Spec.Security.TLS.CertsSecretPrefix != "" {
		return types.NamespacedName{
			Name:      fmt.Sprintf("%s-%s-search-lb-client-cert", s.Spec.Security.TLS.CertsSecretPrefix, s.Name),
			Namespace: s.Namespace,
		}
	}
	return types.NamespacedName{Name: fmt.Sprintf("%s-search-lb-client-cert", s.Name), Namespace: s.Namespace}
}

// LoadBalancerProxyServiceNameForShard returns the per-shard proxy Service name used for SNI routing.
func (s *MongoDBSearch) LoadBalancerProxyServiceNameForShard(shardName string) string {
	return fmt.Sprintf("%s-search-0-%s-proxy-svc", s.Name, shardName)
}
