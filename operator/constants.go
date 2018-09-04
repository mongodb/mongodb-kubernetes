package operator

const (
	// Ops manager config map and secret variables
	OmBaseUrl      = "baseUrl"
	OmOrgId        = "orgId"
	OmProjectName  = "projectName"
	OmUser         = "user"
	OmPublicApiKey = "publicApiKey"
	OmAgentApiKey  = "agentApiKey"

	// Env variables names for pods
	ENV_VAR_BASE_URL      = "BASE_URL"
	ENV_VAR_PROJECT_ID    = "GROUP_ID"
	ENV_VAR_USER          = "USER_LOGIN"
	ENV_VAR_AGENT_API_KEY = "AGENT_API_KEY"

	// Pod specific constants
	ContainerName     = "mongodb-enterprise-database"
	OmControllerLabel = "mongodb-enterprise-operator"
	LivenessProbe     = "/mongodb-automation/files/probe.sh"

	// Operator Env configuration properties
	AutomationAgentImageUrl        = "MONGODB_ENTERPRISE_DATABASE_IMAGE"
	AutomationAgentImagePullPolicy = "IMAGE_PULL_POLICY"
	AutomationAgentPullSecrets     = "IMAGE_PULL_SECRETS"
	OmOperatorEnv                  = "OPERATOR_ENV"
	StatefulSetWaitSecondsEnv      = "STS_WAIT_SEC"
	StatefulSetWaitRetrialsEnv     = "STS_WAIT_RETRIALS"

	// Different default configuration values
	DefaultMongodStorageSize       = "16G"
	DefaultConfigSrvStorageSize    = "5G"
	DefaultAntiAffinityTopologyKey = "kubernetes.io/hostname"
	MongoDbDefaultPort             = 27017
	PersistentVolumeClaimName      = "data"
	PersistentVolumePath           = "/data"
	UserGroupId                    = 1000080000 // see docs/dev/openshift_scc.md for more details
	DefaultWaitSecondsProd         = "5"
	DefaultWaitSecondsDev          = "3"
	DefaultWaitRetrialsProd        = "180" // 180 * 5 = 900 seconds = 15 min (Azure launch time is approximately 10 min)
	DefaultWaitRetrialsDev         = "20"

	// All others
	OmGroupExternallyManagedTag = "EXTERNALLY_MANAGED_BY_KUBERNETES"
)
