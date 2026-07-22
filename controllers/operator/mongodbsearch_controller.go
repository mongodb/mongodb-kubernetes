package operator

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
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
const (
	secretsCheckRequeueAfter = 30 * time.Second
	searchMongotComponent    = "mongot"
	searchProxyComponent     = "search-proxy"
)

// prepareSearchFunc is the shared pre-reconcile gate for both search reconcilers;
// writeStatus routes validation failures to the caller's own status surface. Returns
// skip=true when the caller must stop reconciling (validation failed, or this
// operator does not own the CR).
type prepareSearchFunc func(search *searchv1.MongoDBSearch, log *zap.SugaredLogger, writeStatus func(workflow.Status) (reconcile.Result, error)) (skip bool, res reconcile.Result, err error)

func isRemovedSearchOperatorCluster(search *searchv1.MongoDBSearch, operatorClusterName string) bool {
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
	memberClusterReadersMap map[string]client.Reader
	localNamedClusters      bool
	operatorClusterName     string

	prepareSearch prepareSearchFunc
}

func newMongoDBSearchReconciler(
	kubeClient client.Client,
	apiReader client.Reader,
	operatorSearchConfig searchcontroller.OperatorSearchConfig,
	memberClustersMap map[string]client.Client,
	memberClusterReadersMap map[string]client.Reader,
	operatorClusterName string,
) *MongoDBSearchReconciler {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClustersMap))
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}
	if memberClusterReadersMap == nil {
		memberClusterReadersMap = make(map[string]client.Reader, len(memberClustersMap))
		for clusterName, memberClient := range memberClustersMap {
			memberClusterReadersMap[clusterName] = memberClient
		}
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
		memberClusterReadersMap: memberClusterReadersMap,
		localNamedClusters:      operatorClusterName != "" || (len(memberClustersMap) == 0 && !env.ReadBoolOrDefault(util.SearchEnableMultiClusterEnv, false)), // nolint:forbidigo
		operatorClusterName:     operatorClusterName,
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

		r.watch.RemoveDependentWatchedResources(request.NamespacedName)

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
	if mdbSearch.IsReconciliationDisabled() {
		log.Infof("MongoDBSearch %s/%s reconciliation disabled by %s annotation; skipping",
			mdbSearch.GetNamespace(), mdbSearch.GetName(), searchv1.DisableReconciliationAnnotation)
		return reconcile.Result{}, nil
	}

	topologyCurrent, err := cleanupRemovedMemberSearchResources(
		ctx,
		r.uncachedReader,
		mdbSearch,
		r.memberClusterReadersMap,
		r.memberClusterClientsMap,
		mainSearchResourceCleanups(mdbSearch),
		log,
	)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !topologyCurrent {
		return reconcile.Result{Requeue: true}, nil
	}

	if isRemovedSearchOperatorCluster(mdbSearch, r.operatorClusterName) {
		r.watch.RemoveDependentWatchedResources(mdbSearch.NamespacedName())
		topologyCurrent, err := cleanupRemovedLocalSearchResources(
			ctx,
			r.uncachedReader,
			r.uncachedReader,
			r.kubeClient,
			mdbSearch,
			r.operatorClusterName,
			mainSearchResourceCleanups(mdbSearch),
			log,
		)
		if err != nil {
			return reconcile.Result{}, err
		}
		if !topologyCurrent {
			return reconcile.Result{Requeue: true}, nil
		}
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

	// Adopt state written by GA releases even when its data needs no change.
	state, err := searchcontroller.MutateSearchState(ctx, r.kubeClient, mdbSearch, func(*searchcontroller.SearchDeploymentState) bool {
		return false
	})
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.kubeClient, mdbSearch, workflow.Failed(xerrors.Errorf("failed to read search state: %w", err)), log)
	}

	reconcileHelper := searchcontroller.NewMongoDBSearchReconcileHelper(
		r.kubeClient,
		mdbSearch,
		searchSource,
		r.operatorSearchConfig,
		r.memberClusterClientsMap,
		state,
		r.localNamedClusters,
	).WithCleanupReaders(
		r.uncachedReader,
		r.memberClusterReadersMap,
	)

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

func (r *MongoDBSearchReconciler) getSourceMongoDBForSearch(ctx context.Context, kubeClient client.Reader, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (searchcontroller.SearchSourceDBResource, error) {
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

	clusters := []searchSweepCluster{
		{name: "central", listReader: r.uncachedReader, parentReader: r.uncachedReader, deleteClient: r.kubeClient},
	}
	for clusterName, memberClient := range r.memberClusterClientsMap {
		clusters = append(clusters, searchSweepCluster{
			name:         clusterName,
			listReader:   memberClient,
			parentReader: r.memberClusterReadersMap[clusterName],
			deleteClient: memberClient,
		})
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
			if err := r.sweepOwnedResourceKind(ctx, nsName, cluster.listReader, cluster.parentReader, cluster.deleteClient, cluster.name, k.kind, k.newList(), searchSelector, log); err != nil {
				errs = errors.Join(errs, err)
			}
		}
	}
	return errs
}

type searchSweepCluster struct {
	name         string
	listReader   client.Reader
	parentReader client.Reader
	deleteClient kubernetesClient.Client
}

type searchResourceCleanup struct {
	kind        string
	name        string
	component   string
	omitCluster bool
	newList     func() client.ObjectList
}

func mainSearchResourceCleanups(search *searchv1.MongoDBSearch) []searchResourceCleanup {
	return []searchResourceCleanup{
		{kind: "StatefulSet", component: searchMongotComponent, newList: func() client.ObjectList { return &appsv1.StatefulSetList{} }},
		{kind: "headless Service", component: searchMongotComponent, newList: func() client.ObjectList { return &corev1.ServiceList{} }},
		{kind: "proxy Service", component: searchProxyComponent, newList: func() client.ObjectList { return &corev1.ServiceList{} }},
		{kind: "ConfigMap", component: searchMongotComponent, newList: func() client.ObjectList { return &corev1.ConfigMapList{} }},
		{kind: "state ConfigMap", name: searchcontroller.SearchStateCMName(search), omitCluster: true, newList: func() client.ObjectList { return &corev1.ConfigMapList{} }},
		{kind: "x509 client auth Secret", name: search.X509OperatorManagedSecret().Name, newList: func() client.ObjectList { return &corev1.SecretList{} }},
		{kind: "SCRAM client auth Secret", name: search.ScramClientCertOperatorManagedSecret().Name, newList: func() client.ObjectList { return &corev1.SecretList{} }},
		{kind: "Secret", component: searchMongotComponent, newList: func() client.ObjectList { return &corev1.SecretList{} }},
	}
}

func envoySearchResourceCleanups() []searchResourceCleanup {
	return []searchResourceCleanup{
		{kind: "Deployment", component: searchProxyComponent, newList: func() client.ObjectList { return &appsv1.DeploymentList{} }},
		{kind: "ConfigMap", component: searchProxyComponent, newList: func() client.ObjectList { return &corev1.ConfigMapList{} }},
	}
}

func cleanupRemovedMemberSearchResources(
	ctx context.Context,
	parentReader client.Reader,
	search *searchv1.MongoDBSearch,
	memberReaders map[string]client.Reader,
	memberClients map[string]kubernetesClient.Client,
	cleanups []searchResourceCleanup,
	log *zap.SugaredLogger,
) (bool, error) {
	desired := make(map[string]struct{}, len(search.Spec.Clusters))
	for _, cluster := range search.Spec.Clusters {
		desired[cluster.Name] = struct{}{}
	}
	var errs error
	topologyCurrent := true
	for clusterName, memberClient := range memberClients {
		if _, ok := desired[clusterName]; ok {
			continue
		}
		memberReader := memberReaders[clusterName]
		if memberReader == nil {
			errs = errors.Join(errs, xerrors.Errorf("cannot clean removed cluster %q MongoDBSearch resources without an API reader", clusterName))
			continue
		}
		clusterCurrent, err := cleanupRemovedLocalSearchResources(
			ctx,
			parentReader,
			memberReader,
			memberClient,
			search,
			clusterName,
			cleanups,
			log,
		)
		topologyCurrent = topologyCurrent && clusterCurrent
		errs = errors.Join(errs, err)
	}
	return topologyCurrent, errs
}

func cleanupRemovedLocalSearchResources(
	ctx context.Context,
	parentReader client.Reader,
	listReader client.Reader,
	deleteClient kubernetesClient.Client,
	search *searchv1.MongoDBSearch,
	clusterName string,
	cleanups []searchResourceCleanup,
	log *zap.SugaredLogger,
) (bool, error) {
	if search.UID == "" {
		return false, xerrors.Errorf("cannot clean removed cluster %q resources for MongoDBSearch %s without a UID", clusterName, search.NamespacedName())
	}
	// One up-front uncached confirmation that the cluster really is removed from the
	// live CR; the label + UID + precondition gates below make each delete safe.
	removed, err := confirmSearchClusterRemoved(ctx, parentReader, search, clusterName)
	if err != nil {
		return false, err
	}
	if !removed {
		return false, nil
	}
	var errs error
	for _, cleanup := range cleanups {
		selector := client.MatchingLabels{
			khandler.MongoDBSearchOwnerNameLabel:      search.Name,
			khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
			khandler.MongoDBSearchOwnerUIDLabel:       string(search.UID),
		}
		if !cleanup.omitCluster {
			selector[khandler.MongoDBSearchClusterNameLabel] = clusterName
		}
		if cleanup.component != "" {
			selector[khandler.MongoDBSearchComponentLabel] = cleanup.component
		}
		list := cleanup.newList()
		if err := listReader.List(ctx, list, client.InNamespace(search.Namespace), selector); err != nil {
			errs = errors.Join(errs, xerrors.Errorf("failed listing removed cluster %q MongoDBSearch %ss: %w", clusterName, cleanup.kind, err))
			continue
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			errs = errors.Join(errs, xerrors.Errorf("failed extracting removed cluster %q MongoDBSearch %s list: %w", clusterName, cleanup.kind, err))
			continue
		}
		for _, item := range items {
			obj, ok := item.(client.Object)
			if !ok {
				continue
			}
			current, ok := obj.DeepCopyObject().(client.Object)
			if !ok {
				continue
			}
			if err := listReader.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				errs = errors.Join(errs, xerrors.Errorf("failed confirming removed cluster %q MongoDBSearch %s %s: %w", clusterName, cleanup.kind, obj.GetName(), err))
				continue
			}
			if cleanup.name != "" && current.GetName() != cleanup.name {
				continue
			}
			if current.GetUID() != obj.GetUID() ||
				current.GetLabels()[khandler.MongoDBSearchOwnerUIDLabel] != string(search.UID) {
				continue
			}
			if !cleanup.omitCluster && current.GetLabels()[khandler.MongoDBSearchClusterNameLabel] != clusterName {
				continue
			}
			if cleanup.component != "" && current.GetLabels()[khandler.MongoDBSearchComponentLabel] != cleanup.component {
				continue
			}
			if err := deleteClient.Delete(ctx, current, client.Preconditions{
				UID:             ptr.To(current.GetUID()),
				ResourceVersion: ptr.To(current.GetResourceVersion()),
			}); err != nil && !apierrors.IsNotFound(err) {
				errs = errors.Join(errs, xerrors.Errorf("failed deleting removed cluster %q MongoDBSearch %s %s: %w", clusterName, cleanup.kind, obj.GetName(), err))
				continue
			}
			log.Infof("Deleted removed cluster %q MongoDBSearch %s %s", clusterName, cleanup.kind, obj.GetName())
		}
	}
	return true, errs
}

func confirmSearchClusterRemoved(ctx context.Context, reader client.Reader, expected *searchv1.MongoDBSearch, clusterName string) (bool, error) {
	current := &searchv1.MongoDBSearch{}
	if err := reader.Get(ctx, expected.NamespacedName(), current); err != nil {
		return false, xerrors.Errorf("failed confirming cluster %q removal from MongoDBSearch %s: %w", clusterName, expected.NamespacedName(), err)
	}
	if current.UID != expected.UID {
		return false, nil
	}
	return isRemovedSearchOperatorCluster(current, clusterName), nil
}

func (r *MongoDBSearchReconciler) sweepOwnedResourceKind(
	ctx context.Context,
	search types.NamespacedName,
	listReader client.Reader,
	parentReader client.Reader,
	deleteClient kubernetesClient.Client,
	clusterName string,
	kind string,
	list client.ObjectList,
	selector client.MatchingLabels,
	log *zap.SugaredLogger,
) error {
	if err := listReader.List(ctx, list, client.InNamespace(search.Namespace), selector); err != nil {
		return xerrors.Errorf("failed listing %ss for deleted MongoDBSearch %s on cluster %q: %w", kind, search, clusterName, err)
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return xerrors.Errorf("failed extracting %s list for deleted MongoDBSearch %s on cluster %q: %w", kind, search, clusterName, err)
	}

	var errs error
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok {
			continue
		}
		eligible, err := searchCleanupCandidateEligible(ctx, search, parentReader, clusterName, kind, obj)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if !eligible {
			continue
		}
		log.Infof("Deleting %s %s for deleted MongoDBSearch %s on cluster %q", kind, obj.GetName(), search, clusterName)
		if err := deleteClient.Delete(ctx, obj, client.Preconditions{
			UID:             ptr.To(obj.GetUID()),
			ResourceVersion: ptr.To(obj.GetResourceVersion()),
		}); err != nil && !apierrors.IsNotFound(err) {
			errs = errors.Join(errs, xerrors.Errorf("failed deleting %s %s for deleted MongoDBSearch %s on cluster %q: %w", kind, obj.GetName(), search, clusterName, err))
		}
	}
	return errs
}

func searchCleanupCandidateEligible(
	ctx context.Context,
	search types.NamespacedName,
	parentReader client.Reader,
	clusterName string,
	kind string,
	obj client.Object,
) (bool, error) {
	ownerUID := obj.GetLabels()[khandler.MongoDBSearchOwnerUIDLabel]
	if ownerUID == "" {
		return false, nil
	}
	if parentReader == nil {
		return false, xerrors.Errorf("no live reader available to verify MongoDBSearch %s on cluster %q before deleting %s %s", search, clusterName, kind, obj.GetName())
	}

	localSearch := &searchv1.MongoDBSearch{}
	if err := parentReader.Get(ctx, search, localSearch); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
			return true, nil
		}
		return false, xerrors.Errorf("failed reading MongoDBSearch %s on cluster %q before deleting %s %s: %w", search, clusterName, kind, obj.GetName(), err)
	}
	return ownerUID != string(localSearch.UID), nil
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
		{obj: &searchv1.MongoDBSearch{}, handler: &handler.EnqueueRequestForObject{}},
		{obj: &mdbv1.MongoDB{}, handler: &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch}},
		{obj: &mdbcv1.MongoDBCommunity{}, handler: &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch}},
		{obj: &appsv1.Deployment{}, handler: searchOwnerHandler, predicates: searchOwnerPredicates},
		{obj: &appsv1.StatefulSet{}, handler: searchOwnerHandler, predicates: searchOwnerPredicates},
		{obj: &corev1.Service{}, handler: searchOwnerHandler, predicates: searchOwnerPredicates},
		{obj: &corev1.Secret{}, handler: &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch, MapFunc: khandler.EnqueueMemberClusterObjectToSearch}},
		{obj: &corev1.ConfigMap{}, handler: &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch, MapFunc: khandler.EnqueueMemberClusterObjectToSearch}},
	}
}

func memberMongoDBSearchResourceWatches(r *MongoDBSearchReconciler) []mongoDBSearchResourceWatch {
	searchOwnerHandler := handler.EnqueueRequestsFromMapFunc(khandler.EnqueueMemberClusterObjectToSearch)
	searchOwnerPredicates := []predicate.Predicate{watch.PredicatesForMultiClusterSearchResource()}
	return []mongoDBSearchResourceWatch{
		{obj: &appsv1.Deployment{}, handler: searchOwnerHandler, predicates: searchOwnerPredicates},
		{obj: &appsv1.StatefulSet{}, handler: searchOwnerHandler, predicates: searchOwnerPredicates},
		{obj: &corev1.Service{}, handler: searchOwnerHandler, predicates: searchOwnerPredicates},
		{obj: &corev1.ConfigMap{}, handler: &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch, MapFunc: khandler.EnqueueMemberClusterObjectToSearch}},
		{obj: &corev1.Secret{}, handler: &watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: r.watch, MapFunc: khandler.EnqueueMemberClusterObjectToSearch}},
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

var errSearchSourceNotFound = errors.New("MongoDBSearch source not found")

// getSearchSource resolves the source database for a MongoDBSearch resource.
// Shared by both the main search controller and the Envoy controller.
func getSearchSource(ctx context.Context, kubeClient client.Reader, watcher *watch.ResourceWatcher, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (searchcontroller.SearchSourceDBResource, error) {
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
		if watcher != nil {
			watcher.AddWatchedResourceIfNotAdded(sourceMongoDBResourceRef.Name, sourceMongoDBResourceRef.Namespace, watch.MongoDB, search.NamespacedName())
		}
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
		if watcher != nil {
			watcher.AddWatchedResourceIfNotAdded(sourceMongoDBResourceRef.Name, sourceMongoDBResourceRef.Namespace, "MongoDBCommunity", search.NamespacedName())
		}
		return searchcontroller.NewCommunityResourceSearchSource(mdbc), nil
	}

	return nil, xerrors.Errorf("%w: no database resource named %s", errSearchSourceNotFound, sourceName)
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

	memberClusterReadersMap := make(map[string]client.Reader, len(memberClusterObjectsMap))
	for clusterName, memberCluster := range memberClusterObjectsMap {
		memberClusterReadersMap[clusterName] = memberCluster.GetAPIReader()
	}
	r := newMongoDBSearchReconciler(
		mgr.GetClient(),
		mgr.GetAPIReader(),
		operatorSearchConfig,
		multicluster.ClustersMapToClientMap(memberClusterObjectsMap),
		memberClusterReadersMap,
		operatorClusterName,
	)

	c, err := controller.New(util.MongoDbSearchController, mgr, controller.Options{
		Reconciler:              r,
		MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1), // nolint:forbidigo
	})
	if err != nil {
		return err
	}

	for _, w := range centralMongoDBSearchResourceWatches(r) {
		if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), w.obj, w.handler, w.predicates...)); err != nil {
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
		for k, v := range memberClusterObjectsMap {
			for _, w := range memberMongoDBSearchResourceWatches(r) {
				if err := c.Watch(source.Kind[client.Object](v.GetCache(), w.obj, w.handler, w.predicates...)); err != nil {
					return xerrors.Errorf("failed to set MongoDBSearch member-cluster watch on %s for %T: %w", k, w.obj, err)
				}
			}
		}
	}

	return nil
}
