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
	Status MongoDbShardedClusterStatus `json:"status,omitempty"`
}

// MongodbShardedClusterSizeConfig describes the numbers and sizes of replica sets inside
// sharded cluster
type MongodbShardedClusterSizeConfig struct {
	ShardCount           int `json:"shardCount"`
	MongodsPerShardCount int `json:"mongodsPerShardCount"`
	MongosCount          int `json:"mongosCount"`
	ConfigServerCount    int `json:"configServerCount"`
}

// MongoDbShardedClusterSpec desired status of MongoDB Sharded Cluster
type MongoDbShardedClusterSpec struct {
	CommonSpec
	MongodbShardedClusterSizeConfig
	Version string `json:"version"`

	ConfigSrvPodSpec MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     MongoDbPodSpec `json:"shardPodSpec,omitempty"`
}

// MongoDbShardedClusterStatus defines the observed state of MongoDbShardedCluster
type MongoDbShardedClusterStatus struct {
	MongodbShardedClusterSizeConfig
	Version string `json:"version"`

	// TODO
	State string `json:"state"`
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

// UpdateSuccessful
func (c *MongoDbShardedCluster) UpdateSuccessful() {
	c.Status.Version = c.Spec.Version

	c.Status.MongosCount = c.Spec.MongosCount
	c.Status.MongodsPerShardCount = c.Spec.MongodsPerShardCount
	c.Status.ConfigServerCount = c.Spec.ConfigServerCount
	c.Status.ShardCount = c.Spec.ShardCount

	c.Status.State = StateRunning
}

// UpdateError
func (c *MongoDbShardedCluster) UpdateError(msg string) {
	c.Status.State = StateFailed
}

// GetStatus
func (c *MongoDbShardedCluster) GetStatus() string {
	return c.Status.State
}

// IsEmpty will check this is an "Empty" object
func (c *MongoDbShardedCluster) IsEmpty() bool {
	return c.Spec.ShardCount == 0 &&
		c.Spec.MongosCount == 0 &&
		c.Spec.MongodsPerShardCount == 0 &&
		c.Spec.ConfigServerCount == 0
}
