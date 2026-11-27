package operator

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status/pvc"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/host"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/create"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/agentVersionManagement"
	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/scramcredentials"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	tlspkg "github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	envtestlib "github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func init() {
	mock.InitDefaultEnvVariables()
}

// getReleaseJsonPath searches for a specified target directory by traversing the directory tree backwards from the current working directory
func getReleaseJsonPath() (string, error) {
	releaseFileName := "release.json"

	currentDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for currentDir != "/" {
		if _, err := os.Stat(filepath.Join(currentDir, releaseFileName)); !errors.Is(err, os.ErrNotExist) {
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
	os.Exit(envtestlib.RunShared(m, envtestlib.WithCRDs("mongodb.com_opsmanagers.yaml")))
}

func TestMongoDB_ConnectionURL_DefaultCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	appdb := opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000&ssl=false", cnx)

	// Special symbols in the url
	cnx = appdb.BuildConnectionURL("special/user#", "@passw!", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://special%2Fuser%23:%40passw%21@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000&ssl=false", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)
}

func TestMongoDB_ConnectionURL_OtherCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().SetClusterDomain("my-cluster").Build()
	appdb := opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000&ssl=false", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000&ssl=false",
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

	memberCluster := multicluster.GetLegacyCentralMemberCluster(opsManager.Spec.Replicas, 0, reconciler.client, reconciler.SecretClient)
	automationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.NoError(t, err)

	version, err := reconciler.publishAutomationConfig(ctx, opsManager, automationConfig, appdb.AutomationConfigSecretName(), memberCluster.SecretClient)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	monitoringAutomationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.NoError(t, err)
	version, err = reconciler.publishAutomationConfig(ctx, opsManager, monitoringAutomationConfig, monitoringAutomationConfigSecretName(appdb), memberCluster.SecretClient)
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

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, monitoringAutomationConfigSecretName(appdb)))
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

	automationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.NoError(t, err)
	// processes
	assert.Len(t, automationConfig.Processes, 2)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[0].Version)
	assert.Equal(t, "test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[0].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[0].FeatureCompatibilityVersion)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[1].Version)
	assert.Equal(t, "test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[1].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[1].FeatureCompatibilityVersion)
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

func TestBuildAppDbAutomationConfig_MonitoringNotEmbeddedInAC(t *testing.T) {
	// OPS_MANAGER_MONITOR_APPDB defaults true; set explicitly to avoid env-state sensitivity
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "my-api-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	require.NotEmpty(t, ac.MonitoringVersions)
	// Under Option B the credentials are delivered to the agent as CLI flags, not via additionalParams.
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsGroupId")
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsApiKey")
	require.NotNil(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, 1000, ac.MonitoringVersions[0].LogRotate.SizeThresholdMB)
	assert.Equal(t, 24, ac.MonitoringVersions[0].LogRotate.TimeThresholdHrs)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestBuildAppDbAutomationConfig_NoMonitoringWhenDisabled(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "false")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	// nil podVars => ShouldEnableMonitoring returns false
	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, nil, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	assert.Empty(t, ac.MonitoringVersions)
}

func TestBuildAppDbAutomationConfig_MonitoringLogRotateCustom(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	opsManager.Spec.AppDB.AutomationAgent.MonitoringAgent.LogRotate = &mdbv1.LogRotateForBackupAndMonitoring{
		SizeThresholdMB:  500,
		TimeThresholdHrs: 12,
	}
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "my-api-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	require.NotEmpty(t, ac.MonitoringVersions)
	require.NotNil(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, 500, ac.MonitoringVersions[0].LogRotate.SizeThresholdMB)
	assert.Equal(t, 12, ac.MonitoringVersions[0].LogRotate.TimeThresholdHrs)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestBuildAppDbAutomationConfig_ManagedCACertificatePath(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()

	newReconciler := func(t *testing.T, opsManager *omv1.MongoDBOpsManager) (kubernetesClient.Client, *ReconcileAppDbReplicaSet) {
		t.Helper()
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
		reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")
		return kubeClient, reconciler
	}
	publishBundle := func(t *testing.T, kubeClient kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, hash string) {
		t.Helper()
		cm := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.ManagedServerCABundleConfigMapName(opsManager.Spec.AppDB.Name()),
				Namespace: opsManager.Namespace,
			},
			Data: map[string]string{util.LatestHashSecretKey: hash},
		}
		require.NoError(t, kubeClient.Create(ctx, &cm))
	}
	podVars := &env.PodEnvVars{ProjectID: "abc123", AgentAPIKey: "my-api-key"}

	t.Run("managed certificates use the hash-keyed bundle path for mongod and the monitoring agent", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		opsManager.Spec.AppDB.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
		kubeClient, reconciler := newReconciler(t, opsManager)
		publishBundle(t, kubeClient, opsManager, "hash-one")

		ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
		require.NoError(t, err)

		expected := path.Join(util.ConfigMapVolumeCAMountPath, "hash-one")
		require.NotNil(t, ac.TLSConfig)
		assert.Equal(t, expected, ac.TLSConfig.CAFilePath)
		require.NotEmpty(t, ac.MonitoringVersions)
		assert.Equal(t, expected, ac.MonitoringVersions[0].AdditionalParams["sslTrustedServerCertificates"])
	})

	t.Run("a republished bundle changes the automation config path", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		opsManager.Spec.AppDB.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
		kubeClient, reconciler := newReconciler(t, opsManager)
		publishBundle(t, kubeClient, opsManager, "hash-one")

		require.NoError(t, kubeClient.UpdateConfigMap(ctx, corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.ManagedServerCABundleConfigMapName(opsManager.Spec.AppDB.Name()),
				Namespace: opsManager.Namespace,
			},
			Data: map[string]string{util.LatestHashSecretKey: "hash-two"},
		}))

		ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
		require.NoError(t, err)
		assert.Equal(t, path.Join(util.ConfigMapVolumeCAMountPath, "hash-two"), ac.TLSConfig.CAFilePath)
	})

	t.Run("a missing bundle fails the build so the reconcile retries", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		opsManager.Spec.AppDB.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
		_, reconciler := newReconciler(t, opsManager)

		_, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AppDB CA bundle hash")
	})

	t.Run("non-managed certificates keep the fixed ca-pem path", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		_, reconciler := newReconciler(t, opsManager)

		ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
		require.NoError(t, err)
		assert.Equal(t, appdbCAFilePath, ac.TLSConfig.CAFilePath)
		// TLS is not enabled for this AppDB, so the monitoring agent gets no TLS CA parameter.
		require.NotEmpty(t, ac.MonitoringVersions)
		assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "sslTrustedServerCertificates")
	})
}

// TestGetAppDBConfig_ManagedCABundleHash checks that the AppDB config used to build the Ops Manager
// and backup daemon StatefulSets carries the managed AppDB CA bundle hash. Both hold the AppDB CA in
// their client setup, so a widened bundle must restart them before the AppDB leaf moves to the new CA.
func TestGetAppDBConfig_ManagedCABundleHash(t *testing.T) {
	ctx := context.Background()

	newReconciler := func(t *testing.T, opsManager *omv1.MongoDBOpsManager) (kubernetesClient.Client, *ReconcileAppDbReplicaSet) {
		t.Helper()
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
		reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		require.NoError(t, createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password"))
		return kubeClient, reconciler
	}
	bundle := func(opsManager *omv1.MongoDBOpsManager, hash string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.ManagedServerCABundleConfigMapName(opsManager.Spec.AppDB.Name()),
				Namespace: opsManager.Namespace,
			},
			Data: map[string]string{util.LatestHashSecretKey: hash},
		}
	}

	t.Run("managed certificates carry the published bundle hash", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		opsManager.Spec.AppDB.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
		kubeClient, reconciler := newReconciler(t, opsManager)
		require.NoError(t, kubeClient.Create(ctx, bundle(opsManager, "hash-one")))

		cfg, err := reconciler.GetAppDBConfig(ctx, opsManager, zap.S())
		require.NoError(t, err)
		assert.Equal(t, "hash-one", cfg.CABundleHash)

		require.NoError(t, kubeClient.UpdateConfigMap(ctx, *bundle(opsManager, "hash-two")))
		cfg, err = reconciler.GetAppDBConfig(ctx, opsManager, zap.S())
		require.NoError(t, err)
		assert.Equal(t, "hash-two", cfg.CABundleHash, "a widened bundle must change the hash")
	})

	t.Run("a missing managed bundle fails so the reconcile retries", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		opsManager.Spec.AppDB.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
		_, reconciler := newReconciler(t, opsManager)

		_, err := reconciler.GetAppDBConfig(ctx, opsManager, zap.S())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AppDB CA bundle hash")
	})

	t.Run("non-managed certificates carry no hash", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		_, reconciler := newReconciler(t, opsManager)

		cfg, err := reconciler.GetAppDBConfig(ctx, opsManager, zap.S())
		require.NoError(t, err)
		assert.Empty(t, cfg.CABundleHash)
	})
}

// TestManagedOpsManagerCABundleHash checks that the AppDB StatefulSet options carry the managed Ops
// Manager CA bundle hash. The monitoring connection is initialized with -httpsCAFile, so a widened
// bundle must restart the AppDB pods before Ops Manager serves its leaf on the new CA.
func TestManagedOpsManagerCABundleHash(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()

	newReconciler := func(t *testing.T, opsManager *omv1.MongoDBOpsManager) (kubernetesClient.Client, *ReconcileAppDbReplicaSet) {
		t.Helper()
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
		reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		return kubeClient, reconciler
	}
	managedOpsManager := func() *omv1.MongoDBOpsManager {
		opsManager := DefaultOpsManagerBuilder().Build()
		opsManager.Spec.Security = &omv1.MongoDBOpsManagerSecurity{ManagedCertificate: &omv1.ManagedCertificate{}}
		return opsManager
	}
	bundle := func(opsManager *omv1.MongoDBOpsManager, hash string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: opsManager.GetOpsManagerCA(), Namespace: opsManager.Namespace},
			Data:       map[string]string{util.LatestHashSecretKey: hash},
		}
	}
	podVarsFor := func(opsManager *omv1.MongoDBOpsManager) *env.PodEnvVars {
		return &env.PodEnvVars{ProjectID: "abc123", AgentAPIKey: "my-api-key", SSLProjectConfig: env.SSLProjectConfig{SSLMMSCAConfigMap: opsManager.GetOpsManagerCA()}}
	}

	t.Run("managed certificates carry the published bundle hash", func(t *testing.T) {
		opsManager := managedOpsManager()
		kubeClient, reconciler := newReconciler(t, opsManager)
		require.NoError(t, kubeClient.Create(ctx, bundle(opsManager, "hash-one")))

		hash, err := reconciler.managedOpsManagerCABundleHash(ctx, opsManager, podVarsFor(opsManager))
		require.NoError(t, err)
		assert.Equal(t, "hash-one", hash)

		require.NoError(t, kubeClient.UpdateConfigMap(ctx, *bundle(opsManager, "hash-two")))
		hash, err = reconciler.managedOpsManagerCABundleHash(ctx, opsManager, podVarsFor(opsManager))
		require.NoError(t, err)
		assert.Equal(t, "hash-two", hash, "a widened bundle must change the hash")
	})

	t.Run("a missing managed bundle fails so the reconcile retries", func(t *testing.T) {
		opsManager := managedOpsManager()
		_, reconciler := newReconciler(t, opsManager)

		_, err := reconciler.managedOpsManagerCABundleHash(ctx, opsManager, podVarsFor(opsManager))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Ops Manager CA bundle hash")
	})

	t.Run("no hash before monitoring is configured", func(t *testing.T) {
		opsManager := managedOpsManager()
		_, reconciler := newReconciler(t, opsManager)

		hash, err := reconciler.managedOpsManagerCABundleHash(ctx, opsManager, &env.PodEnvVars{})
		require.NoError(t, err)
		assert.Empty(t, hash)
	})

	t.Run("non-managed certificates carry no hash", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		_, reconciler := newReconciler(t, opsManager)

		podVars := &env.PodEnvVars{ProjectID: "abc123", SSLProjectConfig: env.SSLProjectConfig{SSLMMSCAConfigMap: "user-om-ca"}}
		hash, err := reconciler.managedOpsManagerCABundleHash(ctx, opsManager, podVars)
		require.NoError(t, err)
		assert.Empty(t, hash)
	})
}

func TestManagedAppDBCABundleOrdering(t *testing.T) {
	ctx := t.Context()
	replicas := int32(1)
	legacyMounts := []corev1.Volume{{
		Name:         util.SecretVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "test-omdb-cert-pem"}},
	}}
	managedMount := corev1.Volume{
		Name:         util.SecretVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "test-omdb-managed-cert-pem"}},
	}

	for _, tc := range []struct {
		name        string
		annotations map[string]string
		volumes     []corev1.Volume
		serverHash  string
		wantPublish bool
		wantManaged bool
	}{
		{
			name:        "managed AppDB CA bundle change",
			annotations: map[string]string{construct.ManagedServerCABundleHashAnnotation: "old-server"},
			serverHash:  "new-server",
			wantPublish: true,
			wantManaged: true,
		},
		{
			name:        "unchanged managed AppDB CA bundle",
			annotations: map[string]string{construct.ManagedServerCABundleHashAnnotation: "old-server"},
			serverHash:  "old-server",
			wantPublish: false,
			wantManaged: false,
		},
		{
			name:        "legacy AppDB adopting managed certificates",
			volumes:     legacyMounts,
			serverHash:  "new-server",
			wantPublish: false,
			wantManaged: false,
		},
		{
			name:        "managed AppDB without the bundle annotation",
			volumes:     []corev1.Volume{managedMount},
			serverHash:  "new-server",
			wantPublish: true,
			wantManaged: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opsManager := DefaultOpsManagerBuilder().Build()
			opsManager.Spec.AppDB.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
			reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
			require.NoError(t, err)

			require.NoError(t, kubeClient.Create(ctx, &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: opsManager.Spec.AppDB.Name(), Namespace: opsManager.Namespace},
				Spec: appsv1.StatefulSetSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Annotations: tc.annotations},
						Spec:       corev1.PodSpec{Volumes: tc.volumes},
					},
				},
			}))

			opts := construct.AppDBStatefulSetOptions{ManagedServerCABundleHash: tc.serverHash}
			publishFirst, managedChange, err := reconciler.publishAutomationConfigFirst(ctx, opsManager, true, opts, zap.S())
			require.NoError(t, err)
			assert.Equal(t, tc.wantPublish, publishFirst)
			assert.Equal(t, tc.wantManaged, managedChange)
		})
	}
}

func TestMonitoringToggledOff_ClearsMonitoringVersions(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "false")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	// Non-nil podVars with a project ID — monitoring is disabled via the env var, not nil podVars
	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "some-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)
	assert.Empty(t, ac.MonitoringVersions, "MonitoringVersions must be empty when monitoring env var is false")
}

func TestMonitoringToggledBackOn_PopulatesMonitoringVersions(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "re-enabled-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)
	require.NotEmpty(t, ac.MonitoringVersions, "MonitoringVersions must be populated when monitoring is re-enabled")
	assert.Equal(t, "abc123", podVars.ProjectID)
	// Credentials are no longer embedded in additionalParams under Option B.
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsGroupId")
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsApiKey")
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
		err = reconciler.registerAppDBHostsWithProject(ctx, opsManager.Spec.AppDB.GetSecurity(), hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetHosts(ctx)
		assert.Len(t, hosts.Results, 3)
		for _, h := range hosts.Results {
			assert.False(t, h.SSLEnabled, "AppDB TLS is not enabled, so host %s must be registered with SSL off", h.Hostname)
		}
	})

	t.Run("Ensure hosts are added when scaled up", func(t *testing.T) {
		opsManager.Spec.AppDB.Members = 5
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(ctx, opsManager.Spec.AppDB.GetSecurity(), hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := omConnectionFactory.GetConnection().GetHosts(ctx)
		assert.Len(t, hosts.Results, 5)
	})

	t.Run("Ensure hosts are removed when scaled down", func(t *testing.T) {
		opsManager.Spec.AppDB.Members = 3
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(ctx, opsManager.Spec.AppDB.GetSecurity(), hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		// After scale-down, hosts should be removed from monitoring
		hosts, _ := omConnectionFactory.GetConnection().GetHosts(ctx)
		assert.Len(t, hosts.Results, 3, "Expected 3 hosts after scaling down from 5 to 3 members")
	})

	t.Run("Ensure hosts are registered with SSL on when the AppDB has TLS enabled", func(t *testing.T) {
		opsManager.Spec.AppDB.Security = &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(ctx, opsManager.Spec.AppDB.GetSecurity(), hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		require.NoError(t, err)

		hosts, _ := omConnectionFactory.GetConnection().GetHosts(ctx)
		require.Len(t, hosts.Results, 3)
		for _, h := range hosts.Results {
			assert.True(t, h.SSLEnabled, "host %s should be registered with SSL on", h.Hostname)
		}
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
	_, err = reconciler.ensureAppDbAgentApiKey(ctx, opsManager, omConnectionFactory.GetConnection(), omConnectionFactory.GetConnection().GroupID(), zap.S())
	assert.NoError(t, err)

	secretName := mdbv1.AgentAPIKeySecretName(omConnectionFactory.GetConnection().GroupID())
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
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Empty(t, podVars.ProjectID)
	assert.Empty(t, podVars.User)

	opsManager.Spec.AppDB.Members = 5
	appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
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
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	// once the secret exists, monitoring should be fully configured
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "publicApiKey", podVars.User)

	expectedHostnames := []string{
		"test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-3.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-4.test-om-db-svc.my-namespace.svc.cluster.local",
	}

	assertExpectedHostnamesAndPreferred(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection), expectedHostnames)

	appDbSts, err = construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	assert.NoError(t, err)

	// Monitoring is enabled now (ProjectID populated), so the agent-api-key volume must be mounted
	// to feed the -mmsApiKey CLI flag via ${AGENT_API_KEY}.
	assert.NotNil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))
}

func TestTryConfigureMonitoring_PopulatesAgentAPIKey(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// Create the OM API key secret (required by tryConfigureMonitoringInOpsManager)
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	require.NoError(t, err)
	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(map[string]string{util.OmPublicApiKey: "publicApiKey", util.OmPrivateKey: "privateApiKey"}).
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()
	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	require.NoError(t, err)

	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/cert", zap.S())
	require.NoError(t, err)
	// MockedOmConnection.AgentAPIKey is om.TestAgentKey by default
	assert.Equal(t, om.TestAgentKey, podVars.AgentAPIKey)
}

// TestTryConfigureMonitoringInOpsManagerWithCustomTemplate runs different scenarios with activating monitoring and pod templates
func TestTryConfigureMonitoringInOpsManagerWithCustomTemplate(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdbScaler := scalers.GetAppDBScaler(opsManager, multicluster.LegacyCentralClusterName, 0, nil)

	opsManager.Spec.AppDB.PodSpec.PodTemplateWrapper = v1.PodTemplateSpecWrapper{
		PodTemplate: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "mongodb-agent",
						Image: "quay.io/mongodb/mongodb-agent:10",
					},
					{
						Name:  "mongod",
						Image: "quay.io/mongodb/mongodb:10",
					},
					{
						Name:  "mongodb-agent-monitoring",
						Image: "quay.io/mongodb/mongodb-agent:20",
					},
				},
			},
		},
	}

	t.Run("do not override images while activating monitoring", func(t *testing.T) {
		podVars := env.PodEnvVars{ProjectID: "something"}
		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
		assert.NoError(t, err)
		assert.NotNil(t, appDbSts)

		foundImages := 0
		for _, c := range appDbSts.Spec.Template.Spec.Containers {
			if c.Name == "mongodb-agent" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongod" {
				assert.Equal(t, "quay.io/mongodb/mongodb:10", c.Image)
				foundImages += 1
			}
		}

		// monitoring container is stripped from user-supplied templates; only agent + mongod remain
		assert.Equal(t, 2, foundImages)
		assert.Equal(t, 2, len(appDbSts.Spec.Template.Spec.Containers))
	})

	t.Run("do not override images, but remove monitoring if not activated", func(t *testing.T) {
		podVars := env.PodEnvVars{}
		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
		assert.NoError(t, err)
		assert.NotNil(t, appDbSts)

		foundImages := 0
		for _, c := range appDbSts.Spec.Template.Spec.Containers {
			if c.Name == "mongodb-agent" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongod" {
				assert.Equal(t, "quay.io/mongodb/mongodb:10", c.Image)
				foundImages += 1
			}
		}

		assert.Equal(t, 2, foundImages)
		assert.Equal(t, 2, len(appDbSts.Spec.Template.Spec.Containers))
	})
}

func TestTryConfigureMonitoringInOpsManagerWithExternalDomains(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().
		SetAppDbExternalAccess(mdbv1.ExternalAccessConfiguration{
			ExternalDomain: new("custom.domain"),
		}).Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	appdbScaler := scalers.GetAppDBScaler(opsManager, multicluster.LegacyCentralClusterName, 0, nil)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// attempt configuring monitoring when there is no api key secret
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Empty(t, podVars.ProjectID)
	assert.Empty(t, podVars.User)

	opsManager.Spec.AppDB.Members = 5
	appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
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
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	// once the secret exists, monitoring should be fully configured
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "publicApiKey", podVars.User)

	expectedHostnames := []string{
		"test-om-db-0.custom.domain",
		"test-om-db-1.custom.domain",
		"test-om-db-2.custom.domain",
		"test-om-db-3.custom.domain",
		"test-om-db-4.custom.domain",
	}

	assertExpectedHostnamesAndPreferred(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection), expectedHostnames)

	appDbSts, err = construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	assert.NoError(t, err)

	// Monitoring is enabled now (ProjectID populated), so the agent-api-key volume must be mounted.
	assert.NotNil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))
}

func TestTryConfigureMonitoringInOpsManagerWithMalformedCredentials(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(5)
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	data := map[string]string{
		util.OmPublicApiKey + "Malformed": "publicApiKey",
		util.OmPrivateKey:                 "privateApiKey",
	}
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	// the secret is malformed and tryConfigureMonitoringInOpsManager fails
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.Error(t, err)
	assert.ErrorContains(t, err, "error reading opsManager credentials")
	assert.Empty(t, podVars)

	updatedData := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}

	updatedApiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(updatedData).
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()

	err = reconciler.client.UpdateSecret(ctx, updatedApiKeySecret)
	assert.NoError(t, err)

	// the secret is correct and tryConfigureMonitoringInOpsManager succeeds
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)
	assert.NotEmpty(t, podVars)
	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "publicApiKey", podVars.User)
}

func TestReadExistingPodVars_ReturnsAgentAPIKey(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-123"
	agentKey := "fallback-agent-key"

	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// 1. Create projectID configmap
	err = reconciler.ensureProjectIDConfigMapForCluster(ctx, opsManager, projectID, reconciler.client)
	require.NoError(t, err)

	// 2. Create OM API key secret (needed by ReadCredentials for User field)
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	require.NoError(t, err)
	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(map[string]string{util.OmPublicApiKey: "publicApiKey", util.OmPrivateKey: "privateApiKey"}).
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()
	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	require.NoError(t, err)

	// 3. Create agent API key secret
	agentKeySecret := secret.Builder().
		SetNamespace(opsManager.Namespace).
		SetName(mdbv1.AgentAPIKeySecretName(projectID)).
		SetStringMapToData(map[string]string{util.OmAgentApiKey: agentKey}).
		Build()
	err = reconciler.client.CreateSecret(ctx, agentKeySecret)
	require.NoError(t, err)

	podVars, err := reconciler.readExistingPodVars(ctx, opsManager, zap.S())
	require.NoError(t, err)
	assert.Equal(t, agentKey, podVars.AgentAPIKey)
	assert.Equal(t, projectID, podVars.ProjectID)
}

func TestReadExistingPodVars_MissingAgentKeyIsNonFatal(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-123"

	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// projectID configmap exists (so the function gets past the projectId guard)...
	err = reconciler.ensureProjectIDConfigMapForCluster(ctx, opsManager, projectID, reconciler.client)
	require.NoError(t, err)

	// OM API key secret exists (needed by ReadCredentials for the User field)...
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	require.NoError(t, err)
	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(map[string]string{util.OmPublicApiKey: "publicApiKey", util.OmPrivateKey: "privateApiKey"}).
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()
	require.NoError(t, reconciler.client.CreateSecret(ctx, apiKeySecret))

	// ...but the agent API key secret is intentionally NOT created.
	podVars, err := reconciler.readExistingPodVars(ctx, opsManager, zap.S())
	require.NoError(t, err)
	assert.Empty(t, podVars.AgentAPIKey, "missing agent key must not be fatal")
	assert.Equal(t, projectID, podVars.ProjectID)
}

func TestAppDBServiceCreation_WithExternalName(t *testing.T) {
	tests := map[string]struct {
		members                int
		externalAccess         *mdbv1.ExternalAccessConfiguration
		additionalMongodConfig *mdbv1.AdditionalMongodConfig
		result                 map[int]corev1.Service
	}{
		"empty external access configured for one pod": {
			members:        1,
			externalAccess: &mdbv1.ExternalAccessConfiguration{},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"external access configured for two pods": {
			members: 2,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalService: mdbv1.ExternalServiceConfiguration{
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "LoadBalancer",
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
								{
									Name: "backup",
									Port: 27018,
								},
								{
									Name: "testing2",
									Port: 27019,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
							{
								Name: "backup",
								Port: 27018,
							},
							{
								Name: "testing2",
								Port: 27019,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
				1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-1-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
							{
								Name: "backup",
								Port: 27018,
							},
							{
								Name: "testing2",
								Port: 27019,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
						},
					},
				},
			},
		},
		"external domain configured for single pod in first cluster": {
			members: 1,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalDomain: new("some.domain"),
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"non default port set in additional mongod config": {
			members:                1,
			externalAccess:         &mdbv1.ExternalAccessConfiguration{},
			additionalMongodConfig: mdbv1.NewAdditionalMongodConfig("net.port", 27027),
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27027,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"external service of NodePort type set in first cluster": {
			members: 1,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalService: mdbv1.ExternalServiceConfiguration{
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "NodePort",
							Ports: []corev1.ServicePort{
								{
									Name:     "mongodb",
									Port:     27017,
									NodePort: 30003,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "NodePort",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name:     "mongodb",
								Port:     27017,
								NodePort: 30003,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"service with annotations with placeholders": {
			members: 2,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalService: mdbv1.ExternalServiceConfiguration{
					Annotations: map[string]string{
						create.PlaceholderPodIndex:            "{podIndex}",
						create.PlaceholderNamespace:           "{namespace}",
						create.PlaceholderResourceName:        "{resourceName}",
						create.PlaceholderPodName:             "{podName}",
						create.PlaceholderStatefulSetName:     "{statefulSetName}",
						create.PlaceholderExternalServiceName: "{externalServiceName}",
						create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
						create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
					},
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "LoadBalancer",
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "0",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-0",
							create.PlaceholderExternalServiceName: "test-om-db-0-svc-external",
							create.PlaceholderMongodProcessDomain: "test-om-db-svc.my-namespace.svc.cluster.local",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
				1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-1-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "1",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-1",
							create.PlaceholderExternalServiceName: "test-om-db-1-svc-external",
							create.PlaceholderMongodProcessDomain: "test-om-db-svc.my-namespace.svc.cluster.local",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
						},
					},
				},
			},
		},
		"service with annotations with placeholders and external domain": {
			members: 2,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalDomain: new("custom.domain"),
				ExternalService: mdbv1.ExternalServiceConfiguration{
					Annotations: map[string]string{
						create.PlaceholderPodIndex:            "{podIndex}",
						create.PlaceholderNamespace:           "{namespace}",
						create.PlaceholderResourceName:        "{resourceName}",
						create.PlaceholderPodName:             "{podName}",
						create.PlaceholderStatefulSetName:     "{statefulSetName}",
						create.PlaceholderExternalServiceName: "{externalServiceName}",
						create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
						create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
					},
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "LoadBalancer",
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "0",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-0",
							create.PlaceholderExternalServiceName: "test-om-db-0-svc-external",
							create.PlaceholderMongodProcessDomain: "custom.domain",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-0.custom.domain",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
				1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-1-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "1",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-1",
							create.PlaceholderExternalServiceName: "test-om-db-1-svc-external",
							create.PlaceholderMongodProcessDomain: "custom.domain",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-1.custom.domain",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
						},
					},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			opsManagerBuilder := DefaultOpsManagerBuilder().
				SetAppDbExternalAccess(*test.externalAccess).
				SetAppDbMembers(test.members).
				SetAdditionalMongodbConfig(test.additionalMongodConfig)

			opsManager := opsManagerBuilder.Build()

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
			reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
			require.NoError(t, err)

			err = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
			assert.NoError(t, err)
			reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
			require.NoError(t, err)

			assert.Equal(t, time.Duration(10000000000), reconcileResult.RequeueAfter)

			for podIdx := 0; podIdx < opsManager.Spec.AppDB.Members; podIdx++ {
				svcName := dns.GetExternalServiceName(opsManager.Spec.AppDB.GetName(), podIdx)
				svcNamespace := opsManager.Namespace

				svcList := corev1.ServiceList{}
				err = kubeClient.List(ctx, &svcList)
				assert.NoError(t, err)

				actualSvc := corev1.Service{}

				err = kubeClient.Get(ctx, kube.ObjectKey(svcNamespace, svcName), &actualSvc)
				assert.NoError(t, err)

				expectedSvc := test.result[podIdx]
				assert.Equal(t, expectedSvc, actualSvc)
			}
		})
	}
}

func findVolumeByName(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := range volumes {
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
	omReconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, nil, omConnectionFactory, architectures.NonStatic)

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
		SetAdditionalMongodbConfig(mdbv1.NewAdditionalMongodConfig("net.port", 30000)).
		Build()
	omConnectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	omReconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, nil, omConnectionFactory, architectures.NonStatic)

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
		memberCluster := multicluster.GetLegacyCentralMemberCluster(opsManager.Spec.Replicas, 0, reconciler.client, reconciler.SecretClient)

		// create a pre-existing automation config based on the resource provided.
		// if the automation is not there, we will always want to reconcile. Otherwise, we may not reconcile
		// based on whether or not there are disabled processes.
		if createAutomationConfig {
			ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, nil, UnusedPrometheusConfiguration, multicluster.LegacyCentralClusterName, zap.S())
			assert.NoError(t, err)

			_, err = reconciler.publishAutomationConfig(ctx, opsManager, ac, opsManager.Spec.AppDB.AutomationConfigSecretName(), memberCluster.SecretClient)
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

		opsManager = DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
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
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
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
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
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
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
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
	labels := map[string]string{"app": serviceName, util.OperatorLabelName: util.OperatorLabelValue, "pod-anti-affinity": appDbName}
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

func buildAutomationConfigForAppDb(ctx context.Context, builder *omv1.OpsManagerBuilder, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, log *zap.SugaredLogger) (automationconfig.AutomationConfig, error) {
	opsManager := builder.Build()

	// Ensure the password exists for the Ops Manager User. The Ops Manager controller will have ensured this.
	// We are ignoring this err on purpose since the secret might already exist.
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactoryFunc, zap.S())
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}
	return reconciler.buildAppDbAutomationConfig(ctx, opsManager, nil, UnusedPrometheusConfiguration, multicluster.LegacyCentralClusterName, zap.S())
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

func TestBuildMongoConnectionUrl(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()

	url := buildMongoConnectionUrl(opsManager, "password", nil)

	opsManager.Spec.AppDB.Members = 5
	assert.NotEqual(t, url, buildMongoConnectionUrl(opsManager, "password", nil),
		"Changing the number of members should result in a different connection string")

	opsManager.Spec.AppDB.Members = 3
	opsManager.Spec.AppDB.Version = "4.2.0"
	assert.Equal(t, url, buildMongoConnectionUrl(opsManager, "password", nil),
		"Changing version should not change the connection string")
}

func TestEnsureResourcesForArchitectureChange(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	t.Run("When no automation config is present, there is no error", func(t *testing.T) {
		client := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
		reconciler, err := newAppDbReconciler(ctx, client, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)

		err = reconciler.ensureResourcesForArchitectureChange(ctx, opsManager)
		assert.NoError(t, err)
	})

	t.Run("If User is not present, there is an error", func(t *testing.T) {
		client := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
		ac, err := automationconfig.NewBuilder().SetAuth(automationconfig.Auth{
			Users: []automationconfig.MongoDBUser{
				{
					Username: "not-ops-manager-user",
				},
			},
		}).Build()

		assert.NoError(t, err)

		acBytes, err := json.Marshal(ac)
		assert.NoError(t, err)

		// create the automation config secret
		err = client.CreateSecret(ctx, secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		reconciler, err := newAppDbReconciler(ctx, client, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)

		err = reconciler.ensureResourcesForArchitectureChange(ctx, opsManager)
		assert.Error(t, err)
	})

	t.Run("If an automation config is present, all secrets are created with the correct values", func(t *testing.T) {
		client := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
		ac, err := automationconfig.NewBuilder().SetAuth(automationconfig.Auth{
			AutoPwd: "VrBQgsUZJJs",
			Key:     "Z8PSBtvvjnvds4zcI6iZ",
			Users: []automationconfig.MongoDBUser{
				{
					Username: util.OpsManagerMongoDBUserName,
					ScramSha256Creds: &scramcredentials.ScramCreds{
						Salt:      "sha256-salt-value",
						ServerKey: "sha256-serverkey-value",
						StoredKey: "sha256-storedkey-value",
					},
					ScramSha1Creds: &scramcredentials.ScramCreds{
						Salt:      "sha1-salt-value",
						ServerKey: "sha1-serverkey-value",
						StoredKey: "sha1-storedkey-value",
					},
				},
			},
		}).Build()

		assert.NoError(t, err)

		acBytes, err := json.Marshal(ac)
		assert.NoError(t, err)

		// create the automation config secret
		err = client.CreateSecret(ctx, secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		// create the old ops manager user password
		err = client.CreateSecret(ctx, secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.Name()+"-password").SetField("my-password", "jrJP7eUeyn").Build())
		assert.NoError(t, err)

		reconciler, err := newAppDbReconciler(ctx, client, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)

		err = reconciler.ensureResourcesForArchitectureChange(ctx, opsManager)
		assert.NoError(t, err)

		t.Run("Scram credentials have been created", func(t *testing.T) {
			ctx := context.Background()
			scramCreds, err := client.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.OpsManagerUserScramCredentialsName()))
			assert.NoError(t, err)

			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.Salt, string(scramCreds.Data["sha256-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.StoredKey, string(scramCreds.Data["sha-256-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.ServerKey, string(scramCreds.Data["sha-256-server-key"]))

			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.Salt, string(scramCreds.Data["sha1-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.StoredKey, string(scramCreds.Data["sha-1-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.ServerKey, string(scramCreds.Data["sha-1-server-key"]))
		})

		t.Run("Ops Manager user password has been copied", func(t *testing.T) {
			ctx := context.Background()
			newOpsManagerUserPassword, err := client.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName()))
			assert.NoError(t, err)
			assert.Equal(t, string(newOpsManagerUserPassword.Data["my-password"]), "jrJP7eUeyn")
		})

		t.Run("Agent password has been created", func(t *testing.T) {
			ctx := context.Background()
			agentPasswordSecret, err := client.GetSecret(ctx, opsManager.Spec.AppDB.GetAgentPasswordSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.AutoPwd, string(agentPasswordSecret.Data[constants.AgentPasswordKey]))
		})

		t.Run("Keyfile has been created", func(t *testing.T) {
			ctx := context.Background()
			keyFileSecret, err := client.GetSecret(ctx, opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.Key, string(keyFileSecret.Data[constants.AgentKeyfileKey]))
		})
	})
}

func newAppDbReconciler(ctx context.Context, c client.Client, opsManager *omv1.MongoDBOpsManager, omConnectionFactoryFunc om.ConnectionFactory, log *zap.SugaredLogger) (*ReconcileAppDbReplicaSet, error) {
	commonController := NewReconcileCommonController(ctx, c, nil)
	return NewAppDBReplicaSetReconciler(ctx, nil, "", opsManager, commonController, omConnectionFactoryFunc, nil, architectures.NonStatic, "", log)
}

func newAppDbMultiReconciler(ctx context.Context, c client.Client, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]client.Client, log *zap.SugaredLogger, omConnectionFactoryFunc om.ConnectionFactory) (*ReconcileAppDbReplicaSet, error) {
	_ = c.Update(ctx, opsManager)
	commonController := NewReconcileCommonController(ctx, c, nil)
	return NewAppDBReplicaSetReconciler(ctx, nil, "", opsManager, commonController, omConnectionFactoryFunc, memberClusterMap, architectures.NonStatic, "", log)
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
	key := kube.ObjectKey(opsManager.Namespace, monitoringAutomationConfigSecretName(opsManager.Spec.AppDB))
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
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
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
		SetOwnerReference(kube.BaseOwnerReference(opsManager)).
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

func assertDefaultLogRotate(t *testing.T, lr *automationconfig.MonitoringLogRotate) {
	t.Helper()
	require.NotNil(t, lr)
	assert.Equal(t, 1000, lr.SizeThresholdMB)
	assert.Equal(t, 24, lr.TimeThresholdHrs)
}

func TestConfigureMonitoring_NonTLS(t *testing.T) {
	ac := automationconfig.AutomationConfig{
		Processes: []automationconfig.Process{{HostName: "host-0"}},
	}
	configureMonitoring(&ac, zap.S(), false, "myGroupId", "myApiKey", nil, appdbCAFilePath)

	require.Len(t, ac.MonitoringVersions, 1)
	params := ac.MonitoringVersions[0].AdditionalParams
	// Under Option B credentials are delivered to the agent as CLI flags, not via additionalParams.
	assert.NotContains(t, params, "mmsGroupId")
	assert.NotContains(t, params, "mmsApiKey")
	assert.NotContains(t, params, "useSslForAllConnections")
	assertDefaultLogRotate(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestConfigureMonitoring_TLS(t *testing.T) {
	tests := []struct {
		name               string
		monitoringVersions []automationconfig.MonitoringVersion
	}{
		{name: "new monitoring entry"},
		{
			name: "existing entry with server PEM as client certificate",
			monitoringVersions: []automationconfig.MonitoringVersion{
				{Hostname: "host-0", AdditionalParams: map[string]string{"sslClientCertificate": "/certs/server.pem"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := automationconfig.AutomationConfig{
				Processes:          []automationconfig.Process{{HostName: "host-0"}},
				MonitoringVersions: tt.monitoringVersions,
			}
			ac.Processes[0].SetArgs26Field("net.tls.certificateKeyFile", "/certs/server.pem")
			configureMonitoring(&ac, zap.S(), true, "myGroupId", "myApiKey", nil, appdbCAFilePath)

			require.Len(t, ac.MonitoringVersions, 1)
			params := ac.MonitoringVersions[0].AdditionalParams
			assert.NotContains(t, params, "mmsGroupId")
			assert.NotContains(t, params, "mmsApiKey")
			assert.Equal(t, "true", params["useSslForAllConnections"])
			// additionalParams override agent flags, so forcing this would disable the agent's default
			// validation of the Ops Manager certificate against -httpsCAFile.
			assert.NotContains(t, params, "sslRequireValidMMSServerCertificates")
			assert.Equal(t, appdbCAFilePath, params["sslTrustedServerCertificates"])
			assert.NotContains(t, params, "sslClientCertificate")
			assertDefaultLogRotate(t, ac.MonitoringVersions[0].LogRotate)
			assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
		})
	}
}

func TestConfigureMonitoring_TLS_DropsStaleRequireValidCertFalse(t *testing.T) {
	ac := automationconfig.AutomationConfig{
		Processes: []automationconfig.Process{{HostName: "host-0"}},
		MonitoringVersions: []automationconfig.MonitoringVersion{
			{Hostname: "host-0", AdditionalParams: map[string]string{"sslRequireValidMMSServerCertificates": "false"}},
		},
	}
	configureMonitoring(&ac, zap.S(), true, "myGroupId", "myApiKey", nil, appdbCAFilePath)

	require.Len(t, ac.MonitoringVersions, 1)
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "sslRequireValidMMSServerCertificates")
}

func TestConfigureMonitoring_ClearsWhenDisabled(t *testing.T) {
	ac := automationconfig.AutomationConfig{
		Processes: []automationconfig.Process{{HostName: "host-0"}},
		MonitoringVersions: []automationconfig.MonitoringVersion{
			{Hostname: "host-0", AdditionalParams: map[string]string{"mmsGroupId": "old"}},
		},
	}
	// called with empty credentials simulates monitoring disabled
	configureMonitoring(&ac, zap.S(), false, "", "", nil, appdbCAFilePath)

	assert.Empty(t, ac.MonitoringVersions)
}

// TestClearTLSParams tests CLOUDP-351614 fix:
// When TLS is disabled on AppDB, TLS-specific params should be cleared from
// the monitoring config's additionalParams to prevent the monitoring agent
// from trying to use certificate files that no longer exist.
func TestClearTLSParams(t *testing.T) {
	tests := []struct {
		name           string
		input          map[string]string
		expectedOutput map[string]string
	}{
		{
			name:           "nil map",
			input:          nil,
			expectedOutput: nil,
		},
		{
			name:           "empty map",
			input:          map[string]string{},
			expectedOutput: map[string]string{},
		},
		{
			name: "only TLS params",
			input: map[string]string{
				"useSslForAllConnections":      "true",
				"sslTrustedServerCertificates": "/some/path/ca.pem",
				"sslClientCertificate":         "/some/path/cert.pem",
			},
			expectedOutput: map[string]string{},
		},
		{
			name: "mixed params - TLS and non-TLS",
			input: map[string]string{
				"useSslForAllConnections":      "true",
				"sslTrustedServerCertificates": "/some/path/ca.pem",
				"sslClientCertificate":         "/some/path/cert.pem",
				"someOtherParam":               "someValue",
				"anotherParam":                 "anotherValue",
			},
			expectedOutput: map[string]string{
				"someOtherParam": "someValue",
				"anotherParam":   "anotherValue",
			},
		},
		{
			name: "only non-TLS params",
			input: map[string]string{
				"someOtherParam": "someValue",
				"anotherParam":   "anotherValue",
			},
			expectedOutput: map[string]string{
				"someOtherParam": "someValue",
				"anotherParam":   "anotherValue",
			},
		},
		{
			name: "partial TLS params",
			input: map[string]string{
				"useSslForAllConnections": "true",
				"someOtherParam":          "someValue",
			},
			expectedOutput: map[string]string{
				"someOtherParam": "someValue",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			om.ClearTLSParams(tt.input)
			assert.Equal(t, tt.expectedOutput, tt.input)
		})
	}
}

// TestAppDB_PVCStatusClearedAfterSuccessfulResize is a regression test for KUBE-108.
// After a PVC resize completes, the AppDB controller must clear the stale PVC status
// from the CRD. Without the fix, the PhaseSTSOrphaned entry persists indefinitely.
func TestAppDB_PVCStatusClearedAfterSuccessfulResize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		builder := DefaultOpsManagerBuilder().SetAppDbMembers(3)
		opsManager := builder.Build()

		fakeClient, omConnectionFactory := mock.NewDefaultFakeClient()
		createRunningAppDB(ctx, t, 3, fakeClient, opsManager, omConnectionFactory)

		// Create the PVCs that Kubernetes would generate for the AppDB StatefulSet.
		// VolumeClaimTemplate name is "data" (AppDBSpec.DataVolumeName()), STS name is "<om-name>-db".
		stsName := opsManager.Spec.AppDB.Name()
		initialStorage := resource.MustParse("16G")
		newStorage := resource.MustParse("50G")

		var pvcs []corev1.PersistentVolumeClaim
		for i := range 3 {
			p := corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("data-%s-%d", stsName, i),
					Namespace: opsManager.Namespace,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: initialStorage},
					},
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Capacity: corev1.ResourceList{corev1.ResourceStorage: initialStorage},
				},
			}
			require.NoError(t, fakeClient.Create(ctx, &p))
			pvcs = append(pvcs, p)
		}

		// Trigger a resize by increasing the storage in the AppDB spec.
		opsManager.Spec.AppDB.PodSpec.Persistence = &v1.Persistence{
			SingleConfig: &v1.PersistenceConfig{Storage: "50G"},
		}
		require.NoError(t, fakeClient.Update(ctx, opsManager))

		// Reconcile 1: resize detected; PVCs are patched but storage has not propagated yet.
		reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager))
		require.Equal(t, pvc.PhasePVCResize, opsManager.Status.AppDbStatus.PVCs[0].Phase)

		// Simulate Kubernetes completing the PVC resize (update status.Capacity on each PVC).
		for i := range pvcs {
			var updatedPVC corev1.PersistentVolumeClaim
			require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: pvcs[i].Name, Namespace: pvcs[i].Namespace}, &updatedPVC))
			updatedPVC.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: newStorage}
			updatedPVC.Spec.Resources.Requests[corev1.ResourceStorage] = newStorage
			require.NoError(t, fakeClient.SubResource("status").Update(ctx, &updatedPVC))
		}

		// Reconcile 2: PVCs done; the STS is orphaned and recreated. The reconcile completes
		// successfully, so the final updateStatus clears the stale PVC entry in the same pass.
		reconciler, err = newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager))

		assert.Nil(t, opsManager.Status.AppDbStatus.PVCs,
			"PVC status must be cleared after a successful resize")
	})
}

func TestMonitoringAgentStartupParametersIgnored(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	//nolint:staticcheck // intentionally sets the deprecated field to verify it is ignored
	opsManager.Spec.AppDB.MonitoringAgent.StartupParameters = map[string]string{"myCustomParam": "myValue"}

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "my-api-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	require.NotEmpty(t, ac.MonitoringVersions)
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "myCustomParam",
		"deprecated monitoringAgent.startupOptions must not be forwarded to monitoringVersions additionalParams")
}

// monitoringAutomationConfigSecretName / monitoringAutomationConfigConfigMapName reproduce the
// names of the pre-single-agent monitoring automation config resources. The operator no longer
// creates these; the helpers exist only so tests can assert their absence (or exercise legacy paths).
func monitoringAutomationConfigSecretName(appdb *omv1.AppDBSpec) string {
	return appdb.Name() + "-monitoring-config"
}

func monitoringAutomationConfigConfigMapName(appdb *omv1.AppDBSpec) string {
	return appdb.Name() + "-monitoring-automation-config-version"
}

func TestReconcileAppDbReplicaSet_BuildAppDBConnectionURL(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	_, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	appDbReconciler, err := newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	require.NoError(t, appDbReconciler.client.CreateSecret(ctx, secret.Builder().
		SetName(testOm.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetNamespace(testOm.Namespace).
		SetField(util.OpsManagerPasswordKey, "test-password").
		Build()))

	cfg, err := appDbReconciler.GetAppDBConfig(ctx, testOm, zap.S())
	require.NoError(t, err)
	assert.Contains(t, cfg.ConnectionString, util.OpsManagerMongoDBUserName)
}

// TestReconcileAppDB_ReshapesReAdoptedStatefulSet reproduces the reverse-migration state right
// after re-adoption: the StatefulSet is OM-owned again but still carries the
// AppDBReverseMigrationReadyAnnotation, signaling that the pod template must be rewritten.
// A single ReconcileAppDB pass must rewrite the pod template to the internal-AppDB shape
// instead of deadlocking on the agent goal-state wait.
func TestReconcileAppDB_ReshapesReAdoptedStatefulSet(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	require.NoError(t, createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "pass"))
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	matchLabels, serviceName := appDBStatefulSetLabelsAndServiceName(opsManager.Name)
	sts, err := statefulset.NewBuilder().
		SetName(opsManager.Spec.AppDB.Name()).
		SetNamespace(opsManager.Namespace).
		SetMatchLabels(matchLabels).
		SetServiceName(serviceName).
		AddVolumeClaimTemplates(appDBStatefulSetVolumeClaimTemplates()).
		SetReplicas(3).
		SetOwnerReference(kube.BaseOwnerReference(opsManager)).
		Build()
	require.NoError(t, err)
	sts.Annotations = map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"}
	sts.Spec.Template.Spec.Containers = []corev1.Container{{Name: util.DatabaseContainerName, Image: "busybox"}}
	require.NoError(t, kubeClient.CreateStatefulSet(ctx, sts))

	res, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	// first pass after re-adoption ends with a short requeue to configure monitoring;
	// the point is that it deploys the StatefulSet instead of blocking on the agent goal-state
	// wait which the CR-shaped pods can never satisfy
	assert.Equal(t, reconcile.Result{RequeueAfter: time.Second}, res, "reconcile must not block on the agent goal-state wait for a CR-shaped StatefulSet")

	result, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()))
	require.NoError(t, err)
	containerNames := make([]string, 0, len(result.Spec.Template.Spec.Containers))
	for _, c := range result.Spec.Template.Spec.Containers {
		containerNames = append(containerNames, c.Name)
	}
	assert.Contains(t, containerNames, util.MongodbContainerName, "pod template must be rewritten to the internal-AppDB shape")
	assert.NotContains(t, containerNames, util.DatabaseContainerName, "the MongoDB-CR container must not survive the reshape")
	assert.NotContains(t, result.Annotations, util.AppDBReverseMigrationReadyAnnotation, "reverse migration annotation must be cleared after STS reshape")
	assert.NotContains(t, result.Annotations, util.AppDBMigrationReadyAnnotation, "forward migration annotation must not be present after STS reshape")
}

func TestEnsureAppDBStatefulSetOwnership(t *testing.T) {
	const foreignUID = "cr-uid-2222"

	mergeLabels := func(base map[string]string, extra map[string]string) map[string]string {
		merged := make(map[string]string, len(base)+len(extra))
		maps.Copy(merged, base)
		maps.Copy(merged, extra)
		return merged
	}

	tests := []struct {
		name                      string
		sts                       func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet
		expectedLabels            func(testOm *omv1.MongoDBOpsManager) map[string]string
		expectedOwnerReferences   func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference
		expectedOwned             bool
		expectedReverseAnnotation bool
		expectedForwardAnnotation bool
		multiCluster              bool
		centralObjects            func(testOm *omv1.MongoDBOpsManager) []client.Object
	}{
		{
			name:          "StatefulSet absent: recreate-from-scratch path proceeds",
			expectedOwned: true,
		},
		{
			name: "label-owned StatefulSet proceeds even with a foreign ownerReference",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(mergeLabels(map[string]string{"app": "demo"}, testOm.GetOwnerLabels())).
					SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID(foreignUID)}}).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return mergeLabels(map[string]string{"app": "demo"}, testOm.GetOwnerLabels())
			},
			expectedOwnerReferences: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID(foreignUID)}}
			},
			expectedOwned: true,
		},
		{
			name: "foreign label requests release and blocks even with an OM ownerReference",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: testOm.Namespace + "-" + testOm.Spec.AppDB.Name(), "app": "demo"}).
					SetOwnerReferences(kube.BaseOwnerReference(testOm)).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: testOm.Namespace + "-" + testOm.Spec.AppDB.Name(), "app": "demo"}
			},
			expectedOwnerReferences: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			centralObjects: func(testOm *omv1.MongoDBOpsManager) []client.Object {
				return []client.Object{&mdbmultiv1.MongoDBMultiCluster{
					ObjectMeta: metav1.ObjectMeta{Name: testOm.Spec.AppDB.Name(), Namespace: testOm.Namespace},
				}}
			},
			expectedOwned:             false,
			expectedReverseAnnotation: true,
		},
		{
			name:         "orphaned multi-cluster owner label is reclaimed",
			multiCluster: true,
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(map[string]string{
						util.MongoDBMultiClusterResourceOwnerLabel: testOm.Namespace + "-" + testOm.Spec.AppDB.Name(),
						"app": "demo",
					}).
					SetAnnotations(map[string]string{
						util.AppDBMigrationReadyAnnotation:        trueString,
						util.AppDBReverseMigrationReadyAnnotation: trueString,
					}).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return mergeLabels(map[string]string{"app": "demo"}, testOm.GetOwnerLabels())
			},
			expectedOwnerReferences: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return testOm.AppDBOwnerReferenceForMemberCluster()
			},
			expectedOwned: true,
		},
		{
			name:         "live multi-cluster owner label requests release",
			multiCluster: true,
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(map[string]string{
						util.MongoDBMultiClusterResourceOwnerLabel: testOm.Namespace + "-" + testOm.Spec.AppDB.Name(),
						"app": "demo",
					}).
					SetAnnotations(map[string]string{util.AppDBMigrationReadyAnnotation: trueString}).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return map[string]string{
					util.MongoDBMultiClusterResourceOwnerLabel: testOm.Namespace + "-" + testOm.Spec.AppDB.Name(),
					"app": "demo",
				}
			},
			centralObjects: func(testOm *omv1.MongoDBOpsManager) []client.Object {
				return []client.Object{&mdbmultiv1.MongoDBMultiCluster{
					ObjectMeta: metav1.ObjectMeta{Name: testOm.Spec.AppDB.Name(), Namespace: testOm.Namespace},
				}}
			},
			expectedOwned:             false,
			expectedReverseAnnotation: true,
		},
		{
			name: "ownerless with release request adopts and clears migration annotations",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(map[string]string{"app": "demo"}).
					SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID(foreignUID)}}).
					SetAnnotations(map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"}).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return mergeLabels(map[string]string{"app": "demo"}, testOm.GetOwnerLabels())
			},
			expectedOwnerReferences: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			expectedOwned: true,
		},
		{
			name: "ownerless with stale forward annotation adopts and clears it",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(map[string]string{"app": "demo"}).
					SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID(foreignUID)}}).
					SetAnnotations(map[string]string{util.AppDBMigrationReadyAnnotation: "true"}).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return mergeLabels(map[string]string{"app": "demo"}, testOm.GetOwnerLabels())
			},
			expectedOwnerReferences: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			expectedOwned: true,
		},
		{
			name: "stale Ops Manager owner label is overwritten at reclaim",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(map[string]string{util.MongoDBOpsManagerResourceOwnerLabel: "other-om", "app": "demo"}).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return mergeLabels(map[string]string{"app": "demo"}, testOm.GetOwnerLabels())
			},
			expectedOwnerReferences: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			expectedOwned: true,
		},
		{
			name: "legacy own ownerReference without the owner label is backfilled and adopted without a write",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
					SetLabels(map[string]string{"app": "demo"}).
					SetOwnerReferences(kube.BaseOwnerReference(testOm)).
					Build()
			},
			expectedLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return map[string]string{"app": "demo"}
			},
			expectedOwnerReferences: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			expectedOwned: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			opsManagerBuilder := DefaultOpsManagerBuilder().SetName("test-om")
			if tt.multiCluster {
				opsManagerBuilder = opsManagerBuilder.
					SetAppDbMembers(0).
					SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
					SetAppDBClusterSpecList(mdbv1.ClusterSpecList{{ClusterName: "cluster-1", Members: 3}})
			}
			testOm := opsManagerBuilder.Build()
			testOm.UID = types.UID("om-uid-1111")

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
			statefulSetClient := client.Client(kubeClient)
			var reconciler *ReconcileAppDbReplicaSet
			var err error
			if tt.multiCluster {
				memberClustersMap := getAppDBFakeMultiClusterMapWithClusters([]string{"cluster-1"}, omConnectionFactory)
				reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, testOm, memberClustersMap, zap.S(), omConnectionFactory.GetConnectionFunc)
				statefulSetClient = memberClustersMap["cluster-1"]
			} else {
				reconciler, err = newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
			}
			require.NoError(t, err)
			if tt.centralObjects != nil {
				for _, obj := range tt.centralObjects(testOm) {
					require.NoError(t, kubeClient.Create(ctx, obj))
				}
			}
			if tt.sts != nil {
				sts := tt.sts(testOm)
				require.NoError(t, statefulSetClient.Create(ctx, &sts))
			}

			ownershipStatus := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm, zap.S())
			assert.Equal(t, tt.expectedOwned, ownershipStatus.IsOK())
			result, err := ownershipStatus.ReconcileResult()
			require.NoError(t, err)
			if tt.expectedOwned {
				assert.Equal(t, 24*time.Hour, result.RequeueAfter)
			} else {
				assert.Equal(t, 10*time.Second, result.RequeueAfter)
			}

			if tt.sts != nil {
				resultSts := appsv1.StatefulSet{}
				require.NoError(t, statefulSetClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.NameForCluster(0)), &resultSts))
				if tt.expectedLabels != nil {
					assert.Equal(t, tt.expectedLabels(testOm), resultSts.Labels)
				}
				if tt.expectedOwnerReferences != nil {
					assert.Equal(t, tt.expectedOwnerReferences(testOm), resultSts.OwnerReferences)
				}
				assert.Equal(t, tt.expectedReverseAnnotation, resultSts.Annotations[util.AppDBReverseMigrationReadyAnnotation] == "true")
				assert.Equal(t, tt.expectedForwardAnnotation, resultSts.Annotations[util.AppDBMigrationReadyAnnotation] == "true")
			}
		})
	}
}

func TestEnsureAppDBStatefulSetOwnership_ClaimsSharedSecretsOnAdoption(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetName("test-om").Build()
	testOm.UID = types.UID("om-uid-1111")

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	sts := DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
		SetOwnerReferences(nil).
		SetAnnotations(map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"}).Build()
	require.NoError(t, kubeClient.Create(ctx, &sts))

	crOwnerRef := []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: "cr-uid-2222"}}
	for _, name := range []string{omv1.OpsManagerUserPasswordSecretName("test-om-db"), testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name} {
		s := secret.Builder().SetName(name).SetNamespace(testOm.Namespace).SetField("k", "v").SetOwnerReferences(crOwnerRef).Build()
		require.NoError(t, kubeClient.CreateSecret(ctx, s))
	}

	ownershipStatus := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm, zap.S())
	require.True(t, ownershipStatus.IsOK())

	for _, name := range []string{omv1.OpsManagerUserPasswordSecretName("test-om-db"), testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name} {
		s := corev1.Secret{}
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, name), &s))
		require.Len(t, s.OwnerReferences, 1, name)
		assert.Equal(t, testOm.UID, s.OwnerReferences[0].UID, "secret %s must be claimed by the OM at adoption", name)
	}
}

type unauthorizedGetHostsOMConnection struct {
	*om.MockedOmConnection
}

func (c unauthorizedGetHostsOMConnection) GetHosts(context.Context) (*host.Result, error) {
	return nil, errors.New(unauthorizedOMError)
}

const unauthorizedOMError = "Status: 401 (Unauthorized), Detail: You are not authorized for this resource."

func TestReconcileAppDB_DeploysAppDBWhenOpsManagerReturnsUnauthorized(t *testing.T) {
	tests := map[string]struct {
		unauthorized            bool
		expectedOMStatusPhase   status.Phase
		expectedOMStatusMessage string
	}{
		"Ops Manager reachable": {
			unauthorized: false,
		},
		"Ops Manager returns 401": {
			unauthorized:            true,
			expectedOMStatusPhase:   status.PhaseFailed,
			expectedOMStatusMessage: "The admin-key secret might be corrupted: error registering hosts with project: error fetching existing hosts: " + unauthorizedOMError,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			opsManager := DefaultOpsManagerBuilder().Build()
			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
			connectionFunc := omConnectionFactory.GetConnectionFunc
			if tc.unauthorized {
				connectionFunc = func(omContext *om.OMContext) om.Connection {
					return unauthorizedGetHostsOMConnection{omConnectionFactory.GetConnectionFunc(omContext).(*om.MockedOmConnection)}
				}
			}
			reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, connectionFunc, zap.S())
			require.NoError(t, err)
			require.NoError(t, createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "password"))
			createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

			_, err = reconciler.ReconcileAppDB(ctx, opsManager)
			require.NoError(t, err)

			sts := &appsv1.StatefulSet{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()), sts))

			updated := &omv1.MongoDBOpsManager{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Name), updated))
			assert.Equal(t, tc.expectedOMStatusPhase, updated.Status.OpsManagerStatus.Phase)
			assert.Equal(t, tc.expectedOMStatusMessage, updated.Status.OpsManagerStatus.Message)
		})
	}
}

func TestEnsureAppDBStatefulSetOwnership_ReclaimsSharedSecretsWhenStatefulSetIsAlreadyOwned(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetName("test-om").Build()
	testOm.UID = types.UID("om-uid-1111")

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	sts := DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
		SetLabels(map[string]string{util.MongoDBOpsManagerResourceOwnerLabel: testOm.GetName()}).
		Build()
	require.NoError(t, kubeClient.Create(ctx, &sts))

	crOwnerRef := []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: "cr-uid-2222"}}
	secretNames := []string{omv1.OpsManagerUserPasswordSecretName("test-om-db"), testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name}
	for _, name := range secretNames {
		s := secret.Builder().SetName(name).SetNamespace(testOm.Namespace).SetField("k", "v").SetOwnerReferences(crOwnerRef).Build()
		require.NoError(t, kubeClient.CreateSecret(ctx, s))
	}

	ownershipStatus := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm, zap.S())
	require.True(t, ownershipStatus.IsOK())

	for _, name := range secretNames {
		s := corev1.Secret{}
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, name), &s))
		require.Len(t, s.OwnerReferences, 1, name)
		assert.Equal(t, testOm.UID, s.OwnerReferences[0].UID, "secret %s must be reclaimed even when the StatefulSet is already owned", name)
	}
}

func TestEnsureAppDBStatefulSetOwnership_MultiCluster(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().
		SetName("test-om").
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{ClusterName: "cluster-1", Members: 3},
			{ClusterName: "cluster-2", Members: 3},
		}).
		Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	memberClustersMap := getAppDBFakeMultiClusterMapWithClusters([]string{"cluster-1", "cluster-2"}, omConnectionFactory)
	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, testOm, memberClustersMap, zap.S(), omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	cluster0Name := testOm.Spec.AppDB.NameForCluster(0)
	cluster1Name := testOm.Spec.AppDB.NameForCluster(1)
	foreignOwnerRef := []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID("foreign-uid-4444")}}

	// cluster-1: the external AppDB CR still exists, so its StatefulSet must be released, not reclaimed
	liveOwner := &mdbmultiv1.MongoDBMultiCluster{
		ObjectMeta: metav1.ObjectMeta{Name: testOm.Spec.AppDB.Name(), Namespace: testOm.Namespace},
	}
	require.NoError(t, kubeClient.Create(ctx, liveOwner))

	cluster0Sts := DefaultStatefulSetBuilder().SetName(cluster0Name).
		SetLabels(map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: testOm.Namespace + "-" + testOm.Spec.AppDB.Name(), "app": "demo"}).
		SetOwnerReferences(kube.BaseOwnerReference(testOm)).Build()
	require.NoError(t, memberClustersMap["cluster-1"].Create(ctx, &cluster0Sts))
	// cluster-2: ownerless, so the StatefulSet is reclaimed immediately
	cluster1Sts := DefaultStatefulSetBuilder().SetName(cluster1Name).
		SetLabels(map[string]string{"app": "demo"}).
		SetOwnerReferences(foreignOwnerRef).Build()
	require.NoError(t, memberClustersMap["cluster-2"].Create(ctx, &cluster1Sts))

	ownershipStatus := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm, zap.S())
	require.False(t, ownershipStatus.IsOK())
	result, err := ownershipStatus.ReconcileResult()
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, result.RequeueAfter)

	cluster0Result := appsv1.StatefulSet{}
	require.NoError(t, memberClustersMap["cluster-1"].Get(ctx, kube.ObjectKey(mock.TestNamespace, cluster0Name), &cluster0Result))
	assert.Equal(t, kube.BaseOwnerReference(testOm), cluster0Result.OwnerReferences)
	assert.Equal(t, map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: testOm.Namespace + "-" + testOm.Spec.AppDB.Name(), "app": "demo"}, cluster0Result.Labels)
	assert.Equal(t, "true", cluster0Result.Annotations[util.AppDBReverseMigrationReadyAnnotation])
	assert.NotContains(t, cluster0Result.Annotations, util.AppDBMigrationReadyAnnotation)

	cluster1Result := appsv1.StatefulSet{}
	require.NoError(t, memberClustersMap["cluster-2"].Get(ctx, kube.ObjectKey(mock.TestNamespace, cluster1Name), &cluster1Result))
	assert.Empty(t, cluster1Result.OwnerReferences)
	expectedLabels := map[string]string{"app": "demo"}
	maps.Copy(expectedLabels, testOm.GetOwnerLabels())
	assert.Equal(t, expectedLabels, cluster1Result.Labels)
	assert.NotContains(t, cluster1Result.Annotations, util.AppDBReverseMigrationReadyAnnotation)
	assert.NotContains(t, cluster1Result.Annotations, util.AppDBMigrationReadyAnnotation)
}

// The AppDB StatefulSet can lag behind in one member cluster while it rolls out; the gate must
// arbitrate only the clusters where it exists.
func TestEnsureAppDBStatefulSetOwnership_PartialRollout(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().
		SetName("test-om").
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{ClusterName: "cluster-1", Members: 3},
			{ClusterName: "cluster-2", Members: 3},
		}).
		Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	memberClustersMap := getAppDBFakeMultiClusterMapWithClusters([]string{"cluster-1", "cluster-2"}, omConnectionFactory)
	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, testOm, memberClustersMap, zap.S(), omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	ownedSts := DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.NameForCluster(0)).
		SetLabels(testOm.GetOwnerLabels()).Build()
	require.NoError(t, memberClustersMap["cluster-1"].Create(ctx, &ownedSts))

	ownershipStatus := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm, zap.S())
	require.True(t, ownershipStatus.IsOK())
}

func TestAllStatefulSetsExistsInValidState(t *testing.T) {
	ctx := context.Background()

	const appDBClusterName = "cluster-1"

	rows := []struct {
		name              string
		createStatefulSet bool
		reverseAnnotation bool
		createACSecret    bool
		createACConfigMap bool
		expected          bool
	}{
		{name: "missing StatefulSet is invalid", expected: false},
		{name: "reverse migration annotation is invalid", createStatefulSet: true, reverseAnnotation: true, createACSecret: true, createACConfigMap: true, expected: false},
		{name: "missing automation config Secret is invalid", createStatefulSet: true, createACConfigMap: true, expected: false},
		{name: "missing automation config version ConfigMap is invalid", createStatefulSet: true, createACSecret: true, expected: false},
		{name: "existing StatefulSet with automation config is valid", createStatefulSet: true, createACSecret: true, createACConfigMap: true, expected: true},
	}

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			opsManager := DefaultOpsManagerBuilder().
				SetName("test-om").
				SetAppDbMembers(0).
				SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
				SetAppDBClusterSpecList(mdbv1.ClusterSpecList{{ClusterName: appDBClusterName, Members: 3}}).
				Build()
			opsManager.UID = types.UID("om-uid-1111")

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
			memberClustersMap := getAppDBFakeMultiClusterMapWithClusters([]string{appDBClusterName}, omConnectionFactory)
			memberClient := memberClustersMap[appDBClusterName]

			if tt.createStatefulSet {
				sts := DefaultStatefulSetBuilder().SetName(opsManager.Spec.AppDB.NameForCluster(0)).Build()
				if tt.reverseAnnotation {
					sts.Annotations = map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}
				}
				require.NoError(t, memberClient.Create(ctx, &sts))
			}

			if tt.createACSecret {
				require.NoError(t, memberClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
					Name:      opsManager.Spec.AppDB.AutomationConfigSecretName(),
					Namespace: opsManager.Namespace,
				}}))
			}

			if tt.createACConfigMap {
				require.NoError(t, memberClient.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name:      opsManager.Spec.AppDB.AutomationConfigConfigMapName(),
					Namespace: opsManager.Namespace,
				}}))
			}

			reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClustersMap, zap.S(), omConnectionFactory.GetConnectionFunc)
			require.NoError(t, err)

			valid, err := reconciler.allStatefulSetsExistsInValidState(ctx, opsManager, zap.S())
			require.NoError(t, err)
			assert.Equal(t, tt.expected, valid)
		})
	}
}

const appDBMigrationLegacyPrefix = "appdb"

// appDBMigrationLegacySecurity is the AppDB security of a deployment that used user-provided
// certificates, with the same SCRAM authentication the migrated spec keeps.
func appDBMigrationLegacySecurity(authentication *mdbv1.Authentication, prefix string) *mdbv1.Security {
	return &mdbv1.Security{
		TLSConfig:                 &mdbv1.TLSConfig{Enabled: true, CA: "legacy-appdb-ca"},
		CertificatesSecretsPrefix: prefix,
		Authentication:            authentication,
	}
}

func appDBMigrationManagedSecurity(authentication *mdbv1.Authentication) *mdbv1.Security {
	return &mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
		Authentication:     authentication,
	}
}

// markManagedCertificatesReadyForMigration sets the Ready condition through the status subresource,
// which the mock default fake client registers for Certificate (a plain Update would drop it).
func markManagedCertificatesReadyForMigration(t *testing.T, ctx context.Context, c kubernetesClient.Client) {
	t.Helper()
	certificates := listManagedCertificateObjects(t, ctx, c)
	require.NotEmpty(t, certificates)
	for i := range certificates {
		certificate := certificates[i]
		certificate.Status.Conditions = []certmanagerv1.CertificateCondition{{
			Type:               certmanagerv1.CertificateConditionReady,
			Status:             cmmeta.ConditionTrue,
			ObservedGeneration: certificate.Generation,
		}}
		require.NoError(t, c.Status().Update(ctx, &certificate))
	}
}

func readAppDBMigrationSecret(ctx context.Context, t *testing.T, c kubernetesClient.Client, name string) *corev1.Secret {
	t.Helper()
	secretObj := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(mock.TestNamespace, name), secretObj))
	return secretObj
}

// TestAppDBManagedCertificateMigrationSameCA walks the AppDB half of a same-CA migration from
// legacy TLS to operator-managed certificates. The StatefulSet is updated before the automation
// config, so the managed -pem Secret has to still hold the file the old automation config names.
func TestAppDBManagedCertificateMigrationSameCA(t *testing.T) {
	ctx := t.Context()
	opsManager := newAppDBManagedCertificateTestOM()
	authentication := opsManager.Spec.AppDB.Security.Authentication
	opsManager.Spec.AppDB.Security = appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix)

	legacySecretName := opsManager.Spec.AppDB.GetSecurity().MemberCertificateSecretName(opsManager.Spec.AppDB.Name())
	legacyPEMSecretName := legacySecretName + certs.OperatorGeneratedCertSuffix
	managedSecretName := appDBMigrationManagedSecurity(authentication).MemberCertificateSecretName(opsManager.Spec.AppDB.Name())
	managedPEMSecretName := managedSecretName + certs.OperatorGeneratedCertSuffix

	// The legacy deployment's own leaf and CA, signed by the CA the migration keeps.
	migrationCA, issue := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, appDBManagedHostnames(opsManager)...)
	legacyCRT, legacyKey := issue()
	legacyTLSSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: legacySecretName, Namespace: mock.TestNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": legacyCRT, "tls.key": legacyKey},
	}
	legacyCAConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-appdb-ca", Namespace: mock.TestNamespace},
		Data:       map[string]string{tlspkg.CAConfigMapKey: string(migrationCA)},
	}

	// Capture the managed PEM secret as it is right before the first StatefulSet write.
	var pemAtStatefulSetWrite map[string][]byte
	captureStatefulSetWrite := func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*appsv1.StatefulSet); ok && pemAtStatefulSetWrite == nil {
			pem := &corev1.Secret{}
			if err := c.Get(ctx, kube.ObjectKey(obj.GetNamespace(), managedPEMSecretName), pem); err == nil {
				pemAtStatefulSetWrite = pem.Data
			}
		}
		return c.Create(ctx, obj, opts...)
	}
	captureStatefulSetUpdate := func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if _, ok := obj.(*appsv1.StatefulSet); ok && pemAtStatefulSetWrite == nil {
			pem := &corev1.Secret{}
			if err := c.Get(ctx, kube.ObjectKey(obj.GetNamespace(), managedPEMSecretName), pem); err == nil {
				pemAtStatefulSetWrite = pem.Data
			}
		}
		return c.Update(ctx, obj, opts...)
	}

	omConnectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	fakeClientBuilder := mock.NewEmptyFakeClientBuilder()
	fakeClientBuilder.WithObjects(append([]client.Object{legacyTLSSecret, legacyCAConfigMap}, mock.GetDefaultResources()...)...)
	fakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
		Get:    mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
		Create: captureStatefulSetWrite,
		Update: captureStatefulSetUpdate,
	})
	kubeClient := kubernetesClient.NewClient(fakeClientBuilder.Build())

	// Phase 1: reconcile the legacy deployment, exactly as the operator did before the migration.
	createRunningAppDB(ctx, t, 3, kubeClient, opsManager, omConnectionFactory)

	legacyPEM := readAppDBMigrationSecret(ctx, t, kubeClient, legacyPEMSecretName)
	legacyHash := string(legacyPEM.Data[util.LatestHashSecretKey])
	require.NotEmpty(t, legacyHash, "the legacy PEM secret must name its current file")
	require.NotEmpty(t, legacyPEM.Data[legacyHash], "the legacy PEM secret must hold the file it names")

	legacyAC, err := automationconfig.ReadFromSecret(ctx, kubeClient, kube.ObjectKey(mock.TestNamespace, opsManager.Spec.AppDB.AutomationConfigSecretName()))
	require.NoError(t, err)
	require.NotEmpty(t, legacyAC.Processes)
	require.Equal(t,
		path.Join(util.SecretVolumeMountPath, "certs", legacyHash),
		legacyAC.Processes[0].Args26.Get("net.tls.certificateKeyFile").String(),
		"the legacy automation config must name the legacy certificate file")

	// Phase 2: migrate both security blocks in one update, keeping the legacy spec as the baseline.
	legacySpecBytes, err := json.Marshal(opsManager.Spec)
	require.NoError(t, err)
	persisted := &omv1.MongoDBOpsManager{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Name), persisted))
	persisted.Spec.AppDB.Security = appDBMigrationManagedSecurity(authentication)
	if persisted.Annotations == nil {
		persisted.Annotations = map[string]string{}
	}
	persisted.Annotations[util.LastAchievedSpec] = string(legacySpecBytes)
	require.NoError(t, kubeClient.Update(ctx, persisted))
	opsManager = persisted

	// Issue the operator-managed AppDB certificate and publish its CA bundle.
	omReconciler := newManagedCertificateTestReconciler(ctx, kubeClient)
	omReconciler.certManagerReadiness = certs.CertManagerReadiness{Ready: true}
	helper := newManagedCertificateTestHelper(t, omReconciler, opsManager)
	_, blocked, err := omReconciler.prepareManagedCertificates(ctx, opsManager, helper, zap.S())
	require.NoError(t, err)
	require.True(t, blocked)
	markManagedCertificatesReadyForMigration(t, ctx, kubeClient)
	createSecretsForManagedCertificates(t, ctx, kubeClient)
	writeMigrationManagedLeafFromIssue(t, ctx, kubeClient, managedSecretName, migrationCA, issue)
	appDBCASecret := readAppDBMigrationSecret(ctx, t, kubeClient, opsManager.AppDBName()+"-server-ca")
	appDBCASecret.Data = map[string][]byte{"tls.crt": migrationCA, "ca.crt": migrationCA}
	require.NoError(t, kubeClient.Update(ctx, appDBCASecret))
	_, blocked, err = omReconciler.prepareManagedCertificates(ctx, opsManager, helper, zap.S())
	require.NoError(t, err)
	require.False(t, blocked)

	// Phase 3: reconcile the migrated deployment. The StatefulSet is written first, so the PEM
	// secret observed at that moment is the one the pod mounts before the automation config moves.
	appDBReconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_, err = appDBReconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	require.NotNil(t, pemAtStatefulSetWrite, "the migrated AppDB StatefulSet must be written")
	managedHash := string(pemAtStatefulSetWrite[util.LatestHashSecretKey])
	require.NotEmpty(t, managedHash)
	require.NotEqual(t, legacyHash, managedHash, "the managed certificate must be a new file")
	require.Contains(t, pemAtStatefulSetWrite, legacyHash,
		"the managed PEM secret must still hold the file the old automation config names")
	require.Equal(t, legacyPEM.Data[legacyHash], pemAtStatefulSetWrite[legacyHash],
		"the retained legacy file must be byte-for-byte the one the pod used before")

	// The managed CA bundle keeps the fixed ca-pem file the old automation config reads.
	bundleConfigMap := &corev1.ConfigMap{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(mock.TestNamespace, certs.ManagedServerCABundleConfigMapName(opsManager.Spec.AppDB.Name())), bundleConfigMap))
	require.Contains(t, bundleConfigMap.Data, tlspkg.CAConfigMapKey,
		"the managed CA bundle must expose the legacy ca-pem file name")
	require.NotEmpty(t, bundleConfigMap.Data[tlspkg.CAConfigMapKey])
	bundleHash := bundleConfigMap.Data[util.LatestHashSecretKey]
	require.NotEmpty(t, bundleHash)

	// The final automation config names the managed certificate and CA files.
	finalAC, err := automationconfig.ReadFromSecret(ctx, kubeClient, kube.ObjectKey(mock.TestNamespace, opsManager.Spec.AppDB.AutomationConfigSecretName()))
	require.NoError(t, err)
	require.NotEmpty(t, finalAC.Processes)
	require.Equal(t,
		path.Join(util.SecretVolumeMountPath, "certs", managedHash),
		finalAC.Processes[0].Args26.Get("net.tls.certificateKeyFile").String(),
		"the automation config must select the managed certificate")
	require.NotNil(t, finalAC.TLSConfig)
	require.Equal(t, path.Join(util.ConfigMapVolumeCAMountPath, bundleHash), finalAC.TLSConfig.CAFilePath,
		"the automation config must select the managed CA file")

	// The StatefulSet mounts the managed secrets, and the legacy source is never rewritten.
	statefulSet := &appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(mock.TestNamespace, opsManager.Spec.AppDB.Name()), statefulSet))
	volumes := map[string]corev1.Volume{}
	for _, volume := range statefulSet.Spec.Template.Spec.Volumes {
		volumes[volume.Name] = volume
	}
	require.Contains(t, volumes, util.SecretVolumeName)
	require.NotNil(t, volumes[util.SecretVolumeName].Secret)
	require.Equal(t, managedPEMSecretName, volumes[util.SecretVolumeName].Secret.SecretName)
	require.Contains(t, volumes, "secret-ca")
	require.NotNil(t, volumes["secret-ca"].ConfigMap)
	require.Equal(t, certs.ManagedServerCABundleConfigMapName(opsManager.Spec.AppDB.Name()), volumes["secret-ca"].ConfigMap.Name)

	require.Equal(t, legacyPEM.Data, readAppDBMigrationSecret(ctx, t, kubeClient, legacyPEMSecretName).Data,
		"the legacy PEM secret must not be rewritten")
}

// TestAppDBManagedCertificateMigrationPreconditions covers when the AppDB migration seeding must
// run and when it must refuse or skip, without running the whole AppDB reconcile.
func TestAppDBManagedCertificateMigrationPreconditions(t *testing.T) {
	serializeSecuritySpec := func(t *testing.T, appDBSecurity *mdbv1.Security) string {
		t.Helper()
		bytes, err := json.Marshal(omv1.MongoDBOpsManagerSpec{
			Version:  "7.0.0",
			Replicas: 1,
			AppDB: &omv1.AppDBSpec{
				Version:  "6.0.0",
				Members:  3,
				Security: appDBSecurity,
			},
		})
		require.NoError(t, err)
		return string(bytes)
	}

	legacyPEMData := func() map[string][]byte {
		return map[string][]byte{util.LatestHashSecretKey: []byte("legacy"), "legacy": []byte("legacy cert")}
	}

	tests := []struct {
		name              string
		annotation        func(t *testing.T, authentication *mdbv1.Authentication) *string
		currentSecurity   func(authentication *mdbv1.Authentication) *mdbv1.Security
		legacyPEM         map[string][]byte
		externalAppDB     bool
		useZeroReconciler bool
		wantSeeded        string
		wantFailed        bool
	}{
		{
			name:            "fresh deployment without a baseline does not seed",
			currentSecurity: appDBMigrationManagedSecurity,
		},
		{
			name: "achieved TLS-off does not seed",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, &mdbv1.Security{Authentication: authentication})
				return &raw
			},
			currentSecurity: appDBMigrationManagedSecurity,
		},
		{
			name: "an already managed deployment does not seed",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, appDBMigrationManagedSecurity(authentication))
				return &raw
			},
			currentSecurity: appDBMigrationManagedSecurity,
		},
		{
			name: "an unchanged legacy deployment does not seed",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix))
				return &raw
			},
			currentSecurity: func(authentication *mdbv1.Authentication) *mdbv1.Security {
				return appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix)
			},
		},
		{
			name: "a referenced external AppDB does not seed",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix))
				return &raw
			},
			currentSecurity:   appDBMigrationManagedSecurity,
			externalAppDB:     true,
			useZeroReconciler: true,
		},
		{
			name:            "a malformed baseline fails",
			annotation:      func(t *testing.T, authentication *mdbv1.Authentication) *string { raw := "{not json"; return &raw },
			currentSecurity: appDBMigrationManagedSecurity,
			wantFailed:      true,
		},
		{
			name: "a migration seeds the managed PEM secret with the legacy file",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix))
				return &raw
			},
			currentSecurity: appDBMigrationManagedSecurity,
			legacyPEM:       legacyPEMData(),
			wantSeeded:      "managed-om-db-managed-cert-pem",
		},
		{
			name: "a detected migration without the legacy PEM fails",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix))
				return &raw
			},
			currentSecurity: appDBMigrationManagedSecurity,
			wantFailed:      true,
		},
		{
			name: "a latest hash pointing at absent data fails",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix))
				return &raw
			},
			currentSecurity: appDBMigrationManagedSecurity,
			legacyPEM:       map[string][]byte{util.LatestHashSecretKey: []byte("legacy")},
			wantFailed:      true,
		},
		{
			name: "prefix-only legacy TLS and a custom name seed",
			annotation: func(t *testing.T, authentication *mdbv1.Authentication) *string {
				raw := serializeSecuritySpec(t, &mdbv1.Security{CertificatesSecretsPrefix: "custom-prefix", Authentication: authentication})
				return &raw
			},
			currentSecurity: appDBMigrationManagedSecurity,
			legacyPEM: func() map[string][]byte {
				return map[string][]byte{util.LatestHashSecretKey: []byte("custom"), "custom": []byte("custom cert")}
			}(),
			wantSeeded: "managed-om-db-managed-cert-pem",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			opsManager := newAppDBManagedCertificateTestOM()
			authentication := opsManager.Spec.AppDB.Security.Authentication
			opsManager.Spec.AppDB.Security = test.currentSecurity(authentication)
			if test.annotation != nil {
				if opsManager.Annotations == nil {
					opsManager.Annotations = map[string]string{}
				}
				opsManager.Annotations[util.LastAchievedSpec] = *test.annotation(t, authentication)
			}
			if test.externalAppDB {
				opsManager.Spec.AppDB = nil
				opsManager.Spec.ExternalAppDBRef = &omv1.ExternalAppDBRef{Name: "external-appdb", Kind: omv1.ExternalAppDBRefKindMongoDB}
			}

			kubeClient := newManagedCertificateKubeClient(t, opsManager)
			if test.legacyPEM != nil {
				legacyName := fmt.Sprintf("%s-managed-om-db-cert", appDBMigrationLegacyPrefix)
				if test.name == "prefix-only legacy TLS and a custom name seed" {
					legacyName = "custom-prefix-managed-om-db-cert"
				}
				require.NoError(t, kubeClient.Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: legacyName + certs.OperatorGeneratedCertSuffix, Namespace: mock.TestNamespace},
					Data:       test.legacyPEM,
				}))
				if test.wantSeeded != "" {
					hash := string(test.legacyPEM[util.LatestHashSecretKey])
					writeAppDBPublishedCertificateFile(t, ctx, kubeClient, opsManager, hash)
					ca, issue := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
					test.legacyPEM[hash] = migrationServerCertificate(t, issue)
					require.NoError(t, kubeClient.Update(ctx, &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: legacyName + certs.OperatorGeneratedCertSuffix, Namespace: mock.TestNamespace},
						Data:       test.legacyPEM,
					}))
					require.NoError(t, kubeClient.Create(ctx, &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: opsManager.Spec.AppDB.GetCAConfigMapName(), Namespace: opsManager.Namespace},
						Data:       map[string]string{util.LatestHashSecretKey: "active", "active": string(ca)},
					}))
				}
			}

			var reconciler *ReconcileAppDbReplicaSet
			if test.useZeroReconciler {
				reconciler = &ReconcileAppDbReplicaSet{}
			} else {
				var err error
				reconciler, err = newAppDbReconciler(ctx, kubeClient, opsManager, nil, zap.S())
				require.NoError(t, err)
			}

			status := reconciler.seedManagedPEMSecretsFromLegacy(ctx, opsManager, zap.S())
			if test.wantFailed {
				require.False(t, status.IsOK(), "a detected migration without usable legacy material must fail")
				unexpected := &corev1.Secret{}
				require.True(t, apiErrors.IsNotFound(kubeClient.Get(ctx, kube.ObjectKey(mock.TestNamespace, "managed-om-db-managed-cert-pem"), unexpected)),
					"a failed precondition must not write the managed PEM secret")
				return
			}
			require.True(t, status.IsOK())

			managedPEM := &corev1.Secret{}
			err := kubeClient.Get(ctx, kube.ObjectKey(mock.TestNamespace, "managed-om-db-managed-cert-pem"), managedPEM)
			if test.wantSeeded == "" {
				require.True(t, apiErrors.IsNotFound(err), "no managed PEM secret must be created")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantSeeded, managedPEM.Name)

			legacyHash := string(test.legacyPEM[util.LatestHashSecretKey])
			if legacyHash == "" {
				for key := range test.legacyPEM {
					legacyHash = key
				}
			}
			require.Equal(t, legacyHash, string(managedPEM.Data[util.LatestHashSecretKey]))
			require.Equal(t, test.legacyPEM[legacyHash], managedPEM.Data[legacyHash])
		})
	}
}

// newAppDBMigrationSeedTestOM returns a managed AppDB migration with the legacy security as the
// successful checkpoint.
func newAppDBMigrationSeedTestOM(t *testing.T) (*omv1.MongoDBOpsManager, *mdbv1.Security, *mdbv1.Security) {
	t.Helper()
	om := newAppDBManagedCertificateTestOM()
	authentication := om.Spec.AppDB.Security.Authentication
	lastSecurity := appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix)
	om.Spec.AppDB.Security = appDBMigrationManagedSecurity(authentication)
	lastSpec, err := json.Marshal(omv1.MongoDBOpsManagerSpec{
		Version:  om.Spec.Version,
		Replicas: om.Spec.Replicas,
		AppDB: &omv1.AppDBSpec{
			Version:  om.Spec.AppDB.Version,
			Members:  om.Spec.AppDB.Members,
			Security: lastSecurity,
		},
	})
	require.NoError(t, err)
	om.Annotations = map[string]string{util.LastAchievedSpec: string(lastSpec)}
	return om, lastSecurity, om.Spec.AppDB.GetSecurity()
}

// writeAppDBPublishedCertificateFile publishes an AppDB automation config whose TLS certificate
// file is the given hash.
func writeAppDBPublishedCertificateFile(t *testing.T, ctx context.Context, c kubernetesClient.Client, om *omv1.MongoDBOpsManager, hash string) {
	t.Helper()
	ac := automationconfig.AutomationConfig{Processes: []automationconfig.Process{{Name: "appdb-0"}}}
	ac.Processes[0].SetArgs26Field("net.tls.certificateKeyFile", path.Join(util.SecretVolumeMountPath, "certs", hash))
	data, err := json.Marshal(ac)
	require.NoError(t, err)
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: om.Spec.AppDB.AutomationConfigSecretName(), Namespace: om.Namespace},
		Data:       map[string][]byte{automationconfig.ConfigKey: data},
	}))
}

// TestAppDBManagedCertificateMigrationSeedFailsOnMissingSource verifies the validated member seed
// fails on the same source read when the latest certificate file is absent or empty.
func TestAppDBManagedCertificateMigrationSeedFailsOnMissingSource(t *testing.T) {
	ctx := t.Context()
	managedCA, _ := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})

	for _, tc := range []struct {
		name   string
		source func(t *testing.T, c kubernetesClient.Client, om *omv1.MongoDBOpsManager, lastSecurity *mdbv1.Security)
	}{
		{name: "missing source Secret"},
		{
			name: "empty latest file",
			source: func(t *testing.T, c kubernetesClient.Client, om *omv1.MongoDBOpsManager, lastSecurity *mdbv1.Security) {
				writeMigrationSecret(t, ctx, c, lastSecurity.MemberCertificateSecretName(om.Spec.AppDB.Name())+certs.OperatorGeneratedCertSuffix, map[string][]byte{
					util.LatestHashSecretKey: []byte("published"),
					"published":              {},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			om, lastSecurity, currentSecurity := newAppDBMigrationSeedTestOM(t)
			c := newManagedCertificateKubeClient(t, om)
			require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: om.Spec.AppDB.GetCAConfigMapName(), Namespace: om.Namespace},
				Data:       map[string]string{util.LatestHashSecretKey: "active", "active": string(managedCA)},
			}))
			if tc.source != nil {
				tc.source(t, c, om, lastSecurity)
			}
			r, err := newAppDbReconciler(ctx, c, om, nil, zap.S())
			require.NoError(t, err)

			status := r.seedManagedPEMSecretsFromLegacy(ctx, om, zap.S())
			require.False(t, status.IsOK())
			require.True(t, apiErrors.IsNotFound(c.Get(ctx, kube.ObjectKey(om.Namespace, currentSecurity.MemberCertificateSecretName(om.Spec.AppDB.Name())+certs.OperatorGeneratedCertSuffix), &corev1.Secret{})),
				"a failed source read must not write the managed destination")
		})
	}
}

func TestAppDBManagedCertificateMigrationSeedLeavesExistingDestinationAlone(t *testing.T) {
	ctx := t.Context()
	_, issueManaged := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	_, issueLegacy := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	managedLeaf := migrationServerCertificate(t, issueManaged)
	legacyTrusted := migrationServerCertificate(t, issueManaged)
	legacyUntrusted := migrationServerCertificate(t, issueLegacy)

	setup := func(t *testing.T) (*omv1.MongoDBOpsManager, *mdbv1.Security, kubernetesClient.Client, *ReconcileAppDbReplicaSet, *corev1.Secret) {
		t.Helper()
		om, lastSecurity, currentSecurity := newAppDBMigrationSeedTestOM(t)
		legacyBaseName := lastSecurity.MemberCertificateSecretName(om.Spec.AppDB.Name())
		managedBaseName := currentSecurity.MemberCertificateSecretName(om.Spec.AppDB.Name())
		c := newManagedCertificateKubeClient(t, om)
		writeMigrationLegacyPEM(t, ctx, c, legacyBaseName, legacyTrusted)
		destination := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: managedBaseName + certs.OperatorGeneratedCertSuffix, Namespace: om.Namespace},
			Data: map[string][]byte{
				util.LatestHashSecretKey:   []byte("managed"),
				"managed":                  managedLeaf,
				util.PreviousHashSecretKey: []byte("legacy"),
				"legacy":                   legacyUntrusted,
			},
		}
		require.NoError(t, c.Create(ctx, destination))
		r, err := newAppDbReconciler(ctx, c, om, nil, zap.S())
		require.NoError(t, err)
		return om, lastSecurity, c, r, destination
	}

	t.Run("an existing destination is not reseeded", func(t *testing.T) {
		om, _, c, r, destination := setup(t)
		before := destination.DeepCopy()

		require.True(t, r.seedManagedPEMSecretsFromLegacy(ctx, om, zap.S()).IsOK())
		after := &corev1.Secret{}
		require.NoError(t, c.Get(ctx, kube.ObjectKey(destination.Namespace, destination.Name), after))
		assert.Equal(t, before.Data, after.Data, "the idempotent seed must not rewrite or rewind the destination")
	})

	t.Run("seeding is a no-op without the legacy source on retry", func(t *testing.T) {
		om, lastSecurity, c, r, destination := setup(t)
		legacySource := lastSecurity.MemberCertificateSecretName(om.Spec.AppDB.Name()) + certs.OperatorGeneratedCertSuffix
		require.NoError(t, c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: legacySource, Namespace: om.Namespace}}))
		before := destination.DeepCopy()

		require.True(t, r.seedManagedPEMSecretsFromLegacy(ctx, om, zap.S()).IsOK())
		after := &corev1.Secret{}
		require.NoError(t, c.Get(ctx, kube.ObjectKey(destination.Namespace, destination.Name), after))
		assert.Equal(t, before.Data, after.Data)
	})
}

// TestAppDBManagedCertificateMigrationSeedingIsStable re-runs the seeding from a reconstructed
// reconciler: the managed latest hash, the legacy source and the baseline annotation must survive.
func TestAppDBManagedCertificateMigrationSeedingIsStable(t *testing.T) {
	ctx := t.Context()
	opsManager := newAppDBManagedCertificateTestOM()
	authentication := opsManager.Spec.AppDB.Security.Authentication
	opsManager.Spec.AppDB.Security = appDBMigrationManagedSecurity(authentication)
	legacySpec, err := json.Marshal(omv1.MongoDBOpsManagerSpec{
		Version:  "7.0.0",
		Replicas: 1,
		AppDB: &omv1.AppDBSpec{
			Version:  "6.0.0",
			Members:  3,
			Security: appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix),
		},
	})
	require.NoError(t, err)
	opsManager.Annotations = map[string]string{util.LastAchievedSpec: string(legacySpec)}

	kubeClient := newManagedCertificateKubeClient(t, opsManager)
	legacyPEMName := "appdb-managed-om-db-cert" + certs.OperatorGeneratedCertSuffix
	ca, issue := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	legacyPEM := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: legacyPEMName, Namespace: mock.TestNamespace},
		Data:       map[string][]byte{util.LatestHashSecretKey: []byte("legacy"), "legacy": migrationServerCertificate(t, issue)},
	}
	require.NoError(t, kubeClient.Create(ctx, legacyPEM))
	writeAppDBPublishedCertificateFile(t, ctx, kubeClient, opsManager, "legacy")
	require.NoError(t, kubeClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: opsManager.Spec.AppDB.GetCAConfigMapName(), Namespace: opsManager.Namespace},
		Data:       map[string]string{util.LatestHashSecretKey: "active", "active": string(ca)},
	}))

	seed := func() *ReconcileAppDbReplicaSet {
		r, err := newAppDbReconciler(ctx, kubeClient, opsManager, nil, zap.S())
		require.NoError(t, err)
		require.True(t, r.seedManagedPEMSecretsFromLegacy(ctx, opsManager, zap.S()).IsOK())
		return r
	}

	seed()
	managedPEMName := "managed-om-db-managed-cert-pem"
	seeded := readAppDBMigrationSecret(ctx, t, kubeClient, managedPEMName)

	// The normal writer then publishes the managed certificate.
	require.NoError(t, certs.CreateOrUpdatePEMSecretWithPreviousCert(ctx, secrets.SecretClient{KubeClient: kubeClient},
		kube.ObjectKey(mock.TestNamespace, "managed-om-db-managed-cert"), "managed", "managed cert", nil, nil, certs.AppDB))
	afterFirstWrite := readAppDBMigrationSecret(ctx, t, kubeClient, managedPEMName)
	require.Equal(t, string(afterFirstWrite.Data[util.LatestHashSecretKey]), "managed")

	// A reconstructed reconciler re-runs the seeding and the writer.
	seed()
	require.NoError(t, certs.CreateOrUpdatePEMSecretWithPreviousCert(ctx, secrets.SecretClient{KubeClient: kubeClient},
		kube.ObjectKey(mock.TestNamespace, "managed-om-db-managed-cert"), "managed", "managed cert", nil, nil, certs.AppDB))

	afterSecondWrite := readAppDBMigrationSecret(ctx, t, kubeClient, managedPEMName)
	require.Equal(t, afterFirstWrite.Data, afterSecondWrite.Data, "a retry must not rewind the managed latest hash")
	require.Contains(t, afterSecondWrite.Data, "legacy", "the retained legacy file must survive a retry")
	require.Equal(t, seeded.Data[util.LatestHashSecretKey], afterFirstWrite.Data[util.PreviousHashSecretKey])

	require.Equal(t, legacyPEM.Data, readAppDBMigrationSecret(ctx, t, kubeClient, legacyPEMName).Data,
		"the legacy source must not be rewritten")
	persisted := &omv1.MongoDBOpsManager{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Name), persisted))
	require.Equal(t, string(legacySpec), persisted.Annotations[util.LastAchievedSpec],
		"the pending baseline annotation must survive the retry")
}

// The MongoDB multi-cluster resources seed every member cluster from that cluster's own legacy
// -pem secret. The AppDB migration does the same: an MC AppDB keeps the file its old automation
// config names in every cluster it can reach.
func TestAppDBManagedCertificateMigrationMultiClusterSeedsEveryHealthyCluster(t *testing.T) {
	newOM := func(t *testing.T) (*omv1.MongoDBOpsManager, kubernetesClient.Client, *OpsManagerReconciler, *mdbv1.Security) {
		t.Helper()
		opsManager, central, omReconciler := newMultiClusterManagedAppDB(t)
		authentication := opsManager.Spec.AppDB.Security.Authentication
		lastSecurity := appDBMigrationLegacySecurity(authentication, appDBMigrationLegacyPrefix)
		lastSpec, err := json.Marshal(omv1.MongoDBOpsManagerSpec{
			Version:  "7.0.0",
			Replicas: 1,
			AppDB: &omv1.AppDBSpec{
				Version:         "6.0.0",
				Members:         3,
				Topology:        omv1.ClusterTopologyMultiCluster,
				ClusterSpecList: opsManager.Spec.AppDB.ClusterSpecList,
				Security:        lastSecurity,
			},
		})
		require.NoError(t, err)
		opsManager.Annotations = map[string]string{util.LastAchievedSpec: string(lastSpec)}
		return opsManager, central, omReconciler, lastSecurity
	}

	assertSeeded := func(t *testing.T, ctx context.Context, opsManager *omv1.MongoDBOpsManager, memberClusters map[string]client.Client, legacy []byte) {
		t.Helper()
		managedName := opsManager.Spec.AppDB.GetSecurity().MemberCertificateSecretName(opsManager.Spec.AppDB.Name()) + certs.OperatorGeneratedCertSuffix
		require.NotEmpty(t, memberClusters)
		for name, memberClient := range memberClusters {
			managed := &corev1.Secret{}
			require.NoError(t, memberClient.Get(ctx, kube.ObjectKey(mock.TestNamespace, managedName), managed), name)
			require.Equal(t, "legacy-hash", string(managed.Data[util.LatestHashSecretKey]), name)
			require.Equal(t, legacy, managed.Data["legacy-hash"], name)
		}
	}

	t.Run("every member cluster seeds latest-hash even when its automation config names an older file", func(t *testing.T) {
		ctx := t.Context()
		opsManager, central, omReconciler, lastSecurity := newOM(t)
		managedCA, issueManaged := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
		legacyLeaf := migrationServerCertificate(t, issueManaged)
		legacyBaseName := lastSecurity.MemberCertificateSecretName(opsManager.Spec.AppDB.Name())
		ac := automationconfig.AutomationConfig{Processes: []automationconfig.Process{{Name: "appdb-0"}}}
		ac.Processes[0].SetArgs26Field("net.tls.certificateKeyFile", fmt.Sprintf("%s/certs/%s", util.SecretVolumeMountPath, "previous-hash"))
		acBytes, err := json.Marshal(ac)
		require.NoError(t, err)
		for name, memberClient := range omReconciler.memberClustersMap {
			require.NoError(t, memberClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: legacyBaseName + certs.OperatorGeneratedCertSuffix, Namespace: mock.TestNamespace},
				Data: map[string][]byte{
					util.LatestHashSecretKey: []byte("legacy-hash"), "legacy-hash": legacyLeaf,
					util.PreviousHashSecretKey: []byte("previous-hash"), "previous-hash": legacyLeaf,
				},
			}), name)
			// The automation config is published per member cluster, not into the operator's cluster.
			require.NoError(t, memberClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: opsManager.Spec.AppDB.AutomationConfigSecretName(), Namespace: opsManager.Namespace},
				Data:       map[string][]byte{automationconfig.ConfigKey: acBytes},
			}), name)
		}
		// The validator resolves the managed CA bundle through the operator's cluster.
		require.NoError(t, central.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: opsManager.Spec.AppDB.GetCAConfigMapName(), Namespace: opsManager.Namespace},
			Data:       map[string]string{util.LatestHashSecretKey: "active", "active": string(managedCA)},
		}))

		reconciler, err := newAppDbMultiReconciler(ctx, central, opsManager, omReconciler.memberClustersMap, zap.S(), nil)
		require.NoError(t, err)
		require.True(t, reconciler.seedManagedPEMSecretsFromLegacy(ctx, opsManager, zap.S()).IsOK())
		assertSeeded(t, ctx, opsManager, omReconciler.memberClustersMap, legacyLeaf)
	})
}
