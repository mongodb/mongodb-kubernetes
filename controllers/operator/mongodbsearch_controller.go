package operator

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// preGACRReason is the event reason emitted when a pre-GA MongoDBSearch CR (missing
// spec.clusters) is detected. It is intentionally a Warning so it surfaces in kubectl events.
const preGACRReason = "PreGACR"

type MongoDBSearchReconciler struct {
	kubeClient           kubernetesClient.Client
	watch                *watch.ResourceWatcher
	operatorSearchConfig searchcontroller.OperatorSearchConfig
	recorder             record.EventRecorder
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

	// Pre-GA gate: spec.clusters is required at GA. CRs without it are from a pre-GA schema
	// and cannot be reconciled. Emit a Warning event so the user knows to migrate, then no-op.
	// Status is intentionally left unchanged — do not set Failed, which would mislead the user
	// into thinking reconcile ran and failed rather than deliberately skipped.
	if len(mdbSearch.Spec.Clusters) == 0 {
		log.Warn("Pre-GA MongoDBSearch CR detected: spec.clusters is empty, skipping reconcile")
		if r.recorder != nil {
			r.recorder.Event(mdbSearch, corev1.EventTypeWarning, preGACRReason,
				"Pre-GA MongoDBSearch CR detected: spec.clusters is required at GA. "+
					"The pre-GA top-level fields spec.replicas, spec.resourceRequirements, "+
					"spec.persistence, and spec.statefulSet have been removed; migrate "+
					"them to spec.clusters[i].* and re-apply.")
		}
		return reconcile.Result{}, nil
	}

	searchSource, err := r.getSourceMongoDBForSearch(ctx, r.kubeClient, mdbSearch, log)
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, workflow.Failed(xerrors.Errorf("Waiting for MongoDB source: %s", err)), log)
	}

	stateStore := NewStateStore[searchcontroller.MongoDBSearchDeploymentState](mdbSearch, kube.BaseOwnerReference(mdbSearch), r.kubeClient)
	reconcileHelper, err := searchcontroller.NewMongoDBSearchReconcileHelper(ctx, kubernetesClient.NewClient(r.kubeClient), mdbSearch, searchSource, r.operatorSearchConfig, stateStore, log)
	if err != nil {
		return reconcile.Result{}, xerrors.Errorf("failed to initialize search reconcile helper: %w", err)
	}

	// Phase 1 is single-cluster only: always use the first (and only) cluster's name.
	// GetFirstCluster() is safe here because P1.5 admission ensures spec.clusters is non-empty.
	clusterIndex := reconcileHelper.ClusterIndexFor(mdbSearch.GetFirstCluster().ClusterName)

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
		if shardedSource, ok := searchSource.(searchcontroller.SearchSourceShardedDeployment); ok {
			// Sharded: watch per-shard source secrets (one per shard)
			for _, shardName := range shardedSource.GetShardNames() {
				shardSecretNsName := mdbSearch.TLSSecretForShard(clusterIndex, shardName)
				r.watch.AddWatchedResourceIfNotAdded(shardSecretNsName.Name, shardSecretNsName.Namespace, watch.Secret, mdbSearch.NamespacedName())
			}
		} else {
			// Non-sharded: watch the single source secret
			sourceSecretNsName := mdbSearch.TLSSecretNamespacedName(clusterIndex)
			r.watch.AddWatchedResourceIfNotAdded(sourceSecretNsName.Name, sourceSecretNsName.Namespace, watch.Secret, mdbSearch.NamespacedName())
		}
	}

	if mdbSearch.Spec.AutoEmbedding != nil {
		r.watch.AddWatchedResourceIfNotAdded(mdbSearch.Spec.AutoEmbedding.EmbeddingModelAPIKeySecret.Name, mdbSearch.Namespace, watch.Secret, mdbSearch.NamespacedName())
	}

	return reconcileHelper.Reconcile(ctx, log).ReconcileResult()
}

func (r *MongoDBSearchReconciler) getSourceMongoDBForSearch(ctx context.Context, kubeClient client.Client, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (searchcontroller.SearchSourceDBResource, error) {
	return getSearchSource(ctx, kubeClient, r.watch, search, log)
}

// getSearchSource resolves the source database for a MongoDBSearch resource.
// Shared by both the main search controller and the Envoy controller.
func getSearchSource(ctx context.Context, kubeClient client.Client, watcher *watch.ResourceWatcher, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (searchcontroller.SearchSourceDBResource, error) {
	if search.IsExternalMongoDBSource() {
		externalSpec := search.Spec.Source.ExternalMongoDBSource
		if search.IsExternalSourceSharded() {
			return searchcontroller.NewShardedExternalSearchSource(search.Namespace, externalSpec), nil
		}
		return searchcontroller.NewExternalSearchSource(search.Namespace, externalSpec), nil
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
		watcher.AddWatchedResourceIfNotAdded(sourceMongoDBResourceRef.Name, sourceMongoDBResourceRef.Namespace, watch.MongoDB, search.NamespacedName())
		if mdb.GetResourceType() == mdbv1.ShardedCluster {
			return searchcontroller.NewShardedInternalSearchSource(mdb, search), nil
		}
		return searchcontroller.NewEnterpriseResourceSearchSource(mdb), nil
	}

	mdbc := &mdbcv1.MongoDBCommunity{}
	if err := kubeClient.Get(ctx, sourceName, mdbc); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, xerrors.Errorf("error getting MongoDBCommunity %s: %w", sourceName, err)
		}
	} else {
		watcher.AddWatchedResourceIfNotAdded(sourceMongoDBResourceRef.Name, sourceMongoDBResourceRef.Namespace, "MongoDBCommunity", search.NamespacedName())
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
	if err := mgr.GetFieldIndexer().IndexField(ctx, &searchv1.MongoDBSearch{}, searchv1.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder); err != nil {
		return err
	}

	r := newMongoDBSearchReconciler(kubernetesClient.NewClient(mgr.GetClient()), operatorSearchConfig)
	r.recorder = mgr.GetEventRecorderFor("mongodbsearch")

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		For(&searchv1.MongoDBSearch{}).
		Watches(&mdbv1.MongoDB{}, &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch}).
		Watches(&mdbcv1.MongoDBCommunity{}, &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch}).
		Watches(&corev1.Secret{}, &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch}).
		Watches(&corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
