package replicaset

import (
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/stretchr/testify/assert"
	zap "go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

func TestBuildReplicaSetFromStatefulSetAppDb(t *testing.T) {

	for i := 0; i < 10; i++ {
		opsManager := omv1.NewOpsManagerBuilder().SetName("default-om").SetAppDbPodSpec(mdbv1.MongoDbPodSpec{}).Build()
		opsManager.Spec.AppDB.Members = i
		appDbSts, err := construct.AppDbStatefulSet(opsManager, &env.PodEnvVars{ProjectID: "abcd"}, construct.AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(&opsManager, omv1.DummmyCentralClusterName, 0, nil), nil)
		assert.NoError(t, err)
		omRs := BuildAppDBFromStatefulSet(appDbSts, omv1.AppDBSpec{Version: "4.4.0"})
		assert.Len(t, omRs.Processes, i)
	}
}
