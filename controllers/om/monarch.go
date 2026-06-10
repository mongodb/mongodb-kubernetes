package om

import (
	"encoding/json"
	"fmt"
	"strings"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	monarchpkg "github.com/mongodb/mongodb-kubernetes/pkg/monarch"
)

// AwsStorageConfig holds S3 credentials and bucket configuration nested under "awsConfig".
type AwsStorageConfig struct {
	AWSBucketName      string `json:"awsBucketName"`
	AWSRegion          string `json:"awsRegion"`
	AWSAccessKeyID     string `json:"awsAccessKeyId"`
	AWSSecretAccessKey string `json:"awsSecretAccessKey"`
	S3BucketEndpoint   string `json:"s3BucketEndPoint,omitempty"`
	S3PathStyleAccess  bool   `json:"s3PathStyleAccess,omitempty"`
}

// MaintainedMonarchComponents is the automation config section that tells the agent
// about the Monarch shipper/injector configuration for a cluster.
type MaintainedMonarchComponents struct {
	ReplicaSetID  string            `json:"replicaSetId"`
	ClusterPrefix string            `json:"clusterPrefix"`
	InitialMode   string            `json:"initialMode"`
	AwsConfig     *AwsStorageConfig `json:"awsConfig,omitempty"`
	// InjectorConfig is populated for standby clusters (role: standby).
	InjectorConfig *InjectorConfig `json:"injectorConfig,omitempty"`
	// ShipperConfig is populated for active clusters (role: active).
	ShipperConfig *ShipperConfig `json:"shipperConfig,omitempty"`
}

// MonarchShard is a shard entry used by both ShipperConfig and InjectorConfig.
type MonarchShard struct {
	ShardID     string            `json:"shardId"`
	ReplSetName string            `json:"replSetName"`
	Instances   []MonarchInstance `json:"instances"`
}

// MonarchInstance describes a single shipper or injector pod endpoint.
// ExternallyManaged=true tells the agent not to manage the process lifecycle.
// When HealthAPIEndpoint/MonarchAPIEndpoint are set the agent routes through
// them directly, bypassing hostname locality matching.
type MonarchInstance struct {
	ID                 int    `json:"id"`
	Hostname           string `json:"hostname"`
	Disabled           bool   `json:"disabled"`
	Port               int    `json:"port"`
	ExternallyManaged  bool   `json:"externallyManaged"`
	HealthAPIEndpoint  string `json:"healthApiEndpoint"`
	MonarchAPIEndpoint string `json:"monarchApiEndpoint"`
	Mode               string `json:"mode,omitempty"`
	BackupMongoNodeURI string `json:"backupMongoNodeURI,omitempty"`
	SrcURI             string `json:"srcURI,omitempty"`
	// MongodURIs and InjectorHosts are required by the injector binary's
	// ValidateStandbyConfig (monarch/injector/replstatus/validate_config.go):
	//   expected = mongodHosts ∪ injectorHosts
	// must equal the actual replSet member set (mongods + injector members
	// that OM's StandbyModificationsSvc adds via the standby JSON patch).
	// Without them, validation fails → ReplStatusUpdater never writes the
	// per-shard status → coordinator's earliestSafeTimestamp stays 0 →
	// StopReplication during failover returns EarliestSafeTimestampNotSetError.
	// Standby (injector) only — empty/omitted for the active (shipper) path.
	MongodURIs    []string `json:"mongodURIs,omitempty"`
	InjectorHosts []string `json:"injectorHosts,omitempty"`
}

// monarchShipperMode is the only valid value for ShipperConfig.Mode per ops-manager validation.
const monarchShipperMode = "shipperAndSnapshotter"

type ShipperConfig struct {
	Version string         `json:"version"`
	Shards  []MonarchShard `json:"shards"`
	// ShipperUser/ShipperPwd are populated by OM (server-side) when the deployment has
	// password-based auth enabled and at least one MaintainedMonarchComponents entry.
	// They surface only on the agent-API AC fetch (not the public REST AC, which
	// returns SCRAM hashes only). The operator omits them from outgoing AC pushes;
	// they're read-only on the inbound side.
	ShipperUser string `json:"shipperUser,omitempty"`
	ShipperPwd  string `json:"shipperPwd,omitempty"`
}

type InjectorConfig struct {
	Version string         `json:"version"`
	Shards  []MonarchShard `json:"shards"`
}

// SetMaintainedMonarchComponents sets the maintainedMonarchComponents field in the automation config.
func (d Deployment) SetMaintainedMonarchComponents(mc []MaintainedMonarchComponents) {
	d["maintainedMonarchComponents"] = mc
}

// GetMaintainedMonarchComponents reads the maintainedMonarchComponents field from
// the underlying deployment map. Used after fetching the agent-API AC to extract
// OM-populated cleartext fields like ShipperConfig.ShipperUser/ShipperPwd.
//
// Round-trips through JSON because the underlying map deserialization yields
// []interface{} of map[string]interface{}; a typed re-encode is the simplest
// way to get back to []MaintainedMonarchComponents without writing per-field
// extraction logic.
func (ac *AutomationConfig) GetMaintainedMonarchComponents() []MaintainedMonarchComponents {
	raw, ok := ac.Deployment["maintainedMonarchComponents"]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []MaintainedMonarchComponents
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return out
}

// BuildMaintainedMonarchComponents builds the automation config entries for Monarch.
//
// Both shipper (active) and injector (standby) run as separate Deployments behind a K8s
// Service. serviceDNS is the in-cluster DNS of that Service.
//
// ExternallyManaged=true tells the agent not to manage the process lifecycle.
// When MonarchApiEndpoint/HealthApiEndpoint are set, the agent routes through them
// directly, bypassing hostname locality matching (mms-automation/standby/injectorclient.go).
// A single instance per shard is sufficient because the Service load-balances across pods.
func BuildMaintainedMonarchComponents(mdb *mdbv1.MongoDB, rsName string, awsAccessKeyId string, awsSecretAccessKey string, serviceDNS string, mongoURI string) ([]MaintainedMonarchComponents, error) {
	monarch := mdb.Spec.Monarch
	if monarch == nil {
		return nil, fmt.Errorf("monarch spec is nil")
	}

	initialMode := "STANDBY"
	if monarch.Role == mdbv1.MonarchRoleActive {
		initialMode = "ACTIVE"
	}

	mc := MaintainedMonarchComponents{
		ReplicaSetID:  rsName,
		ClusterPrefix: monarch.S3.GetPrefix(rsName),
		InitialMode:   initialMode,
		AwsConfig: &AwsStorageConfig{
			AWSBucketName:      monarch.S3.Bucket,
			AWSRegion:          monarch.S3.Region,
			AWSAccessKeyID:     awsAccessKeyId,
			AWSSecretAccessKey: awsSecretAccessKey,
			S3BucketEndpoint:   monarch.S3.Endpoint,
			S3PathStyleAccess:  monarch.S3.PathStyle,
		},
	}

	version := extractVersionFromImage(monarch.Image)

	// Build the single instance pointing at the K8s Service.
	shard := MonarchShard{
		ShardID:     "0",
		ReplSetName: rsName,
		Instances: []MonarchInstance{
			{
				ID:                 0,
				Hostname:           serviceDNS,
				Port:               int(monarchpkg.ReplicationPort), //nolint:gosec
				ExternallyManaged:  true,
				HealthAPIEndpoint:  fmt.Sprintf("%s:%d", serviceDNS, monarchpkg.HealthPort),
				MonarchAPIEndpoint: fmt.Sprintf("%s:%d", serviceDNS, monarchpkg.APIPort),
			},
		},
	}

	if monarch.Role == mdbv1.MonarchRoleActive {
		shard.Instances[0].Mode = monarchShipperMode
		shard.Instances[0].BackupMongoNodeURI = mongoURI
		mc.ShipperConfig = &ShipperConfig{
			Version: version,
			Shards:  []MonarchShard{shard},
		}
	} else {
		shard.Instances[0].SrcURI = mongoURI
		// Populate the per-instance mongodURIs and injectorHosts so the
		// injector binary's ValidateStandbyConfig sees
		//   expected = mongodHosts ∪ injectorHosts == actual replSet members
		// (See struct doc on MonarchInstance.) injectorHosts has a single
		// entry — the K8s Service DNS — because OM's StandbyModificationsSvc
		// adds exactly one injector RS member per instance, and we emit one
		// instance per shard.
		shard.Instances[0].MongodURIs = buildMongodURIs(mdb)
		shard.Instances[0].InjectorHosts = []string{
			fmt.Sprintf("%s:%d", serviceDNS, monarchpkg.ReplicationPort),
		}
		mc.InjectorConfig = &InjectorConfig{
			Version: version,
			Shards:  []MonarchShard{shard},
		}
	}

	return []MaintainedMonarchComponents{mc}, nil
}

// buildMongodURIs returns one mongodb:// URI per replica set member, using the
// pod's stable DNS name. Format matches what the injector binary's
// `--mongodURIs` flag expects (one host per URI; no credentials in the URI).
func buildMongodURIs(mdb *mdbv1.MongoDB) []string {
	uris := make([]string, mdb.Spec.Members)
	for i := 0; i < mdb.Spec.Members; i++ {
		uris[i] = fmt.Sprintf("mongodb://%s-%d.%s.%s.svc.%s:27017/",
			mdb.Name, i, mdb.ServiceName(), mdb.Namespace, mdb.Spec.GetClusterDomain())
	}
	return uris
}

// extractVersionFromImage extracts the tag from a container image reference.
// e.g., "quay.io/mongodb/monarch:0.1.1" -> "0.1.1"
// If no tag is found, returns "latest".
func extractVersionFromImage(image string) string {
	// Handle digest references (image@sha256:...)
	if idx := strings.LastIndex(image, "@"); idx != -1 {
		return "latest" // digest-based images don't have a version tag
	}
	// Handle tag references (image:tag)
	if idx := strings.LastIndex(image, ":"); idx != -1 {
		// Make sure we're not matching a port in the registry (e.g., localhost:5000/image)
		tag := image[idx+1:]
		if !strings.Contains(tag, "/") {
			return tag
		}
	}
	return "latest"
}
