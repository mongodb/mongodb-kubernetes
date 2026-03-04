package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	communityConstruct "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
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
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
	assert.NoError(t, err)

	command := sts.Spec.Template.Spec.Containers[0].Command
	assert.Contains(t, command[len(command)-1], "-Key1=Value1", "-Key2=Value2")
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

	om.Spec.AppDB.PodSpec.PodTemplateWrapper = common.PodTemplateSpecWrapper{
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
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, "central", 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
	assert.NoError(t, err)

	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == "mongodb-agent" {
			assert.Equal(t, agentResourceRequirements, c.Resources)
		}
	}
}

func TestAppdbContainerEnv_HeadlessMode(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
	require.NoError(t, err)

	agentContainer := findContainer(t, sts, communityConstruct.AgentName)
	assertEnvVarPresent(t, agentContainer.Env, headlessAgentEnv, "true")
	assertEnvVarPresent(t, agentContainer.Env, automationConfigMapEnv, om.Name+"-db-config")
	assertEnvVarAbsent(t, agentContainer.Env, metaOMServerEnv)
}

func TestAppdbContainerEnv_MetaOMMode(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	opts := AppDBStatefulSetOptions{
		MetaOM: MetaOMEnvVars{
			Enabled: true,
			Server:  "http://om-meta-svc.meta-ns.svc.cluster.local:8080",
			GroupID: "aabbccdd112233445566",
			APIKey:  "secret-agent-key",
		},
	}
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
		opts, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
	require.NoError(t, err)

	agentContainer := findContainer(t, sts, communityConstruct.AgentName)
	assertEnvVarPresent(t, agentContainer.Env, metaOMServerEnv, opts.MetaOM.Server)
	// mmsGroupId and mmsApiKey are passed as explicit command params, not env vars
	assertEnvVarAbsent(t, agentContainer.Env, "MMS_GROUP_ID")
	assertEnvVarAbsent(t, agentContainer.Env, "MMS_API_KEY")
	assertEnvVarAbsent(t, agentContainer.Env, headlessAgentEnv)
	assertEnvVarAbsent(t, agentContainer.Env, automationConfigMapEnv)
}

func TestAppdbContainerEnv_MetaOMDisabled_FallsBackToHeadless(t *testing.T) {
	partialConfigs := []AppDBStatefulSetOptions{
		{MetaOM: MetaOMEnvVars{Server: "http://om:8080"}},
		{MetaOM: MetaOMEnvVars{Server: "http://om:8080", GroupID: "gid"}},
		{MetaOM: MetaOMEnvVars{GroupID: "gid", APIKey: "key"}},
	}
	for _, opts := range partialConfigs {
		om := omv1.NewOpsManagerBuilderDefault().Build()
		sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
			opts, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
		require.NoError(t, err)

		agentContainer := findContainer(t, sts, communityConstruct.AgentName)
		assertEnvVarPresent(t, agentContainer.Env, headlessAgentEnv, "true")
		assertEnvVarAbsent(t, agentContainer.Env, metaOMServerEnv)
	}
}

func TestAppdbContainerEnv_MetaOMEnabledWithEmptyFields_GoesToOnlineMode(t *testing.T) {
	// When Enabled is true the construction functions enter online mode regardless of
	// whether individual fields are empty. Field validation is the reconciler's responsibility:
	// it only sets Enabled=true after successfully resolving all MetaOM credentials.
	configs := []AppDBStatefulSetOptions{
		{MetaOM: MetaOMEnvVars{Enabled: true}},
		{MetaOM: MetaOMEnvVars{Enabled: true, Server: "http://om:8080"}},
		{MetaOM: MetaOMEnvVars{Enabled: true, Server: "http://om:8080", GroupID: "gid"}},
		{MetaOM: MetaOMEnvVars{Enabled: true, GroupID: "gid", APIKey: "key"}},
	}
	for _, opts := range configs {
		om := omv1.NewOpsManagerBuilderDefault().Build()
		sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
			opts, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
		require.NoError(t, err)

		agentContainer := findContainer(t, sts, communityConstruct.AgentName)
		assertEnvVarAbsent(t, agentContainer.Env, headlessAgentEnv)
		assertEnvVarAbsent(t, agentContainer.Env, automationConfigMapEnv)
	}
}

func findContainer(t *testing.T, sts v1.StatefulSet, name string) corev1.Container {
	t.Helper()
	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("container %q not found in StatefulSet", name)
	return corev1.Container{}
}

func assertEnvVarPresent(t *testing.T, envVars []corev1.EnvVar, name, value string) {
	t.Helper()
	for _, e := range envVars {
		if e.Name == name {
			assert.Equal(t, value, e.Value, "env var %q has unexpected value", name)
			return
		}
	}
	t.Errorf("env var %q not found", name)
}

func assertEnvVarAbsent(t *testing.T, envVars []corev1.EnvVar, name string) {
	t.Helper()
	for _, e := range envVars {
		if e.Name == name {
			t.Errorf("env var %q should not be present but was found with value %q", name, e.Value)
		}
	}
}
