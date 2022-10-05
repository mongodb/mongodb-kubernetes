package construct

import (
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

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
	expectedContainer := &corev1.Container{
		Name:         InitDatabaseContainerName,
		Image:        "quay.io/mongodb/mongodb-enterprise-init-database:latest",
		VolumeMounts: expectedVolumeMounts,
	}
	assert.Equal(t, expectedContainer, container)

}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	t.Run("Empty Agent Image", func(t *testing.T) {
		defer mock.InitDefaultEnvVariables()
		os.Setenv(util.AutomationAgentImage, "")
		rs := mdbv1.NewReplicaSetBuilder().Build()
		assert.Panics(t, func() {
			DatabaseStatefulSet(*rs, ReplicaSetOptions(GetpodEnvOptions()))
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

	_ = DatabaseStatefulSet(*rs, ReplicaSetOptions(GetpodEnvOptions()))
	assert.True(t, time.Since(start) < time.Second*4) // we waited only a little (considering 2 seconds of wait as well)
}

func TestDatabaseEnvVars(t *testing.T) {
	envVars := defaultPodVars()
	opts := DatabaseStatefulSetOptions{PodVars: envVars}
	podEnv := databaseEnvVars(opts)
	assert.Len(t, podEnv, 4)

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	opts = DatabaseStatefulSetOptions{PodVars: envVars}

	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 5)
	assert.Equal(t, podEnv[4], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})

	envVars = defaultPodVars()
	envVars.SSLMMSCAConfigMap = "custom-ca"
	v := &caVolumeSource{}
	extraEnvs := v.GetEnvs()

	opts = DatabaseStatefulSetOptions{PodVars: envVars, ExtraEnvs: extraEnvs}
	trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 5)
	assert.Equal(t, podEnv[4], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	envVars.SSLMMSCAConfigMap = "custom-ca"
	opts = DatabaseStatefulSetOptions{PodVars: envVars, ExtraEnvs: extraEnvs}
	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})
	assert.Equal(t, podEnv[4], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})

}

func TestAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}

	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{StartupParameters: agentStartupParameters}).Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetpodEnvOptions()))
	variablesMap := env.ToMap(sts.Spec.Template.Spec.Containers[0].Env...)
	val, ok := variablesMap["AGENT_FLAGS"]
	assert.True(t, ok)
	assert.Contains(t, val, "-Key1,Value1", "-Key2,Value2")

}

func TestLabelsAndAnotations(t *testing.T) {
	labels := map[string]string{"l1": "val1", "l2": "val2"}
	annotations := map[string]string{"a1": "val1", "a2": "val2"}

	mdb := mdbv1.NewReplicaSetBuilder().SetAnnotations(annotations).SetLabels(labels).Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetpodEnvOptions()))

	// add the default label to the map
	labels["app"] = "test-mdb-svc"
	assert.Equal(t, labels, sts.Labels)
}

func TestReplaceImageTagOrDigestToTag(t *testing.T) {
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:9876-54321", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-agent:1234-567", "9876-54321"))
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:9876-54321", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-agent@sha256:6a82abae27c1ba1133f3eefaad71ea318f8fa87cc57fe9355d6b5b817ff97f1a", "9876-54321"))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:some-tag", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-enterprise-database:45678", "some-tag"))
}

func TestContainerImage(t *testing.T) {
	initDatabaseRelatedImageEnv1 := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", InitDatabaseVersionEnv)
	initDatabaseRelatedImageEnv2 := fmt.Sprintf("RELATED_IMAGE_%s_12_0_4_7554_1", InitDatabaseVersionEnv)
	initDatabaseRelatedImageEnv3 := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0_b20220912000000", InitDatabaseVersionEnv)

	defer env.RevertEnvVariables(initDatabaseRelatedImageEnv1, initDatabaseRelatedImageEnv2, initDatabaseRelatedImageEnv3, InitDatabaseVersionEnv, util.InitAppdbImageUrlEnv)()

	_ = os.Setenv(InitDatabaseVersionEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	_ = os.Setenv(initDatabaseRelatedImageEnv1, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd")
	_ = os.Setenv(initDatabaseRelatedImageEnv2, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:b631ee886bb49ba8d7b90bb003fe66051dadecbc2ac126ac7351221f4a7c377c")
	_ = os.Setenv(initDatabaseRelatedImageEnv3, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:f1a7f49cd6533d8ca9425f25cdc290d46bb883997f07fac83b66cc799313adad")

	// there is no related image for 0.0.1
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:0.0.1", ContainerImage(InitDatabaseVersionEnv, "0.0.1"))
	// for 10.2.25.6008-1 there is no RELATED_IMAGE variable set, so we use version instead of digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:10.2.25.6008-1", ContainerImage(InitDatabaseVersionEnv, "10.2.25.6008-1"))
	// for following versions we set RELATED_IMAGE_MONGODB_IMAGE_* env variables to sha256 digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd", ContainerImage(InitDatabaseVersionEnv, "1.0.0"))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:b631ee886bb49ba8d7b90bb003fe66051dadecbc2ac126ac7351221f4a7c377c", ContainerImage(InitDatabaseVersionEnv, "12.0.4.7554-1"))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:f1a7f49cd6533d8ca9425f25cdc290d46bb883997f07fac83b66cc799313adad", ContainerImage(InitDatabaseVersionEnv, "2.0.0-b20220912000000"))

	// env var has version already, so it is replaced
	_ = os.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb:12.0.4.7554-1")
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb:10.2.25.6008-1", ContainerImage(util.InitAppdbImageUrlEnv, "10.2.25.6008-1"))

	// env var has version already, but there is related image with this version
	_ = os.Setenv(fmt.Sprintf("RELATED_IMAGE_%s_12_0_4_7554_1", util.InitAppdbImageUrlEnv), "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c")
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c", ContainerImage(util.InitAppdbImageUrlEnv, "12.0.4.7554-1"))

	_ = os.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd")
	// env var has version already as digest, but there is related image with this version
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c", ContainerImage(util.InitAppdbImageUrlEnv, "12.0.4.7554-1"))
	// env var has version already as digest, there is no related image with this version, so we use version instead of digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb:1.2.3", ContainerImage(util.InitAppdbImageUrlEnv, "1.2.3"))
}

func Test_DatabaseStatefulSetWithRelatedImages(t *testing.T) {
	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.AutomationAgentImage)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0", util.InitDatabaseImageUrlEnv)

	defer env.RevertEnvVariables(databaseRelatedImageEnv, initDatabaseRelatedImageEnv, util.AutomationAgentImage, DatabaseVersionEnv, util.InitDatabaseImageUrlEnv, InitDatabaseVersionEnv)()

	_ = os.Setenv(util.AutomationAgentImage, "quay.io/mongodb/mongodb-enterprise-database")
	_ = os.Setenv(DatabaseVersionEnv, "1.0.0")
	_ = os.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	_ = os.Setenv(InitDatabaseVersionEnv, "2.0.0")
	_ = os.Setenv(databaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE")
	_ = os.Setenv(initDatabaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE")

	rs := mdbv1.NewReplicaSetBuilder().Build()
	sts := DatabaseStatefulSet(*rs, ReplicaSetOptions(GetpodEnvOptions()))

	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
}
