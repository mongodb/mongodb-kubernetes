package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

func newOpsManagerReconcilerForValidation(objects ...client.Object) *OpsManagerReconciler {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(objects...)
	return NewOpsManagerReconciler(context.Background(), kubeClient, map[string]client.Client{}, images.ImageUrls{}, "", "", architectures.Static, omConnectionFactory.GetConnectionFunc, nil, nil)
}

func TestValidateExternalAppDBReference(t *testing.T) {
	ctx := context.Background()

	validMongoDB := mdbv1.NewReplicaSetBuilder().
		SetName("test-om-db").
		SetNamespace(mock.TestNamespace).
		SetVersion("6.0.0").
		Build()
	validMongoDB.Spec.Role = mdbv1.RoleAppDB

	tests := []struct {
		name          string
		om            *omv1.MongoDBOpsManager
		objects       []client.Object
		expectedError string
	}{
		{
			name: "no externalApplicationDatabaseRef is a no-op",
			om:   DefaultOpsManagerBuilder().Build(),
		},
		{
			name: "name does not match naming convention",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "wrong-name",
				Kind: "MongoDB",
			}),
			expectedError: `externalApplicationDatabaseRef.name "wrong-name" does not match required naming convention "test-om-db"`,
		},
		{
			name: "referenced MongoDB does not exist",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			expectedError: "externalApplicationDatabaseRef points to MongoDB my-namespace/test-om-db which does not exist",
		},
		{
			name: "referenced MongoDB does not have role AppDB",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			objects: []client.Object{
				mdbv1.NewReplicaSetBuilder().SetName("test-om-db").SetNamespace(mock.TestNamespace).SetVersion("6.0.0").Build(),
			},
			expectedError: `externalApplicationDatabaseRef my-namespace/test-om-db must have spec.role set to "AppDB"`,
		},
		{
			name: "referenced MongoDB has version < 4.0.0",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			objects: []client.Object{
				func() *mdbv1.MongoDB {
					mdb := mdbv1.NewReplicaSetBuilder().SetName("test-om-db").SetNamespace(mock.TestNamespace).SetVersion("3.6.0").Build()
					mdb.Spec.Role = mdbv1.RoleAppDB
					return mdb
				}(),
			},
			expectedError: `externalApplicationDatabaseRef my-namespace/test-om-db must have a MongoDB version >= 4.0.0, got "3.6.0"`,
		},
		{
			name: "referenced MongoDB is valid",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			objects: []client.Object{validMongoDB},
		},
		{
			name: "referenced MongoDBMulti is valid",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "MongoDBMultiCluster",
			}),
			objects: []client.Object{
				func() *mdbmulti.MongoDBMultiCluster {
					mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetName("test-om-db").Build()
					mrs.Spec.Role = mdbv1.RoleAppDB
					return mrs
				}(),
			},
		},
		{
			name: "unsupported kind",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "SomethingElse",
			}),
			expectedError: `externalApplicationDatabaseRef.kind "SomethingElse" is not supported`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := newOpsManagerReconcilerForValidation(tt.objects...)
			err := reconciler.createNewExternalAppDBReconciler(zap.S()).validateExternalAppDBReference(ctx, tt.om)
			if tt.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.expectedError)
			}
		})
	}
}

func validExternalAppDBRef() *omv1.ExternalApplicationDatabaseRef {
	return &omv1.ExternalApplicationDatabaseRef{
		Name: "test-om-db",
		Kind: "MongoDB",
	}
}

func validExternalAppDBMongoDB() *mdbv1.MongoDB {
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName("test-om-db").
		SetNamespace(mock.TestNamespace).
		SetVersion("6.0.0").
		Build()
	mdb.Spec.Role = mdbv1.RoleAppDB
	return mdb
}

func TestDetachInternalAppDB_StripsOwnerReferencesAndAnnotates(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	mdb := validExternalAppDBMongoDB()

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-om-db",
			Namespace:       mock.TestNamespace,
			OwnerReferences: kube.BaseOwnerReference(testOm),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
		},
	}
	passwordSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testOm.Spec.AppDB.GetOpsManagerUserPasswordSecretName(),
			Namespace:       mock.TestNamespace,
			OwnerReferences: kube.BaseOwnerReference(testOm),
		},
	}

	stateConfigMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-om-db-state",
			Namespace:       mock.TestNamespace,
			OwnerReferences: kube.BaseOwnerReference(testOm),
		},
		Data: map[string]string{
			"state": `{"clusterMapping":{},"lastAppliedMemberSpec":{},"lastAppliedMongoDBVersion":""}`,
		},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))
	require.NoError(t, reconciler.client.Create(ctx, &passwordSecret))
	require.NoError(t, reconciler.client.Create(ctx, &stateConfigMap))

	err := reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S())
	assert.NoError(t, err)

	resultSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
	assert.Empty(t, resultSts.OwnerReferences)
	assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])

	resultSecret := corev1.Secret{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, passwordSecret.Name), &resultSecret))
	assert.Empty(t, resultSecret.OwnerReferences)

	resultStateConfigMap := corev1.ConfigMap{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, stateConfigMap.Name), &resultStateConfigMap))
	assert.Empty(t, resultStateConfigMap.OwnerReferences)
}

func TestDetachInternalAppDB_NoOpWhenNoStatefulSetExists(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	mdb := validExternalAppDBMongoDB()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))

	err := reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S())
	assert.NoError(t, err, "Fresh Start: no internal AppDB StatefulSet ever existed, detach must be a no-op")
}

func TestDetachInternalAppDB_NoOpWhenNoExternalRef(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetName("test-om").Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	err := reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S())
	assert.NoError(t, err)
}

func TestDetachInternalAppDB_IsIdempotent(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	mdb := validExternalAppDBMongoDB()

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-om-db",
			Namespace:       mock.TestNamespace,
			OwnerReferences: kube.BaseOwnerReference(testOm),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
		},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S()))
	require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S()))

	resultSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
	assert.Empty(t, resultSts.OwnerReferences)
	assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
}

// TestDetachInternalAppDB_OnlyStripsHealthyAppDBMemberClusters proves the strip step iterates the
// AppDB's own healthy member clusters (ReconcileAppDbReplicaSet.GetHealthyMemberClusters), not the
// operator-wide set of every registered member cluster. memberClusterUnrelatedToAppDB is
// registered operator-wide (e.g. used by some other multi-cluster resource) but isn't part of
// this AppDB's ClusterSpecList, so it's given a nil client: if the strip loop touched it, calling
// Get on a nil client.Client would panic.
func TestDetachInternalAppDB_OnlyStripsHealthyAppDBMemberClusters(t *testing.T) {
	ctx := context.Background()

	memberClusterName := "kind-e2e-cluster-1"
	memberClusterName2 := "kind-e2e-cluster-2"
	memberClusterUnrelatedToAppDB := "kind-e2e-cluster-unrelated"

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	globalMemberClustersMap := getFakeMultiClusterMapWithClusters([]string{memberClusterName, memberClusterName2}, omConnectionFactory)
	globalMemberClustersMap[memberClusterUnrelatedToAppDB] = nil

	appDBClusterSpecItems := mdbv1.ClusterSpecList{
		{ClusterName: memberClusterName, Members: 1},
		{ClusterName: memberClusterName2, Members: 1},
	}

	testOm := withExternalAppDBRef(
		DefaultOpsManagerBuilder().
			SetName("test-om").
			SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
			SetAppDbMembers(0).
			SetAppDBClusterSpecList(appDBClusterSpecItems).
			Build(),
		validExternalAppDBRef(),
	)
	mdb := validExternalAppDBMongoDB()

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-om-db",
			Namespace:       mock.TestNamespace,
			OwnerReferences: kube.BaseOwnerReference(testOm),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
		},
	}

	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, globalMemberClustersMap, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	require.NotPanics(t, func() {
		err := reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S())
		assert.NoError(t, err)
	})
}

func TestComputeExternalAppDBConnectionString_WritesFixedSecret(t *testing.T) {
	ctx := context.Background()

	externalAppDB := mdbv1.NewReplicaSetBuilder().
		SetName("test-om-db").
		SetNamespace(mock.TestNamespace).
		SetVersion("6.0.0").
		SetMembers(3).
		EnableAuth([]mdbv1.AuthMode{util.SCRAM}).
		Build()
	externalAppDB.Spec.Role = mdbv1.RoleAppDB

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
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

	helper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, reconciler.memberClustersMap, zap.S())
	require.NoError(t, err)

	connString, err := reconciler.createNewExternalAppDBReconciler(zap.S()).computeExternalAppDBConnectionString(ctx, testOm)
	require.NoError(t, err)
	assert.Contains(t, connString, util.OpsManagerMongoDBUserName)
	assert.Contains(t, connString, "test-password")

	for _, memberCluster := range helper.getHealthyMemberClusters() {
		require.NoError(t, reconciler.ensureAppDBConnectionStringInMemberCluster(ctx, testOm, connString, memberCluster, zap.S()))
	}

	result := corev1.Secret{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()), &result))
	assert.Contains(t, string(result.Data[util.AppDbConnectionStringKey]), util.OpsManagerMongoDBUserName)
}

func TestDetachInternalAppDB_OnlyDetachesOMOwnedStatefulSet(t *testing.T) {
	// real UIDs needed: the ownership check compares OwnerReference UIDs, and empty test UIDs
	// ("" == "") would make every StatefulSet look OM-owned
	const omUID = "om-uid-1111"
	const crUID = "cr-uid-2222"

	tests := []struct {
		name             string
		stsOwnerRefs     func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference
		expectedDetached bool
	}{
		{
			name: "OM-owned StatefulSet is stripped and annotated",
			stsOwnerRefs: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			expectedDetached: true,
		},
		{
			name: "CR-owned StatefulSet (fresh start) is untouched",
			stsOwnerRefs: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return []metav1.OwnerReference{{
					APIVersion: "mongodb.com/v1",
					Kind:       "MongoDB",
					Name:       "test-om-db",
					UID:        crUID,
				}}
			},
			expectedDetached: false,
		},
		{
			name: "ownerRef-free StatefulSet (already detached and consumed) is not re-annotated",
			stsOwnerRefs: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return nil
			},
			expectedDetached: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
			testOm.UID = types.UID(omUID)
			mdb := validExternalAppDBMongoDB()

			originalOwnerRefs := tt.stsOwnerRefs(testOm)
			sts := appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-om-db",
					Namespace:       mock.TestNamespace,
					OwnerReferences: originalOwnerRefs,
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			}
			passwordSecret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            testOm.Spec.AppDB.GetOpsManagerUserPasswordSecretName(),
					Namespace:       mock.TestNamespace,
					OwnerReferences: originalOwnerRefs,
				},
			}
			keyfileSecret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name,
					Namespace:       mock.TestNamespace,
					OwnerReferences: originalOwnerRefs,
				},
			}

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
			require.NoError(t, reconciler.client.Create(ctx, mdb))
			require.NoError(t, reconciler.client.Create(ctx, &sts))
			require.NoError(t, reconciler.client.Create(ctx, &passwordSecret))
			require.NoError(t, reconciler.client.Create(ctx, &keyfileSecret))

			require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S()))

			resultSts := appsv1.StatefulSet{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
			resultSecret := corev1.Secret{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, passwordSecret.Name), &resultSecret))
			resultKeyfileSecret := corev1.Secret{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, keyfileSecret.Name), &resultKeyfileSecret))

			if tt.expectedDetached {
				assert.Empty(t, resultSts.OwnerReferences)
				assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
				assert.Empty(t, resultSecret.OwnerReferences)
				assert.Empty(t, resultKeyfileSecret.OwnerReferences)
			} else {
				assert.Equal(t, originalOwnerRefs, resultSts.OwnerReferences)
				assert.NotContains(t, resultSts.Annotations, util.AppDBMigrationReadyAnnotation)
				assert.Equal(t, originalOwnerRefs, resultSecret.OwnerReferences)
				assert.Equal(t, originalOwnerRefs, resultKeyfileSecret.OwnerReferences)
			}
		})
	}
}

func TestDetachInternalAppDB_ClearsStaleReverseMigrationRequest(t *testing.T) {
	// abort path: user re-added the ref while a reverse migration was in flight; the ownerless
	// StatefulSet must be handed to the CR (annotation swap), exactly like a forward detach state
	ctx := context.Background()
	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	testOm.UID = types.UID("om-uid-1111")
	mdb := validExternalAppDBMongoDB()

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-om-db",
			Namespace:   mock.TestNamespace,
			Annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"},
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S()))

	result := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &result))
	assert.NotContains(t, result.Annotations, util.AppDBReverseMigrationReadyAnnotation)
	assert.Equal(t, "true", result.Annotations[util.AppDBMigrationReadyAnnotation],
		"ownerless StatefulSet must be handed to the CR via the forward-adoption annotation")
}
