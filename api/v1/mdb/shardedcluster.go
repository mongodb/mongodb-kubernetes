package mdb

// ShardedClusterSpec is the spec consisting of configuration specific for sharded cluster only
type ShardedClusterSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	ConfigSrvSpec *ShardedClusterComponentSpec `json:"configSrv,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	MongosSpec *ShardedClusterComponentSpec `json:"mongos,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	ShardSpec *ShardedClusterComponentSpec `json:"shard,omitempty"`

	// TODO should pod/statefulset specs for ShardedCluster be moved to "mongos,shard,configSrv" sections above?

	ConfigSrvPodSpec *MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    *MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     *MongoDbPodSpec `json:"shardPodSpec,omitempty"`
}

type ShardedClusterComponentSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	AdditionalMongodConfig AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
	Agent                  AgentConfig            `json:"agent,omitempty"`
}

func (s *ShardedClusterComponentSpec) GetAdditionalMongodConfig() AdditionalMongodConfig {
	if s == nil {
		return AdditionalMongodConfig{Object: nil}
	}

	if s.AdditionalMongodConfig.Object == nil {
		return AdditionalMongodConfig{Object: nil}
	}

	return s.AdditionalMongodConfig
}

func (s *ShardedClusterComponentSpec) GetAgentConfig() AgentConfig {
	if s == nil {
		return AgentConfig{
			StartupParameters: StartupParameters{},
		}
	}
	return s.Agent
}

// MongodbShardedClusterSizeConfig describes the numbers and sizes of replica sets inside
// sharded cluster
type MongodbShardedClusterSizeConfig struct {
	ShardCount           int `json:"shardCount,omitempty"`
	MongodsPerShardCount int `json:"mongodsPerShardCount,omitempty"`
	MongosCount          int `json:"mongosCount,omitempty"`
	ConfigServerCount    int `json:"configServerCount,omitempty"`
}
