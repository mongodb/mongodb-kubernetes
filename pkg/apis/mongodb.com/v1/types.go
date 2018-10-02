package v1

import (
	"fmt"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbReplicaSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              MongoDbReplicaSetSpec `json:"spec"`
}

type MongoDbReplicaSetSpec struct {
	Members int    `json:"members"`
	Version string `json:"version"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service     string         `json:"service,omitempty"`
	ClusterName string         `json:"clusterName,omitempty"`
	Persistent  *bool          `json:"persistent,omitempty"`
	PodSpec     MongoDbPodSpec `json:"podSpec,omitempty"`

	Project     string `json:"project"`
	Credentials string `json:"credentials"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbReplicaSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbReplicaSet `json:"items"`
}

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbStandalone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              MongoDbStandaloneSpec `json:"spec"`
}

type MongoDbStandaloneSpec struct {
	Version string `json:"version"`
	// this is an optional service, it will get the name "<standaloneName>-service" in case not provided
	Service     string                 `json:"service,omitempty"`
	ClusterName string                 `json:"clusterName,omitempty"`
	Persistent  *bool                  `json:"persistent,omitempty"`
	PodSpec     MongoDbPodSpecStandard `json:"podSpec,omitempty"`
	Project     string                 `json:"project"`
	Credentials string                 `json:"credentials"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbStandaloneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbStandalone `json:"items"`
}

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbShardedCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              MongoDbShardedClusterSpec `json:"spec"`
}

type MongoDbShardedClusterSpec struct {
	ShardCount           int    `json:"shardCount"`
	MongodsPerShardCount int    `json:"mongodsPerShardCount"`
	MongosCount          int    `json:"mongosCount"`
	ConfigServerCount    int    `json:"configServerCount"`
	Version              string `json:"version"`

	// TODO seems the ObjectMeta contains the field for ClusterName - may be we should use it instead
	ClusterName string `json:"clusterName,omitempty"`
	// this is an optional service that will be mapped to mongos pods, it will get the name "<clusterName>-svc" in case not provided
	Service string `json:"service,omitempty"`

	Persistent       *bool          `json:"persistent,omitempty"`
	ConfigSrvPodSpec MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     MongoDbPodSpec `json:"shardPodSpec,omitempty"`

	// Project is a grouping mechanism that is related to the concept of
	// a project in Ops Manager.
	Project string `json:"project"`

	// Credentials is a Secret object containing Ops/Cloud Manager API credentials (User and Public API Key).
	Credentials string `json:"credentials"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbShardedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbShardedCluster `json:"items"`
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

// TopologyKey is not used for standalones so we have to separate different spec schemas
type MongoDbPodSpec struct {
	MongoDbPodSpecStandard
	PodAntiAffinityTopologyKey string `json:"podAntiAffinityTopologyKey"`
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

// These are some methods for mongodb objects that calculate some names

// Example hostnames for sharded cluster:
// * electron-mongos-1.electron-svc.mongodb.svc.cluster.local
// * electron-config-1.electron-cs.mongodb.svc.cluster.local
// * electron-1-1.electron-sh.mongodb.svc.cluster.local

func (c *MongoDbShardedCluster) MongosServiceName() string {
	return getServiceOrDefault(c.Spec.Service, c.Name, "-svc")
}

func (c *MongoDbShardedCluster) ConfigSrvServiceName() string {
	return c.Name + "-cs"
}

func (c *MongoDbShardedCluster) ShardServiceName() string {
	return c.Name + "-sh"
}

func (c *MongoDbShardedCluster) MongosRsName() string {
	return c.Name + "-mongos"
}

func (c *MongoDbShardedCluster) ConfigRsName() string {
	return c.Name + "-config"
}

func (c *MongoDbShardedCluster) ShardRsName(i int) string {
	// Unfortunately the pattern used by OM (name_idx) doesn't work as Kubernetes doesn't create the stateful set with an
	// exception: "a DNS-1123 subdomain must consist of lower case alphanumeric characters, '-' or '.'"
	return fmt.Sprintf("%s-%d", c.Name, i)
}

func (c *MongoDbReplicaSet) ServiceName() string {
	return getServiceOrDefault(c.Spec.Service, c.Name, "-svc")
}

func (c *MongoDbStandalone) ServiceName() string {
	return getServiceOrDefault(c.Spec.Service, c.Name, "-svc")
}

func getServiceOrDefault(service, objectName, suffix string) string {
	if service == "" {
		return objectName + suffix
	}
	return service
}

func GetStorageOrDefault(config, defaultConfig *PersistenceConfig) string {
	if config == nil || config.Storage == "" {
		return defaultConfig.Storage
	}
	return config.Storage
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
