package operator

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

type appDBStatefulSetState struct {
	ownerReferences []metav1.OwnerReference
	annotations     map[string]string
}

func newAppDBStatefulSetStatefulSet(name string, state appDBStatefulSetState) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       mock.TestNamespace,
			OwnerReferences: state.ownerReferences,
			Annotations:     state.annotations,
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))},
	}
}

func getAppDBStatefulSetState(t *testing.T, ctx context.Context, c client.Client, namespace, name string) appDBStatefulSetState {
	t.Helper()

	sts := appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(namespace, name), &sts))
	return appDBStatefulSetState{ownerReferences: sts.OwnerReferences, annotations: sts.Annotations}
}

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
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			expectedError: "failed to fetch externalApplicationDatabaseRef my-namespace/test-om-db: externalApplicationDatabaseRef points to MongoDB my-namespace/test-om-db which does not exist",
		},
		{
			name: "referenced MongoDB does not have role AppDB",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
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
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
				Name: "test-om-db",
				Kind: "MongoDB",
			}),
			objects: []client.Object{validMongoDB},
		},
		{
			name: "referenced MongoDBMultiCluster is valid",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
				Name: "test-om-db",
				Kind: omv1.ExternalAppDBRefKindMongoDBMultiCluster,
			}),
			objects: []client.Object{validExternalAppDBMongoDBMultiCluster()},
		},
		{
			name: "referenced MongoDBMultiCluster does not have role AppDB",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
				Name: "test-om-db",
				Kind: omv1.ExternalAppDBRefKindMongoDBMultiCluster,
			}),
			objects: []client.Object{
				func() *mdbmulti.MongoDBMultiCluster {
					mdbm := mdbmulti.DefaultMultiReplicaSetBuilder().SetName("test-om-db").Build()
					mdbm.Namespace = mock.TestNamespace
					return mdbm
				}(),
			},
			expectedError: `externalApplicationDatabaseRef my-namespace/test-om-db must have spec.role set to "AppDB"`,
		},
		{
			name: "referenced MongoDBMultiCluster does not exist",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
				Name: "test-om-db",
				Kind: omv1.ExternalAppDBRefKindMongoDBMultiCluster,
			}),
			expectedError: "failed to fetch externalApplicationDatabaseRef my-namespace/test-om-db: externalApplicationDatabaseRef points to MongoDBMultiCluster my-namespace/test-om-db which does not exist",
		},
		{
			name: "unsupported kind",
			om: withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
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

func validExternalAppDBRef() *omv1.ExternalAppDBRef {
	return &omv1.ExternalAppDBRef{
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

func validExternalAppDBMongoDBWithTLS(tlsEnabled bool, caConfigMapName string) *mdbv1.MongoDB {
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName("test-om-db").
		SetNamespace(mock.TestNamespace).
		SetVersion("6.0.0").
		SetMembers(3).
		SetSecurity(&mdbv1.Security{
			TLSConfig:      &mdbv1.TLSConfig{Enabled: tlsEnabled, CA: caConfigMapName},
			Authentication: &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM}},
		}).
		Build()
	mdb.Spec.Role = mdbv1.RoleAppDB
	return mdb
}

func validExternalAppDBMongoDBMultiCluster() *mdbmulti.MongoDBMultiCluster {
	return validExternalAppDBMongoDBMultiClusterWithTLS(false, "")
}

func validExternalAppDBMongoDBMultiClusterWithTLS(tlsEnabled bool, caConfigMapName string) *mdbmulti.MongoDBMultiCluster {
	mdbm := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetName("test-om-db").
		SetRole(mdbv1.RoleAppDB).
		SetClusterSpecList([]string{"cluster-1", "cluster-2", "cluster-3"}).
		SetSecurity(&mdbv1.Security{
			TLSConfig:      &mdbv1.TLSConfig{Enabled: tlsEnabled, CA: caConfigMapName},
			Authentication: &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM}},
		}).
		Build()
	mdbm.Spec.Mapping = map[string]int{"cluster-1": 0, "cluster-2": 1, "cluster-3": 2}
	return mdbm
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

func TestEnsureAppDBStatefulSetOwnership_MultiCluster(t *testing.T) {
	ctx := context.Background()
	const (
		omUID   = "om-uid-1111"
		mdbmUID = "mdbm-uid-2222"
	)

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), &omv1.ExternalAppDBRef{
		Name:      "test-om-db",
		Kind:      omv1.ExternalAppDBRefKindMongoDBMultiCluster,
		Namespace: mock.TestNamespace,
	})
	testOm.UID = types.UID(omUID)

	foreignOwner := []metav1.OwnerReference{{
		APIVersion: "mongodb.com/v1",
		Kind:       "MongoDBMultiCluster",
		Name:       "test-om-db",
		UID:        types.UID(mdbmUID),
	}}

	rows := []struct {
		name                  string
		createStates          map[string]appDBStatefulSetState
		missingClients        []string
		expectedStates        map[string]appDBStatefulSetState
		expectedErrorContains string
		runTwice              bool
	}{
		{
			name:           "fresh start skips missing StatefulSets in every cluster",
			createStates:   map[string]appDBStatefulSetState{},
			expectedStates: map[string]appDBStatefulSetState{},
		},
		{
			name: "OM-owned StatefulSets detach per cluster and leave others untouched",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}},
				"cluster-2": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}},
				"cluster-3": {ownerReferences: foreignOwner},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
				"cluster-2": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
				"cluster-3": {ownerReferences: foreignOwner},
			},
		},
		{
			name: "foreign-owned StatefulSets stay untouched",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: foreignOwner},
				"cluster-2": {ownerReferences: foreignOwner},
				"cluster-3": {ownerReferences: foreignOwner},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: foreignOwner},
				"cluster-2": {ownerReferences: foreignOwner},
				"cluster-3": {ownerReferences: foreignOwner},
			},
		},
		{
			name: "missing client reports the cluster and still detaches the others",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}},
				"cluster-2": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}},
				"cluster-3": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}},
			},
			missingClients: []string{"cluster-2"},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
				"cluster-3": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
			},
			expectedErrorContains: "cluster-2",
		},
		{
			name: "already detached StatefulSets are idempotent on a second run",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
				"cluster-2": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
				"cluster-3": {ownerReferences: foreignOwner},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
				"cluster-2": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}},
				"cluster-3": {ownerReferences: foreignOwner},
			},
			runTwice: true,
		},
	}

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			ref := &mdbmulti.MongoDBMultiCluster{}
			ref.Namespace = mock.TestNamespace
			ref.Name = "test-om-db"
			ref.UID = types.UID(mdbmUID)
			ref.Spec = mdbmulti.DefaultMultiReplicaSetBuilder().SetName("test-om-db").SetRole(mdbv1.RoleAppDB).Build().Spec
			ref.Spec.ClusterSpecList = []mdbv1.ClusterSpecItem{{ClusterName: "cluster-1"}, {ClusterName: "cluster-2"}, {ClusterName: "cluster-3"}}
			ref.Spec.Mapping = map[string]int{"cluster-1": 0, "cluster-2": 1, "cluster-3": 2}

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			memberClustersMap := getAppDBFakeMultiClusterMapWithClusters([]string{"cluster-1", "cluster-2", "cluster-3"}, omConnectionFactory)
			for clusterName, state := range tt.createStates {
				require.NoError(t, memberClustersMap[clusterName].Create(ctx, newAppDBStatefulSetStatefulSet(ref.StatefulSetNameForCluster(clusterName), state)))
			}

			for _, clusterName := range tt.missingClients {
				delete(memberClustersMap, clusterName)
			}

			reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClustersMap, omConnectionFactory, architectures.NonStatic)
			require.NoError(t, reconciler.client.Create(ctx, ref))

			err := reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm)
			if tt.expectedErrorContains != "" {
				require.ErrorContains(t, err, tt.expectedErrorContains)
			} else {
				require.NoError(t, err)
			}

			if tt.runTwice {
				require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm))
			}

			for _, clusterName := range []string{"cluster-1", "cluster-2", "cluster-3"} {
				if _, ok := tt.expectedStates[clusterName]; !ok {
					if _, present := tt.createStates[clusterName]; !present {
						err := memberClustersMap[clusterName].Get(ctx, kube.ObjectKey(mock.TestNamespace, ref.StatefulSetNameForCluster(clusterName)), &appsv1.StatefulSet{})
						require.Error(t, err)
						require.True(t, apiErrors.IsNotFound(err))
					}
					continue
				}

				expected := tt.expectedStates[clusterName]
				result := getAppDBStatefulSetState(t, ctx, memberClustersMap[clusterName], mock.TestNamespace, ref.StatefulSetNameForCluster(clusterName))
				assert.Equal(t, expected.ownerReferences, result.ownerReferences, "cluster %s owner refs", clusterName)
				assert.Equal(t, expected.annotations, result.annotations, "cluster %s annotations", clusterName)
			}
		})
	}
}

func TestEnsureAppDBStatefulSetOwnership_MongoDBRegression(t *testing.T) {
	ctx := context.Background()
	const omUID = "om-uid-3333"

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	testOm.UID = types.UID(omUID)

	rows := []struct {
		name          string
		state         appDBStatefulSetState
		expectedState appDBStatefulSetState
	}{
		{
			name: "OM-owned central StatefulSet detaches and annotates",
			state: appDBStatefulSetState{
				ownerReferences: kube.BaseOwnerReference(testOm),
				annotations:     map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString},
			},
			expectedState: appDBStatefulSetState{
				annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString},
			},
		},
		{
			name: "foreign-owned central StatefulSet stays untouched",
			state: appDBStatefulSetState{
				ownerReferences: []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID("mongo-uid-1")}},
			},
			expectedState: appDBStatefulSetState{
				ownerReferences: []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID("mongo-uid-1")}},
			},
		},
	}

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			memberClustersMap := map[string]client.Client{}
			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClustersMap, omConnectionFactory, architectures.NonStatic)
			require.NoError(t, reconciler.client.Create(ctx, validExternalAppDBMongoDB()))
			require.NoError(t, kubeClient.Create(ctx, newAppDBStatefulSetStatefulSet("test-om-db", tt.state)))

			require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm))

			result := getAppDBStatefulSetState(t, ctx, kubeClient, mock.TestNamespace, "test-om-db")
			assert.Equal(t, tt.expectedState.ownerReferences, result.ownerReferences)
			assert.Equal(t, tt.expectedState.annotations, result.annotations)
		})
	}
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
	const password = "test-password"

	variants := []struct {
		kind  string
		build func(tlsEnabled bool, caConfigMapName string) client.Object
	}{
		{
			kind: omv1.ExternalAppDBRefKindMongoDB,
			build: func(tlsEnabled bool, caConfigMapName string) client.Object {
				return validExternalAppDBMongoDBWithTLS(tlsEnabled, caConfigMapName)
			},
		},
		{
			kind: omv1.ExternalAppDBRefKindMongoDBMultiCluster,
			build: func(tlsEnabled bool, caConfigMapName string) client.Object {
				return validExternalAppDBMongoDBMultiClusterWithTLS(tlsEnabled, caConfigMapName)
			},
		},
	}

	tests := []struct {
		name                    string
		tlsEnabled              bool
		caConfigMapName         string
		createPasswordSecret    bool
		expectedIsTLSEnabled    bool
		expectedCAConfigMapName string
		expectedErrorContains   string
	}{
		{
			name:                 "TLS disabled returns empty CA and disabled flag",
			createPasswordSecret: true,
		},
		{
			name:                    "TLS enabled with CA returns resolved TLS config",
			tlsEnabled:              true,
			caConfigMapName:         "app-db-issuer-ca",
			createPasswordSecret:    true,
			expectedIsTLSEnabled:    true,
			expectedCAConfigMapName: "app-db-issuer-ca",
		},
		{
			name:                  "without password secret returns an error",
			createPasswordSecret:  false,
			expectedErrorContains: "failed to read shared password secret",
		},
	}

	for _, v := range variants {
		for _, tt := range tests {
			t.Run(v.kind+"/"+tt.name, func(t *testing.T) {
				externalAppDB := v.build(tt.tlsEnabled, tt.caConfigMapName)
				connectionStringBuilder, ok := externalAppDB.(connectionstring.ConnectionStringBuilder)
				require.True(t, ok, "referenced external app DB must build connection strings")

				testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
					Name: "test-om-db",
					Kind: v.kind,
				})

				omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
				reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
				require.NoError(t, reconciler.client.Create(ctx, externalAppDB))
				if tt.createPasswordSecret {
					require.NoError(t, reconciler.client.CreateSecret(ctx, secret.Builder().
						SetName(omv1.OpsManagerUserPasswordSecretName("test-om-db")).
						SetNamespace(testOm.Namespace).
						SetField(util.OpsManagerPasswordKey, password).
						Build()))
				}

				cfg, err := reconciler.createNewExternalAppDBReconciler(zap.S()).GetAppDBConfig(ctx, testOm, zap.S())
				if tt.expectedErrorContains != "" {
					require.ErrorContains(t, err, tt.expectedErrorContains)
					return
				}

				require.NoError(t, err)
				expectedConnectionString := connectionStringBuilder.BuildConnectionString(util.OpsManagerMongoDBUserName, password, "", connectionstring.SchemeMongoDB, map[string]string{"authMechanism": "SCRAM-SHA-256"})
				assert.Equal(t, expectedConnectionString, cfg.ConnectionString)
				assert.Equal(t, tt.expectedIsTLSEnabled, cfg.IsTLSEnabled)
				assert.Equal(t, tt.expectedCAConfigMapName, cfg.CAConfigMapName)

				helper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, reconciler.memberClustersMap, zap.S())
				require.NoError(t, err)

				for _, memberCluster := range helper.getHealthyMemberClusters() {
					require.NoError(t, reconciler.ensureAppDBConnectionStringInMemberCluster(ctx, testOm, cfg.ConnectionString, memberCluster, zap.S()))
				}

				result := corev1.Secret{}
				require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()), &result))
				assert.Contains(t, string(result.Data[util.AppDbConnectionStringKey]), util.OpsManagerMongoDBUserName)
			})
		}
	}
}

func TestValidateExternalAppDBTopologyGuards(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                  string
		appDBStsClusterNums   map[string]int
		externalClusterNames  []string
		expectedErrorContains string
	}{
		{
			name:                 "aligned internal and external multi-cluster AppDB passes",
			appDBStsClusterNums:  map[string]int{"cluster-1": 0, "cluster-2": 1, "cluster-3": 2},
			externalClusterNames: []string{"cluster-1", "cluster-2", "cluster-3"},
		},
		{
			name:                  "external cluster without an AppDB StatefulSet is rejected",
			appDBStsClusterNums:   map[string]int{"cluster-1": 0, "cluster-2": 1, "cluster-3": 2},
			externalClusterNames:  []string{"cluster-1", "cluster-2", "cluster-3", "cluster-4"},
			expectedErrorContains: "cluster-4",
		},
		{
			name:                  "cluster number mismatch is rejected",
			appDBStsClusterNums:   map[string]int{"cluster-1": 1, "cluster-2": 0, "cluster-3": 2},
			externalClusterNames:  []string{"cluster-1", "cluster-2", "cluster-3"},
			expectedErrorContains: "cluster-1",
		},
		{
			name:                  "AppDB StatefulSet outside the external clusters is rejected",
			appDBStsClusterNums:   map[string]int{"cluster-1": 0, "cluster-2": 1},
			externalClusterNames:  []string{"cluster-1"},
			expectedErrorContains: "cluster-2",
		},
		{
			name:                 "fresh adoption without AppDB StatefulSets passes",
			appDBStsClusterNums:  map[string]int{},
			externalClusterNames: []string{"cluster-1", "cluster-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetCurrMockedAdmin(t)

			opsManager := DefaultOpsManagerBuilder().SetName("test-om").Build()
			testOm := withExternalAppDBRef(opsManager, &omv1.ExternalAppDBRef{
				Name:      "test-om-db",
				Kind:      omv1.ExternalAppDBRefKindMongoDBMultiCluster,
				Namespace: mock.TestNamespace,
			})

			allClusters := map[string]struct{}{}
			for clusterName := range tt.appDBStsClusterNums {
				allClusters[clusterName] = struct{}{}
			}
			for _, clusterName := range tt.externalClusterNames {
				allClusters[clusterName] = struct{}{}
			}
			clusterNames := make([]string, 0, len(allClusters))
			for clusterName := range allClusters {
				clusterNames = append(clusterNames, clusterName)
			}
			slices.Sort(clusterNames)

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			memberClusterMap := getAppDBFakeMultiClusterMapWithClusters(clusterNames, omConnectionFactory)
			reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClusterMap, omConnectionFactory, architectures.NonStatic)

			external := mdbmulti.DefaultMultiReplicaSetBuilder().
				SetName("test-om-db").
				SetRole(mdbv1.RoleAppDB).
				SetClusterSpecList(tt.externalClusterNames).
				Build()
			external.Namespace = mock.TestNamespace
			require.NoError(t, reconciler.client.Create(ctx, external))

			for clusterName, clusterNum := range tt.appDBStsClusterNums {
				sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-om-db-%d", clusterNum),
					Namespace: mock.TestNamespace,
				}}
				require.NoError(t, memberClusterMap[clusterName].Create(ctx, sts))
			}

			err := reconciler.createNewExternalAppDBReconciler(zap.S()).validateExternalAppDBReference(ctx, testOm)
			if tt.expectedErrorContains != "" {
				require.ErrorContains(t, err, tt.expectedErrorContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateSingleClusterExternalAppDBTopology(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                  string
		appDBStsClusterNums   map[string]int
		expectedErrorContains string
	}{
		{
			name:                "single-cluster external MongoDB without per-cluster AppDB StatefulSets passes",
			appDBStsClusterNums: map[string]int{},
		},
		{
			name:                  "per-cluster AppDB StatefulSet is rejected for a single-cluster external MongoDB",
			appDBStsClusterNums:   map[string]int{"cluster-1": 0},
			expectedErrorContains: "single-cluster external MongoDB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetCurrMockedAdmin(t)

			opsManager := DefaultOpsManagerBuilder().SetName("test-om").Build()
			testOm := withExternalAppDBRef(opsManager, &omv1.ExternalAppDBRef{
				Name:      "test-om-db",
				Kind:      omv1.ExternalAppDBRefKindMongoDB,
				Namespace: mock.TestNamespace,
			})

			clusterNames := make([]string, 0, len(tt.appDBStsClusterNums))
			for clusterName := range tt.appDBStsClusterNums {
				clusterNames = append(clusterNames, clusterName)
			}
			slices.Sort(clusterNames)

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			memberClusterMap := getAppDBFakeMultiClusterMapWithClusters(clusterNames, omConnectionFactory)
			reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClusterMap, omConnectionFactory, architectures.NonStatic)

			require.NoError(t, reconciler.client.Create(ctx, validExternalAppDBMongoDB()))

			for clusterName, clusterNum := range tt.appDBStsClusterNums {
				sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-om-db-%d", clusterNum),
					Namespace: mock.TestNamespace,
				}}
				require.NoError(t, memberClusterMap[clusterName].Create(ctx, sts))
			}

			err := reconciler.createNewExternalAppDBReconciler(zap.S()).validateExternalAppDBReference(ctx, testOm)
			if tt.expectedErrorContains != "" {
				require.ErrorContains(t, err, tt.expectedErrorContains)
				return
			}

			require.NoError(t, err)
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
