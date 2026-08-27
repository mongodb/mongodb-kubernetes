package operator

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// secretsCheckRequeueAfter is the requeue interval used when CheckSecretsPresence
// reports any per-cluster customer-replicated secret missing. Reconcile returns
// (Result{RequeueAfter: secretsCheckRequeueAfter}, nil) so we don't trigger
// exponential backoff while the customer is fixing the gap.
const (
	secretsCheckRequeueAfter = 30 * time.Second
	searchMongotComponent    = "mongot"
	searchProxyComponent     = "search-proxy"
)

// prepareSearchFuncs are the two pre-reconcile gates shared by the search
// reconcilers. validate checks the full spec; a non-OK status stops the
// reconcile. shouldSkipCluster reports whether another operator owns this CR;
// when it returns false it also narrows spec.clusters down to this operator's
// entry. Removed-cluster cleanup must run between the two gates: after
// validation so a bad spec never drives deletions (cleanup pauses until the
// spec is fixed), and before the narrowing so the full spec decides which
// clusters were removed.
type prepareSearchFuncs struct {
	validate          func(search *searchv1.MongoDBSearch) workflow.Status
	shouldSkipCluster func(search *searchv1.MongoDBSearch, log *zap.SugaredLogger) bool
}

// operatorClusterNotInSearchSpec reports whether this operator serves a named
// cluster that a NON-EMPTY spec.clusters no longer lists — the signal to clean up
// local resources instead of reconciling. Deliberately false for an empty
// spec.clusters: rejecting an empty topology is validation's job, not cleanup's.
func operatorClusterNotInSearchSpec(search *searchv1.MongoDBSearch, operatorClusterName string) bool {
	if operatorClusterName == "" || len(search.Spec.Clusters) == 0 {
		return false
	}
	for _, cluster := range search.Spec.Clusters {
		if cluster.Name == operatorClusterName {
			return false
		}
	}
	return true
}

// newPrepareSearch picks the gates once at construction so Reconcile never
// branches on the operator mode. validate must see the full spec: once
// spec.clusters is narrowed to one entry, the multi-cluster validators skip
// themselves and would let a bad multi-cluster spec through.
func newPrepareSearch(operatorClusterName string) prepareSearchFuncs {
	validateSpec := func(search *searchv1.MongoDBSearch) workflow.Status {
		if vErr := search.ValidateSpec(); vErr != nil {
			return workflow.Invalid("%s", vErr.Error())
		}
		return workflow.OK()
	}
	if operatorClusterName == "" {
		return prepareSearchFuncs{
			validate:          validateSpec,
			shouldSkipCluster: func(*searchv1.MongoDBSearch, *zap.SugaredLogger) bool { return false },
		}
	}
	return prepareSearchFuncs{
		validate: func(search *searchv1.MongoDBSearch) workflow.Status {
			if st := validateSpec(search); !st.IsOK() {
				return st
			}
			if vErr := search.ValidateOperatorPerClusterIndices(); vErr != nil {
				return workflow.Invalid("%s", vErr.Error())
			}
			return workflow.OK()
		},
		shouldSkipCluster: func(search *searchv1.MongoDBSearch, log *zap.SugaredLogger) bool {
			if !search.LocalizeToCluster(operatorClusterName) {
				log.Infof("spec.clusters does not list this operator's cluster %q; skipping (another operator owns this CR)", operatorClusterName)
				return true
			}
			return false
		},
	}
}

type MongoDBSearchReconciler struct {
	kubeClient           kubernetesClient.Client
	watch                *watch.ResourceWatcher
	operatorSearchConfig searchcontroller.OperatorSearchConfig

	memberClustersProvider *multicluster.Provider
	operatorClusterName    string

	prepareSearch prepareSearchFuncs
}

func newMongoDBSearchReconciler(
	kubeClient client.Client,
	operatorSearchConfig searchcontroller.OperatorSearchConfig,
	memberClustersProvider *multicluster.Provider,
	operatorClusterName string,
) *MongoDBSearchReconciler {
	central := kubernetesClient.NewClient(kubeClient)
	return &MongoDBSearchReconciler{
		kubeClient:             central,
		watch:                  watch.NewResourceWatcher(),
		operatorSearchConfig:   operatorSearchConfig,
		memberClustersProvider: memberClustersProvider,
		operatorClusterName:    operatorClusterName,
		prepareSearch:          newPrepareSearch(operatorClusterName),
	}
}

// memberClusterClients derives per-cluster Kubernetes clients from a provider
// snapshot, matching the shape of the startup-built client map the reconcilers
// previously held.
func memberClusterClients(memberClusterEntries map[string]multicluster.Entry) map[string]kubernetesClient.Client {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClusterEntries))
	for k, v := range memberClusterEntries {
		clientsMap[k] = kubernetesClient.NewClient(v.Client)
	}
	return clientsMap
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status},verbs=*,namespace=placeholder
func (r *MongoDBSearchReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBSearch", request.NamespacedName)
	log.Info("-> MongoDBSearch.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, mdbSearch, log); err != nil {
		return result, err
	}

	if !mdbSearch.DeletionTimestamp.IsZero() {
		log.Infof("MongoDBSearch %s/%s is deleting; skipping main-controller reconcile", mdbSearch.Namespace, mdbSearch.Name)
		return reconcile.Result{}, nil
	}

	// Live snapshot of the member-cluster registry for this reconcile.
	memberClusterEntries := r.memberClustersProvider.Entries()
	memberClients := memberClusterClients(memberClusterEntries)

	// Short-circuit: the disable-reconciliation annotation allows to
	// pause reconciliation on a single CR so owned objects can be mutated
	// without the operator reverting them.
	// Useful for tests when the operator is running locally and not in the pod.
	if mdbSearch.IsReconciliationDisabled() {
		log.Infof("MongoDBSearch %s/%s reconciliation disabled by %s annotation; skipping",
			mdbSearch.GetNamespace(), mdbSearch.GetName(), searchv1.DisableReconciliationAnnotation)
		return reconcile.Result{}, nil
	}

	if st := r.prepareSearch.validate(mdbSearch); !st.IsOK() {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, st, log)
	}

	// Removed-cluster cleanup runs after validation (an invalid spec must never
	// drive deletions) but on the PRE-localization spec (a narrowed spec would
	// mark sibling clusters as removed). Best-effort: failures are logged, never
	// fail the reconcile, and are retried on the next reconcile of the live CR.
	// The two checks split the cleanup by mode: the member map is only populated
	// in hub-and-spoke, so this call covers that mode; in operator-per-cluster
	// the map is empty and the operatorClusterNotInSearchSpec check below lets
	// each operator clean up its own cluster.
	if err := deleteRemovedMemberClusterResources(ctx, mdbSearch, memberClients, deleteMemberSearchResources, log); err != nil {
		log.Warnf("Failed to clean up Search resources on removed member clusters: %v", err)
	}

	if operatorClusterNotInSearchSpec(mdbSearch, r.operatorClusterName) {
		r.watch.RemoveDependentWatchedResources(mdbSearch.NamespacedName())
		if err := deleteLocalSearchResources(ctx, r.kubeClient, mdbSearch, r.operatorClusterName, log); err != nil {
			log.Warnf("Failed to clean up Search resources on removed cluster %q: %v", r.operatorClusterName, err)
		}
		return reconcile.Result{}, nil
	}

	if r.prepareSearch.shouldSkipCluster(mdbSearch, log) {
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

	r.registerTLSResourceWatches(mdbSearch, searchSource)

	if mdbSearch.Spec.AutoEmbedding != nil {
		r.watch.AddWatchedResourceIfNotAdded(mdbSearch.Spec.AutoEmbedding.EmbeddingModelAPIKeySecret.Name, mdbSearch.Namespace, watch.Secret, mdbSearch.NamespacedName())
	}

	// Watch the dedicated keyFilePassword secrets so correcting a wrong password (without a cert/key
	// change) triggers a reconcile.
	for _, nn := range []types.NamespacedName{
		mdbSearch.GrpcKeyFilePasswordSecret(),
		mdbSearch.X509KeyFilePasswordSecret(),
		mdbSearch.ScramKeyFilePasswordSecret(),
	} {
		if nn.Name != "" {
			r.watch.AddWatchedResourceIfNotAdded(nn.Name, nn.Namespace, watch.Secret, mdbSearch.NamespacedName())
		}
	}

	// The no-op mutation reads the state and, as a side effect, repairs legacy
	// owner labels on the state ConfigMap.
	state, err := searchcontroller.MutateSearchState(ctx, r.kubeClient, mdbSearch, func(*searchcontroller.SearchDeploymentState) bool {
		return false
	})
	if err != nil {
		// A concurrent writer bumped the ConfigMap between read and update; retry
		// instead of marking the CR Failed over a transient race.
		if apierrors.IsConflict(err) {
			return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, workflow.Pending("Search state was modified concurrently, re-queuing").Requeue(), log)
		}
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, workflow.Failed(xerrors.Errorf("failed to read or repair search state: %w", err)), log)
	}

	reconcileHelper := searchcontroller.NewMongoDBSearchReconcileHelper(
		r.kubeClient,
		mdbSearch,
		searchSource,
		r.operatorSearchConfig,
		memberClusterEntries,
		r.operatorClusterName,
		state,
	)

	result, err := reconcileHelper.Reconcile(ctx, log).ReconcileResult()
	if err != nil {
		return result, err
	}

	// Diagnostic only: missing customer-replicated secrets are logged and
	// re-checked after a delay, never failing the reconcile. Skip when reconcile
	// already requeued — its own gates cover that case.
	if result.RequeueAfter == 0 {
		if gaps := searchcontroller.CheckSecretsPresence(ctx, mdbSearch, r.kubeClient, memberClients); len(gaps) > 0 {
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

// OnDelete runs one best-effort cleanup pass when a MongoDBSearch is deleted.
// Kubernetes garbage collection only works within a single cluster: resources
// on the CR's own cluster carry owner references and are collected by
// Kubernetes, while an owner reference on a member-cluster object pointing at
// the central-cluster CR does nothing. This pass therefore deletes
// member-cluster resources itself, selecting them by the search-name and
// search-namespace labels. A same-name successor CR created within the
// deletion window may transiently match that selection — an accepted race.
// There are no retries and no post-restart recovery: anything this pass misses
// is logged and left to manual cleanup.
func (r *MongoDBSearchReconciler) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	search, ok := obj.(*searchv1.MongoDBSearch)
	if !ok {
		return xerrors.Errorf("expected a deleted MongoDBSearch, got %T", obj)
	}

	memberClusterEntries := r.memberClustersProvider.Entries()
	for _, clusterName := range slices.Sorted(maps.Keys(memberClusterEntries)) {
		memberClient := kubernetesClient.NewClient(memberClusterEntries[clusterName].Client)
		errs := deleteOwnedClusterResources(ctx, memberClient, clusterName, search, log)
		// deleteOwnedClusterResources' kind list has no Deployment, but Search
		// also owns per-cluster Envoy and metrics-forwarder Deployments.
		if err := memberClient.DeleteAllOf(ctx, &appsv1.Deployment{}, &mdbv1.MongodbCleanUpOptions{Namespace: search.Namespace, Labels: search.GetOwnerLabels()}); err != nil {
			errs = errors.Join(errs, err)
		}
		if errs != nil {
			log.Warnf("Failed to clean up resources of deleted MongoDBSearch %s on cluster %q: %v", search.NamespacedName(), clusterName, errs)
		}
	}

	r.watch.RemoveDependentWatchedResources(search.NamespacedName())
	return nil
}

// deleteMemberSearchResources reaps one removed cluster's label-owned Search
// resources. The state ConfigMap is deliberately absent: it lives on the
// central cluster only, and a hub whose own cluster is member-registered would
// otherwise delete the live central state — see deleteLocalSearchResources.
func deleteMemberSearchResources(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName string, log *zap.SugaredLogger) error {
	errs := errors.Join(
		searchcontroller.DeleteAllOwnedResources(ctx, c, search, clusterName, "StatefulSet", searchMongotComponent, &appsv1.StatefulSetList{}, log),
		searchcontroller.DeleteAllOwnedResources(ctx, c, search, clusterName, "headless Service", searchMongotComponent, &corev1.ServiceList{}, log),
		searchcontroller.DeleteAllOwnedResources(ctx, c, search, clusterName, "proxy Service", searchProxyComponent, &corev1.ServiceList{}, log),
		searchcontroller.DeleteAllOwnedResources(ctx, c, search, clusterName, "ConfigMap", searchMongotComponent, &corev1.ConfigMapList{}, log),
		searchcontroller.DeleteAllOwnedResources(ctx, c, search, clusterName, "Secret", searchMongotComponent, &corev1.SecretList{}, log),
	)
	for _, s := range []struct{ kind, name string }{
		{"x509 client auth Secret", search.X509OperatorManagedSecret().Name},
		{"SCRAM client auth Secret", search.ScramClientCertOperatorManagedSecret().Name},
	} {
		_, err := searchcontroller.DeleteOwnedResource(ctx, c, search, clusterName, s.kind, "",
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: search.Namespace}}, log)
		errs = errors.Join(errs, err)
	}
	return errs
}

// deleteLocalSearchResources additionally deletes the central state ConfigMap:
// it runs only when THIS operator's own cluster was removed from spec.clusters.
func deleteLocalSearchResources(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName string, log *zap.SugaredLogger) error {
	stateCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      searchcontroller.SearchStateCMName(search),
			Namespace: search.Namespace,
		},
	}
	_, stateErr := searchcontroller.DeleteOwnedResource(ctx, c, search, clusterName, "state ConfigMap", "", stateCM, log)
	return errors.Join(deleteMemberSearchResources(ctx, c, search, clusterName, log), stateErr)
}

func deleteEnvoySearchResources(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName string, log *zap.SugaredLogger) error {
	return errors.Join(
		searchcontroller.DeleteAllOwnedResources(ctx, c, search, clusterName, "Deployment", searchProxyComponent, &appsv1.DeploymentList{}, log),
		searchcontroller.DeleteAllOwnedResources(ctx, c, search, clusterName, "ConfigMap", searchProxyComponent, &corev1.ConfigMapList{}, log),
	)
}

// deleteRemovedMemberClusterResources deletes the label-owned Search resources
// on every member cluster that is no longer listed in spec.clusters.
// Best-effort: one cluster's failure never blocks the others, and anything
// missed is retried on the next reconcile of the still-live CR.
func deleteRemovedMemberClusterResources(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	memberClients map[string]kubernetesClient.Client,
	deleteResources func(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName string, log *zap.SugaredLogger) error,
	log *zap.SugaredLogger,
) error {
	// An unnamed entry deploys on the central cluster, which may itself be
	// member-registered under a name the spec never lists; sweeping members
	// would delete the live local deployment. Unnamed entries are only legal
	// at len==1, so this check is exact.
	if len(search.Spec.Clusters) == 1 && search.Spec.Clusters[0].Name == "" {
		return nil
	}
	desired := make(map[string]struct{}, len(search.Spec.Clusters))
	for _, cluster := range search.Spec.Clusters {
		desired[cluster.Name] = struct{}{}
	}
	var errs error
	for clusterName, memberClient := range memberClients {
		if _, ok := desired[clusterName]; ok {
			continue
		}
		if err := deleteResources(ctx, memberClient, search, clusterName, log); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

type mongoDBSearchResourceWatch struct {
	obj        client.Object
	handler    handler.EventHandler
	predicates []predicate.Predicate
}

func centralMongoDBSearchResourceWatches(r *MongoDBSearchReconciler) []mongoDBSearchResourceWatch {
	searchOwnerHandler := handler.EnqueueRequestsFromMapFunc(khandler.EnqueueMemberClusterObjectToSearch)
	searchOwnerPredicates := []predicate.Predicate{watch.PredicatesForMultiClusterSearchResource()}
	return []mongoDBSearchResourceWatch{
		// The delete override runs the one-shot best-effort cleanup pass with the
		// deleted CR object; create/update events enqueue normally.
		{
			obj:     &searchv1.MongoDBSearch{},
			handler: &ResourceEventHandler{deleter: r},
		},
		{
			obj:     &mdbv1.MongoDB{},
			handler: &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch},
		},
		{
			obj:     &mdbcv1.MongoDBCommunity{},
			handler: &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch},
		},
		{
			obj:        &appsv1.Deployment{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
		{
			obj:        &appsv1.StatefulSet{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
		{
			obj:        &corev1.Service{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
		{
			obj:     &corev1.Secret{},
			handler: &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch},
		},
		{
			obj:     &corev1.ConfigMap{},
			handler: &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch},
		},
	}
}

func memberMongoDBSearchResourceWatches(r *MongoDBSearchReconciler) []mongoDBSearchResourceWatch {
	searchOwnerHandler := handler.EnqueueRequestsFromMapFunc(khandler.EnqueueMemberClusterObjectToSearch)
	searchOwnerPredicates := []predicate.Predicate{watch.PredicatesForMultiClusterSearchResource()}
	return []mongoDBSearchResourceWatch{
		{
			obj:        &appsv1.Deployment{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
		{
			obj:        &appsv1.StatefulSet{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
		{
			obj:        &corev1.Service{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
		{
			obj:        &corev1.ConfigMap{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
		{
			obj:        &corev1.Secret{},
			handler:    searchOwnerHandler,
			predicates: searchOwnerPredicates,
		},
	}
}

func (r *MongoDBSearchReconciler) registerTLSResourceWatches(mdbSearch *searchv1.MongoDBSearch, searchSource searchcontroller.SearchSourceDBResource) {
	if tlsSourceConfig := searchSource.TLSConfig(); tlsSourceConfig != nil {
		for wType, resources := range tlsSourceConfig.ResourcesToWatch {
			for _, resource := range resources {
				r.watch.AddWatchedResourceIfNotAdded(resource.Name, resource.Namespace, wType, mdbSearch.NamespacedName())
			}
		}
	}
	if mdbSearch.Spec.Security.TLS == nil {
		return
	}
	if shardedSource, ok := searchSource.(searchcontroller.SearchSourceShardedDeployment); ok {
		for _, cluster := range mdbSearch.Spec.Clusters {
			for _, shardName := range shardedSource.GetShardNames() {
				sourceSecretNsName := mdbSearch.TLSSecretForClusterShard(cluster.ResolveIndex(), shardName)
				r.watch.AddWatchedResourceIfNotAdded(sourceSecretNsName.Name, sourceSecretNsName.Namespace, watch.Secret, mdbSearch.NamespacedName())
			}
		}
		return
	}
	for _, secret := range []types.NamespacedName{
		mdbSearch.TLSSecretNamespacedName(),
		mdbSearch.TLSOperatorSecretNamespacedName(),
	} {
		r.watch.AddWatchedResourceIfNotAdded(secret.Name, secret.Namespace, watch.Secret, mdbSearch.NamespacedName())
	}
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
	memberClustersProvider *multicluster.Provider,
	operatorClusterName string,
	maxConcurrentReconciles int,
	memberClusterClientTimeout int,
	requiredHealthyStreak int,
) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &searchv1.MongoDBSearch{}, searchv1.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder); err != nil {
		return err
	}

	r := newMongoDBSearchReconciler(
		mgr.GetClient(),
		operatorSearchConfig,
		memberClustersProvider,
		operatorClusterName,
	)

	c, err := controller.New(util.MongoDbSearchController, mgr, controller.Options{
		Reconciler:              r,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	})
	if err != nil {
		return err
	}

	for _, w := range centralMongoDBSearchResourceWatches(r) {
		if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), w.obj, w.handler, w.predicates...)); err != nil {
			return xerrors.Errorf("failed to set MongoDBSearch central watch for %T: %w", w.obj, err)
		}
	}

	// Per-member-cluster watches follow member-cluster engagement: on every cluster add attach
	// the watches to the new cluster (they map events back to the parent MongoDBSearch via the
	// search-owner labels — cross-cluster owner refs do not GC) and enqueue all MongoDBSearch
	// CRs — watch replay alone cannot reach CRs that own no resources on the new cluster yet.
	clusterAddedEvents := make(chan event.GenericEvent)
	if err := c.Watch(source.Channel[client.Object](clusterAddedEvents, &handler.EnqueueRequestForObject{})); err != nil {
		return xerrors.Errorf("failed to set MongoDBSearch cluster-added channel watch: %w", err)
	}
	memberClustersProvider.RegisterHooks(ctx, multicluster.Hooks{
		OnAdd: func(ctx context.Context, clusterName string, entry multicluster.Entry) {
			for _, w := range memberMongoDBSearchResourceWatches(r) {
				if err := c.Watch(source.Kind[client.Object](entry.Cluster.GetCache(), w.obj, w.handler, w.predicates...)); err != nil {
					zap.S().Errorf("failed to set MongoDBSearch member-cluster watch on %s for %T: %s", clusterName, w.obj, err)
				}
			}
			if err := multicluster.EnqueueAll(ctx, mgr.GetClient(), &searchv1.MongoDBSearchList{}, clusterAddedEvents); err != nil {
				zap.S().Errorf("failed to enqueue MongoDBSearch resources on member cluster %s add: %s", clusterName, err)
			}
		},
	})

	return nil
}
