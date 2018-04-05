package operator

const (
	ContainerName            = "ops-manager-agent"
	ContainerImage           = "quay.io/rodrigo_valin/automation-agent:latest"
	ContainerImagePullPolicy = "Always"

	CreatedByOperator = "CreatedByOmOperator"

	ContainerConfigMapName = "ops-manager-config"

	MongoDbStandalone     = "MongoDbStandalone"
	MongoDbReplicaSet     = "MongoDbReplicaSet"
	MongoDbShardedCluster = "MongoDbShardedCluster"

	MongoDbDefaultPort = 27017

	ResourceName = "MongoDB"

	AgentKey = "AGENT_API_KEY"
)
