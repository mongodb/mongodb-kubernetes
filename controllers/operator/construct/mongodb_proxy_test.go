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

func proxyTestStatefulSet(annotations map[string]string, arch architectures.DefaultArchitecture, proxyImage string) appsv1.StatefulSet {
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	mdb.Annotations = annotations
	return DatabaseStatefulSet(*mdb, ReplicaSetOptions(
		GetPodEnvOptions(),
		WithDefaultArchitecture(arch),
		func(o *DatabaseStatefulSetOptions) {
			o.MongodbProxyImage = proxyImage
			o.MongodbImage = testMongodbImage
			o.InitDatabaseImage = testInitImage
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
	sts := proxyTestStatefulSet(map[string]string{MongodbProxyAnnotation: "true"}, architectures.Static, testProxyImage)

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

	t.Run("the agent container gets the MongoDB image and the proxy address", func(t *testing.T) {
		agent := containerByName(t, sts, util.AgentContainerName)
		assert.Contains(t, agent.Env, corev1.EnvVar{Name: "MDB_MONGODB_PROXY_ADDRESS", Value: "unix:///var/run/mongodb-proxy/proxy.sock"})
		assert.Contains(t, agent.Env, corev1.EnvVar{Name: "MDB_MONGOD_ROOT", Value: "/mnt/mongodb"})
		assert.Contains(t, agent.VolumeMounts, corev1.VolumeMount{Name: "mongodb-binaries", MountPath: "/mnt/mongodb", ReadOnly: true})
		assert.Contains(t, agent.Env, corev1.EnvVar{Name: "MDB_INIT_ROOT", Value: "/mnt/init-database"})
		assert.Contains(t, agent.VolumeMounts, corev1.VolumeMount{Name: "mongodb-init-database", MountPath: "/mnt/init-database", ReadOnly: true})

		initVolume, found := volumeByName(sts, "mongodb-init-database")
		require.True(t, found)
		require.NotNil(t, initVolume.Image)
		assert.Equal(t, testInitImage, initVolume.Image.Reference)

		volume, found := volumeByName(sts, "mongodb-binaries")
		require.True(t, found)
		require.NotNil(t, volume.Image)
		assert.Equal(t, testMongodbImage, volume.Image.Reference)
	})

	t.Run("the proxy listens on a socket that only the agent and the database container mount", func(t *testing.T) {
		socketMount := corev1.VolumeMount{Name: "mongodb-proxy-socket", MountPath: "/var/run/mongodb-proxy"}

		mongod := containerByName(t, sts, util.DatabaseContainerName)
		assert.Equal(t, []string{"-listen", "unix:///var/run/mongodb-proxy/proxy.sock"}, mongod.Args)
		assert.Contains(t, mongod.VolumeMounts, socketMount)
		assert.Contains(t, containerByName(t, sts, util.AgentContainerName).VolumeMounts, socketMount)

		volume, found := volumeByName(sts, "mongodb-proxy-socket")
		require.True(t, found)
		require.NotNil(t, volume.EmptyDir, "the socket lives on an emptyDir")
		require.NotNil(t, volume.EmptyDir.SizeLimit)
		assert.Equal(t, "1Mi", volume.EmptyDir.SizeLimit.String())

		// The operator mounts it in no other container. A container that the user adds is not the operator's.
		mounting := 0
		for _, c := range sts.Spec.Template.Spec.Containers {
			for _, m := range c.VolumeMounts {
				if m.Name == "mongodb-proxy-socket" {
					mounting++
				}
			}
		}
		assert.Equal(t, 2, mounting)
		for _, c := range sts.Spec.Template.Spec.InitContainers {
			for _, m := range c.VolumeMounts {
				assert.NotEqual(t, "mongodb-proxy-socket", m.Name, "init container %s", c.Name)
			}
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

func TestMongodbProxyIsOffUnlessAllConditionsHold(t *testing.T) {
	for name, tc := range map[string]struct {
		annotations map[string]string
		arch        architectures.DefaultArchitecture
		proxyImage  string
	}{
		"no annotation":                {nil, architectures.Static, testProxyImage},
		"annotation is not true":       {map[string]string{MongodbProxyAnnotation: "false"}, architectures.Static, testProxyImage},
		"no proxy image configured":    {map[string]string{MongodbProxyAnnotation: "true"}, architectures.Static, ""},
		"non-static architecture":      {map[string]string{MongodbProxyAnnotation: "true"}, architectures.NonStatic, testProxyImage},
		"resource opts out of static":  {map[string]string{MongodbProxyAnnotation: "true", architectures.ArchitectureAnnotation: string(architectures.NonStatic)}, architectures.Static, testProxyImage},
		"annotation true, image empty": {map[string]string{MongodbProxyAnnotation: "true"}, architectures.Static, ""},
	} {
		t.Run(name, func(t *testing.T) {
			sts := proxyTestStatefulSet(tc.annotations, tc.arch, tc.proxyImage)

			_, hasProxyVolume := volumeByName(sts, "mongodb-proxy")
			_, hasBinariesVolume := volumeByName(sts, "mongodb-binaries")
			_, hasInitVolume := volumeByName(sts, "mongodb-init-database")
			assert.False(t, hasProxyVolume)
			assert.False(t, hasBinariesVolume)
			assert.False(t, hasInitVolume)
			_, hasSocketVolume := volumeByName(sts, "mongodb-proxy-socket")
			assert.False(t, hasSocketVolume)
			for _, c := range sts.Spec.Template.Spec.Containers {
				assert.NotContains(t, c.Command, "/mnt/proxy/proxy")
				for _, e := range c.Env {
					assert.NotEqual(t, "MDB_MONGODB_PROXY_ADDRESS", e.Name)
					assert.NotEqual(t, "MDB_INIT_ROOT", e.Name)
				}
			}
		})
	}
}

func TestStaticArchitectureWithoutTheProxyKeepsTheSharedProcessNamespace(t *testing.T) {
	sts := proxyTestStatefulSet(nil, architectures.Static, testProxyImage)

	require.NotNil(t, sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.True(t, *sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.Equal(t, []string{"bash", "-c", "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"},
		containerByName(t, sts, util.DatabaseContainerName).Command)
	assert.Contains(t, containerByName(t, sts, util.AgentContainerName).Env,
		corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"})
	// the container that the agent finds through /proc stays in this mode
	assert.Equal(t, util.AgentContainerUtilitiesName, containerByName(t, sts, util.AgentContainerUtilitiesName).Name)
}
