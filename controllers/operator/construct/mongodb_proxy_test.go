package construct

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

const (
	testProxyImage   = "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy:1.0.0"
	testMongodbImage = "quay.io/mongodb/mongodb-enterprise-server:8.0.0-ubi9"
	testInitImage    = "quay.io/mongodb/mongodb-kubernetes-init-database:1.0.0"
)

func proxyTestStatefulSet(
	annotations map[string]string,
	arch architectures.DefaultArchitecture,
	proxyImage, mongodbImage, initDatabaseImage, customAgentURL string,
) appsv1.StatefulSet {
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	mdb.Annotations = annotations
	return DatabaseStatefulSet(*mdb, ReplicaSetOptions(
		GetPodEnvOptions(),
		WithDefaultArchitecture(arch),
		func(o *DatabaseStatefulSetOptions) {
			o.MongodbProxyImage = proxyImage
			o.MongodbImage = mongodbImage
			o.InitDatabaseImage = initDatabaseImage
			o.CustomAgentURL = customAgentURL
		},
	), zap.S())
}

func containerByName(t *testing.T, sts appsv1.StatefulSet, name string) corev1.Container {
	t.Helper()
	i := slices.IndexFunc(sts.Spec.Template.Spec.Containers, func(c corev1.Container) bool { return c.Name == name })
	require.NotEqual(t, -1, i, "container %s", name)
	return sts.Spec.Template.Spec.Containers[i]
}

func volumeByName(sts appsv1.StatefulSet, name string) (corev1.Volume, bool) {
	i := slices.IndexFunc(sts.Spec.Template.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == name })
	if i == -1 {
		return corev1.Volume{}, false
	}
	return sts.Spec.Template.Spec.Volumes[i], true
}

func TestMongodbProxyRunsMongodThroughTheProxy(t *testing.T) {
	sts := proxyTestStatefulSet(
		nil,
		architectures.StaticGA,
		testProxyImage,
		testMongodbImage,
		testInitImage,
		"https://example.com/mongodb-mms-automation-agent-1000.2.0.11583-1.rhel8_x86_64.tar.gz",
	)

	t.Run("the pods do not share a process namespace", func(t *testing.T) {
		assert.Nil(t, sts.Spec.Template.Spec.ShareProcessNamespace)
	})

	t.Run("the database container runs the proxy from the image volume", func(t *testing.T) {
		mongod := containerByName(t, sts, util.DatabaseContainerName)
		assert.Equal(t, []string{"/mnt/proxy/proxy"}, mongod.Command)
		assert.Contains(t, mongod.VolumeMounts, corev1.VolumeMount{Name: "mongodb-proxy", MountPath: "/mnt/proxy", ReadOnly: true})

		volume, found := volumeByName(sts, "mongodb-proxy")
		require.True(t, found)
		require.NotNil(t, volume.Image)
		assert.Equal(t, testProxyImage, volume.Image.Reference)
	})

	t.Run("the agent container runs the launcher and probes from the init-database image", func(t *testing.T) {
		agent := containerByName(t, sts, util.AgentContainerName)
		assert.Contains(t, agent.Env, corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "static-ga"})
		assert.Contains(t, agent.Env, corev1.EnvVar{Name: "MDB_MONGODB_PROXY_ADDRESS", Value: "127.0.0.1:8099"})
		assert.Contains(t, agent.Env, corev1.EnvVar{
			Name: "AGENT_FLAGS",
			Value: "-binariesFixedPath=/tools,-logFile=/var/log/mongodb-mms-automation/automation-agent.log," +
				"-mongodProxyAddress=127.0.0.1:8099,",
		})
		assert.Equal(t, []string{"/mnt/init-database/scripts/agent-launcher.sh"}, agent.Command)
		assert.Contains(t, agent.VolumeMounts, corev1.VolumeMount{
			Name:      "mongodb-init-database",
			MountPath: "/mnt/init-database",
			ReadOnly:  true,
		})
		require.NotNil(t, agent.LivenessProbe)
		assert.Equal(t, []string{"/mnt/init-database/probes/probe.sh"}, agent.LivenessProbe.Exec.Command)
		require.NotNil(t, agent.StartupProbe)
		assert.Equal(t, []string{"/mnt/init-database/probes/probe.sh"}, agent.StartupProbe.Exec.Command)
		require.NotNil(t, agent.ReadinessProbe)
		assert.Equal(t, []string{"/mnt/init-database/probes/readinessprobe"}, agent.ReadinessProbe.Exec.Command)

		initVolume, hasInitVolume := volumeByName(sts, "mongodb-init-database")
		require.True(t, hasInitVolume)
		require.NotNil(t, initVolume.Image)
		assert.Equal(t, testInitImage, initVolume.Image.Reference)

		_, hasBinariesVolume := volumeByName(sts, "mongodb-binaries")
		assert.False(t, hasBinariesVolume)
		_, hasScriptsVolume := volumeByName(sts, "database-scripts")
		assert.False(t, hasScriptsVolume)

		for _, c := range sts.Spec.Template.Spec.InitContainers {
			assert.NotEqual(t, InitDatabaseContainerName, c.Name)
		}
	})

	t.Run("the utilities container is not part of the pod", func(t *testing.T) {
		for _, c := range sts.Spec.Template.Spec.Containers {
			assert.NotEqual(t, util.AgentContainerUtilitiesName, c.Name)
		}

		assert.Len(t, sts.Spec.Template.Spec.Containers, 2)
	})

	t.Run("the proxy image is not mounted into the agent container", func(t *testing.T) {
		for _, m := range containerByName(t, sts, util.AgentContainerName).VolumeMounts {
			assert.NotEqual(t, "mongodb-proxy", m.Name)
		}
	})
}

func TestMongodbProxyIsAutomaticForStaticGAOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		annotations map[string]string
		arch        architectures.DefaultArchitecture
		wantProxy   bool
	}{
		"static-ga default": {
			annotations: nil,
			arch:        architectures.StaticGA,
			wantProxy:   true,
		},
		"legacy static remains unchanged": {
			annotations: nil,
			arch:        architectures.Static,
			wantProxy:   false,
		},
		"non-static remains unchanged": {
			annotations: nil,
			arch:        architectures.NonStatic,
			wantProxy:   false,
		},
		"resource annotation opts out": {
			annotations: map[string]string{architectures.ArchitectureAnnotation: string(architectures.NonStatic)},
			arch:        architectures.StaticGA,
			wantProxy:   false,
		},
		"legacy static annotation overrides static-ga default": {
			annotations: map[string]string{architectures.ArchitectureAnnotation: string(architectures.Static)},
			arch:        architectures.StaticGA,
			wantProxy:   false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			sts := proxyTestStatefulSet(
				tc.annotations,
				tc.arch,
				testProxyImage,
				testMongodbImage,
				testInitImage,
				"https://example.com/mongodb-mms-automation-agent-1000.2.0.11583-1.rhel8_x86_64.tar.gz",
			)

			_, hasProxyVolume := volumeByName(sts, "mongodb-proxy")
			assert.Equal(t, tc.wantProxy, hasProxyVolume)
			if tc.wantProxy {
				mongodb := containerByName(t, sts, util.DatabaseContainerName)
				agent := containerByName(t, sts, util.AgentContainerName)
				assert.Equal(t, []string{"/mnt/proxy/proxy"}, mongodb.Command)
				assert.Contains(t, agent.Env, corev1.EnvVar{Name: "MDB_MONGODB_PROXY_ADDRESS", Value: "127.0.0.1:8099"})
			} else {
				for _, c := range sts.Spec.Template.Spec.Containers {
					assert.NotEqual(t, []string{"/mnt/proxy/proxy"}, c.Command)
					for _, e := range c.Env {
						assert.NotEqual(t, "MDB_MONGODB_PROXY_ADDRESS", e.Name)
					}
				}
			}
		})
	}
}

func TestStaticArchitectureWithoutTheProxyKeepsTheSharedProcessNamespace(t *testing.T) {
	sts := proxyTestStatefulSet(
		nil,
		architectures.Static,
		testProxyImage,
		testMongodbImage,
		testInitImage,
		"https://example.com/mongodb-mms-automation-agent-1000.2.0.11583-1.rhel8_x86_64.tar.gz",
	)

	require.NotNil(t, sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.True(t, *sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.Equal(t, []string{"bash", "-c", "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"},
		containerByName(t, sts, util.DatabaseContainerName).Command)
	assert.Contains(t, containerByName(t, sts, util.AgentContainerName).Env,
		corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"})
	// the container that the agent finds through /proc stays in this mode
	assert.Equal(t, util.AgentContainerUtilitiesName, containerByName(t, sts, util.AgentContainerUtilitiesName).Name)
}
