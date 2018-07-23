package operator

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/om"
	"go.uber.org/zap"
)

// TestPrepareScaleDown_OpsManagerRemovedMember tests the situation when during scale down some replica set member doesn't
// exist (this can happen when for example the member was removed from Ops Manager manually). The exception is handled
// and only the existing member is marked as unvoted
func TestPrepareScaleDown_OpsManagerRemovedMember(t *testing.T) {
	// This is deployment with 2 members (emulating that OpsManager removed the 3rd one)
	rs := DefaultReplicaSetBuilder().SetName("bam").SetMembers(2).Build()
	oldDeployment := createDeploymentFromReplicaSet(rs)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)

	// We try to prepare two members for scale down, but one of them will fail (bam-2)
	rsWithThreeMembers := map[string][]string{"bam": {"bam-1", "bam-2"}}
	prepareScaleDown(mockedOmConnection, rsWithThreeMembers, zap.S())

	expectedDeployment := createDeploymentFromReplicaSet(rs)

	expectedDeployment.MarkRsMembersUnvoted("bam", []string{"bam-1"})

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
}
