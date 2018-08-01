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
	Service     string                   `json:"service,omitempty"`
	ClusterName string                   `json:"clusterName,omitempty"`
	Persistent  *bool                    `json:"persistent,omitempty"`
	PodSpec     MongoDbPodSpecStandalone `json:"podSpec,omitempty"`
	Project     string                   `json:"project"`
	Credentials string                   `json:"credentials"`
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

// This is a struct providing the opportunity to customize the pod created under the hood. If it grows it may make sense
// to separate properties further (e.g. resources/affinity etc) but now it seems to look nice as a flat structure
// It naturally delegates to inner object and provides some defaults that can be overriden in each specific case
type PodSpecWrapper struct {
	MongoDbPodSpec
	// These are the default values, unfortunately Golang doesn't provide the possibility to inline default values into
	// structs so use the operator.NewDefaultPodSpec constructor for this
	Default MongoDbPodSpec
}

type MongoDbPodSpecStandalone struct {
	Cpu          string           `json:"cpu,omitempty"`
	Memory       string           `json:"memory,omitempty"`
	Storage      string           `json:"storage,omitempty"`
	StorageClass string           `json:"storageClass,omitempty"`
	NodeAffinity *v1.NodeAffinity `json:"nodeAffinity,omitempty"`
	PodAffinity  *v1.PodAffinity  `json:"podAffinity,omitempty"`
}

// Note that we make topologyKey a required attribute as it is a mandatory attribute to create a pod anti affinity rule
// (used only for replicated stateful sets, so not applicable for standalones)
type MongoDbPodSpec struct {
	MongoDbPodSpecStandalone
	PodAntiAffinityTopologyKey string `json:"podAntiAffinityTopologyKey"`
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

func (p PodSpecWrapper) GetStorageOrDefault() string {
	if p.Storage == "" {
		return p.Default.Storage
	}
	return p.Storage
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
func (p PodSpecWrapper) SetStorage(storage string) PodSpecWrapper {
	p.Storage = storage
	return p
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
