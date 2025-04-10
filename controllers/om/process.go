package om

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/spf13/cast"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
)

// MongoType refers to the type of the Mongo process, `mongos` or `mongod`.
type MongoType string

const (
	// ProcessTypeMongos defines a constant for the mongos process type
	ProcessTypeMongos MongoType = "mongos"

	// ProcessTypeMongod defines a constant for the mongod process type.
	ProcessTypeMongod MongoType = "mongod"
)

/*
This is a class for all types of processes.
Note, that mongos types of processes don't have some fields (replication, storage etc.) but it's impossible to use a
separate types for different processes (mongos, mongod) with different methods due to limitation of Go embedding model.
So the code using this type must be careful and make sure the state is consistent.

Dev notes:
- any new configurations must be "mirrored" in 'mergeFrom' method which merges the "operator owned" fields into
the process that was read from Ops Manager.
- the main principle used everywhere in 'om' code: the Operator overrides only the configurations it "owns" but leaves
the other properties unmodified. That's why structs are not used anywhere as they would result in possible overriding of
the whole elements which we don't want. Deal with data as with maps, create convenience methods (setters, getters,
ensuremap etc.) and make sure not to override anything unrelated.

The resulting json for this type (example):

	{
		"args2_6": {
			"net": {
				"port": 28002,
                "ssl": {
					"mode": "requireSSL",
					"PEMKeyFile": "/mongodb-automation/server.pem"
					"clusterAuthFile: "/mongodb-automation/clusterfile.pem"
				}
			},
			"security" {
				"clusterAuthMode":"x509"
			}
			"replication": {
				"replSetName": "blue"
			},
			"storage": {
				"dbPath": "/data/blue_2",
				"wiredTiger": {
					"engineConfig": {
						"cacheSizeGB": 0.3
					}
				}
			},
			"systemLog": {
				"destination": "file",
				"path": "/data/blue_2/mongodb.log"
			}
		},
		"hostname": "AGENT_HOSTNAME",
		"logRotate": {
			"sizeThresholdMB": 1000,
			"timeThresholdHrs": 24
		},
		"name": "blue_2",
		"processType": "mongod",
		"version": "3.0.1",
		"authSchemaVersion": 3
	}
*/

// used to indicate standalones when a type is used as an identifier
type Standalone map[string]interface{}

type Process map[string]interface{}

func NewProcessFromInterface(i interface{}) Process {
	return i.(map[string]interface{})
}

func NewMongosProcess(name, hostName, mongoDBImage string, forceEnterprise bool, additionalMongodConfig *mdbv1.AdditionalMongodConfig, spec mdbv1.DbSpec, certificateFilePath string, annotations map[string]string, fcv string) Process {
	if additionalMongodConfig == nil {
		additionalMongodConfig = mdbv1.NewEmptyAdditionalMongodConfig()
	}

	architecture := architectures.GetArchitecture(annotations)
	processVersion := architectures.GetMongoVersionForAutomationConfig(mongoDBImage, spec.GetMongoDBVersion(), forceEnterprise, architecture)
	p := createProcess(
		WithName(name),
		WithHostname(hostName),
		WithProcessType(ProcessTypeMongos),
		WithAdditionalMongodConfig(*additionalMongodConfig),
		WithResourceSpec(processVersion, fcv),
	)

	// default values for configurable values
	p.SetLogPath(path.Join(util.PvcMountPathLogs, "/mongodb.log"))
	if certificateFilePath == "" {
		certificateFilePath = util.PEMKeyFilePathInContainer
	}

	p.ConfigureTLS(getTLSMode(spec, *additionalMongodConfig), certificateFilePath)

	return p
}

func NewMongodProcess(name, hostName, mongoDBImage string, forceEnterprise bool, additionalConfig *mdbv1.AdditionalMongodConfig, spec mdbv1.DbSpec, certificateFilePath string, annotations map[string]string, fcv string) Process {
	if additionalConfig == nil {
		additionalConfig = mdbv1.NewEmptyAdditionalMongodConfig()
	}

	architecture := architectures.GetArchitecture(annotations)
	processVersion := architectures.GetMongoVersionForAutomationConfig(mongoDBImage, spec.GetMongoDBVersion(), forceEnterprise, architecture)
	p := createProcess(
		WithName(name),
		WithHostname(hostName),
		WithProcessType(ProcessTypeMongod),
		WithAdditionalMongodConfig(*additionalConfig),
		WithResourceSpec(processVersion, fcv),
	)

	// default values for configurable values
	p.SetDbPath("/data")
	agentConfig := spec.GetAgentConfig()
	if agentConfig.Mongod.SystemLog != nil {
		p.SetLogPathFromCommunitySystemLog(agentConfig.Mongod.SystemLog)
	} else {
		// CLOUDP-33467: we put mongod logs to the same directory as AA/Monitoring/Backup ones to provide single mount point
		// for all types of logs
		p.SetLogPath(path.Join(util.PvcMountPathLogs, "mongodb.log"))
	}

	if certificateFilePath == "" {
		certificateFilePath = util.PEMKeyFilePathInContainer
	}
	p.ConfigureTLS(getTLSMode(spec, *additionalConfig), certificateFilePath)

	return p
}

func getTLSMode(spec mdbv1.DbSpec, additionalMongodConfig mdbv1.AdditionalMongodConfig) tls.Mode {
	if !spec.IsSecurityTLSConfigEnabled() {
		return tls.Disabled
	}
	return tls.GetTLSModeFromMongodConfig(additionalMongodConfig.ToMap())
}

func (p Process) DeepCopy() (Process, error) {
	return util.MapDeepCopy(p)
}

// Name returns the name of the process.
func (p Process) Name() string {
	return p["name"].(string)
}

// HostName returns the hostname for this process.
func (p Process) HostName() string {
	return p["hostname"].(string)
}

// GetVotes returns the number of votes requested for the member using this process.
func (p Process) GetVotes() int {
	if votes, ok := p["votes"]; ok {
		return cast.ToInt(votes)
	}
	return 1
}

// GetPriority returns the requested priority for the member using this process.
func (p Process) GetPriority() float32 {
	if priority, ok := p["priority"]; ok {
		return cast.ToFloat32(priority)
	}
	return 1.0
}

func (p Process) GetLogRotate() map[string]interface{} {
	if logRotate, ok := p["logRotate"]; ok {
		return logRotate.(map[string]interface{})
	}
	return make(map[string]interface{})
}

func (p Process) GetAuditLogRotate() map[string]interface{} {
	if logRotate, ok := p["auditLogRotate"]; ok {
		return logRotate.(map[string]interface{})
	}
	return make(map[string]interface{})
}

// GetTags returns the requested tags for the member using this process.
func (p Process) GetTags() map[string]string {
	if tags, ok := p["tags"]; ok {
		tagMap, ok := tags.(map[string]interface{})
		if !ok {
			return nil
		}
		result := make(map[string]string)
		for k, v := range tagMap {
			result[k] = cast.ToString(v)
		}
		return result
	}
	return nil
}

// SetDbPath sets the DbPath for this process.
func (p Process) SetDbPath(dbPath string) Process {
	util.ReadOrCreateMap(p.Args(), "storage")["dbPath"] = dbPath
	return p
}

// DbPath returns the DbPath for this process.
func (p Process) DbPath() string {
	return maputil.ReadMapValueAsString(p.Args(), "storage", "dbPath")
}

func (p Process) SetWiredTigerCache(cacheSizeGb float32) Process {
	if p.ProcessType() != ProcessTypeMongod {
		// WiredTigerCache can be set only for mongod processes
		return p
	}
	storageMap := util.ReadOrCreateMap(p.Args(), "storage")
	wiredTigerMap := util.ReadOrCreateMap(storageMap, "wiredTiger")
	engineConfigMap := util.ReadOrCreateMap(wiredTigerMap, "engineConfig")
	engineConfigMap["cacheSizeGB"] = cacheSizeGb
	return p
}

// WiredTigerCache returns wired tiger cache as pointer as it may be absent
func (p Process) WiredTigerCache() *float32 {
	value := maputil.ReadMapValueAsInterface(p.Args(), "storage", "wiredTiger", "engineConfig", "cacheSizeGB")
	if value == nil {
		return nil
	}
	f := cast.ToFloat32(value)
	return &f
}

func (p Process) SetLogPath(logPath string) Process {
	sysLogMap := util.ReadOrCreateMap(p.Args(), "systemLog")
	sysLogMap["destination"] = "file"
	sysLogMap["path"] = logPath
	return p
}

func (p Process) SetLogPathFromCommunitySystemLog(systemLog *automationconfig.SystemLog) Process {
	sysLogMap := util.ReadOrCreateMap(p.Args(), "systemLog")
	sysLogMap["destination"] = string(systemLog.Destination)
	sysLogMap["path"] = systemLog.Path
	sysLogMap["logAppend"] = systemLog.LogAppend
	return p
}

func (p Process) LogPath() string {
	return maputil.ReadMapValueAsString(p.Args(), "systemLog", "path")
}

func (p Process) LogRotateSizeThresholdMB() interface{} {
	return maputil.ReadMapValueAsInterface(p, "logRotate", "sizeThresholdMB")
}

// Args returns the "args" attribute in the form of a map, creates if it doesn't exist
func (p Process) Args() map[string]interface{} {
	return util.ReadOrCreateMap(p, "args2_6")
}

// Version of the process. This refers to the MongoDB server version.
func (p Process) Version() string {
	return p["version"].(string)
}

// ProcessType returns the type of process for the current process.
// It can be `mongos` or `mongod`.
func (p Process) ProcessType() MongoType {
	switch v := p["processType"].(type) {
	case string:
		return MongoType(v)
	case MongoType:
		return v
	default:
		panic(fmt.Sprintf("Unexpected type of processType variable: %T", v))
	}
}

// IsDisabled returns the "disabled" attribute.
func (p Process) IsDisabled() bool {
	return p["disabled"].(bool)
}

// SetDisabled sets the "disabled" attribute to `disabled`.
func (p Process) SetDisabled(disabled bool) {
	p["disabled"] = disabled
}

// EnsureNetConfig returns the Net configuration map ("net"), creates an empty map if it didn't exist
func (p Process) EnsureNetConfig() map[string]interface{} {
	return util.ReadOrCreateMap(p.Args(), "net")
}

// EnsureTLSConfig returns the TLS configuration map ("net.tls"), creates an empty map if it didn't exist.
// Use this method if you intend to make updates to the map returned
func (p Process) EnsureTLSConfig() map[string]interface{} {
	netConfig := p.EnsureNetConfig()
	return util.ReadOrCreateMap(netConfig, "tls")
}

// SSLConfig returns the TLS configuration map ("net.tls") or an empty map if it doesn't exist.
// Use this method only to read values, not update
func (p Process) TLSConfig() map[string]interface{} {
	netConfig := p.EnsureNetConfig()
	if _, ok := netConfig["tls"]; ok {
		return netConfig["tls"].(map[string]interface{})
	}

	return make(map[string]interface{})
}

func (p Process) EnsureSecurity() map[string]interface{} {
	return util.ReadOrCreateMap(p.Args(), "security")
}

// ConfigureClusterAuthMode sets the cluster auth mode for the process.
// Only accepted value for now is X509.
// internalClusterAuth is a parameter that overrides where the cert is located.
// If provided with an empty string, the operator will set it to
// a concatenation of the default mount path and the name of the process-pem
func (p Process) ConfigureClusterAuthMode(clusterAuthMode string, internalClusterPath string) Process {
	if strings.ToUpper(clusterAuthMode) == util.X509 { // Ops Manager value is "x509"
		// the individual key per pod will be podname-pem e.g. my-replica-set-0-pem
		p.setClusterAuthMode("x509")
		clusterFile := fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, p.Name())
		if internalClusterPath != "" {
			clusterFile = internalClusterPath
		}
		p.setClusterFile(clusterFile)
	}
	return p
}

func (p Process) IsTLSEnabled() bool {
	_, keyFile0 := p.TLSConfig()["PEMKeyFile"]
	_, keyFile1 := p.TLSConfig()["certificateKeyFile"]

	return keyFile0 || keyFile1
}

func (p Process) HasInternalClusterAuthentication() bool {
	return p.ClusterAuthMode() != ""
}

func (p Process) FeatureCompatibilityVersion() string {
	if p["featureCompatibilityVersion"] == nil {
		return ""
	}
	return p["featureCompatibilityVersion"].(string)
}

func (p Process) Alias() string {
	if alias, ok := p["alias"].(string); ok {
		return alias
	}

	return ""
}

// String
func (p Process) String() string {
	return fmt.Sprintf("\"%s\" (hostName: %s, version: %s, args: %s)", p.Name(), p.HostName(), p.Version(), p.Args())
}

// ****************** These ones are private methods not exposed to other packages *************************************

// createProcess initializes a process. It's a common initialization done for both mongos and mongod processes
func createProcess(opts ...ProcessOption) Process {
	process := Process{}
	for _, opt := range opts {
		opt(process)
	}
	return process
}

type ProcessOption func(process Process)

func WithResourceSpec(processVersion, fcv string) ProcessOption {
	return func(process Process) {
		process["version"] = processVersion
		process["authSchemaVersion"] = CalculateAuthSchemaVersion()
		process["featureCompatibilityVersion"] = fcv
	}
}

func WithName(name string) ProcessOption {
	return func(process Process) {
		process["name"] = name
	}
}

func WithHostname(hostname string) ProcessOption {
	return func(process Process) {
		process["hostname"] = hostname
	}
}

func WithProcessType(processType MongoType) ProcessOption {
	return func(process Process) {
		process["processType"] = processType
	}
}

func WithAdditionalMongodConfig(additionalConfig mdbv1.AdditionalMongodConfig) ProcessOption {
	return func(process Process) {
		// Applying the user-defined options if any
		process["args2_6"] = additionalConfig.ToMap()
		process.EnsureNetConfig()["port"] = additionalConfig.GetPortOrDefault()
	}
}

// ConfigureTLS enable TLS for this process. TLS will always be enabled after calling this. This function expects
// the value of "mode" to be an allowed ssl.mode from OM API perspective.
func (p Process) ConfigureTLS(mode tls.Mode, pemKeyFileLocation string) {
	// Initializing SSL configuration if it's necessary
	tlsConfig := p.EnsureTLSConfig()
	tlsConfig["mode"] = string(mode)

	if mode == tls.Disabled {
		// If these attribute exists, it needs to be removed
		// PEMKeyFile is older
		// certificateKeyFile is the current one
		delete(tlsConfig, "certificateKeyFile")
		delete(tlsConfig, "PEMKeyFile")
	} else {
		// PEMKeyFile is the legacy option found under net.ssl, deprecated since version 4.2
		// https://www.mongodb.com/docs/manual/reference/configuration-options/#mongodb-setting-net.ssl.PEMKeyFile
		_, oldKeyInConfig := tlsConfig["PEMKeyFile"]
		// certificateKeyFile is the current option under net.tls
		// https://www.mongodb.com/docs/manual/reference/configuration-options/#mongodb-setting-net.tls.certificateKeyFile
		_, newKeyInConfig := tlsConfig["certificateKeyFile"]

		// If both options are present in the TLS config we only want to keep the recent option. The problem
		// can be encountered when migrating the operator from an older version which pushed an automation config
		// containing the old key and the new operator is attempting to configure the new key.
		if oldKeyInConfig == newKeyInConfig {
			tlsConfig["certificateKeyFile"] = pemKeyFileLocation
			delete(tlsConfig, "PEMKeyFile")
		} else if newKeyInConfig {
			tlsConfig["certificateKeyFile"] = pemKeyFileLocation
		} else {
			tlsConfig["PEMKeyFile"] = pemKeyFileLocation
		}
	}
}

func CalculateAuthSchemaVersion() int {
	return 5
}

// mergeFrom merges the Operator version of process ('operatorProcess') into OM one ('p').
// Considers the type of process and rewrites only relevant fields
func (p Process) mergeFrom(operatorProcess Process, specArgs26, prevArgs26 map[string]interface{}) {
	// Dev note: merging the maps overrides/add map keys+value but doesn't remove the existing ones
	// If there are any keys that need to be removed explicitly (to ensure OM changes haven't sneaked through)
	// this must be done manually
	maputil.MergeMaps(p, operatorProcess)

	p["args2_6"] = maputil.RemoveFieldsBasedOnDesiredAndPrevious(p.Args(), specArgs26, prevArgs26)

	// Merge SSL configuration (update if it's specified - delete otherwise)
	if mode, ok := operatorProcess.TLSConfig()["mode"]; ok {
		tlsConfig := p.EnsureTLSConfig()
		for key, value := range operatorProcess.TLSConfig() {
			tlsConfig[key] = value
		}
		// PEMKeyFile is the legacy option found under net.ssl, deprecated since version 4.2
		// https://www.mongodb.com/docs/manual/reference/configuration-options/#mongodb-setting-net.ssl.PEMKeyFile
		_, oldKeyInConfig := tlsConfig["PEMKeyFile"]
		// certificateKeyFile is the current option under net.tls
		// https://www.mongodb.com/docs/manual/reference/configuration-options/#mongodb-setting-net.tls.certificateKeyFile
		_, newKeyInConfig := tlsConfig["certificateKeyFile"]
		// If both options are present in the TLS config we only want to keep the recent option.
		if oldKeyInConfig && newKeyInConfig {
			delete(tlsConfig, "PEMKeyFile")
		}
		// if the mode is specified as disabled, providing "PEMKeyFile" is an invalid config
		if mode == string(tls.Disabled) {
			delete(tlsConfig, "PEMKeyFile")
			delete(tlsConfig, "certificateKeyFile")
		}
	} else {
		delete(p.EnsureNetConfig(), "tls")
	}
}

func (p Process) setName(name string) Process {
	p["name"] = name
	return p
}

func (p Process) setClusterFile(filePath string) Process {
	p.EnsureTLSConfig()["clusterFile"] = filePath
	return p
}

func (p Process) setClusterAuthMode(authMode string) Process {
	p.EnsureSecurity()["clusterAuthMode"] = authMode
	return p
}

func (p Process) authSchemaVersion() int {
	return p["authSchemaVersion"].(int)
}

// These methods are ONLY FOR REPLICA SET members!
// external packages are not supposed to call this method directly as it should be called during replica set building
func (p Process) setReplicaSetName(rsName string) Process {
	util.ReadOrCreateMap(p.Args(), "replication")["replSetName"] = rsName
	return p
}

func (p Process) replicaSetName() string {
	return maputil.ReadMapValueAsString(p.Args(), "replication", "replSetName")
}

func (p Process) security() map[string]interface{} {
	args := p.Args()
	if _, ok := args["security"]; ok {
		return args["security"].(map[string]interface{})
	}
	return make(map[string]interface{})
}

func (p Process) ClusterAuthMode() string {
	if authMode, ok := p.security()["clusterAuthMode"]; ok {
		return authMode.(string)
	}
	return ""
}

// These methods are ONLY FOR CONFIG SERVER REPLICA SET members!
// external packages are not supposed to call this method directly as it should be called during sharded cluster merge
func (p Process) setClusterRoleConfigSrv() Process {
	util.ReadOrCreateMap(p.Args(), "sharding")["clusterRole"] = "configsvr"
	return p
}

// These methods are ONLY FOR MONGOS types!
// external packages are not supposed to call this method directly as it should be called during sharded cluster building
func (p Process) setCluster(clusterName string) Process {
	p["cluster"] = clusterName
	return p
}

func (p Process) cluster() string {
	return p["cluster"].(string)
}

func (p Process) json() string {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
	}
	return string(b)
}
