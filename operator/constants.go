package operator

const (
	// Ops manager config map variables
	OmBaseUrl   = "BASE_URL"
	OmPublicKey = "PUBLIC_API_KEY"
	OmUserName  = "USER_LOGIN"
	OmGroupId   = "GROUP_ID"

	// Variable for agent key stored in the Secret
	AgentKey = "AGENT_API_KEY"

	ContainerName = "automation-agent"

	CreatedByOperator = "CreatedByOmOperator"

	MongoDbStandalone     = "MongoDbStandalone"
	MongoDbReplicaSet     = "MongoDbReplicaSet"
	MongoDbShardedCluster = "MongoDbShardedCluster"

	MongoDbDefaultPort = 27017

	ResourceName = "MongoDB"

	// Configuration keys (must match the properties in files in config directory)
	Mode                           = "mode"
	AutomationAgentImageUrl        = "AUTOMATION_AGENT_IMAGE"
	AutomationAgentImagePullPolicy = "AUTOMATION_AGENT_PULL_POLICY"
	AutomationAgentPullSecrets     = "AUTOMATION_AGENT_PULL_SECRETS"
)
