package operator

const (
	// Ops manager config map variables
	OmBaseUrl   = "BASE_URL"
	OmPublicKey = "PUBLIC_API_KEY"
	OmUserName  = "USER_LOGIN"
	OmGroupId   = "GROUP_ID"

	// Variable for agent key stored in the Secret
	AgentKey = "AGENT_API_KEY"

	ContainerName     = "mongodb-enterprise-database"
	OmControllerLabel = "mongodb-enterprise-operator"

	MongoDbStandalone     = "MongoDbStandalone"
	MongoDbReplicaSet     = "MongoDbReplicaSet"
	MongoDbShardedCluster = "MongoDbShardedCluster"

	ResourceName = "MongoDB"
	LivenessProbe = "/mongodb-automation/files/probe.sh"

	// Env configuration properties
	AutomationAgentImageUrl        = "AUTOMATION_AGENT_IMAGE"
	AutomationAgentImagePullPolicy = "AUTOMATION_AGENT_PULL_POLICY"
	AutomationAgentPullSecrets     = "AUTOMATION_AGENT_PULL_SECRETS"
	OmOperatorEnv                  = "OM_OPERATOR_ENV"

	// Different default configuration values
	DefaultMongodStorageSize    = "16G"
	DefaultConfigSrvStorageSize = "5G"
	MongoDbDefaultPort          = 27017
	PersistentVolumeClaimName   = "data"
	PersistentVolumePath        = "/data"
)
