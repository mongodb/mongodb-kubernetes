package telemetry

import "time"

// Helm Chart Settings
const (
	Enabled             = "MDB_OPERATOR_TELEMETRY_ENABLED"
	BaseUrl             = "MDB_OPERATOR_TELEMETRY_SEND_BASEURL"
	KubeTimeout         = "MDB_OPERATOR_TELEMETRY_KUBE_TIMEOUT"
	CollectionFrequency = "MDB_OPERATOR_TELEMETRY_COLLECTION_FREQUENCY"
	SendEnabled         = "MDB_OPERATOR_TELEMETRY_SEND_ENABLED"
	SendFrequency       = "MDB_OPERATOR_TELEMETRY_SEND_FREQUENCY"
)

// Default Settings
const (
	DefaultCollectionFrequency    = 1 * time.Hour
	DefaultCollectionFrequencyStr = "1h"
	DefaultSendFrequencyStr       = "168h"
	DefaultSendFrequency          = time.Hour * 168
)

const (
	OperatorConfigMapTelemetryConfigMapName = "mongodb-enterprise-operator-telemetry"
)

func IsTelemetryActivated() bool {
	return ReadBoolWithTrueAsDefault(Enabled)
}
