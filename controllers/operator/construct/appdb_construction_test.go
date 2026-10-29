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

func TestAppDBAgentSettings(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.AutomationAgent.StartupParameters = agentStartupParameters
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	assert.NoError(t, err)

	cm, err := AppDBAgentSettingsConfigMap(sts, om.Spec.AppDB, &env.PodEnvVars{ProjectID: "abcd"})
	require.NoError(t, err)
	assert.Contains(t, cm.Data[AgentSettingsFileName], "Key1=Value1\n")
	assert.Contains(t, cm.Data[AgentSettingsFileName], "Key2=Value2\n")
}

func TestAppDBAgentSettingsPreservesCustomAPIKeyExec(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.AutomationAgent.StartupParameters = mdbv1.StartupParameters{
		"mmsApiKeyExec": "cat '$CUSTOM_KEY_PATH/agent-key'",
	}
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	require.NoError(t, err)

	cm, err := AppDBAgentSettingsConfigMap(sts, om.Spec.AppDB, &env.PodEnvVars{ProjectID: "abcd"})
	require.NoError(t, err)
	assert.Contains(t, cm.Data[AgentSettingsFileName], "mmsApiKeyExec=cat '$CUSTOM_KEY_PATH/agent-key'\n")
}

func TestAppDBAgentSettingsRejectsCustomAgentFlags(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	require.NoError(t, err)
	agent := AgentContainer(sts)
	require.NotNil(t, agent, "AppDB StatefulSet must expose an agent container")
	agent.Env = append(agent.Env, corev1.EnvVar{Name: "AGENT_FLAGS", Value: "-logLevel=DEBUG,"})

	_, err = AppDBAgentSettingsConfigMap(sts, om.Spec.AppDB, &env.PodEnvVars{ProjectID: "abcd"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent.startupOptions")
}

func TestAppDBAgentSettingsRejectsRuntimeAgentSettingsEnv(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
	require.NoError(t, err)
	agent := AgentContainer(sts)
	require.NotNil(t, agent, "AppDB StatefulSet must expose an agent container")
	agent.Env = append(agent.Env, corev1.EnvVar{Name: util.EnvVarBaseUrl, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "runtime"}, Key: "url"},
	}})

	_, err = AppDBAgentSettingsConfigMap(sts, om.Spec.AppDB, &env.PodEnvVars{ProjectID: "abcd"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent.startupOptions")
}

// TestAppDBAgentSettingsCredentialPrecedence pins which credential source wins: monitoring
// defaults, an empty exec command, a raw mmsApiKey override, and a custom exec command.
func TestAppDBAgentSettingsCredentialPrecedence(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	for _, tt := range []struct {
		name       string
		projectID  string
		parameters mdbv1.StartupParameters
		command    string
	}{
		{name: "monitoring", projectID: "project", command: "cat '/mongodb-automation/agent-api-key/agentApiKey'"},
		{name: "empty command retains monitoring credential", projectID: "project", parameters: mdbv1.StartupParameters{"mmsApiKeyExec": ""}, command: "cat '/mongodb-automation/agent-api-key/agentApiKey'"},
		{name: "raw key without monitoring", parameters: mdbv1.StartupParameters{"mmsApiKey": "raw-key"}, command: "cat '/mongodb-automation/agent-api-key/agentApiKey'"},
		{name: "custom command wins over raw key", projectID: "project", parameters: mdbv1.StartupParameters{"mmsApiKey": "raw-key", "mmsApiKeyExec": "read-customer-key"}, command: "read-customer-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			om := omv1.NewOpsManagerBuilderDefault().Build()
			om.Spec.AppDB.AutomationAgent.StartupParameters = tt.parameters
			cm, err := AppDBAgentSettingsConfigMap(appsv1.StatefulSet{}, om.Spec.AppDB, &env.PodEnvVars{ProjectID: tt.projectID})
			require.NoError(t, err)
			settings := cm.Data[AgentSettingsFileName]
			assert.Contains(t, settings, "mmsApiKeyExec="+tt.command+"\n")
			assert.NotContains(t, settings, "raw-key")
			assert.NotContains(t, settings, "mmsApiKey=")
			assert.Contains(t, settings, "logFile="+om.Spec.AppDB.GetAgentLogFile()+"\n")
			assert.Contains(t, settings, "maxLogFileDurationHrs=24\n")
			assert.Equal(t, tt.parameters, om.Spec.AppDB.AutomationAgent.StartupParameters)
		})
	}
}

func TestAppDBAgentHostnameArguments(t *testing.T) {
	for _, tt := range []struct {
		name           string
		multiCluster   bool
		externalDomain *string
		parameters     mdbv1.StartupParameters
		hostname       string
	}{
		{name: "native single cluster"},
		{name: "external domain", externalDomain: new("external.example.com"), hostname: "$(POD_NAME).external.example.com"},
		{name: "multi-cluster custom domain", multiCluster: true, hostname: "$(POD_NAME)-svc.$(POD_NAMESPACE).svc.custom.local"},
		{name: "per-cluster external domain", multiCluster: true, externalDomain: new("member.example.com"), hostname: "$(POD_NAME).member.example.com"},
		{name: "generated hostname wins over customer", externalDomain: new("external.example.com"), parameters: mdbv1.StartupParameters{"overrideLocalHost": "customer.example.com"}, hostname: "$(POD_NAME).external.example.com"},
		{name: "customer hostname without generated identity", parameters: mdbv1.StartupParameters{"overrideLocalHost": "customer.example.com"}, hostname: "customer.example.com"},
		{name: "explicit empty hostname", parameters: mdbv1.StartupParameters{"overrideLocalHost": ""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			om := omv1.NewOpsManagerBuilderDefault().Build()
			om.Spec.AppDB.ClusterDomain = "custom.local"
			om.Spec.AppDB.AutomationAgent.StartupParameters = tt.parameters
			clusterName := multicluster.LegacyCentralClusterName
			if tt.multiCluster {
				clusterName = "member"
				om.Spec.AppDB.Topology = omv1.ClusterTopologyMultiCluster
				om.Spec.AppDB.ClusterSpecList = mdbv1.ClusterSpecList{{ClusterName: clusterName, Members: 1, ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: tt.externalDomain}}}
			} else if tt.externalDomain != nil {
				om.Spec.AppDB.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: tt.externalDomain}
			}
			sts, err := AppDbStatefulSet(*om, nil, AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, clusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
			require.NoError(t, err)
			agent := findContainerByName(t, sts.Spec.Template.Spec.Containers, util.AgentContainerName)
			args := []string{"-f", "/etc/mongodb-agent/automation-agent.conf"}
			if _, explicit := tt.parameters["overrideLocalHost"]; explicit || tt.hostname != "" {
				args = append(args, "-overrideLocalHost="+tt.hostname)
			}
			assert.Equal(t, args, agent.Args)
			assert.Contains(t, agent.Env, corev1.EnvVar{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}})
			cm, err := AppDBAgentSettingsConfigMap(sts, om.Spec.AppDB, nil)
			require.NoError(t, err)
			assert.NotContains(t, cm.Data[AgentSettingsFileName], "overrideLocalHost")
		})
	}
}

func TestAppDBAgentSettingsExpandsLiteralEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      corev1.EnvVar
		expected string
	}{
		{name: "literal value", env: corev1.EnvVar{Name: "LOG_ROOT", Value: "/var/log/custom"}, expected: "logFile=/var/log/custom/agent.log\n"},
		{name: "runtime value stays literal", env: corev1.EnvVar{Name: "LOG_ROOT", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "runtime"}, Key: "root"}}}, expected: "logFile=$LOG_ROOT/agent.log\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			om := omv1.NewOpsManagerBuilderDefault().Build()
			om.Spec.AppDB.AutomationAgent.StartupParameters = mdbv1.StartupParameters{"logFile": "$LOG_ROOT/agent.log"}
			sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
				AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, nil)
			require.NoError(t, err)
			agent := AgentContainer(sts)
			require.NotNil(t, agent)
			agent.Env = append(agent.Env, tc.env)

			cm, err := AppDBAgentSettingsConfigMap(sts, om.Spec.AppDB, &env.PodEnvVars{ProjectID: "abcd"})
			require.NoError(t, err)
			assert.Contains(t, cm.Data[AgentSettingsFileName], tc.expected)
		})
	}
}

func TestAppDBMonitoringGroupIDWinsOverCustomer(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.AutomationAgent.StartupParameters = mdbv1.StartupParameters{"mmsGroupId": "customer-group"}
	cm, err := AppDBAgentSettingsConfigMap(appsv1.StatefulSet{}, om.Spec.AppDB, &env.PodEnvVars{ProjectID: "operator-group"})
	require.NoError(t, err)
	assert.Contains(t, cm.Data[AgentSettingsFileName], "mmsGroupId=operator-group\n")
	assert.NotContains(t, cm.Data[AgentSettingsFileName], "customer-group")
}

func TestAppDBEmptyBooleanKeepsLegacyTrue(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.AutomationAgent.StartupParameters = mdbv1.StartupParameters{"logAppend": ""}
	cm, err := AppDBAgentSettingsConfigMap(appsv1.StatefulSet{}, om.Spec.AppDB, nil)
	require.NoError(t, err)
	assert.Contains(t, cm.Data[AgentSettingsFileName], "logAppend=true\n")
	assert.NotContains(t, cm.Data[AgentSettingsFileName], "logAppend=\n")
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

func TestAppDbStatefulSet_MonitoringCredentialsInSettings(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	om := omv1.NewOpsManagerBuilderDefault().Build()
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)
	podVars := &env.PodEnvVars{ProjectID: "proj-123", AgentAPIKey: "key"}

	sts, err := AppDbStatefulSet(*om, podVars, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)

	agent := findContainerByName(t, sts.Spec.Template.Spec.Containers, util.AgentContainerName)
	cm, err := AppDBAgentSettingsConfigMap(sts, om.Spec.AppDB, podVars)
	require.NoError(t, err)
	assert.Contains(t, cm.Data[AgentSettingsFileName], "mmsGroupId=proj-123\n")
	assert.Contains(t, cm.Data[AgentSettingsFileName], "mmsApiKeyExec=cat '/mongodb-automation/agent-api-key/agentApiKey'\n")
	assert.NotContains(t, cm.Data[AgentSettingsFileName], "mmsApiKey=")
	assert.NotContains(t, agent.Command[2], "AGENT_API_KEY")
	assert.Equal(t, []string{"-f", "/etc/mongodb-agent/automation-agent.conf"}, agent.Args)

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
	command := agent.Command[2]
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
	t.Run("non-static includes download snippet", func(t *testing.T) {
		cmd := AutomationAgentCommand(false)
		assert.Equal(t, "/bin/bash", cmd[0])
		assert.Equal(t, "-c", cmd[1])
		assert.Contains(t, cmd[2], "MDB_CUSTOM_AGENT_URL")
		assert.Contains(t, cmd[2], "agent_binary=/tmp/mongodb-agent")
		assert.Contains(t, cmd[2], "${agent_binary:-agent/mongodb-agent}")
		assert.Contains(t, cmd[2], `"$@"`)
		assert.Equal(t, "mongodb-agent", cmd[3])
	})

	t.Run("static excludes download snippet", func(t *testing.T) {
		cmd := AutomationAgentCommand(true)
		assert.Equal(t, "/bin/bash", cmd[0])
		assert.Equal(t, "-c", cmd[1])
		assert.NotContains(t, cmd[2], "MDB_CUSTOM_AGENT_URL")
		assert.NotContains(t, cmd[2], "agent_binary=/tmp/mongodb-agent")
		assert.Contains(t, cmd[2], "${agent_binary:-agent/mongodb-agent}")
		assert.Contains(t, cmd[2], "/usr/local/bin/setup-agent-files.sh")
		assert.Contains(t, cmd[2], `"$@"`)
		assert.Equal(t, "mongodb-agent", cmd[3])
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
