package operator

import (
	"context"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/search_controller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/watch"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

type MongoDBSearchReconciler struct {
	kubeClient           kubernetesClient.Client
	mdbcWatcher          *watch.ResourceWatcher
	operatorSearchConfig search_controller.OperatorSearchConfig
}

func newMongoDBSearchReconciler(client client.Client, operatorSearchConfig search_controller.OperatorSearchConfig) *MongoDBSearchReconciler {
	mdbcWatcher := watch.New()
	return &MongoDBSearchReconciler{
		kubeClient:           kubernetesClient.NewClient(client),
		mdbcWatcher:          &mdbcWatcher,
		operatorSearchConfig: operatorSearchConfig,
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status,mongodbsearch/finalizers},verbs=*,namespace=placeholder
func (r *MongoDBSearchReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBSearch", request.NamespacedName)
	log.Info("-> MongoDBSearch.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if _, err := commoncontroller.GetResource(ctx, r.kubeClient, request, mdbSearch, log); err != nil {
		log.Warnf("Error getting MongoDBSearch %s", request.NamespacedName)
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, nil
	}

	sourceResource, err := getSourceMongoDBForSearch(ctx, r.kubeClient, mdbSearch)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, nil
	}

	r.mdbcWatcher.Watch(ctx, sourceResource.NamespacedName(), request.NamespacedName)

	reconcileHelper := search_controller.NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(r.kubeClient), mdbSearch, sourceResource, r.operatorSearchConfig)
	return reconcileHelper.Reconcile(ctx, log).ReconcileResult()
}

func getSourceMongoDBForSearch(ctx context.Context, kubeClient client.Client, search *searchv1.MongoDBSearch) (construct.SearchSourceDBResource, error) {
	sourceMongoDBResourceRef := search.GetMongoDBResourceRef()
	mdbcName := types.NamespacedName{Namespace: search.GetNamespace(), Name: sourceMongoDBResourceRef.Name}
	mdbc := &mdbcv1.MongoDBCommunity{}
	if err := kubeClient.Get(ctx, mdbcName, mdbc); err != nil {
		return nil, xerrors.Errorf("error getting MongoDBCommunity %s", mdbcName)
	}
	return construct.NewSearchSourceDBResourceFromMongoDBCommunity(mdbc), nil
}

func mdbcSearchIndexBuilder(rawObj client.Object) []string {
	mdbSearch := rawObj.(*searchv1.MongoDBSearch)
	return []string{mdbSearch.GetMongoDBResourceRef().Namespace + "/" + mdbSearch.GetMongoDBResourceRef().Name}
}

func AddMongoDBSearchController(ctx context.Context, mgr manager.Manager, operatorSearchConfig search_controller.OperatorSearchConfig) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &searchv1.MongoDBSearch{}, search_controller.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder); err != nil {
		return err
	}

	r := newMongoDBSearchReconciler(kubernetesClient.NewClient(mgr.GetClient()), operatorSearchConfig)

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		For(&searchv1.MongoDBSearch{}).
		Watches(&mdbcv1.MongoDBCommunity{}, r.mdbcWatcher).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}
