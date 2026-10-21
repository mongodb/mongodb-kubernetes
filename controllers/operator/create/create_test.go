package create

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	enterprisests "github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func init() {
	mock.InitDefaultEnvVariables()
}

func TestDatabaseAgentSettingsDelivery(t *testing.T) {
	for _, architecture := range []architectures.DefaultArchitecture{architectures.Static, architectures.NonStatic} {
		t.Run(string(architecture), func(t *testing.T) {
			ctx := context.Background()
			fakeClient, _ := mock.NewDefaultFakeClient()
			secretClient := secrets.SecretClient{KubeClient: fakeClient}
			mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{
				StartupParameters: mdbv1.StartupParameters{"logLevel": "DEBUG", "mmsApiKey": "customer-api-key", "overrideLocalHost": "customer.example.com"},
			}).Build()
			options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), construct.WithDefaultArchitecture(architecture))
			sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
			sts.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
			created, err := DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
			require.NoError(t, err)
			assert.Equal(t, sts.Spec.UpdateStrategy, created.Spec.UpdateStrategy)
			cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(mdb.Namespace, sts.Name+"-agent-settings"))
			require.NoError(t, err)
			assert.Contains(t, cm.Data[construct.AgentSettingsFileName], "logLevel=DEBUG\n")
			assert.NotContains(t, cm.Data[construct.AgentSettingsFileName], "customer-api-key")
			assert.NotContains(t, cm.Data[construct.AgentSettingsFileName], "overrideLocalHost=")
			keySecret, err := fakeClient.GetSecret(ctx, kube.ObjectKey(mdb.Namespace, sts.Name+"-agent-api-key"))
			require.NoError(t, err)
			assert.Equal(t, "customer-api-key", string(keySecret.Data[util.OmAgentApiKey]))
			agent := container.GetByName(util.AgentContainerName, created.Spec.Template.Spec.Containers)
			if agent == nil {
				agent = container.GetByName(util.DatabaseContainerName, created.Spec.Template.Spec.Containers)
			}
			require.NotNil(t, agent)
			assert.Equal(t, []string{"-f", "/etc/mongodb-agent/automation-agent.conf", "-overrideLocalHost=customer.example.com"}, agent.Args)
			assert.NotContains(t, strings.Join(agent.Args, " "), "customer-api-key")
			for _, variable := range agent.Env {
				assert.NotEqual(t, "AGENT_FLAGS", variable.Name)
			}
			assert.Contains(t, agent.VolumeMounts, corev1.VolumeMount{Name: construct.AgentSettingsVolume, MountPath: construct.AgentSettingsPath, ReadOnly: true})
			assert.NotEmpty(t, created.Spec.Template.Annotations[construct.AgentSettingsHash])
			stored, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(mdb.Namespace, sts.Name))
			require.NoError(t, err)
			_, err = DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
			require.NoError(t, err)
			repeated, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(mdb.Namespace, sts.Name))
			require.NoError(t, err)
			assert.Equal(t, stored.Spec.Template, repeated.Spec.Template)
		})
	}
}

func TestDatabaseAgentCredentialOverrideWithSecretProjection(t *testing.T) {
	for _, architecture := range []architectures.DefaultArchitecture{architectures.Static, architectures.NonStatic} {
		t.Run(string(architecture), func(t *testing.T) {
			ctx := context.Background()
			fakeClient, _ := mock.NewDefaultFakeClient()
			secretClient := secrets.SecretClient{KubeClient: fakeClient}
			mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{
				StartupParameters: mdbv1.StartupParameters{"mmsApiKey": "customer-api-key"},
			}).Build()
			options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), construct.WithDefaultArchitecture(architecture))
			sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
			defaultMode, fileMode := int32(0o440), int32(0o400)
			optional := true
			for i := range sts.Spec.Template.Spec.Volumes {
				volume := &sts.Spec.Template.Spec.Volumes[i]
				if volume.Name == construct.AgentAPIKeyVolumeName {
					volume.Secret.SecretName = "customer-credential"
					volume.Secret.DefaultMode = &defaultMode
					volume.Secret.Optional = &optional
					volume.Secret.Items = []corev1.KeyToPath{{Key: "custom-key", Path: util.OmAgentApiKey, Mode: &fileMode}}
				}
			}
			created, err := DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
			require.NoError(t, err)
			keySecret, err := fakeClient.GetSecret(ctx, kube.ObjectKey(mdb.Namespace, sts.Name+"-agent-api-key"))
			require.NoError(t, err)
			for _, volume := range created.Spec.Template.Spec.Volumes {
				if volume.Name != construct.AgentAPIKeyVolumeName {
					continue
				}
				assert.Equal(t, keySecret.Name, volume.Secret.SecretName)
				assert.Equal(t, &defaultMode, volume.Secret.DefaultMode)
				assert.Equal(t, &optional, volume.Secret.Optional)
				require.Len(t, volume.Secret.Items, 1)
				item := volume.Secret.Items[0]
				assert.Equal(t, util.OmAgentApiKey, item.Path)
				assert.Equal(t, &fileMode, item.Mode)
				assert.Equal(t, "customer-api-key", string(keySecret.Data[item.Key]), "projected key must exist in generated credential Secret")
			}
			credential, err := databaseAgentCredential(ctx, secretClient, *created)
			require.NoError(t, err)
			assert.Equal(t, "customer-api-key", credential)
			stored, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(mdb.Namespace, sts.Name))
			require.NoError(t, err)
			_, err = DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
			require.NoError(t, err)
			repeated, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(mdb.Namespace, sts.Name))
			require.NoError(t, err)
			assert.Equal(t, stored.Spec.Template, repeated.Spec.Template)
		})
	}
}

func TestDatabaseAgentSettingsRejectsInvalidFileBeforeWriting(t *testing.T) {
	ctx := context.Background()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{
		StartupParameters: mdbv1.StartupParameters{"logLevel": "INFO\nmmsApiKey=secret-api-key-value", "mmsApiKey": "customer-api-key"},
	}).Build()
	options := construct.ReplicaSetOptions(construct.GetPodEnvOptions())
	sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
	_, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, options, zap.S())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-api-key-value")
	_, err = fakeClient.GetStatefulSet(ctx, kube.ObjectKey(mdb.Namespace, sts.Name))
	assert.True(t, errors.IsNotFound(err))
	_, err = fakeClient.GetConfigMap(ctx, kube.ObjectKey(mdb.Namespace, sts.Name+"-agent-settings"))
	assert.True(t, errors.IsNotFound(err))
	_, err = fakeClient.GetSecret(ctx, kube.ObjectKey(mdb.Namespace, sts.Name+"-agent-api-key"))
	assert.True(t, errors.IsNotFound(err))
}

func TestDatabaseAgentSettingsPreservesCLIFlagPrefixes(t *testing.T) {
	ctx := context.Background()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{
		StartupParameters: mdbv1.StartupParameters{"-logLevel": "DEBUG", "-mmsApiKey": "customer-api-key", "-overrideLocalHost": "customer.example.com"},
	}).Build()
	options := construct.ReplicaSetOptions(construct.GetPodEnvOptions())
	sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
	created, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, options, zap.S())
	require.NoError(t, err)
	cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(mdb.Namespace, sts.Name+"-agent-settings"))
	require.NoError(t, err)
	assert.Contains(t, cm.Data[construct.AgentSettingsFileName], "logLevel=DEBUG\n")
	assert.NotContains(t, cm.Data[construct.AgentSettingsFileName], "customer-api-key")
	keySecret, err := fakeClient.GetSecret(ctx, kube.ObjectKey(mdb.Namespace, sts.Name+"-agent-api-key"))
	require.NoError(t, err)
	assert.Equal(t, "customer-api-key", string(keySecret.Data[util.OmAgentApiKey]))
	assert.Contains(t, created.Spec.Template.Spec.Containers[0].Args, "-overrideLocalHost=customer.example.com")
	assert.Contains(t, mdb.Spec.Agent.StartupParameters, "-mmsApiKey")
}

func TestDatabaseAgentCredentialOverrideUsesVault(t *testing.T) {
	t.Setenv("SECRET_BACKEND", vault.VaultBackend)
	ctx := context.Background()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{
		StartupParameters: mdbv1.StartupParameters{"mmsApiKey": "customer-api-key"},
	}).Build()
	configuration := vault.VaultConfiguration{DatabaseSecretPath: "agent-config-tests"}
	secretPath := "/secret/data/agent-config-tests/" + mdb.Namespace + "/" + mdb.Name + "-agent-api-key"
	writes := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1"+secretPath {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload struct {
			Data map[string]string `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		writes <- payload.Data
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	vaultClient, err := vault.NewVaultClientForTesting(server.URL, "test-token", configuration)
	require.NoError(t, err)
	options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), func(opts *construct.DatabaseStatefulSetOptions) { opts.VaultConfig = configuration })
	sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
	created, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient, VaultClient: vaultClient}, *mdb, sts, options, zap.S())
	require.NoError(t, err)
	select {
	case data := <-writes:
		assert.Equal(t, map[string]string{util.OmAgentApiKey: "customer-api-key"}, data)
	default:
		t.Fatal("credential override was not written to Vault")
	}
	_, err = fakeClient.GetSecret(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name+"-agent-api-key"))
	assert.True(t, errors.IsNotFound(err), "Vault credentials must not also be stored in Kubernetes")
	assert.Equal(t, secretPath, created.Spec.Template.Annotations["vault.hashicorp.com/agent-inject-secret-agentApiKey"])
	assert.Contains(t, created.Spec.Template.Annotations["vault.hashicorp.com/agent-inject-template-agentApiKey"], secretPath)
	cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name+"-agent-settings"))
	require.NoError(t, err)
	assert.NotContains(t, cm.Data[construct.AgentSettingsFileName], "customer-api-key")
}

func TestDatabaseAgentSettingsHashTracksConsumedCredentials(t *testing.T) {
	ctx := context.Background()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), func(opts *construct.DatabaseStatefulSetOptions) {
		opts.PodVars.BaseURL = "https://ops-manager.example.com"
		opts.PodVars.AgentAPIKey = "unused-pod-vars-key"
	})
	sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
	var keySecret corev1.Secret
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name == construct.AgentAPIKeyVolumeName {
			keySecret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: volume.Secret.SecretName, Namespace: sts.Namespace}, Data: map[string][]byte{util.OmAgentApiKey: []byte("consumed-key-one")}}
		}
	}
	require.NotEmpty(t, keySecret.Name)
	require.NoError(t, fakeClient.CreateSecret(ctx, keySecret))
	secretClient := secrets.SecretClient{KubeClient: fakeClient}
	apply := func() string {
		t.Helper()
		created, err := DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
		require.NoError(t, err)
		hash := created.Spec.Template.Annotations[construct.AgentSettingsHash]
		assert.Len(t, hash, 64)
		return hash
	}
	original := apply()
	keySecret, err := fakeClient.GetSecret(ctx, kube.ObjectKey(sts.Namespace, keySecret.Name))
	require.NoError(t, err)
	keySecret.Labels = map[string]string{"unrelated": "change"}
	keySecret.Data["unrelated"] = []byte("not-consumed")
	require.NoError(t, fakeClient.UpdateSecret(ctx, keySecret))
	assert.Equal(t, original, apply(), "unrelated Secret data and metadata must not alter startup inputs")
	keySecret, err = fakeClient.GetSecret(ctx, kube.ObjectKey(sts.Namespace, keySecret.Name))
	require.NoError(t, err)
	keySecret.Data[util.OmAgentApiKey] = []byte("consumed-key-two")
	require.NoError(t, fakeClient.UpdateSecret(ctx, keySecret))
	assert.NotEqual(t, original, apply(), "rotating the consumed key must change the startup digest")
}

func TestDatabaseAgentSettingsCredentialSources(t *testing.T) {
	for _, tc := range []struct {
		name       string
		baseURL    string
		parameters mdbv1.StartupParameters
		wantError  bool
	}{
		{name: "headless needs no credential"},
		{name: "missing OM credential blocks apply", baseURL: "https://ops-manager.example.com", wantError: true},
		{name: "customer command takes precedence over raw key", baseURL: "https://ops-manager.example.com", parameters: mdbv1.StartupParameters{"mmsApiKey": "unused-raw-key", "mmsApiKeyExec": "cat /custom/key"}},
		{name: "empty URL override needs no credential", baseURL: "https://ops-manager.example.com", parameters: mdbv1.StartupParameters{"mmsBaseUrl": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fakeClient, _ := mock.NewDefaultFakeClient()
			mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{StartupParameters: tc.parameters}).Build()
			options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), func(opts *construct.DatabaseStatefulSetOptions) { opts.PodVars.BaseURL = tc.baseURL })
			sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
			_, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, options, zap.S())
			if tc.wantError {
				require.Error(t, err)
				_, err = fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
				assert.True(t, errors.IsNotFound(err))
				_, err = fakeClient.GetConfigMap(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-settings"))
				assert.True(t, errors.IsNotFound(err))
			} else {
				require.NoError(t, err)
				cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-settings"))
				require.NoError(t, err)
				assert.NotContains(t, cm.Data[construct.AgentSettingsFileName], "unused-raw-key")
			}
			_, err = fakeClient.GetSecret(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-api-key"))
			assert.True(t, errors.IsNotFound(err), "do not generate credentials that the agent will not consume")
		})
	}
}

func TestDatabaseAgentRuntimeEnvironmentDelivery(t *testing.T) {
	for _, architecture := range []architectures.DefaultArchitecture{architectures.Static, architectures.NonStatic} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/override=%t", architecture, override), func(t *testing.T) {
				ctx := context.Background()
				fakeClient, _ := mock.NewDefaultFakeClient()
				mdb := mdbv1.NewReplicaSetBuilder().Build()
				if override {
					mdb.Spec.Agent.StartupParameters = mdbv1.StartupParameters{"logLevel": "DEBUG", "mmsApiKey": "private-key"}
				}
				options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), construct.WithDefaultArchitecture(architecture))
				sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
				agent := container.GetByName(util.AgentContainerName, sts.Spec.Template.Spec.Containers)
				if agent == nil {
					agent = container.GetByName(util.DatabaseContainerName, sts.Spec.Template.Spec.Containers)
				}
				container.WithEnvs(corev1.EnvVar{Name: util.EnvVarBaseUrl, Value: "$(RUNTIME_URL)"}, corev1.EnvVar{
					Name: util.EnvVarLogLevel, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "runtime"}, Key: "level"}},
				})(agent)
				created, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, options, zap.S())
				require.NoError(t, err)
				agent = container.GetByName(agent.Name, created.Spec.Template.Spec.Containers)
				variables := env.ToMap(agent.Env...)
				assert.Equal(t, "$(RUNTIME_URL)", variables[util.EnvVarBaseUrl])
				assert.Contains(t, variables[construct.AgentRuntimeSettingsEnv], "mmsBaseUrl")
				_, hasFlags := variables["AGENT_FLAGS"]
				assert.False(t, hasFlags)
				cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-settings"))
				require.NoError(t, err)
				settings := cm.Data[construct.AgentSettingsFileName]
				assert.NotContains(t, settings, "$(RUNTIME_URL)")
				assert.NotContains(t, settings, "private-key")
				assert.NotContains(t, strings.Join(agent.Args, " "), "private-key")
				if override {
					assert.Contains(t, settings, "logLevel=DEBUG\n")
					assert.NotContains(t, variables[construct.AgentRuntimeSettingsEnv], "logLevel")
					assert.NotContains(t, settings, "if [ -n")
				} else {
					assert.NotContains(t, settings, "logLevel=")
					assert.Contains(t, variables[construct.AgentRuntimeSettingsEnv], "logLevel")
					assert.Contains(t, settings, `if [ -n "${BASE_URL%/}" ]; then`)
				}
			})
		}
	}
}

func TestDatabaseAgentSettingsHashTracksVaultCredential(t *testing.T) {
	t.Setenv("SECRET_BACKEND", vault.VaultBackend)
	ctx := context.Background()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	configuration := vault.VaultConfiguration{DatabaseSecretPath: "agent-config-tests"}
	data := map[string]string{util.OmAgentApiKey: "vault-key-one"}
	var dataLock sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/secret/data/agent-config-tests/"+mdb.Namespace+"/abcd-group-secret" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		dataLock.Lock()
		defer dataLock.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data}})
	}))
	t.Cleanup(server.Close)
	vaultClient, err := vault.NewVaultClientForTesting(server.URL, "test-token", configuration)
	require.NoError(t, err)
	options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), func(opts *construct.DatabaseStatefulSetOptions) {
		opts.VaultConfig = configuration
		opts.PodVars.BaseURL = "https://ops-manager.example.com"
	})
	sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
	secretClient := secrets.SecretClient{KubeClient: fakeClient, VaultClient: vaultClient}
	created, err := DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
	require.NoError(t, err)
	original := created.Spec.Template.Annotations[construct.AgentSettingsHash]
	dataLock.Lock()
	data["unrelated"] = "not-consumed"
	dataLock.Unlock()
	created, err = DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
	require.NoError(t, err)
	assert.Equal(t, original, created.Spec.Template.Annotations[construct.AgentSettingsHash])
	dataLock.Lock()
	data[util.OmAgentApiKey] = "vault-key-two"
	dataLock.Unlock()
	created, err = DatabaseInKubernetes(ctx, fakeClient, secretClient, *mdb, sts, options, zap.S())
	require.NoError(t, err)
	assert.NotEqual(t, original, created.Spec.Template.Annotations[construct.AgentSettingsHash])
}

func TestDatabaseAgentSettingsHostname(t *testing.T) {
	for _, tc := range []struct {
		name        string
		domain      *string
		multi       bool
		override    *string
		want        string
		wantPodName bool
	}{
		{name: "default keeps native hostname"},
		{name: "external domain", domain: new("example.com"), want: "$(POD_NAME).example.com", wantPodName: true},
		{name: "multicluster service identity", multi: true, want: "$(POD_NAME)-svc." + mock.TestNamespace + ".svc.custom.local", wantPodName: true},
		{name: "customer hostname wins", domain: new("example.com"), multi: true, override: new("customer.example.com"), want: "customer.example.com"},
		{name: "empty customer hostname is preserved", domain: new("example.com"), override: new("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fakeClient, _ := mock.NewDefaultFakeClient()
			parameters := mdbv1.StartupParameters{}
			if tc.override != nil {
				parameters["overrideLocalHost"] = *tc.override
			}
			mdb := mdbv1.NewReplicaSetBuilder().SetNamespace(mock.TestNamespace).SetAgentConfig(mdbv1.AgentConfig{StartupParameters: parameters}).Build()
			mdb.Spec.ClusterDomain = "custom.local"
			options := construct.ReplicaSetOptions(construct.GetPodEnvOptions(), func(opts *construct.DatabaseStatefulSetOptions) {
				opts.MultiClusterMode = tc.multi
				if tc.domain != nil {
					opts.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: tc.domain}
				}
			})
			sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
			created, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, options, zap.S())
			require.NoError(t, err)
			agent := &created.Spec.Template.Spec.Containers[0]
			wantArgs := []string{"-f", "/etc/mongodb-agent/automation-agent.conf"}
			if tc.want != "" || tc.override != nil {
				wantArgs = append(wantArgs, "-overrideLocalHost="+tc.want)
			}
			assert.Equal(t, wantArgs, agent.Args)
			for _, variable := range agent.Env {
				if variable.Name == "POD_NAME" {
					assert.True(t, tc.wantPodName)
					require.NotNil(t, variable.ValueFrom)
					assert.Equal(t, "metadata.name", variable.ValueFrom.FieldRef.FieldPath)
				}
			}
		})
	}
}

type agentSettingsWriteFailureClient struct {
	kubernetesClient.Client
}

func (c agentSettingsWriteFailureClient) UpdateConfigMap(context.Context, corev1.ConfigMap) error {
	return fmt.Errorf("settings ConfigMap write failed")
}

func TestDatabaseAgentSettingsConfigMapFailurePreservesStatefulSet(t *testing.T) {
	ctx := context.Background()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	options := construct.ReplicaSetOptions(construct.GetPodEnvOptions())
	sts := construct.DatabaseStatefulSet(*mdb, options, zap.S())
	require.NoError(t, fakeClient.CreateStatefulSet(ctx, sts))
	stored, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
	require.NoError(t, err)
	_, err = DatabaseInKubernetes(ctx, agentSettingsWriteFailureClient{Client: fakeClient}, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, options, zap.S())
	require.ErrorContains(t, err, "settings ConfigMap write failed")
	after, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
	require.NoError(t, err)
	assert.Equal(t, stored, after)
}

func TestBuildService(t *testing.T) {
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	svc := BuildService(kube.ObjectKey(mock.TestNamespace, "my-svc"), mdb, new("label"), nil, 2000, omv1.MongoDBOpsManagerServiceDefinition{
		Type:           corev1.ServiceTypeClusterIP,
		Port:           2000,
		LoadBalancerIP: "loadbalancerip",
	})

	// BuildService does not set OwnerReferences; callers are responsible for setting them
	// explicitly (single-cluster) or leaving them nil (multi-cluster).
	assert.Empty(t, svc.OwnerReferences)
	assert.Equal(t, mock.TestNamespace, svc.Namespace)
	assert.Equal(t, "my-svc", svc.Name)
	assert.Equal(t, "loadbalancerip", svc.Spec.LoadBalancerIP)
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
	assert.Equal(t, "label", svc.Labels[appLabelKey])
	assert.NotContains(t, svc.Labels, appsv1.StatefulSetPodNameLabel)
	assert.True(t, svc.Spec.PublishNotReadyAddresses)

	// test podName label not nil
	svc = BuildService(kube.ObjectKey(mock.TestNamespace, "my-svc"), mdb, nil, new("podName"), 2000, omv1.MongoDBOpsManagerServiceDefinition{
		Type:           corev1.ServiceTypeClusterIP,
		Port:           2000,
		LoadBalancerIP: "loadbalancerip",
	})

	assert.Empty(t, svc.OwnerReferences)
	assert.Equal(t, mock.TestNamespace, svc.Namespace)
	assert.Equal(t, "my-svc", svc.Name)
	assert.Equal(t, "loadbalancerip", svc.Spec.LoadBalancerIP)
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
	assert.NotContains(t, svc.Labels, appLabelKey)
	assert.Equal(t, "podName", svc.Labels[appsv1.StatefulSetPodNameLabel])
	assert.True(t, svc.Spec.PublishNotReadyAddresses)
}

func TestOpsManagerInKubernetes_InternalConnectivityOverride(t *testing.T) {
	ctx := context.Background()
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetInternalConnectivity(omv1.MongoDBOpsManagerServiceDefinition{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: new("0.0.12.0"),
			Port:      5000,
		}).
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}

	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	mutatedSts, err := OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
	assert.NoError(t, err)
	assert.NotNil(t, mutatedSts)

	svc, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP, "The operator creates a ClusterIP service if explicitly requested to do so.")
	assert.Equal(t, svc.Spec.ClusterIP, "0.0.12.0", "The operator configures the requested ClusterIP for the service")

	assert.Len(t, svc.Spec.Ports, 2, "Backup port should have been added to existing internal service")

	port0 := svc.Spec.Ports[0]
	assert.Equal(t, internalConnectivityPortName, port0.Name)

	port1 := svc.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)
}

func TestOpsManagerInKubernetes_DefaultInternalServiceForMultiCluster(t *testing.T) {
	ctx := context.Background()
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}

	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	mutatedSts, err := OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
	assert.NoError(t, err)
	assert.NotNil(t, mutatedSts)

	svc, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP, "Default internal service for OM multicluster is of type ClusterIP")
	assert.Equal(t, svc.Spec.ClusterIP, "", "Default internal service for OM multicluster is not a headless service")
}

func TestOpsManagerInKubernetes_ClusterSpecificExternalConnectivity(t *testing.T) {
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"

	type testCase struct {
		clusterSpecList            []omv1.ClusterSpecOMItem
		commonExternalConnectivity *omv1.MongoDBOpsManagerServiceDefinition
		expectedServices           map[string]corev1.Service
	}

	testCases := map[string]testCase{
		"no common external connectivity + cluster specific": {
			clusterSpecList: []omv1.ClusterSpecOMItem{
				{
					ClusterName: memberClusterName1,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type: corev1.ServiceTypeNodePort,
						Port: 30006,
					},
				},
				{
					ClusterName: memberClusterName2,
					Members:     1,
				},
				{
					ClusterName: memberClusterName3,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type:           corev1.ServiceTypeLoadBalancer,
						Port:           8080,
						LoadBalancerIP: "10.10.10.1",
					},
				},
			},
			commonExternalConnectivity: nil,
			expectedServices: map[string]corev1.Service{
				memberClusterName1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: nil,
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       30006,
								TargetPort: intstr.FromInt32(8080),
								NodePort:   30006,
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeNodePort,
						PublishNotReadyAddresses: false,
					},
				},
				memberClusterName3: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: nil,
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       8080,
								TargetPort: intstr.FromInt32(8080),
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeLoadBalancer,
						LoadBalancerIP:           "10.10.10.1",
						PublishNotReadyAddresses: false,
					},
				},
			},
		},
		"common external connectivity + cluster specific": {
			clusterSpecList: []omv1.ClusterSpecOMItem{
				{
					ClusterName: memberClusterName1,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type: corev1.ServiceTypeNodePort,
						Port: 30006,
					},
				},
				{
					ClusterName: memberClusterName2,
					Members:     1,
				},
				{
					ClusterName: memberClusterName3,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type:           corev1.ServiceTypeLoadBalancer,
						Port:           8080,
						LoadBalancerIP: "10.10.10.1",
					},
				},
			},
			commonExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
				Type:           corev1.ServiceTypeLoadBalancer,
				Port:           5005,
				LoadBalancerIP: "20.20.20.2",
				Annotations: map[string]string{
					"test-annotation": "test-value",
				},
			},
			expectedServices: map[string]corev1.Service{
				memberClusterName1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: nil,
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       30006,
								TargetPort: intstr.FromInt32(8080),
								NodePort:   30006,
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeNodePort,
						PublishNotReadyAddresses: false,
					},
				},
				memberClusterName2: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: nil,
						Annotations: map[string]string{
							"test-annotation": "test-value",
						},
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       5005,
								TargetPort: intstr.FromInt32(8080),
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeLoadBalancer,
						LoadBalancerIP:           "20.20.20.2",
						PublishNotReadyAddresses: false,
					},
				},
				memberClusterName3: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: nil,
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       8080,
								TargetPort: intstr.FromInt32(8080),
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeLoadBalancer,
						LoadBalancerIP:           "10.10.10.1",
						PublishNotReadyAddresses: false,
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			testOmBuilder := omv1.NewOpsManagerBuilderDefault().
				SetName("test-om").
				SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
				SetAppDBPassword("my-secret", "password").
				SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: true}).
				AddConfiguration("brs.queryable.proxyPort", "1234").
				SetOpsManagerClusterSpecList(tc.clusterSpecList)

			if tc.commonExternalConnectivity != nil {
				testOmBuilder.SetExternalConnectivity(*tc.commonExternalConnectivity)
			}
			testOm := testOmBuilder.Build()

			memberClusters := make([]multicluster.MemberCluster, len(tc.clusterSpecList))
			for clusterIndex, clusterSpecItem := range tc.clusterSpecList {
				fakeClient, _ := mock.NewDefaultFakeClient()
				secretsClient := secrets.SecretClient{
					VaultClient: &vault.VaultClient{},
					KubeClient:  fakeClient,
				}

				memberClusters[clusterIndex] = multicluster.MemberCluster{
					Name:         clusterSpecItem.ClusterName,
					Index:        clusterIndex,
					Replicas:     clusterSpecItem.Members,
					Client:       fakeClient,
					SecretClient: secretsClient,
					Active:       true,
					Healthy:      true,
					Legacy:       false,
				}
			}

			for _, memberCluster := range memberClusters {
				ctx := context.Background()
				sts, err := construct.OpsManagerStatefulSet(ctx, memberCluster.SecretClient, testOm, memberCluster, zap.S())
				assert.NoError(t, err)

				mutatedSts, err := OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
				assert.NoError(t, err)
				assert.NotNil(t, mutatedSts)

				expectedService, ok := tc.expectedServices[memberCluster.Name]
				svc, err := memberCluster.Client.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.ExternalSvcName()))
				if ok {
					assert.NoError(t, err)
					assert.Equal(t, expectedService, svc, "service for cluster %s does not match", memberCluster.Name)
				} else {
					assert.Error(t, err)
				}
			}
		})
	}
}

func TestBackupServiceCreated_NoExternalConnectivity(t *testing.T) {
	ctx := context.Background()
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}

	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	mutatedSts, err := OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
	assert.NoError(t, err)
	assert.NotNil(t, mutatedSts)

	_, err = fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()+"-ext"))
	assert.Error(t, err, "No external service should have been created")

	svc, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP, "Default internal service is of type ClusterIP")
	assert.Equal(t, svc.Spec.ClusterIP, corev1.ClusterIPNone, "Default internal service is a headless service")

	assert.Len(t, svc.Spec.Ports, 2, "Backup port should have been added to existing internal service")

	port0 := svc.Spec.Ports[0]
	assert.Equal(t, internalConnectivityPortName, port0.Name)

	port1 := svc.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)
}

func TestBackupServiceCreated_ExternalConnectivity(t *testing.T) {
	ctx := context.Background()
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetAppDBPassword("my-secret", "password").
		SetBackup(omv1.MongoDBOpsManagerBackup{
			Enabled: true,
		}).AddConfiguration("brs.queryable.proxyPort", "1234").
		SetExternalConnectivity(omv1.MongoDBOpsManagerServiceDefinition{
			Type: corev1.ServiceTypeNodePort,
			Port: 5000,
		}).
		Build()
	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}
	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	mutatedSts, err := OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
	assert.NoError(t, err)
	assert.NotNil(t, mutatedSts)

	externalService, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()+"-ext"))
	assert.NoError(t, err, "An External service should have been created")

	assert.Len(t, externalService.Spec.Ports, 2, "Backup port should have been added to existing external service")

	port0 := externalService.Spec.Ports[0]
	assert.Equal(t, externalConnectivityPortName, port0.Name)
	assert.Equal(t, int32(5000), port0.Port)
	assert.Equal(t, intstr.FromInt(8080), port0.TargetPort)
	assert.Equal(t, int32(5000), port0.NodePort)

	port1 := externalService.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)
}

func TestDatabaseInKubernetes_ExternalServicesWithoutExternalDomain(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
			},
			Type:                     corev1.ServiceTypeLoadBalancer,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	expectedServices := []corev1.Service{service1, service2}

	testDatabaseInKubernetesExternalServices(ctx, t, mdbv1.ExternalAccessConfiguration{}, expectedServices)
}

func TestDatabaseInKubernetes_ExternalServicesWithExternalDomainHaveAdditionalBackupPort(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
				{
					Name:       "backup",
					TargetPort: intstr.IntOrString{IntVal: 27018},
				},
			},
			Type:                     corev1.ServiceTypeLoadBalancer,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	expectedServices := []corev1.Service{service1, service2}

	testDatabaseInKubernetesExternalServices(ctx, t, mdbv1.ExternalAccessConfiguration{ExternalDomain: new("example.com")}, expectedServices)
}

func TestDatabaseInKubernetes_ExternalServicesWithServiceSpecOverrides(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external", Annotations: map[string]string{
			"key": "value",
		}},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
				{
					Name:       "backup",
					TargetPort: intstr.IntOrString{IntVal: 27018},
				},
			},
			Type:                     corev1.ServiceTypeNodePort,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	expectedServices := []corev1.Service{service1, service2}

	externalAccessConfiguration := mdbv1.ExternalAccessConfiguration{
		ExternalDomain: new("example.com"),
		ExternalService: mdbv1.ExternalServiceConfiguration{
			SpecWrapper: &v1.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
			}},
			Annotations: map[string]string{
				"key": "value",
			},
		},
	}
	testDatabaseInKubernetesExternalServices(ctx, t, externalAccessConfiguration, expectedServices)
}

const (
	defaultResourceName = "mdb"
	defaultNamespace    = "my-namespace"
)

func TestDatabaseInKubernetes_ExternalServicesWithPlaceholders(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external", Annotations: map[string]string{
			"key": "value",
		}},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
			},
			Type:                     corev1.ServiceTypeNodePort,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	externalAccessConfiguration := mdbv1.ExternalAccessConfiguration{
		ExternalService: mdbv1.ExternalServiceConfiguration{
			SpecWrapper: &v1.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
			}},
			Annotations: map[string]string{
				PlaceholderPodIndex:            "{podIndex}",
				PlaceholderNamespace:           "{namespace}",
				PlaceholderResourceName:        "{resourceName}",
				PlaceholderPodName:             "{podName}",
				PlaceholderStatefulSetName:     "{statefulSetName}",
				PlaceholderExternalServiceName: "{externalServiceName}",
				PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
				PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
			},
		},
	}

	podIndex := 0
	podName := fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	mongodProcessDomain := fmt.Sprintf("%s-svc.%s.svc.cluster.local", defaultResourceName, defaultNamespace)
	service1.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	podIndex = 1
	podName = fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	service2.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	testDatabaseInKubernetesExternalServices(ctx, t, externalAccessConfiguration, []corev1.Service{service1, service2})
}

func TestDatabaseInKubernetes_ExternalServicesWithPlaceholders_WithExternalDomain(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external", Annotations: map[string]string{
			"key": "value",
		}},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
				{
					Name:       "backup",
					TargetPort: intstr.IntOrString{IntVal: 27018},
				},
			},
			Type:                     corev1.ServiceTypeNodePort,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	externalDomain := "external.domain.example.com"
	externalAccessConfiguration := mdbv1.ExternalAccessConfiguration{
		ExternalDomain: &externalDomain,
		ExternalService: mdbv1.ExternalServiceConfiguration{
			SpecWrapper: &v1.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
			}},
			Annotations: map[string]string{
				PlaceholderPodIndex:            "{podIndex}",
				PlaceholderNamespace:           "{namespace}",
				PlaceholderResourceName:        "{resourceName}",
				PlaceholderPodName:             "{podName}",
				PlaceholderStatefulSetName:     "{statefulSetName}",
				PlaceholderExternalServiceName: "{externalServiceName}",
				PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
				PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
			},
		},
	}

	podIndex := 0
	podName := fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	mongodProcessDomain := externalDomain
	service1.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	podIndex = 1
	podName = fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	service2.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	testDatabaseInKubernetesExternalServices(ctx, t, externalAccessConfiguration, []corev1.Service{service1, service2})
}

func testDatabaseInKubernetesExternalServices(ctx context.Context, t *testing.T, externalAccessConfiguration mdbv1.ExternalAccessConfiguration, expectedServices []corev1.Service) {
	log := zap.S()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName(defaultResourceName).
		SetNamespace(defaultNamespace).
		SetMembers(2).
		Build()
	mdb.Spec.ExternalAccessConfiguration = &externalAccessConfiguration

	sts := construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions(construct.GetPodEnvOptions()), log)
	_, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, construct.ReplicaSetOptions(), log)
	assert.NoError(t, err)

	// we only test a subset of fields from service spec, which are the most relevant for external services
	for _, expectedService := range expectedServices {
		actualService, err := fakeClient.GetService(ctx, types.NamespacedName{Name: expectedService.GetName(), Namespace: defaultNamespace})
		require.NoError(t, err, "serviceName: %s", expectedService.GetName())
		require.NotNil(t, actualService)
		require.Len(t, actualService.Spec.Ports, len(expectedService.Spec.Ports))
		for i, expectedPort := range expectedService.Spec.Ports {
			actualPort := actualService.Spec.Ports[i]
			assert.Equal(t, expectedPort.Name, actualPort.Name)
			assert.Equal(t, expectedPort.TargetPort.IntVal, actualPort.TargetPort.IntVal)
		}
		assert.Equal(t, expectedService.Spec.Type, actualService.Spec.Type)
		assert.True(t, expectedService.Spec.PublishNotReadyAddresses, actualService.Spec.PublishNotReadyAddresses)
		if expectedService.Annotations != nil {
			assert.Equal(t, expectedService.Annotations, actualService.Annotations)
		}
	}

	// disable external access -> remove external services
	mdb.Spec.ExternalAccessConfiguration = nil
	_, err = DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, construct.ReplicaSetOptions(), log)
	assert.NoError(t, err)

	for _, expectedService := range expectedServices {
		_, err := fakeClient.GetService(ctx, types.NamespacedName{Name: expectedService.GetName(), Namespace: defaultNamespace})
		assert.True(t, errors.IsNotFound(err))
	}
}

func TestDatabaseInKubernetesExternalServicesSharded(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewDefaultShardedClusterBuilder().
		SetName("mdb").
		SetNamespace("my-namespace").
		SetMongosCountSpec(2).
		SetShardCountSpec(1).
		SetConfigServerCountSpec(1).
		Build()

	mdb.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}

	createShardSts(ctx, t, mdb, log, fakeClient)

	createMongosSts(ctx, t, mdb, log, fakeClient)

	actualService, err := fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-mongos-0-svc-external", Namespace: "my-namespace"})
	require.NoError(t, err)
	require.NotNil(t, actualService)

	actualService, err = fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-mongos-1-svc-external", Namespace: "my-namespace"})
	require.NoError(t, err)
	require.NotNil(t, actualService)

	_, err = fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-config-0-svc-external", Namespace: "my-namespace"})
	require.Errorf(t, err, "expected no config service")

	_, err = fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-0-svc-external", Namespace: "my-namespace"})
	require.Errorf(t, err, "expected no shard service")
}

// TestDatabaseInKubernetesExternalServicesShardedPerTier covers the single-cluster per-tier
// externalAccess field: config servers and shards only get external services when an external domain
// is configured, which is the rule the multi-cluster path applies too. Without that rule a
// domain-less spec.shard.externalAccess would create LoadBalancer services no hostname points at.
func TestDatabaseInKubernetesExternalServicesShardedPerTier(t *testing.T) {
	ctx := context.Background()
	log := zap.S()

	newShardedCluster := func() *mdbv1.MongoDB {
		return mdbv1.NewDefaultShardedClusterBuilder().
			SetName("mdb").
			SetNamespace("my-namespace").
			SetMongosCountSpec(1).
			SetShardCountSpec(1).
			SetConfigServerCountSpec(1).
			Build()
	}

	// createShardSts leaves opts.Replicas at zero, which would skip the per-pod loop entirely and make
	// every assertion below vacuous, so the shard cases build their own options with one replica.
	createShard0Sts := func(t *testing.T, mdb *mdbv1.MongoDB, kubeClient kubernetesClient.Client) {
		shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, mdb)
		withReplicas := func(options *construct.DatabaseStatefulSetOptions) { options.Replicas = 1 }
		opts := construct.ShardOptions(0, shardSpec, memberCluster.Name, withReplicas)
		sts := construct.DatabaseStatefulSet(*mdb, construct.ShardOptions(0, shardSpec, memberCluster.Name, withReplicas, construct.GetPodEnvOptions()), log)
		mutatedSts, err := DatabaseInKubernetes(ctx, kubeClient, secrets.SecretClient{KubeClient: kubeClient}, *mdb, sts, opts, log)
		require.NoError(t, err)
		require.NotNil(t, mutatedSts)
	}

	t.Run("shard tier without external domain gets no external service", func(t *testing.T) {
		fakeClient, _ := mock.NewDefaultFakeClient()
		mdb := newShardedCluster()
		mdb.Spec.ShardSpec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}

		createShard0Sts(t, mdb, fakeClient)

		_, err := fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-0-0-svc-external", Namespace: "my-namespace"})
		require.Error(t, err, "expected no shard external service without an external domain")
	})

	t.Run("shard tier with external domain gets an external service", func(t *testing.T) {
		fakeClient, _ := mock.NewDefaultFakeClient()
		mdb := newShardedCluster()
		mdb.Spec.ShardSpec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{
			ExternalDomain: new("shards.example.com"),
		}

		createShard0Sts(t, mdb, fakeClient)

		svc, err := fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-0-0-svc-external", Namespace: "my-namespace"})
		require.NoError(t, err)
		assert.Equal(t, corev1.ServiceTypeLoadBalancer, svc.Spec.Type)
	})

	t.Run("mongos tier without external domain still gets an external service", func(t *testing.T) {
		fakeClient, _ := mock.NewDefaultFakeClient()
		mdb := newShardedCluster()
		mongosSpec := mdb.Spec.MongosSpec.DeepCopy()
		mongosSpec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}
		mongosSpec.ClusterSpecList = mdbv1.ClusterSpecList{{ClusterName: multicluster.LegacyCentralClusterName, Members: 1}}
		withReplicas := func(options *construct.DatabaseStatefulSetOptions) { options.Replicas = 1 }

		opts := construct.MongosOptions(mongosSpec, multicluster.LegacyCentralClusterName, withReplicas)
		sts := construct.DatabaseStatefulSet(*mdb, construct.MongosOptions(mongosSpec, multicluster.LegacyCentralClusterName, withReplicas, construct.GetPodEnvOptions()), log)
		_, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, opts, log)
		require.NoError(t, err)

		svc, err := fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-mongos-0-svc-external", Namespace: "my-namespace"})
		require.NoError(t, err)
		assert.Equal(t, corev1.ServiceTypeLoadBalancer, svc.Spec.Type)
	})
}

// TestDatabaseInKubernetesSkipsServicesForMultiClusterSharded pins down that DatabaseInKubernetes
// touches no Services at all for a multi-cluster sharded cluster: reconcileServices owns them there.
// The per-cluster externalAccess carries multi-cluster placeholders ({clusterName}, {clusterIndex})
// that the single-cluster placeholder replacer cannot resolve, so were the per-pod loop below to run
// it would both overwrite reconcileServices' objects and hard-fail the reconciliation.
func TestDatabaseInKubernetesSkipsServicesForMultiClusterSharded(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	fakeClient, _ := mock.NewDefaultFakeClient()

	mdb := mdbv1.NewDefaultShardedClusterBuilder().
		SetName("mdb").
		SetNamespace("my-namespace").
		Build()
	mdb.Spec.Topology = mdbv1.ClusterTopologyMultiCluster
	require.True(t, mdb.Spec.IsMultiCluster())

	configSrvSpec := mdb.Spec.ConfigSrvSpec.DeepCopy()
	configSrvSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: "cluster-0",
			Members:     1,
			ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
				ExternalDomain: new("cfg.example.com"),
				ExternalService: mdbv1.ExternalServiceConfiguration{
					Annotations: map[string]string{
						"cluster": "{clusterName}-{clusterIndex}",
					},
				},
			},
		},
	}

	// The equivalents of the operator package's Replicas/StatefulSetNameOverride/ServiceName option
	// helpers, which this package cannot import (the operator package imports create).
	memberClusterNaming := func(options *construct.DatabaseStatefulSetOptions) {
		options.Replicas = 1
		options.StatefulSetNameOverride = "mdb-config-0"
		options.ServiceName = "mdb-cs-0-svc"
	}

	opts := construct.ConfigServerOptions(configSrvSpec, "cluster-0", memberClusterNaming)
	sts := construct.DatabaseStatefulSet(*mdb, construct.ConfigServerOptions(configSrvSpec, "cluster-0", memberClusterNaming, construct.GetPodEnvOptions()), log)

	// The per-tier configuration must have been resolved onto the options - otherwise this test would
	// pass for the wrong reason.
	require.NotNil(t, opts(*mdb).ExternalAccessConfiguration)

	mutatedSts, err := DatabaseInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, *mdb, sts, opts, log)
	require.NoError(t, err)
	require.NotNil(t, mutatedSts)

	serviceList := &corev1.ServiceList{}
	require.NoError(t, fakeClient.List(ctx, serviceList))
	assert.Empty(t, serviceList.Items, "DatabaseInKubernetes must not create Services for multi-cluster sharded clusters")
}

func createShardSpecAndDefaultCluster(client kubernetesClient.Client, sc *mdbv1.MongoDB) (*mdbv1.ShardedClusterComponentSpec, multicluster.MemberCluster) {
	shardSpec := sc.Spec.ShardSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
			PodSpec:     sc.Spec.PodSpec,
		},
	}

	return shardSpec, multicluster.GetLegacyCentralMemberCluster(sc.Spec.MongodsPerShardCount, 0, client, secrets.SecretClient{KubeClient: client})
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

func createShardSts(ctx context.Context, t *testing.T, mdb *mdbv1.MongoDB, log *zap.SugaredLogger, kubeClient kubernetesClient.Client) {
	shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, mdb)
	sts := construct.DatabaseStatefulSet(*mdb, construct.ShardOptions(1, shardSpec, memberCluster.Name, construct.GetPodEnvOptions()), log)
	mutatedSts, err := DatabaseInKubernetes(ctx, kubeClient, secrets.SecretClient{KubeClient: kubeClient}, *mdb, sts, construct.ShardOptions(1, shardSpec, memberCluster.Name), log)
	assert.NoError(t, err)
	assert.NotNil(t, mutatedSts)
}

func createMongosSts(ctx context.Context, t *testing.T, mdb *mdbv1.MongoDB, log *zap.SugaredLogger, kubeClient kubernetesClient.Client) {
	mongosSpec := createMongosSpec(mdb)
	withReplicas := func(options *construct.DatabaseStatefulSetOptions) {
		options.Replicas = mdb.Spec.MongosCount
	}
	sts := construct.DatabaseStatefulSet(*mdb, construct.MongosOptions(mongosSpec, multicluster.LegacyCentralClusterName, construct.GetPodEnvOptions(), withReplicas), log)
	mutatedSts, err := DatabaseInKubernetes(ctx, kubeClient, secrets.SecretClient{KubeClient: kubeClient}, *mdb, sts, construct.MongosOptions(mongosSpec, multicluster.LegacyCentralClusterName, withReplicas), log)
	assert.NoError(t, err)
	assert.NotNil(t, mutatedSts)
}

func TestResizePVCsStorage(t *testing.T) {
	fakeClient, _ := mock.NewDefaultFakeClient()

	const testStsName = "test"
	const testStsNamespace = "mongodb-test"
	initialSts := createStatefulSet(testStsName, testStsNamespace, "20Gi", "20Gi", "20Gi")

	// Create the StatefulSet that we want to resize the PVC to
	err := fakeClient.CreateStatefulSet(context.TODO(), *initialSts)
	assert.NoError(t, err)

	for _, template := range initialSts.Spec.VolumeClaimTemplates {
		for i := range *initialSts.Spec.Replicas {
			pvc := createPVCFromTemplate(template, initialSts.Name, initialSts.Namespace, i)
			err = fakeClient.Create(context.TODO(), pvc)
			assert.NoError(t, err)
		}
	}

	// PVCs from different STS (same name, but different namespace) should be ignored and not resized
	// Previously, we had not taken into account namespace when listing PVCs https://jira.mongodb.org/browse/HELP-85556
	const otherTestStsNamespace = "mongodb-test-2"
	otherSts := createStatefulSet(testStsName, otherTestStsNamespace, "25Gi", "20Gi", "15Gi")

	// Create the StatefulSet that we want to resize the PVC to
	err = fakeClient.CreateStatefulSet(context.TODO(), *otherSts)
	assert.NoError(t, err)

	for _, template := range otherSts.Spec.VolumeClaimTemplates {
		for i := range *otherSts.Spec.Replicas {
			pvc := createPVCFromTemplate(template, otherSts.Name, otherSts.Namespace, i)
			err = fakeClient.Create(context.TODO(), pvc)
			assert.NoError(t, err)
		}
	}

	// We are resizing only initialSts PVCs here and otherSts PVCs should remain unchanged
	err = resizePVCsStorage(context.TODO(), fakeClient, createStatefulSet(testStsName, testStsNamespace, "30Gi", "30Gi", "20Gi"), zap.S())
	assert.NoError(t, err)

	pvcList := corev1.PersistentVolumeClaimList{}
	err = fakeClient.List(context.TODO(), &pvcList)
	assert.NoError(t, err)

	expectedPVCSizesPerNamespace := map[string]map[string]string{
		// PVCs from the STS namespace that was resized should have the new sizes
		testStsNamespace: {
			"data":    "30Gi",
			"journal": "30Gi",
			"logs":    "20Gi",
		},
		// PVCs from other namespace should remain unchanged
		otherTestStsNamespace: {
			"data":    "25Gi",
			"journal": "20Gi",
			"logs":    "15Gi",
		},
	}

	for _, pvc := range pvcList.Items {
		pvcSizes, ok := expectedPVCSizesPerNamespace[pvc.Namespace]
		if !ok {
			t.Fatalf("unexpected namespace %s for pvc %s", pvc.Namespace, pvc.Name)
		}

		if strings.HasPrefix(pvc.Name, "data") {
			assert.Equal(t, pvc.Spec.Resources.Requests.Storage().String(), pvcSizes["data"])
		} else if strings.HasPrefix(pvc.Name, "journal") {
			assert.Equal(t, pvc.Spec.Resources.Requests.Storage().String(), pvcSizes["journal"])
		} else if strings.HasPrefix(pvc.Name, "logs") {
			assert.Equal(t, pvc.Spec.Resources.Requests.Storage().String(), pvcSizes["logs"])
		} else {
			t.Fatal("no pvc was compared while we should have at least detected and compared one")
		}
	}
}

// Helper function to create a StatefulSet
func createStatefulSet(name, namespace, size1, size2, size3 string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(int32(3)),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(size1),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "journal",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(size2),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "logs",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(size3),
							},
						},
					},
				},
			},
		},
	}
}

func createPVCFromTemplate(pvcTemplate corev1.PersistentVolumeClaim, stsName string, namespace string, ordinal int32) *corev1.PersistentVolumeClaim {
	pvcName := fmt.Sprintf("%s-%s-%d", pvcTemplate.Name, stsName, ordinal)
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
		Spec: pvcTemplate.Spec,
	}
}

func TestResourceStorageHasChanged(t *testing.T) {
	type args struct {
		existingPVC []corev1.PersistentVolumeClaim
		toCreatePVC []corev1.PersistentVolumeClaim
	}
	tests := []struct {
		name string
		args args
		want []pvcResize
	}{
		{
			name: "empty",
			want: nil,
		},
		{
			name: "existing is larger",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
							},
						},
					},
				},
				toCreatePVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
			},
			want: []pvcResize{{resizeIndicator: 1, from: "2Gi", to: "1Gi"}},
		},
		{
			name: "toCreate is larger",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
				toCreatePVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
							},
						},
					},
				},
			},
			want: []pvcResize{{resizeIndicator: -1, from: "1Gi", to: "2Gi"}},
		},
		{
			name: "both are equal",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
				toCreatePVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
			},
			want: []pvcResize{{resizeIndicator: 0, from: "1Gi", to: "1Gi"}},
		},
		{
			name: "none exist",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{},
				toCreatePVC: []corev1.PersistentVolumeClaim{},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, resourceStorageHasChanged(tt.args.existingPVC, tt.args.toCreatePVC), "resourceStorageHasChanged(%v, %v)", tt.args.existingPVC, tt.args.toCreatePVC)
		})
	}
}

func TestHasFinishedResizing(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "mongodb-test",
		},
		Spec: appsv1.StatefulSetSpec{
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("20Gi"),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "logs",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("30Gi"),
							},
						},
					},
				},
			},
		},
	}

	ctx := context.TODO()
	{
		fakeClient, _ := mock.NewDefaultFakeClient()
		// Scenario 1: All PVCs have finished resizing
		pvc1 := createPVCWithCapacity("data-"+sts.Name+"-0", sts.Namespace, "20Gi")
		pvc2 := createPVCWithCapacity("logs-"+sts.Name+"-0", sts.Namespace, "30Gi")
		// PVCs in different namespace, but same name, should be ignored and not taken into account when checking resizing status
		pvc1InDifferentNamespace := createPVCWithCapacity("data-"+sts.Name+"-0", "mongodb-test-2", "15Gi")
		pvc2InDifferentNamespace := createPVCWithCapacity("logs-"+sts.Name+"-0", "mongodb-test-2", "10Gi")
		err := fakeClient.Create(ctx, pvc1)
		assert.NoError(t, err)
		err = fakeClient.Create(ctx, pvc2)
		assert.NoError(t, err)
		err = fakeClient.Create(ctx, pvc1InDifferentNamespace)
		assert.NoError(t, err)
		err = fakeClient.Create(ctx, pvc2InDifferentNamespace)
		assert.NoError(t, err)

		finished, err := hasFinishedResizing(ctx, fakeClient, sts)
		assert.NoError(t, err)
		assert.True(t, finished, "PVCs should be finished resizing")
	}

	{
		// Scenario 2: Some PVCs are still resizing
		fakeClient, _ := mock.NewDefaultFakeClient()
		pvc2Incomplete := createPVCWithCapacity("logs-"+sts.Name+"-0", sts.Namespace, "10Gi")
		err := fakeClient.Create(ctx, pvc2Incomplete)
		assert.NoError(t, err)

		finished, err := hasFinishedResizing(ctx, fakeClient, sts)
		assert.NoError(t, err)
		assert.False(t, finished, "PVCs should not be finished resizing")
	}
}

// Helper function to create a PVC with a specific capacity and status
func createPVCWithCapacity(name string, namespace string, capacity string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(capacity),
				},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse(capacity),
			},
		},
	}
}

func TestGetMatchingPVCTemplateFromSTS(t *testing.T) {
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example-sts",
		},
		Spec: appsv1.StatefulSetSpec{
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data-pvc",
					},
					Spec: corev1.PersistentVolumeClaimSpec{},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "logs-pvc",
					},
					Spec: corev1.PersistentVolumeClaimSpec{},
				},
			},
		},
	}

	tests := []struct {
		name             string
		pvcName          string
		expectedTemplate *corev1.PersistentVolumeClaim
		expectedIndex    int
	}{
		{
			name:    "Matching data-pvc with ordinal 0",
			pvcName: "data-pvc-example-sts-0",
			expectedTemplate: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "data-pvc",
				},
			},
			expectedIndex: 0,
		},
		{
			name:    "Matching logs-pvc with ordinal 1",
			pvcName: "logs-pvc-example-sts-1",
			expectedTemplate: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "logs-pvc",
				},
			},
			expectedIndex: 1,
		},
		{
			name:             "Non-matching PVC name",
			pvcName:          "cache-pvc-example-sts-0",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:    "Matching data-pvc with high ordinal",
			pvcName: "data-pvc-example-sts-1000",
			expectedTemplate: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "data-pvc",
				},
			},
			expectedIndex: 0,
		},
		{
			name:             "PVC name with similar prefix but different StatefulSet name",
			pvcName:          "data-pvc-other-sts-0",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:             "Not matching logs-pvc without ordinal",
			pvcName:          "logs-pvc-example-sts",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:             "Empty PVC name",
			pvcName:          "",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:             "PVC name with extra suffix",
			pvcName:          "data-pvc-example-sts-extra-0",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.pvcName,
				},
			}

			template, index := getMatchingPVCTemplateFromSTS(statefulSet, p)

			if tt.expectedTemplate == nil {
				assert.Nil(t, template, "Expected no matching PVC template")
			} else {
				if assert.NotNil(t, template, "Expected a matching PVC template") {
					assert.Equal(t, tt.expectedTemplate.Name, template.Name, "PVC template name should match")
				}
			}

			assert.Equal(t, tt.expectedIndex, index, "PVC template index should match")
		})
	}
}

func TestAppDBNativeSettingsDelivery(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	for _, architecture := range []architectures.DefaultArchitecture{architectures.Static, architectures.NonStatic} {
		for _, projectID := range []string{"", "project"} {
			t.Run(string(architecture)+"/project="+projectID, func(t *testing.T) {
				ctx := context.Background()
				om := omv1.NewOpsManagerBuilderDefault().Build()
				om.Spec.AppDB.AutomationAgent.StartupParameters = mdbv1.StartupParameters{"logLevel": "DEBUG", "overrideLocalHost": "custom.example.com"}
				podVars := &env.PodEnvVars{ProjectID: projectID, AgentAPIKey: "unused-podvars-key"}
				fakeClient, _ := mock.NewDefaultFakeClient()
				if projectID != "" {
					key := secret.Builder().SetName(projectID+"-group-secret").SetNamespace(om.Namespace).SetField(util.OmAgentApiKey, "mounted-key").Build()
					require.NoError(t, fakeClient.CreateSecret(ctx, key))
				}
				sts, err := construct.AppDbStatefulSet(*om, podVars, construct.AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architecture, zap.S())
				require.NoError(t, err)
				created, err := AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, om, podVars, sts, "appdb", zap.S())
				require.NoError(t, err)
				cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-settings"))
				require.NoError(t, err)
				settings := cm.Data[construct.AgentSettingsFileName]
				assert.Contains(t, settings, "cluster=/var/lib/automation/config/cluster-config.json\n")
				assert.Contains(t, settings, "skipMongoStart=true\n")
				assert.Contains(t, settings, "noDaemonize=true\n")
				assert.Contains(t, settings, "logLevel=DEBUG\n")
				assert.NotContains(t, settings, "mounted-key")
				assert.NotContains(t, settings, "overrideLocalHost")
				agent := container.GetByName(util.AgentContainerName, created.Spec.Template.Spec.Containers)
				assert.Equal(t, []string{"-f", "/etc/mongodb-agent/automation-agent.conf", "-overrideLocalHost=custom.example.com"}, agent.Args)
				assert.NotContains(t, strings.Join(agent.Command, " "), "AGENT_API_KEY")
				assert.Equal(t, projectID != "", strings.Contains(settings, "mmsApiKeyExec="))
				assert.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, created.Spec.UpdateStrategy.Type)
				stored, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
				require.NoError(t, err)
				_, err = AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, om, podVars, sts, "appdb", zap.S())
				require.NoError(t, err)
				repeated, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
				require.NoError(t, err)
				assert.Equal(t, stored.Spec.Template, repeated.Spec.Template)
				assert.Contains(t, agent.VolumeMounts, corev1.VolumeMount{Name: construct.AgentSettingsVolume, MountPath: construct.AgentSettingsPath, ReadOnly: true})
				if projectID != "" {
					key, err := fakeClient.GetSecret(ctx, kube.ObjectKey(om.Namespace, projectID+"-group-secret"))
					require.NoError(t, err)
					key.Data["unrelated"] = []byte("noise")
					require.NoError(t, fakeClient.UpdateSecret(ctx, key))
					unchanged, err := AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, om, podVars, sts, "appdb", zap.S())
					require.NoError(t, err)
					assert.Equal(t, created.Spec.Template.Annotations[construct.AgentSettingsHash], unchanged.Spec.Template.Annotations[construct.AgentSettingsHash])
					key, err = fakeClient.GetSecret(ctx, kube.ObjectKey(om.Namespace, key.Name))
					require.NoError(t, err)
					key.Data[util.OmAgentApiKey] = []byte("rotated-key")
					require.NoError(t, fakeClient.UpdateSecret(ctx, key))
					rotated, err := AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, om, podVars, sts, "appdb", zap.S())
					require.NoError(t, err)
					assert.NotEqual(t, created.Spec.Template.Annotations[construct.AgentSettingsHash], rotated.Spec.Template.Annotations[construct.AgentSettingsHash])
				}
			})
		}
	}
}

func TestAppDBAgentSettingsCredentialSources(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	for _, tt := range []struct {
		name       string
		projectID  string
		parameters mdbv1.StartupParameters
		wantError  bool
		wantRawKey bool
	}{
		{name: "monitoring disabled needs no key"},
		{name: "missing monitoring key blocks apply", projectID: "project", wantError: true},
		{name: "empty command retains default key requirement", projectID: "project", parameters: mdbv1.StartupParameters{"mmsApiKeyExec": ""}, wantError: true},
		{name: "raw key without monitoring", parameters: mdbv1.StartupParameters{"mmsApiKey": "customer-key"}, wantRawKey: true},
		{name: "custom command wins over raw key", projectID: "project", parameters: mdbv1.StartupParameters{"mmsApiKey": "customer-key", "mmsApiKeyExec": "read-custom-credential"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fakeClient, _ := mock.NewDefaultFakeClient()
			om := omv1.NewOpsManagerBuilderDefault().Build()
			om.Spec.AppDB.AutomationAgent.StartupParameters = tt.parameters
			podVars := &env.PodEnvVars{ProjectID: tt.projectID}
			sts, err := construct.AppDbStatefulSet(*om, podVars, construct.AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
			require.NoError(t, err)
			created, err := AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, om, podVars, sts, "appdb", zap.S())
			if tt.wantError {
				require.Error(t, err)
				_, err = fakeClient.GetConfigMap(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-settings"))
				assert.True(t, errors.IsNotFound(err))
				_, err = fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
				assert.True(t, errors.IsNotFound(err))
				return
			}
			require.NoError(t, err)
			key, err := fakeClient.GetSecret(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-api-key"))
			if tt.wantRawKey {
				require.NoError(t, err)
				assert.Equal(t, "customer-key", string(key.Data[util.OmAgentApiKey]))
				agent := container.GetByName(util.AgentContainerName, created.Spec.Template.Spec.Containers)
				assert.Contains(t, agent.VolumeMounts, corev1.VolumeMount{Name: construct.AgentAPIKeyVolumeName, MountPath: construct.AgentAPIKeySecretPath, ReadOnly: true})
				for _, volume := range created.Spec.Template.Spec.Volumes {
					if volume.Name == construct.AgentAPIKeyVolumeName {
						require.NotNil(t, volume.Secret)
						assert.Equal(t, key.Name, volume.Secret.SecretName)
					}
				}
			} else {
				assert.True(t, errors.IsNotFound(err))
			}
			cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-settings"))
			require.NoError(t, err)
			assert.NotContains(t, cm.Data[construct.AgentSettingsFileName], "customer-key")
			for _, agent := range created.Spec.Template.Spec.Containers {
				assert.NotContains(t, strings.Join(append(agent.Command, agent.Args...), " "), "customer-key")
			}
		})
	}
}

func TestAppDBAgentSettingsConfigMapFailurePreservesStatefulSet(t *testing.T) {
	ctx := context.Background()
	fakeClient, _ := mock.NewDefaultFakeClient()
	om := omv1.NewOpsManagerBuilderDefault().Build()
	sts, err := construct.AppDbStatefulSet(*om, nil, construct.AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)
	require.NoError(t, fakeClient.CreateStatefulSet(ctx, sts))
	stored, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
	require.NoError(t, err)
	_, err = AppDBInKubernetes(ctx, agentSettingsWriteFailureClient{Client: fakeClient}, secrets.SecretClient{KubeClient: fakeClient}, om, nil, sts, "appdb", zap.S())
	require.ErrorContains(t, err, "settings ConfigMap write failed")
	after, err := fakeClient.GetStatefulSet(ctx, kube.ObjectKey(sts.Namespace, sts.Name))
	require.NoError(t, err)
	assert.Equal(t, stored, after)
}

func TestAppDBAgentSettingsVaultCredentials(t *testing.T) {
	t.Setenv("SECRET_BACKEND", vault.VaultBackend)
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	for _, rawOverride := range []bool{false, true} {
		t.Run(fmt.Sprintf("rawOverride=%t", rawOverride), func(t *testing.T) {
			ctx := context.Background()
			fakeClient, _ := mock.NewDefaultFakeClient()
			om := omv1.NewOpsManagerBuilderDefault().SetNamespace("appdb-vault-tests").Build()
			podVars := &env.PodEnvVars{ProjectID: "project"}
			keyName := "project-group-secret"
			if rawOverride {
				podVars = nil
				keyName = om.Spec.AppDB.Name() + "-agent-api-key"
				om.Spec.AppDB.AutomationAgent.StartupParameters = mdbv1.StartupParameters{"mmsApiKey": "customer-vault-key"}
			}
			configuration := vault.VaultConfiguration{AppDBSecretPath: "appdb-config-tests", DatabaseSecretPath: "not-appdb"}
			secretPath := "/secret/data/appdb-config-tests/" + om.Namespace + "/" + keyName
			data := map[string]string{util.OmAgentApiKey: "vault-key-one"}
			if rawOverride {
				data = nil
			}
			var dataLock sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1"+secretPath {
					t.Errorf("unexpected credential path %s; expected /v1%s", r.URL.Path, secretPath)
					http.NotFound(w, r)
					return
				}
				dataLock.Lock()
				defer dataLock.Unlock()
				if r.Method == http.MethodGet {
					if data == nil {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data}})
					return
				}
				var payload struct {
					Data map[string]string `json:"data"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					http.Error(w, "invalid payload", http.StatusBadRequest)
					return
				}
				data = payload.Data
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			vaultClient, err := vault.NewVaultClientForTesting(server.URL, "test-token", configuration)
			require.NoError(t, err)
			sts, err := construct.AppDbStatefulSet(*om, podVars, construct.AppDBStatefulSetOptions{VaultConfig: configuration}, scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil), appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
			require.NoError(t, err)
			apply := func() string {
				t.Helper()
				created, err := AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient, VaultClient: vaultClient}, om, podVars, sts, "appdb", zap.S())
				require.NoError(t, err)
				assert.Equal(t, secretPath, created.Spec.Template.Annotations["vault.hashicorp.com/agent-inject-secret-agentApiKey"])
				assert.Contains(t, created.Spec.Template.Annotations["vault.hashicorp.com/agent-inject-template-agentApiKey"], secretPath)
				assert.Equal(t, construct.AgentAPIKeySecretPath, created.Spec.Template.Annotations["vault.hashicorp.com/secret-volume-path-agentApiKey"])
				return created.Spec.Template.Annotations[construct.AgentSettingsHash]
			}
			original := apply()
			dataLock.Lock()
			if rawOverride {
				assert.Equal(t, "customer-vault-key", data[util.OmAgentApiKey])
			}
			data["unrelated"] = "noise"
			dataLock.Unlock()
			assert.Equal(t, original, apply())
			if !rawOverride {
				dataLock.Lock()
				data[util.OmAgentApiKey] = "vault-key-two"
				dataLock.Unlock()
				assert.NotEqual(t, original, apply())
			}
			_, err = fakeClient.GetSecret(ctx, kube.ObjectKey(sts.Namespace, keyName))
			assert.True(t, errors.IsNotFound(err))
			cm, err := fakeClient.GetConfigMap(ctx, kube.ObjectKey(sts.Namespace, sts.Name+"-agent-settings"))
			require.NoError(t, err)
			assert.NotContains(t, cm.Data[construct.AgentSettingsFileName], "customer-vault-key")
		})
	}
}

// TestAppDBInKubernetes_ServiceOwnerReference verifies that the headless service created by
// AppDBInKubernetes has no ownerReference in multi-cluster mode.
// In multi-cluster mode the MongoDBOpsManager CR only exists in the central cluster.
// A cross-cluster ownerReference causes the Kubernetes garbage collector in remote member
// clusters to delete the service as an orphan, preventing the AppDB from ever becoming ready.
func TestAppDBInKubernetes_ServiceOwnerReference(t *testing.T) {
	ctx := context.Background()

	minimalSts := func(namespace, serviceName string) appsv1.StatefulSet {
		return appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "appdb-0",
				Namespace: namespace,
			},
			Spec: appsv1.StatefulSetSpec{
				ServiceName: serviceName,
				Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "appdb"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "appdb"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "mongodb-agent", Image: "agent:latest"}}},
				},
			},
		}
	}

	t.Run("multi-cluster mode produces no ownerReferences on the service", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			SetName("test-om").
			SetAppDBTopology(omv1.ClusterTopologyMultiCluster).
			Build()

		fakeClient, _ := mock.NewDefaultFakeClient()
		sts := minimalSts(om.Namespace, "appdb-svc")
		_, err := AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, om, nil, sts, "appdb-0", zap.S())
		assert.NoError(t, err)

		svc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, "appdb-svc"))
		assert.NoError(t, err)
		assert.Empty(t, svc.OwnerReferences,
			"headless service in a remote member cluster must not carry an ownerReference pointing to the MongoDBOpsManager CR")
	})

	t.Run("single-cluster mode produces an ownerReference on the service", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			SetName("test-om").
			Build()

		fakeClient, _ := mock.NewDefaultFakeClient()
		sts := minimalSts(om.Namespace, "appdb-svc")
		_, err := AppDBInKubernetes(ctx, fakeClient, secrets.SecretClient{KubeClient: fakeClient}, om, nil, sts, "appdb-0", zap.S())
		assert.NoError(t, err)

		svc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, "appdb-svc"))
		assert.NoError(t, err)
		assert.Len(t, svc.OwnerReferences, 1,
			"headless service in single-cluster mode must carry an ownerReference so Kubernetes GC can clean it up")
	})
}

// TestBackupDaemonInKubernetes_ServiceOwnerReference verifies that the service created by
// BackupDaemonInKubernetes has no ownerReference in multi-cluster mode.
// In multi-cluster mode the MongoDBOpsManager CR only exists in the central cluster.
// A cross-cluster ownerReference causes the Kubernetes garbage collector in remote member
// clusters to delete the service as an orphan, preventing the backup daemon from starting.
func TestBackupDaemonInKubernetes_ServiceOwnerReference(t *testing.T) {
	ctx := context.Background()

	minimalSts := func(namespace, serviceName string) appsv1.StatefulSet {
		return appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "backup-daemon-0",
				Namespace: namespace,
			},
			Spec: appsv1.StatefulSetSpec{
				ServiceName: serviceName,
				Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "backup-daemon"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "backup-daemon"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "mongodb-backup-daemon", Image: "agent:latest"}}},
				},
			},
		}
	}

	t.Run("multi-cluster mode produces no ownerReferences on the service", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			SetName("test-om").
			SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
			Build()

		fakeClient, _ := mock.NewDefaultFakeClient()
		sts := minimalSts(om.Namespace, "backup-daemon-svc")
		_, err := BackupDaemonInKubernetes(ctx, fakeClient, om, sts, zap.S())
		assert.NoError(t, err)

		svc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, "backup-daemon-svc"))
		assert.NoError(t, err)
		assert.Empty(t, svc.OwnerReferences,
			"backup daemon service in a remote member cluster must not carry an ownerReference pointing to the MongoDBOpsManager CR")
	})

	t.Run("single-cluster mode produces an ownerReference on the service", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			SetName("test-om").
			Build()

		fakeClient, _ := mock.NewDefaultFakeClient()
		sts := minimalSts(om.Namespace, "backup-daemon-svc")
		_, err := BackupDaemonInKubernetes(ctx, fakeClient, om, sts, zap.S())
		assert.NoError(t, err)

		svc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, "backup-daemon-svc"))
		assert.NoError(t, err)
		assert.Len(t, svc.OwnerReferences, 1,
			"backup daemon service in single-cluster mode must carry an ownerReference so Kubernetes GC can clean it up")
	})
}

// TestOpsManagerInKubernetes_ServiceOwnerReference verifies that the internal and external
// services created by OpsManagerInKubernetes have no ownerReference in multi-cluster mode.
// In multi-cluster mode the MongoDBOpsManager CR only exists in the central cluster.
// A cross-cluster ownerReference causes the Kubernetes garbage collector in remote member
// clusters to delete those services as orphans.
func TestOpsManagerInKubernetes_ServiceOwnerReference(t *testing.T) {
	ctx := context.Background()

	t.Run("multi-cluster mode produces no ownerReferences on internal and external services", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			SetName("test-om").
			SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
			SetExternalConnectivity(omv1.MongoDBOpsManagerServiceDefinition{
				Type: corev1.ServiceTypeLoadBalancer,
			}).
			SetAppDBPassword("my-secret", "password").
			Build()

		fakeClient, _ := mock.NewDefaultFakeClient()
		secretsClient := secrets.SecretClient{VaultClient: &vault.VaultClient{}, KubeClient: fakeClient}
		memberCluster := multicluster.GetLegacyCentralMemberCluster(om.Spec.Replicas, 0, fakeClient, secretsClient)

		sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, om, memberCluster, zap.S())
		assert.NoError(t, err)

		_, err = OpsManagerInKubernetes(ctx, memberCluster, om, sts, zap.S())
		assert.NoError(t, err)

		internalSvc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, om.SvcName()))
		assert.NoError(t, err)
		assert.Empty(t, internalSvc.OwnerReferences,
			"internal service in a remote member cluster must not carry an ownerReference pointing to the MongoDBOpsManager CR")

		externalSvc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, om.ExternalSvcName()))
		assert.NoError(t, err)
		assert.Empty(t, externalSvc.OwnerReferences,
			"external service in a remote member cluster must not carry an ownerReference pointing to the MongoDBOpsManager CR")
	})

	t.Run("single-cluster mode produces ownerReferences on internal and external services", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			SetName("test-om").
			SetExternalConnectivity(omv1.MongoDBOpsManagerServiceDefinition{
				Type: corev1.ServiceTypeLoadBalancer,
			}).
			SetAppDBPassword("my-secret", "password").
			Build()

		fakeClient, _ := mock.NewDefaultFakeClient()
		secretsClient := secrets.SecretClient{VaultClient: &vault.VaultClient{}, KubeClient: fakeClient}
		memberCluster := multicluster.GetLegacyCentralMemberCluster(om.Spec.Replicas, 0, fakeClient, secretsClient)

		sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, om, memberCluster, zap.S())
		assert.NoError(t, err)

		_, err = OpsManagerInKubernetes(ctx, memberCluster, om, sts, zap.S())
		assert.NoError(t, err)

		internalSvc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, om.SvcName()))
		assert.NoError(t, err)
		assert.Len(t, internalSvc.OwnerReferences, 1,
			"internal service in single-cluster mode must carry an ownerReference so Kubernetes GC can clean it up")

		externalSvc, err := fakeClient.GetService(ctx, kube.ObjectKey(om.Namespace, om.ExternalSvcName()))
		assert.NoError(t, err)
		assert.Len(t, externalSvc.OwnerReferences, 1,
			"external service in single-cluster mode must carry an ownerReference so Kubernetes GC can clean it up")
	})
}

func TestCheckStatefulsetIsDeleted(t *testing.T) {
	ctx := context.TODO()
	sleepDuration := 100 * time.Millisecond
	log := zap.NewNop().Sugar()

	namespace := "default"
	stsName := "test-sts"
	desiredSts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsName,
			Namespace: namespace,
		},
		Spec: appsv1.StatefulSetSpec{Replicas: new(int32(3))},
	}

	t.Run("StatefulSet is deleted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fakeClient, _ := mock.NewDefaultFakeClient()
			err := fakeClient.CreateStatefulSet(ctx, *desiredSts)
			assert.NoError(t, err)

			// Delete before calling the function
			err = fakeClient.DeleteStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
			assert.NoError(t, err)

			result := checkStatefulsetIsDeleted(ctx, fakeClient, desiredSts, sleepDuration, log)

			assert.True(t, result, "StatefulSet should be detected as deleted")
		})
	})

	t.Run("StatefulSet is not deleted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fakeClient, _ := mock.NewDefaultFakeClient()
			err := fakeClient.CreateStatefulSet(ctx, *desiredSts)
			assert.NoError(t, err)

			// Don't delete - should return false after exhausting retries
			result := checkStatefulsetIsDeleted(ctx, fakeClient, desiredSts, sleepDuration, log)

			assert.False(t, result, "StatefulSet should not be detected as deleted")
		})
	})

	t.Run("StatefulSet is deleted after some retries", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fakeClient, _ := mock.NewDefaultFakeClient()
			err := fakeClient.CreateStatefulSet(ctx, *desiredSts)
			assert.NoError(t, err)

			// Delete after 150ms - will be found on second retry (after 200ms of sleeps)
			go func() {
				time.Sleep(150 * time.Millisecond)
				_ = fakeClient.DeleteStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
			}()

			result := checkStatefulsetIsDeleted(ctx, fakeClient, desiredSts, sleepDuration, log)

			assert.True(t, result, "StatefulSet should be detected as deleted")
		})
	})
}

func TestPreserveExistingVolumeClaimTemplateMetadata(t *testing.T) {
	ctx := context.Background()

	newSts := func(claimLabels map[string]string) appsv1.StatefulSet {
		return appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "om-db", Namespace: mock.TestNamespace},
			Spec: appsv1.StatefulSetSpec{
				Replicas: new(int32(1)),
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{ObjectMeta: metav1.ObjectMeta{Name: "data", Labels: claimLabels}},
					{ObjectMeta: metav1.ObjectMeta{Name: "logs"}},
				},
			},
		}
	}

	t.Run("statefulset does not exist yet, desired claims are left untouched", func(t *testing.T) {
		fakeClient, _ := mock.NewDefaultFakeClient()
		desiredSts := newSts(nil)

		require.NoError(t, preserveExistingVolumeClaimTemplateMetadata(ctx, fakeClient, &desiredSts, zap.S()))
		assert.Nil(t, desiredSts.Spec.VolumeClaimTemplates[0].Labels)
	})

	t.Run("labels propagated by an older operator version are kept", func(t *testing.T) {
		existingSts := newSts(map[string]string{"label1": "val1"})
		fakeClient, _ := mock.NewDefaultFakeClient(&existingSts)

		// The desired statefulset no longer propagates the resource labels onto the claims.
		desiredSts := newSts(nil)

		require.NoError(t, preserveExistingVolumeClaimTemplateMetadata(ctx, fakeClient, &desiredSts, zap.S()))
		assert.Equal(t, map[string]string{"label1": "val1"}, desiredSts.Spec.VolumeClaimTemplates[0].Labels)
		assert.Nil(t, desiredSts.Spec.VolumeClaimTemplates[1].Labels)

		// The statefulset can now be updated, which is exactly what a label change used to break.
		_, err := enterprisests.CreateOrUpdateStatefulset(ctx, fakeClient, desiredSts.Namespace, zap.S(), &desiredSts)
		assert.NoError(t, err)
	})

	t.Run("diverging desired labels are dropped in favour of the existing ones", func(t *testing.T) {
		existingSts := newSts(map[string]string{"label1": "val1"})
		fakeClient, _ := mock.NewDefaultFakeClient(&existingSts)

		desiredSts := newSts(map[string]string{"label1": "changed", "label2": "val2"})

		require.NoError(t, preserveExistingVolumeClaimTemplateMetadata(ctx, fakeClient, &desiredSts, zap.S()))
		assert.Equal(t, map[string]string{"label1": "val1"}, desiredSts.Spec.VolumeClaimTemplates[0].Labels)
	})
}

func TestDroppedMetadataEntries(t *testing.T) {
	tests := map[string]struct {
		desired  map[string]string
		existing map[string]string
		expected map[string]string
	}{
		"labels propagated by an older operator version are not reported": {
			desired:  nil,
			existing: map[string]string{"label1": "val1"},
			expected: map[string]string{},
		},
		"already applied entries are not reported": {
			desired:  map[string]string{"label1": "val1"},
			existing: map[string]string{"label1": "val1"},
			expected: map[string]string{},
		},
		"entries dropped from an override are not reported": {
			desired:  map[string]string{"label1": "val1"},
			existing: map[string]string{"label1": "val1", "label2": "val2"},
			expected: map[string]string{},
		},
		"added and changed entries are reported": {
			desired:  map[string]string{"label1": "changed", "label2": "val2"},
			existing: map[string]string{"label1": "val1"},
			expected: map[string]string{"label1": "changed", "label2": "val2"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, droppedMetadataEntries(tc.desired, tc.existing))
		})
	}
}
