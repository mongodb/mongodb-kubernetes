package deployment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/replicaset"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

// TestPrepareScaleDown_OpsManagerRemovedMember tests the situation when during scale down some replica set member doesn't
// exist (this can happen when for example the member was removed from Ops Manager manually). The exception is handled
// and only the existing member is marked as unvoted
func TestPrepareScaleDown_OpsManagerRemovedMember(t *testing.T) {
	// This is deployment with 2 members (emulating that OpsManager removed the 3rd one)
	rs := mdbv1.NewReplicaSetBuilder().SetName("bam").SetMembers(2).Build()
	oldDeployment := CreateFromReplicaSet("fake-mongoDBImage", rs)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)

	// We try to prepare two members for scale down, but one of them will fail (bam-2)
	rsWithThreeMembers := map[string][]string{"bam": {"bam-1", "bam-2"}}
	assert.NoError(t, replicaset.PrepareScaleDownFromMap(mockedOmConnection, rsWithThreeMembers, rsWithThreeMembers["bam"], zap.S()))

	expectedDeployment := CreateFromReplicaSet("fake-mongoDBImage", rs)

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted("bam", []string{"bam-1"}))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
}
