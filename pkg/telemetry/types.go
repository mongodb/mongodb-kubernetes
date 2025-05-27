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
	OmClusters               *int     `json:"OmClusters,omitempty"`
	DeploymentUID            string   `json:"deploymentUID"`
	OperatorID               string   `json:"operatorID"`
	Architecture             string   `json:"architecture"`
	IsMultiCluster           bool     `json:"isMultiCluster"`
	Type                     string   `json:"type"` // RS, SC, OM, Single
	IsRunningEnterpriseImage bool     `json:"IsRunningEnterpriseImage"`
	ExternalDomains          string   `json:"externalDomains"`                   // None, Uniform, ClusterSpecific, Mixed
	AuthenticationAgentMode  string   `json:"authenticationAgentMode,omitempty"` // Agent authentication mode
	AuthenticationModes      []string `json:"-"`                                 // Deployment authentication modes
}

type FakeDeploymentUsageSnapshotProperties DeploymentUsageSnapshotProperties

func (u DeploymentUsageSnapshotProperties) ConvertToFlatMap() (map[string]any, error) {
	// FakeDeploymentUsageSnapshotProperties is used to avoid infinite recursion - maputil.StructToMap will call MarshalJSON
	properties, err := maputil.StructToMap(FakeDeploymentUsageSnapshotProperties(u))
	if err != nil {
		return nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	if len(u.AuthenticationModes) > 0 {
		for _, value := range u.AuthenticationModes {
			properties["authenticationMode"+value] = true
		}
	}

	return properties, nil
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
