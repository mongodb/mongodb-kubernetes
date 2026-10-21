package construct

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

func Test_tlsVolumeSource_CAFilePath(t *testing.T) {
	t.Run("custom path uses KeyToPath directory mount", func(t *testing.T) {
		customPath := "/etc/ssl/certs/ca.pem"
		src := &tlsVolumeSource{
			security: &mdbv1.Security{
				TLSConfig: &mdbv1.TLSConfig{
					Enabled:    true,
					CA:         "my-ca-configmap",
					CAFilePath: customPath,
				},
			},
			databaseOpts: DatabaseStatefulSetOptions{Name: "my-rs"},
			logger:       zap.S(),
		}

		volumes, mounts := src.getVolumesAndMounts()

		assert.True(t, slices.ContainsFunc(mounts, func(m corev1.VolumeMount) bool {
			return m.MountPath == path.Dir(customPath) && m.SubPath == "" && m.ReadOnly
		}), "custom CAFilePath should mount at the parent directory")
		assert.False(t, slices.ContainsFunc(mounts, func(m corev1.VolumeMount) bool {
			return m.MountPath == util.TLSCaMountPath
		}), "default TLSCaMountPath should not be present in custom mode")
		assert.True(t, slices.ContainsFunc(volumes, func(v corev1.Volume) bool {
			cm := v.ConfigMap
			return cm != nil &&
				len(cm.Items) == 1 &&
				cm.Items[0].Key == tls.CAConfigMapKey &&
				cm.Items[0].Path == path.Base(customPath)
		}), "custom CAFilePath volume should map ca-pem key to the custom filename")
	})

	t.Run("default path mounts whole ConfigMap at TLSCaMountPath", func(t *testing.T) {
		src := &tlsVolumeSource{
			security: &mdbv1.Security{
				TLSConfig: &mdbv1.TLSConfig{
					Enabled: true,
					CA:      "my-ca-configmap",
				},
			},
			databaseOpts: DatabaseStatefulSetOptions{Name: "my-rs"},
			logger:       zap.S(),
		}

		volumes, mounts := src.getVolumesAndMounts()

		// Default mode: full directory mount at TLSCaMountPath, no KeyToPath items.
		assert.True(t, slices.ContainsFunc(mounts, func(m corev1.VolumeMount) bool {
			return m.MountPath == util.TLSCaMountPath
		}), "default mode should mount at TLSCaMountPath")
		assert.True(t, slices.ContainsFunc(volumes, func(v corev1.Volume) bool {
			cm := v.ConfigMap
			return cm != nil && len(cm.Items) == 0
		}), "default mode should not restrict ConfigMap keys via Items")
	})
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
			ReadOnlyRootFilesystem:   new(true),
			AllowPrivilegeEscalation: new(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
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
		t.Setenv(util.ImagePullPolicyEnv, "")
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
	t.Run("Empty Image Pull Policy", func(t *testing.T) {
		t.Setenv(util.ImagePullPolicyEnv, "")
		sc := mdbv1.NewClusterBuilder().Build()
		kubeClient, _ := mock.NewDefaultFakeClient(sc)
		shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, sc)
		configServerSpec := createConfigSrvSpec(sc)
		mongosSpec := createMongosSpec(sc)
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ShardOptions(0, shardSpec, memberCluster.Name, WithDefaultArchitecture(architectures.Static)), zap.S())
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ConfigServerOptions(configServerSpec, memberCluster.Name, WithDefaultArchitecture(architectures.Static)), zap.S())
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, MongosOptions(mongosSpec, memberCluster.Name, WithDefaultArchitecture(architectures.Static)), zap.S())
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
	opts = DatabaseStatefulSetOptions{
		PodVars:              envVars,
		ExternalAgentVersion: "108.0.1.8800-1",
	}
	podEnv = databaseEnvVars(opts)
	var names []string
	for _, ev := range podEnv {
		names = append(names, ev.Name)
	}
	assert.Contains(t, names, util.EnvVarAgentVersion)
	for _, ev := range podEnv {
		if ev.Name == util.EnvVarAgentVersion {
			assert.Equal(t, "108.0.1.8800-1", ev.Value)
		}
	}

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

func TestDatabaseAgentSettings(t *testing.T) {
	tests := []struct {
		name           string
		variables      map[string]string
		parameters     mdbv1.StartupParameters
		externalDomain *string
		multiCluster   bool
		want           mdbv1.StartupParameters
		absent         []string
	}{
		{name: "OM defaults"},
		{
			name:      "headless",
			variables: map[string]string{util.EnvVarBaseUrl: "", util.EnvVarProjectId: ""},
			want:      mdbv1.StartupParameters{"mmsGroupId": "", "cluster": "/var/lib/mongodb-automation/cluster-config.json", "serveStatusPort": "5000"},
			absent:    []string{"mmsBaseUrl", "mmsApiKeyExec"},
		},
		{
			// An explicit empty value cannot mean "connect to an empty URL". It must not wipe the
			// environment-derived mode, which would leave neither mmsBaseUrl nor the headless keys.
			name:       "explicit empty mmsBaseUrl is treated as unset",
			variables:  map[string]string{util.EnvVarBaseUrl: "https://ops-manager.example.com"},
			parameters: mdbv1.StartupParameters{"mmsBaseUrl": ""},
			want:       mdbv1.StartupParameters{"mmsBaseUrl": "https://ops-manager.example.com"},
			absent:     []string{"cluster", "serveStatusPort"},
		},
		{
			name:      "static binaries and download base",
			variables: map[string]string{"MDB_STATIC_CONTAINERS_ARCHITECTURE": "true", DownloadBaseEnv: "/custom/download"},
			want:      mdbv1.StartupParameters{"operatorMode": "true", "binariesFixedPath": "/custom/download/mongod/bin"},
			absent:    []string{"useLocalMongoDbTools"},
		},
		{
			name:       "customer settings win including unknown keys",
			parameters: mdbv1.StartupParameters{"logLevel": "DEBUG", "logFile": "/custom logs/agent.log", "maxLogFileDurationHrs": "12", "futureAgentSetting": "one,two=three"},
			want:       mdbv1.StartupParameters{"logLevel": "DEBUG", "logFile": "/custom logs/agent.log", "maxLogFileDurationHrs": "12", "futureAgentSetting": "one,two=three"},
		},
		{
			name:       "raw credentials and identity stay outside the shared file",
			parameters: mdbv1.StartupParameters{"mmsApiKey": "secret-api-key-value", "overrideLocalHost": "customer.example.com"},
			absent:     []string{"mmsApiKey", "overrideLocalHost"},
		},
		{
			name:       "customer credential command wins",
			parameters: mdbv1.StartupParameters{"mmsApiKey": "secret-api-key-value", "mmsApiKeyExec": "cat /custom/key"},
			want:       mdbv1.StartupParameters{"mmsApiKeyExec": "cat /custom/key"},
			absent:     []string{"mmsApiKey"},
		},
		{
			name:      "effective TLS and runtime directories",
			variables: map[string]string{"MMS_HOME": "/custom/automation", "MMS_LOG_DIR": "/custom/logs", util.EnvVarSSLRequireValidMMSCertificates: "false", util.EnvVarSSLTrustedMMSServerCertificate: "/custom/ca.pem"},
			want:      mdbv1.StartupParameters{"pidfilepath": "/custom/automation/mongodb-mms-automation-agent.pid", "healthCheckFilePath": "/custom/logs/agent-health-status.json", "tlsRequireValidMMSServerCertificates": "false", "httpsCAFile": "/custom/ca.pem", "mmsApiKeyExec": "cat '/custom/automation/agent-api-key/agentApiKey'"},
		},
		{
			name:      "credential paths are shell quoted",
			variables: map[string]string{"MMS_HOME": "/custom O'Reilly/$HOME"},
			want:      mdbv1.StartupParameters{"pidfilepath": "/custom O'Reilly/$HOME/mongodb-mms-automation-agent.pid", "mmsApiKeyExec": "cat '/custom O'\\''Reilly/$HOME/agent-api-key/agentApiKey'"},
		},
		{name: "external domain", externalDomain: new("example.com"), want: mdbv1.StartupParameters{"ephemeralPortOffset": "1"}},
		{name: "multi cluster", multiCluster: true, want: mdbv1.StartupParameters{"ephemeralPortOffset": "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			variables := map[string]string{util.EnvVarBaseUrl: "https://ops-manager.example.com/", util.EnvVarProjectId: "project"}
			maps.Copy(variables, tt.variables)
			opts := DatabaseStatefulSetOptions{AgentConfig: &mdbv1.AgentConfig{StartupParameters: tt.parameters}, MultiClusterMode: tt.multiCluster}
			if tt.externalDomain != nil {
				opts.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: tt.externalDomain}
			}
			original := maps.Clone(tt.parameters)
			settings := databaseAgentSettings(opts, variables)
			assert.Equal(t, original, tt.parameters)
			expected := mdbv1.StartupParameters{
				"mmsBaseUrl":                           "https://ops-manager.example.com",
				"mmsGroupId":                           "project",
				"pidfilepath":                          "/mongodb-automation/mongodb-mms-automation-agent.pid",
				"maxLogFileDurationHrs":                "24",
				"logLevel":                             "INFO",
				"logFile":                              "/var/log/mongodb-mms-automation/automation-agent.log",
				"healthCheckFilePath":                  "/var/log/mongodb-mms-automation/agent-health-status.json",
				"tlsRequireValidMMSServerCertificates": "true",
				"useLocalMongoDbTools":                 "true",
				"mmsApiKeyExec":                        "cat '/mongodb-automation/agent-api-key/agentApiKey'",
			}
			maps.Copy(expected, tt.want)
			for _, key := range tt.absent {
				delete(expected, key)
			}
			assert.Equal(t, expected, settings)
			content, err := settings.ToSettingsFile()
			require.NoError(t, err)
			assert.NotContains(t, content, "secret-api-key-value")
		})
	}
}

func TestDatabaseAgentSettingsConfigMap(t *testing.T) {
	for _, architecture := range []architectures.DefaultArchitecture{architectures.Static, architectures.NonStatic} {
		t.Run(string(architecture), func(t *testing.T) {
			mdb := mdbv1.NewReplicaSetBuilder().SetName("my-rs").SetNamespace("my-ns").SetAgentConfig(mdbv1.AgentConfig{
				StartupParameters: mdbv1.StartupParameters{"logLevel": "DEBUG", "mmsApiKey": "secret-api-key-value", "overrideLocalHost": "customer.example.com"},
			}).Build()
			options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architecture))
			sts := DatabaseStatefulSet(*mdb, options, zap.S())
			cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
			require.NoError(t, err)
			assert.Equal(t, "my-rs-agent-settings", cm.Name)
			assert.Equal(t, sts.Namespace, cm.Namespace)
			assert.Equal(t, sts.Labels, cm.Labels)
			assert.Equal(t, sts.OwnerReferences, cm.OwnerReferences)
			require.Len(t, cm.Data, 1)
			content := cm.Data[AgentSettingsFileName]
			assert.Contains(t, content, "logLevel=DEBUG\n")
			assert.Contains(t, content, "mmsApiKeyExec=cat '/mongodb-automation/agent-api-key/agentApiKey'\n")
			assert.NotContains(t, content, "secret-api-key-value")
			assert.NotContains(t, content, "overrideLocalHost=")
			if architecture == architectures.Static {
				assert.Contains(t, content, "operatorMode=true\n")
			} else {
				assert.Contains(t, content, "useLocalMongoDbTools=true\n")
			}
		})
	}
	t.Run("invalid merged settings return no ConfigMap", func(t *testing.T) {
		mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{
			StartupParameters: mdbv1.StartupParameters{"logLevel": "INFO\nmmsApiKey=secret-api-key-value"},
		}).Build()
		options := ReplicaSetOptions(GetPodEnvOptions())
		sts := DatabaseStatefulSet(*mdb, options, zap.S())
		cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
		require.Error(t, err)
		assert.Empty(t, cm.Data)
		assert.NotContains(t, err.Error(), "secret-api-key-value")
	})
	for _, variable := range []corev1.EnvVar{
		{Name: "AGENT_FLAGS", Value: "-mmsApiKey=secret-api-key-value,"},
		{Name: "AGENT_FLAGS", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "flags"}, Key: "value"}}},
	} {
		t.Run("unresolved environment override "+variable.Name, func(t *testing.T) {
			mdb := mdbv1.NewReplicaSetBuilder().Build()
			options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.NonStatic))
			sts := DatabaseStatefulSet(*mdb, options, zap.S())
			for i := range sts.Spec.Template.Spec.Containers[0].Env {
				if sts.Spec.Template.Spec.Containers[0].Env[i].Name == variable.Name {
					sts.Spec.Template.Spec.Containers[0].Env[i] = variable
				}
			}
			cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
			require.Error(t, err)
			assert.Empty(t, cm.Data)
			assert.NotContains(t, err.Error(), "secret-api-key-value")
		})
	}
	t.Run("custom AGENT_FLAGS rejection names the supported surface", func(t *testing.T) {
		mdb := mdbv1.NewReplicaSetBuilder().Build()
		options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.NonStatic))
		sts := DatabaseStatefulSet(*mdb, options, zap.S())
		for i := range sts.Spec.Template.Spec.Containers[0].Env {
			if sts.Spec.Template.Spec.Containers[0].Env[i].Name == "AGENT_FLAGS" {
				sts.Spec.Template.Spec.Containers[0].Env[i].Value = "-logLevel=DEBUG,"
			}
		}
		_, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent.startupOptions")
	})
	t.Run("operator generated AGENT_FLAGS is not rejected", func(t *testing.T) {
		mdb := mdbv1.NewReplicaSetBuilder().Build()
		options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.NonStatic))
		sts := DatabaseStatefulSet(*mdb, options, zap.S())
		cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
		require.NoError(t, err)
		assert.NotEmpty(t, cm.Data)
	})
	// The cases above inject the environment directly. This one goes through the real customer
	// surface: spec.podSpec.podTemplate, merged by DatabaseStatefulSet before the guard runs.
	t.Run("customer pod template AGENT_FLAGS is rejected through the merge path", func(t *testing.T) {
		mdb := mdbv1.NewReplicaSetBuilder().Build()
		if mdb.Spec.PodSpec == nil {
			mdb.Spec.PodSpec = &mdbv1.MongoDbPodSpec{}
		}
		mdb.Spec.PodSpec.PodTemplateWrapper.PodTemplate = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: util.DatabaseContainerName,
					Env:  []corev1.EnvVar{{Name: "AGENT_FLAGS", Value: "-logLevel=DEBUG,"}},
				}},
			},
		}
		options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.NonStatic))
		sts := DatabaseStatefulSet(*mdb, options, zap.S())
		cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
		require.Error(t, err)
		assert.Empty(t, cm.Data)
		assert.Contains(t, err.Error(), "agent.startupOptions")
	})
}

func TestDatabaseAgentSettingsRuntimeEnvironment(t *testing.T) {
	for _, architecture := range []architectures.DefaultArchitecture{architectures.Static, architectures.NonStatic} {
		for _, tc := range []struct {
			variable string
			absent   []string
		}{
			{util.EnvVarBaseUrl, []string{"mmsBaseUrl", "cluster", "serveStatusPort"}},
			{util.EnvVarProjectId, []string{"mmsGroupId"}},
			{util.EnvVarLogLevel, []string{"logLevel"}},
			{"MMS_HOME", []string{"pidfilepath"}},
			{"MMS_LOG_DIR", []string{"healthCheckFilePath"}},
			{"MDB_STATIC_CONTAINERS_ARCHITECTURE", []string{"operatorMode", "useLocalMongoDbTools", "binariesFixedPath"}},
			{DownloadBaseEnv, []string{"binariesFixedPath"}},
			{util.EnvVarMultiClusterMode, []string{"ephemeralPortOffset"}},
			{util.EnvVarSSLRequireValidMMSCertificates, []string{"tlsRequireValidMMSServerCertificates"}},
			{util.EnvVarSSLTrustedMMSServerCertificate, []string{"httpsCAFile"}},
		} {
			for _, expression := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/expression=%t", architecture, tc.variable, expression), func(t *testing.T) {
					mdb := mdbv1.NewReplicaSetBuilder().Build()
					options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architecture), func(opts *DatabaseStatefulSetOptions) {
						opts.PodVars.BaseURL = "https://ops-manager.example.com"
					})
					sts := DatabaseStatefulSet(*mdb, options, zap.S())
					agent := container.GetByName(util.AgentContainerName, sts.Spec.Template.Spec.Containers)
					if agent == nil {
						agent = container.GetByName(util.DatabaseContainerName, sts.Spec.Template.Spec.Containers)
					}
					variable := corev1.EnvVar{Name: tc.variable, Value: "$(RUNTIME_VALUE)"}
					if !expression {
						variable.Value = ""
						variable.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "runtime"}, Key: "value"}}
					}
					container.WithEnvs(variable)(agent)
					cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
					require.NoError(t, err)
					for _, key := range tc.absent {
						assert.NotContains(t, "\n"+cm.Data[AgentSettingsFileName], "\n"+key+"=")
					}
					assert.NotContains(t, cm.Data[AgentSettingsFileName], "$(RUNTIME_VALUE)")
					if tc.variable == "MMS_HOME" {
						assert.Contains(t, cm.Data[AgentSettingsFileName], `mmsApiKeyExec=cat "${MMS_HOME:-/mongodb-automation}/agent-api-key/agentApiKey"`)
					}
				})
			}
		}
	}
}

func TestDatabaseAgentRuntimeEnvironmentPrecedence(t *testing.T) {
	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{
		StartupParameters: mdbv1.StartupParameters{"logLevel": "DEBUG", "mmsApiKeyExec": "cat /custom/key"},
	}).Build()
	options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.NonStatic))
	sts := DatabaseStatefulSet(*mdb, options, zap.S())
	agent := container.GetByName(util.DatabaseContainerName, sts.Spec.Template.Spec.Containers)
	agent.EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "runtime"}}}}
	container.WithEnvs(corev1.EnvVar{Name: util.EnvVarLogLevel, Value: "$(RUNTIME_LEVEL)"})(agent)
	cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
	require.NoError(t, err)
	settings := cm.Data[AgentSettingsFileName]
	assert.Contains(t, settings, "logLevel=DEBUG\n")
	assert.Contains(t, settings, "mmsApiKeyExec=cat /custom/key\n")
	assert.NotContains(t, "\n"+settings, "\npidfilepath=")
	assert.Contains(t, settings, "mmsGroupId=abcd\n", "explicit Env shadows EnvFrom")
	runtime := DatabaseAgentRuntimeSettings(*agent, options(*mdb))
	assert.Contains(t, runtime, "pidfilepath")
	assert.NotContains(t, runtime, "logLevel")
	assert.NotContains(t, runtime, "mmsGroupId")
	assert.True(t, slices.IsSorted(runtime))
}

func TestDatabaseAgentRuntimeCredentialCommand(t *testing.T) {
	home := path.Join(t.TempDir(), "home '$(not-a-command)'")
	require.NoError(t, os.MkdirAll(path.Join(home, "agent-api-key"), 0o700))
	require.NoError(t, os.WriteFile(path.Join(home, "agent-api-key", util.OmAgentApiKey), []byte("test-key"), 0o600))
	for _, baseURL := range []string{"", "https://ops-manager.example.com/"} {
		for _, rawKey := range []bool{false, true} {
			t.Run(fmt.Sprintf("url=%q/raw=%t", baseURL, rawKey), func(t *testing.T) {
				parameters := mdbv1.StartupParameters{}
				if rawKey {
					parameters["mmsApiKey"] = "test-key"
				}
				mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{StartupParameters: parameters}).Build()
				options := ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.NonStatic))
				sts := DatabaseStatefulSet(*mdb, options, zap.S())
				agent := container.GetByName(util.DatabaseContainerName, sts.Spec.Template.Spec.Containers)
				for _, name := range []string{"MMS_HOME", util.EnvVarBaseUrl} {
					container.WithEnvs(corev1.EnvVar{Name: name, Value: "$(RUNTIME_VALUE)"})(agent)
				}
				cm, err := DatabaseAgentSettingsConfigMap(sts, options(*mdb))
				require.NoError(t, err)
				var command string
				for line := range strings.SplitSeq(cm.Data[AgentSettingsFileName], "\n") {
					if value, ok := strings.CutPrefix(line, "mmsApiKeyExec="); ok {
						command = value
					}
				}
				require.NotEmpty(t, command)
				cmd := exec.Command("/bin/sh", "-c", command)
				cmd.Env = []string{"PATH=/usr/bin:/bin", "MMS_HOME=" + home, "BASE_URL=" + baseURL}
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, string(output))
				if baseURL == "" && !rawKey {
					assert.Empty(t, string(output))
				} else {
					assert.Equal(t, "test-key", string(output))
				}
			})
		}
	}
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

func TestLogConfigurationToEnvVars(t *testing.T) {
	var parameters mdbv1.StartupParameters = map[string]string{
		"a":       "1",
		"logFile": "/var/log/mongodb-mms-automation/log.file",
	}
	additionalMongodConfig := mdbv1.NewEmptyAdditionalMongodConfig()
	additionalMongodConfig.AddOption("auditLog", map[string]any{
		"destination": "file",
		"format":      "JSON",
		"path":        "/var/log/mongodb-mms-automation/audit.log",
	})

	envVars := logConfigurationToEnvVars(parameters, additionalMongodConfig)
	assert.Len(t, envVars, 6)

	logFileAutomationAgentEnvVar := corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: path.Join(util.PvcMountPathLogs, "log.file")}
	logFileAutomationAgentVerboseEnvVar := corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: path.Join(util.PvcMountPathLogs, "log-verbose.file")}
	logFileAutomationAgentDefaultEnvVar := corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: path.Join(util.PvcMountPathLogs, "automation-agent.log")}
	logFileAutomationAgentVerboseDefaultEnvVar := corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: path.Join(util.PvcMountPathLogs, "automation-agent-verbose.log")}
	logFileMongoDBAuditEnvVar := corev1.EnvVar{Name: LogFileMongoDBAuditEnv, Value: path.Join(util.PvcMountPathLogs, "audit.log")}
	logFileMongoDBAuditDefaultEnvVar := corev1.EnvVar{Name: LogFileMongoDBAuditEnv, Value: path.Join(util.PvcMountPathLogs, "mongodb-audit.log")}
	logFileMongoDBEnvVar := corev1.EnvVar{Name: LogFileMongoDBEnv, Value: path.Join(util.PvcMountPathLogs, "mongodb.log")}
	logFileAgentMonitoringEnvVar := corev1.EnvVar{Name: LogFileAgentMonitoringEnv, Value: path.Join(util.PvcMountPathLogs, "monitoring-agent.log")}
	logFileAgentBackupEnvVar := corev1.EnvVar{Name: LogFileAgentBackupEnv, Value: path.Join(util.PvcMountPathLogs, "backup-agent.log")}

	numberOfLogFilesInEnvVars := 6

	t.Run("automation log is changed and audit log is changed", func(t *testing.T) {
		envVars := logConfigurationToEnvVars(parameters, additionalMongodConfig)
		assert.Len(t, envVars, numberOfLogFilesInEnvVars)
		assert.Contains(t, envVars, logFileAutomationAgentEnvVar)
		assert.Contains(t, envVars, logFileAutomationAgentVerboseEnvVar)
		assert.Contains(t, envVars, logFileMongoDBAuditEnvVar)
		assert.Contains(t, envVars, logFileMongoDBEnvVar)
		assert.Contains(t, envVars, logFileAgentMonitoringEnvVar)
		assert.Contains(t, envVars, logFileAgentBackupEnvVar)
	})

	t.Run("automation log is changed and audit log is default", func(t *testing.T) {
		envVars := logConfigurationToEnvVars(parameters, additionalMongodConfig)
		assert.Len(t, envVars, numberOfLogFilesInEnvVars)
		assert.Contains(t, envVars, logFileAutomationAgentEnvVar)
		assert.Contains(t, envVars, logFileAutomationAgentVerboseEnvVar)
		assert.Contains(t, envVars, logFileMongoDBAuditEnvVar)
		assert.Contains(t, envVars, logFileMongoDBEnvVar)
		assert.Contains(t, envVars, logFileAgentMonitoringEnvVar)
		assert.Contains(t, envVars, logFileAgentBackupEnvVar)
	})

	t.Run("automation log is default and audit log is changed", func(t *testing.T) {
		envVars = logConfigurationToEnvVars(map[string]string{}, additionalMongodConfig)
		assert.Len(t, envVars, numberOfLogFilesInEnvVars)
		assert.Contains(t, envVars, logFileAutomationAgentDefaultEnvVar)
		assert.Contains(t, envVars, logFileAutomationAgentVerboseDefaultEnvVar)
		assert.Contains(t, envVars, logFileMongoDBAuditEnvVar)
		assert.Contains(t, envVars, logFileMongoDBEnvVar)
		assert.Contains(t, envVars, logFileAgentMonitoringEnvVar)
		assert.Contains(t, envVars, logFileAgentBackupEnvVar)
	})

	t.Run("all log files are default", func(t *testing.T) {
		envVars = logConfigurationToEnvVars(map[string]string{"other": "value"}, mdbv1.NewEmptyAdditionalMongodConfig().AddOption("other", "value"))
		assert.Len(t, envVars, numberOfLogFilesInEnvVars)
		assert.Contains(t, envVars, logFileAutomationAgentDefaultEnvVar)
		assert.Contains(t, envVars, logFileAutomationAgentVerboseDefaultEnvVar)
		assert.Contains(t, envVars, logFileMongoDBAuditDefaultEnvVar)
		assert.Contains(t, envVars, logFileMongoDBEnvVar)
		assert.Contains(t, envVars, logFileAgentMonitoringEnvVar)
		assert.Contains(t, envVars, logFileAgentBackupEnvVar)
	})
}

func TestGetAutomationLogEnvVars(t *testing.T) {
	t.Run("automation log file with extension", func(t *testing.T) {
		envVars := getAutomationLogEnvVars(map[string]string{"logFile": "path/to/log.file"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: "path/to/log.file"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: "path/to/log-verbose.file"})
	})

	t.Run("automation log file without extension", func(t *testing.T) {
		envVars := getAutomationLogEnvVars(map[string]string{"logFile": "path/to/logfile"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: "path/to/logfile"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: "path/to/logfile-verbose"})
	})
	t.Run("invalid automation log file is not crashing", func(t *testing.T) {
		envVars := getAutomationLogEnvVars(map[string]string{"logFile": "path/to/"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: "path/to/"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: "path/to/-verbose"})
	})

	t.Run("dash-prefixed logFile retargets the tailed log", func(t *testing.T) {
		envVars := getAutomationLogEnvVars(map[string]string{"-logFile": "/custom/agent.log"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: "/custom/agent.log"})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: "/custom/agent-verbose.log"})
	})

	t.Run("empty automation log file is falling back to default names", func(t *testing.T) {
		envVars := getAutomationLogEnvVars(map[string]string{"logFile": ""})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: path.Join(util.PvcMountPathLogs, "automation-agent.log")})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: path.Join(util.PvcMountPathLogs, "automation-agent-verbose.log")})
	})

	t.Run("not set logFile cause falling back to default names", func(t *testing.T) {
		envVars := getAutomationLogEnvVars(map[string]string{})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentEnv, Value: path.Join(util.PvcMountPathLogs, "automation-agent.log")})
		assert.Contains(t, envVars, corev1.EnvVar{Name: LogFileAutomationAgentVerboseEnv, Value: path.Join(util.PvcMountPathLogs, "automation-agent-verbose.log")})
	})
}

func TestDatabaseStatefulSet_DownloadBaseEnvVar(t *testing.T) {
	t.Run("static architecture sets MMS_DOWNLOAD_BASE to the configured value", func(t *testing.T) {
		mdb := mdbv1.NewReplicaSetBuilder().Build()
		mdb.Spec.DownloadBase = "/custom/download/base"

		sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.Static)), zap.S())

		agentIdx := slices.IndexFunc(sts.Spec.Template.Spec.Containers, func(c corev1.Container) bool {
			return c.Name == util.AgentContainerName
		})
		require.NotEqual(t, -1, agentIdx)
		assert.Contains(t, sts.Spec.Template.Spec.Containers[agentIdx].Env,
			corev1.EnvVar{Name: DownloadBaseEnv, Value: "/custom/download/base"})
	})

	t.Run("non-static architecture sets MMS_DOWNLOAD_BASE to the configured value", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.NonStatic))

		mdb := mdbv1.NewReplicaSetBuilder().Build()
		mdb.Spec.DownloadBase = "/custom/download/base"

		sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), zap.S())

		agentIdx := slices.IndexFunc(sts.Spec.Template.Spec.Containers, func(c corev1.Container) bool {
			return c.Name == util.DatabaseContainerName
		})
		require.NotEqual(t, -1, agentIdx)
		assert.Contains(t, sts.Spec.Template.Spec.Containers[agentIdx].Env,
			corev1.EnvVar{Name: DownloadBaseEnv, Value: "/custom/download/base"})
	})

	t.Run("default download base does not set MMS_DOWNLOAD_BASE", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.NonStatic))

		// No DownloadBase configured, so GetDownloadBase returns util.DefaultPvcMmsMountPath.
		mdb := mdbv1.NewReplicaSetBuilder().Build()

		sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), zap.S())

		agentIdx := slices.IndexFunc(sts.Spec.Template.Spec.Containers, func(c corev1.Container) bool {
			return c.Name == util.DatabaseContainerName
		})
		require.NotEqual(t, -1, agentIdx)
		for _, env := range sts.Spec.Template.Spec.Containers[agentIdx].Env {
			assert.NotEqual(t, DownloadBaseEnv, env.Name)
		}
	})
}

func TestBuildStatefulSet_ShardedCustomDownloadBase(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.NonStatic))

	sc := mdbv1.NewClusterBuilder().Build()
	sc.Spec.DownloadBase = "/custom/download/base"

	kubeClient, _ := mock.NewDefaultFakeClient(sc)
	shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, sc)
	configServerSpec := createConfigSrvSpec(sc)
	mongosSpec := createMongosSpec(sc)

	withPodVars := func(options *DatabaseStatefulSetOptions) {
		options.PodVars = defaultPodVars()
	}

	cases := map[string]func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions{
		"shard":        ShardOptions(0, shardSpec, memberCluster.Name, withPodVars),
		"configserver": ConfigServerOptions(configServerSpec, memberCluster.Name, withPodVars),
		"mongos":       MongosOptions(mongosSpec, memberCluster.Name, withPodVars),
	}

	for name, optFunc := range cases {
		t.Run(name, func(t *testing.T) {
			set := DatabaseStatefulSet(*sc, optFunc, zap.S())
			assertAgentDownloadMount(t, set, "/custom/download/base")
		})
	}
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
			mdb := mdbv1.NewReplicaSetBuilder().SetAnnotations(tt.annotations).Build()
			sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions(), WithDefaultArchitecture(architectures.DefaultArchitecture(tt.defaultArchitecture))), zap.S())

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

func TestShardedOptionsResolveExternalAccessPerTier(t *testing.T) {
	perTierConfigSrv := &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("config.example.com")}
	topLevel := &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("top.example.com")}

	newShardedCluster := func(mutate func(spec *mdbv1.MongoDbSpec)) mdbv1.MongoDB {
		sc := mdbv1.NewDefaultShardedClusterBuilder().
			SetName("slaney").
			SetNamespace("test-ns").
			SetShardCountSpec(1).
			SetMongosCountSpec(2).
			Build()
		mutate(&sc.Spec)

		// single-cluster StatefulSet construction looks up the member cluster in each tier's
		// ClusterSpecList, which the reconciler normally populates during defaulting.
		for _, componentSpec := range []*mdbv1.ShardedClusterComponentSpec{sc.Spec.ConfigSrvSpec, sc.Spec.MongosSpec, sc.Spec.ShardSpec} {
			componentSpec.ClusterSpecList = mdbv1.ClusterSpecList{
				{ClusterName: multicluster.LegacyCentralClusterName, Members: 3},
			}
		}

		return *sc
	}

	t.Run("config servers pick up their per-tier configuration in single-cluster", func(t *testing.T) {
		sc := newShardedCluster(func(spec *mdbv1.MongoDbSpec) {
			spec.ConfigSrvSpec.ExternalAccessConfiguration = perTierConfigSrv
		})

		opts := ConfigServerOptions(sc.Spec.ConfigSrvSpec, multicluster.LegacyCentralClusterName)(sc)

		require.NotNil(t, opts.ExternalAccessConfiguration)
		assert.Equal(t, "config.example.com", *opts.ExternalAccessConfiguration.ExternalDomain)
		assert.Equal(t, sc.GetHostNameOverrideConfigmapName(), opts.HostNameOverrideConfigmapName,
			"a tier with an external domain must mount the hostname-override ConfigMap")
	})

	t.Run("config servers get nothing from the top-level field in single-cluster", func(t *testing.T) {
		sc := newShardedCluster(func(spec *mdbv1.MongoDbSpec) {
			spec.ExternalAccessConfiguration = topLevel
		})

		opts := ConfigServerOptions(sc.Spec.ConfigSrvSpec, multicluster.LegacyCentralClusterName)(sc)

		assert.Nil(t, opts.ExternalAccessConfiguration)
		assert.Empty(t, opts.HostNameOverrideConfigmapName)
	})

	t.Run("mongos still honours the top-level field in single-cluster", func(t *testing.T) {
		sc := newShardedCluster(func(spec *mdbv1.MongoDbSpec) {
			spec.ExternalAccessConfiguration = topLevel
		})

		opts := MongosOptions(sc.Spec.MongosSpec, multicluster.LegacyCentralClusterName)(sc)

		require.NotNil(t, opts.ExternalAccessConfiguration)
		assert.Equal(t, "top.example.com", *opts.ExternalAccessConfiguration.ExternalDomain)
		assert.Equal(t, sc.GetHostNameOverrideConfigmapName(), opts.HostNameOverrideConfigmapName)
	})

	t.Run("shards get nothing when nothing is configured", func(t *testing.T) {
		sc := newShardedCluster(func(spec *mdbv1.MongoDbSpec) {})

		opts := ShardOptions(0, sc.Spec.ShardSpec, multicluster.LegacyCentralClusterName)(sc)

		assert.Nil(t, opts.ExternalAccessConfiguration)
		assert.Empty(t, opts.HostNameOverrideConfigmapName)
	})
}

func TestReplicaSetOptionsCarryTopLevelExternalAccess(t *testing.T) {
	topLevel := &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("rs.example.com")}
	rs := mdbv1.NewReplicaSetBuilder().SetName("rs").SetNamespace("test-ns").SetMembers(3).Build()
	rs.Spec.ExternalAccessConfiguration = topLevel

	opts := ReplicaSetOptions()(*rs)

	require.NotNil(t, opts.ExternalAccessConfiguration)
	assert.Equal(t, "rs.example.com", *opts.ExternalAccessConfiguration.ExternalDomain)
	// An external domain means processes advertise their external hostname, which only works with the
	// hostname override ConfigMap mounted into the pods.
	assert.Equal(t, rs.GetHostNameOverrideConfigmapName(), opts.HostNameOverrideConfigmapName)
}

// TestStandaloneOptionsCarryTopLevelExternalAccess guards against the external service of a standalone
// being deleted on the next reconciliation: create.DatabaseInKubernetes creates or deletes it based on
// DatabaseStatefulSetOptions.ExternalAccessConfiguration, so StandaloneOptions has to populate it.
func TestStandaloneOptionsCarryTopLevelExternalAccess(t *testing.T) {
	st := mdbv1.NewStandaloneBuilder().SetName("st").SetNamespace("test-ns").Build()
	st.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("st.example.com")}

	opts := StandaloneOptions()(*st)

	require.NotNil(t, opts.ExternalAccessConfiguration)
	assert.Equal(t, "st.example.com", *opts.ExternalAccessConfiguration.ExternalDomain)
}

func TestStandaloneOptionsWithoutExternalAccess(t *testing.T) {
	st := mdbv1.NewStandaloneBuilder().SetName("st").SetNamespace("test-ns").Build()

	opts := StandaloneOptions()(*st)

	assert.Nil(t, opts.ExternalAccessConfiguration)
}
