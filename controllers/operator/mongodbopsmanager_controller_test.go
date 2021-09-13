package operator

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scram"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scramcredentials"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"k8s.io/apimachinery/pkg/types"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/api"
	operatorConstruct "github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	os.Setenv(util.AppDBReadinessWaitEnv, "0")
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
	assert.Contains(t, reconciler.WatchedResources, key)
	assert.Contains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(&testOm))
	assert.Contains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(&otherTestOm))

	// if backup is disabled, should be removed from watched resources
	testOm.Spec.Backup.Enabled = false
	reconciler.watchMongoDBResourcesReferencedByBackup(testOm)
	assert.Contains(t, reconciler.WatchedResources, key)
	assert.Contains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(&otherTestOm))
	assert.NotContains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(&testOm))
}

//TestOMTLSResourcesAreWatchedAndUnwatched verifies that TLS config map and secret are added to the internal
//map that allows to watch them for changes
func TestOMTLSResourcesAreWatchedAndUnwatched(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).SetAppDBTLSConfig(mdbv1.TLSConfig{
		Enabled: true,
		CA:      "custom-ca-appdb",
		SecretRef: mdbv1.TLSSecretRef{
			Name: "om-appdb-tls-secret",
		},
	}).SetTLSConfig(omv1.MongoDBOpsManagerTLS{
		SecretRef: omv1.TLSSecretRef{
			Name: "om-tls-secret",
		},
		CA: "custom-ca",
	}).Build()

	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)
	checkOMReconcilliationSuccessful(t, reconciler, &testOm)

	appDBCAKey := watch.Object{
		ResourceType: watch.ConfigMap,
		Resource: types.NamespacedName{
			Namespace: testOm.Namespace,
			Name:      "custom-ca-appdb",
		},
	}
	omCAKey := watch.Object{
		ResourceType: watch.ConfigMap,
		Resource: types.NamespacedName{
			Namespace: testOm.Namespace,
			Name:      "custom-ca",
		},
	}
	appdbTLSSecretKey := watch.Object{
		ResourceType: watch.Secret,
		Resource: types.NamespacedName{
			Namespace: testOm.Namespace,
			Name:      "om-tls-secret",
		},
	}
	omTLSSecretKey := watch.Object{
		ResourceType: watch.Secret,
		Resource: types.NamespacedName{
			Namespace: testOm.Namespace,
			Name:      "om-tls-secret",
		},
	}

	assert.Contains(t, reconciler.WatchedResources, appDBCAKey)
	assert.Contains(t, reconciler.WatchedResources, omCAKey)
	assert.Contains(t, reconciler.WatchedResources, appdbTLSSecretKey)
	assert.Contains(t, reconciler.WatchedResources, omTLSSecretKey)

	testOm.Spec.Security.TLS.SecretRef.Name = ""

	err := client.Update(context.TODO(), &testOm)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(&testOm))
	assert.Equal(t, reconcile.Result{}, res)
	assert.NoError(t, err)

	assert.NotContains(t, reconciler.WatchedResources, omTLSSecretKey)
	assert.NotContains(t, reconciler.WatchedResources, omCAKey)

	testOm.Spec.AppDB.Security.TLSConfig.Enabled = false
	testOm.Spec.AppDB.Security.TLSConfig.SecretRef.Name = ""

	err = client.Update(context.TODO(), &testOm)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(&testOm))
	assert.Equal(t, reconcile.Result{}, res)
	assert.NoError(t, err)

	assert.NotContains(t, reconciler.WatchedResources, appDBCAKey)
	assert.NotContains(t, reconciler.WatchedResources, appdbTLSSecretKey)

}

func TestOpsManagerReconciler_removeWatchedResources(t *testing.T) {
	resourceName := "oplog1"
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: resourceName}}}

	reconciler, _, _, _ := defaultTestOmReconciler(t, testOm)
	reconciler.watchMongoDBResourcesReferencedByBackup(testOm)

	key := watch.Object{
		ResourceType: watch.MongoDB,
		Resource:     types.NamespacedName{Name: resourceName, Namespace: testOm.Namespace},
	}

	// om watches oplog MDB resource
	assert.Contains(t, reconciler.WatchedResources, key)
	assert.Contains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(&testOm))

	// watched resources list is cleared when CR is deleted
	reconciler.delete(&testOm, zap.S())
	assert.Zero(t, len(reconciler.WatchedResources))
}

func TestOpsManagerReconciler_prepareOpsManager(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, admin := defaultTestOmReconciler(t, testOm)

	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())

	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com", admin.PublicKey)

	// the user "created" in Ops Manager
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "Jane", initializer.currentUsers[0].FirstName)
	assert.Equal(t, "Doe", initializer.currentUsers[0].LastName)
	assert.Equal(t, "pwd", initializer.currentUsers[0].Password)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	// One secret was created by the user, another one - by the Operator for the user public key
	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)
	expectedSecretData := map[string]string{"publicKey": "jane.doe@g.com", "privateKey": "jane.doe@g.com-key"}

	APIKeySecretName, err := testOm.APIKeySecretName(client)
	assert.NoError(t, err)

	existingSecretData, _ := secret.ReadStringData(client, kube.ObjectKey(OperatorNamespace, APIKeySecretName))
	assert.Equal(t, expectedSecretData, existingSecretData)
}

// TestOpsManagerReconciler_prepareOpsManagerTwoCalls checks that second call to 'prepareOpsManager' doesn't call
// OM api to create a user as the API secret already exists
func TestOpsManagerReconciler_prepareOpsManagerTwoCalls(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, admin := defaultTestOmReconciler(t, testOm)

	reconciler.prepareOpsManager(testOm, zap.S())

	APIKeySecretName, err := testOm.APIKeySecretName(client)
	assert.NoError(t, err)

	// let's "update" the user admin secret - this must not affect anything
	client.GetMapForObject(&corev1.Secret{})[kube.ObjectKey(OperatorNamespace, APIKeySecretName)].(*corev1.Secret).Data["Username"] = []byte("this-is-not-expected@g.com")

	// second call is ok - we just don't create the admin user in OM and don't add new secrets
	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())
	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com-key", admin.PrivateKey)

	// the call to the api didn't happen
	assert.Equal(t, 1, initializer.numberOfCalls)
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)

	data, _ := secret.ReadStringData(client, kube.ObjectKey(OperatorNamespace, APIKeySecretName))
	assert.Equal(t, "jane.doe@g.com", data["publicKey"])
}

// TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser checks that if the public API key secret is removed by the
// user - the Operator will try to create a user again and this will result in UserAlreadyExists error
func TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, _ := defaultTestOmReconciler(t, testOm)

	reconciler.prepareOpsManager(testOm, zap.S())

	APIKeySecretName, err := testOm.APIKeySecretName(client)
	assert.NoError(t, err)

	// for some reasons the admin removed the public Api key secret so the call will be done to OM to create a user -
	// it will fail as the user already exists
	_ = client.Delete(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: OperatorNamespace, Name: APIKeySecretName},
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

	assert.NotContains(t, client.GetMapForObject(&corev1.Secret{}), kube.ObjectKey(OperatorNamespace, APIKeySecretName))
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

	client.GetMapForObject(&corev1.Secret{})[kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.PasswordSecretKeyRef.Name)] = &corev1.Secret{
		Data: map[string][]byte{
			testOm.Spec.AppDB.PasswordSecretKeyRef.Key: []byte("my-password"), // create the secret with the password
		},
	}

	password, err := reconciler.getAppDBPassword(testOm, zap.S())

	assert.NoError(t, err)
	assert.Equal(t, password, "my-password", "the password specified by the SecretRef should have been returned when specified")
}

func TestBackupStatefulSetIsNotRemoved_WhenDisabled(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	checkOMReconcilliationSuccessful(t, reconciler, &testOm)

	backupSts := appsv1.StatefulSet{}
	err := client.Get(context.TODO(), kube.ObjectKey(testOm.Namespace, testOm.BackupStatefulSetName()), &backupSts)
	assert.NoError(t, err, "Backup StatefulSet should have been created when backup is enabled")

	testOm.Spec.Backup.Enabled = false
	err = client.Update(context.TODO(), &testOm)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(&testOm))
	assert.Equal(t, reconcile.Result{}, res)
	assert.NoError(t, err)

	backupSts = appsv1.StatefulSet{}
	err = client.Get(context.TODO(), kube.ObjectKey(testOm.Namespace, testOm.BackupStatefulSetName()), &backupSts)
	assert.NoError(t, err, "Backup StatefulSet should not be removed when backup is disabled")
}

func TestOpsManagerPodTemplateSpec_IsAnnotatedWithHash(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	s := secret.Builder().
		SetName(testOm.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetNamespace(testOm.Namespace).
		SetOwnerReferences(kube.BaseOwnerReference(&testOm)).
		SetByteData(map[string][]byte{
			"password": []byte("password"),
		}).Build()

	err := reconciler.client.UpdateSecret(s)
	assert.NoError(t, err)

	checkOMReconcilliationSuccessful(t, reconciler, &testOm)

	connectionString, err := secret.ReadKey(reconciler.client, util.AppDbConnectionStringKey, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()))
	assert.NoError(t, err)
	assert.NotEmpty(t, connectionString)

	sts := appsv1.StatefulSet{}
	err = client.Get(context.TODO(), kube.ObjectKey(testOm.Namespace, testOm.Name), &sts)
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
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	checkOMReconcilliationSuccessful(t, reconciler, &testOm)

	sts := appsv1.StatefulSet{}
	err := client.Get(context.TODO(), kube.ObjectKey(testOm.Namespace, testOm.Name), &sts)
	assert.NoError(t, err)

	var uriVol corev1.Volume
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.Name == operatorConstruct.AppDBConnectionStringVolume {
			uriVol = v
			break
		}
	}
	assert.NotEmpty(t, uriVol.Name, "MmsMongoUri volume should have been present!")
	assert.NotNil(t, uriVol.VolumeSource)
	assert.NotNil(t, uriVol.VolumeSource.Secret)
	assert.Equal(t, uriVol.VolumeSource.Secret.SecretName, testOm.AppDBMongoConnectionStringSecretName())
}

// TODO move this test to 'opsmanager_types_test.go' when the builder is moved to 'apis' package
func TestOpsManagerCentralUrl(t *testing.T) {
	assert.Equal(t, "http://test-om-svc.my-namespace.svc.cluster.local:8080",
		DefaultOpsManagerBuilder().Build().CentralURL())
	assert.Equal(t, "http://test-om-svc.my-namespace.svc.some.domain:8080",
		DefaultOpsManagerBuilder().SetClusterDomain("some.domain").Build().CentralURL())
}

// TODO move this test to 'opsmanager_types_test.go' when the builder is moved to 'apis' package
func TestOpsManagerBackupDaemonHostName(t *testing.T) {
	assert.Equal(t, []string{"test-om-backup-daemon-0"},
		DefaultOpsManagerBuilder().Build().BackupDaemonHostNames())
	// The host name doesn't depend on cluster domain
	assert.Equal(t, []string{"test-om-backup-daemon-0"},
		DefaultOpsManagerBuilder().SetClusterDomain("some.domain").Build().BackupDaemonHostNames())

	assert.Equal(t, []string{"test-om-backup-daemon-0", "test-om-backup-daemon-1", "test-om-backup-daemon-2"},
		DefaultOpsManagerBuilder().SetBackupMembers(3).Build().BackupDaemonHostNames())
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
	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(&testOm))
	assert.NoError(t, err)
	assert.Equal(t, true, res.Requeue)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(&testOm))

	assert.NoError(t, err)
	assert.Equal(t, false, res.Requeue)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)

}

func TestEnsureResourcesForArchitectureChange(t *testing.T) {
	om := DefaultOpsManagerBuilder().Build()

	t.Run("When no automation config is present, there is no error", func(t *testing.T) {
		client := mock.NewClient()
		err := ensureResourcesForArchitectureChange(client, om)
		assert.NoError(t, err)
	})

	t.Run("If User is not present, there is an error", func(t *testing.T) {
		client := mock.NewClient()
		ac, err := automationconfig.NewBuilder().SetAuth(automationconfig.Auth{
			Users: []automationconfig.MongoDBUser{
				{
					Username: "not-ops-manager-user",
				}},
		}).Build()

		assert.NoError(t, err)

		acBytes, err := json.Marshal(ac)
		assert.NoError(t, err)

		// create the automation config secret
		err = client.CreateSecret(secret.Builder().SetNamespace(om.Namespace).SetName(om.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		err = ensureResourcesForArchitectureChange(client, om)
		assert.Error(t, err)
	})

	t.Run("If an automation config is present, all secrets are created with the correct values", func(t *testing.T) {
		client := mock.NewClient()
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
				}},
		}).Build()

		assert.NoError(t, err)

		acBytes, err := json.Marshal(ac)
		assert.NoError(t, err)

		// create the automation config secret
		err = client.CreateSecret(secret.Builder().SetNamespace(om.Namespace).SetName(om.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		// create the old ops manager user password
		err = client.CreateSecret(secret.Builder().SetNamespace(om.Namespace).SetName(om.Spec.AppDB.Name()+"-password").SetField("my-password", "jrJP7eUeyn").Build())
		assert.NoError(t, err)

		err = ensureResourcesForArchitectureChange(client, om)
		assert.NoError(t, err)

		t.Run("Scram credentials have been created", func(t *testing.T) {
			scramCreds, err := client.GetSecret(kube.ObjectKey(om.Namespace, om.Spec.AppDB.OpsManagerUserScramCredentialsName()))
			assert.NoError(t, err)

			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.Salt, string(scramCreds.Data["sha256-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.StoredKey, string(scramCreds.Data["sha-256-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.ServerKey, string(scramCreds.Data["sha-256-server-key"]))

			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.Salt, string(scramCreds.Data["sha1-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.StoredKey, string(scramCreds.Data["sha-1-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.ServerKey, string(scramCreds.Data["sha-1-server-key"]))
		})

		t.Run("Ops Manager user password has been copied", func(t *testing.T) {
			newOpsManagerUserPassword, err := client.GetSecret(kube.ObjectKey(om.Namespace, om.Spec.AppDB.GetOpsManagerUserPasswordSecretName()))
			assert.NoError(t, err)
			assert.Equal(t, string(newOpsManagerUserPassword.Data["my-password"]), "jrJP7eUeyn")
		})

		t.Run("Agent password has been created", func(t *testing.T) {
			agentPasswordSecret, err := client.GetSecret(om.Spec.AppDB.GetAgentPasswordSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.AutoPwd, string(agentPasswordSecret.Data[scram.AgentPasswordKey]))
		})

		t.Run("Keyfile has been created", func(t *testing.T) {
			keyFileSecret, err := client.GetSecret(om.Spec.AppDB.GetAgentKeyfileSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.Key, string(keyFileSecret.Data[scram.AgentKeyfileKey]))
		})
	})

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
		SetOwnerReferences(kube.BaseOwnerReference(&opsManager)).
		Build()

	initializer := &MockedInitializer{expectedOmURL: opsManager.CentralURL(), t: t}

	// It's important to clean the om state as soon as the reconciler is built!
	admin := api.NewMockedAdmin()
	reconciler := newOpsManagerReconciler(manager, om.NewEmptyMockedOmConnection, initializer, api.NewMockedAdminProvider, func(s string) ([]byte, error) {
		return nil, nil
	})
	reconciler.client.CreateSecret(s)
	return reconciler,
		manager.Client, initializer, admin
}

func DefaultOpsManagerBuilder() *omv1.OpsManagerBuilder {
	spec := omv1.MongoDBOpsManagerSpec{
		Version:     "4.4.0",
		AppDB:       *omv1.DefaultAppDbBuilder().Build(),
		AdminSecret: "om-admin",
	}
	resource := omv1.MongoDBOpsManager{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "test-om", Namespace: mock.TestNamespace}}
	return omv1.NewOpsManagerBuilderFromResource(resource)
}

type MockedInitializer struct {
	currentUsers     []api.User
	expectedAPIError *apierror.Error
	expectedOmURL    string
	t                *testing.T
	numberOfCalls    int
}

func (o *MockedInitializer) TryCreateUser(omUrl string, omVersion string, user api.User) (api.OpsManagerKeyPair, error) {
	o.numberOfCalls++
	assert.Equal(o.t, o.expectedOmURL, omUrl)

	if o.expectedAPIError != nil {
		return api.OpsManagerKeyPair{}, o.expectedAPIError
	}
	// OM logic: any number of users is created. But we cannot of course create the user with the same name
	for _, v := range o.currentUsers {
		if v.Username == user.Username {
			return api.OpsManagerKeyPair{}, apierror.NewErrorWithCode(apierror.UserAlreadyExists)
		}
	}
	o.currentUsers = append(o.currentUsers, user)

	return api.OpsManagerKeyPair{
		PublicKey:  user.Username,
		PrivateKey: user.Username + "-key",
	}, nil
}
