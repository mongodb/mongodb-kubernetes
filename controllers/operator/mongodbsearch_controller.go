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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/search_controller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/watch"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

type MongoDBSearchReconciler struct {
	kubeClient           kubernetesClient.Client
	mdbcWatcher          watch.ResourceWatcher
	mdbWatcher           watch.ResourceWatcher
	secretWatcher        watch.ResourceWatcher
	operatorSearchConfig search_controller.OperatorSearchConfig
}

func newMongoDBSearchReconciler(client client.Client, operatorSearchConfig search_controller.OperatorSearchConfig) *MongoDBSearchReconciler {
	return &MongoDBSearchReconciler{
		kubeClient:           kubernetesClient.NewClient(client),
		mdbcWatcher:          watch.New(),
		mdbWatcher:           watch.New(),
		secretWatcher:        watch.New(),
		operatorSearchConfig: operatorSearchConfig,
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status,mongodbsearch/finalizers},verbs=*,namespace=placeholder
func (r *MongoDBSearchReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBSearch", request.NamespacedName)
	log.Info("-> MongoDBSearch.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, mdbSearch, log); err != nil {
		return result, err
	}

	sourceResource, err := r.getSourceMongoDBForSearch(ctx, r.kubeClient, mdbSearch, log)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, err
	}
	r.secretWatcher.Watch(ctx, kube.ObjectKey(sourceResource.GetNamespace(), sourceResource.KeyfileSecretName()), mdbSearch.NamespacedName())

	reconcileHelper := search_controller.NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(r.kubeClient), mdbSearch, sourceResource, r.operatorSearchConfig)

	return reconcileHelper.Reconcile(ctx, log).ReconcileResult()
}

func (r *MongoDBSearchReconciler) getSourceMongoDBForSearch(ctx context.Context, kubeClient client.Client, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (search_controller.SearchSourceDBResource, error) {
	sourceMongoDBResourceRef := search.GetMongoDBResourceRef()
	sourceName := types.NamespacedName{Namespace: search.GetNamespace(), Name: sourceMongoDBResourceRef.Name}
	log.Infof("Looking up Search source %s", sourceName)

	mdb := &mdbv1.MongoDB{}
	if err := kubeClient.Get(ctx, sourceName, mdb); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, xerrors.Errorf("error getting MongoDB %s: %w", sourceName, err)
		}
	} else {
		r.mdbWatcher.Watch(ctx, sourceName, search.NamespacedName())
		return search_controller.NewEnterpriseResourceSearchSource(mdb), nil
	}

	mdbc := &mdbcv1.MongoDBCommunity{}
	if err := kubeClient.Get(ctx, sourceName, mdbc); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, xerrors.Errorf("error getting MongoDBCommunity %s: %w", sourceName, err)
		}
	} else {
		r.mdbcWatcher.Watch(ctx, sourceName, search.NamespacedName())
		return search_controller.NewCommunityResourceSearchSource(mdbc), nil
	}

	return nil, xerrors.Errorf("No database resource named %s found", sourceName)
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
		Watches(&mdbv1.MongoDB{}, r.mdbWatcher).
		Watches(&mdbcv1.MongoDBCommunity{}, r.mdbcWatcher).
		Watches(&corev1.Secret{}, r.secretWatcher).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
