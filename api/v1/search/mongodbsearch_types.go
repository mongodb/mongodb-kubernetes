package search

import (
	"fmt"

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
	// LoadBalancer configures how mongod/mongos connect to mongot (Envoy vs External LB).
	// +optional
	LoadBalancer *LoadBalancerConfig `json:"lb,omitempty"`
}

// LBMode defines the load balancer mode for Search
// +kubebuilder:validation:Enum=Envoy;External
type LBMode string

const (
	// LBModeEnvoy indicates operator-managed Envoy load balancer
	LBModeEnvoy LBMode = "Envoy"
	// LBModeExternal indicates user-provided external L7 load balancer
	LBModeExternal LBMode = "External"
)

// LoadBalancerConfig configures how mongod/mongos connect to mongot
type LoadBalancerConfig struct {
	// Mode specifies the load balancer mode: Envoy (operator-managed) or External (BYO L7 LB)
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
	// Endpoint is a single LB endpoint for ReplicaSet or shared sharded LB (host:port)
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Sharded contains per-shard LB endpoint configuration for sharded clusters
	// +optional
	Sharded *ShardedExternalLBConfig `json:"sharded,omitempty"`
}

// ShardedExternalLBConfig contains per-shard LB endpoint configuration
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
	// When Replicas > 1, a load balancer configuration (lb.mode: External with lb.external.endpoint)
	// is required to distribute traffic across mongot instances.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int `json:"replicas,omitempty"`
}

type ExternalMongoDBSource struct {
	HostAndPorts []string `json:"hostAndPorts,omitempty"`
	// mongod keyfile used to connect to the external MongoDB deployment
	KeyFileSecretKeyRef *userv1.SecretKeyRef `json:"keyfileSecretRef,omitempty"`
	// TLS configuration for the external MongoDB deployment
	// +optional
	TLS *ExternalMongodTLS `json:"tls,omitempty"`
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
	CertificateKeySecret corev1.LocalObjectReference `json:"certificateKeySecretRef"`
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

// TLSSecretNamespacedName will get the namespaced name of the Secret containing the server certificate and key
func (s *MongoDBSearch) TLSSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Spec.Security.TLS.CertificateKeySecret.Name, Namespace: s.Namespace}
}

// TLSOperatorSecretNamespacedName will get the namespaced name of the Secret created by the operator
// containing the combined certificate and key.
func (s *MongoDBSearch) TLSOperatorSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: s.Name + "-search-certificate-key", Namespace: s.Namespace}
}

func (s *MongoDBSearch) GetMongotHealthCheckPort() int32 {
	return MongotDefautHealthCheckPort
}

func (s *MongoDBSearch) IsExternalMongoDBSource() bool {
	return s.Spec.Source != nil && s.Spec.Source.ExternalMongoDBSource != nil
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
	return s.Spec.LoadBalancer != nil && s.Spec.LoadBalancer.Mode == LBModeExternal
}

func (s *MongoDBSearch) IsReplicaSetExternalLB() bool {
	return s.IsExternalLBMode() &&
		s.Spec.LoadBalancer.External != nil &&
		s.Spec.LoadBalancer.External.Endpoint != ""
}

func (s *MongoDBSearch) GetReplicaSetExternalLBEndpoint() string {
	if !s.IsReplicaSetExternalLB() {
		return ""
	}
	return s.Spec.LoadBalancer.External.Endpoint
}

func (s *MongoDBSearch) IsShardedExternalLB() bool {
	return s.IsExternalLBMode() &&
		s.Spec.LoadBalancer.External != nil &&
		s.Spec.LoadBalancer.External.Sharded != nil &&
		len(s.Spec.LoadBalancer.External.Sharded.Endpoints) > 0
}

func (s *MongoDBSearch) GetShardEndpointMap() map[string]string {
	result := make(map[string]string)
	if !s.IsShardedExternalLB() {
		return result
	}
	for _, e := range s.Spec.LoadBalancer.External.Sharded.Endpoints {
		result[e.ShardName] = e.Endpoint
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
