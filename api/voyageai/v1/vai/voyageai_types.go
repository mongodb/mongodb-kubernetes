package vai

import (
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

func init() {
	SchemeBuilder.Register(&VoyageAI{}, &VoyageAIList{})
}

// VoyageAIModel defines the supported VoyageAI model types.
// +kubebuilder:validation:Enum=voyage-4;voyage-4-lite;rerank-2.5;rerank-2.5-lite
type VoyageAIModel string

const (
	VoyageAIModelVoyage4      VoyageAIModel = "voyage-4"
	VoyageAIModelVoyage4Lite  VoyageAIModel = "voyage-4-lite"
	VoyageAIModelRerank25     VoyageAIModel = "rerank-2.5"
	VoyageAIModelRerank25Lite VoyageAIModel = "rerank-2.5-lite"
)

// +k8s:deepcopy-gen=true
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the VoyageAI deployment."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",description="VoyageAI version reconciled by the operator."
// +kubebuilder:printcolumn:name="Model",type="string",JSONPath=".spec.model",description="VoyageAI model."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the VoyageAI resource was created."
// +kubebuilder:resource:path=voyageai,scope=Namespaced,shortName=vai
type VoyageAI struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec VoyageAISpec `json:"spec"`
	// +optional
	Status VoyageAIStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type VoyageAIList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []VoyageAI `json:"items"`
}

type VoyageAISpec struct {
	// Model is the VoyageAI model to deploy.
	// +kubebuilder:validation:Required
	Model VoyageAIModel `json:"model"`

	// Version is the version of the VoyageAI model image.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Replicas is the number of VoyageAI pods to deploy.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// Server configures the VoyageAI server settings.
	// +optional
	// +kubebuilder:default={}
	Server ServerConfig `json:"server,omitempty"`

	// Security configures TLS settings for the VoyageAI server.
	// +optional
	Security Security `json:"security,omitempty"`

	// DataParallel configures data parallel processing settings.
	// +optional
	DataParallel DataParallelConfig `json:"dataParallel,omitempty"`

	// ResourceRequirements configures resource requests and limits for the VoyageAI container.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`

	// NodeAffinity configures node affinity scheduling rules for VoyageAI pods.
	// +optional
	NodeAffinity *corev1.NodeAffinity `json:"nodeAffinity,omitempty"`
}
type ServerConfig struct {
	// Port is the port the VoyageAI server listens on.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	Port int32 `json:"port,omitempty"`

	// Workers is the number of server workers.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Workers int32 `json:"workers,omitempty"`

	// Timeout is the server request timeout in seconds.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=120
	Timeout int32 `json:"timeout,omitempty"`

	// MaxRequests is the maximum number of requests before a worker is restarted.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1000
	MaxRequests int32 `json:"maxRequests,omitempty"`

	// MaxRequestsJitter is the jitter applied to MaxRequests.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=50
	MaxRequestsJitter int32 `json:"maxRequestsJitter,omitempty"`
}

type Security struct {
	// TLS configures TLS for the VoyageAI server. When set, TLS is enabled.
	// +optional
	TLS *TLS `json:"tls,omitempty"`
}

type TLS struct {
	// CertificateKeySecretRef is a reference to a Secret containing a private key and certificate for TLS.
	// The key and cert are expected to be PEM encoded and available at "tls.key" and "tls.crt".
	// +kubebuilder:validation:Required
	CertificateKeySecretRef corev1.LocalObjectReference `json:"certificateKeySecretRef"`

	// CAConfigMapRef is an optional reference to a ConfigMap containing a CA certificate at "ca.crt".
	// +optional
	CAConfigMapRef *corev1.LocalObjectReference `json:"caConfigMapRef,omitempty"`
}

type DataParallelConfig struct {
	// Enabled controls whether data parallel processing is enabled.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// NumWorkers is the number of data parallel workers. Nil means auto.
	// +optional
	NumWorkers *int32 `json:"numWorkers,omitempty"`

	// LoadBalancingStrategy is the strategy for distributing work across workers.
	// +optional
	// +kubebuilder:validation:Enum=round_robin;least_loaded;token_aware
	// +kubebuilder:default=token_aware
	LoadBalancingStrategy string `json:"loadBalancingStrategy,omitempty"`

	// WorkerInitTimeoutSeconds is the timeout in seconds for worker initialization.
	// +optional
	// +kubebuilder:default=600
	WorkerInitTimeoutSeconds int32 `json:"workerInitTimeoutSeconds,omitempty"`

	// WorkerExecutionTimeoutSeconds is the timeout in seconds for worker execution.
	// +optional
	// +kubebuilder:default=30
	WorkerExecutionTimeoutSeconds int32 `json:"workerExecutionTimeoutSeconds,omitempty"`

	// WorkerQueueMaxSize is the maximum size of the worker queue.
	// +optional
	// +kubebuilder:default=100
	WorkerQueueMaxSize int32 `json:"workerQueueMaxSize,omitempty"`

	// Batching configures batching settings for data parallel processing.
	// +optional
	Batching *BatchingConfig `json:"batching,omitempty"`

	// HealthMonitoring configures health monitoring settings for data parallel workers.
	// +optional
	HealthMonitoring *HealthMonitoringConfig `json:"healthMonitoring,omitempty"`
}

type BatchingConfig struct {
	// Strategy is the batching strategy to use.
	// +optional
	// +kubebuilder:validation:Enum=simple;time_window
	Strategy string `json:"strategy,omitempty"`

	// MaxWaitTimeMs is the maximum time in milliseconds to wait for a batch to fill.
	// +optional
	// +kubebuilder:default=10
	MaxWaitTimeMs int32 `json:"maxWaitTimeMs,omitempty"`

	// MaxQueueSize is the maximum number of items in the batch queue.
	// +optional
	// +kubebuilder:default=2000
	MaxQueueSize int32 `json:"maxQueueSize,omitempty"`
}

type HealthMonitoringConfig struct {
	// CheckIntervalSeconds is the interval in seconds between health checks.
	// +optional
	// +kubebuilder:default=5
	CheckIntervalSeconds int32 `json:"checkIntervalSeconds,omitempty"`

	// MaxConsecutiveTimeouts is the maximum number of consecutive timeouts before a worker is considered unhealthy.
	// +optional
	// +kubebuilder:default=3
	MaxConsecutiveTimeouts int32 `json:"maxConsecutiveTimeouts,omitempty"`

	// EnableActiveChecks controls whether active health checks are enabled. Nil means use default.
	// +optional
	EnableActiveChecks *bool `json:"enableActiveChecks,omitempty"`

	// ActiveCheckIntervalSeconds is the interval in seconds between active health checks.
	// +optional
	// +kubebuilder:default=60
	ActiveCheckIntervalSeconds int32 `json:"activeCheckIntervalSeconds,omitempty"`

	// ActiveCheckTimeoutSeconds is the timeout in seconds for active health checks.
	// +optional
	// +kubebuilder:default=5
	ActiveCheckTimeoutSeconds int32 `json:"activeCheckTimeoutSeconds,omitempty"`

	// MaxRestartAttempts is the maximum number of restart attempts for an unhealthy worker.
	// +optional
	// +kubebuilder:default=3
	MaxRestartAttempts int32 `json:"maxRestartAttempts,omitempty"`

	// RestartCooldownSeconds is the cooldown period in seconds between restart attempts.
	// +optional
	// +kubebuilder:default=30
	RestartCooldownSeconds int32 `json:"restartCooldownSeconds,omitempty"`
}

type VoyageAIStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

func (v *VoyageAI) GetCommonStatus(options ...status.Option) *status.Common {
	return &v.Status.Common
}

func (v *VoyageAI) GetStatus(options ...status.Option) interface{} {
	return v.Status
}

func (v *VoyageAI) GetStatusPath(options ...status.Option) string {
	return "/status"
}

func (v *VoyageAI) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	v.Status.Warnings = warnings
}

func (v *VoyageAI) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	v.Status.UpdateCommonFields(phase, v.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		v.Status.Warnings = append(v.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}
	if option, exists := status.GetOption(statusOptions, VoyageAIVersionOption{}); exists {
		v.Status.Version = option.(VoyageAIVersionOption).Version
	}
}

func (v *VoyageAI) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: v.Name, Namespace: v.Namespace}
}

// IsTLSConfigured returns true if TLS is enabled (TLS struct is present).
func (v *VoyageAI) IsTLSConfigured() bool {
	return v.Spec.Security.TLS != nil
}
