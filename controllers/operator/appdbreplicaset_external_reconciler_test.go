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
			name:          "no externalApplicationDatabaseRef is an error",
			om:            DefaultOpsManagerBuilder().Build(),
			expectedError: "externalApplicationDatabaseRef is nil, must be set to a valid MongoDB reference",
		},
		{
			name: "referenced MongoDB does not exist",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			expectedError: "failed to fetch externalApplicationDatabaseRef my-namespace/test-om-db: externalApplicationDatabaseRef points to MongoDB my-namespace/test-om-db which does not exist",
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
			name: "referenced MongoDB is valid",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			objects: []client.Object{validMongoDB},
		},
		{
			name: "unsupported kind",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalApplicationDatabaseRef{
				Name: "test-om-db",
				Kind: "SomethingElse",
			}),
			expectedError: `failed to fetch externalApplicationDatabaseRef my-namespace/test-om-db: externalApplicationDatabaseRef.kind "SomethingElse" is not supported`,
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
		Name:      "test-om-db",
		Kind:      "MongoDB",
		Namespace: mock.TestNamespace,
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

func TestEnsureAppDBStatefulSetOwnership_StripsOwnerReferencesAndAnnotates(t *testing.T) {
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

	err := reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm)
	assert.NoError(t, err)

	resultSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
	assert.Empty(t, resultSts.OwnerReferences)
	assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
}

func TestEnsureAppDBStatefulSetOwnership_NoOpWhenNoStatefulSetExists(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	mdb := validExternalAppDBMongoDB()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))

	err := reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm)
	assert.NoError(t, err, "Fresh Start: no internal AppDB StatefulSet ever existed, detach must be a no-op")
}

func TestEnsureAppDBStatefulSetOwnership_IsIdempotent(t *testing.T) {
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

	require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm))
	require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm))

	resultSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
	assert.Empty(t, resultSts.OwnerReferences)
	assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
}

func TestGetAppDBConfig_ExternalAppDB(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                    string
		enableTLS               bool
		caConfigMapName         string
		expectedIsTLSEnabled    bool
		expectedCAConfigMapName string
	}{
		{
			name:                    "TLS disabled returns empty CA and disabled flag",
			expectedIsTLSEnabled:    false,
			expectedCAConfigMapName: "",
		},
		{
			name:                    "TLS enabled with CA returns resolved TLS config",
			enableTLS:               true,
			caConfigMapName:         "app-db-issuer-ca",
			expectedIsTLSEnabled:    true,
			expectedCAConfigMapName: "app-db-issuer-ca",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := mdbv1.NewReplicaSetBuilder().
				SetName("test-om-db").
				SetNamespace(mock.TestNamespace).
				SetVersion("6.0.0").
				SetMembers(3).
				EnableAuth([]mdbv1.AuthMode{util.SCRAM})
			if tt.enableTLS {
				builder = builder.SetSecurityTLSEnabled()
			}
			externalAppDB := builder.Build()
			externalAppDB.Spec.Role = mdbv1.RoleAppDB
			if tt.enableTLS {
				externalAppDB.Spec.Security.TLSConfig.CA = tt.caConfigMapName
			}

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

			cfg, err := reconciler.createNewExternalAppDBReconciler(zap.S()).GetAppDBConfig(ctx, testOm, zap.S())
			require.NoError(t, err)
			assert.Contains(t, cfg.ConnectionString, util.OpsManagerMongoDBUserName)
			assert.Contains(t, cfg.ConnectionString, "test-password")
			assert.Equal(t, tt.expectedIsTLSEnabled, cfg.IsTLSEnabled)
			assert.Equal(t, tt.expectedCAConfigMapName, cfg.CAConfigMapName)

			for _, memberCluster := range helper.getHealthyMemberClusters() {
				require.NoError(t, reconciler.ensureAppDBConnectionStringInMemberCluster(ctx, testOm, cfg.ConnectionString, memberCluster, zap.S()))
			}

			result := corev1.Secret{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()), &result))
			assert.Contains(t, string(result.Data[util.AppDbConnectionStringKey]), util.OpsManagerMongoDBUserName)
		})
	}
}

func TestEnsureAppDBStatefulSetOwnership_OnlyDetachesOMOwnedStatefulSet(t *testing.T) {
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

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
			require.NoError(t, reconciler.client.Create(ctx, mdb))
			require.NoError(t, reconciler.client.Create(ctx, &sts))

			require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm))

			resultSts := appsv1.StatefulSet{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))

			if tt.expectedDetached {
				assert.Empty(t, resultSts.OwnerReferences)
				assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
			} else {
				assert.Equal(t, originalOwnerRefs, resultSts.OwnerReferences)
				assert.NotContains(t, resultSts.Annotations, util.AppDBMigrationReadyAnnotation)
			}
		})
	}
}
