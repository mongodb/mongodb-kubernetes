package operator

const (
	// Ops manager config map variables
	OmBaseUrl      = "baseUrl"
	OmProjectId    = "projectId"
	OmUser         = "user"
	OmPublicApiKey = "publicApiKey"
	OmAgentApiKey  = "agentApiKey"

	ENV_VAR_BASE_URL      = "BASE_URL"
	ENV_VAR_PROJECT_ID    = "GROUP_ID"
	ENV_VAR_USER          = "USER_LOGIN"
	ENV_VAR_AGENT_API_KEY = "AGENT_API_KEY"

	ContainerName     = "mongodb-enterprise-database"
	OmControllerLabel = "mongodb-enterprise-operator"

	MongoDbStandalone     = "MongoDbStandalone"
	MongoDbReplicaSet     = "MongoDbReplicaSet"
	MongoDbShardedCluster = "MongoDbShardedCluster"

	ResourceName  = "MongoDB"
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
