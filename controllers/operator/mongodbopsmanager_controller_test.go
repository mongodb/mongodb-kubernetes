package operator

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/replicaset"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	certspkg "github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	operatorConstruct "github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func TestOpsManagerReconciler_watchedResources(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	otherTestOm := DefaultOpsManagerBuilder().Build()
	otherTestOm.Name = "otherOM"

	otherTestOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.Enabled = true
	otherTestOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: "oplog1"}}}
	testOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: "oplog1"}}}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	reconciler.watchMongoDBResourcesReferencedByBackup(ctx, testOm, zap.S())
	reconciler.watchMongoDBResourcesReferencedByBackup(ctx, otherTestOm, zap.S())

	key := watch.Object{
		ResourceType: watch.MongoDB,
		Resource: types.NamespacedName{
			Name:      "oplog1",
			Namespace: testOm.Namespace,
		},
	}

	// om watches oplog MDB resource
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources(), key)
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources()[key], mock.ObjectKeyFromApiObject(testOm))
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources()[key], mock.ObjectKeyFromApiObject(otherTestOm))
}

func TestOpsManagerReconciler_ExternalAppDBRefMongoDBMultiClusterWatchesReferencedResource(t *testing.T) {
	ctx := context.Background()
	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
		Name: "external-appdb",
		Kind: omv1.ExternalAppDBRefKindMongoDBMultiCluster,
	})

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	reconciler.configureWatchersForDynamicResources(ctx, testOm, zap.S())

	key := watch.Object{
		ResourceType: watch.MongoDBMultiCluster,
		Resource: types.NamespacedName{
			Name:      testOm.Spec.ExternalAppDBRef.Name,
			Namespace: testOm.Namespace,
		},
	}

	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources(), key)
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources()[key], mock.ObjectKeyFromApiObject(testOm))
}

func TestOpsManagerReconciler_ExternalAppDBRefMongoDBMultiClusterEventHandlerRoutesOnlyWatchedObjects(t *testing.T) {
	ctx := context.Background()
	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
		Name: "external-appdb",
		Kind: omv1.ExternalAppDBRefKindMongoDBMultiCluster,
	})

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	reconciler.configureWatchersForDynamicResources(ctx, testOm, zap.S())
	handler := watch.ResourcesHandler{ResourceType: watch.MongoDBMultiCluster, ResourceWatcher: reconciler.resourceWatcher}

	tests := []struct {
		name      string
		objectOld *mdbmulti.MongoDBMultiCluster
		objectNew *mdbmulti.MongoDBMultiCluster
		watched   bool
	}{
		{
			name: "referenced MongoDBMultiCluster spec change enqueues OM",
			objectOld: &mdbmulti.MongoDBMultiCluster{ObjectMeta: metav1.ObjectMeta{
				Name:      testOm.Spec.ExternalAppDBRef.Name,
				Namespace: testOm.Namespace,
			}},
			objectNew: &mdbmulti.MongoDBMultiCluster{ObjectMeta: metav1.ObjectMeta{
				Name:      testOm.Spec.ExternalAppDBRef.Name,
				Namespace: testOm.Namespace,
			}, Spec: mdbmulti.MongoDBMultiSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "6.0.1"}}},
			watched: true,
		},
		{
			name: "unrelated MongoDBMultiCluster does not enqueue OM",
			objectOld: &mdbmulti.MongoDBMultiCluster{ObjectMeta: metav1.ObjectMeta{
				Name:      "other",
				Namespace: testOm.Namespace,
			}},
			objectNew: &mdbmulti.MongoDBMultiCluster{ObjectMeta: metav1.ObjectMeta{
				Name:      "other",
				Namespace: testOm.Namespace,
			}, Spec: mdbmulti.MongoDBMultiSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "6.0.1"}}},
			watched: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer queue.ShutDown()

			handler.Update(ctx, event.TypedUpdateEvent[client.Object]{ObjectOld: tt.objectOld, ObjectNew: tt.objectNew}, queue)
			if !tt.watched {
				assert.Zero(t, queue.Len())
				return
			}

			req, shutdown := queue.Get()
			require.False(t, shutdown)
			assert.Equal(t, mock.ObjectKeyFromApiObject(testOm), req.NamespacedName)
			queue.Done(req)
			assert.Zero(t, queue.Len())
		})
	}
}

// TestOMTLSResourcesAreWatchedAndUnwatched verifies that TLS config map and secret are added to the internal
// map that allows to watch them for changes
func TestOMTLSResourcesAreWatchedAndUnwatched(t *testing.T) {
	ctx := context.Background()
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

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	addOMTLSResources(ctx, client, "om-tls-secret")
	addAppDBTLSResources(ctx, client, testOm.Spec.AppDB.GetTlsCertificatesSecretName())
	addKMIPTestResources(ctx, client, testOm, "test-mdb", "test-prefix")
	addOmCACm(ctx, t, testOm, reconciler)

	configureBackupResources(ctx, client, testOm)

	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

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
	for obj := range reconciler.resourceWatcher.GetWatchedResources() {
		actual = append(actual, obj)
	}

	assert.ElementsMatch(t, expectedWatchedResources, actual)
	testOm.Spec.Security.TLS.SecretRef.Name = ""
	testOm.Spec.Backup.Enabled = false

	err := client.Update(ctx, testOm)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, res)
	assert.NoError(t, err)
	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))

	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), omTLSSecretKey)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), omCAKey)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), KmipMongoDBKey)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), KmipMongoDBPasswordKey)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), KmipCaKey)

	testOm.Spec.AppDB.Security.TLSConfig.Enabled = false
	testOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.Encryption.Kmip = nil
	err = client.Update(ctx, testOm)
	require.NoError(t, err)

	res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, res)
	assert.NoError(t, err)
	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))

	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), appDBCAKey)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), appdbTLSecretCert)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), KmipMongoDBKey)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), KmipMongoDBPasswordKey)
	assert.NotContains(t, reconciler.resourceWatcher.GetWatchedResources(), KmipCaKey)
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
	ctx := context.Background()
	resourceName := "oplog1"
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.Backup.Enabled = true
	testOm.Spec.Backup.OplogStoreConfigs = []omv1.DataStoreConfig{{MongoDBResourceRef: userv1.MongoDBResourceRef{Name: resourceName}}}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	reconciler.watchMongoDBResourcesReferencedByBackup(ctx, testOm, zap.S())

	key := watch.Object{
		ResourceType: watch.MongoDB,
		Resource:     types.NamespacedName{Name: resourceName, Namespace: testOm.Namespace},
	}

	// om watches oplog MDB resource
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources(), key)
	assert.Contains(t, reconciler.resourceWatcher.GetWatchedResources()[key], mock.ObjectKeyFromApiObject(testOm))

	// watched resources list is cleared when CR is deleted
	reconciler.OnDelete(ctx, testOm, zap.S())
	assert.Zero(t, len(reconciler.resourceWatcher.GetWatchedResources()))
}

// TestOpsManagerReconciler_OnDeleteClusterResourceCleanup verifies the DeleteAllOf behavior:
// - In single-cluster mode: DeleteAllOf should NOT be called (OwnerReferences handle cleanup)
// - In multi-cluster mode: DeleteAllOf SHOULD be called (can't use OwnerReferences cross-cluster)
func TestOpsManagerReconciler_OnDeleteClusterResourceCleanup(t *testing.T) {
	multiClusterSpecItems := []omv1.ClusterSpecOMItem{
		{ClusterName: "cluster-a", Members: 1},
		{ClusterName: "cluster-b", Members: 1},
	}

	testCases := []struct {
		name             string
		isMultiCluster   bool
		clusterSpecItems []omv1.ClusterSpecOMItem
	}{
		{
			name:             "SingleCluster_SkipsDeleteAllOf",
			isMultiCluster:   false,
			clusterSpecItems: nil,
		},
		{
			name:             "MultiCluster_CallsDeleteAllOf",
			isMultiCluster:   true,
			clusterSpecItems: multiClusterSpecItems,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			deleteAllOfCallCount := 0
			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

			omBuilder := DefaultOpsManagerBuilder()
			var memberClustersMap map[string]client.Client

			// Create fake client with interceptor that tracks DeleteAllOf calls
			deleteAllOfInterceptor := func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
				deleteAllOfCallCount++
				return nil
			}

			if tc.isMultiCluster {
				omBuilder.SetOpsManagerTopology(omv1.ClusterTopologyMultiCluster).
					SetOpsManagerClusterSpecList(tc.clusterSpecItems)
				memberClustersMap = make(map[string]client.Client)
				for _, clusterSpec := range tc.clusterSpecItems {
					memberFakeClientBuilder := mock.NewEmptyFakeClientBuilder()
					memberFakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
						Get:         mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
						DeleteAllOf: deleteAllOfInterceptor,
					})
					memberClustersMap[clusterSpec.ClusterName] = memberFakeClientBuilder.Build()
				}
			}

			testOm := omBuilder.Build()
			assert.Equal(t, tc.isMultiCluster, testOm.Spec.IsMultiCluster(), "topology mismatch")

			fakeClientBuilder := mock.NewEmptyFakeClientBuilder()
			fakeClientBuilder.WithObjects(testOm.DeepCopy())
			fakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
				Get:         mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
				DeleteAllOf: deleteAllOfInterceptor,
			})
			kubeClient := kubernetesClient.NewClient(fakeClientBuilder.Build())

			reconciler := NewOpsManagerReconciler(ctx, kubeClient, memberClustersMap, images.ImageUrls{}, "", "", architectures.NonStatic, "", omConnectionFactory.GetConnectionFunc, &MockedInitializer{expectedOmURL: testOm.CentralURL(), t: t}, func(baseUrl string, user string, publicApiKey string, ca *string) api.OpsManagerAdmin {
				return api.NewMockedAdminProvider(baseUrl, user, publicApiKey, true).(*api.MockedOmAdmin)
			})

			reconciler.OnDelete(ctx, testOm, zap.S())

			if tc.isMultiCluster {
				assert.Greater(t, deleteAllOfCallCount, 0, "DeleteAllOf should be called in multi-cluster mode")
			} else {
				assert.Equal(t, 0, deleteAllOfCallCount, "DeleteAllOf should not be called in single-cluster mode; cleanup relies on OwnerReferences")
			}
		})
	}
}

func resetCurrMockedAdmin(t *testing.T) {
	t.Helper()
	previousAdmin := api.CurrMockedAdmin
	api.CurrMockedAdmin = nil
	t.Cleanup(func() { api.CurrMockedAdmin = previousAdmin })
}

func TestOpsManagerReconciler_prepareOpsManager(t *testing.T) {
	ctx := context.Background()
	resetCurrMockedAdmin(t)
	testOm := DefaultOpsManagerBuilder().Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, initializer := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	reconcileStatus, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())

	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com", api.CurrMockedAdmin.PublicKey)

	// the user "created" in Ops Manager
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "Jane", initializer.currentUsers[0].FirstName)
	assert.Equal(t, "Doe", initializer.currentUsers[0].LastName)
	assert.Equal(t, "pwd", initializer.currentUsers[0].Password)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	// One secret was created by the user, another one - by the Operator for the user public key
	assert.Len(t, mock.GetMapForObject(client, &corev1.Secret{}), 2)
	expectedSecretData := map[string]string{"publicKey": "jane.doe@g.com", "privateKey": "jane.doe@g.com-key"}

	APIKeySecretName, err := testOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	assert.NoError(t, err)

	existingSecretData, _ := secret.ReadStringData(ctx, client, kube.ObjectKey(OperatorNamespace, APIKeySecretName))
	assert.Equal(t, expectedSecretData, existingSecretData)
}

func TestOpsManagerReconcilerPrepareOpsManagerWithTLS(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetTLSConfig(omv1.MongoDBOpsManagerTLS{
		SecretRef: omv1.TLSSecretRef{
			Name: "om-tls-secret",
		},
		CA: "custom-ca",
	}).Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, initializer := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	initializer.expectedCaContent = new("abc")

	addOmCACm(ctx, t, testOm, reconciler)

	reconcileStatus, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())

	assert.Equal(t, workflow.OK(), reconcileStatus)
}

func TestOpsManagerReconcilePrepareOpsManagerWithTLSHostCA(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetTLSConfig(omv1.MongoDBOpsManagerTLS{
		SecretRef: omv1.TLSSecretRef{
			Name: "om-tls-secret",
		},
	}).Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	reconcileStatus, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())

	assert.Equal(t, workflow.OK(), reconcileStatus)
}

func addOmCACm(ctx context.Context, t *testing.T, testOm *omv1.MongoDBOpsManager, reconciler *OpsManagerReconciler) {
	cm := configmap.Builder().
		SetName(testOm.GetOpsManagerCA()).
		SetNamespace(testOm.Namespace).
		SetData(map[string]string{"mms-ca.crt": "abc"}).
		SetOwnerReferences(kube.BaseOwnerReference(testOm)).
		Build()
	assert.NoError(t, reconciler.client.CreateConfigMap(ctx, cm))
}

// TestOpsManagerReconciler_prepareOpsManagerTwoCalls checks that second call to 'prepareOpsManager' doesn't call
// OM api to create a user as the API secret already exists
func TestOpsManagerReconciler_prepareOpsManagerTwoCalls(t *testing.T) {
	ctx := context.Background()
	resetCurrMockedAdmin(t)
	testOm := DefaultOpsManagerBuilder().Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, initializer := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())

	APIKeySecretName, err := testOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	assert.NoError(t, err)

	// let's "update" the user admin secret - this must not affect anything
	s, _ := client.GetSecret(ctx, kube.ObjectKey(OperatorNamespace, APIKeySecretName))
	s.Data["Username"] = []byte("this-is-not-expected@g.com")
	err = client.UpdateSecret(ctx, s)
	assert.NoError(t, err)

	// second call is ok - we just don't create the admin user in OM and don't add new secrets
	reconcileStatus, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())
	assert.Equal(t, workflow.OK(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com-key", api.CurrMockedAdmin.PrivateKey)

	// the call to the api didn't happen
	assert.Equal(t, 1, initializer.numberOfCalls)
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	assert.Len(t, mock.GetMapForObject(client, &corev1.Secret{}), 2)

	data, _ := secret.ReadStringData(ctx, client, kube.ObjectKey(OperatorNamespace, APIKeySecretName))
	assert.Equal(t, "jane.doe@g.com", data["publicKey"])
}

// TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser checks that if the public API key secret is removed by the
// user - the Operator will try to create a user again and this will result in UserAlreadyExists error
func TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser(t *testing.T) {
	ctx := context.Background()
	resetCurrMockedAdmin(t)
	testOm := DefaultOpsManagerBuilder().Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, initializer := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())

	APIKeySecretName, err := testOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	assert.NoError(t, err)

	// for some reason the admin removed the public Api key secret so the call will be done to OM to create a user -
	// it will fail as the user already exists
	_ = client.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: OperatorNamespace, Name: APIKeySecretName},
	})

	reconcileStatus, admin := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())
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
	assert.Len(t, mock.GetMapForObject(client, &corev1.Secret{}), 1)

	assert.NotContains(t, mock.GetMapForObject(client, &corev1.Secret{}), kube.ObjectKey(OperatorNamespace, APIKeySecretName))
}

func TestOpsManagerGeneratesAppDBPassword_IfNotProvided(t *testing.T) {
	ctx := context.Background()

	testOm := DefaultOpsManagerBuilder().Build()
	kubeManager, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	appDBReconciler, err := newAppDbReconciler(ctx, kubeManager, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	password, err := appDBReconciler.ensureAppDbPassword(ctx, testOm, zap.S())
	assert.NoError(t, err)
	assert.Len(t, password, 12, "auto generated password should have a size of 12")
}

func TestEnsureAppDbPassword_ErrorWhenPasswordSecretKeyRefNotFound(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").Build()
	kubeManager, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	appDBReconciler, err := newAppDbReconciler(ctx, kubeManager, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	password, err := appDBReconciler.ensureAppDbPassword(ctx, testOm, zap.S())

	assert.Error(t, err)
	assert.Empty(t, password)
	assert.Contains(t, err.Error(), "my-secret")
	assert.Contains(t, err.Error(), "does not exist")
}

func TestOpsManagerUsersPassword_SpecifiedInSpec(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	_, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	s := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testOm.Spec.AppDB.PasswordSecretKeyRef.Name, Namespace: testOm.Namespace},
		Data: map[string][]byte{
			testOm.Spec.AppDB.PasswordSecretKeyRef.Key: []byte("my-password"), // create the secret with the password
		},
	}
	err := client.CreateSecret(ctx, s)

	require.NoError(t, err)
	err = client.UpdateSecret(ctx, s)

	require.NoError(t, err)

	appDBReconciler, err := newAppDbReconciler(ctx, client, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	password, err := appDBReconciler.ensureAppDbPassword(ctx, testOm, zap.S())

	assert.NoError(t, err)
	assert.Equal(t, password, "my-password", "the password specified by the SecretRef should have been returned when specified")
}

func TestBackupStatefulSetIsNotRemoved_WhenDisabled(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	checkOMReconciliationInvalid(ctx, t, reconciler, testOm, client)

	backupSts := appsv1.StatefulSet{}
	err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.BackupDaemonStatefulSetName()), &backupSts)
	assert.NoError(t, err, "Backup StatefulSet should have been created when backup is enabled")

	testOm.Spec.Backup.Enabled = false
	err = client.Update(ctx, testOm)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, res)
	assert.NoError(t, err)
	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))

	backupSts = appsv1.StatefulSet{}
	err = client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.BackupDaemonStatefulSetName()), &backupSts)
	assert.NoError(t, err, "Backup StatefulSet should not be removed when backup is disabled")
}

func TestOpsManagerPodTemplateSpec_IsAnnotatedWithHash(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	s := secret.Builder().
		SetName(testOm.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetNamespace(testOm.Namespace).
		SetOwnerReferences(kube.BaseOwnerReference(testOm)).
		SetByteData(map[string][]byte{
			"password": []byte("password"),
		}).Build()

	err := reconciler.client.CreateSecret(ctx, s)
	assert.NoError(t, err)

	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

	connectionString, err := secret.ReadKey(ctx, reconciler.client, util.AppDbConnectionStringKey, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()))
	assert.NoError(t, err)
	assert.NotEmpty(t, connectionString)

	sts := appsv1.StatefulSet{}
	err = client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Name), &sts)
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

func TestOpsManagerReconcileContainerImages(t *testing.T) {
	initOpsManagerRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_2_3", util.InitOpsManagerImageUrl)
	opsManagerRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_8_0_0", util.OpsManagerImageUrl)
	mongodbRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_8_0_0", util.MongodbImageEnv)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_3_4_5", util.InitDatabaseImageUrlEnv)

	imageUrlsMock := images.ImageUrls{
		// Ops manager & backup daemon images
		initOpsManagerRelatedImageEnv: "quay.io/mongodb/mongodb-kubernetes-init-ops-manager:@sha256:MONGODB_INIT_OPS_MANAGER",
		opsManagerRelatedImageEnv:     "quay.io/mongodb/mongodb-enterprise-ops-manager:@sha256:MONGODB_OPS_MANAGER",

		// AppDB images
		util.AgentImageEnv:          "quay.io/mongodb/mongodb-agent@sha256:AGENT_SHA",
		mongodbRelatedImageEnv:      "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi@sha256:MONGODB_SHA",
		initDatabaseRelatedImageEnv: "quay.io/mongodb/mongodb-kubernetes-init-database@sha256:INIT_DATABASE_SHA",
	}

	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().
		SetVersion("8.0.0").
		SetAppDbVersion("8.0.0").
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, imageUrlsMock, "3.4.5", "1.2.3", testOm, nil, omConnectionFactory, architectures.NonStatic)
	configureBackupResources(ctx, client, testOm)

	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

	for stsAlias, stsName := range map[string]string{
		"opsManagerSts": testOm.Name,
		"backupSts":     testOm.BackupDaemonStatefulSetName(),
	} {
		t.Run(stsAlias, func(t *testing.T) {
			sts := appsv1.StatefulSet{}
			err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, stsName), &sts)
			require.NoError(t, err)

			require.Len(t, sts.Spec.Template.Spec.InitContainers, 1)
			require.Len(t, sts.Spec.Template.Spec.Containers, 1)

			assert.Equal(t, "quay.io/mongodb/mongodb-kubernetes-init-ops-manager:@sha256:MONGODB_INIT_OPS_MANAGER", sts.Spec.Template.Spec.InitContainers[0].Image)
			assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:@sha256:MONGODB_OPS_MANAGER", sts.Spec.Template.Spec.Containers[0].Image)
		})
	}

	appDBSts := appsv1.StatefulSet{}
	err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &appDBSts)
	require.NoError(t, err)

	require.Len(t, appDBSts.Spec.Template.Spec.InitContainers, 1)
	require.Len(t, appDBSts.Spec.Template.Spec.Containers, 2)

	assert.Equal(t, "quay.io/mongodb/mongodb-kubernetes-init-database@sha256:INIT_DATABASE_SHA", appDBSts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-agent@sha256:AGENT_SHA", appDBSts.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi@sha256:MONGODB_SHA", appDBSts.Spec.Template.Spec.Containers[1].Image)
}

func TestOpsManagerReconcileContainerImagesWithStaticArchitecture(t *testing.T) {
	opsManagerRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_8_0_0", util.OpsManagerImageUrl)
	mongodbRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_8_0_0", util.MongodbImageEnv)

	imageUrlsMock := images.ImageUrls{
		// Ops manager & backup daemon images
		opsManagerRelatedImageEnv: "quay.io/mongodb/mongodb-enterprise-ops-manager:@sha256:MONGODB_OPS_MANAGER",

		// AppDB images
		mongodbRelatedImageEnv: "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi@sha256:MONGODB_SHA",
		util.AgentImageUrlEnv:  "quay.io/mongodb/mongodb-agent",
	}

	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().
		SetVersion("8.0.0").
		SetAppDbVersion("8.0.0").
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, imageUrlsMock, "", "", testOm, nil, omConnectionFactory, architectures.Static)
	configureBackupResources(ctx, client, testOm)

	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

	for stsAlias, stsName := range map[string]string{
		"opsManagerSts": testOm.Name,
		"backupSts":     testOm.BackupDaemonStatefulSetName(),
	} {
		t.Run(stsAlias, func(t *testing.T) {
			sts := appsv1.StatefulSet{}
			err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, stsName), &sts)
			require.NoError(t, err)

			assert.Len(t, sts.Spec.Template.Spec.InitContainers, 0)
			require.Len(t, sts.Spec.Template.Spec.Containers, 1)

			assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:@sha256:MONGODB_OPS_MANAGER", sts.Spec.Template.Spec.Containers[0].Image)
		})
	}

	appDBSts := appsv1.StatefulSet{}
	err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &appDBSts)
	require.NoError(t, err)

	require.Len(t, appDBSts.Spec.Template.Spec.InitContainers, 0)
	require.Len(t, appDBSts.Spec.Template.Spec.Containers, 3)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi@sha256:MONGODB_SHA", appDBSts.Spec.Template.Spec.Containers[1].Image)
}

func TestOpsManagerReconcileAppDBContainerImageType(t *testing.T) {
	tests := []struct {
		name                string
		defaultArchitecture architectures.DefaultArchitecture
		defaultImageType    architectures.ImageType
		annotations         map[string]string
		wantImageType       architectures.ImageType
	}{
		{
			name:                "static without configuration uses ubi9",
			defaultArchitecture: architectures.Static,
			wantImageType:       architectures.ImageTypeUBI9,
		},
		{
			name:                "static uses global image type",
			defaultArchitecture: architectures.Static,
			defaultImageType:    architectures.ImageTypeUBI9Slim,
			wantImageType:       architectures.ImageTypeUBI9Slim,
		},
		{
			name:                "static annotation overrides global image type",
			defaultArchitecture: architectures.Static,
			defaultImageType:    architectures.ImageTypeUBI9,
			annotations: map[string]string{
				architectures.ContainerImageTypeAnnotation: string(architectures.ImageTypeUBI9Slim),
			},
			wantImageType: architectures.ImageTypeUBI9Slim,
		},
		{
			name:                "non-static without configuration uses ubi8",
			defaultArchitecture: architectures.NonStatic,
			wantImageType:       architectures.ImageTypeUBI8,
		},
		{
			name:                "non-static uses global image type",
			defaultArchitecture: architectures.NonStatic,
			defaultImageType:    architectures.ImageTypeUBI9Slim,
			wantImageType:       architectures.ImageTypeUBI9Slim,
		},
		{
			name:                "non-static annotation overrides global image type",
			defaultArchitecture: architectures.NonStatic,
			defaultImageType:    architectures.ImageTypeUBI9,
			annotations: map[string]string{
				architectures.ContainerImageTypeAnnotation: string(architectures.ImageTypeUBI9Slim),
			},
			wantImageType: architectures.ImageTypeUBI9Slim,
		},
		{
			name:                "non-static architecture annotation honors image type annotation",
			defaultArchitecture: architectures.Static,
			annotations: map[string]string{
				architectures.ArchitectureAnnotation:       string(architectures.NonStatic),
				architectures.ContainerImageTypeAnnotation: string(architectures.ImageTypeUBI9Slim),
			},
			wantImageType: architectures.ImageTypeUBI9Slim,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			imageUrlsMock := images.ImageUrls{
				fmt.Sprintf("RELATED_IMAGE_%s_8_0_0", util.OpsManagerImageUrl): "quay.io/mongodb/mongodb-enterprise-ops-manager:@sha256:MONGODB_OPS_MANAGER",
				util.AgentImageUrlEnv: "quay.io/mongodb/mongodb-agent",
				util.MongodbImageEnv:  "quay.io/mongodb/mongodb-enterprise-server",
			}

			ctx := context.Background()
			testOm := DefaultOpsManagerBuilder().
				SetVersion("8.0.0").
				SetAppDbVersion("8.0.23").
				SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).
				Build()
			testOm.SetAnnotations(test.annotations)

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, client, _ := defaultTestOmReconciler(ctx, t, imageUrlsMock, "", "", testOm, nil, omConnectionFactory, test.defaultArchitecture)
			reconciler.defaultImageType = test.defaultImageType

			checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

			appDBSts := appsv1.StatefulSet{}
			err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &appDBSts)
			require.NoError(t, err)
			wantImage := fmt.Sprintf("quay.io/mongodb/mongodb-enterprise-server:8.0.23-%s", test.wantImageType)
			assert.Equal(t, wantImage, findContainerImage(appDBSts.Spec.Template.Spec.Containers, util.MongodbContainerName))
			assert.False(t, hasContainerImageTypeWarning(testOm.Status.AppDbStatus.Warnings), "AppDB status warnings: %v", testOm.Status.AppDbStatus.Warnings)
		})
	}
}

func TestOpsManagerAppDBContainerImageTypeAnnotationWarning(t *testing.T) {
	tests := []struct {
		name                string
		defaultArchitecture architectures.DefaultArchitecture
		imageType           string
		wantWarning         bool
	}{
		{
			name:                "supported annotation on static has no warning",
			defaultArchitecture: architectures.Static,
			imageType:           string(architectures.ImageTypeUBI9Slim),
		},
		{
			name:                "unsupported annotation on static warns",
			defaultArchitecture: architectures.Static,
			imageType:           "ubi9slim",
			wantWarning:         true,
		},
		{
			name:                "supported annotation on non-static has no warning",
			defaultArchitecture: architectures.NonStatic,
			imageType:           string(architectures.ImageTypeUBI9Slim),
		},
		{
			name:                "unsupported annotation on non-static warns",
			defaultArchitecture: architectures.NonStatic,
			imageType:           "ubi9slim",
			wantWarning:         true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).Build()
			testOm.SetAnnotations(map[string]string{architectures.ContainerImageTypeAnnotation: test.imageType})

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, test.defaultArchitecture)

			checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

			assert.Equal(t, test.wantWarning, hasContainerImageTypeWarning(testOm.Status.AppDbStatus.Warnings), "AppDB status warnings: %v", testOm.Status.AppDbStatus.Warnings)
		})
	}
}

func TestOpsManagerConnectionString_IsPassedAsSecretRef(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: false,
	}).Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

	sts := appsv1.StatefulSet{}
	err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Name), &sts)
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
	assert.NotNil(t, uriVol.Secret)
	assert.Equal(t, uriVol.Secret.SecretName, testOm.AppDBMongoConnectionStringSecretName())
}

func TestOpsManagerWithKMIP(t *testing.T) {
	ctx := context.Background()
	// given
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

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	addKMIPTestResources(ctx, client, testOm, mdbName, clientCertificatePrefix)
	configureBackupResources(ctx, client, testOm)

	// when
	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)
	sts := appsv1.StatefulSet{}
	err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Name), &sts)
	envs := sts.Spec.Template.Spec.Containers[0].Env
	volumes := sts.Spec.Template.Spec.Volumes
	volumeMounts := sts.Spec.Template.Spec.Containers[0].VolumeMounts

	// then
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
	ctx := context.Background()
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

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	configureBackupResources(ctx, client, testOm)

	mockedAdmin := api.NewMockedAdminProvider("testUrl", "publicApiKey", "privateApiKey", true)
	defer mockedAdmin.(*api.MockedOmAdmin).Reset()

	reconcilerHelper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, nil, zap.S())
	require.NoError(t, err)

	// when
	reconciler.prepareBackupInOpsManager(ctx, reconcilerHelper, testOm, mockedAdmin, &AppDBConfig{}, zap.S())
	blockStoreConfigs, _ := mockedAdmin.ReadBlockStoreConfigs(ctx)
	oplogConfigs, _ := mockedAdmin.ReadOplogStoreConfigs(ctx)
	s3Configs, _ := mockedAdmin.ReadS3Configs(ctx)
	daemonConfigs, _ := mockedAdmin.(*api.MockedOmAdmin).ReadDaemonConfigs()

	// then
	assert.Equal(t, assignmentLabels, blockStoreConfigs[0].Labels)
	assert.Equal(t, assignmentLabels, oplogConfigs[0].Labels)
	assert.Equal(t, assignmentLabels, s3Configs[0].Labels)
	assert.Equal(t, assignmentLabels, daemonConfigs[0].Labels)
}

func TestOpsManagerBackupObjectLock(t *testing.T) {
	ctx := context.Background()

	testOm := DefaultOpsManagerBuilder().
		SetVersion("8.0.19").
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddS3SnapshotStore(omv1.S3Config{Name: "s3-config", S3SecretRef: &omv1.SecretRef{Name: "s3-secret"}, ObjectLockEnabled: new(true)}).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	configureBackupResources(ctx, client, testOm)

	mockedAdmin := api.NewMockedAdminProvider("testUrl", "publicApiKey", "privateApiKey", true)
	defer mockedAdmin.(*api.MockedOmAdmin).Reset()

	reconcilerHelper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, nil, zap.S())
	require.NoError(t, err)

	// when
	reconciler.prepareBackupInOpsManager(ctx, reconcilerHelper, testOm, mockedAdmin, &AppDBConfig{}, zap.S())
	s3Configs, _ := mockedAdmin.ReadS3Configs(ctx)
	// then
	assert.Equal(t, true, *s3Configs[0].ObjectLockEnabled)
}

func TestOpsManagerBackupObjectLockNotSentWhenUnset(t *testing.T) {
	ctx := context.Background()

	testOm := DefaultOpsManagerBuilder().
		SetVersion("8.0.19").
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddS3SnapshotStore(omv1.S3Config{Name: "s3-config", S3SecretRef: &omv1.SecretRef{Name: "s3-secret"}}).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	configureBackupResources(ctx, client, testOm)

	mockedAdmin := api.NewMockedAdminProvider("testUrl", "publicApiKey", "privateApiKey", true)
	defer mockedAdmin.(*api.MockedOmAdmin).Reset()

	reconcilerHelper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, nil, zap.S())
	require.NoError(t, err)

	// when
	reconciler.prepareBackupInOpsManager(ctx, reconcilerHelper, testOm, mockedAdmin, &AppDBConfig{}, zap.S())
	s3Configs, _ := mockedAdmin.ReadS3Configs(ctx)
	// then
	assert.Nil(t, s3Configs[0].ObjectLockEnabled)
}

func TestOpsManagerBackupObjectLockRetentionFields(t *testing.T) {
	ctx := context.Background()

	testOm := DefaultOpsManagerBuilder().
		SetVersion("8.0.27").
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddS3SnapshotStore(omv1.S3Config{Name: "s3-config", S3SecretRef: &omv1.SecretRef{Name: "s3-secret"}, ObjectLockEnabled: new(true), ObjectRetentionDays: new(30), ObjectRetentionMode: new("COMPLIANCE")}).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	configureBackupResources(ctx, client, testOm)

	mockedAdmin := api.NewMockedAdminProvider("testUrl", "publicApiKey", "privateApiKey", true)
	defer mockedAdmin.(*api.MockedOmAdmin).Reset()

	reconcilerHelper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, nil, zap.S())
	require.NoError(t, err)

	// when
	reconciler.prepareBackupInOpsManager(ctx, reconcilerHelper, testOm, mockedAdmin, &AppDBConfig{}, zap.S())
	s3Configs, _ := mockedAdmin.ReadS3Configs(ctx)
	// then
	require.Len(t, s3Configs, 1)
	assert.Equal(t, true, *s3Configs[0].ObjectLockEnabled)
	assert.Equal(t, 30, *s3Configs[0].ObjectRetentionDays)
	assert.Equal(t, "COMPLIANCE", *s3Configs[0].ObjectRetentionMode)
}

func TestTriggerOmChangedEventIfNeeded(t *testing.T) {
	ctx := context.Background()
	t.Run("Om changed event got triggered, major version update", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(ctx, omv1.NewOpsManagerBuilder().SetVersion("5.2.13").SetOMStatusVersion("4.2.13").Build(), nil, architectures.NonStatic, zap.S()))
		assert.NotEqual(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
	t.Run("Om changed event got triggered, minor version update", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(ctx, omv1.NewOpsManagerBuilder().SetVersion("4.4.0").SetOMStatusVersion("4.2.13").Build(), nil, architectures.NonStatic, zap.S()))
		assert.NotEqual(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
	t.Run("Om changed event got triggered, minor version update, candidate version", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(ctx, omv1.NewOpsManagerBuilder().SetVersion("4.4.0-rc2").SetOMStatusVersion("4.2.13").Build(), nil, architectures.NonStatic, zap.S()))
		assert.NotEqual(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
	t.Run("Om changed event not triggered, patch version update", func(t *testing.T) {
		nextScheduledTime := agents.NextScheduledUpgradeTime()
		assert.NoError(t, triggerOmChangedEventIfNeeded(ctx, omv1.NewOpsManagerBuilder().SetVersion("4.4.10").SetOMStatusVersion("4.4.0").Build(), nil, architectures.NonStatic, zap.S()))
		assert.Equal(t, nextScheduledTime, agents.NextScheduledUpgradeTime())
	})
}

func TestBackupIsStillConfigured_WhenAppDBIsConfigured_WithTls(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().AddS3Config("s3-config", "s3-secret").
		AddOplogStoreConfig("oplog-store-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		SetAppDBTLSConfig(mdbv1.TLSConfig{Enabled: true}).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, mockedClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	addAppDBTLSResources(ctx, mockedClient, fmt.Sprintf("%s-cert", testOm.Spec.AppDB.Name()))
	configureBackupResources(ctx, mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)
	assert.True(t, res.RequeueAfter > 0)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))

	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)
}

func TestBackupConfig_ChangingName_ResultsIn_DeleteAndAdd(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().
		AddOplogStoreConfig("oplog-store", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddS3Config("s3-config-0", "s3-secret").
		AddS3Config("s3-config-1", "s3-secret").
		AddS3Config("s3-config-2", "s3-secret").
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, mockedClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	configureBackupResources(ctx, mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)
	assert.True(t, res.RequeueAfter > 0)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("Configs are created successfully", func(t *testing.T) {
		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs(ctx)
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 3)
	})

	require.NoError(t, mockedClient.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))
	testOm.Spec.Backup.S3Configs[0].Name = "new-name"
	err = mockedClient.Update(ctx, testOm)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("Name change resulted in a different config being created", func(t *testing.T) {
		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs(ctx)
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 3)

		assert.Equal(t, "new-name", s3Configs[0].Id)
		assert.Equal(t, "s3-config-1", s3Configs[1].Id)
		assert.Equal(t, "s3-config-2", s3Configs[2].Id)
	})
}

func TestOpsManagerRace(t *testing.T) {
	ctx := context.Background()
	opsManager1 := DefaultOpsManagerBuilder().SetName("om1").
		AddOplogStoreConfig("oplog-store-1", "my-user", types.NamespacedName{Name: "config-1-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-1", "my-user", types.NamespacedName{Name: "config-1-mdb", Namespace: mock.TestNamespace}).Build()
	opsManager2 := DefaultOpsManagerBuilder().SetName("om2").
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-2-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-2", "my-user", types.NamespacedName{Name: "config-2-mdb", Namespace: mock.TestNamespace}).Build()
	opsManager3 := DefaultOpsManagerBuilder().SetName("om3").
		AddOplogStoreConfig("oplog-store-3", "my-user", types.NamespacedName{Name: "config-3-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-3", "my-user", types.NamespacedName{Name: "config-3-mdb", Namespace: mock.TestNamespace}).Build()

	resourceToProjectMapping := map[string]string{
		"om1": opsManager1.Spec.AppDB.GetName(),
		"om2": opsManager2.Spec.AppDB.GetName(),
		"om3": opsManager3.Spec.AppDB.GetName(),
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory().WithResourceToProjectMapping(resourceToProjectMapping)
	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(opsManager1, opsManager2, opsManager3).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
		}).Build()

	kubeClient := kubernetesClient.NewClient(fakeClient)
	// create an admin user secret
	data := map[string]string{"Username": "jane.doe@g.com", "Password": "pwd", "FirstName": "Jane", "LastName": "Doe"}

	// All instances can use the same secret
	s := secret.Builder().
		SetName(opsManager1.Spec.AdminSecret).
		SetNamespace(opsManager1.Namespace).
		SetStringMapToData(data).
		SetLabels(map[string]string{}).
		SetOwnerReferences(kube.BaseOwnerReference(opsManager1)).
		Build()

	configureBackupResources(ctx, kubeClient, opsManager1)
	configureBackupResources(ctx, kubeClient, opsManager2)
	configureBackupResources(ctx, kubeClient, opsManager3)

	initializer := &MockedInitializer{expectedOmURL: opsManager1.CentralURL(), t: t, skipChecks: true}

	reconciler := NewOpsManagerReconciler(ctx, kubeClient, nil, nil, "fake-initDatabaseVersion", "fake-initOpsManagerImageVersion", architectures.NonStatic, "", omConnectionFactory.GetConnectionFunc, initializer, func(baseUrl string, user string, publicApiKey string, ca *string) api.OpsManagerAdmin {
		return api.NewMockedAdminProvider(baseUrl, user, publicApiKey, false).(*api.MockedOmAdmin)
	})

	assert.NoError(t, reconciler.client.CreateSecret(ctx, s))

	testConcurrentReconciles(ctx, t, kubeClient, reconciler, opsManager1, opsManager2, opsManager3)
}

func TestBackupConfigs_AreRemoved_WhenRemovedFromCR(t *testing.T) {
	ctx := context.Background()
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

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, mockedClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	configureBackupResources(ctx, mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)
	assert.True(t, res.RequeueAfter > 0)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))

	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)

	t.Run("Configs are created successfully", func(t *testing.T) {
		configs, err := api.CurrMockedAdmin.ReadOplogStoreConfigs(ctx)
		assert.NoError(t, err)
		assert.Len(t, configs, 3)

		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs(ctx)
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 3)

		blockstores, err := api.CurrMockedAdmin.ReadBlockStoreConfigs(ctx)
		assert.NoError(t, err)
		assert.Len(t, blockstores, 3)
	})

	require.NoError(t, mockedClient.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))

	// remove the first entry
	testOm.Spec.Backup.OplogStoreConfigs = testOm.Spec.Backup.OplogStoreConfigs[1:]

	// remove middle element
	testOm.Spec.Backup.S3Configs = []omv1.S3Config{testOm.Spec.Backup.S3Configs[0], testOm.Spec.Backup.S3Configs[2]}

	// remove first and last
	testOm.Spec.Backup.BlockStoreConfigs = []omv1.DataStoreConfig{testOm.Spec.Backup.BlockStoreConfigs[1]}

	err = mockedClient.Update(ctx, testOm)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("Configs are removed successfully", func(t *testing.T) {
		configs, err := api.CurrMockedAdmin.ReadOplogStoreConfigs(ctx)
		assert.NoError(t, err)
		assert.Len(t, configs, 2)

		assert.Equal(t, "oplog-store-1", configs[0].Id)
		assert.Equal(t, "oplog-store-2", configs[1].Id)

		s3Configs, err := api.CurrMockedAdmin.ReadS3Configs(ctx)
		assert.NoError(t, err)
		assert.Len(t, s3Configs, 2)

		assert.Equal(t, "s3-config-0", s3Configs[0].Id)
		assert.Equal(t, "s3-config-2", s3Configs[1].Id)

		blockstores, err := api.CurrMockedAdmin.ReadBlockStoreConfigs(ctx)
		assert.NoError(t, err)
		assert.Len(t, blockstores, 1)
		assert.Equal(t, "block-store-config-1", blockstores[0].Id)
	})
}

func TestDependentResources_AreRemoved_WhenBackupIsDisabled(t *testing.T) {
	ctx := context.Background()
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

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, mockedClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	configureBackupResources(ctx, mockedClient, testOm)

	// initially requeued as monitoring needs to be configured
	res, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)
	assert.True(t, res.RequeueAfter > 0)

	// monitoring is configured successfully
	res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))
	assert.NoError(t, err)

	t.Run("All MongoDB resource should be watched.", func(t *testing.T) {
		assert.Len(t, reconciler.resourceWatcher.GetWatchedResourcesOfType(watch.MongoDB, testOm.Namespace), 6, "All non S3 configs should have a corresponding MongoDB resource and should be watched.")
	})

	t.Run("Removing backup configs causes the resource no longer be watched", func(t *testing.T) {
		// remove last
		require.NoError(t, mockedClient.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))
		testOm.Spec.Backup.BlockStoreConfigs = testOm.Spec.Backup.BlockStoreConfigs[0:2]
		// remove first
		testOm.Spec.Backup.OplogStoreConfigs = testOm.Spec.Backup.OplogStoreConfigs[1:3]
		err = mockedClient.Update(ctx, testOm)
		assert.NoError(t, err)

		res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))
		assert.NoError(t, err)

		watchedResources := reconciler.resourceWatcher.GetWatchedResourcesOfType(watch.MongoDB, testOm.Namespace)
		assert.Len(t, watchedResources, 4, "The two configs that were removed should no longer be watched.")

		assert.True(t, containsName("block-store-config-0-mdb", watchedResources))
		assert.True(t, containsName("block-store-config-1-mdb", watchedResources))
		assert.True(t, containsName("config-1-mdb", watchedResources))
		assert.True(t, containsName("config-2-mdb", watchedResources))
	})

	t.Run("Disabling backup should cause all resources to no longer be watched.", func(t *testing.T) {
		require.NoError(t, mockedClient.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))
		testOm.Spec.Backup.Enabled = false
		err = mockedClient.Update(ctx, testOm)
		assert.NoError(t, err)

		res, err = reconciler.Reconcile(ctx, requestFromObject(testOm))
		assert.NoError(t, err)
		assert.Len(t, reconciler.resourceWatcher.GetWatchedResourcesOfType(watch.MongoDB, testOm.Namespace), 0, "Backup has been disabled, none of the resources should be watched anymore.")
	})
}

func TestUniqueClusterNames(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	testOm.Spec.AppDB.Topology = "MultiCluster"
	testOm.Spec.AppDB.ClusterSpecList = mdbv1.ClusterSpecList{
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

	validator := &omv1.MongoDBOpsManagerValidator{}
	_, err := validator.ValidateCreate(ctx, testOm)
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
func configureBackupResources(ctx context.Context, m kubernetesClient.Client, testOm *omv1.MongoDBOpsManager) {
	// configure S3 Secret
	for _, s3Config := range testOm.Spec.Backup.S3Configs {
		// skip if no secret ref is provided (valid for IRSA enabled deployments)
		if s3Config.S3SecretRef == nil {
			continue
		}

		s3Creds := secret.Builder().
			SetName(s3Config.S3SecretRef.Name).
			SetNamespace(testOm.Namespace).
			SetField(util.S3AccessKey, "s3AccessKey").
			SetField(util.S3SecretKey, "s3SecretKey").
			Build()
		_ = m.CreateSecret(ctx, s3Creds)
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

		_ = m.Create(ctx, oplogStoreResource)

		// create user for mdb resource
		oplogStoreUser := DefaultMongoDBUserBuilder().
			SetResourceName(oplogConfig.MongoDBUserRef.Name).
			SetNamespace(testOm.Namespace).
			Build()

		_ = m.Create(ctx, oplogStoreUser)

		// create secret for user
		userPasswordSecret := secret.Builder().
			SetNamespace(testOm.Namespace).
			SetName(oplogStoreUser.Spec.PasswordSecretKeyRef.Name).
			SetField(oplogStoreUser.Spec.PasswordSecretKeyRef.Key, "KeJfV1ucQ_vZl").
			Build()

		_ = m.CreateSecret(ctx, userPasswordSecret)
	}
}

func defaultTestOmReconciler(ctx context.Context, t *testing.T, imageUrls images.ImageUrls, initDatabaseVersion, initOpsManagerImageVersion string, opsManager *omv1.MongoDBOpsManager, globalMemberClustersMap map[string]client.Client, omConnectionFactory *om.CachedOMConnectionFactory, arch architectures.DefaultArchitecture) (*OpsManagerReconciler, kubernetesClient.Client, *MockedInitializer) {
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory, opsManager.DeepCopy())

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

	reconciler := NewOpsManagerReconciler(ctx, kubeClient, globalMemberClustersMap, imageUrls, initDatabaseVersion, initOpsManagerImageVersion, arch, "", omConnectionFactory.GetConnectionFunc, initializer, func(baseUrl string, user string, publicApiKey string, ca *string) api.OpsManagerAdmin {
		if api.CurrMockedAdmin == nil {
			api.CurrMockedAdmin = api.NewMockedAdminProvider(baseUrl, user, publicApiKey, true).(*api.MockedOmAdmin)
		}
		return api.CurrMockedAdmin
	})

	assert.NoError(t, reconciler.client.CreateSecret(ctx, s))
	return reconciler, kubeClient, initializer
}

func DefaultOpsManagerBuilder() *omv1.OpsManagerBuilder {
	spec := omv1.MongoDBOpsManagerSpec{
		Version:     "7.0.0",
		AppDB:       omv1.DefaultAppDbBuilder().Build(),
		AdminSecret: "om-admin",
	}
	resource := omv1.MongoDBOpsManager{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "test-om", Namespace: mock.TestNamespace}}
	return omv1.NewOpsManagerBuilderFromResource(resource)
}

type MockedInitializer struct {
	mu                sync.RWMutex
	currentUsers      []api.User
	expectedAPIError  *apierror.Error
	expectedOmURL     string
	expectedCaContent *string
	t                 *testing.T
	numberOfCalls     int
	skipChecks        bool
}

func (o *MockedInitializer) TryCreateUser(_ context.Context, omUrl string, _ string, user api.User, ca *string) (api.OpsManagerKeyPair, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.numberOfCalls++
	if !o.skipChecks {
		assert.Equal(o.t, o.expectedOmURL, omUrl)
	}

	if o.expectedCaContent != nil {
		assert.Equal(o.t, *o.expectedCaContent, *ca)
	}
	if o.expectedAPIError != nil {
		return api.OpsManagerKeyPair{}, o.expectedAPIError
	}
	// OM logic: any number of users is created. But we cannot of course create the user with the same name
	if !o.skipChecks {
		for _, v := range o.currentUsers {
			if v.Username == user.Username {
				return api.OpsManagerKeyPair{}, apierror.NewErrorWithCode(apierror.UserAlreadyExists)
			}
		}
	}
	o.currentUsers = append(o.currentUsers, user)

	return api.OpsManagerKeyPair{
		PublicKey:  user.Username,
		PrivateKey: user.Username + "-key",
	}, nil
}

func addKMIPTestResources(ctx context.Context, client client.Client, om *omv1.MongoDBOpsManager, mdbName, clientCertificatePrefixName string) {
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
	_ = client.Create(ctx, mdb)

	mockCert, mockKey := createMockCertAndKeyBytes()

	ca := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      om.Spec.Backup.Encryption.Kmip.Server.CA,
			Namespace: om.Namespace,
		},
	}
	ca.Data = map[string]string{}
	ca.Data["ca.pem"] = string(mockCert)
	_ = client.Create(ctx, ca)

	clientCertificate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdb.GetBackupSpec().Encryption.Kmip.Client.ClientCertificateSecretName(mdb.GetName()),
			Namespace: om.Namespace,
		},
	}
	clientCertificate.Data = map[string][]byte{}
	clientCertificate.Data["tls.key"] = mockKey
	clientCertificate.Data["tls.crt"] = mockCert
	_ = client.Create(ctx, clientCertificate)

	clientCertificatePassword := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdb.GetBackupSpec().Encryption.Kmip.Client.ClientCertificatePasswordSecretName(mdb.GetName()),
			Namespace: om.Namespace,
		},
	}
	clientCertificatePassword.Data = map[string]string{
		mdb.GetBackupSpec().Encryption.Kmip.Client.ClientCertificatePasswordKeyName(): "test",
	}
	_ = client.Create(ctx, clientCertificatePassword)
}

func addAppDBTLSResources(ctx context.Context, client client.Client, secretName string) {
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
	_ = client.Create(ctx, certSecret)
}

func withExternalAppDBRef(om *omv1.MongoDBOpsManager, ref *omv1.ExternalAppDBRef) *omv1.MongoDBOpsManager {
	if ref.Namespace == "" {
		ref.Namespace = om.Namespace
	}
	om.Spec.ExternalAppDBRef = ref
	return om
}

func TestOpsManagerReconcile_ExternalAppDBRef_SkipsInternalAppDBReconciliation(t *testing.T) {
	ctx := context.Background()

	externalAppDB := mdbv1.NewReplicaSetBuilder().
		SetName("test-om-db").
		SetNamespace(mock.TestNamespace).
		SetVersion("6.0.0").
		Build()
	externalAppDB.Spec.Role = mdbv1.RoleAppDB

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
		Name: "test-om-db",
		Kind: "MongoDB",
	})

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, externalAppDB))

	_, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	require.NoError(t, err)

	appDBSts := appsv1.StatefulSet{}
	err = kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &appDBSts)
	assert.True(t, apiErrors.IsNotFound(err), "expected internal AppDB StatefulSet to not be created, got err=%v", err)
}

func TestOpsManagerReconcile_NoExternalAppDBRef_StillReconcilesInternalAppDB(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().
		AddOplogStoreConfig("oplog-store-1", "my-user", types.NamespacedName{Name: "config-1-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-1", "my-user", types.NamespacedName{Name: "config-1-mdb", Namespace: mock.TestNamespace}).
		Build()
	require.Nil(t, testOm.Spec.ExternalAppDBRef)

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	configureBackupResources(ctx, client, testOm)

	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

	appDBSts := appsv1.StatefulSet{}
	err := client.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &appDBSts)
	require.NoError(t, err, "internal AppDB StatefulSet should still be created when externalApplicationDatabaseRef is not set")
}

func TestOpsManagerReconcile_InvalidExternalAppDBRef_FailsReconcile(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
		Name: "wrong-name",
		Kind: "MongoDB",
	})

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	res, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)

	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKeyFromApiObject(testOm), testOm))
	assert.Equal(t, status.PhaseFailed, testOm.GetPhase())

	appDBSts := appsv1.StatefulSet{}
	err = kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &appDBSts)
	assert.True(t, apiErrors.IsNotFound(err), "expected internal AppDB StatefulSet to not be created when validation fails")
}

func TestReconcile_ExternalAppDBRef_NeverCreatesInternalPasswordSecret(t *testing.T) {
	ctx := context.Background()

	externalAppDB := mdbv1.NewReplicaSetBuilder().
		SetName("test-om-db").
		SetNamespace(mock.TestNamespace).
		SetVersion("6.0.0").
		SetMembers(3).
		Build()
	externalAppDB.Spec.Role = mdbv1.RoleAppDB

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
		Name: "test-om-db",
		Kind: "MongoDB",
	})

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, externalAppDB))
	require.NoError(t, reconciler.client.CreateSecret(ctx, secret.Builder().
		SetName(omv1.OpsManagerUserPasswordSecretName("test-om-db")).
		SetNamespace(testOm.Namespace).
		SetField(util.OpsManagerPasswordKey, "test-password").
		Build()))

	_, _ = reconciler.Reconcile(ctx, requestFromObject(testOm))

	// the shared password secret ("test-om-db-om-password") is the same secret
	// GetOpsManagerUserPasswordSecretName() would compute for internal AppDB, since
	// externalApplicationDatabaseRef.Name is required to equal AppDBSpec.Name(). If
	// ensureAppDbPassword had run unconditionally, it would either overwrite this
	// value or fail to find automation-config resources it expects — asserting the
	// pre-seeded password is unchanged proves that internal path never ran.
	assert.Equal(t, testOm.Spec.AppDB.GetOpsManagerUserPasswordSecretName(), omv1.OpsManagerUserPasswordSecretName("test-om-db"),
		"test assumption: internal and shared password secret names must coincide")

	result := corev1.Secret{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, omv1.OpsManagerUserPasswordSecretName("test-om-db")), &result))
	assert.Equal(t, "test-password", string(result.Data[util.OpsManagerPasswordKey]))
}

func TestOpsManagerReconcile_ExternalAppDBRef_TLS_MountsAppDBCAVolume(t *testing.T) {
	ctx := context.Background()

	externalAppDB := mdbv1.NewReplicaSetBuilder().
		SetName("test-om-db").
		SetNamespace(mock.TestNamespace).
		SetVersion("6.0.0").
		SetMembers(3).
		SetSecurityTLSEnabled().
		Build()
	externalAppDB.Spec.Role = mdbv1.RoleAppDB
	externalAppDB.Spec.Security.TLSConfig.CA = "app-db-issuer-ca"

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
		Name: "test-om-db",
		Kind: "MongoDB",
	})

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, externalAppDB))
	// the ownership gate waits for the external AppDB StatefulSet to exist before the OM deploys
	externalAppDBSts := DefaultStatefulSetBuilder().SetName("test-om-db").SetLabels(map[string]string{util.MongoDBResourceOwnerLabel: "test-om-db"}).Build()
	require.NoError(t, reconciler.client.Create(ctx, &externalAppDBSts))
	require.NoError(t, reconciler.client.CreateSecret(ctx, secret.Builder().
		SetName(omv1.OpsManagerUserPasswordSecretName("test-om-db")).
		SetNamespace(testOm.Namespace).
		SetField(util.OpsManagerPasswordKey, "test-password").
		Build()))

	_, err := reconciler.Reconcile(ctx, requestFromObject(testOm))
	require.NoError(t, err)

	omSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Name), &omSts))

	var caVolume *corev1.Volume
	for i := range omSts.Spec.Template.Spec.Volumes {
		if omSts.Spec.Template.Spec.Volumes[i].Name == "appdb-ca-certificate" {
			caVolume = &omSts.Spec.Template.Spec.Volumes[i]
		}
	}
	require.NotNil(t, caVolume, "expected appdb-ca-certificate volume on OM StatefulSet when external AppDB has TLS enabled")
	require.NotNil(t, caVolume.ConfigMap)
	assert.Equal(t, "app-db-issuer-ca", caVolume.ConfigMap.Name)
}

func addOMTLSResources(ctx context.Context, client client.Client, secretName string) {
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
	_ = client.Create(ctx, certSecret)
}

func newManagedCertificateKubeClient(t *testing.T, objects ...client.Object) kubernetesClient.Client {
	t.Helper()
	return newManagedCertificateKubeClientWithInterceptor(t, interceptor.Funcs{}, objects...)
}

func newManagedCertificateKubeClientWithInterceptor(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) kubernetesClient.Client {
	t.Helper()
	scheme, err := v1.SchemeBuilder.Build()
	require.NoError(t, err)
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, certmanagerv1.AddToScheme(scheme))

	return kubernetesClient.NewClient(fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&omv1.MongoDBOpsManager{}).
		WithObjects(objects...).
		WithInterceptorFuncs(funcs).
		Build())
}

func newManagedCertificateTestReconciler(ctx context.Context, c kubernetesClient.Client) *OpsManagerReconciler {
	return NewOpsManagerReconciler(ctx, c, nil, images.ImageUrls{}, "", "", architectures.NonStatic, "", om.NewOpsManagerConnection, nil, nil)
}

func newManagedCertificateTestOM() *omv1.MongoDBOpsManager {
	opsManager := DefaultOpsManagerBuilder().
		SetName("managed-om").
		SetNamespace(mock.TestNamespace).
		SetAppDbMembers(1).
		SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: true, Members: 1}).
		Build()
	opsManager.UID = types.UID("managed-om-uid")
	opsManager.Generation = 1
	opsManager.Spec.Security = &omv1.MongoDBOpsManagerSecurity{ManagedCertificate: &omv1.ManagedCertificate{}}
	return opsManager
}

func listManagedCertificateObjects(t *testing.T, ctx context.Context, c kubernetesClient.Client) []certmanagerv1.Certificate {
	t.Helper()
	certificates := &certmanagerv1.CertificateList{}
	require.NoError(t, c.List(ctx, certificates, client.InNamespace(mock.TestNamespace)))
	return certificates.Items
}

func managedCertificateNames(certificates []certmanagerv1.Certificate) []string {
	names := make([]string, 0, len(certificates))
	for _, certificate := range certificates {
		names = append(names, certificate.Name)
	}
	return names
}

func markAllManagedCertificatesReady(t *testing.T, ctx context.Context, c kubernetesClient.Client) {
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
		require.NoError(t, c.Update(ctx, &certificate))
	}
}

func createSecretsForManagedCertificates(t *testing.T, ctx context.Context, c kubernetesClient.Client, caPEM ...[]byte) {
	t.Helper()
	for _, certificate := range listManagedCertificateObjects(t, ctx, c) {
		secretName := certificate.Spec.SecretName
		if secretName == "" {
			secretName = certificate.Name
		}
		issued := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: certificate.Namespace},
			Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte("CA-" + certificate.Name)},
		}
		if len(caPEM) > 0 {
			issued.Data["ca.crt"] = caPEM[0]
			issued.Data["tls.crt"] = caPEM[0]
		}
		err := c.Create(ctx, issued)
		if apiErrors.IsAlreadyExists(err) {
			continue
		}
		require.NoError(t, err)
	}
}

func getPersistedManagedCertificateOM(t *testing.T, ctx context.Context, c kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager) *omv1.MongoDBOpsManager {
	t.Helper()
	persisted := &omv1.MongoDBOpsManager{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(opsManager), persisted))
	return persisted
}

func managedCertificateCondition(t *testing.T, ctx context.Context, c kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager) *metav1.Condition {
	t.Helper()
	persisted := getPersistedManagedCertificateOM(t, ctx, c, opsManager)
	return meta.FindStatusCondition(persisted.Status.Conditions, certspkg.ConditionCertificatesReady)
}

func managedCertificateResult(part status.Part, workflowStatus workflow.Status, conditionStatus metav1.ConditionStatus, reason, message string) componentCertificateResult {
	return componentCertificateResult{
		part:   part,
		status: workflowStatus,
		condition: &metav1.Condition{
			Type:    certspkg.ConditionCertificatesReady,
			Status:  conditionStatus,
			Reason:  reason,
			Message: message,
		},
	}
}

func TestOpsManagerManagedCertificateBootstrap(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	opsManager := newManagedCertificateTestOM()
	c := newManagedCertificateKubeClient(t, opsManager)
	reconciler := newManagedCertificateTestReconciler(ctx, c)
	reconciler.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}
	result, blocked, err := reconciler.prepareManagedCertificates(ctx, opsManager, log)
	require.NoError(t, err)
	require.True(t, blocked)
	require.NotZero(t, result.RequeueAfter)

	certificateNames := managedCertificateNames(listManagedCertificateObjects(t, ctx, c))
	assert.Contains(t, certificateNames, opsManager.TLSCertificateSecretName(), "Ops Manager certificate preparation must run")
	assert.NotContains(t, certificateNames, opsManager.Spec.AppDB.GetSecurity().MemberCertificateSecretName(opsManager.Spec.AppDB.Name()))
	assert.NotContains(t, certificateNames, opsManager.QueryableBackupCertificateSecretName())

	caConfigMap := &corev1.ConfigMap{}
	err = c.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.GetOpsManagerCA()), caConfigMap)
	require.True(t, apiErrors.IsNotFound(err), "the OM CA bundle must not exist before its certificate is ready")
	condition := managedCertificateCondition(t, ctx, c, opsManager)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, "Issuing", condition.Reason)

	markAllManagedCertificatesReady(t, ctx, c)
	ca, issue := managedOMTestCA(t)
	createSecretsForManagedCertificates(t, ctx, c, ca)
	issued := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.TLSCertificateSecretName()), issued))
	issued.Type = corev1.SecretTypeTLS
	issued.Data[corev1.TLSCertKey], issued.Data[corev1.TLSPrivateKeyKey] = issue()
	require.NoError(t, c.Update(ctx, issued))

	_, blocked, err = reconciler.prepareManagedCertificates(ctx, opsManager, log)
	require.NoError(t, err)
	require.False(t, blocked)
	require.NoError(t, c.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.GetOpsManagerCA()), caConfigMap))
	assert.NotEmpty(t, caConfigMap.Data["mms-ca.crt"])

	projectConfig, err := opsManager.GetAppDBProjectConfig(ctx, c)
	require.NoError(t, err)
	assert.Equal(t, opsManager.GetOpsManagerCA(), projectConfig.SSLMMSCAConfigMap)
	assert.Equal(t, caConfigMap.Data["mms-ca.crt"], projectConfig.SSLMMSCAConfigMapContents,
		"the OM CA bundle must be populated before AppDB project configuration consumes it")

	_, err = reconciler.updateStatus(ctx, opsManager, workflow.OK(), log, status.NewOMPartOption(status.OpsManager))
	require.NoError(t, err)

	lastFailure := metav1.NewTime(time.Now().Add(-time.Hour))
	for _, certificate := range listManagedCertificateObjects(t, ctx, c) {
		if certificate.Name != opsManager.TLSCertificateSecretName() {
			continue
		}
		certificate.Status.LastFailureTime = &lastFailure
		require.NoError(t, c.Update(ctx, &certificate))
	}

	_, blocked, err = reconciler.prepareManagedCertificates(ctx, opsManager, log)
	require.NoError(t, err)
	require.False(t, blocked, "a renewal failure must not block workload reconciliation")
	condition = managedCertificateCondition(t, ctx, c, opsManager)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, "RenewalFailed", condition.Reason)
	persisted := getPersistedManagedCertificateOM(t, ctx, c, opsManager)
	assert.Equal(t, status.PhaseRunning, persisted.Status.OpsManagerStatus.Phase)
}

func TestOpsManagerManagedCertificateAggregation(t *testing.T) {
	ready := func(part status.Part, message string) componentCertificateResult {
		return managedCertificateResult(part, workflow.OK(), metav1.ConditionTrue, "AllCertificatesReady", message)
	}
	issuing := func(part status.Part, message string) componentCertificateResult {
		return managedCertificateResult(part, workflow.Pending("issuing"), metav1.ConditionFalse, "Issuing", message)
	}
	failed := func(part status.Part, message string) componentCertificateResult {
		return managedCertificateResult(part, workflow.Failed(errors.New(message)), metav1.ConditionFalse, "IssuanceFailed", message)
	}
	renewalFailed := func(part status.Part, message string) componentCertificateResult {
		return managedCertificateResult(part, workflow.OK(), metav1.ConditionFalse, "RenewalFailed", message)
	}

	t.Run("no managed components yields no condition", func(t *testing.T) {
		assert.Nil(t, aggregateCertificateConditions([]componentCertificateResult{
			{part: status.OpsManager, status: workflow.OK()},
			{part: status.AppDb, status: workflow.OK()},
			{part: status.Backup, status: workflow.OK()},
		}))
	})

	t.Run("issuance failure outranks every other state in fixed component order", func(t *testing.T) {
		condition := aggregateCertificateConditions([]componentCertificateResult{
			ready(status.Backup, "backup ready"),
			issuing(status.AppDb, "appdb issuing"),
			failed(status.OpsManager, "om failed"),
			renewalFailed(status.AppDb, "appdb renewal"),
		})
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, "IssuanceFailed", condition.Reason)
		assert.Contains(t, condition.Message, "OpsManager: om failed")
		assert.Contains(t, condition.Message, "AppDB: appdb issuing")
		assert.Contains(t, condition.Message, "Backup: backup ready")
		require.Less(t, strings.Index(condition.Message, "OpsManager:"), strings.Index(condition.Message, "AppDB:"))
		require.Less(t, strings.Index(condition.Message, "AppDB:"), strings.Index(condition.Message, "Backup:"))
	})

	t.Run("issuing outranks renewal failure", func(t *testing.T) {
		condition := aggregateCertificateConditions([]componentCertificateResult{
			renewalFailed(status.OpsManager, "om renewal"),
			issuing(status.AppDb, "appdb issuing"),
			ready(status.Backup, "backup ready"),
		})
		require.NotNil(t, condition)
		assert.Equal(t, "Issuing", condition.Reason)
	})

	t.Run("renewal failure outranks ready", func(t *testing.T) {
		condition := aggregateCertificateConditions([]componentCertificateResult{
			ready(status.OpsManager, "om ready"),
			renewalFailed(status.AppDb, "appdb renewal"),
			ready(status.Backup, "backup ready"),
		})
		require.NotNil(t, condition)
		assert.Equal(t, "RenewalFailed", condition.Reason)
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
	})

	t.Run("all ready is true", func(t *testing.T) {
		condition := aggregateCertificateConditions([]componentCertificateResult{
			ready(status.OpsManager, "om ready"),
			ready(status.AppDb, "appdb ready"),
			ready(status.Backup, "backup ready"),
		})
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionTrue, condition.Status)
		assert.Equal(t, "AllCertificatesReady", condition.Reason)
	})

	t.Run("a later success cannot overwrite an earlier failure", func(t *testing.T) {
		condition := aggregateCertificateConditions([]componentCertificateResult{
			failed(status.OpsManager, "om failed"),
			ready(status.AppDb, "appdb ready"),
			ready(status.Backup, "backup ready"),
		})
		require.NotNil(t, condition)
		assert.Equal(t, "IssuanceFailed", condition.Reason)
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
	})
}

func TestOpsManagerManagedCertificateStatusPersistence(t *testing.T) {
	ctx := context.Background()
	log := zap.S()

	t.Run("root condition survives component patches and leaves other status intact", func(t *testing.T) {
		opsManager := newManagedCertificateTestOM()
		opsManager.Generation = 7
		opsManager.Status.OpsManagerStatus.Common.Phase = status.PhaseRunning
		opsManager.Status.AppDbStatus.Common.Phase = status.PhaseRunning
		opsManager.Status.BackupStatus.Common.Phase = status.PhaseRunning
		opsManager.Status.Conditions = []metav1.Condition{{
			Type:               "Unrelated",
			Status:             metav1.ConditionTrue,
			Reason:             "Kept",
			Message:            "keep me",
			LastTransitionTime: metav1.Now(),
		}}
		c := newManagedCertificateKubeClient(t, opsManager)
		reconciler := newManagedCertificateTestReconciler(ctx, c)

		results := []componentCertificateResult{
			managedCertificateResult(status.OpsManager, workflow.Pending("om issuing"), metav1.ConditionFalse, "Issuing", "om issuing"),
			managedCertificateResult(status.AppDb, workflow.Failed(errors.New("appdb certificate failure")), metav1.ConditionFalse, "IssuanceFailed", "appdb certificate failure"),
			managedCertificateResult(status.Backup, workflow.OK(), metav1.ConditionTrue, "AllCertificatesReady", "backup ready"),
		}

		_, blocked, err := reconciler.applyManagedCertificateResults(ctx, opsManager, results, log)
		require.NoError(t, err)
		require.True(t, blocked)

		persisted := getPersistedManagedCertificateOM(t, ctx, c, opsManager)
		condition := meta.FindStatusCondition(persisted.Status.Conditions, certspkg.ConditionCertificatesReady)
		require.NotNil(t, condition)
		assert.Equal(t, "IssuanceFailed", condition.Reason)
		assert.Equal(t, int64(7), condition.ObservedGeneration)
		require.NotNil(t, meta.FindStatusCondition(persisted.Status.Conditions, "Unrelated"))
		assert.Equal(t, status.PhasePending, persisted.Status.OpsManagerStatus.Phase)
		assert.Equal(t, status.PhaseFailed, persisted.Status.AppDbStatus.Phase)
		assert.Equal(t, status.PhaseRunning, persisted.Status.BackupStatus.Phase, "an OK component must not mark another component Running")

		require.NoError(t, reconciler.patchCertificateCondition(ctx, persisted, nil))
		persisted = getPersistedManagedCertificateOM(t, ctx, c, opsManager)
		assert.Nil(t, meta.FindStatusCondition(persisted.Status.Conditions, certspkg.ConditionCertificatesReady))
		require.NotNil(t, meta.FindStatusCondition(persisted.Status.Conditions, "Unrelated"))
		assert.Equal(t, status.PhasePending, persisted.Status.OpsManagerStatus.Phase)
		assert.Equal(t, status.PhaseFailed, persisted.Status.AppDbStatus.Phase)
		assert.Equal(t, status.PhaseRunning, persisted.Status.BackupStatus.Phase)
	})

	t.Run("nil condition does not write when already absent", func(t *testing.T) {
		opsManager := newManagedCertificateTestOM()
		opsManager.Status.Conditions = []metav1.Condition{{
			Type:               "Unrelated",
			Status:             metav1.ConditionTrue,
			Reason:             "Kept",
			LastTransitionTime: metav1.Now(),
		}}
		statusPatchCount := 0
		c := newManagedCertificateKubeClientWithInterceptor(t, interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, inner client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				statusPatchCount++
				return inner.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		}, opsManager)
		reconciler := newManagedCertificateTestReconciler(ctx, c)

		require.NoError(t, reconciler.patchCertificateCondition(ctx, opsManager, nil))
		assert.Zero(t, statusPatchCount, "removing an absent condition must not write")

		condition := &metav1.Condition{
			Type:    certspkg.ConditionCertificatesReady,
			Status:  metav1.ConditionFalse,
			Reason:  "Issuing",
			Message: "issuing",
		}
		require.NoError(t, reconciler.patchCertificateCondition(ctx, opsManager, condition))
		require.Equal(t, 1, statusPatchCount)

		statusPatchCount = 0
		require.NoError(t, reconciler.patchCertificateCondition(ctx, opsManager, nil))
		require.Equal(t, 1, statusPatchCount)
		persisted := getPersistedManagedCertificateOM(t, ctx, c, opsManager)
		assert.Nil(t, meta.FindStatusCondition(persisted.Status.Conditions, certspkg.ConditionCertificatesReady))
		require.NotNil(t, meta.FindStatusCondition(persisted.Status.Conditions, "Unrelated"))
	})

	t.Run("root status patch errors propagate", func(t *testing.T) {
		opsManager := newManagedCertificateTestOM()
		c := newManagedCertificateKubeClientWithInterceptor(t, interceptor.Funcs{
			SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
				return errors.New("status patch denied")
			},
		}, opsManager)
		reconciler := newManagedCertificateTestReconciler(ctx, c)

		err := reconciler.patchCertificateCondition(ctx, opsManager, &metav1.Condition{
			Type:    certspkg.ConditionCertificatesReady,
			Status:  metav1.ConditionTrue,
			Reason:  "AllCertificatesReady",
			Message: "ready",
		})
		require.ErrorContains(t, err, "status patch denied")
	})

	t.Run("a result computed for an older generation is rejected instead of stamped as current", func(t *testing.T) {
		opsManager := newManagedCertificateTestOM()
		opsManager.Generation = 2
		c := newManagedCertificateKubeClient(t, opsManager)
		reconciler := newManagedCertificateTestReconciler(ctx, c)

		staleReconcile := opsManager.DeepCopy()
		staleReconcile.Generation = 1
		err := reconciler.patchCertificateCondition(ctx, staleReconcile, &metav1.Condition{
			Type:    certspkg.ConditionCertificatesReady,
			Status:  metav1.ConditionTrue,
			Reason:  "AllCertificatesReady",
			Message: "ready for generation 1",
		})
		require.Error(t, err)
		assert.True(t, apiErrors.IsConflict(err), "a changed generation must surface as a conflict so reconciliation requeues: %v", err)

		persisted := getPersistedManagedCertificateOM(t, ctx, c, opsManager)
		assert.Nil(t, meta.FindStatusCondition(persisted.Status.Conditions, certspkg.ConditionCertificatesReady),
			"a stale reconcile result must not be published as observing the current generation")
	})

	t.Run("a concurrent condition writer is not clobbered", func(t *testing.T) {
		opsManager := newManagedCertificateTestOM()
		inserted := false
		c := newManagedCertificateKubeClientWithInterceptor(t, interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, inner client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == "status" && !inserted {
					inserted = true
					concurrent := &omv1.MongoDBOpsManager{}
					require.NoError(t, inner.Get(ctx, client.ObjectKeyFromObject(obj), concurrent))
					concurrent.SetStatusCondition(metav1.Condition{
						Type:    "OtherWriter",
						Status:  metav1.ConditionTrue,
						Reason:  "Kept",
						Message: "concurrent condition",
					})
					require.NoError(t, inner.Status().Update(ctx, concurrent))
				}
				return inner.Status().Patch(ctx, obj, patch, opts...)
			},
		}, opsManager)
		reconciler := newManagedCertificateTestReconciler(ctx, c)

		err := reconciler.patchCertificateCondition(ctx, opsManager, &metav1.Condition{
			Type:    certspkg.ConditionCertificatesReady,
			Status:  metav1.ConditionTrue,
			Reason:  "AllCertificatesReady",
			Message: "ready",
		})
		require.Error(t, err)
		assert.True(t, apiErrors.IsConflict(err), "the optimistic-lock patch must be rejected after a concurrent status write: %v", err)

		persisted := getPersistedManagedCertificateOM(t, ctx, c, opsManager)
		require.NotNil(t, meta.FindStatusCondition(persisted.Status.Conditions, "OtherWriter"),
			"the concurrent condition must survive the rejected patch")
		assert.Nil(t, meta.FindStatusCondition(persisted.Status.Conditions, certspkg.ConditionCertificatesReady),
			"the rejected patch must not be persisted")
	})
}

func TestOpsManagerManagedCertificateReadinessReason(t *testing.T) {
	for _, tc := range []struct {
		name       string
		readiness  certspkg.CertManagerReadiness
		legacy     bool
		wantReason string
		wantCause  string
	}{
		{name: "missing CRDs", readiness: certspkg.CertManagerReadiness{NotReadyReason: "cert-manager CRDs are not installed"}, wantReason: "IssuanceFailed", wantCause: "cert-manager CRDs are not installed"},
		{name: "RBAC denied", readiness: certspkg.CertManagerReadiness{NotReadyReason: "not allowed to create certificates in namespace my-namespace"}, wantReason: "IssuanceFailed", wantCause: "not allowed to create certificates in namespace my-namespace"},
		{name: "access check failed", readiness: certspkg.CertManagerReadiness{NotReadyReason: "access review failed: API server unavailable"}, wantReason: "IssuanceFailed", wantCause: "access review failed: API server unavailable"},
		{name: "zero value", wantReason: "IssuanceFailed", wantCause: "cert-manager is not installed in the cluster, or the operator is not allowed"},
		{name: "ready", readiness: certspkg.CertManagerReadiness{Ready: true}, wantReason: "Issuing"},
		{name: "legacy without cert-manager", legacy: true, readiness: certspkg.CertManagerReadiness{NotReadyReason: "cert-manager CRDs are not installed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			opsManager := newManagedCertificateTestOM()
			if tc.legacy {
				opsManager.Spec.Security.ManagedCertificate = nil
			}
			c := newManagedCertificateKubeClient(t, opsManager)
			r := newManagedCertificateTestReconciler(ctx, c)
			r.certManagerReadiness = tc.readiness
			result := r.prepareOpsManagerCertificate(ctx, opsManager, zap.S())
			_, blocked, err := r.applyManagedCertificateResults(ctx, opsManager, []componentCertificateResult{result}, zap.S())
			require.NoError(t, err)
			condition := managedCertificateCondition(t, ctx, c, opsManager)
			if tc.legacy {
				assert.False(t, blocked)
				assert.True(t, result.status.IsOK())
				assert.Nil(t, condition)
				assert.Empty(t, listManagedCertificateObjects(t, ctx, c))
				return
			}
			require.True(t, blocked)
			require.NotNil(t, condition)
			assert.Equal(t, tc.wantReason, condition.Reason)
			if tc.readiness.Ready {
				assert.Contains(t, managedCertificateNames(listManagedCertificateObjects(t, ctx, c)), opsManager.TLSCertificateSecretName())
				return
			}
			assert.Contains(t, condition.Message, tc.wantCause)
			assert.Contains(t, result.status.Message(), tc.wantCause)
			persisted := getPersistedManagedCertificateOM(t, ctx, c, opsManager)
			assert.Equal(t, status.PhaseFailed, persisted.Status.OpsManagerStatus.Phase)
			assert.Contains(t, persisted.Status.OpsManagerStatus.Message, tc.wantCause)
			assert.Empty(t, listManagedCertificateObjects(t, ctx, c))
		})
	}
}

func TestOpsManagerManagedCertificateSANs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		centralURL string
		domain     string
		external   bool
		clusters   []omv1.ClusterSpecOMItem
		wantDNS    []string
		wantIPs    []string
		wantError  bool
	}{
		{name: "default", wantDNS: []string{"example-svc.test.svc.cluster.local"}},
		{name: "custom domain and URL", domain: "custom.test", centralURL: "https://om.example.test:8443/path", wantDNS: []string{"example-svc.test.svc.custom.test", "om.example.test"}},
		{name: "IPv4", centralURL: "https://192.0.2.1:8443", wantDNS: []string{"example-svc.test.svc.cluster.local"}, wantIPs: []string{"192.0.2.1"}},
		{name: "IPv6", centralURL: "https://[2001:db8::1]:8443/path", wantDNS: []string{"example-svc.test.svc.cluster.local"}, wantIPs: []string{"2001:db8::1"}},
		{name: "deduplicated", centralURL: "https://example-svc.test.svc.cluster.local:8443", wantDNS: []string{"example-svc.test.svc.cluster.local"}},
		{name: "external service", external: true, wantDNS: []string{"example-svc-ext.test.svc.cluster.local", "example-svc.test.svc.cluster.local"}},
		{name: "multicluster external override", clusters: []omv1.ClusterSpecOMItem{
			{ClusterName: "member-b", Members: 1},
			{ClusterName: "member-a", Members: 1, MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{}},
		}, wantDNS: []string{"example-svc-ext.test.svc.cluster.local", "example-svc.test.svc.cluster.local"}},
		{name: "invalid URL", centralURL: "https://%broken", wantError: true},
		{name: "hostless", centralURL: "https:///path", wantError: true},
		{name: "relative", centralURL: "om.example.test/path", wantError: true},
		{name: "unsupported scheme", centralURL: "ftp://om.example.test", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opsManager := DefaultOpsManagerBuilder().SetName("example").SetNamespace("test").Build()
			opsManager.Spec.ClusterDomain = tc.domain
			opsManager.Spec.OpsManagerURL = tc.centralURL
			opsManager.Spec.ClusterSpecList = tc.clusters
			if tc.external {
				opsManager.Spec.MongoDBOpsManagerExternalConnectivity = &omv1.MongoDBOpsManagerServiceDefinition{}
			}
			dnsNames, ipAddresses, err := opsManagerCertificateSANs(opsManager)
			if tc.wantError {
				require.Error(t, err)
				opsManager.Spec.Security = &omv1.MongoDBOpsManagerSecurity{ManagedCertificate: &omv1.ManagedCertificate{}}
				c := newManagedCertificateKubeClient(t, opsManager)
				r := newManagedCertificateTestReconciler(t.Context(), c)
				r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}
				result := r.prepareOpsManagerCertificate(t.Context(), opsManager, zap.S())
				require.False(t, result.status.IsOK())
				require.NotNil(t, result.condition)
				assert.Equal(t, "IssuanceFailed", result.condition.Reason)
				assert.Empty(t, listManagedCertificateObjects(t, t.Context(), c))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantDNS, dnsNames)
			assert.Equal(t, tc.wantIPs, ipAddresses)
			slices.Reverse(opsManager.Spec.ClusterSpecList)
			reorderedDNS, reorderedIPs, err := opsManagerCertificateSANs(opsManager)
			require.NoError(t, err)
			assert.Equal(t, dnsNames, reorderedDNS)
			assert.Equal(t, ipAddresses, reorderedIPs)
		})
	}
}

func managedOMTestCA(t *testing.T) ([]byte, func() ([]byte, []byte)) {
	t.Helper()
	caPEM, caKeyPEM := createMockCertAndKeyBytes()
	caPair, err := tls.X509KeyPair(caPEM, caKeyPEM)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caPair.Certificate[0])
	require.NoError(t, err)
	serial := int64(1)
	return caPEM, func() ([]byte, []byte) {
		_, keyPEM := createMockCertAndKeyBytes()
		block, _ := pem.Decode(keyPEM)
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		require.NoError(t, err)
		serial++
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			DNSNames:    []string{"managed-om-svc.testNS.svc.cluster.local"},
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.(crypto.Signer).Public(), caPair.PrivateKey)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM
	}
}

func TestOpsManagerManagedHTTPSRenewal(t *testing.T) {
	ctx := t.Context()
	opsManager := newManagedCertificateTestOM()
	opsManager.Spec.AppDB.Security.ManagedCertificate = nil
	opsManager.Spec.Backup.ManagedCertificate = nil
	ca, issue := managedOMTestCA(t)
	c := newManagedCertificateKubeClient(t, opsManager)
	r := newManagedCertificateTestReconciler(ctx, c)
	r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}
	assert.False(t, r.prepareOpsManagerCertificate(ctx, opsManager, zap.S()).status.IsOK())
	markAllManagedCertificatesReady(t, ctx, c)
	createSecretsForManagedCertificates(t, ctx, c, ca)
	crt, key := issue()
	issued := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-om-managed-om-cert", Namespace: opsManager.Namespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": crt, "tls.key": key, "ca.crt": ca},
	}
	existing := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(issued), existing))
	issued.ResourceVersion = existing.ResourceVersion
	require.NoError(t, c.Update(ctx, issued))
	secretClient := secrets.SecretClient{KubeClient: c}
	member := multicluster.GetLegacyCentralMemberCluster(1, 0, c, secretClient)
	build := func() (appsv1.StatefulSet, corev1.Secret, string) {
		prepared := r.prepareOpsManagerCertificate(ctx, opsManager, zap.S())
		require.True(t, prepared.status.IsOK(), "%+v", prepared.condition)
		sts, err := operatorConstruct.OpsManagerStatefulSet(ctx, secretClient, opsManager, member, zap.S())
		require.NoError(t, err)
		derived := corev1.Secret{}
		require.NoError(t, c.Get(ctx, kube.ObjectKey(opsManager.Namespace, "managed-om-managed-om-cert-pem"), &derived))
		hash := string(derived.Data[util.LatestHashSecretKey])
		require.NotEmpty(t, hash)
		assert.Equal(t, append(append([]byte{}, issued.Data["tls.crt"]...), issued.Data["tls.key"]...), derived.Data[hash])
		assert.True(t, slices.ContainsFunc(sts.Spec.Template.Spec.Volumes, func(v corev1.Volume) bool {
			return v.Name == "om-https-certificate" && v.Secret != nil && v.Secret.SecretName == "managed-om-managed-om-cert-pem"
		}))
		assert.Contains(t, sts.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "OM_PROP_mms_https_PEMKeyFile", Value: util.MmsPemKeyFileDirInContainer + "/" + hash})
		bundle := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, kube.ObjectKey(opsManager.Namespace, "managed-om-managed-om-ca-bundle"), bundle))
		assert.Equal(t, string(ca), bundle.Data["mms-ca.crt"])
		return sts, derived, hash
	}
	firstSTS, firstPEM, firstHash := build()
	issued.Data["tls.crt"], issued.Data["tls.key"] = issue()
	require.NoError(t, c.Update(ctx, issued))
	secondSTS, secondPEM, secondHash := build()
	assert.NotEqual(t, firstHash, secondHash)
	assert.NotEqual(t, firstSTS.Spec.Template, secondSTS.Spec.Template, "renewed PEM path must trigger the existing rollout and launcher PKCS12 conversion")
	assert.Equal(t, firstHash, string(secondPEM.Data[util.PreviousHashSecretKey]))
	assert.Equal(t, strings.TrimSpace(string(firstPEM.Data[firstHash])), string(secondPEM.Data[firstHash]))
}

func TestOpsManagerManagedCAConsumableByMongoDBProjectConfig(t *testing.T) {
	ctx := t.Context()
	for _, sameCA := range []bool{false, true} {
		for _, verification := range []string{"", "true"} {
			t.Run(fmt.Sprintf("sameCA=%t/verification=%q", sameCA, verification), func(t *testing.T) {
				opsManager := newManagedCertificateTestOM()
				omCA, _ := managedOMTestCA(t)
				mdbCA, _ := managedOMTestCA(t)
				if sameCA {
					mdbCA = omCA
				}
				c := newManagedCertificateKubeClient(t, opsManager)
				r := newManagedCertificateTestReconciler(ctx, c)
				r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}
				r.prepareOpsManagerCertificate(ctx, opsManager, zap.S())
				markAllManagedCertificatesReady(t, ctx, c)
				createSecretsForManagedCertificates(t, ctx, c, omCA)
				leafSecret := &corev1.Secret{}
				require.NoError(t, c.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.TLSCertificateSecretName()), leafSecret))
				leafSecret.Data["ca.crt"] = omCA
				require.NoError(t, c.Update(ctx, leafSecret))
				prepared := r.prepareOpsManagerCertificate(ctx, opsManager, zap.S())
				require.True(t, prepared.status.IsOK(), "%+v", prepared.condition)
				rs := DefaultReplicaSetBuilder().SetName("workload").SetMembers(1).Build()
				rs.Namespace = opsManager.Namespace
				rs.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
				rs.Spec.OpsManagerConfig.ConfigMapRef.Name = "managed-project"
				projectCM := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "managed-project", Namespace: rs.Namespace}, Data: map[string]string{
					"baseUrl": opsManager.CentralURL(), "orgId": "", "projectName": "workload-project", "sslMMSCAConfigMap": "managed-om-managed-om-ca-bundle",
				}}
				if verification != "" {
					projectCM.Data["sslRequireValidMMSServerCertificates"] = verification
				}
				require.NoError(t, c.Create(ctx, projectCM))
				require.NoError(t, c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "workload-managed-server-ca-bundle", Namespace: rs.Namespace}, Data: map[string]string{"ca-pem": string(mdbCA)}}))
				cfg, err := project.ReadProjectConfig(ctx, c, kube.ObjectKey(rs.Namespace, rs.Spec.OpsManagerConfig.ConfigMapRef.Name), rs.Name)
				require.NoError(t, err)
				assert.True(t, cfg.SSLRequireValidMMSServerCertificates)
				assert.Equal(t, "managed-om-managed-om-ca-bundle", cfg.SSLMMSCAConfigMap)
				assert.Equal(t, string(omCA), cfg.SSLMMSCAConfigMapContents)
				sts := operatorConstruct.DatabaseStatefulSet(*rs, operatorConstruct.ReplicaSetOptions(func(opts *operatorConstruct.DatabaseStatefulSetOptions) {
					opts.PodVars = &env.PodEnvVars{BaseURL: cfg.BaseURL, SSLProjectConfig: cfg.SSLProjectConfig}
				}), zap.S())
				volumes := map[string]string{}
				for _, volume := range sts.Spec.Template.Spec.Volumes {
					if volume.ConfigMap != nil {
						volumes[volume.Name] = volume.ConfigMap.Name
					}
				}
				assert.Contains(t, volumes, operatorConstruct.CaCertName)
				assert.Equal(t, "managed-om-managed-om-ca-bundle", volumes[operatorConstruct.CaCertName])
				assert.Equal(t, "workload-managed-server-ca-bundle", volumes["secret-ca"])
				agent := sts.Spec.Template.Spec.Containers[0]
				assert.Contains(t, agent.Env, corev1.EnvVar{Name: "SSL_TRUSTED_MMS_SERVER_CERTIFICATE", Value: "/mongodb-automation/certs/mms-ca.crt"})
				assert.Contains(t, agent.Env, corev1.EnvVar{Name: "SSL_REQUIRE_VALID_MMS_CERTIFICATES", Value: "true"})
				deployment := om.NewDeployment()
				deployment.ConfigureTLS(rs.GetSecurity(), rs.GetSecurity().GetTLSCAFilePath(util.TLSCaMountPath+"/ca-pem"))
				assert.Equal(t, util.TLSCaMountPath+"/ca-pem", deployment["tls"].(map[string]any)["CAFilePath"])
				processes := replicaset.BuildFromMongoDBWithReplicas("mongodb", false, rs, 1, rs.CalculateFeatureCompatibilityVersion(), util.TLSCertMountPath+"/leaf", architectures.NonStatic, nil)
				assert.Equal(t, "requireTLS", processes.Processes[0].TLSConfig()["mode"])
				foreignProject := projectCM.DeepCopy()
				foreignProject.Namespace, foreignProject.ResourceVersion = "another-namespace", ""
				require.NoError(t, c.Create(ctx, foreignProject))
				_, err = project.ReadProjectConfig(ctx, c, client.ObjectKeyFromObject(foreignProject), rs.Name)
				require.ErrorContains(t, err, "another-namespace/managed-om-managed-om-ca-bundle")
			})
		}
	}
}

type omCertificateWatchManager struct {
	manager.Manager
	cache  cache.Cache
	client client.Client
	mapper meta.RESTMapper
}

func (m omCertificateWatchManager) GetCache() cache.Cache          { return m.cache }
func (m omCertificateWatchManager) GetScheme() *runtime.Scheme     { return m.client.Scheme() }
func (m omCertificateWatchManager) GetRESTMapper() meta.RESTMapper { return m.mapper }

type omCertificateWatchController struct {
	controller.Controller
	sources []source.Source
}

func (c *omCertificateWatchController) Watch(src source.Source) error {
	c.sources = append(c.sources, src)
	return nil
}

func TestOpsManagerManagedCertificateEvents(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprintf("certManagerReady=%t", ready), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opsManager := newManagedCertificateTestOM()
			c := newManagedCertificateKubeClient(t, opsManager)
			r := newManagedCertificateTestReconciler(ctx, c)
			r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: ready}
			r.configureWatchersForDynamicResources(ctx, opsManager, zap.S())
			cache := &informertest.FakeInformers{Scheme: c.Scheme()}
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{v1.SchemeGroupVersion})
			mapper.Add(v1.SchemeGroupVersion.WithKind("MongoDBOpsManager"), meta.RESTScopeNamespace)
			mgr := omCertificateWatchManager{cache: cache, client: c, mapper: mapper}
			watched := &omCertificateWatchController{}
			require.NoError(t, r.watchCertificateResources(watched, mgr))
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			for _, src := range watched.sources {
				require.NoError(t, src.Start(ctx, q))
				require.NoError(t, src.(source.SyncingSource).WaitForSync(ctx))
			}
			checkEvent := func(obj client.Object, want bool) {
				t.Helper()
				informer, err := cache.FakeInformerFor(ctx, obj)
				require.NoError(t, err)
				informer.Add(obj)
				if !want {
					assert.Zero(t, q.Len())
					return
				}
				require.Equal(t, 1, q.Len(), "%T event must enqueue its Ops Manager", obj)
				request, _ := q.Get()
				assert.Equal(t, opsManager.ObjectKey(), request.NamespacedName)
				q.Done(request)
			}
			checkEvent(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: opsManager.TLSCertificateSecretName(), Namespace: opsManager.Namespace}}, true)
			checkEvent(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: opsManager.GetOpsManagerCA(), Namespace: opsManager.Namespace}}, true)
			checkEvent(&certmanagerv1.Certificate{ObjectMeta: metav1.ObjectMeta{Name: opsManager.TLSCertificateSecretName(), Namespace: opsManager.Namespace, OwnerReferences: kube.BaseOwnerReference(opsManager)}}, ready)
		})
	}
}

func TestOpsManagerManagedCertificateFullReconcile(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprintf("certManagerReady=%t", ready), func(t *testing.T) {
			opsManager := newManagedCertificateTestOM()
			opsManager.Spec.AppDB.Security.ManagedCertificate = nil
			opsManager.Spec.Backup.Enabled = false
			opsManager.Spec.Backup.ManagedCertificate = nil
			c := newManagedCertificateKubeClient(t, opsManager)
			r := newManagedCertificateTestReconciler(t.Context(), c)
			r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: ready}
			result, err := r.Reconcile(t.Context(), requestFromObject(opsManager))
			require.NoError(t, err)
			assert.NotZero(t, result.RequeueAfter)
			condition := managedCertificateCondition(t, t.Context(), c, opsManager)
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			if ready {
				assert.Equal(t, "Issuing", condition.Reason)
				assert.Contains(t, managedCertificateNames(listManagedCertificateObjects(t, t.Context(), c)), opsManager.TLSCertificateSecretName())
			} else {
				assert.Equal(t, "IssuanceFailed", condition.Reason)
				assert.Empty(t, listManagedCertificateObjects(t, t.Context(), c))
			}
			statefulSets := &appsv1.StatefulSetList{}
			require.NoError(t, c.List(t.Context(), statefulSets))
			assert.Empty(t, statefulSets.Items, "AppDB and OM consumers must wait for the OM CA")
		})
	}
}

func TestOpsManagerManagedCertificateLegacyStartup(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	c := newManagedCertificateKubeClient(t, opsManager)
	r := newManagedCertificateTestReconciler(t.Context(), c)
	_, blocked, err := r.prepareManagedCertificates(t.Context(), opsManager, zap.S())
	require.NoError(t, err)
	assert.False(t, blocked)
	assert.Empty(t, listManagedCertificateObjects(t, t.Context(), c))
	assert.Nil(t, managedCertificateCondition(t, t.Context(), c, opsManager))
}

func TestOpsManagerManagedCrossTrust(t *testing.T) {
	ctx := t.Context()
	opsManager := newManagedCertificateTestOM()
	opsManager.Spec.AppDB.Security.ManagedCertificate = nil
	opsManager.Spec.AppDB.Security.TLSConfig = &mdbv1.TLSConfig{Enabled: true, CA: "appdb-ca"}
	opsManager.Spec.Backup.ManagedCertificate = nil
	omCA, issue := managedOMTestCA(t)
	appDBCA, _ := managedOMTestCA(t)
	c := newManagedCertificateKubeClient(t, opsManager, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "appdb-ca", Namespace: opsManager.Namespace}, Data: map[string]string{"ca-pem": string(appDBCA)}})
	r := newManagedCertificateTestReconciler(ctx, c)
	r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}
	r.prepareOpsManagerCertificate(ctx, opsManager, zap.S())
	markAllManagedCertificatesReady(t, ctx, c)
	createSecretsForManagedCertificates(t, ctx, c, omCA)
	leaf := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.TLSCertificateSecretName()), leaf))
	leaf.Type = corev1.SecretTypeTLS
	leaf.Data["tls.crt"], leaf.Data["tls.key"] = issue()
	require.NoError(t, c.Update(ctx, leaf))
	require.True(t, r.prepareOpsManagerCertificate(ctx, opsManager, zap.S()).status.IsOK())
	projectConfig, err := opsManager.GetAppDBProjectConfig(ctx, c)
	require.NoError(t, err)
	assert.Equal(t, "managed-om-managed-om-ca-bundle", projectConfig.SSLMMSCAConfigMap)
	assert.Equal(t, string(omCA), projectConfig.SSLMMSCAConfigMapContents)
	secretClient := secrets.SecretClient{KubeClient: c}
	member := multicluster.GetLegacyCentralMemberCluster(1, 0, c, secretClient)
	helper, err := NewOpsManagerReconcilerHelper(ctx, r, opsManager, nil, zap.S())
	require.NoError(t, err)
	appDBConfig := &AppDBConfig{IsTLSEnabled: true, CAConfigMapName: "appdb-ca"}
	r.ensureConfiguration(helper, appDBConfig, zap.S())
	assert.Equal(t, omv1.GetAppDBCaPemPath(), opsManager.Spec.Configuration[util.MmsMongoCA])
	sts, err := operatorConstruct.OpsManagerStatefulSet(ctx, secretClient, opsManager, member, zap.S(), operatorConstruct.WithAppDBTLSCAConfigMapName(appDBConfig.CAConfigMapName))
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(sts.Spec.Template.Spec.Volumes, func(v corev1.Volume) bool {
		return v.Name == "appdb-ca-certificate" && v.ConfigMap != nil && v.ConfigMap.Name == "appdb-ca"
	}))
	bundle := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.GetOpsManagerCA()), bundle))
	assert.Equal(t, string(omCA), bundle.Data["mms-ca.crt"])
	assert.NotContains(t, bundle.Data["mms-ca.crt"], string(appDBCA))
}
