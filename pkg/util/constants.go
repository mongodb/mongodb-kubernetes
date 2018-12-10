package util

const (
	// Controllers names
	MongoDbStandaloneController     = "mongodbstandalone-controller"
	MongoDbReplicaSetController     = "mongodbreplicaset-controller"
	MongoDbShardedClusterController = "mongodbshardedcluster-controller"

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

	// Pod/StatefulSet specific constants
	ContainerName       = "mongodb-enterprise-database"
	OmControllerLabel   = "mongodb-enterprise-operator"
	LivenessProbe       = "/mongodb-automation/files/probe.sh"
	PvcNameData         = "data"
	PvcMountPathData    = "/data"
	PvcNameJournal      = "journal"
	PvcMountPathJournal = "/journal"
	PvcNameLogs         = "logs"
	PvcMountPathLogs    = "/var/log/mongodb-mms-automation"
	RunAsUser           = 2000
	FsGroup             = 2000

	// Operator Env configuration properties
	AutomationAgentImageUrl        = "MONGODB_ENTERPRISE_DATABASE_IMAGE"
	AutomationAgentImagePullPolicy = "IMAGE_PULL_POLICY"
	AutomationAgentPullSecrets     = "IMAGE_PULL_SECRETS"
	OmOperatorEnv                  = "OPERATOR_ENV"
	PodWaitSecondsEnv              = "POD_WAIT_SEC"
	PodWaitRetriesEnv              = "POD_WAIT_RETRIES"
	BackupDisableWaitSecondsEnv    = "BACKUP_WAIT_SEC"
	BackupDisableWaitRetriesEnv    = "BACKUP_WAIT_RETRIES"
	ManagedSecurityContextEnv      = "MANAGED_SECURITY_CONTEXT"

	// Different default configuration values
	DefaultMongodStorageSize        = "16G"
	DefaultConfigSrvStorageSize     = "5G"
	DefaultJournalStorageSize       = "1G" // maximum size for single journal file is 100Mb, journal files are removed soon after checkpoints
	DefaultLogsStorageSize          = "3G"
	DefaultAntiAffinityTopologyKey  = "kubernetes.io/hostname"
	MongoDbDefaultPort              = 27017
	DefaultPodWaitSecondsProd       = "5"
	DefaultPodWaitRetriesProd       = "180" // 180 * 5 = 900 seconds = 15 min (Azure launch time is approximately 10 min)
	DefaultPodWaitSecondsDev        = "3"
	DefaultPodWaitRetriesDev        = "60" // This needs to be bigger for the extreme case when 3 PVs are mounted
	DefaultBackupDisableWaitSeconds = "3"
	DefaultBackupDisableWaitRetries = "30" // 30 * 3 = 90 seconds, should be ok for backup job to terminate

	// All others
	OmGroupExternallyManagedTag = "EXTERNALLY_MANAGED_BY_KUBERNETES"
	MongodbResourceFinalizer    = "resource.finalizer.mongodb.com"
)
