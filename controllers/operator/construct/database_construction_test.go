package construct

import (
	"path"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

func Test_buildDatabaseInitContainer(t *testing.T) {
	modification := buildDatabaseInitContainer("quay.io/mongodb/mongodb-kubernetes-init-database:latest")
	container := &corev1.Container{}
	modification(container)
	expectedVolumeMounts := []corev1.VolumeMount{{
		Name:      PvcNameDatabaseScripts,
		MountPath: PvcMountPathScripts,
		ReadOnly:  false,
	}}
	expectedContainer := &corev1.Container{
		Name:         InitDatabaseContainerName,
		Image:        "quay.io/mongodb/mongodb-kubernetes-init-database:latest",
		VolumeMounts: expectedVolumeMounts,
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false),
		},
	}
	assert.Equal(t, expectedContainer, container)
}

func createShardSpecAndDefaultCluster(client kubernetesClient.Client, sc *mdbv1.MongoDB) (*mdbv1.ShardedClusterComponentSpec, multicluster.MemberCluster) {
	shardSpec := sc.Spec.ShardSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
		},
	}

	return shardSpec, multicluster.GetLegacyCentralMemberCluster(sc.Spec.MongodsPerShardCount, 0, client, secrets.SecretClient{KubeClient: client})
}

func createConfigSrvSpec(sc *mdbv1.MongoDB) *mdbv1.ShardedClusterComponentSpec {
	shardSpec := sc.Spec.ConfigSrvSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
		},
	}

	return shardSpec
}

func createMongosSpec(sc *mdbv1.MongoDB) *mdbv1.ShardedClusterComponentSpec {
	shardSpec := sc.Spec.ConfigSrvSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
		},
	}

	return shardSpec
}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	t.Run("Empty Image Pull Policy", func(t *testing.T) {
		t.Setenv(util.AutomationAgentImagePullPolicy, "")
		sc := mdbv1.NewClusterBuilder().Build()

		kubeClient, _ := mock.NewDefaultFakeClient(sc)
		shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, sc)
		configServerSpec := createConfigSrvSpec(sc)
		mongosSpec := createMongosSpec(sc)

		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ShardOptions(0, shardSpec, memberCluster.Name), zap.S())
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ConfigServerOptions(configServerSpec, memberCluster.Name), zap.S())
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, MongosOptions(mongosSpec, memberCluster.Name), zap.S())
		})
	})
}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSetStatic(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
	t.Run("Empty Image Pull Policy", func(t *testing.T) {
		t.Setenv(util.AutomationAgentImagePullPolicy, "")
		sc := mdbv1.NewClusterBuilder().Build()
		kubeClient, _ := mock.NewDefaultFakeClient(sc)
		shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, sc)
		configServerSpec := createConfigSrvSpec(sc)
		mongosSpec := createMongosSpec(sc)
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ShardOptions(0, shardSpec, memberCluster.Name), zap.S())
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ConfigServerOptions(configServerSpec, memberCluster.Name), zap.S())
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, MongosOptions(mongosSpec, memberCluster.Name), zap.S())
		})
	})
}

func TestStatefulsetCreationSuccessful(t *testing.T) {
	start := time.Now()
	rs := mdbv1.NewReplicaSetBuilder().Build()

	_ = DatabaseStatefulSet(*rs, ReplicaSetOptions(GetPodEnvOptions()), zap.S())
	assert.True(t, time.Since(start) < time.Second*4) // we waited only a little (considering 2 seconds of wait as well)
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
	v := &caVolumeSource{}
	extraEnvs := v.GetEnvs()

	opts = DatabaseStatefulSetOptions{PodVars: envVars, ExtraEnvs: extraEnvs}
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
	opts = DatabaseStatefulSetOptions{PodVars: envVars, ExtraEnvs: extraEnvs}
	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 7)
	assert.Equal(t, podEnv[6], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})
}

func TestAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"key1":    "Value1",
		"key3":    "Value3",
		"message": "Hello",
		"key2":    "Value2",
		// logFile is a default agent variable which we override for illustration in this test
		"logFile": "/etc/agent.log",
	}

	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{StartupParameters: agentStartupParameters}).Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), zap.S())
	variablesMap := env.ToMap(sts.Spec.Template.Spec.Containers[0].Env...)
	val, ok := variablesMap["AGENT_FLAGS"]
	assert.True(t, ok)
	// AGENT_FLAGS environment variable is sorted
	assert.Equal(t, val, "-key1=Value1,-key2=Value2,-key3=Value3,-logFile=/etc/agent.log,-message=Hello,")
}

func TestLabelsAndAnotations(t *testing.T) {
	labels := map[string]string{"l1": "val1", "l2": "val2"}
	annotations := map[string]string{"a1": "val1", "a2": "val2"}

	mdb := mdbv1.NewReplicaSetBuilder().SetAnnotations(annotations).SetLabels(labels).Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), zap.S())

	// add the default label to the map
	labels["app"] = "test-mdb-svc"
	assert.Equal(t, labels, sts.Labels)
}

func TestDatabaseStatefulSet_StaticContainersEnvVars(t *testing.T) {
	tests := []struct {
		name                 string
		defaultArchitecture  string
		annotations          map[string]string
		expectedEnvVar       corev1.EnvVar
		expectAgentContainer bool
	}{
		{
			name:                 "Default architecture - static, no annotations",
			defaultArchitecture:  string(architectures.Static),
			annotations:          nil,
			expectedEnvVar:       corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"},
			expectAgentContainer: true,
		},
		{
			name:                 "Default architecture - non-static, annotations - static",
			defaultArchitecture:  string(architectures.NonStatic),
			annotations:          map[string]string{architectures.ArchitectureAnnotation: string(architectures.Static)},
			expectedEnvVar:       corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"},
			expectAgentContainer: true,
		},
		{
			name:                 "Default architecture - non-static, no annotations",
			defaultArchitecture:  string(architectures.NonStatic),
			annotations:          nil,
			expectedEnvVar:       corev1.EnvVar{},
			expectAgentContainer: false,
		},
		{
			name:                 "Default architecture - static, annotations - non-static",
			defaultArchitecture:  string(architectures.Static),
			annotations:          map[string]string{architectures.ArchitectureAnnotation: string(architectures.NonStatic)},
			expectedEnvVar:       corev1.EnvVar{},
			expectAgentContainer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(architectures.DefaultEnvArchitecture, tt.defaultArchitecture)

			mdb := mdbv1.NewReplicaSetBuilder().SetAnnotations(tt.annotations).Build()
			sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), zap.S())

			agentContainerIdx := slices.IndexFunc(sts.Spec.Template.Spec.Containers, func(container corev1.Container) bool {
				return container.Name == util.AgentContainerName
			})
			if tt.expectAgentContainer {
				require.NotEqual(t, -1, agentContainerIdx)
				assert.Contains(t, sts.Spec.Template.Spec.Containers[agentContainerIdx].Env, tt.expectedEnvVar)
			} else {
				// In non-static architecture there is no agent container
				// so the index should be -1.
				require.Equal(t, -1, agentContainerIdx)
			}
		})
	}
}
