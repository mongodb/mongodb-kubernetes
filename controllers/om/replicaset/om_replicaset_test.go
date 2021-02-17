package replicaset

import (
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

func TestBuildReplicaSetFromStatefulSetAppDb(t *testing.T) {
	for i := 0; i < 10; i++ {
		appDbSts := construct.AppDbStatefulSet(omv1.NewOpsManagerBuilder().SetAppDbPodSpec(mdbv1.MongoDbPodSpec{}).Build(),
			func(options *construct.DatabaseStatefulSetOptions) {
				options.Replicas = i
			},
		)
		omRs := BuildAppDBFromStatefulSet(appDbSts, omv1.AppDB{})
		assert.Len(t, omRs.Processes, i)
	}
}
