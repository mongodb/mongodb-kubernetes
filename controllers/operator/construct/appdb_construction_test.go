package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestAppDBAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.AutomationAgent.StartupParameters = agentStartupParameters
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	assert.NoError(t, err)

	command := sts.Spec.Template.Spec.Containers[0].Command
	assert.Contains(t, command[len(command)-1], "-Key1=Value1", "-Key2=Value2")
}

func TestAppDBMultiClusterPerClusterStatefulSetOverride(t *testing.T) {
	hostAliasesA := []corev1.HostAlias{{IP: "127.0.0.1", Hostnames: []string{"appdb-a.example.com"}}}
	hostAliasesB := []corev1.HostAlias{{IP: "127.0.0.1", Hostnames: []string{"appdb-b.example.com"}}}

	clusterSpecList := mdbv1.ClusterSpecList{
		{
			ClusterName: "cluster-a",
			Members:     2,
			StatefulSetConfiguration: &v1.StatefulSetConfiguration{
				SpecWrapper: v1.StatefulSetSpecWrapper{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{HostAliases: hostAliasesA},
						},
					},
				},
			},
		},
		{
			ClusterName: "cluster-b",
			Members:     1,
			StatefulSetConfiguration: &v1.StatefulSetConfiguration{
				SpecWrapper: v1.StatefulSetSpecWrapper{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{HostAliases: hostAliasesB},
						},
					},
				},
			},
		},
	}

	om := omv1.NewOpsManagerBuilderDefault().
		SetAppDBTopology(omv1.ClusterTopologyMultiCluster).
		SetAppDBClusterSpecList(clusterSpecList).
		Build()

	stsA, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, "cluster-a", 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	assert.NoError(t, err)
	assert.Equal(t, hostAliasesA, stsA.Spec.Template.Spec.HostAliases)

	stsB, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, "cluster-b", 1, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	assert.NoError(t, err)
	assert.Equal(t, hostAliasesB, stsB.Spec.Template.Spec.HostAliases)

	// The per-cluster override only sets hostAliases, so fields set by the base
	// construction (Replicas, ServiceName) must not be overwritten by the merge.
	assert.NotNil(t, stsA.Spec.Replicas)
	assert.Equal(t, int32(2), *stsA.Spec.Replicas)
	assert.NotEmpty(t, stsA.Spec.ServiceName)
	assert.NotNil(t, stsB.Spec.Replicas)
	assert.Equal(t, int32(1), *stsB.Spec.Replicas)
	assert.NotEmpty(t, stsB.Spec.ServiceName)
}

func TestAppDbStatefulSet_SingleAgentContainer(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	om := omv1.NewOpsManagerBuilderDefault().Build()
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)
	podVars := &env.PodEnvVars{ProjectID: "proj-123", AgentAPIKey: "key"}

	sts, err := AppDbStatefulSet(*om, podVars, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)

	containerNames := make([]string, 0)
	for _, c := range sts.Spec.Template.Spec.Containers {
		containerNames = append(containerNames, c.Name)
	}
	assert.Contains(t, containerNames, util.AgentContainerName)
	assert.NotContains(t, containerNames, monitoringAgentContainerName)
}

func TestAppDbStatefulSet_SingleAgentContainer_MonitoringDisabled(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)

	sts, err := AppDbStatefulSet(*om, nil, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)

	for _, c := range sts.Spec.Template.Spec.Containers {
		assert.NotEqual(t, monitoringAgentContainerName, c.Name)
	}
}

func TestAppDbStatefulSet_MonitoringCredentialsAsCLIFlags(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	om := omv1.NewOpsManagerBuilderDefault().Build()
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)
	podVars := &env.PodEnvVars{ProjectID: "proj-123", AgentAPIKey: "key"}

	sts, err := AppDbStatefulSet(*om, podVars, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)

	agent := findContainerByName(t, sts.Spec.Template.Spec.Containers, util.AgentContainerName)
	command := agent.Command[len(agent.Command)-1]
	assert.Contains(t, command, "-mmsGroupId=proj-123")
	assert.Contains(t, command, "-mmsApiKey=${AGENT_API_KEY}")
	assert.Contains(t, command, `AGENT_API_KEY="$(cat /mongodb-automation/agent-api-key/agentApiKey)"`)

	assert.NotNil(t, findVolumeByName(sts.Spec.Template.Spec.Volumes, AgentAPIKeyVolumeName),
		"agent-api-key volume must be present when monitoring is enabled")
	assert.True(t, containerMountsVolume(agent.VolumeMounts, AgentAPIKeyVolumeName),
		"agent container must mount the agent-api-key volume when monitoring is enabled")
}

func TestAppDbStatefulSet_NoMonitoringCredentialsWhenDisabled(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)

	// nil podVars => ShouldEnableMonitoring returns false
	sts, err := AppDbStatefulSet(*om, nil, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)

	agent := findContainerByName(t, sts.Spec.Template.Spec.Containers, util.AgentContainerName)
	command := agent.Command[len(agent.Command)-1]
	assert.NotContains(t, command, "-mmsGroupId=")
	assert.NotContains(t, command, "-mmsApiKey=")
	assert.NotContains(t, command, "AGENT_API_KEY=")

	assert.Nil(t, findVolumeByName(sts.Spec.Template.Spec.Volumes, AgentAPIKeyVolumeName),
		"agent-api-key volume must be absent when monitoring is disabled")
	assert.False(t, containerMountsVolume(agent.VolumeMounts, AgentAPIKeyVolumeName),
		"agent container must not mount the agent-api-key volume when monitoring is disabled")
}

func findContainerByName(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func findVolumeByName(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}
	return nil
}

func containerMountsVolume(mounts []corev1.VolumeMount, name string) bool {
	for _, m := range mounts {
		if m.Name == name {
			return true
		}
	}
	return false
}

func TestAppDbStatefulSet_UserTemplateMonitoringContainerStripped(t *testing.T) {
	monitoringContainer := corev1.Container{Name: monitoringAgentContainerName}
	podTemplate := corev1.PodTemplateSpec{}
	podTemplate.Spec.Containers = []corev1.Container{monitoringContainer}
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.PodSpec.PodTemplateWrapper = v1.PodTemplateSpecWrapper{
		PodTemplate: &podTemplate,
	}

	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)
	sts, err := AppDbStatefulSet(*om, nil, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)

	for _, c := range sts.Spec.Template.Spec.Containers {
		assert.NotEqual(t, monitoringAgentContainerName, c.Name, "monitoring container should be stripped from user template")
	}
}

// TestAppDbStatefulSet_MultiClusterIdentity verifies that in multi-cluster mode the AppDB
// StatefulSet carries no ownerReference (preventing cross-cluster GC orphan deletion) and
// does carry MongoDBMultiResourceAnnotation (so watch predicates and the OM connection
// factory can map the StatefulSet back to its parent MongoDBOpsManager CR).
func TestAppDbStatefulSet_MultiClusterIdentity(t *testing.T) {
	clusterSpecList := mdbv1.ClusterSpecList{
		{ClusterName: "cluster-a", Members: 1},
		{ClusterName: "cluster-b", Members: 1},
	}

	t.Run("multi-cluster mode: no ownerReferences, annotation set", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			SetAppDBTopology(omv1.ClusterTopologyMultiCluster).
			SetAppDBClusterSpecList(clusterSpecList).
			Build()

		sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
			AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, "cluster-a", 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
		assert.NoError(t, err)
		assert.Empty(t, sts.OwnerReferences,
			"StatefulSet in a remote member cluster must not carry an ownerReference pointing to the MongoDBOpsManager CR")
		assert.Equal(t, om.Name, sts.Annotations[handler.MongoDBMultiResourceAnnotation],
			"StatefulSet must carry MongoDBMultiResourceAnnotation so watch predicates and the OM connection factory can map it back to its parent CR")
	})

	t.Run("single-cluster mode: ownerReference set, no multi-cluster annotation", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().Build()

		sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
			AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
		assert.NoError(t, err)
		assert.Len(t, sts.OwnerReferences, 1,
			"StatefulSet in single-cluster mode must carry an ownerReference so Kubernetes GC can clean it up")
		assert.Empty(t, sts.Annotations[handler.MongoDBMultiResourceAnnotation],
			"StatefulSet in single-cluster mode must not carry MongoDBMultiResourceAnnotation")
	})
}

func TestResourceRequirements(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	agentResourceRequirements := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    ParseQuantityOrZero("200"),
			corev1.ResourceMemory: ParseQuantityOrZero("500M"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    ParseQuantityOrZero("100"),
			corev1.ResourceMemory: ParseQuantityOrZero("200M"),
		},
	}

	om.Spec.AppDB.PodSpec.PodTemplateWrapper = v1.PodTemplateSpecWrapper{
		PodTemplate: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:      "mongodb-agent",
						Resources: agentResourceRequirements,
					},
				},
			},
		},
	}

	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, "central", 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	assert.NoError(t, err)

	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == "mongodb-agent" {
			assert.Equal(t, agentResourceRequirements, c.Resources)
		}
	}
}
