package mdb

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

// ShardNameOverride maps a Kubernetes shard StatefulSet name to its automation config identity.
// Use this when the AC _id or replicaSetName for a shard differs from the K8s StatefulSet name.
// Entries are matched by ShardName so order is not significant. Entries are immutable once set.
//
// Two forms are supported:
// 1. ShardName only (brevity): all three values (_id, replicaSetName, K8s name) are equal.
// 2. ShardName + ShardId + ReplicaSetName: full form when any AC value differs from the K8s name.
//
// ShardId and ReplicaSetName must always be set together or omitted together.
type ShardNameOverride struct {
	// ShardName is the operator-generated Kubernetes StatefulSet name for this shard (e.g. "my-mdb-0").
	// It is always computed as {resourceName}-{index} and cannot be changed.
	// Use this field to identify which shard the AC override applies to.
	// +kubebuilder:validation:Required
	ShardName string `json:"shardName"`

	// ShardId is the shard _id in the automation config sharding section.
	// Must be set together with ReplicaSetName. Omit when AC values match the K8s StatefulSet name.
	// +optional
	ShardId string `json:"shardId,omitempty"`

	// ReplicaSetName is the replicaSetName in the automation config for this shard's processes.
	// Must be set together with ShardId. Omit when AC values match the K8s StatefulSet name.
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

	// ShardNameOverrides maps shard K8s StatefulSet names to their automation config identities.
	// Entries are matched by ShardName so order is not significant. Entries are immutable once set.
	// +optional
	ShardNameOverrides []ShardNameOverride `json:"shardNameOverrides,omitempty"`

	// ConfigServerNameOverride overrides the automation config replicaSetName for the config server.
	// When omitted, the default ("{resourceName}-config") is used. Immutable once set.
	// +optional
	ConfigServerNameOverride string `json:"configServerNameOverride,omitempty"`

	// ShardedClusterNameOverride overrides the automation config sharded cluster name.
	// When omitted, the default ("{resourceName}") is used. Immutable once set.
	// +optional
	ShardedClusterNameOverride string `json:"shardedClusterNameOverride,omitempty"`

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
