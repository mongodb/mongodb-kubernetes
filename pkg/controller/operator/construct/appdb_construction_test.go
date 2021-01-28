package construct

import (
	"os"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	_ = os.Setenv(util.InitAppdbImageUrl, "quay.io/mongodb/mongodb-enterprise-init-appdb")
}

func Test_buildAppdbInitContainer(t *testing.T) {
	modification := buildAppdbInitContainer()
	container := &corev1.Container{}
	modification(container)
	expectedVolumeMounts := []corev1.VolumeMount{{
		Name:      "appdb-scripts",
		MountPath: "/opt/scripts",
		ReadOnly:  false,
	}}
	expectedSecurityContext := defaultSecurityContext()
	expectedContainer := &corev1.Container{
		Name:            InitAppDbContainerName,
		Image:           "quay.io/mongodb/mongodb-enterprise-init-appdb:latest",
		VolumeMounts:    expectedVolumeMounts,
		SecurityContext: &expectedSecurityContext,
	}
	assert.Equal(t, expectedContainer, container)

}

func TestAppDBAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}

	om := defaultOpsManagerBuilder().Build()
	om.Spec.AppDB.MongoDbSpec.Agent.StartupParameters = agentStartupParameters
	sts := AppDbStatefulSet(om)

	variablesMap := env.ToMap(sts.Spec.Template.Spec.Containers[0].Env...)
	val, ok := variablesMap["AGENT_FLAGS"]
	assert.True(t, ok)
	assert.Contains(t, val, "-Key1,Value1", "-Key2,Value2")
}

// TODO: remove - use omv1.DefaultOpsManagerBuilder() instead
func defaultOpsManagerBuilder() *omv1.OpsManagerBuilder {
	spec := omv1.MongoDBOpsManagerSpec{
		Version:     "4.2.0",
		AppDB:       *omv1.DefaultAppDbBuilder().Build(),
		AdminSecret: "om-admin",
	}
	resource := omv1.MongoDBOpsManager{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "test-om", Namespace: mock.TestNamespace}}
	return omv1.NewOpsManagerBuilderFromResource(resource)
}
