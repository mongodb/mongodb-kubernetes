package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

	"k8s.io/apimachinery/pkg/types"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/manifest"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const relativeVersionManifestFixturePath = "testdata/version_manifest.json"

const gitVersionFromTestData = "a57d8e71e6998a2d0afde7edc11bd23e5661c915"
const firstMdbVersionInTestManifest = "3.6.0"
const numberOfBuildsInFirstVersion = 2

func init() {
	util.BundledAppDbMongoDBVersion = "4.2.11-ent"
	mock.InitDefaultEnvVariables()
}

func TestMongoDB_ConnectionURL_DefaultCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	appdb := &opsManager.Spec.AppDB
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", appdb.ConnectionURL("user", "passwd", nil))

	// Special symbols in the url
	assert.Equal(t, "mongodb://special%2Fuser%23:%40passw%21@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", appdb.ConnectionURL("special/user#", "@passw!", nil))

	// Connection parameters. The default one is overridden
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		appdb.ConnectionURL("user", "passwd", map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}))
}

func TestMongoDB_ConnectionURL_OtherCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().SetClusterDomain("my-cluster").Build()
	appdb := &opsManager.Spec.AppDB
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", appdb.ConnectionURL("user", "passwd", nil))

	// Connection parameters. The default one is overridden
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		appdb.ConnectionURL("user", "passwd", map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}))
}

// TestAutomationConfig_IsCreatedInSecret verifies that the automation config is created in a secret.
func TestAutomationConfig_IsCreatedInSecret(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewManager(&opsManager)
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})

	createOpsManagerUserPasswordSecret(kubeManager.Client, opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	_, err := reconciler.Reconcile(&opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	assert.NoError(t, err)

	s, err := kubeManager.Client.GetSecret(kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err, "The Automation Config was created in a secret.")
	assert.Contains(t, s.Data, automationconfig.ConfigKey)
}

// TestPublishAutomationConfig_Create verifies that the automation config map is created if it doesn't exist
func TestPublishAutomationConfig_Create(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewEmptyManager()
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	automationConfig, err := buildAutomationConfigForAppDb(builder, kubeManager, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	version, err := reconciler.publishAutomationConfig(appdb, opsManager, automationConfig)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	// verify the secret was created
	acSecret := readAutomationConfigSecret(t, kubeManager, opsManager)
	checkDeploymentEqualToPublished(t, automationConfig, acSecret)
	assert.Len(t, kubeManager.Client.GetMapForObject(&corev1.Secret{}), 5)

	_, err = kubeManager.Client.GetSecret(kube.ObjectKey(opsManager.Namespace, appdb.GetOpsManagerUserPasswordSecretName()))
	assert.NoError(t, err)

	_, err = kubeManager.Client.GetSecret(kube.ObjectKey(opsManager.Namespace, appdb.GetAgentKeyfileSecretNamespacedName().Name))
	assert.NoError(t, err)

	_, err = kubeManager.Client.GetSecret(kube.ObjectKey(opsManager.Namespace, appdb.GetAgentPasswordSecretNamespacedName().Name))
	assert.NoError(t, err)

	_, err = kubeManager.Client.GetSecret(kube.ObjectKey(opsManager.Namespace, appdb.OpsManagerUserScramCredentialsName()))
	assert.NoError(t, err)

	_, err = kubeManager.Client.GetSecret(kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
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
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewEmptyManager()
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	automationConfig, err := buildAutomationConfigForAppDb(builder, kubeManager, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	// create
	version, err := reconciler.publishAutomationConfig(appdb, opsManager, automationConfig)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)
	kubeManager.Client.ClearHistory()

	ac, err := automationconfig.ReadFromSecret(reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)

	// publishing the config without updates should not result in API call
	version, err = reconciler.publishAutomationConfig(appdb, opsManager, ac)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)
	kubeManager.Client.CheckOperationsDidntHappen(t, mock.HItem(reflect.ValueOf(kubeManager.Client.Update), &corev1.Secret{}))

	ac, err = buildAutomationConfigForAppDb(builder, kubeManager, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)

	// publishing changed config will result in update
	ac.MonitoringVersions = append(automationConfig.MonitoringVersions, automationconfig.MonitoringVersion{
		Name: "new-version",
	})

	version, err = reconciler.publishAutomationConfig(appdb, opsManager, ac)
	assert.NoError(t, err)
	assert.Equal(t, 2, version)
	kubeManager.Client.CheckOrderOfOperations(t, mock.HItem(reflect.ValueOf(kubeManager.Client.Update), &corev1.Secret{}))

	// verify the configmap was updated (the version must get incremented)
	acSecret := readAutomationConfigSecret(t, kubeManager, opsManager)

	automationConfig.Version = 2
	checkDeploymentEqualToPublished(t, ac, acSecret)
}

func TestPublishAutomationConfig_ScramShaConfigured(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewEmptyManager()

	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	automationConfig, err := buildAutomationConfigForAppDb(builder, kubeManager, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	version, err := reconciler.publishAutomationConfig(appdb, opsManager, automationConfig)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	acSecret := readAutomationConfigSecret(t, kubeManager, opsManager)

	acBytes := acSecret.Data[util.AppDBAutomationConfigKey]

	ac, err := automationconfig.FromBytes(acBytes)
	assert.NoError(t, err)

	assert.NotEmpty(t, ac.Auth.Key, "key file content should have been generated")
	assert.NotEmpty(t, ac.Auth.AutoPwd, "automation agent password should have been generated")
	assert.False(t, ac.Auth.AuthoritativeSet, "authoritativeSet should be set to false")
	assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser, "agent should have default name")
	assert.True(t, stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(authentication.MongoDBCR)), "MONGODB-CR should be configured")
	assert.True(t, stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(authentication.MongoDBCR)), "MONGODB-CR should be configured")

	omUser := ac.Auth.Users[0]
	assert.Equal(t, omUser.Username, util.OpsManagerMongoDBUserName)
	assert.Equal(t, omUser.Database, util.DefaultUserDatabase)
	assert.NotNil(t, omUser, "ops manager user should have been created")
}

// TestBuildAppDbAutomationConfig checks that the automation config is built correctly
func TestBuildAppDbAutomationConfig(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.2.11-ent").
		SetAppDbFeatureCompatibility("4.0")
	om := builder.Build()

	manager := mock.NewManager(&om)
	createOpsManagerUserPasswordSecret(manager.Client, om, "omPass")

	automationConfig, err := buildAutomationConfigForAppDb(builder, manager, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)

	// processes
	assert.Len(t, automationConfig.Processes, 2)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[0].Version)
	assert.Equal(t, "test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[0].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[0].FeatureCompatibilityVersion)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[1].Version)
	assert.Equal(t, "test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[1].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[1].FeatureCompatibilityVersion)

	// replicasets
	assert.Len(t, automationConfig.ReplicaSets, 1)
	assert.Equal(t, builder.Build().Spec.AppDB.Name(), automationConfig.ReplicaSets[0].Id)

	// monitoring agent has been configured
	assert.Len(t, automationConfig.MonitoringVersions, 2)

	// backup agents have not been configured
	assert.Len(t, automationConfig.BackupVersions, 0)

	// options
	assert.Equal(t, automationconfig.Options{DownloadBase: util.AgentDownloadsDir}, automationConfig.Options)

	// we have only the bundled version here
	assert.Len(t, automationConfig.Versions, 3)

	threeSixZero := automationConfig.Versions[0]

	assert.Equal(t, firstMdbVersionInTestManifest, threeSixZero.Name)
	assert.Len(t, threeSixZero.Builds, numberOfBuildsInFirstVersion)

	// only checking 1st build data matches
	firstBuild := threeSixZero.Builds[0]
	assert.Equal(t, "linux", firstBuild.Platform)
	assert.Equal(t, gitVersionFromTestData, firstBuild.GitVersion)
	assert.Equal(t, "amd64", firstBuild.Architecture)
	assert.Equal(t, "ubuntu", firstBuild.Flavor)
	assert.Equal(t, "14.04", firstBuild.MinOsVersion)
	assert.Equal(t, "15.04", firstBuild.MaxOsVersion)
	assert.Equal(t, "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu1404-3.6.0.tgz", firstBuild.Url)
	assert.Empty(t, firstBuild.Modules)

}

func TestBundledVersionManifestIsUsed_WhenSpecified(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion(util.BundledAppDbMongoDBVersion).
		SetAppDbFeatureCompatibility("4.0")
	om := builder.Build()

	automationConfig, err := buildAutomationConfigForAppDb(builder, mock.NewManager(&om), AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	mongodbVersion := automationConfig.Versions[0]
	mongodbBuilds := mongodbVersion.Builds
	firstBuild := mongodbBuilds[0]

	assert.Equal(t, firstBuild.Platform, "linux")
	assert.Equal(t, firstBuild.GitVersion, gitVersionFromTestData)
	assert.Equal(t, mongodbVersion.Name, firstMdbVersionInTestManifest)
	assert.Len(t, mongodbBuilds, numberOfBuildsInFirstVersion)
}

func TestBundledVersionManifestIsUsed_WhenVersionIsEmpty(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("").
		SetAppDbFeatureCompatibility("4.0")
	om := builder.Build()
	automationConfig, err := buildAutomationConfigForAppDb(builder, mock.NewManager(&om), AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	mongodbVersion := automationConfig.Versions[0]
	mongodbBuilds := mongodbVersion.Builds
	firstBuild := mongodbBuilds[0]

	assert.Equal(t, firstBuild.Platform, "linux")
	assert.Equal(t, firstBuild.GitVersion, gitVersionFromTestData)
	assert.Equal(t, mongodbVersion.Name, firstMdbVersionInTestManifest)
	assert.Len(t, mongodbBuilds, numberOfBuildsInFirstVersion)
}

func TestVersionManifestIsDownloaded_WhenNotUsingBundledVersion(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.1.2-ent").
		SetAppDbFeatureCompatibility("4.0")
	om := builder.Build()
	automationConfig, err := buildAutomationConfigForAppDb(builder, mock.NewManager(&om), manifest.InternetProvider{})
	if err != nil {
		// if failing, checking that the error is connectivity only
		assert.Contains(t, err.Error(), "dial tcp: lookup opsmanager.mongodb.com: no such host")
		return
	}

	// mongodb versions should be non empty
	assert.Greater(t, len(automationConfig.Versions), 0)

	// All versions before 3.6.0 are removed
	threeSix := automationConfig.Versions[0]
	assert.Equal(t, "3.6.0", threeSix.Name)
	assert.Equal(t, "linux", threeSix.Builds[0].Platform)
	assert.Equal(t, "a57d8e71e6998a2d0afde7edc11bd23e5661c915", threeSix.Builds[0].GitVersion)
	assert.Equal(t, "amd64", threeSix.Builds[0].Architecture)
	assert.Equal(t, "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-3.6.0.tgz", threeSix.Builds[0].Url)
	assert.Len(t, threeSix.Builds, 13)
	assert.Len(t, threeSix.Builds[0].Modules, 0)

	var fourTwoEnt automationconfig.MongoDbVersionConfig
	// seems like we cannot rely on the build by index - there used to be the "4.2.0-ent" on 234 position in the
	// builds array but later it was replaced by 4.2.0-rc8-ent and the test started failing..
	// So we try to find the version by name instead
	for _, v := range automationConfig.Versions {
		if v.Name == "4.2.0-ent" {
			fourTwoEnt = v
			break
		}
	}
	assert.Equal(t, "4.2.0-ent", fourTwoEnt.Name)
	assert.Equal(t, "linux", fourTwoEnt.Builds[13].Platform)
	assert.Equal(t, "a4b751dcf51dd249c5865812b390cfd1c0129c30", fourTwoEnt.Builds[13].GitVersion)
	assert.Equal(t, "amd64", fourTwoEnt.Builds[13].Architecture)
	assert.Equal(t, "ubuntu", fourTwoEnt.Builds[13].Flavor)
	assert.Equal(t, "18.04", fourTwoEnt.Builds[13].MinOsVersion)
	assert.Equal(t, "19.04", fourTwoEnt.Builds[13].MaxOsVersion)
	assert.Equal(t, "https://downloads.mongodb.com/linux/mongodb-linux-x86_64-enterprise-ubuntu1804-4.2.0.tgz", fourTwoEnt.Builds[13].Url)
	assert.Len(t, fourTwoEnt.Builds[13].Modules, 1)
}

func TestFetchingVersionManifestFails_WhenUsingNonBundledVersion(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.0.2-ent").
		SetAppDbFeatureCompatibility("4.0")
	om := builder.Build()
	_, err := buildAutomationConfigForAppDb(builder, mock.NewManager(&om), AlwaysFailingManifestProvider{})
	assert.Error(t, err)
}

func TestRegisterAppDBHostsWithProject(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeManager := mock.NewEmptyManager()
	client := kubeManager.Client
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	conn := om.NewMockedOmConnection(om.NewDeployment())

	appDbSts := construct.AppDbStatefulSet(opsManager)

	t.Run("Ensure all hosts are added", func(t *testing.T) {

		_ = client.Update(context.TODO(), &appDbSts)

		err := reconciler.registerAppDBHostsWithProject(&opsManager, conn, "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := conn.GetHosts()
		assert.Len(t, hosts.Results, 3)
	})

	t.Run("Ensure hosts are added when scaled up", func(t *testing.T) {
		appDbSts.Spec.Replicas = util.Int32Ref(5)
		_ = client.Update(context.TODO(), &appDbSts)

		err := reconciler.registerAppDBHostsWithProject(&opsManager, conn, "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := conn.GetHosts()
		assert.Len(t, hosts.Results, 5)
	})
}

func TestEnsureAppDbAgentApiKey(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeManager := mock.NewEmptyManager()
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})

	conn := om.NewMockedOmConnection(om.NewDeployment())
	conn.AgentAPIKey = "my-api-key"
	err := reconciler.ensureAppDbAgentApiKey(&opsManager, conn, zap.S())
	assert.NoError(t, err)

	secretName := agents.ApiKeySecretName(conn.GroupID())
	apiKey, err := secret.ReadKey(reconciler.client, util.OmAgentApiKey, kube.ObjectKey(opsManager.Namespace, secretName))
	assert.NoError(t, err)
	assert.Equal(t, "my-api-key", apiKey)
}

func TestTryConfigureMonitoringInOpsManager(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeManager := mock.NewEmptyManager()
	client := kubeManager.Client
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})

	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		return om.NewEmptyMockedOmConnection(context)
	}

	// attempt configuring monitoring when there is no api key secret
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(&opsManager, "password", zap.S())
	assert.NoError(t, err)

	assert.Empty(t, podVars.ProjectID)
	assert.Empty(t, podVars.User)

	appDbSts := construct.AppDbStatefulSet(opsManager, Replicas(5))

	_ = client.Update(context.TODO(), &appDbSts)

	// create the apiKey and OM user
	data := map[string]string{
		util.OmPublicApiKey: "apiKey",
		util.OmUser:         "omUser",
	}
	APIKeySecretName, err := opsManager.APIKeySecretName(client)
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringData(data).
		Build()

	err = reconciler.client.CreateSecret(apiKeySecret)
	assert.NoError(t, err)

	// once the secret exists, monitoring should be fully configured
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(&opsManager, "password", zap.S())
	assert.NoError(t, err)

	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "omUser", podVars.User)

	hosts, _ := om.CurrMockedConnection.GetHosts()
	assert.Len(t, hosts.Results, 5, "the AppDB hosts should have been added")
}

func TestAppDBScaleUp_HappensIncrementally(t *testing.T) {
	performAppDBScalingTest(t, 1, 5)
}

func TestAppDBScaleDown_HappensIncrementally(t *testing.T) {
	performAppDBScalingTest(t, 5, 1)
}

func TestAppDBScaleUp_HappensIncrementally_FullOpsManagerReconcile(t *testing.T) {

	opsManager := DefaultOpsManagerBuilder().
		SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).
		SetAppDbMembers(1).
		Build()
	omReconciler, client, _, _ := defaultTestOmReconciler(t, opsManager)

	checkOMReconcilliationSuccessful(t, omReconciler, &opsManager)

	err := client.Get(context.TODO(), types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, &opsManager)
	assert.NoError(t, err)

	opsManager.Spec.AppDB.Members = 3

	err = client.Update(context.TODO(), &opsManager)
	assert.NoError(t, err)

	checkOMReconcilliationPending(t, omReconciler, &opsManager)

	err = client.Get(context.TODO(), types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, &opsManager)
	assert.NoError(t, err)

	assert.Equal(t, 2, opsManager.Status.AppDbStatus.Members)

	res, err := omReconciler.Reconcile(context.TODO(), requestFromObject(&opsManager))
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)
	assert.Equal(t, false, res.Requeue)

	err = client.Get(context.TODO(), types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, &opsManager)
	assert.NoError(t, err)

	assert.Equal(t, 3, opsManager.Status.AppDbStatus.Members)

}

func TestAppDbPortIsConfigurable_WithAdditionalMongoConfig(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().
		SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).
		SetAppDbMembers(1).
		SetAdditionalMongodbConfig(mdb.NewAdditionalMongodConfig("net.port", 30000)).
		Build()
	omReconciler, client, _, _ := defaultTestOmReconciler(t, opsManager)
	//createOpsManagerUserPasswordSecret(client, opsManager, "pass")

	checkOMReconcilliationSuccessful(t, omReconciler, &opsManager)

	appdbSvc, err := client.GetService(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), appdbSvc.Spec.Ports[0].Port)
}

// appDBStatefulSetLabelsAndServiceName returns extra fields that we have to manually set to the AppDB statefulset
// since we manually create it. Otherwise the tests will fail as we try to update parts of the sts that we are not
// allowed to change
func appDBStatefulSetLabelsAndServiceName(omResourceName string) (map[string]string, string) {
	appDbName := fmt.Sprintf("%s-db", omResourceName)
	serviceName := fmt.Sprintf("%s-svc", appDbName)
	labels := map[string]string{"app": serviceName, "controller": "mongodb-enterprise-operator", "pod-anti-affinity": appDbName}
	return labels, serviceName
}

func appDBStatefulSetVolumeClaimtemplates() []corev1.PersistentVolumeClaim {

	res, _ := resource.ParseQuantity("16G")
	return []corev1.PersistentVolumeClaim{{
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
			Resources: corev1.ResourceRequirements{
				Requests: map[corev1.ResourceName]resource.Quantity{"storage": res},
			},
		}}}
}

func performAppDBScalingTest(t *testing.T, startingMembers, finalMembers int) {
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(startingMembers)
	opsManager := builder.Build()
	kubeManager := mock.NewEmptyManager()
	client := kubeManager.Client
	createOpsManagerUserPasswordSecret(client, opsManager, "pass")
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})

	// create the apiKey and OM user
	data := map[string]string{
		util.OmPublicApiKey: "apiKey",
		util.OmUser:         "omUser",
	}

	APIKeySecretName, err := opsManager.APIKeySecretName(client)
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringData(data).
		Build()

	err = reconciler.client.CreateSecret(apiKeySecret)
	assert.NoError(t, err)

	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		return om.NewEmptyMockedOmConnection(context)
	}

	err = client.Create(context.TODO(), &opsManager)
	assert.NoError(t, err)

	matchLabels, serviceName := appDBStatefulSetLabelsAndServiceName(opsManager.Name)
	// app db sts should exist before monitoring is configured
	appDbSts, err := statefulset.NewBuilder().
		SetName(opsManager.Spec.AppDB.Name()).
		SetNamespace(opsManager.Namespace).
		SetMatchLabels(matchLabels).
		SetServiceName(serviceName).
		AddVolumeClaimTemplates(appDBStatefulSetVolumeClaimtemplates()).
		SetReplicas(startingMembers).
		SetPodTemplateSpec(
			podtemplatespec.New(
				podtemplatespec.WithInitContainer("mongodb-enterprise-init-appdb",
					container.WithImage("quay.io/mongodb/mongodb-enterprise-init-appdb:1.0.4")))).
		Build()

	assert.NoError(t, err)
	err = client.CreateStatefulSet(appDbSts)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(&opsManager, "i6ocEoHYJTteoNTX")

	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)
	assert.Equal(t, false, res.Requeue)

	// Scale the AppDB
	opsManager.Spec.AppDB.Members = finalMembers

	if startingMembers < finalMembers {
		for i := startingMembers; i < finalMembers-1; i++ {
			err = client.Update(context.TODO(), &opsManager)
			assert.NoError(t, err)

			res, err = reconciler.Reconcile(&opsManager, "i6ocEoHYJTteoNTX")

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	} else {
		for i := startingMembers; i > finalMembers+1; i-- {
			err = client.Update(context.TODO(), &opsManager)
			assert.NoError(t, err)

			res, err = reconciler.Reconcile(&opsManager, "i6ocEoHYJTteoNTX")

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	}

	res, err = reconciler.Reconcile(&opsManager, "i6ocEoHYJTteoNTX")
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)

	err = client.Get(context.TODO(), types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, &opsManager)
	assert.NoError(t, err)

	assert.Equal(t, finalMembers, opsManager.Status.AppDbStatus.Members)
}

func TestIsOldInitAppDBImageForAgentsCheck(t *testing.T) {
	assert.True(t, isOldInitAppDBImageForAgentsCheck("quay.io/mongodb/mongodb-enterprise-init-appdb:1.0.4", zap.S()))
	assert.True(t, isOldInitAppDBImageForAgentsCheck("quay.io/mongodb/mongodb-enterprise-init-appdb:1.0.0", zap.S()))
	assert.False(t, isOldInitAppDBImageForAgentsCheck("quay.io/mongodb/mongodb-enterprise-init-appdb", zap.S()))
	assert.False(t, isOldInitAppDBImageForAgentsCheck("quay.io/mongodb/mongodb-enterprise-init-appdb:latest", zap.S()))
	assert.False(t, isOldInitAppDBImageForAgentsCheck("quay.io/mongodb/mongodb-enterprise-init-appdb:1.0.5", zap.S()))
}

// ***************** Helper methods *******************************

func buildAutomationConfigForAppDb(builder *omv1.OpsManagerBuilder, kubeManager *mock.MockedManager, internetManifestProvider manifest.Provider) (automationconfig.AutomationConfig, error) {
	opsManager := builder.Build()

	// ensure the password exists for the Ops Manager User. The Ops Manager controller will have ensured this
	createOpsManagerUserPasswordSecret(kubeManager.Client, opsManager, "my-password")
	reconciler := newAppDbReconciler(kubeManager, internetManifestProvider)
	sts := construct.AppDbStatefulSet(opsManager)
	return reconciler.buildAppDbAutomationConfig(opsManager.Spec.AppDB, opsManager, sts, zap.S())
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

func newAppDbReconciler(mgr manager.Manager, internetManifestProvider manifest.Provider) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{
		ReconcileCommonController: newReconcileCommonController(mgr),
		VersionManifestFilePath:   relativeVersionManifestFixturePath,
		InternetManifestProvider:  internetManifestProvider,
		omConnectionFactory:       om.NewEmptyMockedOmConnection,
	}
}

func readAutomationConfigSecret(t *testing.T, kubeManager *mock.MockedManager, opsManager omv1.MongoDBOpsManager) *corev1.Secret {
	s := &corev1.Secret{}
	key := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName())
	assert.NoError(t, kubeManager.Client.Get(context.TODO(), key, s))
	return s
}

// AlwaysFailingManifestProvider mimics not having an internet connection
// by failing to fetch the version manifest
type AlwaysFailingManifestProvider struct{}

func (AlwaysFailingManifestProvider) GetVersion() (*manifest.Manifest, error) {
	return nil, errors.New("failed to get version manifest")
}

// createOpsManagerUserPasswordSecret creates the secret which holds the password that will be used for the Ops Manager user.
func createOpsManagerUserPasswordSecret(client *mock.MockedClient, om omv1.MongoDBOpsManager, password string) error {
	return client.CreateSecret(
		secret.Builder().
			SetName(om.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
			SetNamespace(om.Namespace).
			SetField("password", password).
			Build(),
	)
}
