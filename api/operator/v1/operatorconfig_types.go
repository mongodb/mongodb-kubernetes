package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FeatureMode controls whether a feature is enabled or disabled.
// +kubebuilder:validation:Enum=Enabled;Disabled
type FeatureMode string

const (
	FeatureModeEnabled  FeatureMode = "Enabled"
	FeatureModeDisabled FeatureMode = "Disabled"
)

// WatchedResource identifies a CRD that the operator will actively reconcile.
// +kubebuilder:validation:Enum=mongodb;opsmanagers;mongodbusers;mongodbcommunity;mongodbsearch;mongodbmulticluster;clustermongodbroles;voyageais
type WatchedResource string

const (
	WatchedResourceMongoDB             WatchedResource = "mongodb"
	WatchedResourceOpsManagers         WatchedResource = "opsmanagers"
	WatchedResourceMongoDBUsers        WatchedResource = "mongodbusers"
	WatchedResourceMongoDBCommunity    WatchedResource = "mongodbcommunity"
	WatchedResourceMongoDBSearch       WatchedResource = "mongodbsearch"
	WatchedResourceMongoDBMultiCluster WatchedResource = "mongodbmulticluster"
	WatchedResourceClusterMongoDBRoles WatchedResource = "clustermongodbroles"
	WatchedResourceVoyageAI            WatchedResource = "voyageais"
)

// AllWatchedResources lists every CRD the operator can reconcile. It is the default value
// for OperatorConfig.spec.watchedResources when the field is omitted.
var AllWatchedResources = []WatchedResource{
	WatchedResourceMongoDB,
	WatchedResourceOpsManagers,
	WatchedResourceMongoDBUsers,
	WatchedResourceMongoDBCommunity,
	WatchedResourceMongoDBSearch,
	WatchedResourceMongoDBMultiCluster,
	WatchedResourceClusterMongoDBRoles,
	WatchedResourceVoyageAI,
}

// Architecture defines the container architecture used by operator-managed workloads.
// +kubebuilder:validation:Enum=NonStatic;Static
type Architecture string

const (
	ArchitectureNonStatic Architecture = "NonStatic"
	ArchitectureStatic    Architecture = "Static"
)

// ProxyEnvPropagationPolicy controls propagation of proxy env vars to managed workloads.
// +kubebuilder:validation:Enum=Propagate;NoPropagation
type ProxyEnvPropagationPolicy string

const (
	ProxyEnvPropagationPolicyPropagate     ProxyEnvPropagationPolicy = "Propagate"
	ProxyEnvPropagationPolicyNoPropagation ProxyEnvPropagationPolicy = "NoPropagation"
)

// ProxyConfig contains proxy environment variable propagation settings.
type ProxyConfig struct {
	// EnvPropagationPolicy controls whether HTTP_PROXY, HTTPS_PROXY, and NO_PROXY
	// are propagated from the operator container to managed workload containers.
	// +optional
	// +kubebuilder:default=NoPropagation
	EnvPropagationPolicy ProxyEnvPropagationPolicy `json:"envPropagationPolicy,omitempty"`
}

// MultiClusterConfig contains multi-cluster configuration settings.
type MultiClusterConfig struct {
	// MemberClusterClientTimeout is the timeout in seconds for connecting to a member
	// cluster's Kubernetes API server.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10
	MemberClusterClientTimeout int `json:"memberClusterClientTimeout,omitempty"`

	// MemberClusterRequiredHealthyStreak is the number of consecutive successful health
	// checks required before a previously failed member cluster is considered recovered
	// and its failed-cluster annotation is removed. Only relevant when automatic failover
	// is disabled.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=5
	MemberClusterRequiredHealthyStreak int `json:"memberClusterRequiredHealthyStreak,omitempty"`
}

// AutomaticRecoveryConfig controls automatic recovery of resources with broken automation configuration.
type AutomaticRecoveryConfig struct {
	// Mode controls whether automatic recovery of resources with broken automation config is active.
	// +optional
	// +kubebuilder:default=Enabled
	Mode FeatureMode `json:"mode,omitempty"`

	// Delay is the back-off in seconds before the operator attempts automatic recovery.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1200
	Delay int `json:"delay,omitempty"`
}

// TelemetryCollectionClustersConfig controls collection of cluster-level telemetry.
type TelemetryCollectionClustersConfig struct {
	// Mode controls collection of cluster-level telemetry.
	// Note: The cluster UUID is unique but random and MongoDB has no way to map it to a customer.
	// +optional
	// +kubebuilder:default=Enabled
	Mode FeatureMode `json:"mode,omitempty"`
}

// TelemetryCollectionDeploymentsConfig controls collection of deployment-level telemetry.
type TelemetryCollectionDeploymentsConfig struct {
	// Mode controls collection of deployment-level telemetry.
	// +optional
	// +kubebuilder:default=Enabled
	Mode FeatureMode `json:"mode,omitempty"`
}

// TelemetryCollectionOperatorsConfig controls collection of operator-level telemetry.
type TelemetryCollectionOperatorsConfig struct {
	// Mode controls collection of operator-level telemetry.
	// +optional
	// +kubebuilder:default=Enabled
	Mode FeatureMode `json:"mode,omitempty"`
}

// TelemetryCollectionConfig controls how telemetry data is collected.
// Collection does not imply sending; see TelemetrySendConfig for transmission settings.
type TelemetryCollectionConfig struct {
	// Frequency controls how often telemetry data is collected and written to the telemetry ConfigMap.
	// Duration using h (hours) and m (minutes) units, e.g. "30m", "1h", "2h30m". Must be at least 1m; defaults to 1h when omitted.
	// +optional
	// +kubebuilder:default="1h"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1m')",message="frequency must be at least 1m"
	Frequency *metav1.Duration `json:"frequency,omitempty"`

	// KubeTimeout is the timeout for Kubernetes API calls made while collecting telemetry.
	// Duration using h (hours), m (minutes) and s (seconds) units, e.g. "30s", "5m". Must be at least 1s; defaults to 5m when omitted.
	// +optional
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1s')",message="kubeTimeout must be at least 1s"
	KubeTimeout *metav1.Duration `json:"kubeTimeout,omitempty"`

	// Clusters controls collection of cluster-level telemetry.
	// +optional
	Clusters *TelemetryCollectionClustersConfig `json:"clusters,omitempty"`

	// Deployments controls collection of deployment-level telemetry.
	// +optional
	Deployments *TelemetryCollectionDeploymentsConfig `json:"deployments,omitempty"`

	// Operators controls collection of operator-level telemetry.
	// +optional
	Operators *TelemetryCollectionOperatorsConfig `json:"operators,omitempty"`
}

// TelemetrySendConfig controls how collected telemetry is sent to MongoDB.
// Disabling send does not prevent local collection.
type TelemetrySendConfig struct {
	// Mode controls sending of collected telemetry to MongoDB for analysis.
	// +optional
	// +kubebuilder:default=Enabled
	Mode FeatureMode `json:"mode,omitempty"`

	// Frequency controls how often collected telemetry is pushed to MongoDB.
	// Duration using h (hours) units, e.g. "24h", "168h". Must be at least 1h; defaults to 168h when omitted.
	// +optional
	// +kubebuilder:default="168h"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1h')",message="frequency must be at least 1h"
	Frequency *metav1.Duration `json:"frequency,omitempty"`
}

// TelemetryConfig configures collection and sending of operator telemetry.
// Absence of this block implies telemetry is enabled.
type TelemetryConfig struct {
	// Mode is the master switch for telemetry. Set to Disabled to disable all
	// telemetry collection and sending.
	// +optional
	// +kubebuilder:default=Enabled
	Mode FeatureMode `json:"mode,omitempty"`

	// Collection controls how telemetry data is collected.
	// +optional
	Collection *TelemetryCollectionConfig `json:"collection,omitempty"`

	// Send controls how collected telemetry is sent to MongoDB.
	// +optional
	Send *TelemetrySendConfig `json:"send,omitempty"`
}

// ReadinessProbeLogConfig configures log rotation for the community operator's readiness probe binary.
type ReadinessProbeLogConfig struct {
	// FilePath is the file path for readiness probe log output.
	// +optional
	// +kubebuilder:default="/var/log/mongodb-mms-automation/readiness.log"
	FilePath string `json:"filePath,omitempty"`

	// FileOutput controls whether readiness probe logs are written to the file
	// in addition to stdout.
	// +optional
	// +kubebuilder:default=Enabled
	FileOutput FeatureMode `json:"fileOutput,omitempty"`

	// FileBackups is the number of rotated log files to retain.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=5
	FileBackups int `json:"fileBackups,omitempty"`

	// FileCompression controls whether retained log files are gzip-compressed.
	// +optional
	// +kubebuilder:default=Disabled
	FileCompression FeatureMode `json:"fileCompression,omitempty"`

	// FileMaxSize is the maximum size in megabytes of a single log file before rotation.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=100
	FileMaxSize int `json:"fileMaxSize,omitempty"`

	// FileMaxAge is the maximum age in days for rotated log files before deletion.
	// 0 means retain indefinitely.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	FileMaxAge int `json:"fileMaxAge,omitempty"`
}

// ReadinessProbeConfig configures the community operator's readiness probe binary.
type ReadinessProbeConfig struct {
	// Log configures log rotation for the community readiness probe binary.
	// +optional
	Log *ReadinessProbeLogConfig `json:"log,omitempty"`
}

// CommunityConfig contains configuration specific to the community operator and its managed resources.
type CommunityConfig struct {
	// ReadinessProbe configures the community operator's readiness probe binary.
	// +optional
	ReadinessProbe *ReadinessProbeConfig `json:"readinessProbe,omitempty"`
}

// OperatorConfigSpec defines the desired state of OperatorConfig.
type OperatorConfigSpec struct {
	// WatchedResources controls which CRDs the operator actively reconciles.
	// Defaults to all known MCK CRDs when omitted.
	// +optional
	// +listType=set
	WatchedResources []WatchedResource `json:"watchedResources,omitempty"`

	// DefaultArchitecture sets the default container architecture for all managed workloads.
	// Can be overridden per-resource.
	// +optional
	// +kubebuilder:default=NonStatic
	DefaultArchitecture Architecture `json:"defaultArchitecture,omitempty"`

	// MaxConcurrentReconciles is the maximum number of concurrent reconciliations per controller.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MaxConcurrentReconciles int `json:"maxConcurrentReconciles,omitempty"`

	// Proxy contains proxy environment variable propagation settings.
	// +optional
	Proxy *ProxyConfig `json:"proxy,omitempty"`

	// MultiCluster contains multi-cluster configuration.
	// +optional
	MultiCluster *MultiClusterConfig `json:"multiCluster,omitempty"`

	// AutomaticRecovery controls automatic recovery of resources with broken
	// automation configuration.
	// +optional
	AutomaticRecovery *AutomaticRecoveryConfig `json:"automaticRecovery,omitempty"`

	// Telemetry configures collection and sending of operator telemetry.
	// Absence of this block implies telemetry is enabled.
	// +optional
	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`

	// Community contains configuration specific to community operator resources.
	// +optional
	Community *CommunityConfig `json:"community,omitempty"`
}

// OperatorConfig configures the behaviour of the MCK operator instance deployed in the same namespace.
// One instance per operator deployment is expected.
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:path=operatorconfigs,scope=Namespaced,shortName=opconfig
type OperatorConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec OperatorConfigSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type OperatorConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OperatorConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OperatorConfig{}, &OperatorConfigList{})
}
