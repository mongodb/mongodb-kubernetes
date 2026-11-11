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
