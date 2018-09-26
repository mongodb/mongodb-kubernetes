package operator

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/util"

	"os"

	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	InitDefaultEnvVariables()
}

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

func InitDefaultEnvVariables() {
	os.Setenv(util.AutomationAgentImageUrl, "mongodb-enterprise-database")
	os.Setenv(util.AutomationAgentImagePullPolicy, "Never")
	os.Setenv(util.OmOperatorEnv, "test")
	os.Setenv(util.StatefulSetWaitSecondsEnv, "1")
	os.Setenv(util.StatefulSetWaitRetriesEnv, "2")
	os.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	os.Setenv(util.BackupDisableWaitRetriesEnv, "3")
}

func TestCreateProcessesWiredTigerCache(t *testing.T) {
	setHelper := defaultSetHelper().SetReplicas(3)
	set := setHelper.BuildStatefulSet()
	processes := createProcesses(set, "", "4.0.0", om.ProcessTypeMongod)

	assert.Len(t, processes, 3)
	for _, p := range processes {
		// We don't expect wired tiger cache to be set if memory requirements are absent
		assert.Nil(t, p.WiredTigerCache())
	}

	setHelper.SetPodSpec(defaultPodSpec().SetMemory("3G"))

	set = setHelper.BuildStatefulSet()
	processes = createProcesses(set, "", "4.0.0", om.ProcessTypeMongod)

	assert.Len(t, processes, 3)
	for _, p := range processes {
		// Now wired tiger cache must be set to 50% of total memory - 1G
		assert.Equal(t, float32(1.0), *p.WiredTigerCache())
	}
}

func TestWiredTigerCacheConversion(t *testing.T) {
	set := defaultSetHelper().SetPodSpec(defaultPodSpec().SetMemory("1800M")).BuildStatefulSet()
	assert.Equal(t, float32(0.4), *calculateWiredTigerCache(set))

	set = defaultSetHelper().SetPodSpec(defaultPodSpec().SetMemory("2900M")).BuildStatefulSet()
	assert.Equal(t, float32(0.95), *calculateWiredTigerCache(set))

	set = defaultSetHelper().SetPodSpec(defaultPodSpec().SetMemory("32G")).BuildStatefulSet()
	assert.Equal(t, float32(15.5), *calculateWiredTigerCache(set))

	set = defaultSetHelper().SetPodSpec(defaultPodSpec().SetMemory("55.832G")).BuildStatefulSet()
	assert.Equal(t, float32(27.416), *calculateWiredTigerCache(set))

	set = defaultSetHelper().SetPodSpec(defaultPodSpec().SetMemory("181G")).BuildStatefulSet()
	assert.Equal(t, float32(90.0), *calculateWiredTigerCache(set))

	// We round fractional part to two digits, here 256M were rounded to 0.26G
	set = defaultSetHelper().SetPodSpec(defaultPodSpec().SetMemory("300.65Mi")).BuildStatefulSet()
	assert.Equal(t, float32(0.256), *calculateWiredTigerCache(set))

	set = defaultSetHelper().SetPodSpec(defaultPodSpec().SetMemory("0G")).BuildStatefulSet()
	assert.Nil(t, calculateWiredTigerCache(set))
}
