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

func TestAppDbStatefulSet_StripsUserOwnerLabels(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Labels = map[string]string{
		"app":                                    "demo",
		util.MongoDBResourceOwnerLabel:           "spoofed-mdb",
		util.MongoDBOpsManagerResourceOwnerLabel: "spoofed-om",
	}
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)

	sts, err := AppDbStatefulSet(*om, nil, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)

	assert.Equal(t, "demo", sts.Labels["app"], "non-reserved user label must survive")
	assert.NotContains(t, sts.Labels, util.MongoDBResourceOwnerLabel, "user-supplied participant key must be stripped")
	assert.Equal(t, om.GetOwnerLabels()[util.MongoDBOpsManagerResourceOwnerLabel], sts.Labels[util.MongoDBOpsManagerResourceOwnerLabel],
		"user-supplied OpsManager owner label value must be replaced by GetOwnerLabels")

	ownerLabels := om.GetOwnerLabels()
	require.NotEmpty(t, ownerLabels)
	for k, v := range ownerLabels {
		assert.Equal(t, v, sts.Labels[k], "owner label %s from GetOwnerLabels must be present", k)
	}
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

func TestCustomAgentURL(t *testing.T) {
	const customURL = "https://example.com/mongodb-mms-automation-agent-108.0.26.9047-1.rhel8_x86_64.tar.gz"

	t.Run("Database StatefulSet has MDB_CUSTOM_AGENT_URL env var", func(t *testing.T) {
		rs := mdbv1.NewReplicaSetBuilder().Build()
		optsFunc := ReplicaSetOptions(GetPodEnvOptions())
		sts := DatabaseStatefulSet(*rs, func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
			opts := optsFunc(mdb)
			opts.CustomAgentURL = customURL
			return opts
		}, zap.S())

		envMap := env.ToMap(sts.Spec.Template.Spec.Containers[0].Env...)
		assert.Equal(t, customURL, envMap[util.EnvVarCustomAgentURL])
	})

	t.Run("AppDB StatefulSet has MDB_CUSTOM_AGENT_URL env var", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().Build()
		sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
			AppDBStatefulSetOptions{CustomAgentURL: customURL},
			scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil),
			appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
		assert.NoError(t, err)

		for _, c := range sts.Spec.Template.Spec.Containers {
			envMap := env.ToMap(c.Env...)
			if _, ok := envMap[util.EnvVarCustomAgentURL]; ok {
				assert.Equal(t, customURL, envMap[util.EnvVarCustomAgentURL])
				return
			}
		}
		t.Fatal("MDB_CUSTOM_AGENT_URL not found in any AppDB container")
	})

	t.Run("AppDB StatefulSet static has env var but no download snippet", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().Build()
		sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
			AppDBStatefulSetOptions{CustomAgentURL: customURL},
			scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil),
			appsv1.OnDeleteStatefulSetStrategyType, architectures.Static, zap.S())
		assert.NoError(t, err)

		// Env var is still set in static mode.
		found := false
		for _, c := range sts.Spec.Template.Spec.Containers {
			envMap := env.ToMap(c.Env...)
			if v, ok := envMap[util.EnvVarCustomAgentURL]; ok {
				assert.Equal(t, customURL, v)
				found = true
			}
		}
		assert.True(t, found, "MDB_CUSTOM_AGENT_URL should be set in static mode too")

		// Download snippet must not be in the command.
		for _, c := range sts.Spec.Template.Spec.Containers {
			if len(c.Command) > 2 {
				assert.NotContains(t, c.Command[2], "agent_binary=/tmp/mongodb-agent",
					"static mode should not include download snippet")
			}
		}
	})

	t.Run("AppDB StatefulSet without CustomAgentURL has no env var", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().Build()
		sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
			AppDBStatefulSetOptions{},
			scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil),
			appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
		assert.NoError(t, err)

		for _, c := range sts.Spec.Template.Spec.Containers {
			envMap := env.ToMap(c.Env...)
			_, ok := envMap[util.EnvVarCustomAgentURL]
			assert.False(t, ok, "MDB_CUSTOM_AGENT_URL should not be set when CustomAgentURL is empty")
		}
	})
}

func TestAppDbStatefulSet_StaticGAUsesProxyWithLocalConfig(t *testing.T) {
	t.Setenv(util.ImagePullPolicyEnv, string(corev1.PullIfNotPresent))

	om := omv1.NewOpsManagerBuilderDefault().Build()
	opts := AppDBStatefulSetOptions{
		InitAppDBImage:    "init-database:1.0.0",
		MongodbImage:      "mongodb-enterprise-server:8.0.0",
		MongodbProxyImage: "mongodb-proxy:1.0.0",
		AgentImage:        "mongodb-agent:1000.2.0.11583-1",
		CustomAgentURL:    "https://example.com/agent.tar.gz",
	}
	sts, err := AppDbStatefulSet(*om, nil, opts,
		scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil),
		appsv1.OnDeleteStatefulSetStrategyType, architectures.StaticGA, zap.S())
	require.NoError(t, err)

	podSpec := sts.Spec.Template.Spec
	assert.Nil(t, podSpec.ShareProcessNamespace)
	require.Len(t, podSpec.InitContainers, 1)
	initContainer := podSpec.InitContainers[0]
	assert.Equal(t, InitDatabaseContainerName, initContainer.Name)
	assert.Equal(t, opts.InitAppDBImage, initContainer.Image)
	assert.True(t, containerMountsVolume(initContainer.VolumeMounts, "agent-scripts"))
	assert.Equal(t, -1, getContainerIndexByName(podSpec.Containers, util.AgentContainerUtilitiesName))

	agent := findContainerByName(t, podSpec.Containers, util.AgentContainerName)
	agentCommand := agent.Command[len(agent.Command)-1]
	assert.Contains(t, agentCommand, "-cluster=/var/lib/automation/config/cluster-config.json")
	assert.Contains(t, agentCommand, "-operatorMode")
	assert.Contains(t, agentCommand, "-mongodProxyAddress="+MongodbProxyAddress)
	assert.NotContains(t, agentCommand, "-binariesFixedPath")
	assert.NotContains(t, agentCommand, "MDB_MONGOD_ROOT")
	assert.NotContains(t, agentCommand, "-skipMongoStart")
	assert.NotContains(t, agentCommand, "-mmsBaseUrl")
	assert.NotContains(t, agentCommand, "-mmsGroupId=")
	assert.Contains(t, agent.Env, corev1.EnvVar{Name: mongodbProxyAddressEnv, Value: MongodbProxyAddress})
	assert.NotContains(t, agentCommand, "/usr/local/bin/setup-agent-files.sh")
	require.NotNil(t, agent.ReadinessProbe)
	require.NotNil(t, agent.ReadinessProbe.Exec)
	assert.Equal(t, []string{appdbReadinessProbePath}, agent.ReadinessProbe.Exec.Command)
	assert.True(t, containerMountsVolume(agent.VolumeMounts, "agent-scripts"))

	database := findContainerByName(t, podSpec.Containers, util.MongodbContainerName)
	assert.Equal(t, []string{mongodbProxyBinaryPath}, database.Command)
	assert.Equal(t, []string{"-listen", MongodbProxyAddress}, database.Args)
	assert.True(t, containerMountsVolume(database.VolumeMounts, mongodbProxyVolumeName))
	assert.True(t, containerMountsVolume(database.VolumeMounts, mongodbProxySocketVolumeName))
	assert.True(t, containerMountsVolume(agent.VolumeMounts, mongodbProxySocketVolumeName))

	proxyVolume := findVolumeByName(podSpec.Volumes, mongodbProxyVolumeName)
	require.NotNil(t, proxyVolume)
	require.NotNil(t, proxyVolume.Image)
	assert.Equal(t, opts.MongodbProxyImage, proxyVolume.Image.Reference)

	scriptsVolume := findVolumeByName(podSpec.Volumes, "agent-scripts")
	require.NotNil(t, scriptsVolume)
	assert.NotNil(t, scriptsVolume.EmptyDir)

	assert.True(t, containerMountsVolume(agent.VolumeMounts, "automation-config"))
}

func TestAppDbStatefulSet_LegacyStaticPreservesSharedProcessNamespace(t *testing.T) {
	t.Setenv(util.ImagePullPolicyEnv, string(corev1.PullIfNotPresent))

	om := omv1.NewOpsManagerBuilderDefault().Build()
	sts, err := AppDbStatefulSet(*om, nil, AppDBStatefulSetOptions{},
		scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil),
		appsv1.OnDeleteStatefulSetStrategyType, architectures.Static, zap.S())
	require.NoError(t, err)

	require.NotNil(t, sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.True(t, *sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.NotEqual(t, -1, getContainerIndexByName(sts.Spec.Template.Spec.Containers, util.AgentContainerUtilitiesName))
	agent := findContainerByName(t, sts.Spec.Template.Spec.Containers, util.AgentContainerName)
	assert.Contains(t, agent.Command[len(agent.Command)-1], "-skipMongoStart")
}

func TestAppDbStatefulSet_ImagePullPolicy(t *testing.T) {
	// mongod and the single non-static init container have no explicit policy
	unmanaged := map[string]bool{util.MongodbContainerName: true, InitAppDbContainerName: true}

	for _, arch := range []architectures.DefaultArchitecture{architectures.NonStatic, architectures.Static} {
		t.Run(string(arch), func(t *testing.T) {
			t.Setenv(util.ImagePullPolicyEnv, string(corev1.PullIfNotPresent))

			om := omv1.NewOpsManagerBuilderDefault().Build()
			sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
				AppDBStatefulSetOptions{},
				scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil),
				appsv1.OnDeleteStatefulSetStrategyType, arch, zap.S())
			require.NoError(t, err)

			checked := 0
			for _, c := range append(sts.Spec.Template.Spec.Containers, sts.Spec.Template.Spec.InitContainers...) {
				if unmanaged[c.Name] {
					continue
				}
				assert.Equal(t, corev1.PullIfNotPresent, c.ImagePullPolicy, "container %s", c.Name)
				checked++
			}
			assert.Positive(t, checked)
		})
	}
}

func TestAutomationAgentCommandStaticVsNonStatic(t *testing.T) {
	logLevel := v1.LogLevel("INFO")
	logFile := "/var/log/mongodb-mms-automation/automation-agent.log"
	maxLogFileDurationHours := 24

	t.Run("non-static includes download snippet", func(t *testing.T) {
		cmd := AutomationAgentCommand(false, false, false, logLevel, logFile, maxLogFileDurationHours)
		assert.Equal(t, "/bin/bash", cmd[0])
		assert.Equal(t, "-c", cmd[1])
		assert.Contains(t, cmd[2], "MDB_CUSTOM_AGENT_URL")
		assert.Contains(t, cmd[2], "agent_binary=/tmp/mongodb-agent")
		assert.Contains(t, cmd[2], "${agent_binary:-agent/mongodb-agent}")
	})

	t.Run("static excludes download snippet", func(t *testing.T) {
		cmd := AutomationAgentCommand(true, false, false, logLevel, logFile, maxLogFileDurationHours)
		assert.Equal(t, "/bin/bash", cmd[0])
		assert.Equal(t, "-c", cmd[1])
		assert.NotContains(t, cmd[2], "MDB_CUSTOM_AGENT_URL")
		assert.NotContains(t, cmd[2], "agent_binary=/tmp/mongodb-agent")
		assert.Contains(t, cmd[2], "${agent_binary:-agent/mongodb-agent}")
	})

	t.Run("proxy static uses local Agent config", func(t *testing.T) {
		cmd := AutomationAgentCommand(true, true, false, logLevel, logFile, maxLogFileDurationHours)
		assert.Contains(t, cmd[2], "-cluster="+appdbClusterFilePath)
		assert.Contains(t, cmd[2], "-operatorMode")
		assert.Contains(t, cmd[2], "-mongodProxyAddress="+MongodbProxyAddress)
		assert.NotContains(t, cmd[2], "-binariesFixedPath")
		assert.NotContains(t, cmd[2], "MDB_MONGOD_ROOT")
		assert.NotContains(t, cmd[2], "-skipMongoStart")
		assert.NotContains(t, cmd[2], "-mmsBaseUrl")
	})
}

// TestAppDbStatefulSet_LabelsNotPropagatedToVolumeClaimTemplates asserts that the Ops Manager
// resource labels reach the AppDB StatefulSet metadata but not its volume claim templates, which are
// immutable and would wedge the StatefulSet on every label change (CLOUDP-208587).
func TestAppDbStatefulSet_LabelsNotPropagatedToVolumeClaimTemplates(t *testing.T) {
	labels := map[string]string{"label1": "val1"}

	buildStatefulSet := func(t *testing.T, om *omv1.MongoDBOpsManager) appsv1.StatefulSet {
		om.Labels = labels
		scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)
		sts, err := AppDbStatefulSet(*om, nil, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
		require.NoError(t, err)
		return sts
	}

	assertNoClaimLabels := func(t *testing.T, sts appsv1.StatefulSet, expectedClaims int) {
		assert.Equal(t, "val1", sts.Labels["label1"], "the statefulset metadata is mutable and still gets the labels")
		require.Len(t, sts.Spec.VolumeClaimTemplates, expectedClaims)
		for _, claim := range sts.Spec.VolumeClaimTemplates {
			assert.Empty(t, claim.Labels, "volume claim template %s must not carry the resource labels", claim.Name)
		}
	}

	t.Run("single volume", func(t *testing.T) {
		sts := buildStatefulSet(t, omv1.NewOpsManagerBuilderDefault().Build())
		assertNoClaimLabels(t, sts, 1)
	})

	t.Run("separate data, logs and journal volumes", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().SetAppDbPodSpec(mdbv1.MongoDbPodSpec{
			Persistence: &v1.Persistence{
				MultipleConfig: &v1.MultiplePersistenceConfig{
					Data:    &v1.PersistenceConfig{Storage: "1G"},
					Logs:    &v1.PersistenceConfig{Storage: "1G"},
					Journal: &v1.PersistenceConfig{Storage: "1G"},
				},
			},
		}).Build()
		sts := buildStatefulSet(t, om)
		assertNoClaimLabels(t, sts, 3)
	})
}
