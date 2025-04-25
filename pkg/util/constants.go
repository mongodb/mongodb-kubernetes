package util

import (
	"strings"
	"time"
)

const (
	// MongoDbStandaloneController name of the Standalone controller
	MongoDbStandaloneController = "mongodbstandalone-controller"

	// MongoDbReplicaSetController name of the ReplicaSet controller
	MongoDbReplicaSetController = "mongodbreplicaset-controller"

	// MongoDbMultiClusterController is the name of the MongoDB Multi controller
	MongoDbMultiClusterController = "mongodbmulticluster-controller"

	// MongoDbMultiReplicaSetController name of the multi-cluster ReplicaSet controller
	MongoDbMultiReplicaSetController = "mongodbmultireplicaset-controller"

	// MongoDbShardedClusterController name of the ShardedCluster controller
	MongoDbShardedClusterController = "mongodbshardedcluster-controller"

	// MongoDbUserController name of the MongoDBUser controller
	MongoDbUserController = "mongodbuser-controller"

	// MongoDbOpsManagerController name of the OpsManager controller
	MongoDbOpsManagerController = "opsmanager-controller"

	// Ops manager config map and secret variables
	OmBaseUrl         = "baseUrl"
	OmOrgId           = "orgId"
	OmProjectName     = "projectName"
	OldOmPublicApiKey = "publicApiKey"
	OldOmUser         = "user"
	OmPublicApiKey    = "publicKey"
	OmPrivateKey      = "privateKey"
	OmAgentApiKey     = "agentApiKey"
	OmCredentials     = "credentials"

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

	// CaCertMMS is the name of the CA file provided for MMS.
	CaCertMMS = "mms-ca.crt"

	// Env variables names for pods
	EnvVarBaseUrl   = "BASE_URL"
	EnvVarProjectId = "GROUP_ID"
	EnvVarUser      = "USER_LOGIN"
	EnvVarLogLevel  = "LOG_LEVEL"

	// EnvVarDebug is used to decide whether we want to start the agent in debug mode
	EnvVarDebug            = "MDB_AGENT_DEBUG"
	EnvVarAgentVersion     = "MDB_AGENT_VERSION"
	EnvVarMultiClusterMode = "MULTI_CLUSTER_MODE"

	// EnvVarSSLRequireValidMMSCertificates bla bla
	EnvVarSSLRequireValidMMSCertificates = "SSL_REQUIRE_VALID_MMS_CERTIFICATES"

	// EnvVarSSLTrustedMMSServerCertificate env variable will point to where the CA cert is mounted.
	EnvVarSSLTrustedMMSServerCertificate = "SSL_TRUSTED_MMS_SERVER_CERTIFICATE"

	// Pod/StatefulSet specific constants
	OperatorName                   = "mongodb-kubernetes-operator"
	LegacyOperatorName             = "mongodb-enterprise-operator" // Still used for some selectors and labels
	MultiClusterOperatorName       = "mongodb-kubernetes-operator-multi-cluster"
	OperatorLabelName              = "controller"
	OperatorLabelValue             = LegacyOperatorName
	OpsManagerContainerName        = "mongodb-ops-manager"
	BackupDaemonContainerName      = "mongodb-backup-daemon"
	DatabaseContainerName          = "mongodb-enterprise-database"
	AgentContainerName             = "mongodb-agent"
	InitOpsManagerContainerName    = "mongodb-kubernetes-init-ops-manager"
	PvcNameData                    = "data"
	PvcMountPathData               = "/data"
	PvcNameJournal                 = "journal"
	PvcMountPathJournal            = "/journal"
	PvcNameLogs                    = "logs"
	PvcMountPathLogs               = "/var/log/mongodb-mms-automation"
	PvcNameHeadDb                  = "head"
	PvcMountPathHeadDb             = "/head/"
	PvcNameTmp                     = "tmp"
	PvcMountPathTmp                = "/tmp"
	PvcMmsHome                     = "mongodb-automation"
	PvcMmsHomeMountPath            = "/mongodb-automation"
	CAFilePathInContainer          = PvcMmsHomeMountPath + "/ca.pem"
	PEMKeyFilePathInContainer      = PvcMmsHomeMountPath + "/server.pem"
	KMIPSecretsHome                = "/mongodb-ops-manager/kmip" //nolint
	KMIPServerCAHome               = KMIPSecretsHome + "/server"
	KMIPClientSecretsHome          = KMIPSecretsHome + "/client"
	KMIPServerCAName               = "kmip-server"
	KMIPClientSecretNamePrefix     = "kmip-client-" //nolint
	KMIPCAFileInContainer          = KMIPServerCAHome + "/ca.pem"
	PvcMms                         = "mongodb-mms-automation"
	PvcMmsMountPath                = "/var/lib/mongodb-mms-automation"
	PvMms                          = "agent"
	AgentDownloadsDir              = PvcMmsMountPath + "/downloads"
	AgentAuthenticationKeyfilePath = PvcMmsMountPath + "/keyfile"
	AutomationConfigFilePath       = PvcMountPathData + "/automation-mongod.conf"
	MongosConfigFileDirPath        = PvcMmsMountPath + "/workspace"

	MmsPemKeyFileDirInContainer  = "/opt/mongodb/mms/secrets"
	AppDBMmsCaFileDirInContainer = "/opt/mongodb/mms/ca/"

	AutomationAgentName         = "mms-automation-agent"
	AutomationAgentPemSecretKey = AutomationAgentName + "-pem"
	AutomationAgentPemFilePath  = PvcMmsHomeMountPath + "/" + AgentSecretName + "/" + AutomationAgentPemSecretKey

	// Key used in concatenated pem secrets to denote the hash of the latest certificate
	LatestHashSecretKey   = "latestHash"
	PreviousHashSecretKey = "previousHash"

	RunAsUser = 2000
	FsGroup   = 2000

	// Service accounts
	OpsManagerServiceAccount = "mongodb-kubernetes-ops-manager"
	MongoDBServiceAccount    = "mongodb-kubernetes-database-pods"

	// Authentication
	AgentSecretName                   = "agent-certs"
	AutomationConfigX509Option        = "MONGODB-X509"
	AutomationConfigLDAPOption        = "PLAIN"
	AutomationConfigScramSha256Option = "SCRAM-SHA-256"
	AutomationConfigScramSha1Option   = "MONGODB-CR"
	AutomationAgentUserName           = "mms-automation-agent"
	RequireClientCertificates         = "REQUIRE"
	OptionalClientCertficates         = "OPTIONAL"
	ClusterFileName                   = "clusterfile"
	InternalClusterAuthMountPath      = PvcMmsHomeMountPath + "/cluster-auth/"
	DefaultUserDatabase               = "admin"
	X509                              = "X509"
	SCRAM                             = "SCRAM"
	SCRAMSHA1                         = "SCRAM-SHA-1"
	MONGODBCR                         = "MONGODB-CR"
	SCRAMSHA256                       = "SCRAM-SHA-256"
	LDAP                              = "LDAP"
	MinimumScramSha256MdbVersion      = "4.0.0"

	// these were historically used and constituted a security issue—if set they should be changed
	InvalidKeyFileContents         = "DUMMYFILE"
	InvalidAutomationAgentPassword = "D9XK2SfdR2obIevI9aKsYlVH" //nolint //Part of the algorithm

	// AutomationAgentWindowsKeyFilePath is the default path for the windows key file. This is never
	// used, but we want to keep it the default value so it is is possible to add new users without modifying
	// it. Ops Manager will attempt to reset this value to the default if new MongoDB users are added
	// when x509 auth is enabled
	AutomationAgentWindowsKeyFilePath = "%SystemDrive%\\MMSAutomation\\versions\\keyfile"

	// AutomationAgentKeyFilePathInContainer is the default path of the keyfile and should be
	// kept as is for the same reason as above
	AutomationAgentKeyFilePathInContainer = PvcMmsMountPath + "/keyfile"

	// Operator Env configuration properties. Please note that when adding environment variables to this list,
	// make sure you append them to util.go:PrintEnvVars function's `printableEnvPrefixes` if you need the
	// new variable to be printed at operator start.
	OpsManagerImageUrl               = "OPS_MANAGER_IMAGE_REPOSITORY"
	InitOpsManagerImageUrl           = "INIT_OPS_MANAGER_IMAGE_REPOSITORY"
	InitOpsManagerVersion            = "INIT_OPS_MANAGER_VERSION"
	InitAppdbImageUrlEnv             = "INIT_APPDB_IMAGE_REPOSITORY"
	InitDatabaseImageUrlEnv          = "INIT_DATABASE_IMAGE_REPOSITORY"
	OpsManagerPullPolicy             = "OPS_MANAGER_IMAGE_PULL_POLICY"
	NonStaticDatabaseEnterpriseImage = "MONGODB_ENTERPRISE_DATABASE_IMAGE"
	AutomationAgentImagePullPolicy   = "IMAGE_PULL_POLICY"
	ImagePullSecrets                 = "IMAGE_PULL_SECRETS" //nolint
	OmOperatorEnv                    = "OPERATOR_ENV"
	MemberListConfigMapName          = "mongodb-enterprise-operator-member-list"
	BackupDisableWaitSecondsEnv      = "BACKUP_WAIT_SEC"
	BackupDisableWaitRetriesEnv      = "BACKUP_WAIT_RETRIES"
	ManagedSecurityContextEnv        = "MANAGED_SECURITY_CONTEXT"
	CurrentNamespace                 = "NAMESPACE"
	WatchNamespace                   = "WATCH_NAMESPACE"
	OpsManagerMonitorAppDB           = "OPS_MANAGER_MONITOR_APPDB"
	MongodbCommunityAgentImageEnv    = "MDB_COMMUNITY_AGENT_IMAGE"

	MdbWebhookRegisterConfigurationEnv = "MDB_WEBHOOK_REGISTER_CONFIGURATION"
	MdbWebhookPortEnv                  = "MDB_WEBHOOK_PORT"

	MaxConcurrentReconcilesEnv = "MDB_MAX_CONCURRENT_RECONCILES"

	// Different default configuration values
	DefaultMongodStorageSize           = "16G"
	DefaultConfigSrvStorageSize        = "5G"
	DefaultJournalStorageSize          = "1G" // maximum size for single journal file is 100Mb, journal files are removed soon after checkpoints
	DefaultLogsStorageSize             = "3G"
	DefaultHeadDbStorageSize           = "32G"
	DefaultMemoryAppDB                 = "500M"
	DefaultMemoryOpsManager            = "5G"
	DefaultAntiAffinityTopologyKey     = "kubernetes.io/hostname"
	MongoDbDefaultPort                 = 27017
	OpsManagerDefaultPortHTTP          = 8080
	OpsManagerDefaultPortHTTPS         = 8443
	DefaultBackupDisableWaitSeconds    = "3"
	DefaultBackupDisableWaitRetries    = "30" // 30 * 3 = 90 seconds, should be ok for backup job to terminate
	DefaultPodTerminationPeriodSeconds = 600  // 10 min. Keep this in sync with 'cleanup()' function in agent-launcher-lib.sh
	DefaultK8sCacheRefreshTimeSeconds  = 2
	OpsManagerMonitorAppDBDefault      = true

	// S3 constants
	S3AccessKey             = "accessKey"
	S3SecretKey             = "secretKey"
	DefaultS3MaxConnections = 50

	// Ops Manager related constants
	OmPropertyPrefix                   = "OM_PROP_"
	MmsJvmParamEnvVar                  = "CUSTOM_JAVA_MMS_UI_OPTS"
	BackupDaemonJvmParamEnvVar         = "CUSTOM_JAVA_DAEMON_OPTS"
	GenKeyPath                         = "/mongodb-ops-manager/.mongodb-mms"
	LatestOmVersion                    = "5.0"
	AppDBAutomationConfigKey           = "cluster-config.json"
	AppDBMonitoringAutomationConfigKey = "monitoring-cluster-config.json"
	DefaultAppDbPasswordKey            = "password"
	AppDbConnectionStringKey           = "connectionString"
	AppDbProjectIdKey                  = "projectId"

	// Below is a list of non-persistent PV and PVCs for OpsManager
	OpsManagerPvcNameData       = "data"
	OpsManagerPvcNameConf       = "conf"
	OpsManagerPvcMountPathConf  = "/mongodb-ops-manager/conf"
	OpsManagerPvcNameLogs       = "logs"
	OpsManagerPvcMountPathLogs  = "/mongodb-ops-manager/logs"
	OpsManagerPvcNameTmp        = "tmp-ops-manager"
	OpsManagerPvcMountPathTmp   = "/mongodb-ops-manager/tmp"
	OpsManagerPvcNameDownloads  = "mongodb-releases"
	OpsManagerPvcMountDownloads = "/mongodb-ops-manager/mongodb-releases"
	OpsManagerPvcNameEtc        = "etc-ops-manager"
	OpsManagerPvcMountPathEtc   = "/etc/mongodb-mms"

	OpsManagerPvcLogBackNameVolume = "logback-volume"
	OpsManagerPvcLogbackMountPath  = "/mongodb-ops-manager/conf-template/logback.xml"
	OpsManagerPvcLogbackSubPath    = "logback.xml"

	OpsManagerPvcLogBackAccessNameVolume = "logback-access-volume"
	OpsManagerPvcLogbackAccessMountPath  = "/mongodb-ops-manager/conf-template/logback-access.xml"
	OpsManagerPvcLogbackAccessSubPath    = "logback-access.xml"

	// Ops Manager configuration properties
	MmsCentralUrlPropKey = "mms.centralUrl"
	MmsMongoUri          = "mongo.mongoUri"
	MmsMongoSSL          = "mongo.ssl"
	MmsMongoCA           = "mongodb.ssl.CAFile"
	MmsFeatureControls   = "mms.featureControls.enable"
	MmsVersionsDirectory = "automation.versions.directory"
	MmsPEMKeyFile        = "mms.https.PEMKeyFile"
	BrsQueryablePem      = "brs.queryable.pem"

	// SecretVolumeMountPath defines where in the Pod will be the secrets
	// object mounted.
	SecretVolumeMountPath = "/var/lib/mongodb-automation/secrets" //nolint

	SecretVolumeMountPathPrometheus = SecretVolumeMountPath + "/prometheus"

	TLSCertMountPath = PvcMmsHomeMountPath + "/tls"
	TLSCaMountPath   = PvcMmsHomeMountPath + "/tls/ca"

	// TODO: remove this from here and move it to the certs package
	// This currently creates an import cycle
	InternalCertAnnotationKey = "internalCertHash"
	LastAchievedSpec          = "mongodb.com/v1.lastSuccessfulConfiguration"

	// SecretVolumeName is the name of the volume resource.
	SecretVolumeName = "secret-certs"

	// PrometheusSecretVolumeName
	PrometheusSecretVolumeName = "prometheus-certs"

	// ConfigMapVolumeCAMountPath defines where CA root certs will be
	// mounted in the pod
	ConfigMapVolumeCAMountPath = SecretVolumeMountPath + "/ca"

	// Ops Manager authentication constants
	OpsManagerMongoDBUserName = "mongodb-ops-manager"
	OpsManagerPasswordKey     = "password"

	// Env variables used for testing mostly to decrease waiting time
	PodWaitSecondsEnv = "POD_WAIT_SEC"
	PodWaitRetriesEnv = "POD_WAIT_RETRIES"

	// All others
	OmGroupExternallyManagedTag = "EXTERNALLY_MANAGED_BY_KUBERNETES"
	GenericErrorMessage         = "Something went wrong validating your Automation Config"
	MethodNotAllowed            = "405 (Method Not Allowed)"

	RetryTimeSec = 10

	DeprecatedImageAppdbUbiUrl = "mongodb-enterprise-appdb-database-ubi"

	OfficialEnterpriseServerImageUrl = "mongodb-enterprise-server"

	MdbAppdbAssumeOldFormat = "MDB_APPDB_ASSUME_OLD_FORMAT"

	Finalizer = "mongodb.com/v1.userRemovalFinalizer"
)

type OperatorEnvironment string

func (o OperatorEnvironment) String() string {
	return string(o)
}

const (
	OperatorEnvironmentDev   OperatorEnvironment = "dev"
	OperatorEnvironmentProd  OperatorEnvironment = "prod"
	OperatorEnvironmentLocal OperatorEnvironment = "local"
)

// ***** These variables are set at compile time

// OperatorVersion is the version of the current Operator. Important: currently it's empty when the Operator is
// installed for development (using 'make') meaning the Ops Manager/AppDB images deployed won't have
// "operator specific" part of the version tag
var OperatorVersion string

var LogAutomationConfigDiff string

func ShouldLogAutomationConfigDiff() bool {
	return strings.EqualFold(LogAutomationConfigDiff, "true")
}

const (
	TWENTY_FOUR_HOURS = 24 * time.Hour
)

const AlwaysMatchVersionFCV = "AlwaysMatchVersion"
