package constants

const (
	ExternalDB                            = "$external"
	Sha256                                = "SCRAM-SHA-256"
	Sha1                                  = "MONGODB-CR"
	X509                                  = "MONGODB-X509"
	AutomationAgentKeyFilePathInContainer = "/var/lib/mongodb-mms-automation/authentication/keyfile"
	AgentName                             = "mms-automation"
	AgentPasswordKey                      = "password"
	AgentKeyfileKey                       = "keyfile"
	AgentPemFile                          = "agent-certs-pem"
	AutomationAgentWindowsKeyFilePath     = "%SystemDrive%\\MMSAutomation\\versions\\keyfile"
	ClusterDomainEnv                      = "CLUSTER_DOMAIN"
	AutomationAgentAuthSecretKey          = "automation-agent-password"
)

// Upper bounds for the replica/shard count fields of the MongoDB CRDs. These counts are used as
// allocation and loop bounds (for example make([]string, members) when building host seeds), so an
// unbounded value lets a single CR drive a multi-GB allocation and OOMKill the operator. The bounds
// are enforced both by kubebuilder markers on the CRDs and by the webhook validators, and are
// deliberately well above any supported topology.
const (
	// MaxReplicaSetMembers bounds spec.members. MongoDB itself supports at most 50 replica set members.
	MaxReplicaSetMembers = 50
	// MaxShardCount bounds spec.shardCount.
	MaxShardCount = 250
	// MaxSearchReplicas bounds the mongot replica counts on MongoDBSearch.
	MaxSearchReplicas = 50
)
