package util

const (
	// MongoDbStandaloneController name of the Standalone controller
	MongoDbStandaloneController = "mongodbstandalone-controller"

	// MongoDbReplicaSetController name of the ReplicaSet controller
	MongoDbReplicaSetController = "mongodbreplicaset-controller"

	// MongoDbShardedClusterController name of the ShardedCluster controller
	MongoDbShardedClusterController = "mongodbshardedcluster-controller"

	// Ops manager config map and secret variables
	OmBaseUrl      = "baseUrl"
	OmOrgId        = "orgId"
	OmProjectName  = "projectName"
	OmUser         = "user"
	OmPublicApiKey = "publicApiKey"
	OmAgentApiKey  = "agentApiKey"

	// SSLRequireValidMMSServerCertificates points at the string name of the
	// same name variable in OM configuration passed in the "Project" config
	SSLRequireValidMMSServerCertificates = "sslRequireValidMMSServerCertificates"

	// SSLTrustedMMSServerCertificate points at the string name of the
	// same name variable in OM configuration passed in the "Project" config
	SSLTrustedMMSServerCertificate = "sslTrustedMMSServerCertificate"

	// SSLMMSCAConfigMap indicates the name of the ConfigMap that stores the
	// CA certificate used to sign the MMS TLS certificate
	SSLMMSCAConfigMap = "sslMMSCAConfigMap"

	// SSLMMSCALocation Specifies where the CA certificate should be mounted.
	SSLMMSCALocation = "/mongodb-automation/certs/ca.crt"

	// KubernetesCALocation CA For Kubernetes CA
	KubernetesCALocation = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

	// Env variables names for pods
	ENV_VAR_BASE_URL      = "BASE_URL"
	ENV_VAR_PROJECT_ID    = "GROUP_ID"
	ENV_VAR_USER          = "USER_LOGIN"
	ENV_VAR_AGENT_API_KEY = "AGENT_API_KEY"
	ENV_VAR_LOG_LEVEL     = "LOG_LEVEL"

	// EnvVarSSLRequireValidMMSCertificates bla bla
	EnvVarSSLRequireValidMMSCertificates = "SSL_REQUIRE_VALID_MMS_CERTIFICATES"

	// EnvVarSSLTrustedMMSServerCertificate env variable will point to where the CA cert is mounted.
	EnvVarSSLTrustedMMSServerCertificate = "SSL_TRUSTED_MMS_SERVER_CERTIFICATE"

	// Pod/StatefulSet specific constants
	ContainerName             = "mongodb-enterprise-database"
	OmControllerLabel         = "mongodb-enterprise-operator"
	LivenessProbe             = "/mongodb-automation/files/probe.sh"
	PvcNameData               = "data"
	PvcMountPathData          = "/data"
	PvcNameJournal            = "journal"
	PvcMountPathJournal       = "/journal"
	PvcNameLogs               = "logs"
	PvcMountPathLogs          = "/var/log/mongodb-mms-automation"
	CAFilePathInContainer     = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	PEMKeyFilePathInContainer = "/mongodb-automation/server.pem"
	RunAsUser                 = 2000
	FsGroup                   = 2000

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
)

// this is set at compile time
var OperatorVersion string
