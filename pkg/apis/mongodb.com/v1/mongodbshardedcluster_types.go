package v1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&MongoDbShardedCluster{}, &MongoDbShardedClusterList{})
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
type MongoDbShardedCluster struct {
	Meta
	Spec   MongoDbShardedClusterSpec   `json:"spec"`
	Status MongoDbShardedClusterStatus `json:"status"`
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

// MongoDbShardedClusterStatus defines the observed state of MongoDbShardedCluster
type MongoDbShardedClusterStatus struct {
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file

	Version string `json:"version"`
	// TODO
	State                string `json:"state"`
	ShardCount           int    `json:"shardCount"`
	MongodsPerShardCount int    `json:"mongodsPerShardCount"`
	MongosCount          int    `json:"mongosCount"`
	ConfigServerCount    int    `json:"configServerCount"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbShardedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbShardedCluster `json:"items"`
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
