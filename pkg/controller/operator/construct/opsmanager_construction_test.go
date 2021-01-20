package construct

import (
	"os"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func Test_buildOpsManagerandBackupInitContainer(t *testing.T) {
	_ = os.Setenv(util.InitOpsManagerImageUrl, "test-registry")

	modification := buildOpsManagerAndBackupInitContainer()
	container := &corev1.Container{}
	modification(container)
	expectedVolumeMounts := []corev1.VolumeMount{{
		Name:      "ops-manager-scripts",
		MountPath: "/opt/scripts",
		ReadOnly:  false,
	}}
	expectedSecurityContext := defaultSecurityContext()
	expectedContainer := &corev1.Container{
		Name:            util.InitOpsManagerContainerName,
		Image:           "test-registry:latest",
		VolumeMounts:    expectedVolumeMounts,
		SecurityContext: &expectedSecurityContext,
	}
	assert.Equal(t, expectedContainer, container)

}
