package util

import (
	"fmt"
	"strings"
)

const (
	// MongoDbStandaloneController name of the Standalone controller
	MongoDbStandaloneController = "mongodbstandalone-controller"

	// MongoDbReplicaSetController name of the ReplicaSet controller
	MongoDbReplicaSetController = "mongodbreplicaset-controller"

	// MongoDbShardedClusterController name of the ShardedCluster controller
	MongoDbShardedClusterController = "mongodbshardedcluster-controller"

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

	// Env variables names for pods
	ENV_VAR_BASE_URL          = "BASE_URL"
	ENV_VAR_PROJECT_ID        = "GROUP_ID"
	ENV_VAR_USER              = "USER_LOGIN"
	ENV_VAR_AGENT_API_KEY     = "AGENT_API_KEY"
	ENV_VAR_LOG_LEVEL         = "LOG_LEVEL"
	ENV_POD_NAMESPACE         = "POD_NAMESPACE"
	ENV_AUTOMATION_CONFIG_MAP = "AUTOMATION_CONFIG_MAP"
	ENV_HEADLESS_AGENT        = "HEADLESS_AGENT"
	ENV_BACKUP_DAEMON         = "BACKUP_DAEMON"

	// EnvVarSSLRequireValidMMSCertificates bla bla
	EnvVarSSLRequireValidMMSCertificates = "SSL_REQUIRE_VALID_MMS_CERTIFICATES"

	// EnvVarSSLTrustedMMSServerCertificate env variable will point to where the CA cert is mounted.
	EnvVarSSLTrustedMMSServerCertificate = "SSL_TRUSTED_MMS_SERVER_CERTIFICATE"

	// Pod/StatefulSet specific constants
	OperatorName                = "mongodb-enterprise-operator"
	OpsManagerName              = "mongodb-ops-manager"
	BackupdaemonContainerName   = "mongodb-backup-daemon"
	ContainerName               = "mongodb-enterprise-database"
	ContainerAppDbName          = "mongodb-enterprise-appdb"
	OmControllerLabel           = "mongodb-enterprise-operator"
	LivenessProbe               = "/mongodb-automation/files/probe.sh"
	ReadinessProbe              = "/mongodb-automation/files/readinessprobe"
	PvcNameData                 = "data"
	PvcMountPathData            = "/data"
	PvcNameJournal              = "journal"
	PvcMountPathJournal         = "/journal"
	PvcNameLogs                 = "logs"
	PvcMountPathLogs            = "/var/log/mongodb-mms-automation"
	PvcNameHeadDb               = "head"
	PvcMountPathHeadDb          = "/head/"
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
	AppDBServiceAccount         = "mongodb-enterprise-appdb"
	AgentDownloadsDir           = "/var/lib/mongodb-mms-automation/downloads"

	// Operator Filesystem constants
	VersionManifestFilePath = "/var/lib/mongodb-enterprise-operator/version_manifest.json"

	// Authentication

	X509Db                            = "$external"
	AutomationAgentSubject            = "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US"
	BackupAgentSubject                = "CN=mms-backup-agent,OU=MongoDB Kubernetes Operator,O=mms-backup-agent,L=NY,ST=NY,C=US"
	MonitoringAgentSubject            = "CN=mms-monitoring-agent,OU=MongoDB Kubernetes Operator,O=mms-monitoring-agent,L=NY,ST=NY,C=US"
	AgentSecretName                   = "agent-certs"
	AutomationConfigX509Option        = "MONGODB-X509"
	AutomationConfigScramSha256Option = "SCRAM-SHA-256"
	AutomationAgentUserName           = "mms-automation-agent"
	RequireClientCertificates         = "REQUIRE"
	OptionalClientCertficates         = "OPTIONAL"
	ClusterFileName                   = "clusterfile"
	InternalClusterAuthMountPath      = "/mongodb-automation/cluster-auth/"
	DefaultUserDatabase               = "admin"
	X509                              = "X509"
	SCRAM                             = "SCRAM"
	MinimumScramSha256MdbVersion      = "4.0.0"

	// uses a lowercase 'x' and will take precedence over the value specified in
	// the MongoDB resource
	LegacyX509InConfigMapValue = "x509"

	// these were historically used and constituted a security issue—if set they should be changed
	InvalidKeyFileContents         = "DUMMYFILE"
	InvalidAutomationAgentPassword = "D9XK2SfdR2obIevI9aKsYlVH"

	// AutomationAgentWindowsKeyFilePath is the default path for the windows key file. This is never
	// used, but we want to keep it the default value so it is is possible to add new users without modifying
	// it. Ops Manager will attempt to reset this value to the default if new MongoDB users are added
	// when x509 auth is enabled
	AutomationAgentWindowsKeyFilePath = "%SystemDrive%\\MMSAutomation\\versions\\keyfile"

	//AutomationAgentKeyFilePathInContainer is the default path of the keyfile and should be
	// kept as is for the same reason as above
	AutomationAgentKeyFilePathInContainer = "/var/lib/mongodb-mms-automation/keyfile"

	// Operator Env configuration properties. Please note that when adding environment variables to this list,
	// make sure you append them to util.go:PrintEnvVars function's `printableEnvPrefixes` if you need the
	// new variable to be printed at operator start.
	OpsManagerImageUrl             = "OPS_MANAGER_IMAGE_REPOSITORY"
	OpsManagerPullPolicy           = "OPS_MANAGER_IMAGE_PULL_POLICY"
	AutomationAgentImageUrl        = "MONGODB_ENTERPRISE_DATABASE_IMAGE"
	AutomationAgentImagePullPolicy = "IMAGE_PULL_POLICY"
	AutomationAgentPullSecrets     = "IMAGE_PULL_SECRETS"
	OmOperatorEnv                  = "OPERATOR_ENV"
	BackupDisableWaitSecondsEnv    = "BACKUP_WAIT_SEC"
	BackupDisableWaitRetriesEnv    = "BACKUP_WAIT_RETRIES"
	ManagedSecurityContextEnv      = "MANAGED_SECURITY_CONTEXT"
	AppDBImageUrl                  = "APP_DB_IMAGE_REPOSITORY"
	CurrentNamespace               = "CURRENT_NAMESPACE"

	// Different default configuration values
	DefaultMongodStorageSize           = "16G"
	DefaultConfigSrvStorageSize        = "5G"
	DefaultJournalStorageSize          = "1G" // maximum size for single journal file is 100Mb, journal files are removed soon after checkpoints
	DefaultLogsStorageSize             = "3G"
	DefaultHeadDbStorageSize           = "32G"
	DefaultMemoryAppDB                 = "500M"
	DefaultAntiAffinityTopologyKey     = "kubernetes.io/hostname"
	MongoDbDefaultPort                 = 27017
	OpsManagerDefaultPort              = 8080
	DefaultBackupDisableWaitSeconds    = "3"
	DefaultBackupDisableWaitRetries    = "30" // 30 * 3 = 90 seconds, should be ok for backup job to terminate
	DefaultPodTerminationPeriodSeconds = 600  // 10 min
	DefaultK8sCacheRefreshTimeSeconds  = 2

	// S3 constants
	S3AccessKey             = "accessKey"
	S3SecretKey             = "secretKey"
	DefaultS3MaxConnections = 50

	// Ops Manager related constants
	OmPropertyPrefix         = "OM_PROP_"
	GenKeyPath               = "/mongodb-ops-manager/.mongodb-mms"
	LatestOmVersion          = "4.2"
	AppDBAutomationConfigKey = "cluster-config.json"
	DefaultAppDbPasswordKey  = "password"

	// Ops Manager configuration properties
	MmsCentralUrlPropKey      = "mms.centralUrl"
	MmsMongoUri               = "mongo.mongoUri"
	MmsFeatureControls        = "mms.featureControls.enable"
	MmsHeaderContainVersion   = "mms.serviceVersionApiHeader.enabled"
	MmsVersionsDirectory      = "automation.versions.directory"
	OpsManagerMongoDBUserName = "mongodb-ops-manager"

	// Ops Manager authentication constants
	OpsManagerPasswordKey = "password"

	// Env variables used for testing mostly to decrease waiting time
	PodWaitSecondsEnv     = "POD_WAIT_SEC"
	PodWaitRetriesEnv     = "POD_WAIT_RETRIES"
	K8sCacheRefreshEnv    = "K8S_CACHES_REFRESH_TIME_SEC"
	AppDBReadinessWaitEnv = "APPDB_STATEFULSET_WAIT_SEC"

	// All others
	OmGroupExternallyManagedTag = "EXTERNALLY_MANAGED_BY_KUBERNETES"
	GenericErrorMessage         = "Something went wrong validating your Automation Config"
	MethodNotAllowed            = "405 (Method Not Allowed)"
)

// ***** These variables are set at compile time

// OperatorVersion is the version of the current Operator. Important: currently it's empty when the Operator is
// installed for development (using 'make') meaning the Ops Manager/AppDB images deployed won't have
// "operator specific" part of the version tag
var OperatorVersion string
var BundledAppDbMongodbVersion string

func GetBundledAppDbMongoDBVersion() string {
	return fmt.Sprintf("%s-ent", BundledAppDbMongodbVersion)
}

var LogAutomationConfigDiff string

func ShouldLogAutomationConfigDiff() bool {
	return strings.EqualFold(LogAutomationConfigDiff, "true")
}
