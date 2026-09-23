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
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

type appDBStatefulSetState struct {
	ownerReferences []metav1.OwnerReference
	annotations     map[string]string
	labels          map[string]string
}

func newAppDBStatefulSetStatefulSet(name string, state appDBStatefulSetState) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       mock.TestNamespace,
			OwnerReferences: state.ownerReferences,
			Annotations:     state.annotations,
			Labels:          state.labels,
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))},
	}
}

func getAppDBStatefulSetState(t *testing.T, ctx context.Context, c client.Client, namespace, name string) appDBStatefulSetState {
	t.Helper()

	sts := appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(namespace, name), &sts))
	return appDBStatefulSetState{ownerReferences: sts.OwnerReferences, annotations: sts.Annotations, labels: sts.Labels}
}

func mergeAppDBStatefulSetLabels(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

func externalAppDBClusterList(t *testing.T, ctx context.Context, reconciler *OpsManagerReconciler, testOm *omv1.MongoDBOpsManager) []appDBClusterItem {
	t.Helper()
	if testOm.Spec.ExternalAppDBRef != nil && testOm.Spec.ExternalAppDBRef.Kind == omv1.ExternalAppDBRefKindMongoDB {
		return []appDBClusterItem{{clusterName: multicluster.LegacyCentralClusterName, client: reconciler.client, stsName: testOm.Spec.ExternalAppDBRef.Name}}
	}
	externalAppDB, err := reconciler.createNewExternalAppDBReconciler(zap.S()).getExternalAppDBReference(ctx, testOm)
	require.NoError(t, err)
	return externalAppDB.GetClusterList()
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
			_, err := reconciler.createNewExternalAppDBReconciler(zap.S()).getExternalAppDBReference(ctx, tt.om)
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
	return mdbmulti.DefaultMultiReplicaSetBuilder().
		SetName("test-om-db").
		SetRole(mdbv1.RoleAppDB).
		SetClusterSpecList([]string{"cluster-1", "cluster-2", "cluster-3"}).
		SetSecurity(&mdbv1.Security{
			TLSConfig:      &mdbv1.TLSConfig{Enabled: tlsEnabled, CA: caConfigMapName},
			Authentication: &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM}},
		}).
		Build()
}

func centralClusterItemsFor(e *ReconcileExternalAppDBReplicaSet, opsManager *omv1.MongoDBOpsManager) []appDBClusterItem {
	return []appDBClusterItem{{clusterName: multicluster.LegacyCentralClusterName, client: e.client, stsName: opsManager.Spec.ExternalAppDBRef.Name}}
}

func TestEnsureAppDBStatefulSetOwnership_StripsOwnershipAndAnnotates(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	mdb := validExternalAppDBMongoDB()

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-om-db",
			Namespace:       mock.TestNamespace,
			OwnerReferences: kube.BaseOwnerReference(testOm),
			Labels:          testOm.GetOwnerLabels(),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
		},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	ext := reconciler.createNewExternalAppDBReconciler(zap.S())
	err := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, centralClusterItemsFor(ext, testOm))
	assert.NoError(t, err)

	resultSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
	assert.Empty(t, resultSts.OwnerReferences)
	assert.Empty(t, resultSts.Labels[util.MongoDBOpsManagerResourceOwnerLabel])
	assert.Equal(t, util.OperatorLabelValue, resultSts.Labels[util.OperatorLabelName])
	assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
}

func TestEnsureAppDBStatefulSetOwnership_NoOpWhenNoStatefulSetExists(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	mdb := validExternalAppDBMongoDB()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))

	ext := reconciler.createNewExternalAppDBReconciler(zap.S())
	err := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, centralClusterItemsFor(ext, testOm))
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
			Labels:          testOm.GetOwnerLabels(),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
		},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	ext := reconciler.createNewExternalAppDBReconciler(zap.S())
	require.NoError(t, ext.ensureAppDBStatefulSetOwnership(ctx, testOm, centralClusterItemsFor(ext, testOm)))
	require.NoError(t, ext.ensureAppDBStatefulSetOwnership(ctx, testOm, centralClusterItemsFor(ext, testOm)))

	resultSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
	assert.Empty(t, resultSts.OwnerReferences)
	assert.Empty(t, resultSts.Labels[util.MongoDBOpsManagerResourceOwnerLabel])
	assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
}

func TestExternalAppDBReference_SingleClusterUsesCentralClusterName(t *testing.T) {
	ctx := context.Background()
	resetCurrMockedAdmin(t)

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	clusterList := centralClusterItemsFor(reconciler.createNewExternalAppDBReconciler(zap.S()), testOm)
	require.Len(t, clusterList, 1)
	assert.Equal(t, multicluster.LegacyCentralClusterName, clusterList[0].clusterName)
	assert.Equal(t, "test-om-db", clusterList[0].stsName)
}

func TestEnsureAppDBStatefulSetOwnership_DetachesByLabel(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	testOm.UID = types.UID("om-uid-1111")
	foreignOwnerRef := metav1.OwnerReference{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: types.UID("foreign-uid-1")}
	omOwnerLabels := testOm.GetOwnerLabels()

	tests := []struct {
		name          string
		state         appDBStatefulSetState
		expectedState appDBStatefulSetState
	}{
		{
			name: "OM label wins even when a foreign ownerReference is present",
			state: appDBStatefulSetState{
				ownerReferences: []metav1.OwnerReference{kube.BaseOwnerReference(testOm)[0], foreignOwnerRef},
				annotations:     map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString},
				labels:          mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels),
			},
			expectedState: appDBStatefulSetState{
				ownerReferences: nil,
				annotations:     map[string]string{util.AppDBMigrationReadyAnnotation: trueString},
				labels:          map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"},
			},
		},
		{
			name: "missing OM label leaves the StatefulSet untouched",
			state: appDBStatefulSetState{
				ownerReferences: kube.BaseOwnerReference(testOm),
				labels:          map[string]string{"app": "demo"},
			},
			expectedState: appDBStatefulSetState{
				ownerReferences: nil,
				annotations:     map[string]string{util.AppDBMigrationReadyAnnotation: trueString},
				labels:          map[string]string{"app": "demo"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
			require.NoError(t, reconciler.client.Create(ctx, newAppDBStatefulSetStatefulSet("test-om-db", tt.state)))

			err := reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm, centralClusterItemsFor(reconciler.createNewExternalAppDBReconciler(zap.S()), testOm))
			assert.NoError(t, err)

			resultSts := appsv1.StatefulSet{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
			assert.Equal(t, tt.expectedState.ownerReferences, resultSts.OwnerReferences)
			assert.Equal(t, tt.expectedState.annotations, resultSts.Annotations)
			assert.Equal(t, tt.expectedState.labels, resultSts.Labels)
		})
	}
}

func TestEnsureAppDBStatefulSetOwnership_MultiClusterExternal(t *testing.T) {
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
	omOwnerLabels := testOm.GetOwnerLabels()

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
				"cluster-1": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
				"cluster-2": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: nil, annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}, labels: map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"}},
				"cluster-2": {ownerReferences: nil, annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}, labels: map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"}},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
			},
		},
		{
			name: "foreign-owned StatefulSets stay untouched",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
				"cluster-2": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
				"cluster-2": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
			},
		},
		{
			name: "missing client reports the cluster and detaches no StatefulSet",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
				"cluster-2": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
				"cluster-3": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
			},
			missingClients: []string{"cluster-2"},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
				"cluster-3": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
			},
			expectedErrorContains: "cluster-2",
		},
		{
			name: "partially existing StatefulSets are rejected",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
				"cluster-3": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
				"cluster-3": {ownerReferences: kube.BaseOwnerReference(testOm), annotations: map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString}, labels: mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels)},
			},
			expectedErrorContains: "cluster-2",
		},
		{
			name: "already detached StatefulSets are idempotent on a second run",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}, labels: map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"}},
				"cluster-2": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}, labels: map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"}},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}, labels: map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"}},
				"cluster-2": {annotations: map[string]string{util.AppDBMigrationReadyAnnotation: trueString}, labels: map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"}},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
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

			err := reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDBClusterList(t, ctx, reconciler, testOm))
			if tt.expectedErrorContains != "" {
				require.ErrorContains(t, err, tt.expectedErrorContains)
			} else {
				require.NoError(t, err)
			}

			if tt.runTwice {
				require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDBClusterList(t, ctx, reconciler, testOm)))
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
				assert.Equal(t, expected.labels, result.labels, "cluster %s labels", clusterName)
			}
		})
	}
}

func TestEnsureAppDBStatefulSetOwnership_MongoDBRegression(t *testing.T) {
	ctx := context.Background()
	const omUID = "om-uid-3333"

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	testOm.UID = types.UID(omUID)
	omOwnerLabels := testOm.GetOwnerLabels()

	rows := []struct {
		name          string
		state         appDBStatefulSetState
		expectedState appDBStatefulSetState
	}{
		{
			name: "OM label detaches and annotates",
			state: appDBStatefulSetState{
				ownerReferences: kube.BaseOwnerReference(testOm),
				annotations:     map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString},
				labels:          mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels),
			},
			expectedState: appDBStatefulSetState{
				ownerReferences: nil,
				annotations:     map[string]string{util.AppDBMigrationReadyAnnotation: trueString},
				labels:          map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"},
			},
		},
		{
			name: "foreign label keeps the central StatefulSet untouched",
			state: appDBStatefulSetState{
				ownerReferences: kube.BaseOwnerReference(testOm),
				labels:          map[string]string{util.MongoDBResourceOwnerLabel: "test-mdb", "app": "demo"},
			},
			expectedState: appDBStatefulSetState{
				ownerReferences: kube.BaseOwnerReference(testOm),
				labels:          map[string]string{util.MongoDBResourceOwnerLabel: "test-mdb", "app": "demo"},
			},
		},
	}

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			memberClustersMap := map[string]client.Client{}
			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClustersMap, omConnectionFactory, architectures.NonStatic)
			mdb := validExternalAppDBMongoDB()
			mdb.ResourceVersion = ""
			require.NoError(t, reconciler.client.Create(ctx, mdb))
			require.NoError(t, kubeClient.Create(ctx, newAppDBStatefulSetStatefulSet("test-om-db", tt.state)))

			require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDBClusterList(t, ctx, reconciler, testOm)))

			result := getAppDBStatefulSetState(t, ctx, kubeClient, mock.TestNamespace, "test-om-db")
			assert.Equal(t, tt.expectedState.ownerReferences, result.ownerReferences)
			assert.Equal(t, tt.expectedState.annotations, result.annotations)
		})
	}
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

func TestEnsureAppDBStatefulSetOwnership_OnlyDetachesOMOwnedStatefulSet(t *testing.T) {
	// Ownership is decided by the resource-owner label, not by an ownerReference, so each row varies
	// both to pin which one is authoritative. A legacy StatefulSet carrying only this Ops Manager's
	// ownerReference is recognised by backfilling the label in memory. Real UIDs keep an
	// ownerReference comparison from matching on the empty string.
	const omUID = "om-uid-1111"
	const crUID = "cr-uid-2222"
	const otherUID = "other-om-uid"

	tests := []struct {
		name             string
		stsLabels        func(testOm *omv1.MongoDBOpsManager) map[string]string
		stsOwnerRefs     func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference
		expectedDetached bool
	}{
		{
			name: "OM-owned StatefulSet is stripped and annotated",
			stsLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return testOm.GetOwnerLabels()
			},
			stsOwnerRefs: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			expectedDetached: true,
		},
		{
			name: "OM ownerReference without the owner label is detached via in-memory backfill",
			stsLabels: func(*omv1.MongoDBOpsManager) map[string]string {
				return nil
			},
			stsOwnerRefs: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return kube.BaseOwnerReference(testOm)
			},
			expectedDetached: true,
		},
		{
			name: "ownerReference naming this Ops Manager but carrying a foreign UID is not backfilled",
			stsLabels: func(*omv1.MongoDBOpsManager) map[string]string {
				return nil
			},
			stsOwnerRefs: func(testOm *omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return []metav1.OwnerReference{{
					APIVersion: "mongodb.com/v1",
					Kind:       "MongoDBOpsManager",
					Name:       testOm.GetName(),
					UID:        otherUID,
				}}
			},
			expectedDetached: false,
		},
		{
			name: "owner label without an ownerReference is detached",
			stsLabels: func(testOm *omv1.MongoDBOpsManager) map[string]string {
				return testOm.GetOwnerLabels()
			},
			stsOwnerRefs: func(*omv1.MongoDBOpsManager) []metav1.OwnerReference {
				return nil
			},
			expectedDetached: true,
		},
		{
			name: "CR-owned StatefulSet (fresh start) is untouched",
			stsLabels: func(*omv1.MongoDBOpsManager) map[string]string {
				return map[string]string{util.MongoDBResourceOwnerLabel: "test-om-db"}
			},
			stsOwnerRefs: func(*omv1.MongoDBOpsManager) []metav1.OwnerReference {
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
			name: "label-free StatefulSet (already detached and consumed) is not re-annotated",
			stsLabels: func(*omv1.MongoDBOpsManager) map[string]string {
				return nil
			},
			stsOwnerRefs: func(*omv1.MongoDBOpsManager) []metav1.OwnerReference {
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
					Labels:          tt.stsLabels(testOm),
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			}

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
			require.NoError(t, reconciler.client.Create(ctx, mdb))
			require.NoError(t, reconciler.client.Create(ctx, &sts))

			ext := reconciler.createNewExternalAppDBReconciler(zap.S())
			require.NoError(t, ext.ensureAppDBStatefulSetOwnership(ctx, testOm, centralClusterItemsFor(ext, testOm)))

			resultSts := appsv1.StatefulSet{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))

			if tt.expectedDetached {
				assert.Empty(t, resultSts.OwnerReferences)
				assert.Empty(t, resultSts.Labels[util.MongoDBOpsManagerResourceOwnerLabel])
				assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
			} else {
				assert.Equal(t, originalOwnerRefs, resultSts.OwnerReferences)
				assert.NotContains(t, resultSts.Annotations, util.AppDBMigrationReadyAnnotation)
			}
		})
	}
}
