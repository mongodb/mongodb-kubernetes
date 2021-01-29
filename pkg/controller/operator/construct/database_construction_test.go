package construct

import (
	"os"
	"path"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
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
	expectedSecurityContext := defaultSecurityContext()
	expectedContainer := &corev1.Container{
		Name:            initDatabaseContainerName,
		Image:           "quay.io/mongodb/mongodb-enterprise-init-database:latest",
		VolumeMounts:    expectedVolumeMounts,
		SecurityContext: &expectedSecurityContext,
	}
	assert.Equal(t, expectedContainer, container)

}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	t.Run("Empty Agent Image", func(t *testing.T) {
		defer mock.InitDefaultEnvVariables()
		os.Setenv(util.AutomationAgentImage, "")
		rs := mdbv1.NewReplicaSetBuilder().Build()
		assert.Panics(t, func() {
			DatabaseStatefulSet(*rs, ReplicaSetOptions())
		})
	})

	t.Run("Empty Image Pull Policy", func(t *testing.T) {
		defer mock.InitDefaultEnvVariables()
		os.Setenv(util.AutomationAgentImagePullPolicy, "")
		sc := mdbv1.NewClusterBuilder().Build()
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ShardOptions(0))
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ConfigServerOptions())
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, MongosOptions())
		})
	})
}

func TestStatefulsetCreationSuccessful(t *testing.T) {
	start := time.Now()
	rs := mdbv1.NewReplicaSetBuilder().Build()

	_ = DatabaseStatefulSet(*rs, ReplicaSetOptions())
	assert.True(t, time.Now().Sub(start) < time.Second*4) // we waited only a little (considering 2 seconds of wait as well)
}

func TestDatabaseEnvVars(t *testing.T) {
	envVars := defaultPodVars()
	opts := DatabaseStatefulSetOptions{PodVars: envVars}
	podEnv := databaseEnvVars(opts)
	assert.Len(t, podEnv, 5)

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	opts = DatabaseStatefulSetOptions{PodVars: envVars}

	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})

	envVars = defaultPodVars()
	envVars.SSLMMSCAConfigMap = "custom-ca"
	opts = DatabaseStatefulSetOptions{PodVars: envVars}
	trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	envVars.SSLMMSCAConfigMap = "custom-ca"
	opts = DatabaseStatefulSetOptions{PodVars: envVars}
	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 7)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})
	assert.Equal(t, podEnv[6], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})
}

func TestAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}

	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{StartupParameters: agentStartupParameters}).Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions())
	variablesMap := env.ToMap(sts.Spec.Template.Spec.Containers[0].Env...)
	val, ok := variablesMap["AGENT_FLAGS"]
	assert.True(t, ok)
	assert.Contains(t, val, "-Key1,Value1", "-Key2,Value2")

}
