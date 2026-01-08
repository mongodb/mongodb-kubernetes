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
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

type MongoDBSearchReconciler struct {
	kubeClient           kubernetesClient.Client
	watch                *watch.ResourceWatcher
	operatorSearchConfig searchcontroller.OperatorSearchConfig
}

func newMongoDBSearchReconciler(client client.Client, operatorSearchConfig searchcontroller.OperatorSearchConfig) *MongoDBSearchReconciler {
	return &MongoDBSearchReconciler{
		kubeClient:           kubernetesClient.NewClient(client),
		watch:                watch.NewResourceWatcher(),
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

	searchSource, err := r.getSourceMongoDBForSearch(ctx, r.kubeClient, mdbSearch, log)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, err
	}

	if mdbSearch.IsWireprotoEnabled() {
		log.Info("Enabling the mongot wireproto server as required by annotation")
		// the keyfile secret is necessary for wireproto authentication
		r.watch.AddWatchedResourceIfNotAdded(searchSource.KeyfileSecretName(), mdbSearch.Namespace, watch.Secret, mdbSearch.NamespacedName())
	}

	// Watch for changes in database source CA certificate secrets or configmaps
	tlsSourceConfig := searchSource.TLSConfig()
	if tlsSourceConfig != nil {
		for wType, resources := range tlsSourceConfig.ResourcesToWatch {
			for _, resource := range resources {
				r.watch.AddWatchedResourceIfNotAdded(resource.Name, resource.Namespace, wType, mdbSearch.NamespacedName())
			}
		}
	}

	// Watch our own TLS certificate secret for changes
	if mdbSearch.Spec.Security.TLS != nil {
		r.watch.AddWatchedResourceIfNotAdded(mdbSearch.Spec.Security.TLS.CertificateKeySecret.Name, mdbSearch.Namespace, watch.Secret, mdbSearch.NamespacedName())
	}

	if mdbSearch.Spec.AutoEmbedding.EmbeddingModelAPIKeySecret != nil {
		r.watch.AddWatchedResourceIfNotAdded(mdbSearch.Spec.AutoEmbedding.EmbeddingModelAPIKeySecret.Name, mdbSearch.Namespace, watch.Secret, mdbSearch.NamespacedName())
	}

	reconcileHelper := searchcontroller.NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(r.kubeClient), mdbSearch, searchSource, r.operatorSearchConfig)

	return reconcileHelper.Reconcile(ctx, log).ReconcileResult()
}

func (r *MongoDBSearchReconciler) getSourceMongoDBForSearch(ctx context.Context, kubeClient client.Client, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (searchcontroller.SearchSourceDBResource, error) {
	// Resolve the source database for this Search instance.
	// If .spec.source.external is defined immediately return the external search source.
	// Otherwise, read .spec.source.mongodbResourceRef or use the implicit database resource name (same as the Search resource's name).
	// Try to get a MongoDB CR with the computed name and return the enterprise search source if successful.
	// Otherwise, try to get a MongoDBCommunity CR with the same name and return the community search source.
	// If everything fails just error out and the controller will retry reconciliation.

	if search.IsExternalMongoDBSource() {
		return searchcontroller.NewExternalSearchSource(search.Namespace, search.Spec.Source.ExternalMongoDBSource), nil
	}

	sourceMongoDBResourceRef := search.GetMongoDBResourceRef()
	if sourceMongoDBResourceRef == nil {
		return nil, xerrors.New("MongoDBSearch source MongoDB resource reference is not set")
	}

	sourceName := types.NamespacedName{Namespace: search.GetNamespace(), Name: sourceMongoDBResourceRef.Name}
	log.Infof("Looking up Search source %s", sourceName)

	mdb := &mdbv1.MongoDB{}
	if err := kubeClient.Get(ctx, sourceName, mdb); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, xerrors.Errorf("error getting MongoDB %s: %w", sourceName, err)
		}
	} else {
		r.watch.AddWatchedResourceIfNotAdded(sourceMongoDBResourceRef.Name, sourceMongoDBResourceRef.Namespace, watch.MongoDB, search.NamespacedName())
		return searchcontroller.NewEnterpriseResourceSearchSource(mdb), nil
	}

	mdbc := &mdbcv1.MongoDBCommunity{}
	if err := kubeClient.Get(ctx, sourceName, mdbc); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, xerrors.Errorf("error getting MongoDBCommunity %s: %w", sourceName, err)
		}
	} else {
		r.watch.AddWatchedResourceIfNotAdded(sourceMongoDBResourceRef.Name, sourceMongoDBResourceRef.Namespace, "MongoDBCommunity", search.NamespacedName())
		return searchcontroller.NewCommunityResourceSearchSource(mdbc), nil
	}

	return nil, xerrors.Errorf("No database resource named %s found", sourceName)
}

func mdbcSearchIndexBuilder(rawObj client.Object) []string {
	mdbSearch := rawObj.(*searchv1.MongoDBSearch)
	resourceRef := mdbSearch.GetMongoDBResourceRef()
	if resourceRef == nil {
		return []string{}
	}

	return []string{resourceRef.Namespace + "/" + resourceRef.Name}
}

func AddMongoDBSearchController(ctx context.Context, mgr manager.Manager, operatorSearchConfig searchcontroller.OperatorSearchConfig) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &searchv1.MongoDBSearch{}, searchcontroller.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder); err != nil {
		return err
	}

	r := newMongoDBSearchReconciler(kubernetesClient.NewClient(mgr.GetClient()), operatorSearchConfig)

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		For(&searchv1.MongoDBSearch{}).
		Watches(&mdbv1.MongoDB{}, &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch}).
		Watches(&mdbcv1.MongoDBCommunity{}, &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch}).
		Watches(&corev1.Secret{}, &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch}).
		Watches(&corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
