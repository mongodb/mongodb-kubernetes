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
	_ = os.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
}

func Test_buildDatabaseInitContainer(t *testing.T) {
	modification := buildDatabaseInitContainer()
	container := &corev1.Container{}
	modification(container)
	expectedVolumeMounts := []corev1.VolumeMount{{
		Name:      PvcNameDatabaseScripts,
		MountPath: PvcMountPathScripts,
		ReadOnly:  false,
	}}
	expectedContainer := &corev1.Container{
		Name:         initDatabaseContainerName,
		Image:        "quay.io/mongodb/mongodb-enterprise-init-database:latest",
		VolumeMounts: expectedVolumeMounts}
	assert.Equal(t, expectedContainer, container)

}
