package telemetry

import (
	"fmt"
	"time"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

// FlatProperties is a helper interface that makes sure telemetry properties are convertable to unnested, flat map
// with basic types (no arrays or structs allowed). This is a requirement from Segment telemetry subsystem we use
type FlatProperties interface {
	ConvertToFlatMap() (map[string]any, error)
}

// OperatorUsageSnapshotProperties represents the structure for tracking Kubernetes operator usage events.
type OperatorUsageSnapshotProperties struct {
	KubernetesClusterID  string       `json:"kubernetesClusterID"`  // Kubernetes cluster ID where the operator is running
	KubernetesClusterIDs []string     `json:"kubernetesClusterIDs"` // Sorted Kubernetes cluster IDs the operator is managing
	OperatorID           string       `json:"operatorID"`           // Operator UUID
	OperatorVersion      string       `json:"operatorVersion"`      // Version of the operator
	OperatorType         OperatorType `json:"operatorType"`         // MEKO, MCK, MCO (here meko)
	OperatorArchitecture string       `json:"operatorArchitecture"` // Architecture of the operator binary (amd64, arm64, s390x, ppc64le)
	OperatorOS           string       `json:"operatorOS"`           // Operating system of the operator binary (linux, darwin, windows)
	OperatorInstaller    string       `json:"operatorInstaller"`    // Installation method of the operator (helm, olm, yaml, unknown)
}

func (p OperatorUsageSnapshotProperties) ConvertToFlatMap() (map[string]any, error) {
	return maputil.StructToMap(p)
}

// KubernetesClusterUsageSnapshotProperties represents the structure for tracking Kubernetes cluster usage events.
type KubernetesClusterUsageSnapshotProperties struct {
	KubernetesClusterID  string `json:"kubernetesClusterID"` // Kubernetes cluster ID where the operator is running
	KubernetesAPIVersion string `json:"kubernetesAPIVersion"`
	KubernetesFlavour    string `json:"kubernetesFlavour"`
}

func (p KubernetesClusterUsageSnapshotProperties) ConvertToFlatMap() (map[string]any, error) {
	return maputil.StructToMap(p)
}

// DeploymentUsageSnapshotProperties represents the structure for tracking Deployment events.
type DeploymentUsageSnapshotProperties struct {
	DatabaseClusters         *int     `json:"databaseClusters,omitempty"` // pointers allow us to not send that value if it's not set.
	AppDBClusters            *int     `json:"appDBClusters,omitempty"`
	AppDBBackupMode          string   `json:"appDBBackupMode,omitempty"`
	ExternalAppDB            string   `json:"externalAppDB,omitempty"`
	Role                     string   `json:"role,omitempty"`
	OmClusters               *int     `json:"OmClusters,omitempty"`
	DeploymentUID            string   `json:"deploymentUID"`
	OperatorID               string   `json:"operatorID"`
	Architecture             string   `json:"architecture"`
	IsMultiCluster           bool     `json:"isMultiCluster"`
	Type                     string   `json:"type"` // RS, SC, OM, Single
	IsRunningEnterpriseImage bool     `json:"IsRunningEnterpriseImage"`
	ExternalDomains          string   `json:"externalDomains,omitempty"`         // None, Uniform, ClusterSpecific, Mixed
	CustomRoles              string   `json:"customRoles,omitempty"`             // Custom roles used: None, Embedded, Referenced
	AuthenticationAgentMode  string   `json:"authenticationAgentMode,omitempty"` // Agent authentication mode
	AuthenticationModes      []string `json:"-"`                                 // Deployment authentication modes
	MigrationPhase           string   `json:"migrationPhase,omitempty"`          // Validating, Extending, InProgress, Pruning, MigrationComplete
	ExternalMembersCount     *int     `json:"externalMembersCount,omitempty"`    // spec.externalMembers still present at snapshot time
	// MigrationStartedAt and MigrationCompletedAt are exact transition instants, never both on the
	// same snapshot. Migration duration is derived by aggregating a deploymentUID's snapshots rather
	// than reported directly: see populateMigrationFields.
	MigrationStartedAt   string `json:"migrationStartedAt,omitempty"`
	MigrationCompletedAt string `json:"migrationCompletedAt,omitempty"`
	// ManagedCertificateIssuers is only set when spec.security.managedCertificate is enabled, so its
	// presence is the usage signal. Values: bothOperatorManaged (both categories are signed by the
	// operator's self-signed CA), serverUserConfigured, clientUserConfigured, bothUserConfigured.
	ManagedCertificateIssuers string `json:"managedCertificateIssuers,omitempty"`
	// BackupManagedCertificateIssuers reports spec.backup.managedCertificate of a MongoDBOpsManager
	// (the queryable backup daemon cert); it uses the same values as ManagedCertificateIssuers.
	BackupManagedCertificateIssuers string `json:"backupManagedCertificateIssuers,omitempty"`
	// AppDBManagedCertificateIssuers reports the internally-managed AppDB's
	// spec.security.managedCertificate. External AppDBs are not reported here: their MongoDB
	// resource is its own deployment event.
	AppDBManagedCertificateIssuers string `json:"appDBManagedCertificateIssuers,omitempty"`
	// ManagedCertificateServerDurationSeconds is the requested server leaf-certificate lifetime in
	// whole seconds. Set only when the server category's duration is configured; absence means the
	// cert-manager default (90 days). On Ops Manager events the same field reports its own
	// server-only app-server certificate.
	ManagedCertificateServerDurationSeconds *int64 `json:"managedCertificateServerDurationSeconds,omitempty"`
	// ManagedCertificateServerRenewBeforeSeconds is the server certificate renewal lead time in
	// whole seconds; absence means cert-manager's default (1/3 of duration).
	ManagedCertificateServerRenewBeforeSeconds *int64 `json:"managedCertificateServerRenewBeforeSeconds,omitempty"`
	// ManagedCertificateClientDurationSeconds is the requested client leaf-certificate lifetime in
	// whole seconds; absence means the cert-manager default (90 days).
	ManagedCertificateClientDurationSeconds *int64 `json:"managedCertificateClientDurationSeconds,omitempty"`
	// ManagedCertificateClientRenewBeforeSeconds is the client certificate renewal lead time in
	// whole seconds; absence means cert-manager's default (1/3 of duration).
	ManagedCertificateClientRenewBeforeSeconds *int64 `json:"managedCertificateClientRenewBeforeSeconds,omitempty"`
	// BackupManagedCertificateDurationSeconds is the requested lifetime of a MongoDBOpsManager's
	// queryable backup daemon certificate, in whole seconds.
	BackupManagedCertificateDurationSeconds *int64 `json:"backupManagedCertificateDurationSeconds,omitempty"`
	// BackupManagedCertificateRenewBeforeSeconds is the queryable backup daemon certificate's
	// renewal lead time in whole seconds.
	BackupManagedCertificateRenewBeforeSeconds *int64 `json:"backupManagedCertificateRenewBeforeSeconds,omitempty"`
	// AppDBManagedCertificateServerDurationSeconds is the requested lifetime of the internally-
	// managed AppDB's server certificate, in whole seconds. The AppDB client category is not
	// reported.
	AppDBManagedCertificateServerDurationSeconds *int64 `json:"appDBManagedCertificateServerDurationSeconds,omitempty"`
	// AppDBManagedCertificateServerRenewBeforeSeconds is the internally-managed AppDB server
	// certificate's renewal lead time in whole seconds.
	AppDBManagedCertificateServerRenewBeforeSeconds *int64 `json:"appDBManagedCertificateServerRenewBeforeSeconds,omitempty"`
}

type FakeDeploymentUsageSnapshotProperties DeploymentUsageSnapshotProperties

func (u DeploymentUsageSnapshotProperties) ConvertToFlatMap() (map[string]any, error) {
	// FakeDeploymentUsageSnapshotProperties is used to avoid infinite recursion - maputil.StructToMap will call MarshalJSON
	properties, err := maputil.StructToMap(FakeDeploymentUsageSnapshotProperties(u))
	if err != nil {
		return nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	for _, value := range u.AuthenticationModes {
		properties["authenticationMode"+value] = true
	}

	return properties, nil
}

type SearchDeploymentUsageSnapshotProperties struct {
	DeploymentUsageSnapshotProperties `json:",inline"`
	IsAutoEmbeddingEnabled            bool `json:"isAutoEmbeddingEnabled"`
}

func (u SearchDeploymentUsageSnapshotProperties) ConvertToFlatMap() (map[string]any, error) {
	baseProperties, err := u.DeploymentUsageSnapshotProperties.ConvertToFlatMap()
	if err != nil {
		return nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	baseProperties["isAutoEmbeddingEnabled"] = u.IsAutoEmbeddingEnabled

	return baseProperties, nil
}

type Event struct {
	Timestamp  time.Time      `json:"timestamp"`
	Source     EventType      `json:"source"`
	Properties map[string]any `json:"properties"`
}

type OperatorType string

const (
	MCK  OperatorType = "MCK"
	MCO  OperatorType = "MCO"
	MEKO OperatorType = "MEKO"
)

type EventType string

const (
	Deployments EventType = "Deployments"
	Operators   EventType = "Operators"
	Clusters    EventType = "Clusters"
)

var AllEventTypes = []EventType{
	Deployments,
	Operators,
	Clusters,
}

var EventTypeMappingToEnvVar = map[EventType]string{
	Deployments: "MDB_OPERATOR_TELEMETRY_COLLECTION_DEPLOYMENTS_ENABLED",
	Clusters:    "MDB_OPERATOR_TELEMETRY_COLLECTION_CLUSTERS_ENABLED",
	Operators:   "MDB_OPERATOR_TELEMETRY_COLLECTION_OPERATORS_ENABLED",
}

func (e EventType) GetPayloadKey() string {
	return fmt.Sprintf("%s%s", LastSendPayloadKey, e)
}

func (e EventType) GetTimeStampKey() string {
	tsKey := fmt.Sprintf("%s%s", TimestampKey, e)
	return tsKey
}
