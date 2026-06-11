package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster/memberwatch"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// secretsCheckRequeueAfter is the requeue interval used when CheckSecretsPresence
// reports any per-cluster customer-replicated secret missing. Reconcile returns
// (Result{RequeueAfter: secretsCheckRequeueAfter}, nil) so we don't trigger
// exponential backoff while the customer is fixing the gap.
const secretsCheckRequeueAfter = 30 * time.Second

type SearchDeploymentState struct {
	CommonDeploymentState `json:",inline"`
	// RoutingReadyMongotGroups is the one-way latch of shard names whose mongot
	// group has EVER met the routing-readiness threshold; a shard is pending iff
	// it is not listed here. Pruned only when a shard no longer exists.
	RoutingReadyMongotGroups []string `json:"routingReadyMongotGroups,omitempty"`
}

func NewSearchDeploymentState() *SearchDeploymentState {
	return &SearchDeploymentState{
		CommonDeploymentState: CommonDeploymentState{ClusterMapping: map[string]int{}},
	}
}

// loadOrInitSearchState reads the per-CR state ConfigMap, treating NotFound as
// fresh state.
func loadOrInitSearchState(
	ctx context.Context,
	c kubernetesClient.Client,
	search *searchv1.MongoDBSearch,
) (*SearchDeploymentState, error) {
	store := NewStateStore[SearchDeploymentState](search, kube.BaseOwnerReference(search), c)
	state, err := store.ReadState(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return NewSearchDeploymentState(), nil
		}
		return nil, err
	}
	if state.ClusterMapping == nil {
		state.ClusterMapping = map[string]int{}
	}
	return state, nil
}

// mutateSearchState performs a resourceVersion-checked read-modify-write of the
// search state ConfigMap: a stale base yields 409 Conflict and the reconcile
// requeues, instead of silently losing a concurrent write (do NOT replace this
// with configmap.CreateOrUpdate — that is a blind no-RV Update). mutate returns
// true when the state changed and must be persisted.
func mutateSearchState(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, mutate func(*SearchDeploymentState) bool) (*SearchDeploymentState, error) {
	cmName := fmt.Sprintf("%s-state", search.Name)
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, kube.ObjectKey(search.Namespace, cmName), cm)
	if apierrors.IsNotFound(err) {
		state := NewSearchDeploymentState()
		if !mutate(state) {
			return state, nil
		}
		data, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		newCM := configmap.Builder().
			SetName(cmName).
			SetNamespace(search.Namespace).
			SetLabels(search.GetOwnerLabels()).
			SetOwnerReferences(kube.BaseOwnerReference(search)).
			SetDataField(stateKey, string(data)).
			Build()
		return state, c.Create(ctx, &newCM)
	} else if err != nil {
		return nil, err
	}

	state := NewSearchDeploymentState()
	if raw, ok := cm.Data[stateKey]; ok {
		if err := json.Unmarshal([]byte(raw), state); err != nil {
			return nil, xerrors.Errorf("cannot unmarshal search state %s/%s: %w", search.Namespace, cmName, err)
		}
	}
	if state.ClusterMapping == nil {
		state.ClusterMapping = map[string]int{}
	}
	if !mutate(state) {
		return state, nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[stateKey] = string(data)
	// Update on the Get result carries its resourceVersion — stale base → Conflict.
	return state, c.Update(ctx, cm)
}

// searchRoutingLatch implements searchcontroller.RoutingReadyLatch on the search
// state ConfigMap. state is the snapshot loaded at reconcile start, refreshed on
// every successful write.
type searchRoutingLatch struct {
	client kubernetesClient.Client
	search *searchv1.MongoDBSearch
	state  *SearchDeploymentState
}

func (l *searchRoutingLatch) IsRoutingReady(shardName string) bool {
	return slices.Contains(l.state.RoutingReadyMongotGroups, shardName)
}

func (l *searchRoutingLatch) MarkRoutingReady(ctx context.Context, shardName string) error {
	state, err := mutateSearchState(ctx, l.client, l.search, func(s *SearchDeploymentState) bool {
		if slices.Contains(s.RoutingReadyMongotGroups, shardName) {
			return false
		}
		s.RoutingReadyMongotGroups = append(s.RoutingReadyMongotGroups, shardName)
		return true
	})
	if err != nil {
		return err
	}
	l.state = state
	return nil
}

func (l *searchRoutingLatch) PruneRoutingReady(ctx context.Context, liveShardNames []string) error {
	state, err := mutateSearchState(ctx, l.client, l.search, func(s *SearchDeploymentState) bool {
		pruned := slices.DeleteFunc(slices.Clone(s.RoutingReadyMongotGroups), func(name string) bool {
			return !slices.Contains(liveShardNames, name)
		})
		if len(pruned) == len(s.RoutingReadyMongotGroups) {
			return false
		}
		s.RoutingReadyMongotGroups = pruned
		return true
	})
	if err != nil {
		return err
	}
	l.state = state
	return nil
}

type MongoDBSearchReconciler struct {
	kubeClient           kubernetesClient.Client
	watch                *watch.ResourceWatcher
	operatorSearchConfig searchcontroller.OperatorSearchConfig

	memberClusterClientsMap map[string]kubernetesClient.Client // per-cluster Kubernetes client; empty in single-cluster installs
}

func newMongoDBSearchReconciler(
	kubeClient client.Client,
	operatorSearchConfig searchcontroller.OperatorSearchConfig,
	memberClustersMap map[string]client.Client,
) *MongoDBSearchReconciler {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClustersMap))
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	return &MongoDBSearchReconciler{
		kubeClient:              kubernetesClient.NewClient(kubeClient),
		watch:                   watch.NewResourceWatcher(),
		operatorSearchConfig:    operatorSearchConfig,
		memberClusterClientsMap: clientsMap,
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status},verbs=*,namespace=placeholder
func (r *MongoDBSearchReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBSearch", request.NamespacedName)
	log.Info("-> MongoDBSearch.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, mdbSearch, log); err != nil {
		return result, err
	}

	// Short-circuit: the disable-reconciliation annotation allows to
	// pause reconciliation on a single CR so owned objects can be mutated
	// without the operator reverting them.
	// Useful for tests when the operator is running locally and not in the pod.
	if mdbSearch.GetAnnotations()[searchv1.DisableReconciliationAnnotation] == "true" {
		log.Infof("MongoDBSearch %s/%s reconciliation disabled by %s annotation; skipping",
			mdbSearch.GetNamespace(), mdbSearch.GetName(), searchv1.DisableReconciliationAnnotation)
		return reconcile.Result{}, nil
	}

	searchSource, err := r.getSourceMongoDBForSearch(ctx, r.kubeClient, mdbSearch, log)
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, workflow.Failed(xerrors.Errorf("Waiting for MongoDB source: %s", err)), log)
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

	// Watch our own TLS certificate secret for changes (non-sharded only; sharded watches are per-member-cluster)
	if mdbSearch.Spec.Security.TLS != nil {
		if _, ok := searchSource.(searchcontroller.SearchSourceShardedDeployment); !ok {
			// Non-sharded: watch the single source secret
			sourceSecretNsName := mdbSearch.TLSSecretNamespacedName()
			r.watch.AddWatchedResourceIfNotAdded(sourceSecretNsName.Name, sourceSecretNsName.Namespace, watch.Secret, mdbSearch.NamespacedName())
		}
	}

	if mdbSearch.Spec.AutoEmbedding != nil {
		r.watch.AddWatchedResourceIfNotAdded(mdbSearch.Spec.AutoEmbedding.EmbeddingModelAPIKeySecret.Name, mdbSearch.Namespace, watch.Secret, mdbSearch.NamespacedName())
	}

	var currentNames []string
	for _, c := range mdbSearch.Spec.Clusters {
		currentNames = append(currentNames, c.ClusterName)
	}
	state, err := mutateSearchState(ctx, r.kubeClient, mdbSearch, func(s *SearchDeploymentState) bool {
		newMapping := searchv1.AssignClusterIndices(s.ClusterMapping, currentNames)
		if reflect.DeepEqual(newMapping, s.ClusterMapping) {
			return false
		}
		s.ClusterMapping = newMapping
		return true
	})
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, workflow.Failed(err), log)
	}

	routingLatch := &searchRoutingLatch{client: r.kubeClient, search: mdbSearch, state: state}
	reconcileHelper := searchcontroller.NewMongoDBSearchReconcileHelper(r.kubeClient, mdbSearch, searchSource, r.operatorSearchConfig, r.memberClusterClientsMap, state.ClusterMapping, routingLatch)

	result, err := reconcileHelper.Reconcile(ctx, log).ReconcileResult()
	if err != nil {
		return result, err
	}

	// Diagnostic pass for secrets reconcile doesn't gate on with Pending. Skip
	// when reconcile already requeued — its own gates cover that case.
	if result.RequeueAfter == 0 {
		memberClients := make(map[string]client.Client, len(r.memberClusterClientsMap))
		for name, kc := range r.memberClusterClientsMap {
			memberClients[name] = kc
		}
		if gaps := searchcontroller.CheckSecretsPresence(ctx, mdbSearch, r.kubeClient, memberClients, state.ClusterMapping); len(gaps) > 0 {
			r.surfaceMissingSecrets(gaps, log)
			result.RequeueAfter = secretsCheckRequeueAfter
		}
	}
	return result, nil
}

// surfaceMissingSecrets logs one entry per cluster that has gaps. The reconcile
// loop returns RequeueAfter so the controller waits without exponential backoff
// while the customer replicates the missing secrets.
func (r *MongoDBSearchReconciler) surfaceMissingSecrets(
	gaps []searchcontroller.SecretCheckResult,
	log *zap.SugaredLogger,
) {
	for _, g := range gaps {
		clusterLabel := g.Cluster
		if clusterLabel == "" {
			clusterLabel = "central"
		}
		log.Infow("MongoDBSearch missing customer-replicated secrets",
			"cluster", clusterLabel,
			"missing", g.Missing,
		)
	}
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

func AddMongoDBSearchController(
	ctx context.Context,
	mgr manager.Manager,
	operatorSearchConfig searchcontroller.OperatorSearchConfig,
	memberClusterObjectsMap map[string]cluster.Cluster,
) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &searchv1.MongoDBSearch{}, searchv1.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder); err != nil {
		return err
	}

	r := newMongoDBSearchReconciler(
		mgr.GetClient(),
		operatorSearchConfig,
		multicluster.ClustersMapToClientMap(memberClusterObjectsMap),
	)

	c, err := controller.New(util.MongoDbSearchController, mgr, controller.Options{
		Reconciler:              r,
		MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1), // nolint:forbidigo
	})
	if err != nil {
		return err
	}

	ownerHandler := handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &searchv1.MongoDBSearch{}, handler.OnlyControllerOwner())
	centralWatches := []struct {
		obj     client.Object
		handler handler.EventHandler
	}{
		{&searchv1.MongoDBSearch{}, &handler.EnqueueRequestForObject{}},
		{&mdbv1.MongoDB{}, &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch}},
		{&mdbcv1.MongoDBCommunity{}, &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch}},
		{&corev1.Secret{}, &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch}},
		{&corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch}},
		{&appsv1.StatefulSet{}, ownerHandler},
		{&corev1.Service{}, ownerHandler},
		{&corev1.Secret{}, ownerHandler},
	}
	for _, w := range centralWatches {
		if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), w.obj, w.handler)); err != nil {
			return xerrors.Errorf("failed to set MongoDBSearch central watch for %T: %w", w.obj, err)
		}
	}

	// Health-check goroutine fans out per-cluster reachability changes onto a
	// GenericEvent channel that the controller watches. Empty memberClusterObjectsMap
	// (single-cluster install) skips the goroutine entirely — there is nothing to watch.
	if len(memberClusterObjectsMap) > 0 {
		eventChannel := make(chan event.GenericEvent)
		healthChecker := memberwatch.MemberClusterHealthChecker{
			Cache:                 make(map[string]memberwatch.ClusterHealthChecker),
			HealthyStreak:         make(map[string]int),
			RequiredHealthyStreak: env.ReadIntOrDefault(util.RequiredHealthyStreakEnv, util.DefaultRequiredHealthyStreak), // nolint:forbidigo
		}
		go healthChecker.WatchMemberClusterHealth(ctx, zap.S(), eventChannel, r.kubeClient, memberClusterObjectsMap)

		if err := c.Watch(source.Channel[client.Object](eventChannel, &handler.EnqueueRequestForObject{})); err != nil {
			return err
		}

		// Per-member-cluster watches map events back to the parent MongoDBSearch
		// via the search-owner labels (cross-cluster owner refs do not GC).
		searchOwnerHandler := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			if req := khandler.MapMemberClusterObjectToSearch(obj); req != (reconcile.Request{}) {
				return []reconcile.Request{req}
			}
			return nil
		})
		searchOwnerPredicate := watch.PredicatesForMultiClusterSearchResource()
		watchedTypes := []client.Object{
			&appsv1.StatefulSet{},
			&corev1.Service{},
			&appsv1.Deployment{},
			&corev1.ConfigMap{},
			&corev1.Secret{},
		}
		for k, v := range memberClusterObjectsMap {
			for _, gvk := range watchedTypes {
				if err := c.Watch(source.Kind[client.Object](v.GetCache(), gvk, searchOwnerHandler, searchOwnerPredicate)); err != nil {
					return xerrors.Errorf("failed to set MongoDBSearch member-cluster watch on %s for %T: %w", k, gvk, err)
				}
			}
		}
	}

	return nil
}
