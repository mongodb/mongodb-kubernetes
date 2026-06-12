package om

import (
	"encoding/json"
	"fmt"
	"strings"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	monarchpkg "github.com/mongodb/mongodb-kubernetes/pkg/monarch"
	"k8s.io/utils/ptr"
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

const (
	// MonarchShipperUsername is the SCRAM user the Monarch shipper authenticates as
	// against mongod. The operator owns this user (generates it for active clusters,
	// seeds it for standbys) rather than relying on Ops Manager to inject it.
	MonarchShipperUsername = "mms-shipper"
	// MonarchShipperUserDatabase is the auth database for the shipper user.
	MonarchShipperUserDatabase = "admin"
	// MonarchShipperRole is the minimal role granting readBackupFile (SERVER-110899)
	// that OM provisions for the shipper user. The operator references it when adding
	// the user to the automation config.
	MonarchShipperRole = "shipperRole"

	// MonarchUserManagedByKey / MonarchUserManagedByValue stamp the shipper user's
	// customData so Ops Manager can recognise an operator-owned user and leave its
	// password and lifecycle alone (rather than resetting or deleting it). This is
	// the ownership signal the OM-side guard keys on — it is an assertion of
	// ownership, not a heuristic on role presence.
	MonarchUserManagedByKey   = "managedBy"
	MonarchUserManagedByValue = "mongodb-kubernetes"
)

// EnsureMonarchShipperRole adds the shipperRole custom role to deployment.roles[]
// so the agent runs createRole on mongod before the user referencing it is created.
// This must be called before EnsureMonarchShipperUser. Mirrors OM's ensureShipperRole.
// Idempotent: updates the privilege list in place if the role already exists.
func (d Deployment) EnsureMonarchShipperRole() {
	localDb := "local"
	clusterTrue := true
	shipperRole := mdbv1.MongoDBRole{
		Role: MonarchShipperRole,
		Db:   MonarchShipperUserDatabase,
		Privileges: []mdbv1.Privilege{
			{
				Resource: mdbv1.Resource{Db: &localDb, Collection: ptr.To("oplog.rs")},
				Actions:  []string{"find"},
			},
			{
				Resource: mdbv1.Resource{Cluster: &clusterTrue},
				Actions:  []string{"fsync", "readBackupFile", "inprog", "replSetGetStatus"},
			},
		},
	}

	roles := d.GetRoles()
	roleId := MonarchShipperRole + "@" + MonarchShipperUserDatabase
	// Remove any prior entry and re-add with the current privilege list.
	filtered := make([]mdbv1.MongoDBRole, 0, len(roles))
	for _, r := range roles {
		if r.Role+"@"+r.Db != roleId {
			filtered = append(filtered, r)
		}
	}
	filtered = append(filtered, shipperRole)
	d.SetRoles(filtered)
}

// EnsureMonarchShipperUser adds (or updates) the mms-shipper SCRAM user to the
// deployment auth so the agent provisions it server-side from the supplied
// cleartext password (via initPwd — no SCRAM hashing happens in the operator).
//
// This makes the operator own the shipper credential and push it in the same AC
// as maintainedMonarchComponents, eliminating the prior poll-fetch race where the
// operator had to wait for OM to inject shipperUser/shipperPwd on a later
// agent-API AC read. It depends on the OM-side idempotency guard (separate PR)
// that no-ops when the shipper user/role already exists.
//
// Idempotent: when the user already exists with the same initPwd and roles it is
// left untouched so we don't churn the AC every reconcile.
func (a *Auth) EnsureMonarchShipperUser(password string) {
	desired := MongoDBUser{
		Username:   MonarchShipperUsername,
		Database:   MonarchShipperUserDatabase,
		Mechanisms: []string{"SCRAM-SHA-256"},
		Roles: []*Role{
			{Role: MonarchShipperRole, Database: MonarchShipperUserDatabase},
		},
		InitPassword: password,
		// Stamp ownership so Ops Manager leaves this user's password and lifecycle
		// to the operator (see MonarchUserManagedBy* and the OM-side guard).
		CustomData: map[string]interface{}{
			MonarchUserManagedByKey: MonarchUserManagedByValue,
		},
	}

	if _, existing := a.GetUser(MonarchShipperUsername, MonarchShipperUserDatabase); existing != nil {
		if monarchShipperUserMatches(existing, desired) {
			return
		}
	}
	a.EnsureUser(desired)
}

// monarchShipperUserMatches reports whether an existing user already equals the
// desired mms-shipper user on the fields the operator manages (initPwd + roles +
// mechanisms), so EnsureMonarchShipperUser can stay idempotent.
func monarchShipperUserMatches(existing *MongoDBUser, desired MongoDBUser) bool {
	if existing.InitPassword != desired.InitPassword {
		return false
	}
	if len(existing.Mechanisms) != len(desired.Mechanisms) {
		return false
	}
	for i := range desired.Mechanisms {
		if existing.Mechanisms[i] != desired.Mechanisms[i] {
			return false
		}
	}
	if len(existing.Roles) != len(desired.Roles) {
		return false
	}
	for i := range desired.Roles {
		if existing.Roles[i] == nil || desired.Roles[i] == nil {
			return false
		}
		if existing.Roles[i].Role != desired.Roles[i].Role || existing.Roles[i].Database != desired.Roles[i].Database {
			return false
		}
	}
	// The ownership stamp must match too, so a user lacking it (or carrying a
	// different value) is rewritten to assert operator ownership.
	for k, v := range desired.CustomData {
		if existing.CustomData[k] != v {
			return false
		}
	}
	return true
}

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

// ShardInfo holds metadata for one shard (configRS or data shard) when building
// Monarch AC components for a sharded cluster.
type ShardInfo struct {
	ShardID    string   // "configRS" or "myShard_0", "myShard_1", etc.
	RSName     string   // Actual replica set name (e.g., "mycluster-config", "sh-0")
	ServiceDNS string   // K8s Service DNS for the Monarch Deployment for this shard
	MongoURI   string   // mongodb:// URI for this shard's mongod members
	MongodURIs []string // Per-member URIs for injector config (standby only)
}

// BuildMaintainedMonarchComponentsSharded builds the automation config entries for Monarch
// on a sharded cluster. Each shard (configRS + data shards) gets an entry in the
// shards[] array, each pointing to its own K8s Service.
//
// clusterName is the sharded cluster's name (used for clusterPrefix if spec.monarch.s3.prefix is empty).
// shards contains info for configRS first, then each data shard in order.
func BuildMaintainedMonarchComponentsSharded(
	monarch *mdbv1.MonarchSpec,
	clusterName string,
	awsAccessKeyId, awsSecretAccessKey string,
	shards []ShardInfo,
) ([]MaintainedMonarchComponents, error) {
	if monarch == nil {
		return nil, fmt.Errorf("monarch spec is nil")
	}
	if len(shards) == 0 {
		return nil, fmt.Errorf("shards slice is empty")
	}

	initialMode := "STANDBY"
	if monarch.Role == mdbv1.MonarchRoleActive {
		initialMode = "ACTIVE"
	}

	// For sharded clusters, replicaSetId must match an actual RS in the AC.
	// We use the configRS name (first shard in the list).
	configRSName := shards[0].RSName
	mc := MaintainedMonarchComponents{
		ReplicaSetID:  configRSName,
		ClusterPrefix: monarch.S3.GetPrefix(clusterName),
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

	// Build MonarchShard for each shard (configRS + data shards)
	monarchShards := make([]MonarchShard, len(shards))
	for i, shard := range shards {
		instance := MonarchInstance{
			ID:                 i,
			Hostname:           shard.ServiceDNS,
			Port:               int(monarchpkg.ReplicationPort), //nolint:gosec
			ExternallyManaged:  true,
			HealthAPIEndpoint:  fmt.Sprintf("%s:%d", shard.ServiceDNS, monarchpkg.HealthPort),
			MonarchAPIEndpoint: fmt.Sprintf("%s:%d", shard.ServiceDNS, monarchpkg.APIPort),
		}

		if monarch.Role == mdbv1.MonarchRoleActive {
			instance.Mode = monarchShipperMode
			instance.BackupMongoNodeURI = shard.MongoURI
		} else {
			instance.SrcURI = shard.MongoURI
			instance.MongodURIs = shard.MongodURIs
			instance.InjectorHosts = []string{
				fmt.Sprintf("%s:%d", shard.ServiceDNS, monarchpkg.ReplicationPort),
			}
		}

		monarchShards[i] = MonarchShard{
			ShardID:     shard.ShardID,
			ReplSetName: shard.RSName,
			Instances:   []MonarchInstance{instance},
		}
	}

	if monarch.Role == mdbv1.MonarchRoleActive {
		mc.ShipperConfig = &ShipperConfig{
			Version: version,
			Shards:  monarchShards,
		}
	} else {
		mc.InjectorConfig = &InjectorConfig{
			Version: version,
			Shards:  monarchShards,
		}
	}

	return []MaintainedMonarchComponents{mc}, nil
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
