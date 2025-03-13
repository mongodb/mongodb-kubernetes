package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
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
