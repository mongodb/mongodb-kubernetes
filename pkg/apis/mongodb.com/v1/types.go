package v1

import (
	"fmt"

	"encoding/json"

	"reflect"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	SchemeBuilder.Register(&MongoDB{}, &MongoDBList{})
}

type LogLevel string
type Phase string

type ResourceType string

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

	Standalone     ResourceType = "Standalone"
	ReplicaSet     ResourceType = "ReplicaSet"
	ShardedCluster ResourceType = "ShardedCluster"
)

// Seems this should be removed as soon as CLOUDP-35934 is resolved
var AllLogLevels = []LogLevel{Debug, Info, Warn, Error, Fatal}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
type MongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            MongoDbStatus `json:"status"`
	Spec              MongoDbSpec   `json:"spec"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDB `json:"items"`
}

type MongoDbStatus struct {
	MongodbShardedClusterSizeConfig
	Members        int          `json:"members,omitempty"`
	Version        string       `json:"version"`
	Phase          Phase        `json:"phase"`
	Message        string       `json:"message,omitempty"`
	Link           string       `json:"link,omitempty"`
	LastTransition string       `json:"lastTransition,omitempty"`
	ResourceType   ResourceType `json:"type"`
}

type MongoDbSpec struct {
	Version string `json:"version"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`

	// ExposedExternally determines whether a NodePort service should be created for the deployment
	ExposedExternally bool `json:"exposedExternally"`

	// TODO seems the ObjectMeta contains the field for ClusterName - may be we should use it instead
	ClusterName  string       `json:"clusterName,omitempty"`
	Persistent   *bool        `json:"persistent,omitempty"`
	LogLevel     LogLevel     `json:"logLevel,omitempty"`
	Project      string       `json:"project"`
	Credentials  string       `json:"credentials"`
	ResourceType ResourceType `json:"type"`
	// sharded cluster
	ConfigSrvPodSpec *MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    *MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     *MongoDbPodSpec `json:"shardPodSpec,omitempty"`
	MongodbShardedClusterSizeConfig

	// replica set
	Members int             `json:"members,omitempty"`
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`
}

// when we marshal a MongoDB, we don't want to marshal any "empty" fields
// by setting them to nil, they will be left out with `omitempty`
func (m *MongoDB) MarshalJSON() ([]byte, error) {
	type MongoDBJSON MongoDB

	mdb := m.DeepCopyObject().(*MongoDB) // prevent mutation of the original object
	if reflect.DeepEqual(m.Spec.PodSpec, newMongoDbPodSpec()) {
		mdb.Spec.PodSpec = nil
	}
	if mdb.Spec.ResourceType == ShardedCluster {
		if reflect.DeepEqual(m.Spec.ConfigSrvPodSpec, newMongoDbPodSpec()) {
			mdb.Spec.ConfigSrvPodSpec = nil
		}
		if reflect.DeepEqual(m.Spec.MongosPodSpec, newMongoDbPodSpec()) {
			mdb.Spec.MongosPodSpec = nil
		}
		if reflect.DeepEqual(m.Spec.ShardPodSpec, newMongoDbPodSpec()) {
			mdb.Spec.ShardPodSpec = nil
		}
	}
	return json.Marshal((MongoDBJSON)(*mdb))
}

// when unmarshaling a MongoDB instance, we don't want to have any nil references
// these are replaced with an empty instance to prevent nil references
func (m *MongoDB) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDB
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}
	m.InitDefaults()
	return nil
}

func (m *MongoDB) ServiceName() string {
	if m.Spec.Service == "" {
		return m.Name + "-svc"
	}
	return m.Spec.Service
}

func (m *MongoDB) ConfigSrvServiceName() string {
	return m.Name + "-cs"
}

func (m *MongoDB) ShardServiceName() string {
	return m.Name + "-sh"
}

func (m *MongoDB) MongosRsName() string {
	return m.Name + "-mongos"
}

func (m *MongoDB) ConfigRsName() string {
	return m.Name + "-config"
}

func (m *MongoDB) ShardRsName(i int) string {
	// Unfortunately the pattern used by OM (name_idx) doesn't work as Kubernetes doesn't create the stateful set with an
	// exception: "a DNS-1123 subdomain must consist of lower case alphanumeric characters, '-' or '.'"
	return fmt.Sprintf("%s-%d", m.Name, i)
}

func (m *MongoDB) UpdateError(msg string) {
	m.Status.Message = msg
	m.Status.LastTransition = util.Now()
	m.Status.Phase = PhaseFailed
}

func (m *MongoDB) UpdateSuccessful(deploymentLink string, reconciledResource *MongoDB) {
	spec := reconciledResource.Spec

	// assign all fields common to the different resource types
	m.Status.Version = spec.Version
	m.Status.Message = ""
	m.Status.Link = deploymentLink
	m.Status.LastTransition = util.Now()
	m.Status.Phase = PhaseRunning
	m.Status.ResourceType = spec.ResourceType

	switch spec.ResourceType {
	case ReplicaSet:
		m.Status.Members = spec.Members
	case ShardedCluster:
		m.Status.MongosCount = spec.MongosCount
		m.Status.MongodsPerShardCount = spec.MongodsPerShardCount
		m.Status.ConfigServerCount = spec.ConfigServerCount
		m.Status.ShardCount = spec.ShardCount
	}
}

// InitDefaults prevents any references from having nil values.
// should not be called directly, used in tests and marshalling
func (m *MongoDB) InitDefaults() {
	// al resources have a pod spec
	if m.Spec.PodSpec == nil {
		m.Spec.PodSpec = newMongoDbPodSpec()
	}
	if m.Spec.ResourceType == ShardedCluster {
		if m.Spec.ConfigSrvPodSpec == nil {
			m.Spec.ConfigSrvPodSpec = newMongoDbPodSpec()
		}
		if m.Spec.MongosPodSpec == nil {
			m.Spec.MongosPodSpec = newMongoDbPodSpec()
		}
		if m.Spec.ShardPodSpec == nil {
			m.Spec.ShardPodSpec = newMongoDbPodSpec()
		}
	}
}

func (m *MongoDB) ObjectKey() client.ObjectKey {
	return client.ObjectKey{Name: m.Name, Namespace: m.Namespace}
}

// MongodbShardedClusterSizeConfig describes the numbers and sizes of replica sets inside
// sharded cluster
type MongodbShardedClusterSizeConfig struct {
	ShardCount           int `json:"shardCount,omitempty"`
	MongodsPerShardCount int `json:"mongodsPerShardCount,omitempty"`
	MongosCount          int `json:"mongosCount,omitempty"`
	ConfigServerCount    int `json:"configServerCount,omitempty"`
}

type MongoDbPodSpec struct {
	MongoDbPodSpecStandard
	PodAntiAffinityTopologyKey string `json:"podAntiAffinityTopologyKey,omitempty"`
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

// Create a MongoDbPodSpec reference without any nil references
// used to initialize any MongoDbPodSpec fields with valid values
// in order to prevent panicking at runtime.
func newMongoDbPodSpec() *MongoDbPodSpec {
	return &MongoDbPodSpec{
		MongoDbPodSpecStandard: MongoDbPodSpecStandard{},
	}
}
