package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	"k8s.io/utils/pointer"

	"github.com/stretchr/testify/require"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/constants"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scramcredentials"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"

	"k8s.io/apimachinery/pkg/types"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/api"
	operatorConstruct "github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/stretchr/testify/assert"
)

func init() {
	_ = os.Setenv(util.AppDBReadinessWaitEnv, "0")
}

func TestOpsManagerReconciler_watchedResources(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	otherTestOm := DefaultOpsManagerBuilder().Build()
	otherTestOm.Name = "otherOM"

	otherTestOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.Enabled = true
	otherTestOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: "oplog1"}}}
	testOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: "oplog1"}}}

	reconciler, _, _ := defaultTestOmReconciler(t, testOm, nil)
	reconciler.watchMongoDBResourcesReferencedByBackup(testOm, zap.S())
	reconciler.watchMongoDBResourcesReferencedByBackup(otherTestOm, zap.S())

	key := watch.Object{
		ResourceType: watch.MongoDB,
		Resource: types.NamespacedName{
			Name:      "oplog1",
			Namespace: testOm.Namespace,
		},
	}

	// om watches oplog MDB resource
	assert.Contains(t, reconciler.WatchedResources, key)
	assert.Contains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(testOm))
	assert.Contains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(otherTestOm))
}

// TestOMTLSResourcesAreWatchedAndUnwatched verifies that TLS config map and secret are added to the internal
// map that allows to watch them for changes
func TestOMTLSResourcesAreWatchedAndUnwatched(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).SetAppDBTLSConfig(mdbv1.TLSConfig{
		Enabled: true,
		CA:      "custom-ca-appdb",
	}).SetTLSConfig(omv1.MongoDBOpsManagerTLS{
		SecretRef: omv1.TLSSecretRef{
			Name: "om-tls-secret",
		},
		CA: "custom-ca",
	}).
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		Build()

	testOm.Spec.Backup.Encryption = &omv1.Encryption{
		Kmip: &omv1.KmipConfig{
			Server: v1.KmipServerConfig{
				CA:  "custom-kmip-ca",
				URL: "kmip:8080",
			},
		},
	}

	reconciler, client, _ := defaultTestOmReconciler(t, testOm, nil)
	addOMTLSResources(client, "om-tls-secret")
	addAppDBTLSResources(client, testOm.Spec.AppDB.GetTlsCertificatesSecretName())
	addKMIPTestResources(client, testOm, "test-mdb", "test-prefix")
	addOmCACm(t, testOm, reconciler)

	configureBackupResources(client, testOm)

	checkOMReconciliationSuccessful(t, reconciler, testOm)

	ns := testOm.Namespace
	KmipCaKey := getWatch(ns, "custom-kmip-ca", watch.ConfigMap)
	omCAKey := getWatch(ns, "custom-ca", watch.ConfigMap)
	appDBCAKey := getWatch(ns, "custom-ca-appdb", watch.ConfigMap)
	KmipMongoDBKey := getWatch(ns, "test-prefix-test-mdb-kmip-client", watch.Secret)
	KmipMongoDBPasswordKey := getWatch(ns, "test-prefix-test-mdb-kmip-client-password", watch.Secret)
	omTLSSecretKey := getWatch(ns, "om-tls-secret", watch.Secret)
	appdbTLSecretCert := getWatch(ns, "test-om-db-cert", watch.Secret)

	expectedWatchedResources := []watch.Object{
		getWatch("testNS", "test-mdb", watch.MongoDB),
		getWatch(ns, "config-0-mdb", watch.MongoDB),
		KmipCaKey,
		omCAKey,
		appDBCAKey,
		KmipMongoDBKey,
		KmipMongoDBPasswordKey,
		omTLSSecretKey,
		appdbTLSecretCert,
	}

	var actual []watch.Object
	for obj := range reconciler.WatchedResources {
		actual = append(actual, obj)
	}

	assert.ElementsMatch(t, expectedWatchedResources, actual)
	testOm.Spec.Security.TLS.SecretRef.Name = ""
	testOm.Spec.Backup.Enabled = false

	err := client.Update(context.TODO(), testOm)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.Equal(t, reconcile.Result{}, res)
	assert.NoError(t, err)

	assert.NotContains(t, reconciler.WatchedResources, omTLSSecretKey)
	assert.NotContains(t, reconciler.WatchedResources, omCAKey)
	assert.NotContains(t, reconciler.WatchedResources, KmipMongoDBKey)
	assert.NotContains(t, reconciler.WatchedResources, KmipMongoDBPasswordKey)
	assert.NotContains(t, reconciler.WatchedResources, KmipCaKey)

	testOm.Spec.AppDB.Security.TLSConfig.Enabled = false
	testOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.Encryption.Kmip = nil
	err = client.Update(context.TODO(), testOm)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.Equal(t, reconcile.Result{}, res)
	assert.NoError(t, err)

	assert.NotContains(t, reconciler.WatchedResources, appDBCAKey)
	assert.NotContains(t, reconciler.WatchedResources, appdbTLSecretCert)
	assert.NotContains(t, reconciler.WatchedResources, KmipMongoDBKey)
	assert.NotContains(t, reconciler.WatchedResources, KmipMongoDBPasswordKey)
	assert.NotContains(t, reconciler.WatchedResources, KmipCaKey)
}

func TestOpsManagerPrefixForTLSSecret(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).SetTLSConfig(omv1.MongoDBOpsManagerTLS{
		CA: "custom-ca",
	}).Build()

	testOm.Spec.Security.CertificatesSecretsPrefix = "prefix"
	assert.Equal(t, fmt.Sprintf("prefix-%s-cert", testOm.Name), testOm.TLSCertificateSecretName())

	testOm.Spec.Security.TLS.SecretRef.Name = "om-tls-secret"
	assert.Equal(t, "om-tls-secret", testOm.TLSCertificateSecretName())
}

func TestOpsManagerReconciler_removeWatchedResources(t *testing.T) {
	resourceName := "oplog1"
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: resourceName}}}

	reconciler, _, _ := defaultTestOmReconciler(t, testOm, nil)
	reconciler.watchMongoDBResourcesReferencedByBackup(testOm, zap.S())

	key := watch.Object{
		ResourceType: watch.MongoDB,
		Resource:     types.NamespacedName{Name: resourceName, Namespace: testOm.Namespace},
	}

	// om watches oplog MDB resource
	assert.Contains(t, reconciler.WatchedResources, key)
	assert.Contains(t, reconciler.WatchedResources[key], mock.ObjectKeyFromApiObject(testOm))

	// watched resources list is cleared when CR is deleted
	reconciler.OnDelete(testOm, zap.S())
	assert.Zero(t, len(reconciler.WatchedResources))
}

func TestOpsManagerReconciler_prepareOpsManager(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := defaultTestOmReconciler(t, testOm, nil)

	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())

	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com", api.CurrMockedAdmin.PublicKey)

	// the user "created" in Ops Manager
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "Jane", initializer.currentUsers[0].FirstName)
	assert.Equal(t, "Doe", initializer.currentUsers[0].LastName)
	assert.Equal(t, "pwd", initializer.currentUsers[0].Password)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	// One secret was created by the user, another one - by the Operator for the user public key
	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)
	expectedSecretData := map[string]string{"publicKey": "jane.doe@g.com", "privateKey": "jane.doe@g.com-key"}

	APIKeySecretName, err := testOm.APIKeySecretName(client, "")
	assert.NoError(t, err)

	existingSecretData, _ := secret.ReadStringData(client, kube.ObjectKey(OperatorNamespace, APIKeySecretName))
	assert.Equal(t, expectedSecretData, existingSecretData)
}

func TestOpsManagerReconcilerPrepareOpsManagerWithTLS(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetTLSConfig(omv1.MongoDBOpsManagerTLS{
		SecretRef: omv1.TLSSecretRef{
			Name: "om-tls-secret",
		},
		CA: "custom-ca",
	}).Build()
	reconciler, _, initializer := defaultTestOmReconciler(t, testOm, nil)
	initializer.expectedCaContent = pointer.String("abc")

	addOmCACm(t, testOm, reconciler)

	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())

	assert.Equal(t, workflow.OK(), reconcileStatus)
}

func addOmCACm(t *testing.T, testOm *omv1.MongoDBOpsManager, reconciler *OpsManagerReconciler) {
	cm := configmap.Builder().
		SetName(testOm.Spec.GetOpsManagerCA()).
		SetNamespace(testOm.Namespace).
		SetData(map[string]string{"mms-ca.crt": "abc"}).
		SetOwnerReferences(kube.BaseOwnerReference(testOm)).
		Build()
	assert.NoError(t, reconciler.client.CreateConfigMap(cm))
}

// TestOpsManagerReconciler_prepareOpsManagerTwoCalls checks that second call to 'prepareOpsManager' doesn't call
// OM api to create a user as the API secret already exists
func TestOpsManagerReconciler_prepareOpsManagerTwoCalls(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := defaultTestOmReconciler(t, testOm, nil)

	reconciler.prepareOpsManager(testOm, zap.S())

	APIKeySecretName, err := testOm.APIKeySecretName(client, "")
	assert.NoError(t, err)

	// let's "update" the user admin secret - this must not affect anything
	client.GetMapForObject(&corev1.Secret{})[kube.ObjectKey(OperatorNamespace, APIKeySecretName)].(*corev1.Secret).Data["Username"] = []byte("this-is-not-expected@g.com")

	// second call is ok - we just don't create the admin user in OM and don't add new secrets
	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())
	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com-key", api.CurrMockedAdmin.PrivateKey)

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
	reconciler, client, initializer := defaultTestOmReconciler(t, testOm, nil)

	reconciler.prepareOpsManager(testOm, zap.S())

	APIKeySecretName, err := testOm.APIKeySecretName(client, "")
	assert.NoError(t, err)

	// for some reason the admin removed the public Api key secret so the call will be done to OM to create a user -
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
	kubeManager := mock.NewManager(testOm)
	appDBReconciler, err := newAppDbReconciler(kubeManager, testOm, zap.S())
	require.NoError(t, err)

	password, err := appDBReconciler.ensureAppDbPassword(testOm, zap.S())
	assert.NoError(t, err)
	assert.Len(t, password, 12, "auto generated password should have a size of 12")
}

func TestOpsManagerUsersPassword_SpecifiedInSpec(t *testing.T) {
	log := zap.S()
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").Build()
	reconciler, client, _ := defaultTestOmReconciler(t, testOm, nil)

	client.GetMapForObject(&corev1.Secret{})[kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.PasswordSecretKeyRef.Name)] = &corev1.Secret{
		Data: map[string][]byte{
			testOm.Spec.AppDB.PasswordSecretKeyRef.Key: []byte("my-password"), // create the secret with the password
		},
	}

	appDBReconciler, err := reconciler.createNewAppDBReconciler(testOm, log)
	require.NoError(t, err)
	password, err := appDBReconciler.ensureAppDbPassword(testOm, zap.S())

	assert.NoError(t, err)
	assert.Equal(t, password, "my-password", "the password specified by the SecretRef should have been returned when specified")
}

func TestBackupStatefulSetIsNotRemoved_WhenDisabled(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).Build()
	reconciler, client, _ := defaultTestOmReconciler(t, testOm, nil)

	checkOMReconciliationSuccessful(t, reconciler, testOm)

	backupSts := appsv1.StatefulSet{}
	err := client.Get(context.TODO(), kube.ObjectKey(testOm.Namespace, testOm.BackupStatefulSetName()), &backupSts)
	assert.NoError(t, err, "Backup StatefulSet should have been created when backup is enabled")

	testOm.Spec.Backup.Enabled = false
	err = client.Update(context.TODO(), testOm)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
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
	reconciler, client, _ := defaultTestOmReconciler(t, testOm, nil)

	s := secret.Builder().
		SetName(testOm.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetNamespace(testOm.Namespace).
		SetOwnerReferences(kube.BaseOwnerReference(testOm)).
		SetByteData(map[string][]byte{
			"password": []byte("password"),
		}).Build()

	err := reconciler.client.UpdateSecret(s)
	assert.NoError(t, err)

	checkOMReconciliationSuccessful(t, reconciler, testOm)

	connectionString, err := secret.ReadKey(reconciler.client, util.AppDbConnectionStringKey, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()))
	assert.NoError(t, err)
	assert.NotEmpty(t, connectionString)

	sts := appsv1.StatefulSet{}
	err = client.Get(context.TODO(), kube.ObjectKey(testOm.Namespace, testOm.Name), &sts)
	assert.NoError(t, err)

	podTemplate := sts.Spec.Template

	assert.Contains(t, podTemplate.Annotations, "connectionStringHash")
	assert.Equal(t, podTemplate.Annotations["connectionStringHash"], hashConnectionString(buildMongoConnectionUrl(testOm, "password", nil)))
	testOm.Spec.AppDB.Members = 5
	assert.NotEqual(t, podTemplate.Annotations["connectionStringHash"], hashConnectionString(buildMongoConnectionUrl(testOm, "password", nil)),
		"Changing the number of members should result in a different Connection String and different hash")
	testOm.Spec.AppDB.Members = 3
	testOm.Spec.AppDB.Version = "4.2.0"
	assert.Equal(t, podTemplate.Annotations["connectionStringHash"], hashConnectionString(buildMongoConnectionUrl(testOm, "password", nil)),
		"Changing version should not change connection string and so the hash should stay the same")
}

func TestOpsManagerConnectionString_IsPassedAsSecretRef(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).Build()
	reconciler, client, _ := defaultTestOmReconciler(t, testOm, nil)

	checkOMReconciliationSuccessful(t, reconciler, testOm)

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

func TestOpsManagerWithKMIP(t *testing.T) {
	//given
	kmipURL := "kmip.mongodb.com:5696"
	kmipCAConfigMapName := "kmip-ca"
	mdbName := "test-mdb"

	clientCertificatePrefix := "test-prefix"
	expectedClientCertificateSecretName := clientCertificatePrefix + "-" + mdbName + "-kmip-client"

	testOm := DefaultOpsManagerBuilder().
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		Build()

	testOm.Spec.Backup.Encryption = &omv1.Encryption{
		Kmip: &omv1.KmipConfig{
			Server: v1.KmipServerConfig{
				CA:  kmipCAConfigMapName,
				URL: kmipURL,
			},
		},
	}

	reconciler, client, _ := defaultTestOmReconciler(t, testOm, nil)
	addKMIPTestResources(client, testOm, mdbName, clientCertificatePrefix)
	configureBackupResources(client, testOm)

	//when
	checkOMReconciliationSuccessful(t, reconciler, testOm)
	sts := appsv1.StatefulSet{}
	err := client.Get(context.TODO(), kube.ObjectKey(testOm.Namespace, testOm.Name), &sts)
	envs := sts.Spec.Template.Spec.Containers[0].Env
	volumes := sts.Spec.Template.Spec.Volumes
	volumeMounts := sts.Spec.Template.Spec.Containers[0].VolumeMounts

	//then
	assert.NoError(t, err)
	host, port, _ := net.SplitHostPort(kmipURL)

	expectedVars := []corev1.EnvVar{
		{Name: "OM_PROP_backup_kmip_server_host", Value: host},
		{Name: "OM_PROP_backup_kmip_server_port", Value: port},
		{Name: "OM_PROP_backup_kmip_server_ca_file", Value: util.KMIPCAFileInContainer},
	}
	assert.Subset(t, envs, expectedVars)

	expectedCAMount := corev1.VolumeMount{
		Name:      util.KMIPServerCAName,
		MountPath: util.KMIPServerCAHome,
		ReadOnly:  true,
	}
	assert.Contains(t, volumeMounts, expectedCAMount)
	expectedClientCertMount := corev1.VolumeMount{
		Name:      util.KMIPClientSecretNamePrefix + expectedClientCertificateSecretName,
		MountPath: util.KMIPClientSecretsHome + "/" + expectedClientCertificateSecretName,
		ReadOnly:  true,
	}
	assert.Contains(t, volumeMounts, expectedClientCertMount)

	expectedCAVolume := statefulset.CreateVolumeFromConfigMap(util.KMIPServerCAName, kmipCAConfigMapName)
	assert.Contains(t, volumes, expectedCAVolume)
	expectedClientCertVolume := statefulset.CreateVolumeFromSecret(util.KMIPClientSecretNamePrefix+expectedClientCertificateSecretName, expectedClientCertificateSecretName)
	assert.Contains(t, volumes, expectedClientCertVolume)
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
	assert.Equal(t, []string{"test-om-backup-daemon-0.test-om-backup-daemon-svc.my-namespace.svc.cluster.local"},
		DefaultOpsManagerBuilder().Build().BackupDaemonFQDNs())
	// The host name doesn't depend on cluster domain
	assert.Equal(t, []string{"test-om-backup-daemon-0.test-om-backup-daemon-svc.my-namespace.svc.some.domain"},
		DefaultOpsManagerBuilder().SetClusterDomain("some.domain").Build().BackupDaemonFQDNs())

	assert.Equal(t, []string{"test-om-backup-daemon-0.test-om-backup-daemon-svc.my-namespace.svc.cluster.local", "test-om-backup-daemon-1.test-om-backup-daemon-svc.my-namespace.svc.cluster.local", "test-om-backup-daemon-2.test-om-backup-daemon-svc.my-namespace.svc.cluster.local"},
		DefaultOpsManagerBuilder().SetBackupMembers(3).Build().BackupDaemonFQDNs())
}

func TestOpsManagerBackupAssignmentLabels(t *testing.T) {
	// given
	assignmentLabels := []string{"test"}

	testOm := DefaultOpsManagerBuilder().
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddS3Config("s3-config", "s3-secret").
		Build()

	testOm.Spec.Backup.AssignmentLabels = assignmentLabels
	testOm.Spec.Backup.OplogStoreConfigs[0].AssignmentLabels = assignmentLabels
	testOm.Spec.Backup.BlockStoreConfigs[0].AssignmentLabels = assignmentLabels
	testOm.Spec.Backup.S3Configs[0].AssignmentLabels = assignmentLabels

	reconciler, client, _ := defaultTestOmReconciler(t, testOm, nil)
	configureBackupResources(client, testOm)

	mockedAdmin := api.NewMockedAdminProvider("testUrl", "publicApiKey", "privateApiKey")
	defer mockedAdmin.(*api.MockedOmAdmin).Reset()

	// when
	reconciler.prepareBackupInOpsManager(testOm, mockedAdmin, "", zap.S())
	blockStoreConfigs, _ := mockedAdmin.ReadBlockStoreConfigs()
	oplogConfigs, _ := mockedAdmin.ReadOplogStoreConfigs()
	s3Configs, _ := mockedAdmin.ReadS3Configs()
	daemonConfigs, _ := mockedAdmin.(*api.MockedOmAdmin).ReadDaemonConfigs()

	// then
	assert.Equal(t, assignmentLabels, blockStoreConfigs[0].Labels)
	assert.Equal(t, assignmentLabels, oplogConfigs[0].Labels)
	assert.Equal(t, assignmentLabels, s3Configs[0].Labels)
	assert.Equal(t, assignmentLabels, daemonConfigs[0].Labels)
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

	reconciler, mockedClient, _ := defaultTestOmReconciler(t, testOm, nil)

	addAppDBTLSResources(mockedClient, fmt.Sprintf("%s-cert", testOm.Spec.AppDB.Name()))
	configureBackupResources(mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)
	assert.Equal(t, true, res.Requeue)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))

	assert.NoError(t, err)
	assert.Equal(t, false, res.Requeue)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)

}

func TestBackupConfig_ChangingName_ResultsIn_DeleteAndAdd(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().
		AddOplogStoreConfig("oplog-store", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddS3Config("s3-config-0", "s3-secret").
		AddS3Config("s3-config-1", "s3-secret").
		AddS3Config("s3-config-2", "s3-secret").
		Build()

	reconciler, mockedClient, _ := defaultTestOmReconciler(t, testOm, nil)

	configureBackupResources(mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)
	assert.Equal(t, true, res.Requeue)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("Configs are created successfully", func(t *testing.T) {
		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs()
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 3)
	})

	testOm.Spec.Backup.S3Configs[0].Name = "new-name"
	err = mockedClient.Update(context.TODO(), testOm)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("Name change resulted in a different config being created", func(t *testing.T) {
		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs()
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 3)

		assert.Equal(t, "new-name", s3Configs[0].Id)
		assert.Equal(t, "s3-config-1", s3Configs[1].Id)
		assert.Equal(t, "s3-config-2", s3Configs[2].Id)
	})

}

func TestBackupConfigs_AreRemoved_WhenRemovedFromCR(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().
		AddS3Config("s3-config-0", "s3-secret").
		AddS3Config("s3-config-1", "s3-secret").
		AddS3Config("s3-config-2", "s3-secret").
		AddOplogStoreConfig("oplog-store-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddOplogStoreConfig("oplog-store-1", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-1", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		Build()

	reconciler, mockedClient, _ := defaultTestOmReconciler(t, testOm, nil)

	configureBackupResources(mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)
	assert.Equal(t, true, res.Requeue)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))

	assert.NoError(t, err)
	assert.Equal(t, false, res.Requeue)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)

	t.Run("Configs are created successfully", func(t *testing.T) {
		configs, err := api.CurrMockedAdmin.ReadOplogStoreConfigs()
		assert.NoError(t, err)
		assert.Len(t, configs, 3)

		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs()
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 3)

		blockstores, err := api.CurrMockedAdmin.ReadBlockStoreConfigs()
		assert.NoError(t, err)
		assert.Len(t, blockstores, 3)
	})

	// remove the first entry
	testOm.Spec.Backup.OplogStoreConfigs = testOm.Spec.Backup.OplogStoreConfigs[1:]

	// remove middle element
	testOm.Spec.Backup.S3Configs = []omv1.S3Config{testOm.Spec.Backup.S3Configs[0], testOm.Spec.Backup.S3Configs[2]}

	// remove first and last
	testOm.Spec.Backup.BlockStoreConfigs = []omv1.DataStoreConfig{testOm.Spec.Backup.BlockStoreConfigs[1]}

	err = mockedClient.Update(context.TODO(), testOm)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("Configs are removed successfully", func(t *testing.T) {
		configs, err := api.CurrMockedAdmin.ReadOplogStoreConfigs()
		assert.NoError(t, err)
		assert.Len(t, configs, 2)

		assert.Equal(t, "oplog-store-1", configs[0].Id)
		assert.Equal(t, "oplog-store-2", configs[1].Id)

		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs()
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 2)

		assert.Equal(t, "s3-config-0", s3Configs[0].Id)
		assert.Equal(t, "s3-config-2", s3Configs[1].Id)

		blockstores, err := api.CurrMockedAdmin.ReadBlockStoreConfigs()
		assert.NoError(t, err)
		assert.Len(t, blockstores, 1)
		assert.Equal(t, "block-store-config-1", blockstores[0].Id)

	})

}

func TestEnsureResourcesForArchitectureChange(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()

	t.Run("When no automation config is present, there is no error", func(t *testing.T) {
		client := mock.NewClient()
		err := ensureResourcesForArchitectureChange(client, client, opsManager)
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
		err = client.CreateSecret(secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		err = ensureResourcesForArchitectureChange(client, client, opsManager)
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
		err = client.CreateSecret(secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		// create the old ops manager user password
		err = client.CreateSecret(secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.Name()+"-password").SetField("my-password", "jrJP7eUeyn").Build())
		assert.NoError(t, err)

		err = ensureResourcesForArchitectureChange(client, client, opsManager)
		assert.NoError(t, err)

		t.Run("Scram credentials have been created", func(t *testing.T) {
			scramCreds, err := client.GetSecret(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.OpsManagerUserScramCredentialsName()))
			assert.NoError(t, err)

			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.Salt, string(scramCreds.Data["sha256-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.StoredKey, string(scramCreds.Data["sha-256-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.ServerKey, string(scramCreds.Data["sha-256-server-key"]))

			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.Salt, string(scramCreds.Data["sha1-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.StoredKey, string(scramCreds.Data["sha-1-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.ServerKey, string(scramCreds.Data["sha-1-server-key"]))
		})

		t.Run("Ops Manager user password has been copied", func(t *testing.T) {
			newOpsManagerUserPassword, err := client.GetSecret(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName()))
			assert.NoError(t, err)
			assert.Equal(t, string(newOpsManagerUserPassword.Data["my-password"]), "jrJP7eUeyn")
		})

		t.Run("Agent password has been created", func(t *testing.T) {
			agentPasswordSecret, err := client.GetSecret(opsManager.Spec.AppDB.GetAgentPasswordSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.AutoPwd, string(agentPasswordSecret.Data[constants.AgentPasswordKey]))
		})

		t.Run("Keyfile has been created", func(t *testing.T) {
			keyFileSecret, err := client.GetSecret(opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.Key, string(keyFileSecret.Data[constants.AgentKeyfileKey]))
		})
	})

}

func TestDependentResources_AreRemoved_WhenBackupIsDisabled(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().
		AddS3Config("s3-config-0", "s3-secret").
		AddS3Config("s3-config-1", "s3-secret").
		AddS3Config("s3-config-2", "s3-secret").
		AddOplogStoreConfig("oplog-store-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddOplogStoreConfig("oplog-store-1", "my-user", types.NamespacedName{Name: "config-1-mdb", Namespace: mock.TestNamespace}).
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-2-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "block-store-config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-1", "my-user", types.NamespacedName{Name: "block-store-config-1-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-2", "my-user", types.NamespacedName{Name: "block-store-config-2-mdb", Namespace: mock.TestNamespace}).
		Build()

	reconciler, mockedClient, _ := defaultTestOmReconciler(t, testOm, nil)

	configureBackupResources(mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)
	assert.Equal(t, true, res.Requeue)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("All MongoDB resource should be watched.", func(t *testing.T) {
		assert.Len(t, reconciler.GetWatchedResourcesOfType(watch.MongoDB, testOm.Namespace), 6, "All non S3 configs should have a corresponding MongoDB resource and should be watched.")
	})

	t.Run("Removing backup configs causes the resource no longer be watched", func(t *testing.T) {
		// remove last
		testOm.Spec.Backup.BlockStoreConfigs = testOm.Spec.Backup.BlockStoreConfigs[0:2]
		// remove first
		testOm.Spec.Backup.OplogStoreConfigs = testOm.Spec.Backup.OplogStoreConfigs[1:3]
		err = mockedClient.Update(context.TODO(), testOm)
		assert.NoError(t, err)

		res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
		assert.NoError(t, err)

		watchedResources := reconciler.GetWatchedResourcesOfType(watch.MongoDB, testOm.Namespace)
		assert.Len(t, watchedResources, 4, "The two configs that were removed should no longer be watched.")

		assert.True(t, containsName("block-store-config-0-mdb", watchedResources))
		assert.True(t, containsName("block-store-config-1-mdb", watchedResources))
		assert.True(t, containsName("config-1-mdb", watchedResources))
		assert.True(t, containsName("config-2-mdb", watchedResources))

	})

	t.Run("Disabling backup should cause all resources to no longer be watched.", func(t *testing.T) {
		testOm.Spec.Backup.Enabled = false
		err = mockedClient.Update(context.TODO(), testOm)
		assert.NoError(t, err)

		res, err = reconciler.Reconcile(context.TODO(), requestFromObject(testOm))
		assert.NoError(t, err)
		assert.Len(t, reconciler.GetWatchedResourcesOfType(watch.MongoDB, testOm.Namespace), 0, "Backup has been disabled, none of the resources should be watched anymore.")
	})

}

func TestUniqueClusterNames(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.AppDB.Topology = "MultiCluster"
	testOm.Spec.AppDB.ClusterSpecList = []mdbv1.ClusterSpecItem{
		{
			ClusterName: "abc",
			Members:     2,
		},
		{
			ClusterName: "def",
			Members:     1,
		},
		{
			ClusterName: "abc",
			Members:     1,
		},
	}

	err := testOm.ValidateCreate()
	require.Error(t, err)
	assert.Equal(t, "Multiple clusters with the same name (abc) are not allowed", err.Error())
}

func containsName(name string, nsNames []types.NamespacedName) bool {
	for _, nsName := range nsNames {
		if nsName.Name == name {
			return true
		}
	}
	return false
}

// configureBackupResources ensures all the dependent resources for the Backup configuration
// are created in the mocked client. This includes MongoDB resources for OplogStores, S3 credentials secrets
// MongodbUsers and their credentials secrets.
func configureBackupResources(m *mock.MockedClient, testOm *omv1.MongoDBOpsManager) {
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
	for _, oplogConfig := range append(testOm.Spec.Backup.OplogStoreConfigs, testOm.Spec.Backup.BlockStoreConfigs...) {
		oplogStoreResource := mdbv1.NewReplicaSetBuilder().
			SetName(oplogConfig.MongoDBResourceRef.Name).
			SetNamespace(testOm.Namespace).
			SetVersion("3.6.9").
			SetMembers(3).
			EnableAuth([]mdbv1.AuthMode{util.SCRAM}).
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

func defaultTestOmReconciler(t *testing.T, opsManager *omv1.MongoDBOpsManager, globalMemberClustersMap map[string]cluster.Cluster) (*OpsManagerReconciler, *mock.MockedClient,
	*MockedInitializer) {
	manager := mock.NewManager(opsManager)
	// create an admin user secret
	data := map[string]string{"Username": "jane.doe@g.com", "Password": "pwd", "FirstName": "Jane", "LastName": "Doe"}

	s := secret.Builder().
		SetName(opsManager.Spec.AdminSecret).
		SetNamespace(opsManager.Namespace).
		SetStringMapToData(data).
		SetLabels(map[string]string{}).
		SetOwnerReferences(kube.BaseOwnerReference(opsManager)).
		Build()

	initializer := &MockedInitializer{expectedOmURL: opsManager.CentralURL(), t: t}

	reconciler := newOpsManagerReconciler(manager, globalMemberClustersMap, om.NewEmptyMockedOmConnection, initializer, func(baseUrl string, user string, publicApiKey string, ca *string) api.OpsManagerAdmin {
		if api.CurrMockedAdmin == nil {
			api.CurrMockedAdmin = api.NewMockedAdminProvider(baseUrl, user, publicApiKey).(*api.MockedOmAdmin)
		}
		return api.CurrMockedAdmin
	}, func(s string) ([]byte, error) {
		return nil, nil
	})

	assert.NoError(t, reconciler.client.CreateSecret(s))
	return reconciler, manager.Client, initializer
}

func DefaultOpsManagerBuilder() *omv1.OpsManagerBuilder {
	spec := omv1.MongoDBOpsManagerSpec{
		Version:     "5.0.0",
		AppDB:       *omv1.DefaultAppDbBuilder().Build(),
		AdminSecret: "om-admin",
	}
	resource := omv1.MongoDBOpsManager{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "test-om", Namespace: mock.TestNamespace}}
	return omv1.NewOpsManagerBuilderFromResource(resource)
}

type MockedInitializer struct {
	currentUsers      []api.User
	expectedAPIError  *apierror.Error
	expectedOmURL     string
	expectedCaContent *string
	t                 *testing.T
	numberOfCalls     int
}

func (o *MockedInitializer) TryCreateUser(omUrl string, omVersion string, user api.User, ca *string) (api.OpsManagerKeyPair, error) {
	o.numberOfCalls++
	assert.Equal(o.t, o.expectedOmURL, omUrl)
	if o.expectedCaContent != nil {
		assert.Equal(o.t, *o.expectedCaContent, *ca)
	}
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

func addKMIPTestResources(client *mock.MockedClient, om *omv1.MongoDBOpsManager, mdbName, clientCertificatePrefixName string) {
	mdb := mdbv1.NewReplicaSetBuilder().SetBackup(mdbv1.Backup{
		Mode: "enabled",
		Encryption: &mdbv1.Encryption{
			Kmip: &mdbv1.KmipConfig{
				Client: v1.KmipClientConfig{
					ClientCertificatePrefix: clientCertificatePrefixName,
				},
			},
		},
	}).SetName(mdbName).Build()
	_ = client.Create(context.TODO(), mdb)

	mockCert, mockKey := createMockCertAndKeyBytes()

	ca := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      om.Spec.Backup.Encryption.Kmip.Server.CA,
			Namespace: om.ObjectMeta.Namespace,
		},
	}
	ca.Data = map[string]string{}
	ca.Data["ca.pem"] = string(mockCert)
	_ = client.Create(context.TODO(), ca)

	clientCertificate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdb.GetBackupSpec().Encryption.Kmip.Client.ClientCertificateSecretName(mdb.GetName()),
			Namespace: om.ObjectMeta.Namespace,
		},
	}
	clientCertificate.Data = map[string][]byte{}
	clientCertificate.Data["tls.key"] = mockKey
	clientCertificate.Data["tls.crt"] = mockCert
	_ = client.Create(context.TODO(), clientCertificate)

	clientCertificatePassword := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdb.GetBackupSpec().Encryption.Kmip.Client.ClientCertificatePasswordSecretName(mdb.GetName()),
			Namespace: om.ObjectMeta.Namespace,
		},
	}
	clientCertificatePassword.Data = map[string]string{
		mdb.GetBackupSpec().Encryption.Kmip.Client.ClientCertificatePasswordKeyName(): "test",
	}
	_ = client.Create(context.TODO(), clientCertificatePassword)
}

func addAppDBTLSResources(client *mock.MockedClient, secretName string) {
	// Let's create a secret with Certificates and private keys!
	certSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: mock.TestNamespace,
		},
	}

	certs := map[string][]byte{}
	certs["tls.crt"], certs["tls.key"] = createMockCertAndKeyBytes()

	certSecret.Data = certs
	_ = client.Create(context.TODO(), certSecret)
}
func addOMTLSResources(client *mock.MockedClient, secretName string) {
	// Let's create a secret with Certificates and private keys!
	certSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: mock.TestNamespace,
		},
	}

	certs := map[string][]byte{}
	certs["tls.crt"], certs["tls.key"] = createMockCertAndKeyBytes()

	certSecret.Data = certs
	_ = client.Create(context.TODO(), certSecret)
}
