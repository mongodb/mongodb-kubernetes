package construct

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	_ = os.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb")
	_ = os.Setenv(util.OpsManagerMonitorAppDB, "false")
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

	om.Spec.AppDB.PodSpec.PodTemplateWrapper = mdbv1.PodTemplateSpecWrapper{
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

func TestAppDbStatefulSetWithRelatedImages(t *testing.T) {
	agentRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_10_26_0_6851_1", construct.AgentImageEnv)
	mongodbRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_2_3_ubi8", construct.MongodbImageEnv)
	initAppdbRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_3_4_5", util.InitAppdbImageUrlEnv)

	om := omv1.NewOpsManagerBuilderDefault().Build()

	t.Setenv(construct.MongodbImageEnv, "mongodb-enterprise-appdb-database-ubi")
	t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
	t.Setenv(construct.AgentImageEnv, "quay.io/mongodb/mongodb-agent:10.26.0.6851-1")
	t.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb")
	t.Setenv(initAppdbVersionEnv, "3.4.5")

	// without related imaged sts is configured using env vars
	om.Spec.AppDB.Version = "1.2.3-ent"
	sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"}, AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
	assert.NoError(t, err)
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:10.26.0.6851-1", sts.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:1.2.3-ent", sts.Spec.Template.Spec.Containers[1].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb:3.4.5", sts.Spec.Template.Spec.InitContainers[0].Image)

	// sts should be configured with related images when they are defined
	t.Setenv(agentRelatedImageEnv, "quay.io/mongodb/mongodb-agent@sha256:AGENT_SHA")
	t.Setenv(mongodbRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi@sha256:MONGODB_SHA")
	t.Setenv(initAppdbRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:INIT_APPDB_SHA")

	om.Spec.AppDB.Version = "1.2.3-ent"
	sts, err = AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"}, AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), v1.OnDeleteStatefulSetStrategyType, nil)
	assert.NoError(t, err)
	// agent's image is not used from RELATED_IMAGE because its value is from AGENT_IMAGE which is full image version
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:10.26.0.6851-1", sts.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:1.2.3-ent", sts.Spec.Template.Spec.Containers[1].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:INIT_APPDB_SHA", sts.Spec.Template.Spec.InitContainers[0].Image)
}
