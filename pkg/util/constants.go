package util

const (
	// MongoDbStandaloneController name of the Standalone controller
	MongoDbStandaloneController = "mongodbstandalone-controller"

	// MongoDbReplicaSetController name of the ReplicaSet controller
	MongoDbReplicaSetController = "mongodbreplicaset-controller"

	// MongoDbShardedClusterController name of the ShardedCluster controller
	MongoDbShardedClusterController = "mongodbshardedcluster-controller"

	// MongoDbProjectController name of Project controller
	MongoDbProjectController = "project-controller"

	// MongoDbUserController name of the MongoDBUser controller
	MongoDbUserController = "mongodbuser-controller"

	// MongoDbOpsManagerController name of the OpsManager controller
	MongoDbOpsManagerController = "opsmanager-controller"

	// Ops manager config map and secret variables
	OmBaseUrl      = "baseUrl"
	OmOrgId        = "orgId"
	OmProjectName  = "projectName"
	OmUser         = "user"
	OmPublicApiKey = "publicApiKey"
	OmAgentApiKey  = "agentApiKey"
	OmCredentials  = "credentials"
	OmAuthMode     = "authenticationMode"

	// SSLRequireValidMMSServerCertificates points at the string name of the
	// same name variable in OM configuration passed in the "Project" config
	SSLRequireValidMMSServerCertificates = "sslRequireValidMMSServerCertificates"

	// SSLTrustedMMSServerCertificate points at the string name of the
	// same name variable in OM configuration passed in the "Project" config
	SSLTrustedMMSServerCertificate = "sslTrustedMMSServerCertificate"

	// SSLMMSCAConfigMap indicates the name of the ConfigMap that stores the
	// CA certificate used to sign the MMS TLS certificate
	SSLMMSCAConfigMap = "sslMMSCAConfigMap"

	// UseCustomCAConfigMap flags the operator to try to generate certificates
	// (if false) or to not generate them (if true).
	UseCustomCAConfigMap = "useCustomCA"

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
	OpsManagerName              = "mongodb-ops-manager"
	ContainerName               = "mongodb-enterprise-database"
	OmControllerLabel           = "mongodb-enterprise-operator"
	LivenessProbe               = "/mongodb-automation/files/probe.sh"
	ReadinessProbe              = "/mongodb-automation/files/readinessprobe"
	PvcNameData                 = "data"
	PvcMountPathData            = "/data"
	PvcNameJournal              = "journal"
	PvcMountPathJournal         = "/journal"
	PvcNameLogs                 = "logs"
	PvcMountPathLogs            = "/var/log/mongodb-mms-automation"
	CAFilePathInContainer       = "/mongodb-automation/ca.pem"
	PEMKeyFilePathInContainer   = "/mongodb-automation/server.pem"
	AutomationAgentName         = "mms-automation-agent"
	AutomationAgentPemSecretKey = AutomationAgentName + "-pem"
	MonitoringAgentName         = "mms-monitoring-agent"
	MonitoringAgentPemSecretKey = MonitoringAgentName + "-pem"
	BackupAgentName             = "mms-backup-agent"
	BackupAgentPemSecretKey     = BackupAgentName + "-pem"
	AutomationAgentPemFilePath  = "/mongodb-automation/" + AgentSecretName + "/" + AutomationAgentPemSecretKey
	MonitoringAgentPemFilePath  = "/mongodb-automation/" + AgentSecretName + "/" + MonitoringAgentPemSecretKey
	BackupAgentPemFilePath      = "/mongodb-automation/" + AgentSecretName + "/" + BackupAgentPemSecretKey
	RunAsUser                   = 2000
	FsGroup                     = 2000

	// x509 authentication
	X509Db                         = "$external"
	AutomationAgentSubject         = "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US"
	BackupAgentSubject             = "CN=mms-backup-agent,OU=MongoDB Kubernetes Operator,O=mms-backup-agent,L=NY,ST=NY,C=US"
	MonitoringAgentSubject         = "CN=mms-monitoring-agent,OU=MongoDB Kubernetes Operator,O=mms-monitoring-agent,L=NY,ST=NY,C=US"
	AgentSecretName                = "agent-certs"
	AutomationConfigX509Option     = "MONGODB-X509"
	AutomationAgentKeyFileContents = "DUMMYFILE"
	DefaultAutomationAgentPassword = "D9XK2SfdR2obIevI9aKsYlVH"
	AutomationAgentUserName        = "mms-automation-agent"
	RequireClientCertificates      = "REQUIRE"
	OptionalClientCertficates      = "OPTIONAL"
	ClusterFileName                = "clusterfile"
	InternalClusterAuthMountPath   = "/mongodb-automation/cluster-auth/"
	X509                           = "x509"

	// AutomationAgentWindowsKeyFilePath is the default path for the windows key file. This is never
	// used, but we want to keep it the default value so it is is possible to add new users without modifying
	// it. Ops Manager will attempt to reset this value to the default if new MongoDB users are added
	// when x509 auth is enabled
	AutomationAgentWindowsKeyFilePath = "%SystemDrive%\\MMSAutomation\\versions\\keyfile"

	//AutomationAgentKeyFilePathInContainer is the default path of the keyfile and should be
	// kept as is for the same reason as above
	AutomationAgentKeyFilePathInContainer = "/var/lib/mongodb-mms-automation/keyfile"

	// Operator Env configuration properties
	OpsManagerImageUrl             = "OPS_MANAGER_IMAGE_REPOSITORY"
	OpsManagerPullPolicy           = "OPS_MANAGER_IMAGE_PULL_POLICY"
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
	DefaultMongodStorageSize       = "16G"
	DefaultConfigSrvStorageSize    = "5G"
	DefaultJournalStorageSize      = "1G" // maximum size for single journal file is 100Mb, journal files are removed soon after checkpoints
	DefaultLogsStorageSize         = "3G"
	DefaultAntiAffinityTopologyKey = "kubernetes.io/hostname"
	MongoDbDefaultPort             = 27017
	OpsManagerDefaultPort          = 8080
	// 60 * 3 = 180 seconds = 3 min - should be in general enough for a couple of PVCs to be bound but not too long to
	// block waiting reconciliations
	DefaultPodWaitSecondsProd          = "3"
	DefaultPodWaitRetriesProd          = "60"
	DefaultPodWaitSecondsDev           = "3"
	DefaultPodWaitRetriesDev           = "60" // This needs to be bigger for the extreme case when 3 PVs are mounted
	DefaultBackupDisableWaitSeconds    = "3"
	DefaultBackupDisableWaitRetries    = "30" // 30 * 3 = 90 seconds, should be ok for backup job to terminate
	DefaultPodTerminationPeriodSeconds = 600  // 10 min

	// Ops Manager related constants
	OmPropertyPrefix   = "OM_PROP_"
	GenKeyPath         = "/etc/mongodb-mms"
	ENV_VAR_MANAGED_DB = "MANAGED_APP_DB"

	// Ops Manager configuration properties
	MmsCentralUrlPropKey = "mms.centralUrl"
	MmsManagedAppDB      = "mms.managedAppDb"
	MmsTempAppDB         = "mms.temp.appDb.version"
	MmsMongoUri          = "mongo.mongoUri"

	// All others
	OmGroupExternallyManagedTag = "EXTERNALLY_MANAGED_BY_KUBERNETES"
)

// this is set at compile time
var OperatorVersion string
