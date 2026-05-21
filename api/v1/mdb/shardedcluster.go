package mdb

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

// ShardNameOverride maps one Kubernetes shard to its automation config identity during a VM-to-Kubernetes migration.
// The slice is position-significant: index 0 targets shard 0, index 1 targets shard 1, and so on.
// Shards beyond the slice length keep their K8s default names.
// Once set, entries are immutable and must not be reordered or extended.
// ShardName is the override name used to reference this shard in shardOverrides.shardNames.
// ShardId and ReplicaSetName are the corresponding automation config values for this shard.
type ShardNameOverride struct {
	// ShardName is the override name used to reference this shard in shardOverrides.shardNames. Always required.
	ShardName string `json:"shardName,omitempty"`

	// ShardId is the shard _id in the automation config sharding section.
	// Set this only when the shard _id differs from the replicaSetName.
	// +optional
	ShardId string `json:"shardId,omitempty"`

	// ReplicaSetName is the replica set name used in the automation config for this shard's processes.
	// Set this only when the replicaSetName differs from the shard _id.
	// +optional
	ReplicaSetName string `json:"replicaSetName,omitempty"`
}

// ShardedClusterSpec is the spec consisting of configuration specific for sharded cluster only.
type ShardedClusterSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	ConfigSrvSpec *ShardedClusterComponentSpec `json:"configSrv,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	MongosSpec *ShardedClusterComponentSpec `json:"mongos,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	ShardSpec *ShardedClusterComponentSpec `json:"shard,omitempty"`
	// ShardOverrides allow for overriding the configuration of a specific shard.
	// It replaces deprecated spec.shard.shardSpecificPodSpec field. When spec.shard.shardSpecificPodSpec is still defined then
	// spec.shard.shardSpecificPodSpec is applied first to the particular shard and then spec.shardOverrides is applied on top
	// of that (if defined for the same shard).
	// +kubebuilder:pruning:PreserverUnknownFields
	// +optional
	ShardOverrides []ShardOverride `json:"shardOverrides,omitempty"`

	// ShardNameOverrides provides the VM-managed names for shards during a VM-to-Kubernetes migration.
	// The slice is position-significant: index i overrides shard i. Entries are immutable once set.
	// +optional
	ShardNameOverrides []ShardNameOverride `json:"shardNameOverrides,omitempty"`

	ConfigSrvPodSpec *MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    *MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     *MongoDbPodSpec `json:"shardPodSpec,omitempty"`
	// ShardSpecificPodSpec allows you to provide a Statefulset override per shard.
	// DEPRECATED please use spec.shard.shardOverrides instead
	// +optional
	ShardSpecificPodSpec []MongoDbPodSpec `json:"shardSpecificPodSpec,omitempty"`
}

type ShardedClusterComponentSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	AdditionalMongodConfig *AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
	// Configuring logRotation is not allowed under this section.
	// Please use the most top level resource fields for this; spec.Agent
	Agent           AgentConfig     `json:"agent,omitempty"`
	ClusterSpecList ClusterSpecList `json:"clusterSpecList,omitempty"`
}

type ShardedClusterComponentOverrideSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	AdditionalMongodConfig *AdditionalMongodConfig   `json:"additionalMongodConfig,omitempty"`
	Agent                  *AgentConfig              `json:"agent,omitempty"`
	ClusterSpecList        []ClusterSpecItemOverride `json:"clusterSpecList,omitempty"`
}

type ShardOverride struct {
	// +kubebuilder:validation:MinItems=1
	ShardNames []string `json:"shardNames"`

	ShardedClusterComponentOverrideSpec `json:",inline"`

	// The following override fields work for SingleCluster only. For MultiCluster - fields from specific clusters are used.
	// +optional
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`

	// Number of member nodes in this shard. Used only in SingleCluster. For MultiCluster the number of members is specified in ShardOverride.ClusterSpecList.
	// +optional
	Members *int `json:"members"`
	// Process configuration override for this shard. Used in SingleCluster only. The number of items specified must be >= spec.mongodsPerShardCount or spec.shardOverride.members.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	MemberConfig []automationconfig.MemberOptions `json:"memberConfig,omitempty"`
	// Statefulset override for this particular shard.
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
}

func (s *ShardedClusterComponentSpec) GetAdditionalMongodConfig() *AdditionalMongodConfig {
	if s == nil {
		return &AdditionalMongodConfig{}
	}

	if s.AdditionalMongodConfig == nil {
		return &AdditionalMongodConfig{}
	}

	return s.AdditionalMongodConfig
}

func (s *ShardedClusterComponentSpec) GetAgentConfig() *AgentConfig {
	if s == nil {
		return &AgentConfig{
			StartupParameters: StartupParameters{},
		}
	}
	return &s.Agent
}

func (s *ShardedClusterComponentSpec) ClusterSpecItemExists(clusterName string) bool {
	return s.getClusterSpecItemOrNil(clusterName) != nil
}

func (s *ShardedClusterComponentSpec) GetClusterSpecItem(clusterName string) ClusterSpecItem {
	if clusterSpecItem := s.getClusterSpecItemOrNil(clusterName); clusterSpecItem != nil {
		return *clusterSpecItem
	}

	// it should never occur - we preprocess all clusterSpecLists
	panic(fmt.Errorf("clusterName %s not found in clusterSpecList", clusterName))
}

func (s *ShardedClusterComponentSpec) getClusterSpecItemOrNil(clusterName string) *ClusterSpecItem {
	for i := range s.ClusterSpecList {
		if s.ClusterSpecList[i].ClusterName == clusterName {
			return &s.ClusterSpecList[i]
		}
	}

	return nil
}
