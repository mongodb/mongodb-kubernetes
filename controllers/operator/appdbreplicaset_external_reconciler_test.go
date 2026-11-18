package operator

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	operatorConstruct "github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
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

func managedForwardFixture(t *testing.T) (*omv1.MongoDBOpsManager, *mdbv1.MongoDB, *OpsManagerReconciler, *appsv1.StatefulSet) {
	t.Helper()
	ctx := t.Context()
	om := newAppDBManagedCertificateTestOM()
	om.Spec.AppDB.Security.ManagedCertificate.Server = &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "handover-customer", Kind: "Issuer"}}
	c := newManagedCertificateKubeClient(t, om)
	r := newManagedCertificateTestReconciler(ctx, c)
	r.certManagerReadiness = certs.CertManagerReadiness{Ready: true}
	_, _, err := r.prepareManagedCertificates(ctx, om, newManagedCertificateTestHelper(t, r, om), zap.S())
	require.NoError(t, err)
	markAllManagedCertificatesReady(t, ctx, c)
	createSecretsForManagedCertificates(t, ctx, c)
	issueAppDBManagedCertificate(t, ctx, c, om)
	_, blocked, err := r.prepareManagedCertificates(ctx, om, newManagedCertificateTestHelper(t, r, om), zap.S())
	require.NoError(t, err)
	require.False(t, blocked)
	appdb, err := newAppDbReconciler(ctx, c, om, nil, zap.S())
	require.NoError(t, err)
	_, hash := deliverAppDBManagedPEM(t, ctx, c, appdb, om)
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)
	sts, err := operatorConstruct.AppDbStatefulSet(*om, nil, operatorConstruct.AppDBStatefulSetOptions{CertHash: hash}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)
	sts.Spec.Replicas = new(int32(3))
	sts.Labels = om.GetOwnerLabels()
	sts.OwnerReferences = kube.BaseOwnerReference(om)
	require.NoError(t, c.Create(ctx, &sts))
	current := &omv1.MongoDBOpsManager{}
	require.NoError(t, c.Get(ctx, kube.ObjectKeyFromApiObject(om), current))
	om.ResourceVersion = current.ResourceVersion
	target := DefaultReplicaSetBuilder().SetName(om.AppDBName()).SetRole(mdbv1.RoleAppDB).SetMembers(3).Build()
	target.Namespace = om.Namespace
	target.UID = types.UID("handover-target-uid")
	target.Spec.Security = om.Spec.AppDB.Security.DeepCopy()
	target.Spec.Version = om.Spec.AppDB.Version
	target.Spec.FeatureCompatibilityVersion = om.Spec.AppDB.FeatureCompatibilityVersion
	withExternalAppDBRef(om, &omv1.ExternalAppDBRef{Name: target.Name, Kind: "MongoDB"})
	require.NoError(t, c.Update(ctx, om))
	require.NoError(t, c.Create(ctx, target))
	return om, target, r, &sts
}

// TestManagedAppDBForwardMigrationOrdering verifies the source release is the ordinary
// StatefulSet detach. It must leave the shared certificate graph owned by the source for the
// target's normal certificate reconcile.
func TestManagedAppDBForwardMigrationOrdering(t *testing.T) {
	om, target, r, sts := managedForwardFixture(t)
	e := r.createNewExternalAppDBReconciler(zap.S())
	ref, err := e.getExternalAppDBReference(t.Context(), om)
	require.NoError(t, err)
	status := e.ensureAppDBStatefulSetOwnership(t.Context(), om, ref)
	require.True(t, status.IsOK(), statusMessage(status))
	released := &appsv1.StatefulSet{}
	require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(sts), released))
	require.Equal(t, trueString, released.Annotations[util.AppDBMigrationReadyAnnotation])
	require.Empty(t, released.OwnerReferences)
	leaf := getManagedCertificateObject(t, t.Context(), r.client, target.Name+"-managed-cert")
	assert.Equal(t, om.UID, leaf.OwnerReferences[0].UID, "the ordinary source release must not re-own the certificate graph")
}

// TestManagedAppDBForwardMigrationTargetGateReclaims verifies the target ownership gate keeps
// its ordinary reclaim behavior and does not adopt certificates itself. The target's normal
// EnsureCertificatesAndCA is what re-owns the graph, later in the same reconcile.
func TestManagedAppDBForwardMigrationTargetGateReclaims(t *testing.T) {
	om, target, r, sts := managedForwardFixture(t)
	e := r.createNewExternalAppDBReconciler(zap.S())
	ref, err := e.getExternalAppDBReference(t.Context(), om)
	require.NoError(t, err)
	require.True(t, e.ensureAppDBStatefulSetOwnership(t.Context(), om, ref).IsOK())
	om.Spec.AppDB = nil
	require.NoError(t, r.client.Update(t.Context(), om))
	rs := &ReconcileMongoDbReplicaSet{ReconcileCommonController: r.ReconcileCommonController}
	helper := &ReplicaSetReconcilerHelper{resource: target, reconciler: rs, log: zap.S()}
	require.True(t, helper.ensureAppDBStatefulSetOwnership(t.Context(), target).IsOK())
	leaf := getManagedCertificateObject(t, t.Context(), r.client, target.Name+"-managed-cert")
	assert.Equal(t, om.UID, leaf.OwnerReferences[0].UID, "the ownership gate must not adopt certificates")
	persisted := &appsv1.StatefulSet{}
	require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(sts), persisted))
	require.NotEmpty(t, persisted.OwnerReferences)
	assert.Equal(t, target.UID, persisted.OwnerReferences[0].UID)
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
		Spec: appsv1.StatefulSetSpec{Replicas: new(int32(3))},
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
	maps.Copy(merged, base)
	maps.Copy(merged, extra)
	return merged
}

func newOpsManagerReconcilerForValidation(objects ...client.Object) *OpsManagerReconciler {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(objects...)
	return NewOpsManagerReconciler(context.Background(), kubeClient, map[string]client.Client{}, images.ImageUrls{}, "", "", architectures.Static, "", omConnectionFactory.GetConnectionFunc, nil, nil)
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
			Replicas: new(int32(3)),
		},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	ext := reconciler.createNewExternalAppDBReconciler(zap.S())
	externalAppDB, err := ext.getExternalAppDBReference(ctx, testOm)
	require.NoError(t, err)
	st := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
	require.True(t, st.IsOK())

	resultSts := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &resultSts))
	assert.Empty(t, resultSts.OwnerReferences)
	assert.Empty(t, resultSts.Labels[util.MongoDBOpsManagerResourceOwnerLabel])
	assert.Equal(t, util.OperatorLabelValue, resultSts.Labels[util.OperatorLabelName])
	assert.Equal(t, "true", resultSts.Annotations[util.AppDBMigrationReadyAnnotation])
}

func TestEnsureAppDBStatefulSetOwnership_WaitsWhenNoStatefulSetExists(t *testing.T) {
	ctx := context.Background()

	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	mdb := validExternalAppDBMongoDB()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))

	ext := reconciler.createNewExternalAppDBReconciler(zap.S())
	externalAppDB, err := ext.getExternalAppDBReference(ctx, testOm)
	require.NoError(t, err)
	st := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
	require.Equal(t, status.PhasePending, st.Phase(), "Fresh Start: no internal AppDB StatefulSet ever existed, the gate waits for the external AppDB to create it")
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
			Replicas: new(int32(3)),
		},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	ext := reconciler.createNewExternalAppDBReconciler(zap.S())
	externalAppDB, err := ext.getExternalAppDBReference(ctx, testOm)
	require.NoError(t, err)
	st := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
	require.True(t, st.IsOK())
	st = ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
	require.True(t, st.IsOK())

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
	require.NoError(t, reconciler.client.Create(ctx, validExternalAppDBMongoDB()))

	externalAppDB, err := reconciler.createNewExternalAppDBReconciler(zap.S()).getExternalAppDBReference(ctx, testOm)
	require.NoError(t, err)
	clusterList := externalAppDB.getClusterList()
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
			require.NoError(t, reconciler.client.Create(ctx, validExternalAppDBMongoDB()))
			require.NoError(t, reconciler.client.Create(ctx, newAppDBStatefulSetStatefulSet("test-om-db", tt.state)))

			ext := reconciler.createNewExternalAppDBReconciler(zap.S())
			externalAppDB, err := ext.getExternalAppDBReference(ctx, testOm)
			require.NoError(t, err)
			st := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
			require.True(t, st.IsOK())

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
		name            string
		createStates    map[string]appDBStatefulSetState
		missingClients  []string
		expectedStates  map[string]appDBStatefulSetState
		expectedPhase   status.Phase
		expectedMessage string
		runTwice        bool
	}{
		{
			name:            "fresh start waits for the external AppDB to create the StatefulSets",
			createStates:    map[string]appDBStatefulSetState{},
			expectedStates:  map[string]appDBStatefulSetState{},
			expectedPhase:   status.PhasePending,
			expectedMessage: "waiting for the external AppDB to create StatefulSet test-om-db-0 for cluster cluster-1",
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
			expectedPhase:   status.PhaseFailed,
			expectedMessage: "member cluster cluster-2 client is not available",
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
			expectedPhase:   status.PhaseFailed,
			expectedMessage: "StatefulSet test-om-db-1 for cluster cluster-2 is missing: declared by the external AppDB reference but not part of the internal AppDB",
		},
		{
			name: "partially existing StatefulSets wait for the external AppDB when none are OM-owned",
			createStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
			},
			expectedStates: map[string]appDBStatefulSetState{
				"cluster-1": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
				"cluster-3": {ownerReferences: foreignOwner, labels: map[string]string{util.MongoDBMultiClusterResourceOwnerLabel: "test-mdbm", "app": "demo"}},
			},
			expectedPhase:   status.PhasePending,
			expectedMessage: "waiting for the external AppDB to create StatefulSet test-om-db-1 for cluster cluster-2",
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

			ext := reconciler.createNewExternalAppDBReconciler(zap.S())
			externalAppDB, err := ext.getExternalAppDBReference(ctx, testOm)
			require.NoError(t, err)

			st := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
			if tt.expectedMessage != "" {
				require.Equal(t, tt.expectedPhase, st.Phase())
				assert.Equal(t, tt.expectedMessage, statusMessage(st))
			} else {
				require.True(t, st.IsOK())
			}

			if tt.runTwice {
				st = ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
				require.True(t, st.IsOK())
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

func TestEnsureAppDBStatefulSetOwnership_RejectsUndeclaredInternalAppDBStatefulSet(t *testing.T) {
	ctx := context.Background()
	const omUID = "om-uid-1111"

	rows := []struct {
		name             string
		refKind          string
		declaredClusters []string
		createdClusters  []string
		extraOMOwned     map[string][]string
		wantDetached     bool
		expectedPhase    status.Phase
		expectedMessage  string
	}{
		{
			name:             "rejects an undeclared internal StatefulSet when it exists",
			refKind:          omv1.ExternalAppDBRefKindMongoDBMultiCluster,
			declaredClusters: []string{"cluster-1", "cluster-2"},
			createdClusters:  []string{"cluster-1", "cluster-2", "cluster-3"},
			expectedPhase:    status.PhaseFailed,
			expectedMessage:  "StatefulSet test-om-db-2 in cluster cluster-3 is not declared by the external AppDB reference: the external AppDB cluster numbers do not match the internal AppDB",
		},
		{
			name:             "accepts a declared internal StatefulSet",
			refKind:          omv1.ExternalAppDBRefKindMongoDBMultiCluster,
			declaredClusters: []string{"cluster-1", "cluster-2", "cluster-3"},
			createdClusters:  []string{"cluster-1", "cluster-2", "cluster-3"},
			wantDetached:     true,
		},
		{
			name:             "ignores Ops Manager application and backup-daemon StatefulSets",
			refKind:          omv1.ExternalAppDBRefKindMongoDBMultiCluster,
			declaredClusters: []string{"cluster-1", "cluster-2"},
			createdClusters:  []string{"cluster-1", "cluster-2"},
			extraOMOwned: map[string][]string{
				"cluster-3": {"test-om-0", "test-om-backup-daemon"},
			},
			wantDetached: true,
		},
		{
			name:            "skips the undeclared StatefulSet check for single-cluster references",
			refKind:         omv1.ExternalAppDBRefKindMongoDB,
			createdClusters: []string{multicluster.LegacyCentralClusterName},
			wantDetached:    true,
		},
	}

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			refName := "test-om-db"
			if tt.refKind == omv1.ExternalAppDBRefKindMongoDB {
				refName = "test-om-db-2"
			}

			testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), &omv1.ExternalAppDBRef{Name: refName, Kind: tt.refKind, Namespace: mock.TestNamespace})
			testOm.UID = types.UID(omUID)
			omOwnerLabels := testOm.GetOwnerLabels()
			legacyState := appDBStatefulSetState{
				ownerReferences: kube.BaseOwnerReference(testOm),
				annotations:     map[string]string{util.AppDBReverseMigrationReadyAnnotation: trueString},
				labels:          mergeAppDBStatefulSetLabels(map[string]string{"app": "demo"}, omOwnerLabels),
			}
			detachedState := appDBStatefulSetState{
				ownerReferences: nil,
				annotations:     map[string]string{util.AppDBMigrationReadyAnnotation: trueString},
				labels:          map[string]string{util.OperatorLabelName: util.OperatorLabelValue, "app": "demo"},
			}

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			memberClustersMap := getAppDBFakeMultiClusterMapWithClusters([]string{"cluster-1", "cluster-2", "cluster-3"}, omConnectionFactory)
			reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClustersMap, omConnectionFactory, architectures.NonStatic)

			var (
				clusterList    []appDBClusterItem
				statefulSetFor func(string) string
				isMulticluster bool
			)

			switch tt.refKind {
			case omv1.ExternalAppDBRefKindMongoDBMultiCluster:
				ref := validExternalAppDBMongoDBMultiCluster()
				ref.Namespace = mock.TestNamespace
				ref.Name = refName
				ref.Spec.ClusterSpecList = make(mdbv1.ClusterSpecList, len(tt.declaredClusters))
				for idx, clusterName := range tt.declaredClusters {
					ref.Spec.ClusterSpecList[idx] = mdbv1.ClusterSpecItem{ClusterName: clusterName}
				}
				require.NoError(t, reconciler.client.Create(ctx, ref))
				externalAppDB, err := reconciler.createNewExternalAppDBReconciler(zap.S()).getExternalAppDBReference(ctx, testOm)
				require.NoError(t, err)
				clusterList = externalAppDB.getClusterList()
				statefulSetFor = ref.StatefulSetNameForCluster
				isMulticluster = true
			case omv1.ExternalAppDBRefKindMongoDB:
				ref := validExternalAppDBMongoDB()
				ref.Namespace = mock.TestNamespace
				ref.Name = refName
				require.NoError(t, reconciler.client.Create(ctx, ref))
				externalAppDB, err := reconciler.createNewExternalAppDBReconciler(zap.S()).getExternalAppDBReference(ctx, testOm)
				require.NoError(t, err)
				clusterList = externalAppDB.getClusterList()
				statefulSetFor = func(string) string { return ref.Name }
			default:
				t.Fatalf("unsupported ref kind %q", tt.refKind)
			}

			clientForCluster := func(clusterName string) client.Client {
				if isMulticluster {
					return memberClustersMap[clusterName]
				}
				return clusterList[0].client
			}

			for _, clusterName := range tt.createdClusters {
				require.NoError(t, clientForCluster(clusterName).Create(ctx, newAppDBStatefulSetStatefulSet(statefulSetFor(clusterName), legacyState)))
			}
			for clusterName, names := range tt.extraOMOwned {
				for _, name := range names {
					require.NoError(t, clientForCluster(clusterName).Create(ctx, newAppDBStatefulSetStatefulSet(name, legacyState)))
				}
			}

			externalAppDB, err := reconciler.createNewExternalAppDBReconciler(zap.S()).getExternalAppDBReference(ctx, testOm)
			require.NoError(t, err)
			st := reconciler.createNewExternalAppDBReconciler(zap.S()).ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
			if tt.expectedMessage == "" {
				require.True(t, st.IsOK())
			} else {
				require.Equal(t, tt.expectedPhase, st.Phase())
				assert.Equal(t, tt.expectedMessage, statusMessage(st))
			}

			expectedState := legacyState
			if tt.wantDetached {
				expectedState = detachedState
			}

			for _, clusterName := range tt.createdClusters {
				result := getAppDBStatefulSetState(t, ctx, clientForCluster(clusterName), mock.TestNamespace, statefulSetFor(clusterName))
				assert.Equal(t, expectedState.ownerReferences, result.ownerReferences, "cluster %s owner refs", clusterName)
				assert.Equal(t, expectedState.annotations, result.annotations, "cluster %s annotations", clusterName)
				assert.Equal(t, expectedState.labels, result.labels, "cluster %s labels", clusterName)
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

			ext := reconciler.createNewExternalAppDBReconciler(zap.S())
			externalAppDB, err := ext.getExternalAppDBReference(ctx, testOm)
			require.NoError(t, err)
			st := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
			require.True(t, st.IsOK())

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
		managedCertificate      *mdbv1.ManagedCertificate
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
			name:                    "managed certificates without legacy TLS config use the generated server bundle",
			managedCertificate:      &mdbv1.ManagedCertificate{},
			createPasswordSecret:    true,
			expectedIsTLSEnabled:    true,
			expectedCAConfigMapName: "test-om-db-managed-server-ca-bundle",
		},
		{
			name: "customer issuer CA input still resolves to the generated server bundle",
			managedCertificate: &mdbv1.ManagedCertificate{Server: &mdbv1.CertificateConfig{
				IssuerRef:            &mdbv1.IssuerRef{Name: "appdb-issuer", Kind: "Issuer"},
				CA:                   &mdbv1.CARef{Name: "issuer-ca", Key: "ca.pem"},
				AdditionalTrustedCAs: []mdbv1.CARef{{Name: "additional-ca", Key: "ca.pem"}},
			}},
			createPasswordSecret:    true,
			expectedIsTLSEnabled:    true,
			expectedCAConfigMapName: "test-om-db-managed-server-ca-bundle",
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
				if tt.managedCertificate != nil {
					switch db := externalAppDB.(type) {
					case *mdbv1.MongoDB:
						db.Spec.Security.TLSConfig = nil
						db.Spec.Security.ManagedCertificate = tt.managedCertificate.DeepCopy()
					case *mdbmulti.MongoDBMultiCluster:
						db.Spec.Security.TLSConfig = nil
						db.Spec.Security.ManagedCertificate = tt.managedCertificate.DeepCopy()
					}
				}
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

func TestExternalAppDBManagedCA(t *testing.T) {
	for _, kind := range []string{omv1.ExternalAppDBRefKindMongoDB, omv1.ExternalAppDBRefKindMongoDBMultiCluster} {
		for _, topology := range []string{mdbv1.ClusterTopologySingleCluster, mdbv1.ClusterTopologyMultiCluster} {
			t.Run(kind+"/"+topology, func(t *testing.T) {
				ctx := t.Context()
				builder := DefaultOpsManagerBuilder().SetOpsManagerTopology(topology)
				factory := om.NewDefaultCachedOMConnectionFactory()
				var memberClients map[string]client.Client
				if topology == mdbv1.ClusterTopologyMultiCluster {
					builder.SetOpsManagerClusterSpecList([]omv1.ClusterSpecOMItem{
						{ClusterName: "om-cluster-1", Members: 1, Backup: &omv1.MongoDBOpsManagerBackupClusterSpecItem{Members: 1}},
						{ClusterName: "om-cluster-2", Members: 1, Backup: &omv1.MongoDBOpsManagerBackupClusterSpecItem{Members: 1}},
						{ClusterName: "om-cluster-3", Members: 1, Backup: &omv1.MongoDBOpsManagerBackupClusterSpecItem{Members: 1}},
					})
					memberClients = getFakeMultiClusterMapWithClusters([]string{"om-cluster-1", "om-cluster-2", "om-cluster-3"}, factory)
				}
				testOm := withExternalAppDBRef(builder.Build(), &omv1.ExternalAppDBRef{Name: "test-om-db", Kind: kind})
				testOm.Spec.AppDB = nil
				testOm.Spec.Security = &omv1.MongoDBOpsManagerSecurity{TLS: omv1.MongoDBOpsManagerTLS{CA: "om-https-ca"}}
				testOm.Spec.Backup.Enabled = true
				testOm.Spec.Backup.Members = 1
				reconciler, central, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClients, factory, architectures.NonStatic)

				security := &mdbv1.Security{
					ManagedCertificate: &mdbv1.ManagedCertificate{},
					Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM}},
				}
				var external client.Object
				var ownerLabels map[string]string
				switch kind {
				case omv1.ExternalAppDBRefKindMongoDB:
					db := validExternalAppDBMongoDB()
					db.Spec.Security = security
					external = db
					ownerLabels = db.GetOwnerLabels()
				case omv1.ExternalAppDBRefKindMongoDBMultiCluster:
					db := validExternalAppDBMongoDBMultiCluster()
					db.Spec.Security = security
					external = db
					ownerLabels = db.GetOwnerLabels()
				}
				external.SetUID(types.UID("external-appdb-uid"))
				require.NoError(t, central.Create(ctx, external))
				ca, _ := managedOMTestCA(t)
				bundle := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-om-db-managed-server-ca-bundle", Namespace: testOm.Namespace, UID: types.UID("bundle-uid"),
						Labels:          ownerLabels,
						OwnerReferences: []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: kind, Name: "test-om-db", UID: external.GetUID(), Controller: new(true)}},
					},
					Data: map[string]string{"ca-pem": string(ca)},
				}
				require.NoError(t, central.Create(ctx, bundle))
				if topology == mdbv1.ClusterTopologyMultiCluster {
					memberClients["om-cluster-1"] = central
					sharedBundle := bundle.DeepCopy()
					sharedBundle.ResourceVersion = ""
					sharedBundle.UID = ""
					sharedBundle.OwnerReferences = nil
					sharedBundle.Annotations = map[string]string{"appdb-managed": "true"}
					require.NoError(t, memberClients["om-cluster-2"].Create(ctx, sharedBundle))
				}
				leaf := &certmanagerv1.Certificate{ObjectMeta: metav1.ObjectMeta{Name: "test-om-db-managed-cert", Namespace: testOm.Namespace, OwnerReferences: bundle.OwnerReferences}}
				require.NoError(t, central.Create(ctx, leaf))
				require.NoError(t, central.CreateSecret(ctx, secret.Builder().SetName("test-om-db-om-password").SetNamespace(testOm.Namespace).SetField(util.OpsManagerPasswordKey, "test-password").Build()))

				cfg, err := reconciler.createNewExternalAppDBReconciler(zap.S()).GetAppDBConfig(ctx, testOm, zap.S())
				require.NoError(t, err)
				assert.True(t, cfg.IsTLSEnabled)
				assert.Contains(t, cfg.ConnectionString, "ssl=true")
				assert.Equal(t, "test-om-db-managed-server-ca-bundle", cfg.CAConfigMapName)
				helper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, memberClients, zap.S())
				require.NoError(t, err)
				require.NoError(t, reconciler.replicateAppDBTLSCAInMemberClusters(ctx, helper, cfg))
				for _, member := range helper.getHealthyMemberClusters() {
					omSts, err := reconciler.createOpsManagerStatefulsetInMemberCluster(ctx, helper, cfg, member, "init-image", "om-image", zap.S())
					require.NoError(t, err)
					backupSts, err := reconciler.createBackupDaemonStatefulset(ctx, helper, cfg, member, "init-image", "om-image", zap.S())
					require.NoError(t, err)
					for _, sts := range []*appsv1.StatefulSet{omSts, backupSts} {
						assert.True(t, slices.ContainsFunc(sts.Spec.Template.Spec.Volumes, func(v corev1.Volume) bool {
							return v.Name == "appdb-ca-certificate" && v.ConfigMap != nil && v.ConfigMap.Name == "test-om-db-managed-server-ca-bundle"
						}))
						assert.Contains(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "appdb-ca-certificate", MountPath: util.AppDBMmsCaFileDirInContainer})
					}
					copied, err := member.Client.GetConfigMap(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db-managed-server-ca-bundle"))
					require.NoError(t, err)
					assert.Equal(t, bundle.Data, copied.Data)
					if copied.UID == bundle.UID {
						assert.Equal(t, ownerLabels, copied.Labels, "OM must not overwrite the central source owned by the external database")
						assert.Equal(t, bundle.OwnerReferences, copied.OwnerReferences)
					} else {
						assert.Equal(t, testOm.GetOwnerLabels(), copied.Labels, "new OM-only copies retain OM's cleanup labels")
						assert.Empty(t, copied.OwnerReferences, "owner references must not cross cluster boundaries")
					}
				}
				assert.Equal(t, "true", testOm.Spec.Configuration[util.MmsMongoSSL])
				assert.Equal(t, omv1.GetAppDBCaPemPath(), testOm.Spec.Configuration[util.MmsMongoCA])
				assert.Equal(t, "om-https-ca", testOm.GetOpsManagerCA())
				assert.True(t, reconciler.ensureAppDBCertAndCA(ctx, testOm, zap.S()).status.IsOK())
				certificates := &certmanagerv1.CertificateList{}
				require.NoError(t, central.List(ctx, certificates, client.InNamespace(testOm.Namespace)))
				require.Len(t, certificates.Items, 1, "OM must not issue external AppDB certificates")
				assert.Equal(t, leaf.OwnerReferences, certificates.Items[0].OwnerReferences)
				persisted, err := central.GetConfigMap(ctx, kube.ObjectKey(testOm.Namespace, bundle.Name))
				require.NoError(t, err)
				assert.Equal(t, bundle.Data, persisted.Data)
				assert.Equal(t, bundle.OwnerReferences, persisted.OwnerReferences)
				assert.Equal(t, ownerLabels, persisted.Labels)
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
					Replicas: new(int32(3)),
				},
			}

			omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
			reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
			require.NoError(t, reconciler.client.Create(ctx, mdb))
			require.NoError(t, reconciler.client.Create(ctx, &sts))

			ext := reconciler.createNewExternalAppDBReconciler(zap.S())
			externalAppDB, err := ext.getExternalAppDBReference(ctx, testOm)
			require.NoError(t, err)
			st := ext.ensureAppDBStatefulSetOwnership(ctx, testOm, externalAppDB)
			require.True(t, st.IsOK())

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
