package mdb

// ShardedClusterSpec is the spec consisting of configuration specific for sharded cluster only
type ShardedClusterSpec struct {
	ConfigSrvSpec *ShardedClusterComponentSpec `json:"configSrv,omitempty"`
	MongosSpec    *ShardedClusterComponentSpec `json:"mongos,omitempty"`
	ShardSpec     *ShardedClusterComponentSpec `json:"shard,omitempty"`

	// TODO should pod/statefulset specs for ShardedCluster be moved to "mongos,shard,configSrv" sections above?
	ConfigSrvPodSpec *MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    *MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     *MongoDbPodSpec `json:"shardPodSpec,omitempty"`
}

type ShardedClusterComponentSpec struct {
	AdditionalMongodConfig AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
}

// MongodbShardedClusterSizeConfig describes the numbers and sizes of replica sets inside
// sharded cluster
type MongodbShardedClusterSizeConfig struct {
	ShardCount           int `json:"shardCount,omitempty"`
	MongodsPerShardCount int `json:"mongodsPerShardCount,omitempty"`
	MongosCount          int `json:"mongosCount,omitempty"`
	ConfigServerCount    int `json:"configServerCount,omitempty"`
}
