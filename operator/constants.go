package operator

const (
	// Ops manager config map variables
	OmBaseUrl   = "BASE_URL"
	OmPublicKey = "PUBLIC_API_KEY"
	OmUserName  = "USER_LOGIN"
	OmGroupId   = "GROUP_ID"

	// Variable for agent key stored in the Secret
	AgentKey = "AGENT_API_KEY"

	ContainerName            = "ops-manager-agent"
	ContainerImage           = "quay.io/rodrigo_valin/automation-agent:latest"
	ContainerImagePullPolicy = "Always"

	CreatedByOperator = "CreatedByOmOperator"

	MongoDbStandalone     = "MongoDbStandalone"
	MongoDbReplicaSet     = "MongoDbReplicaSet"
	MongoDbShardedCluster = "MongoDbShardedCluster"

	MongoDbDefaultPort = 27017

	ResourceName = "MongoDB"
)
