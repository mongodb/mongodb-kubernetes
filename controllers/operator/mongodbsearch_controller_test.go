package operator

import (
	"context"
	"fmt"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/search_controller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
)

func newMongoDBCommunity(name, namespace string) *mdbcv1.MongoDBCommunity {
	return &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mdbcv1.MongoDBCommunitySpec{
			Type:    mdbcv1.ReplicaSet,
			Members: 1,
			Version: "8.0",
		},
	}
}

func newMongoDBSearch(name, namespace, mdbcName string) *searchv1.MongoDBSearch {
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{Name: mdbcName},
			},
		},
	}
}

func newSearchReconciler(
	mdbc *mdbcv1.MongoDBCommunity,
	searches ...*searchv1.MongoDBSearch,
) (*MongoDBSearchReconciler, client.Client) {
	builder := mock.NewEmptyFakeClientBuilder()
	builder.WithIndex(&searchv1.MongoDBSearch{}, search_controller.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder)

	if mdbc != nil {
		builder.WithObjects(mdbc)
	}

	for _, search := range searches {
		if search != nil {
			builder.WithObjects(search)
		}
	}

	fakeClient := builder.Build()
	return newMongoDBSearchReconciler(fakeClient, search_controller.OperatorSearchConfig{}), fakeClient
}

func buildExpectedMongotConfig(search *searchv1.MongoDBSearch, mdbc *mdbcv1.MongoDBCommunity) mongot.Config {
	return mongot.Config{CommunityPrivatePreview: mongot.CommunityPrivatePreview{
		MongodHostAndPort: fmt.Sprintf(
			"%s.%s.svc.cluster.local:%d",
			mdbc.ServiceName(), mdbc.Namespace,
			mdbc.GetMongodConfiguration().GetDBPort(),
		),
		QueryServerAddress: fmt.Sprintf("localhost:%d", search.GetMongotPort()),
		KeyFilePath:        "/mongot/keyfile/keyfile",
		DataPath:           "/mongot/data/config.yml",
		Metrics: mongot.Metrics{
			Enabled: true,
			Address: fmt.Sprintf("localhost:%d", search.GetMongotMetricsPort()),
		},
		Logging: mongot.Logging{Verbosity: "DEBUG"},
	}}
}

func TestMongoDBSearchReconcile_NotFound(t *testing.T) {
	ctx := context.Background()
	reconciler, _ := newSearchReconciler(nil, nil)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "missing", Namespace: "test"}},
	)

	assert.Error(t, err)
	assert.True(t, apiErrors.IsNotFound(err))
	assert.Equal(t, reconcile.Result{}, res)
}

func TestMongoDBSearchReconcile_MissingSource(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "source")
	reconciler, _ := newSearchReconciler(nil, search)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)

	assert.Error(t, err)
	assert.True(t, res.RequeueAfter > 0)
}

func TestMongoDBSearchReconcile_Success(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)
	expected, _ := workflow.OK().ReconcileResult()
	assert.NoError(t, err)
	assert.Equal(t, expected, res)

	svc := &corev1.Service{}
	err = c.Get(ctx, search.SearchServiceNamespacedName(), svc)
	assert.NoError(t, err)

	cm := &corev1.ConfigMap{}
	err = c.Get(ctx, search.MongotConfigConfigMapNamespacedName(), cm)
	assert.NoError(t, err)
	expectedConfig := buildExpectedMongotConfig(search, mdbc)
	configYaml, err := yaml.Marshal(expectedConfig)
	assert.NoError(t, err)
	assert.Equal(t, string(configYaml), cm.Data["config.yml"])

	sts := &appsv1.StatefulSet{}
	err = c.Get(ctx, search.StatefulSetNamespacedName(), sts)
	assert.NoError(t, err)

	queue := controllertest.Queue{Interface: workqueue.New()}
	reconciler.mdbcWatcher.Create(ctx, event.CreateEvent{Object: mdbc}, &queue)
	assert.Equal(t, 1, queue.Len())
}

func checkSearchReconcileFailed(
	ctx context.Context,
	t *testing.T,
	reconciler *MongoDBSearchReconciler,
	c client.Client,
	search *searchv1.MongoDBSearch,
	expectedMsg string,
) {
	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)
	assert.NoError(t, err)
	assert.True(t, res.RequeueAfter > 0)

	updated := &searchv1.MongoDBSearch{}
	assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updated))
	assert.Equal(t, status.PhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, expectedMsg)
}

func TestMongoDBSearchReconcile_InvalidVersion(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	mdbc.Spec.Version = "6.0"
	reconciler, c := newSearchReconciler(mdbc, search)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search, "MongoDB version")
}

func TestMongoDBSearchReconcile_TLSNotSupported(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	mdbc.Spec.Security.TLS.Enabled = true
	reconciler, c := newSearchReconciler(mdbc, search)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search, "TLS-enabled")
}

func TestMongoDBSearchReconcile_MultipleSearchResources(t *testing.T) {
	ctx := context.Background()
	search1 := newMongoDBSearch("search1", mock.TestNamespace, "mdb")
	search2 := newMongoDBSearch("search2", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search1, search2)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search1, "multiple MongoDBSearch")
}

func TestMongoDBSearchReconcile_InvalidSearchImageVersion(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	search.Spec.Version = "1.47.0"
	reconciler, c := newSearchReconciler(mdbc, search)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search, "MongoDBSearch version 1.47.0 is not supported")
}
