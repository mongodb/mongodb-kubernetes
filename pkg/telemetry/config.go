package telemetry

import "time"

// BaseUrl is an internal, undocumented env var used only to point telemetry at a mock
// endpoint in tests. Production uses the Atlas SDK default.
// All other telemetry settings are configured via the OperatorConfig CR.
const (
	BaseUrl = "MDB_OPERATOR_TELEMETRY_SEND_BASEURL"
)

// Default Settings. These are only used as defensive fallbacks in the runtime; the
// authoritative defaults are applied when loading the OperatorConfig CR.
const (
	DefaultCollectionFrequency = 1 * time.Hour
	DefaultSendFrequency       = time.Hour * 168
	DefaultKubeTimeout         = 5 * time.Minute
)

const (
	OperatorConfigMapTelemetryConfigMapName = "mongodb-enterprise-operator-telemetry"
)

// Config is the resolved telemetry configuration sourced from the OperatorConfig CR
// (OperatorConfig.spec.telemetry). It is passed into the telemetry runnable at startup.
type Config struct {
	CollectionFrequency time.Duration
	KubeTimeout         time.Duration
	CollectClusters     bool
	CollectDeployments  bool
	CollectOperators    bool
	SendEnabled         bool
	SendFrequency       time.Duration
}

// collectionEnabled reports whether collection of the given event type is enabled.
func (c Config) collectionEnabled(eventType EventType) bool {
	switch eventType {
	case Clusters:
		return c.CollectClusters
	case Deployments:
		return c.CollectDeployments
	case Operators:
		return c.CollectOperators
	default:
		return false
	}
}
