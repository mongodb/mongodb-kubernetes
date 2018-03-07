package om

/*
This corresponds to
{
	"args2_6": {
		"net": {
			"port": 28002
		},
		"replication": {
			"replSetName": "blue"
		},
		"storage": {
			"dbPath": "/data/blue_2"
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

func NewProcess(processVersion string) Process {
	ans := Process{}

	initDefault(processVersion, ans)

	// default values for configurable values
	ans.SetDbPath("/data")
	ans.SetLogPath("/data/mongodb.log")

	return ans
}

func (s Process) SetName(processName string) Process {
	s["name"] = processName
	return s;
}

func (s Process) Name() string {
	return s["name"].(string)
}

func (s Process) SetHostName(processHostname string) Process {
	s["hostname"] = processHostname
	return s;
}

func (s Process) HostName() string {
	return s["hostname"].(string)
}

func (s Process) SetDbPath(dbPath string) Process {
	s.Args()["storage"] = map[string]string{"dbPath": dbPath}
	return s;
}

func (s Process) DbPath() string {
	return s.Args()["storage"].(map[string]string)["dbPath"]
}

func (s Process) SetLogPath(logPath string) Process {
	s.Args()["systemLog"] = map[string]string{"destination": "file", "path": logPath}
	return s;
}

func (s Process) LogPath() string {
	return s.Args()["systemLog"].(map[string]string)["path"]
}

func (s Process) SslCAFilePath(ProcessSslCAFilePath string) Process {
	// todo
	//map[string](s.process.Args["net"])["ssl"]["CAFilePath"] = ProcessSslCAFilePath
	return s;
}
func (s Process) SslPemKeyFilePath(ProcessSslPemKeyFilePath string) Process {
	//map[string](s.process.Args["net"])["ssl"]["autoPEMKeyFilePath"] = ProcessSslCAFilePath
	return s;
}
func (s Process) ClientCertificateMode(ProcessClientCertificateMode bool) Process {
	//map[string](s.process.Args["net"])["ssl"]["clientCertificateMode"] = ProcessClientCertificateMode
	return s;
}
func (s Process) Args() map[string]interface{} {
	if args, ok := s["args2_6"].(map[string]interface{}); ok {
		return args
	}
	args := make(map[string]interface{})
	s["args2_6"] = args
	return args;
}

func (s Process) Version() string {
	return s["version"].(string)
}

func (s Process) MergeFrom(otherProcess Process) {
	s.SetName(otherProcess.Name())
	s.SetHostName(otherProcess.HostName())
	s.SetDbPath(otherProcess.DbPath())
	s.SetLogPath(otherProcess.LogPath())

	initDefault(otherProcess.Version(), s)
	// todo other fields
}

// ****************** These ones are private methods not exposed to other packages *************************************

func initDefault(processVersion string, process Process) {
	process["version"] = processVersion
	process["authSchemaVersion"] = 5 // TODO calculate it based on mongo version
	// todo calcualte feature compatibility version from the version (leave only two digits)
	process["featureCompatibilityVersion"] = "3.6"
	process["processType"] = "mongod"

	if _, ok := process.Args()["net"]; !ok {
		process.Args()["net"] = make(map[string]interface{}, 0)
	}
	process.Args()["net"].(map[string]interface{})["port"] = 27017
}

// external packages are not supposed to call this method directly as it should be called during replica set building
func (s Process) setReplicaSetName(rsName string) Process {
	s.Args()["replication"] = map[string]string{"replSetName": rsName}
	return s
}
