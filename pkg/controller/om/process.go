package om

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/blang/semver"
	"github.com/spf13/cast"
	"go.uber.org/zap"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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
Note, that mongos types of processes don't have some fields (replication, storage etc) but it's impossible to use a
separate types for different processes (mongos, mongod) with different methods due to limitation of Go embedding model.
So the code using this type must be careful and make sure the state is consistent.

Dev notes:
- any new configurations must be "mirrored" in 'mergeFrom' method which merges the "operator owned" fields into
the process that was read from Ops Manager.
- the main principle used everywhere in 'om' code: the Operator overrides only the configurations it "owns" but leaves
the other properties unmodified. That's why structs are not used anywhere as they would result in possible overriding of
the whole elements which we don't want. Deal with data as with maps, create convenience methods (setters, getters,
ensuremap etc) and make sure not to override anything unrelated.

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

// Process
type Process map[string]interface{}

// NewProcessFromInterface
func NewProcessFromInterface(i interface{}) Process {
	return i.(map[string]interface{})
}

// NewMongosProcess
func NewMongosProcess(name, hostName string, resource *mdbv1.MongoDB) Process {
	p := Process{}

	initDefault(name, hostName, resource.Spec.GetVersion(), resource.Spec.FeatureCompatibilityVersion, ProcessTypeMongos, p)

	// default values for configurable values
	p.SetLogPath(path.Join(util.PvcMountPathLogs, "/mongodb.log"))
	p.ConfigureTLS(resource.Spec.GetTLSMode(), util.PEMKeyFilePathInContainer)
	return p
}

// NewMongodProcess
func NewMongodProcess(name, hostName string, resource *mdbv1.MongoDB) Process {
	p := Process{}

	initDefault(name, hostName, resource.Spec.GetVersion(), resource.Spec.FeatureCompatibilityVersion, ProcessTypeMongod, p)

	// default values for configurable values
	p.SetDbPath("/data")
	// CLOUDP-33467: we put mongod logs to the same directory as AA/Monitoring/Backup ones to provide single mount point
	// for all types of logs
	p.SetLogPath(path.Join(util.PvcMountPathLogs, "mongodb.log"))
	p.ConfigureTLS(resource.Spec.GetTLSMode(), util.PEMKeyFilePathInContainer)
	return p
}

// DeepCopy
func (s Process) DeepCopy() (Process, error) {
	return util.MapDeepCopy(s)
}

// Name returns the name of the process.
func (p Process) Name() string {
	return p["name"].(string)
}

// HostName returns the hostname for this process.
func (p Process) HostName() string {
	return p["hostname"].(string)
}

// SetDbPath sets the DbPath for this process.
func (p Process) SetDbPath(dbPath string) Process {
	util.ReadOrCreateMap(p.Args(), "storage")["dbPath"] = dbPath
	return p
}

// DbPath returns the DbPath for this process.
func (p Process) DbPath() string {
	return readMapValueAsString(p.Args(), "storage", "dbPath")
}

// SetWiredTigerCache
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
func (s Process) WiredTigerCache() *float32 {
	value := readMapValueAsInterface(s.Args(), "storage", "wiredTiger", "engineConfig", "cacheSizeGB")
	if value == nil {
		return nil
	}
	f := cast.ToFloat32(value)
	return &f
}

// SetLogPath
func (s Process) SetLogPath(logPath string) Process {
	sysLogMap := util.ReadOrCreateMap(s.Args(), "systemLog")
	sysLogMap["destination"] = "file"
	sysLogMap["path"] = logPath
	return s
}

// LogPath
func (s Process) LogPath() string {
	return readMapValueAsString(s.Args(), "systemLog", "path")
}

// Args returns the "args" attribute in the form of a map, creates if it doesn't exist
func (p Process) Args() map[string]interface{} {
	return util.ReadOrCreateMap(p, "args2_6")
}

// Version of the process. This refers to the MongoDB server version.
func (p Process) Version() string {
	return p["version"].(string)
}

// ProcessType returs the type of process for the current process.
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

// EnsureSSLConfig returns the SSL configuration map ("net.ssl"), creates an empty map if it didn't exist.
// Use this method if you intend to make updates to the map returned
func (p Process) EnsureSSLConfig() map[string]interface{} {
	netConfig := p.EnsureNetConfig()
	return util.ReadOrCreateMap(netConfig, "ssl")
}

// SSLConfig returns the SSL configuration map ("net.ssl") or an empty map if it doesn't exist.
// Use this method only to read values, not update
func (p Process) SSLConfig() map[string]interface{} {
	netConfig := p.EnsureNetConfig()
	if _, ok := netConfig["ssl"]; ok {
		return netConfig["ssl"].(map[string]interface{})
	}

	return make(map[string]interface{})
}

func (p Process) EnsureSecurity() map[string]interface{} {
	return util.ReadOrCreateMap(p.Args(), "security")
}

func (p Process) ConfigureClusterAuthMode(clusterAuthMode string) Process {
	if strings.ToUpper(clusterAuthMode) == util.X509 { // Ops Manager value is "x509"
		// the individual key per pod will be podname-pem e.g. my-replica-set-0-pem
		p.setClusterAuthMode("x509")
		p.setClusterFile(fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, p.Name()))
	}
	return p
}

func (p Process) IsTLSEnabled() bool {
	_, ok := p.SSLConfig()["PEMKeyFile"]
	return ok
}

func (p Process) HasInternalClusterAuthentication() bool {
	return p.ClusterAuthMode() != ""
}

func (s Process) FeatureCompatibilityVersion() string {
	if s["featureCompatibilityVersion"] == nil {
		return ""
	}
	return s["featureCompatibilityVersion"].(string)
}

// String
func (p Process) String() string {
	return fmt.Sprintf("\"%s\" (hostName: %s, version: %s, args: %s)", p.Name(), p.HostName(), p.Version(), p.Args())
}

// ****************** These ones are private methods not exposed to other packages *************************************

// initDefault initializes a process. It's called during "merge" process when the Operator view is merged with OM one -
// it's supposed to override all the OM provided information. So the easiest way to ensure no fields are overriden by
// OM is to set them in this method
func initDefault(name, hostName, processVersion string, featureCompatibilityVersion *string, processType MongoType, process Process) {
	process["version"] = processVersion
	process["authSchemaVersion"] = calculateAuthSchemaVersion(processVersion)
	if featureCompatibilityVersion == nil {
		computedFcv := calculateFeatureCompatibilityVersion(processVersion)
		featureCompatibilityVersion = &computedFcv
	}

	process["featureCompatibilityVersion"] = *featureCompatibilityVersion
	process["processType"] = processType
	process["name"] = name
	process["hostname"] = hostName

	// Implementation note: seems we can easily use the default port for any process (mongos/configSrv/mongod) as all
	// processes are run in isolated containers and no conflicts can happen
	process.EnsureNetConfig()["port"] = util.MongoDbDefaultPort
}

// ConfigureTLS enable TLS for this process. TLS will be always enabled after calling this. This function expects
// the value of "mode" to be an allowed ssl.mode from OM API perspective.
func (p Process) ConfigureTLS(mode mdbv1.SSLMode, pemKeyFileLocation string) {
	// Initializing SSL configuration if it's necessary
	sslConfig := p.EnsureSSLConfig()

	sslConfig["mode"] = string(mode)
	sslConfig["PEMKeyFile"] = pemKeyFileLocation

	if mode == mdbv1.DisabledSSLMode {
		delete(sslConfig, "PEMKeyFile")
	}
}

func calculateFeatureCompatibilityVersion(version string) string {
	v1, err := semver.Make(version)
	if err != nil {
		zap.S().Warnf("Failed to parse version %s: %s", version, err)
		return ""
	}

	baseVersion, _ := semver.Make("3.4.0")
	if v1.GTE(baseVersion) {
		ans, _ := util.MajorMinorVersion(version)
		return ans
	}

	return ""
}

// see https://github.com/10gen/ops-manager-kubernetes/pull/68#issuecomment-397247337
func calculateAuthSchemaVersion(version string) int {
	v, err := semver.Make(version)
	if err != nil {
		zap.S().Warnf("Failed to parse version %s: %s", version, err)
		return 5
	}

	baseVersion, _ := semver.Make("3.0.0")
	if v.GTE(baseVersion) {
		// Version >= 3.0
		return 5
	}

	// Version 2.6
	return 3
}

// mergeFrom merges the Operator version of process ('operatorProcess') into OM one ('p').
// Considers the type of process and rewrites only relevant fields
func (p Process) mergeFrom(operatorProcess Process) {
	p.SetLogPath(operatorProcess.LogPath())

	if operatorProcess.ProcessType() == ProcessTypeMongod {
		p.SetDbPath(operatorProcess.DbPath())
		if operatorProcess.replicaSetName() != "" {
			p.setReplicaSetName(operatorProcess.replicaSetName())
		}
		// we override clusterRole only if it is set to "configsvr" - otherwise we leave the OM value
		if operatorProcess.isClusterRoleConfigSrvSet() {
			p.setClusterRoleConfigSrv()
		}
		// This one is controversial. From some point users may want to change this through UI. From the other - if the
		// kube mongodb resource memory is increased - wired tiger cache must be increased as well automatically, so merge
		// must happen. We leave this even after SERVER-16571 is fixed as we should support manual setting of the cache
		// for earlier versions of mongodb
		if operatorProcess.WiredTigerCache() != nil {
			p.SetWiredTigerCache(*operatorProcess.WiredTigerCache())
		}
	} else {
		p.setCluster(operatorProcess.cluster())
	}

	// update authentication mode and clusterFile path for both process types
	p.ConfigureClusterAuthMode(operatorProcess.ClusterAuthMode())

	fcv := operatorProcess.FeatureCompatibilityVersion()
	initDefault(
		operatorProcess.Name(),
		operatorProcess.HostName(),
		operatorProcess.Version(),
		&fcv,
		operatorProcess.ProcessType(),
		p,
	)

	// Merge SSL configuration (update if it's specified - delete otherwise)
	if mode, ok := operatorProcess.SSLConfig()["mode"]; ok {
		for key, value := range operatorProcess.SSLConfig() {
			p.EnsureSSLConfig()[key] = value
		}
		// if the mode is specified as disabled, providing "PEMKeyFile" is an invalid config
		if mode == string(mdbv1.DisabledSSLMode) {
			delete(p.EnsureSSLConfig(), "PEMKeyFile")
		}
	} else {
		delete(p.EnsureNetConfig(), "ssl")
	}
}

func readMapValueAsInterface(m map[string]interface{}, keys ...string) interface{} {
	currentMap := m
	for i, k := range keys {
		if _, ok := currentMap[k]; !ok {
			return nil
		}
		if i == len(keys)-1 {
			return currentMap[k]
		}
		currentMap = currentMap[k].(map[string]interface{})
	}
	return nil
	/*if _, ok := m[key]; !ok {
		return ""
	}
	secondMap := m[key].(map[string]interface{})

	if _, ok := secondMap[secondKey]; !ok {
		return ""
	}
	return secondMap[secondKey].(string)*/
}

func readMapValueAsString(m map[string]interface{}, keys ...string) string {
	res := readMapValueAsInterface(m, keys...)

	if res == nil {
		return ""
	}
	return res.(string)
}

func (s Process) setName(name string) Process {
	s["name"] = name
	return s
}

func (p Process) setClusterFile(filePath string) Process {
	p.EnsureSSLConfig()["clusterFile"] = filePath
	return p
}

func (p Process) setClusterAuthMode(authMode string) Process {
	p.EnsureSecurity()["clusterAuthMode"] = authMode
	return p
}

func (s Process) authSchemaVersion() int {
	return s["authSchemaVersion"].(int)
}

// These methods are ONLY FOR REPLICA SET members!
// external packages are not supposed to call this method directly as it should be called during replica set building
func (s Process) setReplicaSetName(rsName string) Process {
	util.ReadOrCreateMap(s.Args(), "replication")["replSetName"] = rsName
	return s
}

func (s Process) replicaSetName() string {
	return readMapValueAsString(s.Args(), "replication", "replSetName")
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
func (s Process) setClusterRoleConfigSrv() Process {
	util.ReadOrCreateMap(s.Args(), "sharding")["clusterRole"] = "configsvr"
	return s
}

func (s Process) isClusterRoleConfigSrvSet() bool {
	return readMapValueAsString(s.Args(), "sharding", "clusterRole") == "configsvr"
}

// These methods are ONLY FOR MONGOS types!
// external packages are not supposed to call this method directly as it should be called during sharded cluster building
func (s Process) setCluster(clusterName string) Process {
	s["cluster"] = clusterName
	return s
}

func (s Process) cluster() string {
	return s["cluster"].(string)
}

func (s Process) json() string {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
	}
	return string(b)
}
