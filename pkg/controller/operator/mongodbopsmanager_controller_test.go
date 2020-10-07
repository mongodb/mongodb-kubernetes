package operator

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/apierror"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	userv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/user"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"
	"k8s.io/apimachinery/pkg/types"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	os.Setenv(util.AppDBReadinessWaitEnv, "0")
}

func TestOpsManagerReconciler_performValidation(t *testing.T) {
	assert.NoError(t, performValidation(omWithAppDBVersion("4.0.0")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.0.7")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.2.12")))
	assert.NoError(t, performValidation(omWithAppDBVersion("6.0.0")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.2.0-rc1")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.5.0-ent")))

	assert.Error(t, performValidation(omWithAppDBVersion("3.6.12")))
	assert.Error(t, performValidation(omWithAppDBVersion("3.4.0")))
	assert.Error(t, performValidation(omWithAppDBVersion("3.4.0.0.1.2")))
	assert.Error(t, performValidation(omWithAppDBVersion("foo")))
}

func TestOpsManagerReconciler_watchedResources(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	otherTestOm := DefaultOpsManagerBuilder().Build()
	otherTestOm.Name = "otherOM"

	otherTestOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.Enabled = true
	otherTestOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: "oplog1"}}}
	testOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: "oplog1"}}}

	reconciler, _, _, _ := defaultTestOmReconciler(t, testOm)
	reconciler.watchMongoDBResourcesReferencedByBackup(testOm)
	reconciler.watchMongoDBResourcesReferencedByBackup(otherTestOm)

	key := watch.Object{
		ResourceType: watch.MongoDB,
		Resource: types.NamespacedName{
			Name:      "oplog1",
			Namespace: testOm.Namespace,
		},
	}

	// om watches oplog MDB resource
	assert.Contains(t, reconciler.watchedResources, key)
	assert.Contains(t, reconciler.watchedResources[key], mock.ObjectKeyFromApiObject(&testOm))
	assert.Contains(t, reconciler.watchedResources[key], mock.ObjectKeyFromApiObject(&otherTestOm))

	// if backup is disabled, should be removed from watched resources
	testOm.Spec.Backup.Enabled = false
	reconciler.watchMongoDBResourcesReferencedByBackup(testOm)
	assert.Contains(t, reconciler.watchedResources, key)
	assert.Contains(t, reconciler.watchedResources[key], mock.ObjectKeyFromApiObject(&otherTestOm))
	assert.NotContains(t, reconciler.watchedResources[key], mock.ObjectKeyFromApiObject(&testOm))
}

func TestOpsManagerReconciler_prepareOpsManager(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, admin := defaultTestOmReconciler(t, testOm)

	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())

	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com-key", admin.PublicAPIKey)

	// the user "created" in Ops Manager
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "Jane", initializer.currentUsers[0].FirstName)
	assert.Equal(t, "Doe", initializer.currentUsers[0].LastName)
	assert.Equal(t, "pwd", initializer.currentUsers[0].Password)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	// One secret was created by the user, another one - by the Operator for the user public key
	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)
	expectedSecretData := map[string]string{"user": "jane.doe@g.com", "publicApiKey": "jane.doe@g.com-key"}
	existingSecretData, _ := NewKubeHelper(client).readSecret(objectKey(OperatorNamespace, testOm.APIKeySecretName()))
	assert.Equal(t, expectedSecretData, existingSecretData)
}

// TestOpsManagerReconciler_prepareOpsManagerTwoCalls checks that second call to 'prepareOpsManager' doesn't call
// OM api to create a user as the API secret already exists
func TestOpsManagerReconciler_prepareOpsManagerTwoCalls(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, admin := defaultTestOmReconciler(t, testOm)

	reconciler.prepareOpsManager(testOm, zap.S())

	// let's "update" the user admin secret - this must not affect anything
	client.GetMapForObject(&corev1.Secret{})[objectKey(OperatorNamespace, testOm.APIKeySecretName())].(*corev1.Secret).Data["Username"] = []byte("this-is-not-expected@g.com")

	// second call is ok - we just don't create the admin user in OM and don't add new secrets
	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())
	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com-key", admin.PublicAPIKey)

	// the call to the api didn't happen
	assert.Equal(t, 1, initializer.numberOfCalls)
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)
	data, _ := NewKubeHelper(client).readSecret(objectKey(OperatorNamespace, testOm.APIKeySecretName()))
	assert.Equal(t, "jane.doe@g.com", data["user"])
}

// TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser checks that if the public API key secret is removed by the
// user - the Operator will try to create a user again and this will result in UserAlreadyExists error
func TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, _ := defaultTestOmReconciler(t, testOm)

	reconciler.prepareOpsManager(testOm, zap.S())

	// for some reasons the admin removed the public Api key secret so the call will be done to OM to create a user -
	// it will fail as the user already exists
	_ = client.Delete(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: OperatorNamespace, Name: testOm.APIKeySecretName()},
	})

	reconcileStatus, admin := reconciler.prepareOpsManager(testOm, zap.S())
	assert.Equal(t, status.PhaseFailed, reconcileStatus.Phase())

	option, exists := status.GetOption(reconcileStatus.StatusOptions(), status.MessageOption{})
	assert.True(t, exists)
	assert.Contains(t, option.(status.MessageOption).Message, "USER_ALREADY_EXISTS")
	reconcileStatus.StatusOptions()
	assert.Nil(t, admin)

	// the call to the api happened, but the user wasn't added
	assert.Equal(t, 2, initializer.numberOfCalls)
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	// api secret wasn't created
	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 1)
	assert.NotContains(t, client.GetMapForObject(&corev1.Secret{}), objectKey(OperatorNamespace, testOm.APIKeySecretName()))
}

func TestOpsManagerGeneratesAppDBPassword_IfNotProvided(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, _, _, _ := defaultTestOmReconciler(t, testOm)

	password, err := reconciler.getAppDBPassword(testOm, zap.S())
	assert.NoError(t, err)
	assert.Len(t, password, 12, "auto generated password should have a size of 12")
}

func TestOpsManagerUsersPassword_SpecifiedInSpec(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	client.GetMapForObject(&corev1.Secret{})[objectKey(testOm.Namespace, testOm.Spec.AppDB.PasswordSecretKeyRef.Name)] = &corev1.Secret{
		Data: map[string][]byte{
			testOm.Spec.AppDB.PasswordSecretKeyRef.Key: []byte("my-password"), // create the secret with the password
		},
	}

	password, err := reconciler.getAppDBPassword(testOm, zap.S())

	assert.NoError(t, err)
	assert.Equal(t, password, "my-password", "the password specified by the SecretRef should have been returned when specified")
}

func TestBackupStatefulSetIsNotRemoved_WhenDisabled(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	s := secret.Builder().
		SetName(testOm.Spec.AppDB.PasswordSecretKeyRef.Name).
		SetNamespace(testOm.Namespace).
		SetByteData(map[string][]byte{
			"password": []byte("password"),
		}).SetOwnerReferences(baseOwnerReference(&testOm)).
		Build()

	err := reconciler.kubeHelper.client.CreateSecret(s)
	assert.NoError(t, err)

	checkOMReconcilliationSuccessful(t, reconciler, &testOm)

	backupSts := appsv1.StatefulSet{}
	err = client.Get(context.TODO(), objectKey(testOm.Namespace, testOm.BackupStatefulSetName()), &backupSts)
	assert.NoError(t, err, "Backup StatefulSet should have been created when backup is enabled")

	testOm.Spec.Backup.Enabled = false
	err = client.Update(context.TODO(), &testOm)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(requestFromObject(&testOm))
	assert.Equal(t, reconcile.Result{}, res)
	assert.NoError(t, err)

	backupSts = appsv1.StatefulSet{}
	err = client.Get(context.TODO(), objectKey(testOm.Namespace, testOm.BackupStatefulSetName()), &backupSts)
	assert.NoError(t, err, "Backup StatefulSet should not be removed when backup is disabled")
}

func TestOpsManagerPodTemplateSpec_IsAnnotatedWithHash(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	s := secret.Builder().
		SetName(testOm.Spec.AppDB.PasswordSecretKeyRef.Name).
		SetNamespace(testOm.Namespace).
		SetOwnerReferences(baseOwnerReference(&testOm)).
		SetByteData(map[string][]byte{
			"password": []byte("password"),
		}).Build()

	err := reconciler.kubeHelper.client.CreateSecret(s)
	assert.NoError(t, err)

	checkOMReconcilliationSuccessful(t, reconciler, &testOm)

	connectionString, err := secret.ReadKey(reconciler.kubeHelper.client, util.AppDbConnectionStringKey, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()))
	assert.NoError(t, err)
	assert.NotEmpty(t, connectionString)

	sts := appsv1.StatefulSet{}
	err = client.Get(context.TODO(), objectKey(testOm.Namespace, testOm.Name), &sts)
	assert.NoError(t, err)

	podTemplate := sts.Spec.Template
	assert.Contains(t, podTemplate.Annotations, "connectionStringHash")
	assert.Equal(t, podTemplate.Annotations["connectionStringHash"], hashConnectionString(buildMongoConnectionUrl(testOm, "password")))

	testOm.Spec.AppDB.Members = 5
	assert.NotEqual(t, podTemplate.Annotations["connectionStringHash"], hashConnectionString(buildMongoConnectionUrl(testOm, "password")),
		"Changing the number of members should result in a different Connection String and different hash")

	testOm.Spec.AppDB.Members = 3
	testOm.Spec.AppDB.Version = "4.2.0"

	assert.Equal(t, podTemplate.Annotations["connectionStringHash"], hashConnectionString(buildMongoConnectionUrl(testOm, "password")),
		"Changing version should not change connection string and so the hash should stay the same")
}

func TestOpsManagerConnectionString_IsPassedAsSecretRef(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	s := secret.Builder().
		SetName(testOm.Spec.AppDB.PasswordSecretKeyRef.Name).
		SetNamespace(testOm.Namespace).
		SetByteData(map[string][]byte{
			"password": []byte("password"),
		}).SetOwnerReferences(baseOwnerReference(&testOm)).
		Build()

	err := reconciler.kubeHelper.client.CreateSecret(s)
	assert.NoError(t, err)

	checkOMReconcilliationSuccessful(t, reconciler, &testOm)

	sts := appsv1.StatefulSet{}
	err = client.Get(context.TODO(), objectKey(testOm.Namespace, testOm.Name), &sts)
	assert.NoError(t, err)

	envs := sts.Spec.Template.Spec.Containers[0].Env
	var uriEnv corev1.EnvVar
	for _, e := range envs {
		if e.Name == omv1.ConvertNameToEnvVarFormat(util.MmsMongoUri) {
			uriEnv = e
			break
		}
	}
	assert.NotEmpty(t, uriEnv.Name, "MmsMongoUri env var should have been present!")

	assert.NotNil(t, uriEnv.ValueFrom)
	assert.NotNil(t, uriEnv.ValueFrom.SecretKeyRef)
	assert.Equal(t, uriEnv.ValueFrom.SecretKeyRef.Name, testOm.AppDBMongoConnectionStringSecretName())
	assert.Equal(t, uriEnv.ValueFrom.SecretKeyRef.Key, util.AppDbConnectionStringKey)
	assert.Empty(t, uriEnv.Value, "if ValueFrom is specified, you cannot also specify 'Value'")
}

// TODO move this test to 'opsmanager_types_test.go' when the builder is moved to 'apis' package
func TestOpsManagerCentralUrl(t *testing.T) {
	assert.Equal(t, "http://testOM-svc.my-namespace.svc.cluster.local:8080",
		DefaultOpsManagerBuilder().Build().CentralURL())
	assert.Equal(t, "http://testOM-svc.my-namespace.svc.some.domain:8080",
		DefaultOpsManagerBuilder().SetClusterDomain("some.domain").Build().CentralURL())
}

// TODO move this test to 'opsmanager_types_test.go' when the builder is moved to 'apis' package
func TestOpsManagerBackupDaemonHostName(t *testing.T) {
	assert.Equal(t, "testOM-backup-daemon-0",
		DefaultOpsManagerBuilder().Build().BackupDaemonHostName())
	// The host name doesn't depend on cluster domain
	assert.Equal(t, "testOM-backup-daemon-0",
		DefaultOpsManagerBuilder().SetClusterDomain("some.domain").Build().BackupDaemonHostName())
}

func TestTriggerOmChangedEventIfNeeded(t *testing.T) {
	t.Run("Om changed event got triggered, major version update", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(omv1.NewOpsManagerBuilder().SetVersion("5.2.13").SetOMStatusVersion("4.2.13").Build(), zap.S()))
		assert.NotEqual(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
	t.Run("Om changed event got triggered, minor version update", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(omv1.NewOpsManagerBuilder().SetVersion("4.4.0").SetOMStatusVersion("4.2.13").Build(), zap.S()))
		assert.NotEqual(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
	t.Run("Om changed event got triggered, minor version update, candidate version", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(omv1.NewOpsManagerBuilder().SetVersion("4.4.0-rc2").SetOMStatusVersion("4.2.13").Build(), zap.S()))
		assert.NotEqual(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
	t.Run("Om changed event not triggered, patch version update", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(omv1.NewOpsManagerBuilder().SetVersion("4.4.10").SetOMStatusVersion("4.4.0").Build(), zap.S()))
		assert.Equal(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
}

func TestBackupIsStillConfigured_WhenAppDBIsConfigured_WithTls(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().AddS3Config("s3-config", "s3-secret").
		AddOplogStoreConfig("oplog-store-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		SetAppDBTLSConfig(mdbv1.TLSConfig{Enabled: true}).
		Build()

	reconciler, mockedClient, _, _ := defaultTestOmReconciler(t, testOm)

	configureBackupResources(mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(requestFromObject(&testOm))
	assert.NoError(t, err)
	assert.Equal(t, true, res.Requeue)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(requestFromObject(&testOm))

	assert.NoError(t, err)
	assert.Equal(t, false, res.Requeue)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)

}

// configureBackupResources ensures all of the dependent resources for the Backup configuration
// are created in the mocked client. This includes MongoDB resources for OplogStores, S3 credentials secrets
// MongodbUsers and their credentials secrets.
func configureBackupResources(m *mock.MockedClient, testOm omv1.MongoDBOpsManager) {
	// configure S3 Secret
	for _, s3Config := range testOm.Spec.Backup.S3Configs {
		s3Creds := secret.Builder().
			SetName(s3Config.S3SecretRef.Name).
			SetNamespace(testOm.Namespace).
			SetField(util.S3AccessKey, "s3AccessKey").
			SetField(util.S3SecretKey, "s3SecretKey").
			Build()
		_ = m.CreateSecret(s3Creds)
	}

	// create MDB resource for oplog configs
	for _, oplogConfig := range testOm.Spec.Backup.OplogStoreConfigs {
		oplogStoreResource := mdbv1.NewReplicaSetBuilder().
			SetName(oplogConfig.MongoDBResourceRef.Name).
			SetNamespace(testOm.Namespace).
			SetVersion("3.6.9").
			SetMembers(3).
			EnableAuth([]string{util.SCRAM}).
			Build()

		_ = m.Update(context.TODO(), oplogStoreResource)

		// create user for mdb resource
		oplogStoreUser := DefaultMongoDBUserBuilder().
			SetResourceName(oplogConfig.MongoDBUserRef.Name).
			SetNamespace(testOm.Namespace).
			Build()

		_ = m.Update(context.TODO(), oplogStoreUser)

		// create secret for user
		userPasswordSecret := secret.Builder().
			SetNamespace(testOm.Namespace).
			SetName(oplogStoreUser.Spec.PasswordSecretKeyRef.Name).
			SetField(oplogStoreUser.Spec.PasswordSecretKeyRef.Key, "KeJfV1ucQ_vZl").
			Build()

		_ = m.CreateSecret(userPasswordSecret)
	}
}

// ******************************************* Helper methods *********************************************************

func defaultTestOmReconciler(t *testing.T, opsManager omv1.MongoDBOpsManager) (*OpsManagerReconciler, *mock.MockedClient,
	*MockedInitializer, *api.MockedOmAdmin) {
	manager := mock.NewManager(&opsManager)
	// create an admin user secret
	data := map[string]string{"Username": "jane.doe@g.com", "Password": "pwd", "FirstName": "Jane", "LastName": "Doe"}

	s := secret.Builder().
		SetName(opsManager.Spec.AdminSecret).
		SetNamespace(opsManager.Namespace).
		SetStringData(data).
		SetLabels(map[string]string{}).
		SetOwnerReferences(baseOwnerReference(&opsManager)).
		Build()

	err := NewKubeHelper(manager.Client).client.CreateSecret(s)
	assert.NoError(t, err)

	initializer := &MockedInitializer{expectedOmURL: opsManager.CentralURL(), t: t}

	// It's important to clean the om state as soon as the reconciler is built!
	admin := api.NewMockedAdmin()
	return newOpsManagerReconciler(manager, om.NewEmptyMockedOmConnection, initializer, api.NewMockedAdminProvider, relativeVersionManifestFixturePath),
		manager.Client, initializer, admin
}

func omWithAppDBVersion(version string) omv1.MongoDBOpsManager {
	return DefaultOpsManagerBuilder().SetAppDbVersion(version).Build()
}

func DefaultOpsManagerBuilder() *omv1.OpsManagerBuilder {
	spec := omv1.MongoDBOpsManagerSpec{
		Version:     "4.2.0",
		AppDB:       *omv1.DefaultAppDbBuilder().Build(),
		AdminSecret: "om-admin",
	}
	resource := omv1.MongoDBOpsManager{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "testOM", Namespace: mock.TestNamespace}}
	return omv1.NewOpsManagerBuilderFromResource(resource)
}

func BuildTestStatefulSet(opsManager omv1.MongoDBOpsManager) (appsv1.StatefulSet, error) {
	rs := opsManager.Spec.AppDB
	return (&KubeHelper{}).NewStatefulSetHelper(&opsManager).
		SetName(rs.Name()).
		SetService(rs.ServiceName()).
		SetPodSpec(NewDefaultPodSpecWrapper(*rs.PodSpec)).
		SetPodVars(&PodEnvVars{}). // TODO remove
		SetClusterName(opsManager.ClusterName).
		SetVersion(opsManager.Spec.Version).
		SetContainerName(util.DatabaseContainerName).
		BuildStatefulSet()
}

type MockedInitializer struct {
	currentUsers     []*api.User
	expectedAPIError *apierror.Error
	expectedOmURL    string
	t                *testing.T
	numberOfCalls    int
}

func (o *MockedInitializer) TryCreateUser(omUrl string, user *api.User) (string, error) {
	o.numberOfCalls++
	assert.Equal(o.t, o.expectedOmURL, omUrl)

	if o.expectedAPIError != nil {
		return "", o.expectedAPIError
	}
	// OM logic: any number of users is created. But we cannot of course create the user with the same name
	for _, v := range o.currentUsers {
		if v.Username == user.Username {
			return "", apierror.NewErrorWithCode(apierror.UserAlreadyExists)
		}
	}
	o.currentUsers = append(o.currentUsers, user)

	// let's use username as a public api key for simplicity
	return user.Username + "-key", nil
}
