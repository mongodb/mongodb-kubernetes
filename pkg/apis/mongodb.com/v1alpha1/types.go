package v1alpha1

import (
	"fmt"

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
	Version string `json:"mongodb_version"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service     string `json:"service,omitempty"`
	ClusterName string `json:"cluster_name,omitempty"`
	// this is the name of config map containing information about OpsManager connection parameters
	OmConfigName         string              `json:"ops_manager_config"`
	ResourceRequirements MongoDbRequirements `json:"resources"`
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
	Version string `json:"mongodb_version"`
	// this is an optional service, it will get the name "<standaloneName>-service" in case not provided
	Service     string `json:"service,omitempty"`
	ClusterName string `json:"cluster_name,omitempty"`
	// this is the name of config map containing information about OpsManager connection parameters
	OmConfigName         string              `json:"ops_manager_config"`
	ResourceRequirements MongoDbRequirements `json:"resources"`
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
	ShardsCount       int    `json:"shards_count"`
	ShardMongodsCount int    `json:"shard_mongods_count"`
	MongosCount       int    `json:"mongos_count"`
	ConfigServerCount int    `json:"config_server_count"`
	Version           string `json:"mongodb_version"`
	// TODO seems the ObjectMeta contains the field for ClusterName - may be we should use it instead
	ClusterName string `json:"cluster_name, omitempty"`
	// this is an optional service that will be mapped to mongos pods, it will get the name "<clusterName>-svc" in case not provided
	Service string `json:"service, omitempty"`
	// this is the name of config map containing information about OpsManager connection parameters
	OmConfigName         string              `json:"ops_manager_config"`
	ResourceRequirements MongoDbRequirements `json:"resources"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbShardedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbShardedCluster `json:"items"`
}

type MongoDbRequirements struct {
	Cpu          string `json:"cpu,omitempty"`
	Memory       string `json:"memory,omitempty"`
	Storage      string `json:"storage,omitempty"`
	StorageClass string `json:"storage_class,omitempty"`
}

// These are some methods for mongodb objects that calculate some names

// Example hostnames:
// * electron-mongos-1.electron-svc.mongodb.svc.cluster.local
// * electron-config-1.electron-cs.mongodb.svc.cluster.local
// * electron_1-1.electron-sh.mongodb.svc.cluster.local

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
