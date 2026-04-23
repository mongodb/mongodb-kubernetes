package mdb

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
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

	// Shards, when specified, declares an explicit list of shards with stable
	// identities. Exactly one of spec.shards or spec.shardCount must be set.
	//
	// When spec.shardCount is used, the operator derives an implicit list of
	// the form [{shardName: "<mdb-name>-0"}, ..., {shardName: "<mdb-name>-(N-1)"}].
	// For an existing cluster created with spec.shardCount, migrating to
	// spec.shards is a no-op at the Kubernetes layer as long as each shardName
	// is set to its corresponding implicit value.
	// +optional
	Shards []Shard `json:"shards,omitempty"`

	ConfigSrvPodSpec *MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    *MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     *MongoDbPodSpec `json:"shardPodSpec,omitempty"`
	// ShardSpecificPodSpec allows you to provide a Statefulset override per shard.
	// DEPRECATED please use spec.shard.shardOverrides instead
	// +optional
	ShardSpecificPodSpec []MongoDbPodSpec `json:"shardSpecificPodSpec,omitempty"`
}

// Shard is an explicit declaration of a single shard in a sharded cluster.
// It is an element of spec.shards.
type Shard struct {
	// ShardName is the stem of every Kubernetes resource created for this
	// shard (StatefulSet, Pods, PVCs, headless Service, secrets, config maps).
	// It must be a DNS-1123 label and is immutable after the shard is created.
	// +kubebuilder:validation:Required
	ShardName string `json:"shardName"`

	// ShardId is the shard identity used in Ops Manager automation config
	// (sharded-cluster shard "_id" and replica-set "_id"/"rs").
	// It defaults to ShardName. Set explicitly only when migrating an existing
	// VM/OM deployment whose shard identifier contains characters that are not
	// valid in a Kubernetes resource name (e.g. underscores).
	// Immutable after the shard is created.
	// +optional
	ShardId string `json:"shardId,omitempty"`
}

// GetShardId returns the effective shard id (defaults to ShardName).
func (s Shard) GetShardId() string {
	if s.ShardId != "" {
		return s.ShardId
	}
	return s.ShardName
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
