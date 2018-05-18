package operator

import (
	"fmt"
	"testing"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
)

func TestReflection(t *testing.T) {
	state := KubeState{mongosSet: buildStatefulSet(&mongodb.MongoDbReplicaSet{}, "service", "test", "ns", "config", "agentSecret", 4), shardsSets: nil, configSrvSet: nil}
	state2 := &KubeState{mongosSet: buildStatefulSet(&mongodb.MongoDbReplicaSet{}, "service", "test", "ns", "config", "agentSecret", 4), shardsSets: nil, configSrvSet: nil}
	fmt.Println(state.mongosSet)
	fmt.Println(state2.mongosSet)

}
