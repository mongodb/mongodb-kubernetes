package operator

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const relativeVersionManifestFixturePath = "testdata/version_manifest.json"

const gitVersionFromTestData = "a0bbbff6ada159e19298d37946ac8dc4b497eadf"

func init() {
	util.BundledAppDbMongoDBVersion = "4.2.2-ent"
}

func TestMongoDB_ConnectionURL_DefaultCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	appdb := &opsManager.Spec.AppDB
	assert.Equal(t, "mongodb://user:passwd@testOM-db-0.testOM-db-svc.my-namespace.svc.cluster.local:27017,"+
		"testOM-db-1.testOM-db-svc.my-namespace.svc.cluster.local:27017,testOM-db-2.testOM-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=testOM-db&serverSelectionTimeoutMS=20000", appdb.ConnectionURL("user", "passwd", nil))

	// Connection parameters. The default one is overridden
	assert.Equal(t, "mongodb://user:passwd@testOM-db-0.testOM-db-svc.my-namespace.svc.cluster.local:27017,"+
		"testOM-db-1.testOM-db-svc.my-namespace.svc.cluster.local:27017,testOM-db-2.testOM-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=testOM-db&serverSelectionTimeoutMS=20000",
		appdb.ConnectionURL("user", "passwd", map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}))
}

func TestMongoDB_ConnectionURL_OtherCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().SetClusterDomain("my-cluster").Build()
	appdb := &opsManager.Spec.AppDB
	assert.Equal(t, "mongodb://user:passwd@testOM-db-0.testOM-db-svc.my-namespace.svc.my-cluster:27017,"+
		"testOM-db-1.testOM-db-svc.my-namespace.svc.my-cluster:27017,testOM-db-2.testOM-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=testOM-db&serverSelectionTimeoutMS=20000", appdb.ConnectionURL("user", "passwd", nil))

	// Connection parameters. The default one is overridden
	assert.Equal(t, "mongodb://user:passwd@testOM-db-0.testOM-db-svc.my-namespace.svc.my-cluster:27017,"+
		"testOM-db-1.testOM-db-svc.my-namespace.svc.my-cluster:27017,testOM-db-2.testOM-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=testOM-db&serverSelectionTimeoutMS=20000",
		appdb.ConnectionURL("user", "passwd", map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}))
}

// TestPublishAutomationConfig_Create verifies that the automation config map is created if it doesn't exist
func TestPublishAutomationConfig_Create(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewEmptyManager()
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	automationConfig, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	published, err := reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S())
	assert.NoError(t, err)
	assert.True(t, published)

	// verify the configmap was created
	configMap := readAutomationConfigMap(t, kubeManager, opsManager)
	checkDeploymentEqualToPublished(t, automationConfig.Deployment, configMap)
	assert.Len(t, kubeManager.Client.GetMapForObject(&corev1.ConfigMap{}), 1)
}

// TestPublishAutomationConfig_Update verifies that the automation config map is updated if it has changed
func TestPublishAutomationConfig_Update(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewEmptyManager()
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	automationConfig, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	// create
	published, err := reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S())
	assert.NoError(t, err)
	assert.True(t, published)
	kubeManager.Client.ClearHistory()

	// publishing the config without updates should not result in API call
	published, err = reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S())
	assert.NoError(t, err)
	assert.False(t, published)
	kubeManager.Client.CheckOperationsDidntHappen(t, mock.HItem(reflect.ValueOf(kubeManager.Client.Update), &corev1.ConfigMap{}))

	// publishing changed config will result in update
	automationConfig.Deployment.AddMonitoringAndBackup("foo", zap.S())
	published, err = reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S())
	assert.NoError(t, err)
	assert.True(t, published)
	kubeManager.Client.CheckOrderOfOperations(t, mock.HItem(reflect.ValueOf(kubeManager.Client.Update), &corev1.ConfigMap{}))

	// verify the configmap was updated (the version must get incremented)
	configMap := readAutomationConfigMap(t, kubeManager, opsManager)
	automationConfig.SetVersion(2)
	checkDeploymentEqualToPublished(t, automationConfig.Deployment, configMap)
}

func TestPublishAutomationConfig_ScramShaConfigured(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewEmptyManager()
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	automationConfig, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	published, err := reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S())
	assert.NoError(t, err)
	assert.True(t, published)

	configMap := readAutomationConfigMap(t, kubeManager, opsManager)

	acStr := configMap.Data[util.AppDBAutomationConfigKey]

	ac, _ := om.BuildAutomationConfigFromBytes([]byte(acStr))

	assert.NotEmpty(t, ac.Auth.Key, "key file content should have been generated")
	assert.NotEmpty(t, ac.Auth.AutoPwd, "automation agent password should have been generated")
	assert.False(t, ac.Auth.AuthoritativeSet, "authoritativeSet should be set to false")
	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName, "agent should have default name")
	assert.True(t, stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(authentication.MongoDBCR)), "MONGODB-CR should be configured")
	assert.True(t, stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(authentication.MongoDBCR)), "MONGODB-CR should be configured")

	_, omUser := ac.Auth.GetUser(util.OpsManagerMongoDBUserName, util.DefaultUserDatabase)
	assert.NotNil(t, omUser, "ops manager user should have been created")
}

// TestBuildAppDbAutomationConfig checks that the automation config is built correctly
func TestBuildAppDbAutomationConfig(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.2.2-ent").
		SetAppDbFeatureCompatibility("4.0")
	builder.Build()
	automationConfig, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	deployment := automationConfig.Deployment

	// processes
	assert.Len(t, deployment.ProcessesCopy(), 2)
	assert.Equal(t, "4.2.2-ent", deployment.ProcessesCopy()[0].Version())
	assert.Equal(t, "testOM-db-0.testOM-db-svc.my-namespace.svc.cluster.local", deployment.ProcessesCopy()[0].HostName())
	assert.Equal(t, "4.0", deployment.ProcessesCopy()[0].FeatureCompatibilityVersion())
	assert.Equal(t, "4.2.2-ent", deployment.ProcessesCopy()[1].Version())
	assert.Equal(t, "testOM-db-1.testOM-db-svc.my-namespace.svc.cluster.local", deployment.ProcessesCopy()[1].HostName())
	assert.Equal(t, "4.0", deployment.ProcessesCopy()[1].FeatureCompatibilityVersion())

	// replicasets
	assert.Len(t, deployment.ReplicaSetsCopy(), 1)
	assert.Equal(t, builder.Build().Spec.AppDB.Name(), deployment.ReplicaSetsCopy()[0].Name())

	// no sharded clusters
	assert.Empty(t, deployment.ShardedClustersCopy())

	// monitoring agent has been configured
	assert.Len(t, deployment.MonitoringVersionsCopy(), 1)

	// backup agents have not been configured
	assert.Len(t, deployment.BackupVersionsCopy(), 0)

	// options
	assert.Equal(t, map[string]string{"downloadBase": util.AgentDownloadsDir}, deployment["options"])

	// we have only the bundled version here
	assert.Len(t, automationConfig.MongodbVersions(), 1)

	fourTwoTwoEnt := automationConfig.MongodbVersions()[0]

	assert.Equal(t, "4.2.2-ent", fourTwoTwoEnt.Name)
	// test version_manifest.json has 6 builds
	assert.Len(t, fourTwoTwoEnt.Builds, 6)

	// only checking 1st build data matches
	firstBuild := fourTwoTwoEnt.Builds[0]
	assert.Equal(t, "linux", firstBuild.Platform)
	assert.Equal(t, gitVersionFromTestData, firstBuild.GitVersion)
	assert.Equal(t, "ppc64le", firstBuild.Architecture)
	assert.Equal(t, "rhel", firstBuild.Flavor)
	assert.Equal(t, "7.0", firstBuild.MinOsVersion)
	assert.Equal(t, "8.0", firstBuild.MaxOsVersion)
	assert.Equal(t, "https://downloads.mongodb.com/linux/mongodb-linux-ppc64le-enterprise-rhel71-4.2.2.tgz", firstBuild.Url)
	assert.Equal(t, firstBuild.Modules, []string{"enterprise"})

}

func TestGenerateScramCredentials(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	firstScram1Creds, firstScram256Creds, err := generateScramShaCredentials("my-password", opsManager)
	assert.NoError(t, err)

	secondScram1Creds, secondScram256Creds, err := generateScramShaCredentials("my-password", opsManager)
	assert.NoError(t, err)

	assert.Equal(t, firstScram1Creds, secondScram1Creds, "scram sha 1 credentials should be the same as the password was the same")
	assert.Equal(t, firstScram256Creds, secondScram256Creds, "scram sha 256 credentials should be the same as the password was the same")

	changedPasswordScram1Creds, changedPassword256Creds, err := generateScramShaCredentials("my-changed-password", opsManager)

	assert.NoError(t, err)
	assert.NotEqual(t, changedPasswordScram1Creds, firstScram1Creds, "different scram 1 credentials should have been generated as the password changed")
	assert.NotEqual(t, changedPassword256Creds, firstScram256Creds, "different scram 256 credentials should have been generated as the password changed")

	opsManager.Name = "my-different-ops-manager"

	differentNameScram1Creds, differentNameScram256Creds, err := generateScramShaCredentials("my-password", opsManager)

	assert.NoError(t, err)
	assert.NotEqual(t, differentNameScram1Creds, firstScram1Creds, "a different name should generate different scram 1 credentials even with the same password")
	assert.NotEqual(t, differentNameScram256Creds, firstScram256Creds, "a different name should generate different scram 256 credentials even with the same password")
}

func TestBundledVersionManifestIsUsed_WhenCorrespondingVersionIsUsed(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.2.2-ent").
		SetAppDbFeatureCompatibility("4.0")
	automationConfig, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	mongodbVersion := automationConfig.MongodbVersions()[0]
	mongodbBuilds := mongodbVersion.Builds
	firstBuild := mongodbBuilds[0]

	assert.Equal(t, firstBuild.Platform, "linux")
	assert.Equal(t, firstBuild.GitVersion, gitVersionFromTestData)
	assert.Equal(t, mongodbVersion.Name, "4.2.2-ent")
	assert.Len(t, mongodbBuilds, 6)
}

func TestBundledVersionManifestIsUsed_WhenSpecified(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion(util.BundledAppDbMongoDBVersion).
		SetAppDbFeatureCompatibility("4.0")
	automationConfig, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	mongodbVersion := automationConfig.MongodbVersions()[0]
	mongodbBuilds := mongodbVersion.Builds
	firstBuild := mongodbBuilds[0]

	assert.Equal(t, firstBuild.Platform, "linux")
	assert.Equal(t, firstBuild.GitVersion, gitVersionFromTestData)
	assert.Equal(t, mongodbVersion.Name, "4.2.2-ent")
	assert.Len(t, mongodbBuilds, 6)
}

func TestBundledVersionManifestIsUsed_WhenVersionIsEmpty(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("").
		SetAppDbFeatureCompatibility("4.0")
	automationConfig, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.NoError(t, err)
	mongodbVersion := automationConfig.MongodbVersions()[0]
	mongodbBuilds := mongodbVersion.Builds
	firstBuild := mongodbBuilds[0]

	assert.Equal(t, firstBuild.Platform, "linux")
	assert.Equal(t, firstBuild.GitVersion, gitVersionFromTestData)
	assert.Equal(t, mongodbVersion.Name, "4.2.2-ent")
	assert.Len(t, mongodbBuilds, 6)
}

func TestVersionManifestIsDownloaded_WhenNotUsingBundledVersion(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.1.2-ent").
		SetAppDbFeatureCompatibility("4.0")
	automationConfig, err := buildAutomationConfigForAppDb(builder, om.InternetManifestProvider{})
	if err != nil {
		// if failing, checking that the error is connectivity only
		assert.Equal(t, err.Error(), "Get https://opsmanager.mongodb.com/static/version_manifest/4.2.json: dial tcp: lookup opsmanager.mongodb.com: no such host")
		return
	}

	// mongodb versions (as of OM 4.2.2 version manifests contains 235 entries)
	assert.True(t, len(automationConfig.MongodbVersions()) > 234)

	twoSix := automationConfig.MongodbVersions()[0]
	assert.Equal(t, "2.6.0", twoSix.Name)
	assert.Equal(t, "linux", twoSix.Builds[0].Platform)
	assert.Equal(t, "1c1c76aeca21c5983dc178920f5052c298db616c", twoSix.Builds[0].GitVersion)
	assert.Equal(t, "amd64", twoSix.Builds[0].Architecture)
	assert.Equal(t, "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-2.6.0.tgz", twoSix.Builds[0].Url)
	assert.Len(t, twoSix.Builds[0].Modules, 0)

	var fourTwoEnt om.MongoDbVersionConfig
	// seems like we cannot rely on the build by index - there used to be the "4.2.0-ent" on 234 position in the
	// builds array but later it was replaced by 4.2.0-rc8-ent and the test started failing..
	// So we try to find the version by name instead
	for _, v := range automationConfig.MongodbVersions() {
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
	_, err := buildAutomationConfigForAppDb(builder, AlwaysFailingManifestProvider{})
	assert.Error(t, err)
}

func TestRegisterAppDBHostsWithProject(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeManager := mock.NewEmptyManager()
	client := kubeManager.Client
	reconciler := newAppDbReconciler(kubeManager, AlwaysFailingManifestProvider{})
	conn := om.NewMockedOmConnection(om.NewDeployment())

	appDbSts, err := buildAppDbStatefulSet(*defaultAppDbSetHelper().SetName(opsManager.Spec.AppDB.Name()).SetReplicas(3))

	t.Run("Ensure all hosts are added", func(t *testing.T) {
		assert.NoError(t, err)

		_ = client.Update(context.TODO(), &appDbSts)

		err = reconciler.registerAppDBHostsWithProject(&opsManager, conn, "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := conn.GetHosts()
		assert.Len(t, hosts.Results, 3)
	})

	t.Run("Ensure hosts are added when scaled up", func(t *testing.T) {
		appDbSts.Spec.Replicas = util.Int32Ref(5)
		_ = client.Update(context.TODO(), &appDbSts)

		err = reconciler.registerAppDBHostsWithProject(&opsManager, conn, "password", zap.S())
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

	secretName := agentApiKeySecretName(conn.GroupID())
	apiKey, err := reconciler.kubeHelper.readSecretKey(objectKey(opsManager.Namespace, secretName), util.OmAgentApiKey)
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

	appDbSts, err := buildAppDbStatefulSet(*defaultAppDbSetHelper().SetName(opsManager.Spec.AppDB.Name()).SetReplicas(5))
	assert.NoError(t, err)
	_ = client.Update(context.TODO(), &appDbSts)

	// create the apiKey and OM user
	data := map[string]string{
		util.OmPublicApiKey: "apiKey",
		util.OmUser:         "omUser",
	}

	err = reconciler.kubeHelper.createSecret(objectKey(operatorNamespace(), opsManager.APIKeySecretName()), data, nil, nil)
	assert.NoError(t, err)

	// once the secret exists, monitoring should be fully configured
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(&opsManager, "password", zap.S())
	assert.NoError(t, err)

	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "omUser", podVars.User)

	hosts, _ := om.CurrMockedConnection.GetHosts()
	assert.Len(t, hosts.Results, 5, "the AppDB hosts should have been added")
}

// ***************** Helper methods *******************************

func buildAutomationConfigForAppDb(builder *mdbv1.OpsManagerBuilder, internetManifestProvider om.VersionManifestProvider) (*om.AutomationConfig, error) {
	opsManager := builder.Build()
	kubeManager := mock.NewManager(&opsManager)

	// ensure the password exists for the Ops Manager User. The Ops Manager controller will have ensured this
	kubeManager.Client.GetMapForObject(&corev1.Secret{})[objectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())] = &corev1.Secret{
		StringData: map[string]string{
			util.OpsManagerPasswordKey: "my-password",
		},
	}

	reconciler := newAppDbReconciler(kubeManager, internetManifestProvider)
	sts, _ := BuildTestStatefulSet(opsManager)
	return reconciler.buildAppDbAutomationConfig(opsManager.Spec.AppDB, opsManager, "my-pass", sts, zap.S())
}

func checkDeploymentEqualToPublished(t *testing.T, expected om.Deployment, configMap *corev1.ConfigMap) {
	publishedDeployment, err := om.BuildDeploymentFromBytes([]byte(configMap.Data["cluster-config.json"]))
	assert.NoError(t, err)
	assert.Equal(t, expected.ToCanonicalForm(), publishedDeployment)
}

func newAppDbReconciler(mgr manager.Manager, internetManifestProvider om.VersionManifestProvider) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{ReconcileCommonController: newReconcileCommonController(mgr, nil), VersionManifestFilePath: relativeVersionManifestFixturePath, InternetManifestProvider: internetManifestProvider}
}

func readAutomationConfigMap(t *testing.T, kubeManager *mock.MockedManager, opsManager mdbv1.MongoDBOpsManager) *corev1.ConfigMap {
	configMap := &corev1.ConfigMap{}
	key := objectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName())
	assert.NoError(t, kubeManager.Client.Get(context.TODO(), key, configMap))
	return configMap
}

// AlwaysFailingManifestProvider mimics not having an internet connection
// by failing to fetch the version manifest
type AlwaysFailingManifestProvider struct{}

func (AlwaysFailingManifestProvider) GetVersionManifest() (*om.VersionManifest, error) {
	return nil, errors.New("failed to get version manifest")
}
