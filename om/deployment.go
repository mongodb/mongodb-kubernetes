package om

import (
	"com.tengen/cm/config"
	"com.tengen/cm/core"
	"com.tengen/cm/state"
	"com.tengen/cm/util"
	"k8s.io/apimachinery/pkg/util/json"
)

type ReplicaSets struct {
	*core.ReplSetConfig

	// Mask version, not expected by OM
	Version      bool `json:"version,omitempty"`
	WriteConcern bool `json:"writeConcernMajorityJournalDefault,omitempty"`
	Force        bool `json:"force,omitempty"`
}

// We cannot use ClusterConfig for serialization directly. We "embed" it instead and mask some of the fields (not
// beautiful but seems this is the easiest solution)
type Deployment struct {
	Version            int64                          `json:"version"`
	MonitoringVersions []*config.AgentVersion         `json:"monitoringVersions"`
	Processes          []*state.ProcessConfig         `json:"processes"`
	ReplicaSets        []*ReplicaSets                 `json:"replicaSets"`
	MongoDbVersions    []*config.MongoDbVersionConfig `json:"mongoDbVersions,omitempty"`
	Options            map[string]interface{}         `json:"options"`
	// Sharding           []*core.ShConfig               `json:"sharding,omitempty"`

	// masking this field - it will not be serialized
	Edition bool `json:"Edition,omitempty"`
}

func BuildDeploymentFromBytes(jsonBytes []byte) (ans *Deployment, err error) {
	cc := &Deployment{}
	if err := json.Unmarshal(jsonBytes, &cc); err != nil {
		return nil, err
	}
	return cc, nil
}

func NewDeployment(version string) *Deployment {
	ans := &Deployment{}
	ans.Options = make(map[string]interface{})
	// TODO this must be a global constant
	ans.Options["downloadBase"] = "/var/lib/mongodb-mms-automation"
	ans.MongoDbVersions = make([]*config.MongoDbVersionConfig, 1)
	ans.MongoDbVersions[0] = &config.MongoDbVersionConfig{Name: version}
	ans.ReplicaSets = make([]*ReplicaSets, 0)
	// ans.Sharding = make([]*core.ShConfig, 0)
	// not sure why this one is mandatory - it's necessary only for BI connector
	// ans.Mongosqlds = make([]*config.Mongosqld, 0)
	ans.Processes = make([]*state.ProcessConfig, 0)
	ans.MonitoringVersions = make([]*config.AgentVersion, 0)

	return ans
}

func (d *Deployment) AddStandaloneProcess(sa *state.ProcessConfig) {
	d.Processes = append(d.Processes, sa.DeepCopy(util.NewAtmContext()))
	newVersion := &config.AgentVersion{
		BaseUrl:  "",
		Hostname: string(sa.Hostname),
		Name:     "6.1.2.402-1",
	}
	d.MonitoringVersions = append(d.MonitoringVersions, newVersion)
}

func (d *Deployment) AddReplicaSet(rs *ReplicaSets) {
	d.ReplicaSets = append(d.ReplicaSets, rs)
}

// methods for config:
// merge Standalone. If we found the process with the same name - update some fields there. Otherwise add the new one
func (self *Deployment) MergeStandalone(standaloneMongo *Standalone) {
	for _, pr := range self.Processes {
		if pr.Name == standaloneMongo.Process.Name {
			standaloneMongo.mergeInto(pr)
			// todo logging
			return
		}
	}
	self.Processes = append(self.Processes, standaloneMongo.Process.DeepCopy(util.NewAtmContext()))
}

// merge replicaset
// merge sharded cluster
