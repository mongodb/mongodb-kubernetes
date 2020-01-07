package operator

import (
	"context"
	"reflect"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/spf13/cast"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// TestPublishAutomationConfig_Create verifies that the automation config map is created if it doesn't exist
func TestPublishAutomationConfig_Create(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := &opsManager.Spec.AppDB
	kubeManager := newMockedManager(nil)
	reconciler := newAppDbReconciler(kubeManager)
	automationConfig := buildAutomationConfigForAppDb(t, builder)

	assert.NoError(t, reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S()))

	// verify the configmap was created
	configMap := readAutomationConfigMap(t, kubeManager, opsManager)
	checkDeploymentEqualToPublished(t, automationConfig.Deployment, configMap)
	// one config map is the default one (created inside mocked manager)
	assert.Len(t, kubeManager.client.configMaps, 2)
}

// TestPublishAutomationConfig_Update verifies that the automation config map is updated if it has changed
func TestPublishAutomationConfig_Update(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := &opsManager.Spec.AppDB
	kubeManager := newMockedManager(nil)
	reconciler := newAppDbReconciler(kubeManager)
	automationConfig := buildAutomationConfigForAppDb(t, builder)

	// create
	assert.NoError(t, reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S()))
	kubeManager.client.ClearHistory()

	// publishing the config without updates should not result in API call
	assert.NoError(t, reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S()))
	kubeManager.client.CheckOperationsDidntHappen(t, HItem(reflect.ValueOf(kubeManager.client.Update), &corev1.ConfigMap{}))

	// publishing changed config will result in update
	automationConfig.Deployment.AddMonitoringAndBackup("foo", zap.S())
	assert.NoError(t, reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S()))
	kubeManager.client.CheckOrderOfOperations(t, HItem(reflect.ValueOf(kubeManager.client.Update), &corev1.ConfigMap{}))

	// verify the configmap was updated (the version must get incremented)
	configMap := readAutomationConfigMap(t, kubeManager, opsManager)
	automationConfig.SetVersion(2)
	checkDeploymentEqualToPublished(t, automationConfig.Deployment, configMap)
}

func TestPublishAutomationConfig_ScramShaConfigured(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := &opsManager.Spec.AppDB
	kubeManager := newMockedManager(nil)
	reconciler := newAppDbReconciler(kubeManager)
	automationConfig := buildAutomationConfigForAppDb(t, builder)
	assert.NoError(t, reconciler.publishAutomationConfig(appdb, opsManager, automationConfig, zap.S()))

	configMap := readAutomationConfigMap(t, kubeManager, opsManager)

	acStr := configMap.Data[util.AppDBAutomationConfigKey]

	ac, _ := om.BuildAutomationConfigFromBytes([]byte(acStr))

	assert.NotEmpty(t, ac.Auth.Key, "key file content should have been generated")
	assert.NotEmpty(t, ac.Auth.AutoPwd, "automation agent password should have been generated")
	assert.False(t, ac.Auth.AuthoritativeSet, "authoritativeSet should be set to false")
	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName, "agent should have default name")
	assert.True(t, util.ContainsString(ac.Auth.DeploymentAuthMechanisms, string(authentication.MongoDBCR)), "MONGODB-CR should be configured")
	assert.True(t, util.ContainsString(ac.Auth.AutoAuthMechanisms, string(authentication.MongoDBCR)), "MONGODB-CR should be configured")

	_, omUser := ac.Auth.GetUser(util.OpsManagerMongoDBUserName, util.DefaultUserDatabase)
	assert.NotNil(t, omUser, "ops manager user should have been created")
}

// TestBuildAppDbAutomationConfig checks that the automation config is built correctly
func TestBuildAppDbAutomationConfig(t *testing.T) {
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.2.2").
		SetAppDbFeatureCompatibility("4.0")
	automationConfig := buildAutomationConfigForAppDb(t, builder)

	deployment := automationConfig.Deployment

	// processes
	assert.Len(t, deployment.ProcessesCopy(), 2)
	assert.Equal(t, "4.2.2", deployment.ProcessesCopy()[0].Version())
	assert.Equal(t, "testOM-db-0.testOM-db-svc.my-namespace.svc.cluster.local", deployment.ProcessesCopy()[0].HostName())
	assert.Equal(t, "4.0", deployment.ProcessesCopy()[0].FeatureCompatibilityVersion())
	assert.Equal(t, "4.2.2", deployment.ProcessesCopy()[1].Version())
	assert.Equal(t, "testOM-db-1.testOM-db-svc.my-namespace.svc.cluster.local", deployment.ProcessesCopy()[1].HostName())
	assert.Equal(t, "4.0", deployment.ProcessesCopy()[1].FeatureCompatibilityVersion())

	// replicasets
	assert.Len(t, deployment.ReplicaSetsCopy(), 1)
	assert.Equal(t, builder.Build().Spec.AppDB.Name(), deployment.ReplicaSetsCopy()[0].Name())

	// no sharded clusters
	assert.Empty(t, deployment.ShardedClustersCopy())

	// monitoring and backup agents have baseUrl specified
	omUrl := "http://testOM-svc.my-namespace.svc.cluster.local:8080"
	assert.Len(t, deployment.MonitoringVersionsCopy(), 1)
	assert.Equal(t, omUrl, cast.ToStringMap(deployment.MonitoringVersionsCopy()[0])["baseUrl"])
	assert.Len(t, deployment.BackupVersionsCopy(), 2)
	assert.Equal(t, omUrl, cast.ToStringMap(deployment.BackupVersionsCopy()[0])["baseUrl"])
	assert.Equal(t, omUrl, cast.ToStringMap(deployment.BackupVersionsCopy()[1])["baseUrl"])

	// options
	assert.Equal(t, map[string]string{"downloadBase": "/tmp/mms-automation/test/versions"}, deployment["options"])

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

// ***************** Helper methods *******************************

func buildAutomationConfigForAppDb(t *testing.T, builder *OpsManagerBuilder) *om.AutomationConfig {
	opsManager := builder.Build()
	kubeManager := newMockedManager(opsManager)

	// ensure the password exists for the Ops Manager User. The Ops Manager controller will have ensured this
	kubeManager.client.secrets[objectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())] = &corev1.Secret{
		StringData: map[string]string{
			util.OpsManagerPasswordKey: "my-password",
		},
	}

	reconciler := newAppDbReconciler(kubeManager)
	sts, _ := builder.BuildStatefulSet()
	config, err := reconciler.buildAppDbAutomationConfig(&opsManager.Spec.AppDB, opsManager, "my-pass", sts, zap.S())
	assert.NoError(t, err)
	return config
}

func checkDeploymentEqualToPublished(t *testing.T, expected om.Deployment, configMap *corev1.ConfigMap) {
	publishedDeployment, err := om.BuildDeploymentFromBytes([]byte(configMap.Data["cluster-config.json"]))
	assert.NoError(t, err)
	assert.Equal(t, expected.ToCanonicalForm(), publishedDeployment)
}

func newAppDbReconciler(mgr manager.Manager) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{newReconcileCommonController(mgr, nil)}
}

func readAutomationConfigMap(t *testing.T, kubeManager *MockedManager, opsManager *mdbv1.MongoDBOpsManager) *corev1.ConfigMap {
	configMap := &corev1.ConfigMap{}
	key := objectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName())
	assert.NoError(t, kubeManager.client.Get(context.TODO(), key, configMap))
	return configMap
}
