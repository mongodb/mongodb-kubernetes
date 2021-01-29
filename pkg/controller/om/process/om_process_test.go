package process

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

func TestCreateProcessesWiredTigerCache(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().SetVersion("4.0.0").Build()
	set := construct.DatabaseStatefulSet(*rs, construct.ReplicaSetOptions())
	processes := CreateMongodProcesses(set, util.DatabaseContainerName, rs)

	assert.Len(t, processes, 3)
	for _, p := range processes {
		// We don't expect wired tiger cache to be set if memory requirements are absent
		assert.Nil(t, p.WiredTigerCache())
	}

	rs.Spec.PodSpec = &mdbv1.NewPodSpecWrapperBuilder().SetMemory("3G").Build().MongoDbPodSpec

	set = construct.DatabaseStatefulSet(*rs, construct.ReplicaSetOptions())
	processes = CreateMongodProcesses(set, util.DatabaseContainerName, rs)

	assert.Len(t, processes, 3)
	for _, p := range processes {
		// Now wired tiger cache must be set to 50% of total memory - 1G
		assert.Equal(t, float32(1.0), *p.WiredTigerCache())
	}
}
