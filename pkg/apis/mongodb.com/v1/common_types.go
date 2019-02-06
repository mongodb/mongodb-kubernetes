package v1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TODO rename the file to "common_types.go" later

type LogLevel string
type Phase string

const (
	Debug LogLevel = "DEBUG"
	Info  LogLevel = "INFO"
	Warn  LogLevel = "WARN"
	Error LogLevel = "ERROR"
	Fatal LogLevel = "FATAL"

	// PhasePending means the reconciliation has begun for the Mongodb Resource
	PhasePending Phase = "Pending"

	// PhaseRunning means the Mongodb Resource is in a running state
	PhaseRunning Phase = "Running"

	// PhaseFailed means the Mongodb Resource is in a failed state
	PhaseFailed Phase = "Failed"
)

// Seems this should be removed as soon as CLOUDP-35934 is resolved
var AllLogLevels = []LogLevel{Debug, Info, Warn, Error, Fatal}

// MongoDbResource is the interface that represents a MongoDB Resource. An implementation must provide
// functions which determine if the resource needs to be reconciled or not.
type MongoDbResource interface {
	runtime.Object
	// we update with the reconciled resource not a freshly retrieved resource.
	// we do this to prevent against concurrent modification bugs.
	UpdateSuccessful(deploymentLink string, reconciledResource MongoDbResource)
	UpdateError(errorMessage string)
	GetCommonStatus() *CommonStatus
	GetMeta() *Meta
}

type Meta struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
}

func (m *Meta) ObjectKey() client.ObjectKey {
	return client.ObjectKey{Name: m.Name, Namespace: m.Namespace}
}

// CommonSpec includes fields common for all Mongodb types
type CommonSpec struct {
	Version string `json:"version"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`
	// TODO seems the ObjectMeta contains the field for ClusterName - may be we should use it instead
	ClusterName string   `json:"clusterName,omitempty"`
	Persistent  *bool    `json:"persistent,omitempty"`
	LogLevel    LogLevel `json:"logLevel,omitempty"`
	Project     string   `json:"project"`
	Credentials string   `json:"credentials"`
}

// CommonStatus includes fields common for all status types
type CommonStatus struct {
	Version        string `json:"version"`
	Phase          Phase  `json:"phase"`
	Message        string `json:"message,omitempty"`
	Link           string `json:"link,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
}

type MongoDbPodSpec struct {
	MongoDbPodSpecStandard
	PodAntiAffinityTopologyKey string `json:"podAntiAffinityTopologyKey"`
}

// This is a struct providing the opportunity to customize the pod created under the hood.
// It naturally delegates to inner object and provides some defaults that can be overriden in each specific case
type PodSpecWrapper struct {
	MongoDbPodSpec
	// These are the default values, unfortunately Golang doesn't provide the possibility to inline default values into
	// structs so use the operator.NewDefaultPodSpec constructor for this
	Default MongoDbPodSpec
}

type MongoDbPodSpecStandard struct {
	Cpu             string                 `json:"cpu,omitempty"`
	Memory          string                 `json:"memory,omitempty"`
	PodAffinity     *v1.PodAffinity        `json:"podAffinity,omitempty"`
	NodeAffinity    *v1.NodeAffinity       `json:"nodeAffinity,omitempty"`
	SecurityContext *v1.PodSecurityContext `json:"securityContext,omitempty"`
	Persistence     *Persistence           `json:"persistence,omitempty"`

	// Deprecated: deprecated as of 0.4 and will be removed eventually in next releases. Use Persistence struct instead
	Storage string `json:"storage,omitempty"`
	// Deprecated: deprecated as of 0.4 and will be removed eventually in next releases. Use Persistence struct instead
	StorageClass string `json:"storageClass,omitempty"`
}

type Persistence struct {
	SingleConfig   *PersistenceConfig         `json:"single,omitempty"`
	MultipleConfig *MultiplePersistenceConfig `json:"multiple,omitempty"`
}

type MultiplePersistenceConfig struct {
	Data    *PersistenceConfig `json:"data,omitempty"`
	Journal *PersistenceConfig `json:"journal,omitempty"`
	Logs    *PersistenceConfig `json:"logs,omitempty"`
}

type PersistenceConfig struct {
	Storage       string                `json:"storage,omitempty"`
	StorageClass  *string               `json:"storageClass,omitempty"`
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

func (p PodSpecWrapper) GetCpuOrDefault() string {
	if p.Cpu == "" {
		return p.Default.Cpu
	}
	return p.Cpu
}

func (p PodSpecWrapper) GetMemoryOrDefault() string {
	if p.Memory == "" {
		return p.Default.Memory
	}
	return p.Memory
}

func (p PodSpecWrapper) GetTopologyKeyOrDefault() string {
	if p.PodAntiAffinityTopologyKey == "" {
		return p.Default.PodAntiAffinityTopologyKey
	}
	return p.PodAntiAffinityTopologyKey
}

func (p PodSpecWrapper) SetCpu(cpu string) PodSpecWrapper {
	p.Cpu = cpu
	return p
}

func (p PodSpecWrapper) SetMemory(memory string) PodSpecWrapper {
	p.Memory = memory
	return p
}

func (p PodSpecWrapper) SetTopology(topology string) PodSpecWrapper {
	p.PodAntiAffinityTopologyKey = topology
	return p
}

func GetStorageOrDefault(config, defaultConfig *PersistenceConfig) string {
	if config == nil || config.Storage == "" {
		return defaultConfig.Storage
	}
	return config.Storage
}

func getServiceOrDefault(service, objectName, suffix string) string {
	if service == "" {
		return objectName + suffix
	}
	return service
}
