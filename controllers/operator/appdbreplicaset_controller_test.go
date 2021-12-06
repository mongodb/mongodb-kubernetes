package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connectionstring"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func init() {
	mock.InitDefaultEnvVariables()
}

func TestMongoDB_ConnectionURL_DefaultCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	appdb := &opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Special symbols in the url
	cnx = appdb.BuildConnectionURL("special/user#", "@passw!", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://special%2Fuser%23:%40passw%21@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"})
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		cnx)
}

func TestMongoDB_ConnectionURL_OtherCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().SetClusterDomain("my-cluster").Build()
	appdb := &opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"})
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		cnx)
}

// TestAutomationConfig_IsCreatedInSecret verifies that the automation config is created in a secret.
func TestAutomationConfig_IsCreatedInSecret(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeManager := mock.NewManager(&opsManager)
	reconciler := newAppDbReconciler(kubeManager)

	err := createOpsManagerUserPasswordSecret(kubeManager.Client, opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	assert.NoError(t, err)
	_, err = reconciler.ReconcileAppDB(&opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
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
	reconciler := newAppDbReconciler(kubeManager)
	automationConfig, err := buildAutomationConfigForAppDb(builder, kubeManager, automation)
	assert.NoError(t, err)
	version, err := reconciler.publishAutomationConfig(opsManager, automationConfig, appdb.AutomationConfigSecretName())
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	monitoringAutomationConfig, err := buildAutomationConfigForAppDb(builder, kubeManager, monitoring)
	assert.NoError(t, err)
	version, err = reconciler.publishAutomationConfig(opsManager, monitoringAutomationConfig, appdb.MonitoringAutomationConfigSecretName())
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	// verify the automation config secret for the automation agent
	acSecret := readAutomationConfigSecret(t, kubeManager, opsManager)
	checkDeploymentEqualToPublished(t, automationConfig, acSecret)

	// verify the automation config secret for the monitoring agent
	acMonitoringSecret := readAutomationConfigMonitoringSecret(t, kubeManager, opsManager)
	checkDeploymentEqualToPublished(t, monitoringAutomationConfig, acMonitoringSecret)

	assert.Len(t, kubeManager.Client.GetMapForObject(&corev1.Secret{}), 6)

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

	_, err = kubeManager.Client.GetSecret(kube.ObjectKey(opsManager.Namespace, appdb.MonitoringAutomationConfigSecretName()))
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
	kubeManager := mock.NewManager(&opsManager)
	reconciler := newAppDbReconciler(kubeManager)

	// create
	createOpsManagerUserPasswordSecret(kubeManager.Client, opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	_, err := reconciler.ReconcileAppDB(&opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	assert.NoError(t, err)

	ac, err := automationconfig.ReadFromSecret(reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 1, ac.Version)

	// publishing the config without updates should not result in API call
	_, err = reconciler.ReconcileAppDB(&opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	assert.NoError(t, err)

	ac, err = automationconfig.ReadFromSecret(reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 1, ac.Version)
	kubeManager.Client.CheckOperationsDidntHappen(t, mock.HItem(reflect.ValueOf(kubeManager.Client.Update), &corev1.Secret{}))

	// publishing changed config will result in update
	fcv := "4.4.2"
	opsManager.Spec.AppDB.FeatureCompatibilityVersion = &fcv
	kubeManager.Client.Update(context.TODO(), &opsManager)

	_, err = reconciler.ReconcileAppDB(&opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	assert.NoError(t, err)

	ac, err = automationconfig.ReadFromSecret(reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 2, ac.Version)
	kubeManager.Client.CheckOrderOfOperations(t, mock.HItem(reflect.ValueOf(kubeManager.Client.Update), &corev1.Secret{}))
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

	automationConfig, err := buildAutomationConfigForAppDb(builder, manager, automation)
	assert.NoError(t, err)
	monitoringAutomationConfig, err := buildAutomationConfigForAppDb(builder, manager, monitoring)
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

	// replicasets
	assert.Len(t, automationConfig.ReplicaSets, 1)
	assert.Equal(t, builder.Build().Spec.AppDB.Name(), automationConfig.ReplicaSets[0].Id)

	// monitoring agent has been configured
	assert.Len(t, automationConfig.MonitoringVersions, 0)

	// backup agents have not been configured
	assert.Len(t, automationConfig.BackupVersions, 0)

	// options
	assert.Equal(t, automationconfig.Options{DownloadBase: util.AgentDownloadsDir}, automationConfig.Options)
}

func TestRegisterAppDBHostsWithProject(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeManager := mock.NewEmptyManager()
	client := kubeManager.Client
	reconciler := newAppDbReconciler(kubeManager)
	conn := om.NewMockedOmConnection(om.NewDeployment())

	appDbSts, err := construct.AppDbStatefulSet(opsManager, &env.PodEnvVars{ProjectID: "abcd"}, "", "", "")
	assert.NoError(t, err)

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
	reconciler := newAppDbReconciler(kubeManager)

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
	reconciler := newAppDbReconciler(kubeManager)

	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		return om.NewEmptyMockedOmConnection(context)
	}

	// attempt configuring monitoring when there is no api key secret
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(&opsManager, "password", zap.S())
	assert.NoError(t, err)

	assert.Empty(t, podVars.ProjectID)
	assert.Empty(t, podVars.User)

	opsManager.Spec.AppDB.Members = 5
	appDbSts, err := construct.AppDbStatefulSet(opsManager, &env.PodEnvVars{ProjectID: "abcd"}, "", "", "")
	assert.NoError(t, err)

	_ = client.Update(context.TODO(), &appDbSts)

	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}
	APIKeySecretName, err := opsManager.APIKeySecretName(client.MockedSecretClient, "")
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
	assert.Equal(t, "publicApiKey", podVars.User)

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
	omReconciler, client, _ := defaultTestOmReconciler(t, opsManager)

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
	omReconciler, client, _ := defaultTestOmReconciler(t, opsManager)

	checkOMReconcilliationSuccessful(t, omReconciler, &opsManager)

	appdbSvc, err := client.GetService(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), appdbSvc.Spec.Ports[0].Port)
}

func TestGetMonitoringAgentVersion(t *testing.T) {
	jsonContents := `
{
	"4.2" : "version0",
	"4.4" : "version1"
}`
	t.Run("The version returned for the agent 4.2 when OM is 4.2", func(t *testing.T) {
		opsManager := omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").Build()
		version, err := getMonitoringAgentVersion(opsManager, func(string) ([]byte, error) {
			return []byte(jsonContents), nil
		})
		assert.Nil(t, err)
		assert.Equal(t, "version0", version)
	})

	t.Run("The version returned for the agent 4.4 when OM is 4.4", func(t *testing.T) {
		opsManager := omv1.NewOpsManagerBuilderDefault().SetVersion("4.4.6").Build()
		version, err := getMonitoringAgentVersion(opsManager, func(string) ([]byte, error) {
			return []byte(jsonContents), nil
		})
		assert.Nil(t, err)
		assert.Equal(t, "version1", version)
	})

	t.Run("There is an error when the version is not present", func(t *testing.T) {
		opsManager := omv1.NewOpsManagerBuilderDefault().SetVersion("4.0.6").Build()
		_, err := getMonitoringAgentVersion(opsManager, func(string) ([]byte, error) {
			return []byte(jsonContents), nil
		})
		assert.Error(t, err)
	})
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

	resData, _ := resource.ParseQuantity("16G")
	return []corev1.PersistentVolumeClaim{{
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
			Resources: corev1.ResourceRequirements{
				Requests: map[corev1.ResourceName]resource.Quantity{"storage": resData},
			},
		}},
	}
}

func performAppDBScalingTest(t *testing.T, startingMembers, finalMembers int) {
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(startingMembers)
	opsManager := builder.Build()
	kubeManager := mock.NewEmptyManager()
	client := kubeManager.Client
	createOpsManagerUserPasswordSecret(client, opsManager, "pass")
	reconciler := newAppDbReconciler(kubeManager)

	// create the apiKey and OM user
	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}

	APIKeySecretName, err := opsManager.APIKeySecretName(client, "")
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
		Build()

	assert.NoError(t, err)
	err = client.CreateStatefulSet(appDbSts)
	assert.NoError(t, err)

	res, err := reconciler.ReconcileAppDB(&opsManager, "i6ocEoHYJTteoNTX")

	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)
	assert.Equal(t, false, res.Requeue)

	// Scale the AppDB
	opsManager.Spec.AppDB.Members = finalMembers

	if startingMembers < finalMembers {
		for i := startingMembers; i < finalMembers-1; i++ {
			err = client.Update(context.TODO(), &opsManager)
			assert.NoError(t, err)

			res, err = reconciler.ReconcileAppDB(&opsManager, "i6ocEoHYJTteoNTX")

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	} else {
		for i := startingMembers; i > finalMembers+1; i-- {
			err = client.Update(context.TODO(), &opsManager)
			assert.NoError(t, err)

			res, err = reconciler.ReconcileAppDB(&opsManager, "i6ocEoHYJTteoNTX")

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	}

	res, err = reconciler.ReconcileAppDB(&opsManager, "i6ocEoHYJTteoNTX")
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)

	err = client.Get(context.TODO(), types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, &opsManager)
	assert.NoError(t, err)

	assert.Equal(t, finalMembers, opsManager.Status.AppDbStatus.Members)
}

func buildAutomationConfigForAppDb(builder *omv1.OpsManagerBuilder, kubeManager *mock.MockedManager, acType agentType) (automationconfig.AutomationConfig, error) {
	opsManager := builder.Build()

	// ensure the password exists for the Ops Manager User. The Ops Manager controller will have ensured this
	createOpsManagerUserPasswordSecret(kubeManager.Client, opsManager, "my-password")
	reconciler := newAppDbReconciler(kubeManager)
	sts, err := construct.AppDbStatefulSet(opsManager, &env.PodEnvVars{ProjectID: "abcd"}, "", "", "")
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}
	return reconciler.buildAppDbAutomationConfig(opsManager, sts, acType, "", zap.S())

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

func newAppDbReconciler(mgr manager.Manager) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{
		ReconcileCommonController: newReconcileCommonController(mgr),
		omConnectionFactory:       om.NewEmptyMockedOmConnection,
		versionMappingProvider: func(s string) ([]byte, error) {
			return nil, nil
		},
	}
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

func readAutomationConfigSecret(t *testing.T, kubeManager *mock.MockedManager, opsManager omv1.MongoDBOpsManager) *corev1.Secret {
	s := &corev1.Secret{}
	key := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName())
	assert.NoError(t, kubeManager.Client.Get(context.TODO(), key, s))
	return s
}

func readAutomationConfigMonitoringSecret(t *testing.T, kubeManager *mock.MockedManager, opsManager omv1.MongoDBOpsManager) *corev1.Secret {
	s := &corev1.Secret{}
	key := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.MonitoringAutomationConfigSecretName())
	assert.NoError(t, kubeManager.Client.Get(context.TODO(), key, s))
	return s
}
