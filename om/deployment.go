package om

import (
	"com.tengen/cm/config"
	"com.tengen/cm/core"
	"com.tengen/cm/util"
	"k8s.io/apimachinery/pkg/util/json"
)

// We cannot use ClusterConfig for serialization directly. We "embed" it instead and mask some of the fields (not
// beautiful but seems this is the easiest solution)
type Deployment struct {
	*config.ClusterConfig

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

func newDeployment(version string) *Deployment {
	ans := &Deployment{ClusterConfig: &config.ClusterConfig{}}
	ans.Options = make(map[string]interface{})
	// TODO this must be a global constant
	ans.Options["downloadBase"] = "/var/lib/mongodb-mms-automation"
	ans.MongoDbVersions = make([]*config.MongoDbVersionConfig, 1)
	ans.MongoDbVersions[0] = &config.MongoDbVersionConfig{Name: version}
	ans.ReplicaSets = make([]*core.ReplSetConfig, 0)
	ans.Sharding = make([]*core.ShConfig, 0)
	// not sure why this one is mandatory - it's necessary only for BI connector
	ans.Mongosqlds = make([]*config.Mongosqld, 0)
	return ans
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
