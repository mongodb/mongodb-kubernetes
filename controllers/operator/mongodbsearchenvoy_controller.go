package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
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

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/envvar"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// Some of these variables can be exposed as configuration to the user
const (
	envoyReplicasDefault = int32(1)

	envoyAdminPort = 9901

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
}

type MongoDBSearchEnvoyReconciler struct {
	kubeClient        kubernetesClient.Client
	watch             *watch.ResourceWatcher
	defaultEnvoyImage string

	// memberClusterClientsMap is keyed by the member cluster name and holds the
	// per-cluster Kubernetes client. Empty in single-cluster installs; the
	// Reconcile path falls back to kubeClient (resolved in buildClusterWorkList).
	memberClusterClientsMap map[string]kubernetesClient.Client
}

func newMongoDBSearchEnvoyReconciler(c client.Client, defaultEnvoyImage string, memberClustersMap map[string]client.Client) *MongoDBSearchEnvoyReconciler {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClustersMap))
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	return &MongoDBSearchEnvoyReconciler{
		kubeClient:              kubernetesClient.NewClient(c),
		watch:                   watch.NewResourceWatcher(),
		defaultEnvoyImage:       defaultEnvoyImage,
		memberClusterClientsMap: clientsMap,
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

	// TODO: can we find a better cleanup mechanism, and optimize the watching of the loadbalancer field by this controller ?
	// Only act when lb.mode == Managed.
	// If LB was previously active (status exists), clean up Envoy resources first.
	if !mdbSearch.IsLBModeManaged() {
		if mdbSearch.Status.LoadBalancer != nil {
			state, _, stErr := loadOrInitSearchState(ctx, r.kubeClient, mdbSearch)
			var workList []clusterWorkItem
			if stErr != nil {
				log.Warnf("Failed to load search state for Envoy cleanup, falling back to central only: %s", stErr)
				workList = []clusterWorkItem{{ClusterName: "", ClusterIndex: 0, Client: r.kubeClient}}
			} else {
				workList = r.buildClusterWorkList(mdbSearch, state.ClusterMapping)
			}
			r.deleteEnvoyResources(ctx, mdbSearch, workList, log)
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

	// Load the per-CR state to get the stable clusterName → clusterIndex mapping.
	state, _, err := loadOrInitSearchState(ctx, r.kubeClient, mdbSearch)
	if err != nil {
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("Waiting for search state: %s", err), log)
	}

	tlsCfg := searchSource.TLSConfig()
	tlsEnabled := mdbSearch.IsTLSConfigured()

	workList := r.buildClusterWorkList(mdbSearch, state.ClusterMapping)
	var firstFailure error
	var worstPhase status.Phase

	for _, w := range workList {
		var st workflow.Status
		if w.ClusterIndex == -1 {
			// Cluster not yet registered in state mapping; Envoy controller
			// reconciles after the main search controller writes the mapping.
			st = workflow.Pending("Waiting for cluster %q to be registered in search state", w.ClusterName)
		} else {
			st = r.reconcileForCluster(ctx, mdbSearch, searchSource, tlsEnabled, tlsCfg, w.ClusterName, w.ClusterIndex, w.Client, log)
		}
		worstPhase = searchv1.WorstOfPhase(worstPhase, st.Phase())
		if !st.IsOK() && firstFailure == nil {
			firstFailure = fmt.Errorf("cluster %q: %s", w.ClusterName, searchcontroller.MessageFromStatus(st))
		}
	}

	if firstFailure != nil {
		// Worst-of phase: preserve the most severe phase seen across clusters.
		// Without this branch the JSON patch would downgrade Failed → Pending.
		if worstPhase == status.PhaseFailed {
			return r.updateLBStatus(ctx, mdbSearch, workflow.Failed(firstFailure), log)
		}
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("%s", firstFailure), log)
	}

	log.Info("MongoDBSearchEnvoy reconciliation complete")
	return r.updateLBStatus(ctx, mdbSearch, workflow.OK(), log)
}

// clusterWorkItem represents one (clusterName, clusterIndex, client) unit the reconciler must process.
// In single-cluster (no spec.clusters or empty memberClusterClientsMap) the slice
// has one entry with ClusterName == "" and ClusterIndex == 0.
// ClusterIndex == -1 is a sentinel meaning the cluster has not yet been registered
// in the state mapping; the reconciler surfaces a per-cluster Pending for those.
type clusterWorkItem struct {
	ClusterName  string
	ClusterIndex int
	Client       kubernetesClient.Client
}

// buildClusterWorkList expands spec.clusters[] into the per-cluster work units
// the reconciler will iterate over. Membership rules:
//   - len(memberClusterClientsMap) == 0 → single-cluster install; one work item with ClusterName="" and ClusterIndex=0.
//   - len(spec.clusters) == 0 → single-cluster degenerate; one work item with ClusterName="" and ClusterIndex=0.
//   - otherwise → one work item per spec.clusters[i]. ClusterIndex is resolved from
//     mapping; -1 if the cluster is not yet in the mapping (first reconcile race).
func (r *MongoDBSearchEnvoyReconciler) buildClusterWorkList(search *searchv1.MongoDBSearch, mapping map[string]int) []clusterWorkItem {
	if len(r.memberClusterClientsMap) == 0 || search.Spec.Clusters == nil || len(*search.Spec.Clusters) == 0 {
		return []clusterWorkItem{{ClusterName: "", ClusterIndex: 0, Client: r.kubeClient}}
	}
	work := make([]clusterWorkItem, 0, len(*search.Spec.Clusters))
	for _, c := range *search.Spec.Clusters {
		idx, ok := mapping[c.ClusterName]
		if !ok {
			idx = -1
		}
		cl, ok := r.memberClusterClientsMap[c.ClusterName]
		if !ok {
			cl = r.kubeClient
		}
		work = append(work, clusterWorkItem{ClusterName: c.ClusterName, ClusterIndex: idx, Client: cl})
	}
	return work
}

// reconcileForCluster runs the ConfigMap + Deployment ensure for one cluster.
// clusterName is used for log context; clusterIndex is used for resource naming;
// c is the pre-resolved Kubernetes client for the target cluster.
// Returns a workflow.Status describing the per-cluster outcome.
func (r *MongoDBSearchEnvoyReconciler) reconcileForCluster(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	source searchcontroller.SearchSourceDBResource,
	tlsEnabled bool,
	tlsCfg *searchcontroller.TLSSourceConfig,
	clusterName string,
	clusterIndex int,
	c kubernetesClient.Client,
	log *zap.SugaredLogger,
) workflow.Status {
	// defensive: belt-and-braces guard against an unknown-name path that should
	// already be caught by ClusterIndex == -1 upstream in the work-list loop.
	if clusterName != "" {
		if _, ok := r.memberClusterClientsMap[clusterName]; !ok {
			return workflow.Pending("Member cluster %q not registered with the operator", clusterName)
		}
	}

	routes := buildRoutesForCluster(search, source, clusterIndex, clusterName)
	if len(routes) == 0 {
		return workflow.Pending("No routes to configure for load balancer (cluster=%q)", clusterName)
	}

	caKeyName := caKeyNameFromTLSConfig(tlsCfg)
	envoyJSON, err := buildEnvoyConfigJSON(routes, tlsEnabled, caKeyName)
	if err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	if err := r.ensureConfigMap(ctx, search, envoyJSON, clusterName, clusterIndex, c, log); err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	if err := r.ensureDeployment(ctx, search, envoyJSON, clusterName, clusterIndex, c, tlsCfg, log); err != nil {
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
// ClusterIndex == -1 sentinel is skipped: the cluster isn't yet in state and could not have created anything.
func (r *MongoDBSearchEnvoyReconciler) deleteEnvoyResources(ctx context.Context, search *searchv1.MongoDBSearch, workList []clusterWorkItem, log *zap.SugaredLogger) {
	ns := search.Namespace
	for _, w := range workList {
		if w.ClusterIndex == -1 {
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

// buildRoutes returns the Envoy routes for the given topology.
// It is the single topology-aware path in the controller. Everything downstream (config generation,
// Service creation, cleanup) is topology-agnostic, using the envoyRoute data structure only.
func buildRoutes(search *searchv1.MongoDBSearch, source searchcontroller.SearchSourceDBResource) []envoyRoute {
	if shardedSource, ok := source.(searchcontroller.SearchSourceShardedDeployment); ok {
		return buildShardRoutes(search, shardedSource.GetShardNames(), 0, "")
	}
	return []envoyRoute{buildReplicaSetRoute(search)}
}

// buildShardRoutes builds per-shard routes plus one cluster-level route for a single cluster.
// clusterIndex and clusterName identify the cluster; in single-cluster installs pass (0, "").
// Each per-shard route has one UpstreamHosts entry. The trailing cluster-level route
// aggregates all per-shard mongot Service FQDNs so mongos can use a single SNI/filter chain.
func buildShardRoutes(search *searchv1.MongoDBSearch, shardNames []string, clusterIndex int, clusterName string) []envoyRoute {
	// +1 for the cluster-level route appended at the end.
	routes := make([]envoyRoute, 0, len(shardNames)+1)
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	clusterLevelUpstreams := make([]string, 0, len(shardNames))

	for _, shardName := range shardNames {
		sniServiceName := search.ProxyServiceNameForClusterShard(clusterIndex, shardName).Name
		mongotServiceName := search.MongotServiceForClusterShard(clusterIndex, shardName).Name
		upstreamFQDN := fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace)

		sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", sniServiceName, namespace)
		if endpoint := search.GetManagedLBEndpointForClusterShard(clusterIndex, shardName); endpoint != "" {
			sniHostname = endpoint
		}

		routes = append(routes, envoyRoute{
			Name:          shardName,
			NameSafe:      strings.ReplaceAll(shardName, "-", "_"),
			ClusterID:     clusterName,
			SNIHostname:   sniHostname,
			UpstreamHosts: []string{upstreamFQDN},
			UpstreamPort:  mongotPort,
		})
		clusterLevelUpstreams = append(clusterLevelUpstreams, upstreamFQDN)
	}

	// Cluster-level route: mongos in this cluster uses this single SNI chain to reach
	// all local shard mongot pools. SNI is the cluster-level proxy Service FQDN unless
	// the user supplied a managed-LB externalHostname (with {shardName}. prefix stripped).
	clusterLevelSvcName := search.ProxyServiceNamespacedNameForCluster(clusterIndex).Name
	clusterLevelSNI := fmt.Sprintf("%s.%s.svc.cluster.local", clusterLevelSvcName, namespace)
	if endpoint := search.GetManagedLBEndpointForClusterLevel(clusterIndex); endpoint != "" {
		clusterLevelSNI = endpoint
	}

	nameSafe := "cluster_level"
	if clusterIndex > 0 {
		nameSafe = fmt.Sprintf("cluster_level_%d", clusterIndex)
	}
	routes = append(routes, envoyRoute{
		Name:          "cluster-level",
		NameSafe:      nameSafe,
		ClusterID:     clusterName,
		SNIHostname:   clusterLevelSNI,
		UpstreamHosts: clusterLevelUpstreams,
		UpstreamPort:  mongotPort,
	})

	return routes
}

// buildReplicaSetRoute returns the single route for a ReplicaSet.
func buildReplicaSetRoute(search *searchv1.MongoDBSearch) envoyRoute {
	sniServiceName := search.ProxyServiceNamespacedName().Name
	mongotServiceName := search.SearchServiceNamespacedName().Name
	namespace := search.Namespace

	sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", sniServiceName, namespace)
	if endpoint := search.GetManagedLBEndpoint(); endpoint != "" {
		sniHostname = endpoint
	}

	return envoyRoute{
		Name:          "rs",
		NameSafe:      "rs",
		SNIHostname:   sniHostname,
		UpstreamHosts: []string{fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace)},
		UpstreamPort:  search.GetMongotGrpcPort(),
	}
}

// buildRoutesForCluster returns the Envoy routes for one member cluster.
// Empty clusterName is the single-cluster path.
func buildRoutesForCluster(search *searchv1.MongoDBSearch, source searchcontroller.SearchSourceDBResource, clusterIndex int, clusterName string) []envoyRoute {
	if clusterName == "" {
		return buildRoutes(search, source)
	}

	if shardedSource, ok := source.(searchcontroller.SearchSourceShardedDeployment); ok {
		return buildShardRoutes(search, shardedSource.GetShardNames(), clusterIndex, clusterName)
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
	if endpoint := search.GetManagedLBEndpointForCluster(clusterIndex); endpoint != "" {
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

// ensureConfigMap creates or updates the Envoy ConfigMap in the cluster
// indicated by clusterName ("" = central cluster, single-cluster path).
// clusterIndex is used for resource naming; clusterName is used for client
// lookup and labels.
//
// Cross-cluster ownership note: Kubernetes garbage collection does not span
// clusters, so we only set an OwnerReference when writing into the central
// cluster (clusterName == ""). Cleanup of member-cluster objects is handled
// explicitly in deleteEnvoyResources.
func (r *MongoDBSearchEnvoyReconciler) ensureConfigMap(ctx context.Context, search *searchv1.MongoDBSearch, envoyJSON, clusterName string, clusterIndex int, c kubernetesClient.Client, log *zap.SugaredLogger) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerConfigMapNameForCluster(clusterIndex),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		cm.Labels = envoyLabelsForCluster(search, clusterName, clusterIndex)
		cm.Data = map[string]string{"envoy.json": envoyJSON}
		if clusterName == "" {
			return controllerutil.SetOwnerReference(search, cm, c.Scheme())
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure Envoy ConfigMap: %w", err)
	}

	log.Infof("Envoy ConfigMap created/updated (cluster=%q)", clusterName)
	return nil
}

// ensureDeployment creates or updates the Envoy Deployment in the cluster
// indicated by clusterName ("" = central cluster, single-cluster path).
// clusterIndex is used for resource naming; clusterName is used for labels.
// See ensureConfigMap for the cross-cluster ownership rule.
func (r *MongoDBSearchEnvoyReconciler) ensureDeployment(ctx context.Context, search *searchv1.MongoDBSearch, envoyJSON, clusterName string, clusterIndex int, c kubernetesClient.Client, tlsCfg *searchcontroller.TLSSourceConfig, log *zap.SugaredLogger) error {
	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(envoyJSON)))
	replicas := envoyReplicas(search)
	labels := envoyLabelsForCluster(search, clusterName, clusterIndex)
	podLabels := envoyPodLabelsForCluster(search, clusterIndex)
	tlsEnabled := search.IsTLSConfigured()
	image, err := r.envoyContainerImage()
	if err != nil {
		return err
	}
	resources := envoyResourceRequirements(search)
	managedSecurityContext := envvar.ReadBool(podtemplatespec.ManagedSecurityContextEnv) // nolint:forbidigo

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerDeploymentNameForCluster(clusterIndex),
			Namespace: search.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, c, dep, func() error {
		dep.Labels = labels

		dep.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						envoyConfigHashAnnotation: configHash,
						// Exclude the proxy port from Istio inbound capture: mongod's TLS to
						// Envoy is raw app-TLS, not Istio mTLS, so capture in REDIRECT mode
						// stalls the handshake (20s timeout → code 125). No-op without Istio.
						"traffic.sidecar.istio.io/excludeInboundPorts": fmt.Sprintf("%d", searchv1.EnvoyDefaultProxyPort),
					},
				},
				Spec: buildEnvoyPodSpec(search, clusterIndex, tlsCfg, tlsEnabled, image, resources, managedSecurityContext),
			},
		}

		// Apply user deployment configuration override
		if depCfg := search.GetManagedLBDeploymentConfig(); depCfg != nil {
			dep.Spec = merge.DeploymentSpecs(dep.Spec, depCfg.SpecWrapper.Spec)
			dep.Labels = merge.StringToStringMap(dep.Labels, depCfg.MetadataWrapper.Labels)
			dep.Annotations = merge.StringToStringMap(dep.Annotations, depCfg.MetadataWrapper.Annotations)
		}

		if clusterName == "" {
			return controllerutil.SetOwnerReference(search, dep, c.Scheme())
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure Envoy Deployment: %w", err)
	}

	log.Infof("Envoy Deployment created/updated (cluster=%q)", clusterName)
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
					Secret: &corev1.SecretVolumeSource{SecretName: search.LoadBalancerServerCert().Name},
				},
			},
			corev1.Volume{
				Name: "envoy-client-cert",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: search.LoadBalancerClientCert().Name},
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
		Containers: []corev1.Container{
			{
				Name:    "envoy",
				Image:   image,
				Command: []string{"/usr/local/bin/envoy"},
				Args:    []string{"-c", "/etc/envoy/envoy.json", "--log-level", "info"},
				Ports: []corev1.ContainerPort{
					{Name: "grpc", ContainerPort: searchv1.EnvoyDefaultProxyPort},
					{Name: "admin", ContainerPort: envoyAdminPort},
				},
				Resources:       resources,
				SecurityContext: containerSecurityContext,
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/ready",
							Port: intstr.FromInt32(envoyAdminPort),
						},
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       5,
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/ready",
							Port: intstr.FromInt32(envoyAdminPort),
						},
					},
					InitialDelaySeconds: 10,
					PeriodSeconds:       10,
					FailureThreshold:    3,
				},
				Lifecycle: &corev1.Lifecycle{
					PreStop: &corev1.LifecycleHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/drain_listeners",
							Port: intstr.FromInt32(envoyAdminPort),
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

// envoyResourceRequirements returns user-specified resource requirements
// or the defaults (100m/128Mi requests, 500m/512Mi limits).
func envoyResourceRequirements(search *searchv1.MongoDBSearch) corev1.ResourceRequirements {
	if reqs := search.GetManagedLBResourceRequirements(); reqs != nil {
		return *reqs
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
	labels := map[string]string{
		"app":                                search.LoadBalancerDeploymentNameForCluster(clusterIndex),
		"component":                          labelName,
		khandler.MongoDBSearchOwnerNameLabel: search.Name,
		khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
	}
	// In single-cluster legacy mode (clusterName==""), omit the per-cluster label so existing watchers continue to match.
	if clusterName != "" {
		labels[khandler.MongoDBSearchClusterNameLabel] = clusterName
	}
	return labels
}

// envoyReplicas returns the desired Envoy replica count.
// Uses spec.loadBalancer.managed.replicas if set, otherwise defaults to 1.
func envoyReplicas(search *searchv1.MongoDBSearch) int32 {
	if cfg := search.Spec.LoadBalancer; cfg != nil && cfg.Managed != nil && cfg.Managed.Replicas != nil {
		return *cfg.Managed.Replicas
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

func mapEnvoyObjectToSearch(_ context.Context, obj client.Object) []reconcile.Request {
	req := khandler.MapMemberClusterObjectToSearch(obj)
	if req == (reconcile.Request{}) {
		return nil
	}
	return []reconcile.Request{req}
}

// Controller Registration
//
// memberClusterObjectsMap is the same map main.go passes to AddMongoDBSearchController.
// Empty in single-cluster installs — the controller behaves identically to before
// when the map is empty.
//
// For each member cluster we register watches on Envoy Deployment + ConfigMap
// using the label-based mapper (cross-cluster owner refs do not GC).
func AddMongoDBSearchEnvoyController(ctx context.Context, mgr manager.Manager, defaultEnvoyImage string, memberClusterObjectsMap map[string]runtimeCluster.Cluster) error {
	// NOTE: The field index for MongoDBSearchIndexFieldName is already registered
	// by AddMongoDBSearchController. Do not register it again here.

	r := newMongoDBSearchEnvoyReconciler(mgr.GetClient(), defaultEnvoyImage, multicluster.ClustersMapToClientMap(memberClusterObjectsMap))

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

	// Central-cluster owned Envoy resources (single-cluster path).
	ownerHandler := handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &searchv1.MongoDBSearch{}, handler.OnlyControllerOwner())
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &appsv1.Deployment{}, ownerHandler)); err != nil {
		return err
	}
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{}, ownerHandler)); err != nil {
		return err
	}

	// Per-member-cluster Envoy resource watches: label-based mapper, since
	// cross-cluster owner refs don't GC. Same pattern as the AppDB MC and
	// sharded MC controllers (see appdbreplicaset_controller.go and
	// mongodbshardedcluster_controller.go).
	mapper := handler.EnqueueRequestsFromMapFunc(mapEnvoyObjectToSearch)
	for k, v := range memberClusterObjectsMap {
		if err := c.Watch(source.Kind[client.Object](v.GetCache(), &appsv1.Deployment{}, mapper)); err != nil {
			return fmt.Errorf("failed to set Envoy Deployment watch on member cluster %s: %w", k, err)
		}
		if err := c.Watch(source.Kind[client.Object](v.GetCache(), &corev1.ConfigMap{}, mapper)); err != nil {
			return fmt.Errorf("failed to set Envoy ConfigMap watch on member cluster %s: %w", k, err)
		}
	}

	return nil
}
