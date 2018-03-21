package operator

const (
	ContainerName            = "ops-manager-agent"
	ContainerImage           = "ops-manager-agent"
	ContainerImagePullPolicy = "Never"

	ContainerConfigMapName = "ops-manager-config"

	MongoDbStandalone     = "MongoDbStandalone"
	MongoDbReplicaSet     = "MongoDbReplicaSet"
	MongoDbShardedCluster = "MongoDbShardedCluster"

	ResourceName = "MongoDB"

	AgentKey = "AGENT_API_KEY"
)
