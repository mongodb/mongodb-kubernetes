package operator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeCluster "sigs.k8s.io/controller-runtime/pkg/cluster"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
)

// Some of these variables can be exposed as configuration to the user
const (
	envoyReplicasDefault = int32(1)

	EnvoyAdminPort = 9901

	envoyServerCertPath = "/etc/envoy/tls/server"
	envoyClientCertPath = "/etc/envoy/tls/client"
	envoyCACertPath     = "/etc/envoy/tls/ca"
	envoyConfigPath     = "/etc/envoy"

	// CA key in the MongoDB CA ConfigMap
	envoyCAKey = "ca-pem"

	envoyConfigHashAnnotation = "mongodb.com/envoy-config-hash"

	labelName = "search-proxy"
)

// envoyRoute defines routing information for one Envoy entrypoint.
// Per-shard routes carry exactly one UpstreamHosts entry (the shard's mongot Service FQDN).
// The cluster-level route carries N entries — one per shard mongot Service in that cluster —
// so mongos can reach any shard's mongot pool through a single SNI/filter chain.
type envoyRoute struct {
	Name          string   // identifier: shard name (e.g., "mdb-sh-0"), "rs", or "cluster-level"
	NameSafe      string   // identifier safe for Envoy (hyphens replaced with underscores)
	ClusterID     string   // member cluster name in MC; "" in single-cluster installs
	SNIHostname   string   // FQDN of the proxy service for SNI matching
	UpstreamHosts []string // FQDNs of the mongot headless services (one per pool member)
	UpstreamPort  int32    // typically 27028
	// RoutedFromAnotherShard indicates this is a fallback route for a pending mongot group.
	// When true, the LDS filter chain routes to the cluster-level cluster and injects
	// the "search-envoy-metadata-bin" gRPC binary header (routed_from_another_shard) so mongot returns empty results.
	RoutedFromAnotherShard bool
}

type MongoDBSearchEnvoyReconciler struct {
	kubeClient          kubernetesClient.Client
	watch               *watch.ResourceWatcher
	defaultEnvoyImage   string
	operatorClusterName string
	memberClients       map[string]kubernetesClient.Client

	prepareSearch prepareSearchFunc
	clusterRouter searchcontroller.SearchClusterRouter
}

func newMongoDBSearchEnvoyReconciler(c client.Client, defaultEnvoyImage string, memberClustersMap map[string]client.Client, operatorClusterName string) *MongoDBSearchEnvoyReconciler {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClustersMap))
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	central := kubernetesClient.NewClient(c)
	return &MongoDBSearchEnvoyReconciler{
		kubeClient:          central,
		watch:               watch.NewResourceWatcher(),
		defaultEnvoyImage:   defaultEnvoyImage,
		operatorClusterName: operatorClusterName,
		memberClients:       clientsMap,
		prepareSearch:       newPrepareSearch(operatorClusterName),
		clusterRouter:       searchcontroller.NewSearchClusterRouter(central, clientsMap, operatorClusterName),
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status},verbs=*,namespace=placeholder
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
func (r *MongoDBSearchEnvoyReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("MongoDBSearchEnvoy", request.NamespacedName)
	log.Info("-> MongoDBSearchEnvoy.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, mdbSearch, log); err != nil {
		return result, err
	}

	// Envoy validation failures surface on /status/loadBalancer so the Envoy sub-status
	// stays authoritative for LB shape errors.
	if skip, result, err := r.prepareSearch(mdbSearch, log,
		func(st workflow.Status) (reconcile.Result, error) {
			if !mdbSearch.IsLBModeManaged() && mdbSearch.Status.LoadBalancer == nil {
				// No LB configured and no sub-status to correct: don't invent a
				// /status/loadBalancer for a validation failure the main controller
				// already surfaces on the phase.
				return st.ReconcileResult()
			}
			return r.updateLBStatus(ctx, mdbSearch, st, log)
		}); skip {
		return result, err
	}

	// TODO: can we find a better cleanup mechanism, and optimize the watching of the loadbalancer field by this controller ?
	// Only act when lb.mode == Managed.
	// If LB was previously active (status exists), clean up Envoy resources first.
	if !mdbSearch.IsLBModeManaged() {
		if mdbSearch.Status.LoadBalancer != nil {
			r.deleteEnvoyResources(ctx, mdbSearch, r.buildClusterWorkList(mdbSearch), log)
			r.clearLBStatus(ctx, mdbSearch, log)
		}
		return reconcile.Result{}, nil
	}

	// Fail fast if the envoy image is not configured, this is a terminal config error and should not be re-enqueued
	if _, err := r.envoyContainerImage(); err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Invalid("%s", err), log)
	}

	// Resolve the source database (shared with the main search controller).
	searchSource, err := getSearchSource(ctx, r.kubeClient, r.watch, mdbSearch, log)
	if err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("Waiting for search source: %s", err), log)
	}
	r.watch.AddWatchedResourceIfNotAdded(
		searchcontroller.SearchStateCMName(mdbSearch),
		mdbSearch.Namespace,
		watch.ConfigMap,
		mdbSearch.NamespacedName(),
	)

	// Load the per-CR state for the routing-readiness switch.
	state, err := searchcontroller.ReadSearchState(ctx, r.kubeClient, mdbSearch)
	if err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("Waiting for search state: %s", err), log)
	}

	tlsCfg := searchSource.TLSConfig()
	tlsEnabled := mdbSearch.IsTLSConfigured()

	workList := r.buildClusterWorkList(mdbSearch)
	var reconcileErrs error
	var worstPhase status.Phase

	for _, w := range workList {
		var st workflow.Status
		switch w.Client {
		case nil:
			st = workflow.Pending("Member cluster %q not registered with the operator", w.ClusterName)
		default:
			st = r.reconcileForCluster(ctx, mdbSearch, searchSource, tlsEnabled, tlsCfg, w, state.RoutingReadyMongotGroups, log)
		}
		worstPhase = searchv1.WorstOfPhase(worstPhase, st.Phase())
		if !st.IsOK() {
			reconcileErrs = errors.Join(reconcileErrs, fmt.Errorf("cluster %q: %s", w.ClusterName, searchcontroller.MessageFromStatus(st)))
		}
	}

	if reconcileErrs != nil {
		// Worst-of phase: preserve the most severe phase seen across clusters.
		// Without this branch the JSON patch would downgrade Failed → Pending.
		if worstPhase == status.PhaseFailed {
			return r.updateLBStatus(ctx, mdbSearch, workflow.Failed(reconcileErrs), log)
		}
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("%s", reconcileErrs), log)
	}

	log.Info("MongoDBSearchEnvoy reconciliation complete")
	return r.updateLBStatus(ctx, mdbSearch, workflow.OK(), log)
}

// clusterWorkItem is one per-cluster unit. OwnerReferences carries the local
// Search GC backstop and is nil for cross-cluster member resources.
type clusterWorkItem struct {
	ClusterName     string
	ClusterIndex    int
	Client          kubernetesClient.Client
	OwnerReferences []metav1.OwnerReference
}

// spec.clusters is validated non-empty, so the empty-clusters branch is a
// defensive backstop only.
func (r *MongoDBSearchEnvoyReconciler) buildClusterWorkList(search *searchv1.MongoDBSearch) []clusterWorkItem {
	if len(search.Spec.Clusters) == 0 {
		return []clusterWorkItem{newClusterWorkItem(r.clusterRouter, search, "", 0)}
	}
	work := make([]clusterWorkItem, 0, len(search.Spec.Clusters))
	for _, c := range search.Spec.Clusters {
		work = append(work, newClusterWorkItem(r.clusterRouter, search, c.Name, c.ResolveIndex()))
	}
	return work
}

// newClusterWorkItem builds one per-cluster work unit. A missing member client
// leaves Client nil; callers report the cluster as not registered. Only local
// clusters get owner references — they do nothing across cluster boundaries.
func newClusterWorkItem(router searchcontroller.SearchClusterRouter, search *searchv1.MongoDBSearch, clusterName string, clusterIndex int) clusterWorkItem {
	c, _ := router.ClientForCluster(clusterName)
	work := clusterWorkItem{
		ClusterName:  clusterName,
		ClusterIndex: clusterIndex,
		Client:       c,
	}
	if router.IsLocalCluster(clusterName) {
		work.OwnerReferences = search.GetOwnerReferences()
	}
	return work
}

// reconcileForCluster runs the ConfigMap + Deployment ensure for one cluster's
// work item. clusterName resolves the cluster's LB config and is used for log
// context; clusterIndex is used for resource naming ONLY (it comes from the
// spec.clusters[i] pin). routingReadyMongotGroups is the state-CM switch —
// shards not listed get fallback routing.
// Returns a workflow.Status describing the per-cluster outcome.
func (r *MongoDBSearchEnvoyReconciler) reconcileForCluster(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	source searchcontroller.SearchSourceDBResource,
	tlsEnabled bool,
	tlsCfg *searchcontroller.TLSSourceConfig,
	w clusterWorkItem,
	routingReadyMongotGroups []string,
	log *zap.SugaredLogger,
) workflow.Status {
	clusterName, clusterIndex := w.ClusterName, w.ClusterIndex
	routes := buildRoutesForCluster(search, source, clusterIndex, clusterName, routingReadyMongotGroups)
	if len(routes) == 0 {
		return workflow.Pending("No routes to configure for load balancer (cluster=%q)", clusterName)
	}

	// Generate Envoy config files: static bootstrap + dynamic CDS/LDS
	caKeyName := caKeyNameFromTLSConfig(tlsCfg)
	bootstrapJSON, err := buildBootstrapJSON()
	if err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	cdsJSON, err := buildCDSJSON(routes, tlsEnabled, caKeyName)
	if err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	managedLB := search.GetManagedLBForCluster(clusterName)
	var retryPolicy *searchv1.EnvoyRetryPolicy
	if managedLB != nil {
		retryPolicy = managedLB.RetryPolicy
	}
	ldsJSON, err := buildLDSJSON(routes, tlsEnabled, caKeyName, retryPolicy)
	if err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}

	if err := r.ensureConfigMap(ctx, search, bootstrapJSON, cdsJSON, ldsJSON, w, log); err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	// Ensure Deployment (hash only bootstrap — CDS/LDS are hot-reloaded by Envoy via filesystem xDS)
	if err := r.ensureDeployment(ctx, search, bootstrapJSON, w, managedLB, tlsCfg, log); err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	return workflow.OK()
}

// updateLBStatus patches the loadBalancer sub-status on the MongoDBSearch CR
// and returns the reconcile result derived from the workflow status.
func (r *MongoDBSearchEnvoyReconciler) updateLBStatus(ctx context.Context, search *searchv1.MongoDBSearch, st workflow.Status, log *zap.SugaredLogger) (reconcile.Result, error) {
	partOption := searchv1.NewSearchPartOption(searchv1.SearchPartLoadBalancer)
	return commoncontroller.UpdateStatus(ctx, r.kubeClient, search, st, log, partOption)
}

// clearLBStatus removes the loadBalancer substatus when LB is no longer configured.
// This works because UpdateStatus uses a JSON Patch targeting only /status/loadBalancer,
// so it won't conflict with the main controller patching /status.
func (r *MongoDBSearchEnvoyReconciler) clearLBStatus(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) {
	search.Status.LoadBalancer = nil
	partOption := searchv1.NewSearchPartOption(searchv1.SearchPartLoadBalancer)
	// GetStatus with LB part will return nil, which patches null into /status/loadBalancer
	if _, err := commoncontroller.UpdateStatus(ctx, r.kubeClient, search, workflow.OK(), log, partOption); err != nil {
		log.Warnf("Failed to clear loadBalancer status: %s", err)
	}
}

// deleteEnvoyResources removes per-cluster Envoy resources on managed→unmanaged LB transition.
// Client == nil (cluster not registered) work items are skipped: the reconciler could not
// have created anything for those clusters.
func (r *MongoDBSearchEnvoyReconciler) deleteEnvoyResources(ctx context.Context, search *searchv1.MongoDBSearch, workList []clusterWorkItem, log *zap.SugaredLogger) {
	ns := search.Namespace
	for _, w := range workList {
		if w.Client == nil {
			continue
		}
		depName := search.LoadBalancerDeploymentNameForCluster(w.ClusterIndex)
		cmName := search.LoadBalancerConfigMapNameForCluster(w.ClusterIndex)

		dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: depName, Namespace: ns}}
		if err := w.Client.Delete(ctx, dep); err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Failed to delete Envoy Deployment %s (cluster=%q): %s", depName, w.ClusterName, err)
		} else if err == nil {
			log.Infof("Deleted Envoy Deployment %s (cluster=%q)", depName, w.ClusterName)
		}

		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns}}
		if err := w.Client.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Failed to delete Envoy ConfigMap %s (cluster=%q): %s", cmName, w.ClusterName, err)
		} else if err == nil {
			log.Infof("Deleted Envoy ConfigMap %s (cluster=%q)", cmName, w.ClusterName)
		}
	}
}

// caKeyNameFromTLSConfig returns the CA key filename for Envoy config file paths.
func caKeyNameFromTLSConfig(tlsCfg *searchcontroller.TLSSourceConfig) string {
	if tlsCfg != nil {
		return tlsCfg.CAFileName
	}
	return envoyCAKey
}

// buildShardRoutes builds per-shard routes plus one cluster-level route for a single cluster.
// clusterIndex and clusterName identify the cluster; in single-cluster installs pass (0, "").
// Each per-shard route has one UpstreamHosts entry. The trailing cluster-level route
// aggregates all per-shard mongot Service FQDNs so mongos can use a single SNI/filter chain.
// routingReadyMongotGroups is the state-CM switch; a shard not listed is pending and
// its per-shard filter chain is redirected to the cluster-level cluster with the
// routed_from_another_shard header.
func buildShardRoutes(search *searchv1.MongoDBSearch, shardNames []string, clusterIndex int, clusterName string, routingReadyMongotGroups []string) []envoyRoute {
	// +1 for the cluster-level route appended at the end.
	routes := make([]envoyRoute, 0, len(shardNames)+1)
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	readySet := make(map[string]struct{}, len(routingReadyMongotGroups))
	for _, name := range routingReadyMongotGroups {
		readySet[name] = struct{}{}
	}

	clusterLevelUpstreams := make([]string, 0, len(shardNames))
	allUpstreams := make([]string, 0, len(shardNames))

	for _, shardName := range shardNames {
		sniServiceName := search.ProxyServiceNameForClusterShard(clusterIndex, shardName).Name
		mongotServiceName := search.MongotServiceForClusterShard(clusterIndex, shardName).Name
		upstreamFQDN := fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace)

		sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", sniServiceName, namespace)
		if endpoint := search.GetManagedLBEndpointForClusterShard(clusterName, shardName); endpoint != "" {
			sniHostname = endpoint
		}

		_, ok := readySet[shardName]
		isPending := !ok
		routes = append(routes, envoyRoute{
			Name:                   shardName,
			NameSafe:               strings.ReplaceAll(shardName, "-", "_"),
			ClusterID:              clusterName,
			SNIHostname:            sniHostname,
			UpstreamHosts:          []string{upstreamFQDN},
			UpstreamPort:           mongotPort,
			RoutedFromAnotherShard: isPending,
		})

		// Only include non-pending shards in the cluster-level cluster endpoints.
		allUpstreams = append(allUpstreams, upstreamFQDN)
		if !isPending {
			clusterLevelUpstreams = append(clusterLevelUpstreams, upstreamFQDN)
		}
	}

	// If all shards are pending (fresh install), include all endpoints in cluster-level
	// and disable fallback routing (no healthy target to fall back to).
	if len(clusterLevelUpstreams) == 0 {
		for i := range routes {
			routes[i].RoutedFromAnotherShard = false
		}
		clusterLevelUpstreams = allUpstreams
	}

	// Cluster-level route: mongos in this cluster uses this single SNI chain to reach
	// all local shard mongot pools. SNI is the cluster-level proxy Service FQDN unless
	// the user supplied a managed-LB routerHostname (used verbatim; required for external sharded).
	clusterLevelSvcName := search.ProxyServiceNamespacedNameForCluster(clusterIndex).Name
	clusterLevelSNI := fmt.Sprintf("%s.%s.svc.cluster.local", clusterLevelSvcName, namespace)
	if endpoint := search.GetRouterHostnameForCluster(clusterName); endpoint != "" {
		clusterLevelSNI = endpoint
	}

	routes = append(routes, envoyRoute{
		Name:          "cluster-level",
		NameSafe:      "cluster_level",
		ClusterID:     clusterName,
		SNIHostname:   clusterLevelSNI,
		UpstreamHosts: clusterLevelUpstreams,
		UpstreamPort:  mongotPort,
	})

	return routes
}

// buildRoutesForCluster returns the Envoy routes for one member cluster — the single
// topology-aware path; everything downstream consumes the topology-agnostic envoyRoute.
// clusterName "" is the single-cluster path, but clusterIndex is still the CRD-pinned
// index (a single-entry CR pinned non-zero must not reconcile at 0). routingReadyMongotGroups
// gives a not-yet-listed mongot group fallback routing via the routed_from_another_shard header.
func buildRoutesForCluster(search *searchv1.MongoDBSearch, source searchcontroller.SearchSourceDBResource, clusterIndex int, clusterName string, routingReadyMongotGroups []string) []envoyRoute {
	if shardedSource, ok := source.(searchcontroller.SearchSourceShardedDeployment); ok {
		return buildShardRoutes(search, shardedSource.GetShardNames(), clusterIndex, clusterName, routingReadyMongotGroups)
	}
	return []envoyRoute{buildReplicaSetRouteForCluster(search, clusterIndex, clusterName)}
}

// buildReplicaSetRouteForCluster builds the RS-mode route for one cluster.
// Upstream is the index-suffixed mongot Service — the unindexed name NXDOMAINs
// under STRICT_DNS and fails mongod's gRPC with code 125.
func buildReplicaSetRouteForCluster(search *searchv1.MongoDBSearch, clusterIndex int, clusterName string) envoyRoute {
	mongotServiceName := search.SearchServiceNamespacedNameForCluster(clusterIndex).Name
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	sniServiceName := search.ProxyServiceNamespacedNameForCluster(clusterIndex).Name
	sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", sniServiceName, namespace)
	if endpoint := search.GetManagedLBEndpointForCluster(clusterName); endpoint != "" {
		sniHostname = endpoint
	}

	return envoyRoute{
		Name:          "rs",
		NameSafe:      "rs",
		ClusterID:     clusterName,
		SNIHostname:   sniHostname,
		UpstreamHosts: []string{fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace)},
		UpstreamPort:  mongotPort,
	}
}

// ensureConfigMap creates or updates the Envoy ConfigMap with three files:
// bootstrap.json (static), cds.json (dynamic clusters), and lds.json (dynamic listener).
// Kubernetes ConfigMap updates are atomic (symlink swap), so all files update together.
func (r *MongoDBSearchEnvoyReconciler) ensureConfigMap(ctx context.Context, search *searchv1.MongoDBSearch, bootstrapJSON, cdsJSON, ldsJSON string, w clusterWorkItem, log *zap.SugaredLogger) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerConfigMapNameForCluster(w.ClusterIndex),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, w.Client, cm, func() error {
		cm.OwnerReferences = w.OwnerReferences
		cm.Labels = envoyLabelsForCluster(search, w.ClusterName, w.ClusterIndex)
		cm.Data = map[string]string{
			"bootstrap.json": bootstrapJSON,
			"cds.json":       cdsJSON,
			"lds.json":       ldsJSON,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure Envoy ConfigMap: %w", err)
	}

	log.Infof("Envoy ConfigMap created/updated (cluster=%q)", w.ClusterName)
	return nil
}

// envoyConfigHash hashes whitespace-normalized JSON: protojson output formatting
// is deliberately randomized per compiled binary, so hashing raw bytes would roll
// the Deployment on every operator rebuild.
func envoyConfigHash(configJSON string) (string, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(configJSON)); err != nil {
		return "", fmt.Errorf("failed to compact Envoy config for hashing: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(compact.Bytes())), nil
}

// ensureDeployment creates or updates the Envoy Deployment.
// The config hash is computed from bootstrapJSON only — CDS/LDS changes are
// hot-reloaded by Envoy via filesystem xDS and do not require a pod restart.
func (r *MongoDBSearchEnvoyReconciler) ensureDeployment(ctx context.Context, search *searchv1.MongoDBSearch, bootstrapJSON string, w clusterWorkItem, managedLB *searchv1.ManagedLBConfig, tlsCfg *searchcontroller.TLSSourceConfig, log *zap.SugaredLogger) error {
	configHash, err := envoyConfigHash(bootstrapJSON)
	if err != nil {
		return err
	}
	replicas := envoyReplicas(managedLB)
	labels := envoyLabelsForCluster(search, w.ClusterName, w.ClusterIndex)
	podLabels := envoyPodLabelsForCluster(search, w.ClusterIndex)
	tlsEnabled := search.IsTLSConfigured()
	image, err := r.envoyContainerImage()
	if err != nil {
		return err
	}
	resources := envoyResourceRequirements(managedLB)
	managedSecurityContext := env.ReadBoolOrDefault(podtemplatespec.ManagedSecurityContextEnv, false) // nolint:forbidigo

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerDeploymentNameForCluster(w.ClusterIndex),
			Namespace: search.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, w.Client, dep, func() error {
		dep.OwnerReferences = w.OwnerReferences
		dep.Labels = labels

		podAnnotations := merge.StringToStringMap(dep.Spec.Template.Annotations, map[string]string{
			envoyConfigHashAnnotation: configHash,
		})

		dep.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: podAnnotations,
				},
				Spec: buildEnvoyPodSpec(search, w.ClusterIndex, tlsCfg, tlsEnabled, image, resources, managedSecurityContext),
			},
		}

		// Apply user deployment configuration override
		if managedLB != nil && managedLB.Deployment != nil {
			dep.Spec = merge.DeploymentSpecs(dep.Spec, managedLB.Deployment.SpecWrapper.Spec)
			dep.Labels = merge.StringToStringMap(dep.Labels, managedLB.Deployment.MetadataWrapper.Labels)
			dep.Annotations = merge.StringToStringMap(dep.Annotations, managedLB.Deployment.MetadataWrapper.Annotations)
		}
		// Identity labels are merged after user label overrides so users cannot
		// detach the Deployment from its owning MongoDBSearch.
		dep.Labels = merge.StringToStringMap(dep.Labels, labels)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure Envoy Deployment: %w", err)
	}

	log.Infof("Envoy Deployment created/updated (cluster=%q)", w.ClusterName)
	return nil
}

// buildEnvoyPodSpec builds the PodSpec for the Envoy Deployment.
// tlsCfg may be nil if TLS is not configured on the source.
//
// clusterIndex selects the per-cluster ConfigMap volume name. Without this,
// MC pods mount a ConfigMap that does not exist in the member cluster and
// Envoy never starts.
func buildEnvoyPodSpec(search *searchv1.MongoDBSearch, clusterIndex int, tlsCfg *searchcontroller.TLSSourceConfig, tlsEnabled bool, image string, resources corev1.ResourceRequirements, managedSecurityContext bool) corev1.PodSpec {
	volumes := []corev1.Volume{
		{
			Name: "envoy-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: search.LoadBalancerConfigMapNameForCluster(clusterIndex)},
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "envoy-config", MountPath: envoyConfigPath, ReadOnly: true},
	}

	if tlsEnabled && tlsCfg != nil {
		// Use the CA volume from TLSSourceConfig directly (already ConfigMap or Secret).
		// Add Items to project only the CA key into the mount path.
		caVolume := tlsCfg.CAVolume
		caVolume.Name = "ca-cert"
		if caVolume.Secret != nil {
			caVolume.Secret.Items = []corev1.KeyToPath{{Key: tlsCfg.CAFileName, Path: tlsCfg.CAFileName}}
		} else if caVolume.ConfigMap != nil {
			caVolume.ConfigMap.Items = []corev1.KeyToPath{{Key: tlsCfg.CAFileName, Path: tlsCfg.CAFileName}}
		}

		volumes = append(volumes,
			corev1.Volume{
				Name: "envoy-server-cert",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: search.LoadBalancerServerCert(clusterIndex).Name},
				},
			},
			corev1.Volume{
				Name: "envoy-client-cert",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: search.LoadBalancerClientCert(clusterIndex).Name},
				},
			},
			caVolume,
		)

		volumeMounts = append(volumeMounts,
			corev1.VolumeMount{Name: "envoy-server-cert", MountPath: envoyServerCertPath, ReadOnly: true},
			corev1.VolumeMount{Name: "envoy-client-cert", MountPath: envoyClientCertPath, ReadOnly: true},
			corev1.VolumeMount{Name: "ca-cert", MountPath: envoyCACertPath, ReadOnly: true},
		)
	}

	var podSecurityContext *corev1.PodSecurityContext
	var containerSecurityContext *corev1.SecurityContext
	if !managedSecurityContext {
		psc := podtemplatespec.DefaultPodSecurityContext()
		podSecurityContext = &psc
		csc := container.DefaultSecurityContext()
		containerSecurityContext = &csc
	}

	return corev1.PodSpec{
		SecurityContext: podSecurityContext,
		// Outlive mongot's grace by 10s so in-flight mongod→Envoy→mongot cursor
		// streams (drained gracefully by the preStop hook) finish before SIGKILL
		// during a rolling restart.
		TerminationGracePeriodSeconds: ptr.To(searchv1.EnvoyTerminationGracePeriodSeconds),
		Affinity: &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
					{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": search.LoadBalancerDeploymentNameForCluster(clusterIndex),
								},
							},
							TopologyKey: util.DefaultAntiAffinityTopologyKey,
						},
					},
				},
			},
		},
		Containers: []corev1.Container{
			{
				Name:    "envoy",
				Image:   image,
				Command: []string{"/usr/local/bin/envoy"},
				// --log-format emits component logs as JSON so they share a
				// uniform shape with the JSON access log we configure in the
				// HCM (see buildHCMAccessLog). %j escapes the message body
				// (which can carry quotes from envoy "[Tags: ... ]" frames)
				// so each line stays as a single well-formed JSON object.
				//
				// Token notes (spdlog):
				//   time: %Y-%m-%dT%H:%M:%S.%e%z → ISO 8601 with ms and ±HH:MM
				//     offset (matches mongod/mongot timestamp shape so any
				//     downstream tooling can sort cross-layer by wall clock).
				//   loc: %g:%# → "<source-path>:<line>". The field is named ``loc``
				//     (not ``source``) on purpose to not confuse some log parsing
				//     utilities, e.g. lnav.
				Args: []string{
					"-c", "/etc/envoy/bootstrap.json",
					"--log-level", "info",
					"--log-format", `{"time":"%Y-%m-%dT%H:%M:%S.%e%z","level":"%l","logger":"%n","thread":%t,"loc":"%g:%#","message":"%j"}`,
					// Pairs with the preStop drain: an immediate strategy over a 30s
					// window (< the preStop sleep) GOAWAYs every downstream connection
					// promptly, so mongod migrates new $search streams to a healthy Envoy
					// before SIGTERM. Envoy's 600s default would outlast the drain window.
					"--drain-time-s", "30",
					"--drain-strategy", "immediate",
				},
				Ports: []corev1.ContainerPort{
					{Name: "mongot-grpc", ContainerPort: searchv1.EnvoyDefaultProxyPort},
					{Name: "admin", ContainerPort: EnvoyAdminPort},
				},
				Resources:       resources,
				SecurityContext: containerSecurityContext,
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/ready",
							Port: intstr.FromInt32(EnvoyAdminPort),
						},
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       5,
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/ready",
							Port: intstr.FromInt32(EnvoyAdminPort),
						},
					},
					InitialDelaySeconds: 10,
					PeriodSeconds:       10,
					FailureThreshold:    3,
				},
				Lifecycle: &corev1.Lifecycle{
					PreStop: &corev1.LifecycleHandler{
						// Envoy's /drain_listeners is POST-only and the image ships bash but
						// no curl, hence the hand-rolled POST over bash's /dev/tcp. graceful=true
						// GOAWAYs downstream so new streams move to a healthy Envoy while in-flight
						// getMores finish. The drain is best-effort (subshell + `|| true`); the
						// trailing sleep always runs and defers SIGTERM until the drain completes.
						Exec: &corev1.ExecAction{
							Command: []string{
								"/usr/bin/bash", "-c",
								fmt.Sprintf(
									"( exec 3<>/dev/tcp/127.0.0.1/%d; "+
										"printf 'POST /drain_listeners?graceful=true HTTP/1.1\\r\\nHost: localhost\\r\\nContent-Length: 0\\r\\nConnection: close\\r\\n\\r\\n' >&3; "+
										"cat <&3 >/dev/null ) || true; sleep %d",
									EnvoyAdminPort, searchv1.EnvoyPreStopDrainSleepSeconds,
								),
							},
						},
					},
				},
				VolumeMounts: volumeMounts,
			},
		},
		Volumes: volumes,
	}
}

// envoyContainerImage returns the envoy image from the MDB_ENVOY_IMAGE env var.
// Returns an error if the env var is not set.
func (r *MongoDBSearchEnvoyReconciler) envoyContainerImage() (string, error) {
	if r.defaultEnvoyImage == "" {
		return "", fmt.Errorf("%s environment variable must be set on the operator to use managed load balancer", util.EnvoyImageEnv)
	}
	return r.defaultEnvoyImage, nil
}

// envoyResourceRequirements returns the cluster's resource requirements
// or the defaults (100m/128Mi requests, 500m/512Mi limits).
func envoyResourceRequirements(managedLB *searchv1.ManagedLBConfig) corev1.ResourceRequirements {
	if managedLB != nil && managedLB.ResourceRequirements != nil {
		return *managedLB.ResourceRequirements
	}
	return defaultEnvoyResourceRequirements()
}

func defaultEnvoyResourceRequirements() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
}

// envoyLabelsForCluster returns Envoy resource labels including the cross-cluster
// enqueue labels and an optional cluster-name label. In single-cluster (clusterName
// == "") the cluster-name label is omitted. Both stamp the cross-cluster enqueue
// labels so the label-based mapper can route Deployment/ConfigMap events back
// to the owning MongoDBSearch even when objects live in a member cluster (where
// owner refs don't GC).
// clusterIndex is used for the "app" label (resource name); clusterName is used
// for the cluster-name label so label selectors remain name-keyed.
func envoyLabelsForCluster(search *searchv1.MongoDBSearch, clusterName string, clusterIndex int) map[string]string {
	return khandler.SearchManagedLabels(search, search.LoadBalancerDeploymentNameForCluster(clusterIndex), labelName, clusterName)
}

// envoyReplicas returns the cluster's desired Envoy replica count.
// Uses spec.clusters[].loadBalancer.managed.replicas if set, otherwise defaults to 1.
func envoyReplicas(managedLB *searchv1.ManagedLBConfig) int32 {
	if managedLB != nil && managedLB.Replicas != nil {
		return *managedLB.Replicas
	}
	return envoyReplicasDefault
}

// envoyPodLabelsForCluster returns Envoy pod-selection labels for one cluster.
// The "app" label uses the per-cluster Deployment name so Pods stay distinct
// per (cluster, namespace) — Pod names already carry the Deployment prefix.
func envoyPodLabelsForCluster(search *searchv1.MongoDBSearch, clusterIndex int) map[string]string {
	return map[string]string{
		"app": search.LoadBalancerDeploymentNameForCluster(clusterIndex),
	}
}

// Controller Registration
//
// memberClusterObjectsMap is the same map main.go passes to AddMongoDBSearchController.
// Empty in single-cluster installs — the controller behaves identically to before
// when the map is empty.
//
// For each member cluster we register watches on Envoy Deployment + ConfigMap
// using the label-based mapper (cross-cluster owner refs do not GC).
func AddMongoDBSearchEnvoyController(ctx context.Context, mgr manager.Manager, defaultEnvoyImage string, memberClusterObjectsMap map[string]runtimeCluster.Cluster, operatorClusterName string) error {
	// NOTE: The field index for MongoDBSearchIndexFieldName is already registered
	// by AddMongoDBSearchController. Do not register it again here.

	r := newMongoDBSearchEnvoyReconciler(mgr.GetClient(), defaultEnvoyImage, multicluster.ClustersMapToClientMap(memberClusterObjectsMap), operatorClusterName)

	c, err := controller.New("mongodbsearchenvoy", mgr, controller.Options{
		Reconciler:              r,
		MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1), // nolint:forbidigo
	})
	if err != nil {
		return err
	}

	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &searchv1.MongoDBSearch{}, &handler.EnqueueRequestForObject{})); err != nil {
		return err
	}
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbv1.MongoDB{}, &watch.ResourcesHandler{ResourceType: watch.MongoDB, ResourceWatcher: r.watch})); err != nil {
		return err
	}
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbcv1.MongoDBCommunity{}, &watch.ResourcesHandler{ResourceType: "MongoDBCommunity", ResourceWatcher: r.watch})); err != nil {
		return err
	}
	mapper := handler.EnqueueRequestsFromMapFunc(khandler.EnqueueMemberClusterObjectToSearch)
	searchOwnerPredicate := watch.PredicatesForMultiClusterSearchResource()
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &appsv1.Deployment{}, mapper, searchOwnerPredicate)); err != nil {
		return err
	}
	// Plain ResourcesHandler: only registered dependency ConfigMaps (e.g. CA bundles)
	// route create/update events; the rendered LB ConfigMap is not registered, so
	// manual edits to it never trigger a re-render (Envoy hot-reloads it in place).
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch})); err != nil {
		return err
	}

	// Per-member-cluster Envoy resource watches: label-based mapper, since
	// cross-cluster owner refs don't GC. Same pattern as the AppDB MC and
	// sharded MC controllers (see appdbreplicaset_controller.go and
	// mongodbshardedcluster_controller.go).
	for k, v := range memberClusterObjectsMap {
		if err := c.Watch(source.Kind[client.Object](v.GetCache(), &appsv1.Deployment{}, mapper, searchOwnerPredicate)); err != nil {
			return fmt.Errorf("failed to set Envoy Deployment watch on member cluster %s: %w", k, err)
		}
		if err := c.Watch(source.Kind[client.Object](v.GetCache(), &corev1.ConfigMap{}, mapper, searchOwnerPredicate)); err != nil {
			return fmt.Errorf("failed to set Envoy ConfigMap watch on member cluster %s: %w", k, err)
		}
	}

	return nil
}
