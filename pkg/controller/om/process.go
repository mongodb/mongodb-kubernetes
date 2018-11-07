package om

import (
	"fmt"
	"path"

	"encoding/json"

	"strconv"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/spf13/cast"
)

type MongoType string

const (
	ProcessTypeMongos MongoType = "mongos"
	ProcessTypeMongod MongoType = "mongod"
)

/*
This is a class for all types of processes.
Note, that mongos types of processes don't have some fields (replication, storage etc) but it's impossible to use a
separate types for different processes (mongos, mongod) with different methods due to limitation of Go embedding model.
So the code using this type must be careful and make sure the state is consistent.

The resulting json for this type:

	{
		"args2_6": {
			"net": {
				"port": 28002
			},
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
type Process map[string]interface{}

func NewProcessFromInterface(i interface{}) Process {
	return i.(map[string]interface{})
}

func NewMongosProcess(name, hostName, processVersion string) Process {
	ans := Process{}

	initDefault(name, hostName, processVersion, ProcessTypeMongos, ans)

	// default values for configurable values
	ans.SetLogPath(path.Join(util.PvcMountPathLogs, "/mongodb.log"))

	return ans
}

func NewMongodProcess(name, hostName, processVersion string) Process {
	ans := Process{}

	initDefault(name, hostName, processVersion, ProcessTypeMongod, ans)

	// default values for configurable values
	ans.SetDbPath("/data")
	// CLOUDP-33467: we put mongod logs to the same directory as AA/Monitoring/Backup ones to provide single mount point
	// for all types of logs
	ans.SetLogPath(path.Join(util.PvcMountPathLogs, "mongodb.log"))

	return ans
}

func (s Process) DeepCopy() (Process, error) {
	return util.MapDeepCopy(s)
}

func (s Process) Name() string {
	return s["name"].(string)
}

func (s Process) HostName() string {
	return s["hostname"].(string)
}

func (s Process) SetDbPath(dbPath string) Process {
	readOrCreateMap(s.Args(), "storage")["dbPath"] = dbPath
	return s
}

func (s Process) DbPath() string {
	return readMapValueAsString(s.Args(), "storage", "dbPath")
}

func (s Process) SetWiredTigerCache(cacheSizeGb float32) Process {
	if s.ProcessType() != ProcessTypeMongod {
		// WiredTigerCache can be set only for mongod processes
		return s
	}
	storageMap := readOrCreateMap(s.Args(), "storage")
	wiredTigerMap := readOrCreateMap(storageMap, "wiredTiger")
	engineConfigMap := readOrCreateMap(wiredTigerMap, "engineConfig")
	engineConfigMap["cacheSizeGB"] = cacheSizeGb
	return s
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

func (s Process) SetLogPath(logPath string) Process {
	sysLogMap := readOrCreateMap(s.Args(), "systemLog")
	sysLogMap["destination"] = "file"
	sysLogMap["path"] = logPath
	return s
}

func (s Process) LogPath() string {
	return readMapValueAsString(s.Args(), "systemLog", "path")
}

func (s Process) SslCAFilePath(ProcessSslCAFilePath string) Process {
	// todo
	//map[string](s.process.Args["net"])["ssl"]["CAFilePath"] = ProcessSslCAFilePath
	return s
}
func (s Process) SslPemKeyFilePath(ProcessSslPemKeyFilePath string) Process {
	//map[string](s.process.Args["net"])["ssl"]["autoPEMKeyFilePath"] = ProcessSslCAFilePath
	return s
}
func (s Process) ClientCertificateMode(ProcessClientCertificateMode bool) Process {
	//map[string](s.process.Args["net"])["ssl"]["clientCertificateMode"] = ProcessClientCertificateMode
	return s
}

func (s Process) Args() map[string]interface{} {
	if _, ok := s["args2_6"]; !ok {
		s["args2_6"] = make(map[string]interface{}, 0)
	}
	return s["args2_6"].(map[string]interface{})
}

func (s Process) Version() string {
	return s["version"].(string)
}

func (s Process) ProcessType() MongoType {
	switch v := s["processType"].(type) {
	case string:
		return MongoType(v)
	case MongoType:
		return v
	default:
		panic(fmt.Sprintf("Unexpected type of processType variable: %T", v))
	}
}

func (s Process) Disabled() bool {
	return s["disabled"].(bool)
}

func (s Process) SetDisabled(disabled bool) {
	s["disabled"] = disabled
}

func (s Process) String() string {
	return fmt.Sprintf("\"%s\" (hostName: %s, version: %s, args: %s)", s.Name(), s.HostName(), s.Version(), s.Args())
}

// ****************** These ones are private methods not exposed to other packages *************************************

func initDefault(name, hostName, processVersion string, processType MongoType, process Process) {
	process["version"] = processVersion
	process["authSchemaVersion"] = calculateAuthSchemaVersion(processVersion)
	if compatibilityVersion := calculateFeatureCompatibilityVersion(processVersion); compatibilityVersion != "" {
		process["featureCompatibilityVersion"] = compatibilityVersion
	}
	process["processType"] = processType
	process["name"] = name
	process["hostname"] = hostName

	// Implementation note: seems we can easily use the default port for any process (mongos/configSrv/mongod) as all
	// processes are run in isolated containers and no conflicts can happen
	if _, ok := process.Args()["net"]; !ok {
		process.Args()["net"] = make(map[string]interface{}, 0)
	}
	process.Args()["net"].(map[string]interface{})["port"] = 27017
}

func calculateFeatureCompatibilityVersion(version string) string {
	v, err := util.ParseMongodbMinorVersion(version)
	// if there was error parsing - returning empty compatibility version
	if err != nil || v < 3.2 {
		return ""
	}
	// feature compatibility version has only two numbers, so we cannot just return the version
	return strconv.FormatFloat(float64(v), 'f', 1, 64)
}

func calculateAuthSchemaVersion(version string) int {
	v, err := util.ParseMongodbMinorVersion(version)
	// if there was error parsing - returning 5 as default
	if err != nil || v > 2.6 {
		return 5
	}
	return 3
}

// mergeFrom merges the Kubernetes version of process ("otherProcess") into OM one ("s").
// Considers the type of process and rewrites only relevant fields
func (s Process) mergeFrom(otherProcess Process) {
	s.SetLogPath(otherProcess.LogPath())

	if otherProcess.ProcessType() == ProcessTypeMongod {
		s.SetDbPath(otherProcess.DbPath())
		if otherProcess.replicaSetName() != "" {
			s.setReplicaSetName(otherProcess.replicaSetName())
		}
		// we override clusterRole only if it is set to "configsvr" - otherwise we leave the OM value
		if otherProcess.isClusterRoleConfigSrvSet() {
			s.setClusterRoleConfigSrv()
		}
		// This one is controversial. From some point users may want to change this through UI. From the other - if the
		// kube mongodb resource memory is increased - wired tiger cache must be increased as well automatically, so merge
		// must happen. This controversity will be gone when SERVER-16571 is fixed
		if otherProcess.WiredTigerCache() != nil {
			s.SetWiredTigerCache(*otherProcess.WiredTigerCache())
		}
	} else {
		s.setCluster(otherProcess.cluster())
	}

	initDefault(otherProcess.Name(), otherProcess.HostName(), otherProcess.Version(), otherProcess.ProcessType(), s)
}

func readOrCreateMap(m map[string]interface{}, key string) map[string]interface{} {
	if _, ok := m[key]; !ok {
		m[key] = make(map[string]interface{}, 0)
	}
	return m[key].(map[string]interface{})
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

func (s Process) authSchemaVersion() int {
	return s["authSchemaVersion"].(int)
}

func (s Process) featureCompatibilityVersion() string {
	if s["featureCompatibilityVersion"] == nil {
		return ""
	}
	return s["featureCompatibilityVersion"].(string)
}

// These methods are ONLY FOR REPLICA SET members!
// external packages are not supposed to call this method directly as it should be called during replica set building
func (s Process) setReplicaSetName(rsName string) Process {
	readOrCreateMap(s.Args(), "replication")["replSetName"] = rsName
	return s
}

func (s Process) replicaSetName() string {
	return readMapValueAsString(s.Args(), "replication", "replSetName")
}

// These methods are ONLY FOR CONFIG SERVER REPLICA SET members!
// external packages are not supposed to call this method directly as it should be called during sharded cluster merge
func (s Process) setClusterRoleConfigSrv() Process {
	readOrCreateMap(s.Args(), "sharding")["clusterRole"] = "configsvr"
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
