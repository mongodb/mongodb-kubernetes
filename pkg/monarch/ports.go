// Package monarch contains shared Monarch disaster recovery utilities.
package monarch

// Monarch network port constants.
// These are used in both the Deployment/Service configuration (construct package)
// and the automation config builder (om package).
const (
	// HealthPort is the port for the Monarch health API (/api/v1/status).
	HealthPort int32 = 8080
	// ReplicationPort is the port for Monarch oplog replication traffic.
	ReplicationPort int32 = 9995
	// APIPort is the port for the Monarch management API.
	APIPort int32 = 1122
)
