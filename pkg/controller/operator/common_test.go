package operator

import (
	"context"
	"os"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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
	assert.NoError(t, prepareScaleDown(mockedOmConnection, rsWithThreeMembers, zap.S()))

	expectedDeployment := createDeploymentFromReplicaSet(rs)

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted("bam", []string{"bam-1"}))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
}

func InitDefaultEnvVariables() {
	os.Setenv(util.AppDBImageUrl, "some.repo")
	os.Setenv(util.AutomationAgentImage, "mongodb-enterprise-database")
	os.Setenv(util.AutomationAgentImagePullPolicy, "Never")
	os.Setenv(util.OpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-ops-manager")
	os.Setenv(util.InitOpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-init-ops-manager")
	os.Setenv(util.InitAppdbImageUrl, "quay.io/mongodb/mongodb-enterprise-init-appdb")
	os.Setenv(util.OpsManagerPullPolicy, "Never")
	os.Setenv(util.OmOperatorEnv, "test")
	os.Setenv(util.PodWaitSecondsEnv, "1")
	os.Setenv(util.PodWaitRetriesEnv, "2")
	os.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	os.Setenv(util.BackupDisableWaitRetriesEnv, "3")
	os.Setenv(util.AppDBReadinessWaitEnv, "0")
	os.Setenv(util.K8sCacheRefreshEnv, "0")
	os.Unsetenv(util.ManagedSecurityContextEnv)
	os.Unsetenv(util.ImagePullSecrets)
}

func TestCreateProcessesWiredTigerCache(t *testing.T) {
	st := DefaultStandaloneBuilder().SetVersion("4.0.0").Build()

	setHelper := defaultSetHelper().SetReplicas(3)
	set, _ := setHelper.BuildStatefulSet()
	processes := createProcesses(set, om.ProcessTypeMongod, st)

	assert.Len(t, processes, 3)
	for _, p := range processes {
		// We don't expect wired tiger cache to be set if memory requirements are absent
		assert.Nil(t, p.WiredTigerCache())
	}

	setHelper.SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("3G").Build())

	set, _ = setHelper.BuildStatefulSet()
	processes = createProcesses(set, om.ProcessTypeMongod, st)

	assert.Len(t, processes, 3)
	for _, p := range processes {
		// Now wired tiger cache must be set to 50% of total memory - 1G
		assert.Equal(t, float32(1.0), *p.WiredTigerCache())
	}
}

func TestWiredTigerCacheConversion(t *testing.T) {
	set, _ := defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("1800M").Build()).BuildStatefulSet()
	assert.Equal(t, float32(0.4), *calculateWiredTigerCache(set, "4.0.0"))

	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("2900M").Build()).BuildStatefulSet()
	assert.Equal(t, float32(0.95), *calculateWiredTigerCache(set, "4.0.4"))

	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build()).BuildStatefulSet()
	assert.Equal(t, float32(15.5), *calculateWiredTigerCache(set, "3.6.5"))

	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("55.832G").Build()).BuildStatefulSet()
	assert.Equal(t, float32(27.416), *calculateWiredTigerCache(set, "3.6.12"))

	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("181G").Build()).BuildStatefulSet()
	assert.Equal(t, float32(90.0), *calculateWiredTigerCache(set, "3.4.10"))

	// We round fractional part to two digits, here 256M were rounded to 0.26G
	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("300.65Mi").Build()).BuildStatefulSet()
	assert.Equal(t, float32(0.256), *calculateWiredTigerCache(set, "4.0.8"))

	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("0G").Build()).BuildStatefulSet()
	assert.Nil(t, calculateWiredTigerCache(set, "4.0.0"))

	// We don't calculate wired tiger cache for latest versions of mongodb
	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build()).BuildStatefulSet()
	assert.Nil(t, calculateWiredTigerCache(set, "4.2.0"))

	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build()).BuildStatefulSet()
	assert.Nil(t, calculateWiredTigerCache(set, "4.0.9"))

	set, _ = defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build()).BuildStatefulSet()
	assert.Nil(t, calculateWiredTigerCache(set, "3.6.13"))
}

func getStatefulSet(client *mock.MockedClient, name types.NamespacedName) *appsv1.StatefulSet {
	sts := &appsv1.StatefulSet{}
	_ = client.Get(context.TODO(), name, sts)
	return sts
}
