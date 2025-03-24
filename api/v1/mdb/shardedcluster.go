package mdb

import (
	"fmt"

	mdbcv1 "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

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
	StatefulSetConfiguration *mdbcv1.StatefulSetConfiguration `json:"statefulSet,omitempty"`
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

func (s *ShardedClusterComponentSpec) GetClusterSpecItem(clusterName string) ClusterSpecItem {
	for i := range s.ClusterSpecList {
		if s.ClusterSpecList[i].ClusterName == clusterName {
			return s.ClusterSpecList[i]
		}
	}
	// it should never occur - we preprocess all clusterSpecLists
	panic(fmt.Errorf("clusterName %s not found in clusterSpecList", clusterName))
}
