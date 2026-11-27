package construct

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

const (
	testProxyImage   = "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy:1.0.0"
	testMongodbImage = "quay.io/mongodb/mongodb-enterprise-server:8.0.0-ubi9"
	testInitImage    = "quay.io/mongodb/mongodb-kubernetes-init-database:1.0.0"
)

func proxyTestStatefulSet(annotations map[string]string, arch architectures.DefaultArchitecture, extra ...func(*DatabaseStatefulSetOptions)) appsv1.StatefulSet {
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	mdb.Annotations = annotations
	modifications := []func(*DatabaseStatefulSetOptions){
		GetPodEnvOptions(),
		WithDefaultArchitecture(arch),
		func(o *DatabaseStatefulSetOptions) {
			o.AgentImage = "example.com/mongodb-agent:release"
			o.CustomAgentURL = "https://example.com/agent.tar.gz"
			o.MongodbImage = testMongodbImage
			o.InitDatabaseImage = testInitImage
		},
	}
	return DatabaseStatefulSet(*mdb, ReplicaSetOptions(append(modifications, extra...)...), zap.S())
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
	t.Setenv(util.MongodbProxyImageEnv, testProxyImage)
	sts := proxyTestStatefulSet(nil, architectures.StaticGA)

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

	t.Run("the agent container gets the init-database image", func(t *testing.T) {
		agent := containerByName(t, sts, util.AgentContainerName)
		assert.Contains(t, agent.VolumeMounts, corev1.VolumeMount{Name: "mongodb-init-database", MountPath: "/mnt/init-database", ReadOnly: true})

		initVolume, found := volumeByName(sts, "mongodb-init-database")
		require.True(t, found)
		require.NotNil(t, initVolume.Image)
		assert.Equal(t, testInitImage, initVolume.Image.Reference)

		// The Agent reads the database tools from its own image, so the MongoDB image is mounted nowhere else.
		_, found = volumeByName(sts, "mongodb-binaries")
		assert.False(t, found)
	})

	t.Run("the agent launcher checks the image volume before it runs", func(t *testing.T) {
		agent := containerByName(t, sts, util.AgentContainerName)
		require.Len(t, agent.Command, 3)
		assert.Equal(t, "/bin/bash", agent.Command[0])
		assert.Equal(t, "-c", agent.Command[1])
		assert.True(t, strings.HasPrefix(agent.Command[2], initDatabaseImageVolumeCheck(staticAgentLauncherPath)), agent.Command[2])
		assert.True(t, strings.HasSuffix(agent.Command[2], "exec "+staticAgentLauncherPath), agent.Command[2])
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

	t.Run("the scripts volume is not part of the pod", func(t *testing.T) {
		_, found := volumeByName(sts, PvcNameDatabaseScripts)
		assert.False(t, found, "the static-ga agent runs the launcher from the init-database image")
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
			arch:      architectures.StaticGA,
			wantProxy: true,
		},
		"static-ga annotation over static default": {
			annotations: map[string]string{architectures.ArchitectureAnnotation: string(architectures.StaticGA)},
			arch:        architectures.Static,
			wantProxy:   true,
		},
		"legacy static remains unchanged": {
			arch:      architectures.Static,
			wantProxy: false,
		},
		"non-static remains unchanged": {
			arch:      architectures.NonStatic,
			wantProxy: false,
		},
		"resource opts out into non-static": {
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
			t.Setenv(util.MongodbProxyImageEnv, testProxyImage)
			sts := proxyTestStatefulSet(tc.annotations, tc.arch)

			_, hasProxyVolume := volumeByName(sts, "mongodb-proxy")
			assert.Equal(t, tc.wantProxy, hasProxyVolume)
			if tc.wantProxy {
				mongod := containerByName(t, sts, util.DatabaseContainerName)
				agent := containerByName(t, sts, util.AgentContainerName)
				assert.Equal(t, "example.com/mongodb-agent:release", agent.Image)
				assert.Equal(t, []string{"/mnt/proxy/proxy"}, mongod.Command)
				for _, c := range sts.Spec.Template.Spec.Containers {
					assert.NotEqual(t, util.AgentContainerUtilitiesName, c.Name)
				}
				return
			}
			if tc.arch == architectures.Static || tc.annotations[architectures.ArchitectureAnnotation] == string(architectures.Static) {
				assert.Equal(t, "example.com/mongodb-agent:release", containerByName(t, sts, util.AgentContainerName).Image)
			}
			for _, c := range sts.Spec.Template.Spec.Containers {
				assert.NotEqual(t, []string{"/mnt/proxy/proxy"}, c.Command)
				for _, e := range c.Env {
					assert.NotEqual(t, "MDB_MONGODB_PROXY_ADDRESS", e.Name)
				}
			}
			for _, volumeName := range []string{"mongodb-proxy", "mongodb-proxy-socket"} {
				_, found := volumeByName(sts, volumeName)
				assert.False(t, found, volumeName)
			}
			if tc.arch == architectures.Static || tc.annotations[architectures.ArchitectureAnnotation] == string(architectures.Static) {
				_, found := volumeByName(sts, "mongodb-init-database")
				assert.True(t, found, "legacy static uses the init-database image volume")
			}
		})
	}
}

func TestStaticArchitectureWithoutTheProxyKeepsTheSharedProcessNamespace(t *testing.T) {
	t.Setenv(util.MongodbProxyImageEnv, testProxyImage)
	sts := proxyTestStatefulSet(nil, architectures.Static)

	require.NotNil(t, sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.True(t, *sts.Spec.Template.Spec.ShareProcessNamespace)
	assert.Equal(t, []string{"bash", "-c", "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"},
		containerByName(t, sts, util.DatabaseContainerName).Command)
	assert.Contains(t, containerByName(t, sts, util.AgentContainerName).Env,
		corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"})
	// The agent reads the launcher and probes from the init-database image volume, not from a holder container.
	for _, c := range sts.Spec.Template.Spec.Containers {
		assert.NotEqual(t, util.AgentContainerUtilitiesName, c.Name)
	}
	_, found := volumeByName(sts, "mongodb-init-database")
	assert.True(t, found)
}

func TestValidateStaticGADatabaseKeyfileUsers(t *testing.T) {
	t.Setenv(util.MongodbProxyImageEnv, testProxyImage)
	agent := int64(2000)
	same := int64(2000)
	database := int64(999)
	pod := int64(2000)

	for name, tc := range map[string]struct {
		architecture architectures.DefaultArchitecture
		template     *corev1.PodTemplateSpec
		wantError    bool
	}{
		"same container user":                {architecture: architectures.StaticGA, template: podTemplateWithUsers(nil, &agent, &same)},
		"different container users":          {architecture: architectures.StaticGA, template: podTemplateWithUsers(nil, &agent, &database), wantError: true},
		"pod user with a different override": {architecture: architectures.StaticGA, template: podTemplateWithUsers(&pod, &agent, &database), wantError: true},
		"only a pod user":                    {architecture: architectures.StaticGA, template: podTemplateWithUsers(&pod, nil, nil)},
		"legacy static keeps its own users":  {architecture: architectures.Static, template: podTemplateWithUsers(nil, &agent, &database)},
		"non-static keeps its own users":     {architecture: architectures.NonStatic, template: podTemplateWithUsers(nil, &agent, &database)},
		"nil pod spec":                       {architecture: architectures.StaticGA},
	} {
		t.Run(name, func(t *testing.T) {
			var spec *mdbv1.PodSpecWrapper
			if tc.template != nil {
				spec = podSpecWrapper(tc.template)
			}
			err := ValidateStaticGADatabaseKeyfileUsers(nil, tc.architecture, spec)
			if tc.wantError {
				require.ErrorContains(t, err, "same user")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateStaticGAKeyfileUsersAppliesWithoutTheProxyImage(t *testing.T) {
	t.Setenv(util.MongodbProxyImageEnv, "")
	agent := int64(2000)
	database := int64(999)

	err := ValidateStaticGADatabaseKeyfileUsers(nil, architectures.StaticGA, podSpecWrapper(podTemplateWithUsers(nil, &agent, &database)))

	require.ErrorContains(t, err, "same user", "static-ga is explicit, so the users must match even without the image")
}

func TestValidateStaticGAAppDBKeyfileUsers(t *testing.T) {
	t.Setenv(util.MongodbProxyImageEnv, testProxyImage)
	agent := int64(2000)
	mongod := int64(999)
	template := podTemplateWithUsers(nil, &agent, &mongod)
	template.Spec.Containers[1].Name = util.MongodbContainerName
	spec := &mdbv1.MongoDbPodSpec{PodTemplateWrapper: v1.PodTemplateSpecWrapper{PodTemplate: template}}

	err := ValidateStaticGAAppDBKeyfileUsers(nil, architectures.StaticGA, spec)

	require.ErrorContains(t, err, "same user")
}

func podTemplateWithUsers(podUID, agentUID, databaseUID *int64) *corev1.PodTemplateSpec {
	template := &corev1.PodTemplateSpec{}
	if podUID != nil {
		template.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsUser: podUID}
	}
	template.Spec.Containers = []corev1.Container{
		{Name: util.AgentContainerName, SecurityContext: containerUser(agentUID)},
		{Name: util.DatabaseContainerName, SecurityContext: containerUser(databaseUID)},
	}
	return template
}

func containerUser(uid *int64) *corev1.SecurityContext {
	if uid == nil {
		return nil
	}
	return &corev1.SecurityContext{RunAsUser: uid}
}

func podSpecWrapper(template *corev1.PodTemplateSpec) *mdbv1.PodSpecWrapper {
	return &mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{
			PodTemplateWrapper: v1.PodTemplateSpecWrapper{PodTemplate: template},
		},
	}
}

func TestMongodbProxyIgnoresAgentDebugAndItsSharedProcessNamespace(t *testing.T) {
	t.Setenv(util.MongodbProxyImageEnv, testProxyImage)
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(
		GetPodEnvOptions(),
		WithDefaultArchitecture(architectures.StaticGA),
		func(o *DatabaseStatefulSetOptions) {
			o.AgentImage = "example.com/mongodb-agent:release"
			o.MongodbImage = testMongodbImage
			o.InitDatabaseImage = testInitImage
			o.AgentDebug = true
			o.AgentDebugImage = "example.com/delve:latest"
		},
	), zap.S())

	assert.Nil(t, sts.Spec.Template.Spec.ShareProcessNamespace, "the debug sidecar must not reopen the proxy's process namespace")
	for _, c := range sts.Spec.Template.Spec.InitContainers {
		assert.NotEqual(t, "delve-sidecar", c.Name)
	}
}

func TestMongodbProxyImageVolumesUseTheConfiguredPullPolicy(t *testing.T) {
	t.Setenv(util.MongodbProxyImageEnv, testProxyImage)
	t.Setenv(util.ImagePullPolicyEnv, string(corev1.PullNever))
	sts := proxyTestStatefulSet(nil, architectures.StaticGA)

	for _, name := range []string{mongodbProxyVolumeName, initDatabaseVolumeName} {
		volume, found := volumeByName(sts, name)
		require.True(t, found, name)
		require.NotNil(t, volume.Image, name)
		assert.Equal(t, corev1.PullNever, volume.Image.PullPolicy, name)
	}
}
