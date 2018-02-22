package om

import (
	"com.tengen/cm/config"
	"k8s.io/apimachinery/pkg/util/json"
)

type Deployment config.ClusterConfig

// build object from string
func BuildClusterConfigFromString(Json string) (ans *Deployment, err error) {
	cc := &Deployment{}
	if err := json.Unmarshal([]byte(Json), &cc); err != nil {
		return nil, err
	}
	return cc, nil
}

func newDeployment(version string) *Deployment {
	ans := &Deployment{}
	ans.Options = make(map[string]interface{})
	// TODO this must be a global constant
	ans.Options["downloadBase"] = "/var/lib/mongodb-mms-automation"
	ans.MongoDbVersions = make([]*config.MongoDbVersionConfig, 1)
	ans.MongoDbVersions = append(ans.MongoDbVersions, &config.MongoDbVersionConfig{Name: version})
	return ans
}

// methods for config:
// merge standalone. If we found the process with the same name - update some fields there. Otherwise add the new one
func (self *Deployment) mergeStandalone(standaloneMongo *standalone) {
	for _, pr := range self.Processes {
		if pr.Name == standaloneMongo.Process.Name {
			standaloneMongo.mergeInto(pr)
			// todo logging
			return
		}
	}
	self.Processes = append(self.Processes, standaloneMongo.Process)
}

// merge replicaset
// merge sharded cluster
