package operator

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
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

// prepareSearchFunc is the shared pre-reconcile gate for both search reconcilers;
// writeStatus routes validation failures to the caller's own status surface. Returns
// skip=true when the caller must stop reconciling (validation failed, or this
// operator does not own the CR).
type prepareSearchFunc func(search *searchv1.MongoDBSearch, log *zap.SugaredLogger, writeStatus func(workflow.Status) (reconcile.Result, error)) (skip bool, res reconcile.Result, err error)

// newPrepareSearch picks the gate once at construction so Reconcile never branches
// on the operator mode. ValidateSpec runs on the UN-NARROWED spec first: per-cluster
// operator mode narrows spec.clusters[] via LocalizeToCluster, after which the MC
// validators short-circuit on len(clusters) <= 1 and would silently accept
// misconfigured MC specs.
func newPrepareSearch(operatorClusterName string) prepareSearchFunc {
	validateSpec := func(search *searchv1.MongoDBSearch, log *zap.SugaredLogger, writeStatus func(workflow.Status) (reconcile.Result, error)) (bool, reconcile.Result, error) {
		// A single operator (no operatorClusterName) cannot manage a multi-cluster (>1)
		// search deployment; per-cluster operators narrow to their own entry below.
		if operatorClusterName == "" && len(search.Spec.Clusters) > 1 && !env.ReadBoolOrDefault(util.SearchEnableMultiClusterEnv, false) { // nolint:forbidigo
			r, e := writeStatus(workflow.Invalid("multi-cluster MongoDBSearch is not supported yet: spec.clusters must contain a single entry"))
			return true, r, e
		}
		if vErr := search.ValidateSpec(); vErr != nil {
			r, e := writeStatus(workflow.Invalid("%s", vErr.Error()))
			return true, r, e
		}
		return false, reconcile.Result{}, nil
	}
	if operatorClusterName == "" {
		return validateSpec
	}
	return func(search *searchv1.MongoDBSearch, log *zap.SugaredLogger, writeStatus func(workflow.Status) (reconcile.Result, error)) (bool, reconcile.Result, error) {
		if skip, r, e := validateSpec(search, log, writeStatus); skip {
			return skip, r, e
		}
		if vErr := search.ValidateOperatorPerClusterIndices(); vErr != nil {
			r, e := writeStatus(workflow.Invalid("%s", vErr.Error()))
			return true, r, e
		}
		if !search.LocalizeToCluster(operatorClusterName) {
			log.Infof("spec.clusters does not list this operator's cluster %q; skipping (another operator owns this CR)", operatorClusterName)
			return true, reconcile.Result{}, nil
		}
		return false, reconcile.Result{}, nil
	}
}

type MongoDBSearchReconciler struct {
	kubeClient           kubernetesClient.Client
	uncachedReader       client.Reader
	watch                *watch.ResourceWatcher
	operatorSearchConfig searchcontroller.OperatorSearchConfig

	memberClusterClientsMap map[string]kubernetesClient.Client // per-cluster Kubernetes client; empty in single-cluster installs

	prepareSearch prepareSearchFunc
}

func newMongoDBSearchReconciler(
	kubeClient client.Client,
	apiReader client.Reader,
	operatorSearchConfig searchcontroller.OperatorSearchConfig,
	memberClustersMap map[string]client.Client,
	operatorClusterName string,
) *MongoDBSearchReconciler {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClustersMap))
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	if apiReader == nil {
		apiReader = kubeClient
	}

	return &MongoDBSearchReconciler{
		kubeClient:              kubernetesClient.NewClient(kubeClient),
		uncachedReader:          apiReader,
		watch:                   watch.NewResourceWatcher(),
		operatorSearchConfig:    operatorSearchConfig,
		memberClusterClientsMap: clientsMap,
		prepareSearch:           newPrepareSearch(operatorClusterName),
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status},verbs=*,namespace=placeholder
func (r *MongoDBSearchReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBSearch", request.NamespacedName)
	log.Info("-> MongoDBSearch.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if err := r.kubeClient.Get(ctx, request.NamespacedName, mdbSearch); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Errorf("Failed to query object %s: %s", request.NamespacedName, err)
			return reconcile.Result{RequeueAfter: 10 * time.Second}, err
		}

		absent, confirmErr := r.confirmSearchAbsentWithAPIReader(ctx, request.NamespacedName)
		if confirmErr != nil {
			return reconcile.Result{}, confirmErr
		}
		if !absent {
			log.Infof("MongoDBSearch %s still exists in uncached reader; skipping NotFound sweep", request.NamespacedName)
			return reconcile.Result{}, nil
		}

		if sweepErr := r.sweepOwnedResourcesOnSearchNotFound(ctx, request.NamespacedName, log); sweepErr != nil {
			return reconcile.Result{}, sweepErr
		}
		return reconcile.Result{}, nil
	}

	if !mdbSearch.DeletionTimestamp.IsZero() {
		log.Infof("MongoDBSearch %s/%s is deleting; skipping main-controller reconcile", mdbSearch.Namespace, mdbSearch.Name)
		return reconcile.Result{}, nil
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

	if skip, result, err := r.prepareSearch(mdbSearch, log,
		func(st workflow.Status) (reconcile.Result, error) {
			return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, st, log)
		}); skip {
		return result, err
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

	state, err := searchcontroller.ReadSearchState(ctx, r.kubeClient, mdbSearch)
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, workflow.Failed(xerrors.Errorf("failed to read search state: %w", err)), log)
	}

	reconcileHelper := searchcontroller.NewMongoDBSearchReconcileHelper(r.kubeClient, mdbSearch, searchSource, r.operatorSearchConfig, r.memberClusterClientsMap, state)

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

func (r *MongoDBSearchReconciler) confirmSearchAbsentWithAPIReader(ctx context.Context, nsName types.NamespacedName) (bool, error) {
	search := &searchv1.MongoDBSearch{}
	if err := r.uncachedReader.Get(ctx, nsName, search); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, xerrors.Errorf("failed to confirm MongoDBSearch absence for %s via uncached reader: %w", nsName, err)
	}
	return false, nil
}

func (r *MongoDBSearchReconciler) sweepOwnedResourcesOnSearchNotFound(ctx context.Context, nsName types.NamespacedName, log *zap.SugaredLogger) error {
	searchSelector := client.MatchingLabels{
		khandler.MongoDBSearchOwnerNameLabel:      nsName.Name,
		khandler.MongoDBSearchOwnerNamespaceLabel: nsName.Namespace,
	}

	clusters := []struct {
		name   string
		client kubernetesClient.Client
	}{
		{name: "central", client: r.kubeClient},
	}
	for clusterName, memberClient := range r.memberClusterClientsMap {
		clusters = append(clusters, struct {
			name   string
			client kubernetesClient.Client
		}{name: clusterName, client: memberClient})
	}

	orderedKinds := []struct {
		kind    string
		newList func() client.ObjectList
	}{
		{kind: "Deployment", newList: func() client.ObjectList { return &appsv1.DeploymentList{} }},
		{kind: "StatefulSet", newList: func() client.ObjectList { return &appsv1.StatefulSetList{} }},
		{kind: "Service", newList: func() client.ObjectList { return &corev1.ServiceList{} }},
		{kind: "ConfigMap", newList: func() client.ObjectList { return &corev1.ConfigMapList{} }},
		{kind: "Secret", newList: func() client.ObjectList { return &corev1.SecretList{} }},
	}

	var errs error
	for _, k := range orderedKinds {
		for _, cluster := range clusters {
			if err := r.sweepOwnedResourceKind(ctx, nsName, cluster.client, cluster.name, k.kind, k.newList(), searchSelector, log); err != nil {
				errs = errors.Join(errs, err)
			}
		}
	}
	return errs
}

func (r *MongoDBSearchReconciler) sweepOwnedResourceKind(
	ctx context.Context,
	search types.NamespacedName,
	c kubernetesClient.Client,
	clusterName string,
	kind string,
	list client.ObjectList,
	selector client.MatchingLabels,
	log *zap.SugaredLogger,
) error {
	if err := c.List(ctx, list, client.InNamespace(search.Namespace), selector); err != nil {
		return xerrors.Errorf("failed listing %ss for deleted MongoDBSearch %s on cluster %q: %w", kind, search, clusterName, err)
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return xerrors.Errorf("failed extracting %s list for deleted MongoDBSearch %s on cluster %q: %w", kind, search, clusterName, err)
	}

	var errs error
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok || !ownsSearchForSweep(search, obj) {
			continue
		}
		log.Infof("Deleting %s %s for deleted MongoDBSearch %s on cluster %q", kind, obj.GetName(), search, clusterName)
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			errs = errors.Join(errs, xerrors.Errorf("failed deleting %s %s for deleted MongoDBSearch %s on cluster %q: %w", kind, obj.GetName(), search, clusterName, err))
		}
	}
	return errs
}

func ownsSearchForSweep(search types.NamespacedName, obj client.Object) bool {
	labels := obj.GetLabels()
	return labels[khandler.MongoDBSearchOwnerNameLabel] == search.Name &&
		labels[khandler.MongoDBSearchOwnerNamespaceLabel] == search.Namespace
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
	operatorClusterName string,
) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &searchv1.MongoDBSearch{}, searchv1.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder); err != nil {
		return err
	}

	r := newMongoDBSearchReconciler(
		mgr.GetClient(),
		mgr.GetAPIReader(),
		operatorSearchConfig,
		multicluster.ClustersMapToClientMap(memberClusterObjectsMap),
		operatorClusterName,
	)

	c, err := controller.New(util.MongoDbSearchController, mgr, controller.Options{
		Reconciler:              r,
		MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1), // nolint:forbidigo
	})
	if err != nil {
		return err
	}

	centralWatches := []struct {
		obj     client.Object
		handler handler.EventHandler
	}{
		{&searchv1.MongoDBSearch{}, &handler.EnqueueRequestForObject{}},
		{&mdbv1.MongoDB{}, &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch}},
		{&mdbcv1.MongoDBCommunity{}, &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch}},
		{&corev1.Secret{}, &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch}},
		{&corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch}},
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
		searchOwnerHandler := handler.EnqueueRequestsFromMapFunc(khandler.EnqueueMemberClusterObjectToSearch)
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
