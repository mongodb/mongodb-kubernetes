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
	_ = os.Setenv(util.InitOpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-init-opsmanager")
}

func Test_buildOpsManagerandBackupInitContainer(t *testing.T) {
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
		Image:           "quay.io/mongodb/mongodb-enterprise-init-opsmanager:latest",
		VolumeMounts:    expectedVolumeMounts,
		SecurityContext: &expectedSecurityContext,
	}
	assert.Equal(t, expectedContainer, container)

}
