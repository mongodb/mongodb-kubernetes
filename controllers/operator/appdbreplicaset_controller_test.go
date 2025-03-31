package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connectionstring"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	mdbcv1 "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/agentVersionManagement"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

func init() {
	mock.InitDefaultEnvVariables()
}

// getReleaseJsonPath searches for a specified target directory by traversing the directory tree backwards from the current working directory
func getReleaseJsonPath() (string, error) {
	repositoryRootDirName := "ops-manager-kubernetes"
	releaseFileName := "release.json"

	currentDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for currentDir != "/" {
		if strings.HasSuffix(currentDir, repositoryRootDirName) {
			return filepath.Join(currentDir, releaseFileName), nil
		}
		currentDir = filepath.Dir(currentDir)
	}
	return currentDir, err
}

// This approach ensures all test methods in this file have properly defined variables.
func TestMain(m *testing.M) {
	path, _ := getReleaseJsonPath()
	_ = os.Setenv(agentVersionManagement.MappingFilePathEnv, path) // nolint:forbidigo
	defer func(key string) {
		_ = os.Unsetenv(key) // nolint:forbidigo
	}(agentVersionManagement.MappingFilePathEnv)
	code := m.Run()
	os.Exit(code)
}

func TestMongoDB_ConnectionURL_DefaultCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	appdb := opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Special symbols in the url
	cnx = appdb.BuildConnectionURL("special/user#", "@passw!", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://special%2Fuser%23:%40passw%21@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		cnx)
}

func TestMongoDB_ConnectionURL_OtherCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().SetClusterDomain("my-cluster").Build()
	appdb := opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		cnx)
}

// TestAutomationConfig_IsCreatedInSecret verifies that the automation config is created in a secret.
func TestAutomationConfig_IsCreatedInSecret(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	err = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	assert.NoError(t, err)
	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	s := corev1.Secret{}
	err = kubeClient.Get(ctx, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()), &s)
	assert.NoError(t, err, "The Automation Config was created in a secret.")
	assert.Contains(t, s.Data, automationconfig.ConfigKey)
}

// TestPublishAutomationConfigCreate verifies that the automation config map is created if it doesn't exist
func TestPublishAutomationConfigCreate(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	automationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, automation, zap.S())
	assert.NoError(t, err)
	version, err := reconciler.publishAutomationConfig(ctx, opsManager, automationConfig, appdb.AutomationConfigSecretName(), multicluster.LegacyCentralClusterName)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	monitoringAutomationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, monitoring, zap.S())
	assert.NoError(t, err)
	version, err = reconciler.publishAutomationConfig(ctx, opsManager, monitoringAutomationConfig, appdb.MonitoringAutomationConfigSecretName(), multicluster.LegacyCentralClusterName)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	// verify the automation config secret for the automation agent
	acSecret := readAutomationConfigSecret(ctx, t, kubeClient, opsManager)
	checkDeploymentEqualToPublished(t, automationConfig, acSecret)

	// verify the automation config secret for the monitoring agent
	acMonitoringSecret := readAutomationConfigMonitoringSecret(ctx, t, kubeClient, opsManager)
	checkDeploymentEqualToPublished(t, monitoringAutomationConfig, acMonitoringSecret)

	assert.Len(t, mock.GetMapForObject(kubeClient, &corev1.Secret{}), 6)
	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.GetOpsManagerUserPasswordSecretName()))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.GetAgentKeyfileSecretNamespacedName().Name))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.GetAgentPasswordSecretNamespacedName().Name))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.OpsManagerUserScramCredentialsName()))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.MonitoringAutomationConfigSecretName()))
	assert.NoError(t, err)

	// verifies Users and Roles are created
	assert.Len(t, automationConfig.Auth.Users, 1)

	expectedRoles := []string{"readWriteAnyDatabase", "dbAdminAnyDatabase", "clusterMonitor", "backup", "restore", "hostManager"}
	assert.Len(t, automationConfig.Auth.Users[0].Roles, len(expectedRoles))
	for idx, role := range expectedRoles {
		assert.Equal(t, automationConfig.Auth.Users[0].Roles[idx],
			automationconfig.Role{
				Role:     role,
				Database: "admin",
			})
	}
}

// TestPublishAutomationConfig_Update verifies that the automation config map is updated if it has changed
func TestPublishAutomationConfig_Update(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// create
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	ac, err := automationconfig.ReadFromSecret(ctx, reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 1, ac.Version)

	// publishing the config without updates should not result in API call
	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	ac, err = automationconfig.ReadFromSecret(ctx, reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 1, ac.Version)

	// publishing changed config will result in update
	fcv := "4.4"
	err = reconciler.client.Get(ctx, kube.ObjectKeyFromApiObject(opsManager), opsManager)
	require.NoError(t, err)

	opsManager.Spec.AppDB.FeatureCompatibilityVersion = &fcv
	err = kubeClient.Update(ctx, opsManager)
	assert.NoError(t, err)

	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	ac, err = automationconfig.ReadFromSecret(ctx, reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 2, ac.Version)
}

// TestBuildAppDbAutomationConfig checks that the automation config is built correctly
func TestBuildAppDbAutomationConfig(t *testing.T) {
	ctx := context.Background()
	logRotateConfig := &automationconfig.CrdLogRotate{
		SizeThresholdMB: "1",
	}
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.2.11-ent").
		SetAppDbFeatureCompatibility("4.0").
		SetLogRotate(logRotateConfig).
		SetSystemLog(&automationconfig.SystemLog{
			Destination: automationconfig.File,
			Path:        "/tmp/test",
		})

	om := builder.Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(om)
	err := createOpsManagerUserPasswordSecret(ctx, kubeClient, om, "omPass")
	assert.NoError(t, err)

	automationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, automation, zap.S())
	assert.NoError(t, err)
	monitoringAutomationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, monitoring, zap.S())
	assert.NoError(t, err)
	// processes
	assert.Len(t, automationConfig.Processes, 2)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[0].Version)
	assert.Equal(t, "test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[0].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[0].FeatureCompatibilityVersion)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[1].Version)
	assert.Equal(t, "test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[1].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[1].FeatureCompatibilityVersion)
	assert.Len(t, monitoringAutomationConfig.Processes, 0)
	assert.Len(t, monitoringAutomationConfig.ReplicaSets, 0)
	assert.Equal(t, automationconfig.ConvertCrdLogRotateToAC(logRotateConfig), automationConfig.Processes[0].LogRotate)
	assert.Equal(t, "/tmp/test", automationConfig.Processes[0].Args26.Get("systemLog.path").String())
	assert.Equal(t, "file", automationConfig.Processes[0].Args26.Get("systemLog.destination").String())

	// replicasets
	assert.Len(t, automationConfig.ReplicaSets, 1)
	db := builder.Build().Spec.AppDB
	assert.Equal(t, db.Name(), automationConfig.ReplicaSets[0].Id)

	// monitoring agent has been configured
	assert.Len(t, automationConfig.MonitoringVersions, 0)

	// backup agents have not been configured
	assert.Len(t, automationConfig.BackupVersions, 0)

	// options
	assert.Equal(t, automationconfig.Options{DownloadBase: util.AgentDownloadsDir}, automationConfig.Options)
}

func TestRegisterAppDBHostsWithProject(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	omConnectionFactory := om.NewCachedOMConnectionFactoryWithInitializedConnection(om.NewMockedOmConnection(om.NewDeployment()))
	fakeClient := mock.NewEmptyFakeClientBuilder().Build()
	fakeClient = interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, false)(ctx, client, key, obj, opts...)
		},
	})
	reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	t.Run("Ensure all hosts are added", func(t *testing.T) {
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetHosts()
		assert.Len(t, hosts.Results, 3)
	})

	t.Run("Ensure hosts are added when scaled up", func(t *testing.T) {
		opsManager.Spec.AppDB.Members = 5
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := omConnectionFactory.GetConnection().GetHosts()
		assert.Len(t, hosts.Results, 5)
	})
}

func TestEnsureAppDbAgentApiKey(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	// we need to pre-initialize connection as we don't call full reconciler in this test and connection is never created by calling connection factory func
	omConnectionFactory := om.NewCachedOMConnectionFactoryWithInitializedConnection(om.NewMockedOmConnection(om.NewDeployment()))
	fakeClient := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
	reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	omConnectionFactory.GetConnection().(*om.MockedOmConnection).AgentAPIKey = "my-api-key"
	err = reconciler.ensureAppDbAgentApiKey(ctx, opsManager, omConnectionFactory.GetConnection(), omConnectionFactory.GetConnection().GroupID(), zap.S())
	assert.NoError(t, err)

	secretName := agents.ApiKeySecretName(omConnectionFactory.GetConnection().GroupID())
	apiKey, err := secret.ReadKey(ctx, reconciler.client, util.OmAgentApiKey, kube.ObjectKey(opsManager.Namespace, secretName))
	assert.NoError(t, err)
	assert.Equal(t, "my-api-key", apiKey)
}

func TestTryConfigureMonitoringInOpsManager(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	appdbScaler := scalers.GetAppDBScaler(opsManager, multicluster.LegacyCentralClusterName, 0, nil)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// attempt configuring monitoring when there is no api key secret
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", zap.S())
	assert.NoError(t, err)

	assert.Empty(t, podVars.ProjectID)
	assert.Empty(t, podVars.User)

	opsManager.Spec.AppDB.Members = 5
	appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, v1.OnDeleteStatefulSetStrategyType, zap.S())
	assert.NoError(t, err)

	assert.Nil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))

	_ = kubeClient.Update(ctx, &appDbSts)

	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	// once the secret exists, monitoring should be fully configured
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", zap.S())
	assert.NoError(t, err)

	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "publicApiKey", podVars.User)

	hosts, _ := omConnectionFactory.GetConnection().GetHosts()
	assert.Len(t, hosts.Results, 5, "the AppDB hosts should have been added")

	appDbSts, err = construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, v1.OnDeleteStatefulSetStrategyType, zap.S())
	assert.NoError(t, err)

	assert.NotNil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))
}

// TestTryConfigureMonitoringInOpsManagerWithCustomTemplate runs different scenarios with activating monitoring and pod templates
func TestTryConfigureMonitoringInOpsManagerWithCustomTemplate(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdbScaler := scalers.GetAppDBScaler(opsManager, multicluster.LegacyCentralClusterName, 0, nil)

	opsManager.Spec.AppDB.PodSpec.PodTemplateWrapper = common.PodTemplateSpecWrapper{
		PodTemplate: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "mongodb-agent",
						Image: "quay.io/mongodb/mongodb-agent-ubi:10",
					},
					{
						Name:  "mongod",
						Image: "quay.io/mongodb/mongodb:10",
					},
					{
						Name:  "mongodb-agent-monitoring",
						Image: "quay.io/mongodb/mongodb-agent-ubi:20",
					},
				},
			},
		},
	}

	t.Run("do not override images while activating monitoring", func(t *testing.T) {
		podVars := env.PodEnvVars{ProjectID: "something"}
		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, v1.OnDeleteStatefulSetStrategyType, zap.S())
		assert.NoError(t, err)
		assert.NotNil(t, appDbSts)

		foundImages := 0
		for _, c := range appDbSts.Spec.Template.Spec.Containers {
			if c.Name == "mongodb-agent" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent-ubi:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongod" {
				assert.Equal(t, "quay.io/mongodb/mongodb:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongodb-agent-monitoring" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent-ubi:20", c.Image)
				foundImages += 1
			}
		}

		assert.Equal(t, 3, foundImages)
		assert.Equal(t, 3, len(appDbSts.Spec.Template.Spec.Containers))
	})

	t.Run("do not override images, but remove monitoring if not activated", func(t *testing.T) {
		podVars := env.PodEnvVars{}
		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, v1.OnDeleteStatefulSetStrategyType, zap.S())
		assert.NoError(t, err)
		assert.NotNil(t, appDbSts)

		foundImages := 0
		for _, c := range appDbSts.Spec.Template.Spec.Containers {
			if c.Name == "mongodb-agent" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent-ubi:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongod" {
				assert.Equal(t, "quay.io/mongodb/mongodb:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongodb-agent-monitoring" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent-ubi:20", c.Image)
				foundImages += 1
			}
		}

		assert.Equal(t, 2, foundImages)
		assert.Equal(t, 2, len(appDbSts.Spec.Template.Spec.Containers))
	})
}

func findVolumeByName(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := 0; i < len(volumes); i++ {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}

	return nil
}

func TestAppDBScaleUp_HappensIncrementally(t *testing.T) {
	ctx := context.Background()
	performAppDBScalingTest(ctx, t, 1, 5)
}

func TestAppDBScaleDown_HappensIncrementally(t *testing.T) {
	ctx := context.Background()
	performAppDBScalingTest(ctx, t, 5, 1)
}

func TestAppDBScaleUp_HappensIncrementally_FullOpsManagerReconcile(t *testing.T) {
	ctx := context.Background()

	opsManager := DefaultOpsManagerBuilder().
		SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).
		SetAppDbMembers(1).
		SetVersion("7.0.0").
		Build()
	omConnectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	omReconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, nil, omConnectionFactory)

	checkOMReconciliationSuccessful(ctx, t, omReconciler, opsManager, client)

	err := client.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	opsManager.Spec.AppDB.Members = 3

	err = client.Update(ctx, opsManager)
	assert.NoError(t, err)

	checkOMReconciliationPending(ctx, t, omReconciler, opsManager)

	err = client.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	assert.Equal(t, 2, opsManager.Status.AppDbStatus.Members)

	res, err := omReconciler.Reconcile(ctx, requestFromObject(opsManager))
	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)

	err = client.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	assert.Equal(t, 3, opsManager.Status.AppDbStatus.Members)
}

func TestAppDbPortIsConfigurable_WithAdditionalMongoConfig(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().
		SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).
		SetAppDbMembers(1).
		SetAdditionalMongodbConfig(mdb.NewAdditionalMongodConfig("net.port", 30000)).
		Build()
	omConnectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	omReconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, nil, omConnectionFactory)

	checkOMReconciliationSuccessful(ctx, t, omReconciler, opsManager, client)

	appdbSvc, err := client.GetService(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), appdbSvc.Spec.Ports[0].Port)
}

func TestAppDBSkipsReconciliation_IfAnyProcessesAreDisabled(t *testing.T) {
	ctx := context.Background()
	createReconcilerWithAllRequiredSecrets := func(opsManager *omv1.MongoDBOpsManager, createAutomationConfig bool) *ReconcileAppDbReplicaSet {
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
		err := createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")
		assert.NoError(t, err)
		reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		reconciler.client = kubeClient

		// create a pre-existing automation config based on the resource provided.
		// if the automation is not there, we will always want to reconcile. Otherwise, we may not reconcile
		// based on whether or not there are disabled processes.
		if createAutomationConfig {
			ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, automation, UnusedPrometheusConfiguration, multicluster.LegacyCentralClusterName, zap.S())
			assert.NoError(t, err)
			_, err = reconciler.publishAutomationConfig(ctx, opsManager, ac, opsManager.Spec.AppDB.AutomationConfigSecretName(), multicluster.LegacyCentralClusterName)
			assert.NoError(t, err)
		}
		return reconciler
	}

	t.Run("Reconciliation should happen if we are disabling a process", func(t *testing.T) {
		// In this test, we create an OM + automation config (with no disabled processes),
		// then update OM to have a disabled processes, and we assert that reconciliation should take place.

		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, true)

		opsManager = DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(mdbcv1.AutomationConfigOverride{
			Processes: []mdbcv1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.True(t, shouldReconcile)
	})

	t.Run("Reconciliation should not happen if a process is disabled", func(t *testing.T) {
		// In this test, we create an OM with a disabled process, and assert that a reconciliation
		// should not take place (since we are not changing a process back from disabled).

		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(mdbcv1.AutomationConfigOverride{
			Processes: []mdbcv1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, true)

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.False(t, shouldReconcile)
	})

	t.Run("Reconciliation should happen if no automation config is present", func(t *testing.T) {
		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(mdbcv1.AutomationConfigOverride{
			Processes: []mdbcv1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, false)

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.True(t, shouldReconcile)
	})

	t.Run("Reconciliation should happen we are re-enabling a process", func(t *testing.T) {
		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(mdbcv1.AutomationConfigOverride{
			Processes: []mdbcv1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, true)

		opsManager = DefaultOpsManagerBuilder().SetName(omName).Build()

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.True(t, shouldReconcile)
	})
}

// appDBStatefulSetLabelsAndServiceName returns extra fields that we have to manually set to the AppDB statefulset
// since we manually create it. Otherwise, the tests will fail as we try to update parts of the sts that we are not
// allowed to change
func appDBStatefulSetLabelsAndServiceName(omResourceName string) (map[string]string, string) {
	appDbName := fmt.Sprintf("%s-db", omResourceName)
	serviceName := fmt.Sprintf("%s-svc", appDbName)
	labels := map[string]string{"app": serviceName, "controller": "mongodb-enterprise-operator", "pod-anti-affinity": appDbName}
	return labels, serviceName
}

func appDBStatefulSetVolumeClaimTemplates() []corev1.PersistentVolumeClaim {
	resData, _ := resource.ParseQuantity("16G")
	return []corev1.PersistentVolumeClaim{
		{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
				Resources: corev1.VolumeResourceRequirements{
					Requests: map[corev1.ResourceName]resource.Quantity{"storage": resData},
				},
			},
		},
	}
}

func performAppDBScalingTest(ctx context.Context, t *testing.T, startingMembers, finalMembers int) {
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(startingMembers)
	opsManager := builder.Build()
	fakeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler := createRunningAppDB(ctx, t, startingMembers, fakeClient, opsManager, omConnectionFactory)

	// Scale the AppDB
	opsManager.Spec.AppDB.Members = finalMembers

	if startingMembers < finalMembers {
		for i := startingMembers; i < finalMembers-1; i++ {
			err := fakeClient.Update(ctx, opsManager)
			assert.NoError(t, err)

			res, err := reconciler.ReconcileAppDB(ctx, opsManager)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	} else {
		for i := startingMembers; i > finalMembers+1; i-- {
			err := fakeClient.Update(ctx, opsManager)
			assert.NoError(t, err)

			res, err := reconciler.ReconcileAppDB(ctx, opsManager)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	}

	res, err := reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)

	err = fakeClient.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	assert.Equal(t, finalMembers, opsManager.Status.AppDbStatus.Members)
}

func buildAutomationConfigForAppDb(ctx context.Context, builder *omv1.OpsManagerBuilder, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, acType agentType, log *zap.SugaredLogger) (automationconfig.AutomationConfig, error) {
	opsManager := builder.Build()

	// Ensure the password exists for the Ops Manager User. The Ops Manager controller will have ensured this.
	// We are ignoring this err on purpose since the secret might already exist.
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactoryFunc, zap.S())
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}
	return reconciler.buildAppDbAutomationConfig(ctx, opsManager, acType, UnusedPrometheusConfiguration, multicluster.LegacyCentralClusterName, zap.S())
}

func checkDeploymentEqualToPublished(t *testing.T, expected automationconfig.AutomationConfig, s *corev1.Secret) {
	actual, err := automationconfig.FromBytes(s.Data["cluster-config.json"])
	assert.NoError(t, err)
	expectedBytes, err := json.Marshal(expected)
	assert.NoError(t, err)
	expectedAc := automationconfig.AutomationConfig{}
	err = json.Unmarshal(expectedBytes, &expectedAc)
	assert.NoError(t, err)
	assert.Equal(t, expectedAc, actual)
}

func newAppDbReconciler(ctx context.Context, c client.Client, opsManager *omv1.MongoDBOpsManager, omConnectionFactoryFunc om.ConnectionFactory, log *zap.SugaredLogger) (*ReconcileAppDbReplicaSet, error) {
	commonController := NewReconcileCommonController(ctx, c)
	return NewAppDBReplicaSetReconciler(ctx, nil, "", opsManager.Spec.AppDB, commonController, omConnectionFactoryFunc, opsManager.Annotations, nil, zap.S())
}

func newAppDbMultiReconciler(ctx context.Context, c client.Client, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]client.Client, log *zap.SugaredLogger, omConnectionFactoryFunc om.ConnectionFactory) (*ReconcileAppDbReplicaSet, error) {
	_ = c.Update(ctx, opsManager)
	commonController := NewReconcileCommonController(ctx, c)
	return NewAppDBReplicaSetReconciler(ctx, nil, "", opsManager.Spec.AppDB, commonController, omConnectionFactoryFunc, opsManager.Annotations, memberClusterMap, log)
}

func TestChangingFCVAppDB(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(3)
	opsManager := builder.Build()
	fakeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler := createRunningAppDB(ctx, t, 3, fakeClient, opsManager, omConnectionFactory)

	// Helper function to update and verify FCV
	verifyFCV := func(version, expectedFCV string, fcvOverride *string, t *testing.T) {
		if fcvOverride != nil {
			opsManager.Spec.AppDB.FeatureCompatibilityVersion = fcvOverride
		}

		opsManager.Spec.AppDB.Version = version
		_ = fakeClient.Update(ctx, opsManager)
		_, err := reconciler.ReconcileAppDB(ctx, opsManager)
		assert.NoError(t, err)
		assert.Equal(t, expectedFCV, opsManager.Status.AppDbStatus.FeatureCompatibilityVersion)
	}

	testFCVsCases(t, verifyFCV)
}

// createOpsManagerUserPasswordSecret creates the secret which holds the password that will be used for the Ops Manager user.
func createOpsManagerUserPasswordSecret(ctx context.Context, kubeClient client.Client, om *omv1.MongoDBOpsManager, password string) error {
	sec := secret.Builder().
		SetName(om.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetNamespace(om.Namespace).
		SetField("password", password).
		Build()
	return kubeClient.Create(ctx, &sec)
}

func readAutomationConfigSecret(ctx context.Context, t *testing.T, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager) *corev1.Secret {
	s := &corev1.Secret{}
	key := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName())
	assert.NoError(t, kubeClient.Get(ctx, key, s))
	return s
}

func readAutomationConfigMonitoringSecret(ctx context.Context, t *testing.T, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager) *corev1.Secret {
	s := &corev1.Secret{}
	key := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.MonitoringAutomationConfigSecretName())
	assert.NoError(t, kubeClient.Get(ctx, key, s))
	return s
}

func createRunningAppDB(ctx context.Context, t *testing.T, startingMembers int, fakeClient kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, omConnectionFactory *om.CachedOMConnectionFactory) *ReconcileAppDbReplicaSet {
	err := createOpsManagerUserPasswordSecret(ctx, fakeClient, opsManager, "pass")
	assert.NoError(t, err)
	reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// create the apiKey and OM user
	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}

	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: fakeClient}, "")
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	err = fakeClient.Create(ctx, opsManager)
	assert.NoError(t, err)

	matchLabels, serviceName := appDBStatefulSetLabelsAndServiceName(opsManager.Name)
	// app db sts should exist before monitoring is configured
	appDbSts, err := statefulset.NewBuilder().
		SetName(opsManager.Spec.AppDB.Name()).
		SetNamespace(opsManager.Namespace).
		SetMatchLabels(matchLabels).
		SetServiceName(serviceName).
		AddVolumeClaimTemplates(appDBStatefulSetVolumeClaimTemplates()).
		SetReplicas(startingMembers).
		Build()

	assert.NoError(t, err)
	err = fakeClient.CreateStatefulSet(ctx, appDbSts)
	assert.NoError(t, err)

	res, err := reconciler.ReconcileAppDB(ctx, opsManager)

	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)
	return reconciler
}
