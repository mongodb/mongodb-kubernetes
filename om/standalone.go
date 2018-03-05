package om

import (
	//"github.com/10gen/mms-automation/go_planner/src/com.tengen/cm/state"
	"com.tengen/cm/hosts"
	"com.tengen/cm/state"
	"com.tengen/cm/state/processargs"
)

type Standalone struct {
	Process *state.ProcessConfig
}

func NewStandalone(ProcessVersion string) *Standalone {
	ans := &Standalone{}
	process := &state.ProcessConfig{}
	ans.Process = process

	process.AuthSchemaVersion = 5 // TODO calculate it based on mongo version
	process.Version = ProcessVersion
	// todo calcualte feature compatibility version from the version (leave only two digits)
	process.FeatureCompatibilityVersion = "3.6"
	process.ProcessType = "mongod"

	args := processargs.NewProcArgs(make(map[string]interface{}))
	process.Args = args

	args["net"] = map[string]int{"port": 27017}

	return ans
}

func (s *Standalone) Name(ProcessName string) *Standalone {
	s.Process.Name = ProcessName
	return s
}
func (s *Standalone) HostPort(ProcessHostname string) *Standalone {
	s.Process.Hostname = hosts.Host(ProcessHostname)
	return s
}
func (s *Standalone) DbPath(dbPath string) *Standalone {
	s.Process.Args["storage"] = map[string]string{"dbPath": dbPath}
	return s
}
func (s *Standalone) LogPath(logPath string) *Standalone {
	s.Process.Args["systemLog"] = map[string]string{"destination": "file", "path": logPath}
	return s
}
func (s *Standalone) SslCAFilePath(ProcessSslCAFilePath string) *Standalone {
	// todo
	//map[string](s.process.Args["net"])["ssl"]["CAFilePath"] = ProcessSslCAFilePath
	return s
}
func (s *Standalone) SslPemKeyFilePath(ProcessSslPemKeyFilePath string) *Standalone {
	//map[string](s.process.Args["net"])["ssl"]["autoPEMKeyFilePath"] = ProcessSslCAFilePath
	return s
}
func (s *Standalone) ClientCertificateMode(ProcessClientCertificateMode bool) *Standalone {
	//map[string](s.process.Args["net"])["ssl"]["clientCertificateMode"] = ProcessClientCertificateMode
	return s
}

func (s *Standalone) mergeInto(otherProcess *state.ProcessConfig) {
	otherProcess.Name = s.Process.Name
	otherProcess.Version = s.Process.Version
	otherProcess.Hostname = s.Process.Hostname
	otherProcess.Args["systemLog"] = s.Process.Args["systemLog"]
	otherProcess.Args["storage"] = s.Process.Args["storage"]
	// todo other fields
}
