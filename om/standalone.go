package om

import (
	//"github.com/10gen/mms-automation/go_planner/src/com.tengen/cm/state"
	"com.tengen/cm/state"
	"com.tengen/cm/hosts"
	"com.tengen/cm/state/processargs"
)

type standalone struct {
	Process *state.ProcessConfig
}

func NewStandalone(ProcessVersion string) *standalone {
	ans := &standalone{}
	process := &state.ProcessConfig{}
	ans.Process = process

	process.AuthSchemaVersion = 5 // TODO calculate it based on mongo version
	process.Version = ProcessVersion
	process.ProcessType = "mongod"

	args := processargs.NewProcArgs(make(map[string]interface{}))
	process.Args = args

	args["net"] = map[string]int{"port": 27017}

	return ans
}

func (self *standalone) Name(ProcessName string) *standalone {
	self.Process.Name = ProcessName
	return self;
}
func (self *standalone) HostPort(ProcessHostname string) *standalone {
	self.Process.Hostname = hosts.Host(ProcessHostname)
	return self;
}
func (self *standalone) DbPath(dbPath string) *standalone {
	self.Process.Args["storage"] = map[string]string{"dbPath": dbPath}
	return self;
}
func (self *standalone) LogPath(logPath string) *standalone {
	self.Process.Args["systemLog"] = map[string]string{"destination": "file", "path": logPath}
	return self;
}
func (self *standalone) SslCAFilePath(ProcessSslCAFilePath string) *standalone {
	// todo
	//map[string](self.process.Args["net"])["ssl"]["CAFilePath"] = ProcessSslCAFilePath
	return self;
}
func (self *standalone) SslPemKeyFilePath(ProcessSslPemKeyFilePath string) *standalone {
	//map[string](self.process.Args["net"])["ssl"]["autoPEMKeyFilePath"] = ProcessSslCAFilePath
	return self;
}
func (self *standalone) ClientCertificateMode(ProcessClientCertificateMode bool) *standalone {
	//map[string](self.process.Args["net"])["ssl"]["clientCertificateMode"] = ProcessClientCertificateMode
	return self;
}

func (self *standalone) mergeInto(otherProcess *state.ProcessConfig) {
	otherProcess.Name = self.Process.Name
	otherProcess.Version = self.Process.Version
	otherProcess.Hostname = self.Process.Hostname
	otherProcess.Args["systemLog"] = self.Process.Args["systemLog"]
	otherProcess.Args["storage"] = self.Process.Args["storage"]
	// todo other fields
}
