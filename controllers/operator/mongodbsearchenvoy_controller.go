package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
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
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/envvar"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
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

	// Cross-cluster enqueue labels (B16). Cross-cluster owner refs do not GC,
	// so a label-based mapper is the only path back to the parent MongoDBSearch
	// for member-cluster Deployment/ConfigMap watches.
	envoyOwnerSearchNameLabel      = "mongodb.com/search-name"
	envoyOwnerSearchNamespaceLabel = "mongodb.com/search-namespace"
	envoyClusterNameLabel          = "mongodb.com/cluster-name"
)

// envoyRoute defines routing information for one Envoy entrypoint (one per shard, or one for RS).
type envoyRoute struct {
	Name         string // identifier: shard name (e.g., "mdb-sh-0") or "rs" for ReplicaSets
	NameSafe     string // identifier safe for Envoy (hyphens replaced with underscores)
	ClusterID    string // member cluster name in MC; "" in single-cluster installs
	SNIHostname  string // FQDN of the proxy service for SNI matching
	UpstreamHost string // FQDN of the mongot headless service
	UpstreamPort int32  // typically 27028
}

type MongoDBSearchEnvoyReconciler struct {
	kubeClient        kubernetesClient.Client
	watch             *watch.ResourceWatcher
	defaultEnvoyImage string

	// memberClusterClientsMap is keyed by the member cluster name and holds the
	// per-cluster Kubernetes client. Empty in single-cluster installs; the
	// Reconcile path falls back to kubeClient via selectEnvoyClient().
	memberClusterClientsMap       map[string]kubernetesClient.Client
	memberClusterSecretClientsMap map[string]secrets.SecretClient
}

func newMongoDBSearchEnvoyReconciler(c client.Client, defaultEnvoyImage string, memberClustersMap map[string]client.Client) *MongoDBSearchEnvoyReconciler {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClustersMap))
	secretClientsMap := make(map[string]secrets.SecretClient, len(memberClustersMap))
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
		secretClientsMap[k] = secrets.SecretClient{
			VaultClient: nil, // Vault is not supported on multicluster
			KubeClient:  clientsMap[k],
		}
	}

	return &MongoDBSearchEnvoyReconciler{
		kubeClient:                    kubernetesClient.NewClient(c),
		watch:                         watch.NewResourceWatcher(),
		defaultEnvoyImage:             defaultEnvoyImage,
		memberClusterClientsMap:       clientsMap,
		memberClusterSecretClientsMap: secretClientsMap,
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status,mongodbsearch/finalizers},verbs=*,namespace=placeholder
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
			r.deleteEnvoyResources(ctx, mdbSearch, log)
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

	tlsCfg := searchSource.TLSConfig()
	tlsEnabled := mdbSearch.IsTLSConfigured()

	workList := r.buildClusterWorkList(mdbSearch)
	perClusterStatuses := make([]searchv1.ClusterLoadBalancerStatus, 0, len(workList))
	worstPhase := status.PhaseRunning
	var firstFailure error
	multiCluster := len(workList) > 1 || (len(workList) == 1 && workList[0].ClusterName != "")

	for _, w := range workList {
		st := r.reconcileForCluster(ctx, mdbSearch, searchSource, tlsEnabled, tlsCfg, w.ClusterIndex, w.ClusterName, log)
		if multiCluster {
			perClusterStatuses = append(perClusterStatuses, searchv1.ClusterLoadBalancerStatus{
				ClusterName: w.ClusterName,
				Phase:       st.Phase(),
				Message:     extractWorkflowMsg(st),
			})
		}
		if isWorsePhase(st.Phase(), worstPhase) {
			worstPhase = st.Phase()
		}
		if !st.IsOK() && firstFailure == nil {
			firstFailure = fmt.Errorf("cluster %q: %s", w.ClusterName, extractWorkflowMsg(st))
		}
	}

	if multiCluster {
		mdbSearch.Status.LoadBalancer = &searchv1.LoadBalancerStatus{
			Phase:    worstPhase,
			Clusters: perClusterStatuses,
		}
	}

	if firstFailure != nil {
		// Preserve the worst-of phase computed across clusters; without this branch,
		// the JSON patch would downgrade Failed → Pending and contradict the
		// per-cluster entries.
		if worstPhase == status.PhaseFailed {
			return r.updateLBStatus(ctx, mdbSearch, workflow.Failed(firstFailure), log)
		}
		return r.updateLBStatus(ctx, mdbSearch, workflow.Pending("%s", firstFailure), log)
	}

	log.Info("MongoDBSearchEnvoy reconciliation complete")
	return r.updateLBStatus(ctx, mdbSearch, workflow.OK(), log)
}

// clusterWorkItem represents one (clusterIndex, clusterName) unit the reconciler
// must process. In single-cluster (no spec.clusters or empty memberClusterClientsMap)
// the slice has one entry with ClusterIndex == 0 and ClusterName == "" and writes
// go to the central cluster. ClusterIndex matches the position of the cluster in
// spec.Clusters[] and drives per-cluster proxy-svc naming via
// ProxyServiceNamespacedNameForCluster.
type clusterWorkItem struct {
	ClusterIndex int
	ClusterName  string
}

// buildClusterWorkList expands spec.clusters[] into the per-cluster work units
// the reconciler will iterate over. Membership rules:
//   - len(memberClusterClientsMap) == 0 → single-cluster install; one work item with "".
//   - len(spec.clusters) == 0 → single-cluster degenerate; one work item with "" until B18 lands.
//   - otherwise → one work item per spec.clusters[i]. Member clusters in the
//     reconciler's map but absent from spec.clusters[] are intentionally ignored
//     (the CR drives, not the operator's membership).
func (r *MongoDBSearchEnvoyReconciler) buildClusterWorkList(search *searchv1.MongoDBSearch) []clusterWorkItem {
	if len(r.memberClusterClientsMap) == 0 || search.Spec.Clusters == nil || len(*search.Spec.Clusters) == 0 {
		return []clusterWorkItem{{ClusterIndex: 0, ClusterName: ""}}
	}
	work := make([]clusterWorkItem, 0, len(*search.Spec.Clusters))
	for i, c := range *search.Spec.Clusters {
		work = append(work, clusterWorkItem{ClusterIndex: i, ClusterName: c.ClusterName})
	}
	return work
}

// reconcileForCluster runs the ConfigMap + Deployment ensure for one cluster.
// Returns a workflow.Status describing the per-cluster outcome.
//
// clusterIndex matches the position of the cluster in spec.Clusters[] and drives
// per-cluster proxy-svc naming. Index 0 / empty clusterName is the legacy
// single-cluster path.
func (r *MongoDBSearchEnvoyReconciler) reconcileForCluster(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	source searchcontroller.SearchSourceDBResource,
	tlsEnabled bool,
	tlsCfg *searchcontroller.TLSSourceConfig,
	clusterIndex int,
	clusterName string,
	log *zap.SugaredLogger,
) workflow.Status {
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
	if err := r.ensureConfigMap(ctx, search, envoyJSON, clusterName, log); err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	if err := r.ensureDeployment(ctx, search, envoyJSON, clusterName, tlsCfg, log); err != nil {
		return workflow.Failed(fmt.Errorf("cluster=%q: %w", clusterName, err))
	}
	return workflow.OK()
}

// isWorsePhase returns true if a is "worse" than b in the ordering
// Failed > Pending > Running. Used to compute the top-level Phase from
// per-cluster phases.
func isWorsePhase(a, b status.Phase) bool {
	rank := func(p status.Phase) int {
		switch p {
		case status.PhaseFailed:
			return 3
		case status.PhasePending:
			return 2
		case status.PhaseRunning:
			return 1
		default:
			return 0
		}
	}
	return rank(a) > rank(b)
}

// extractWorkflowMsg pulls the message from a workflow.Status's StatusOptions.
// The workflow.Status interface does not expose Msg() directly; the message
// rides on a status.MessageOption populated by workflow.{Pending,Failed,Invalid}.
func extractWorkflowMsg(st workflow.Status) string {
	if opt, ok := status.GetOption(st.StatusOptions(), status.MessageOption{}); ok {
		return opt.(status.MessageOption).Message
	}
	return ""
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

// deleteEnvoyResources removes the Envoy Deployment and ConfigMap that were
// created when managed LB was active. This is called exactly once per LB removal,
// gated by Status.LoadBalancer != nil (cleared immediately after).
//
// B16 scope: this still walks only the central cluster. Member-cluster
// Deployments + ConfigMaps will leak on managed→unmanaged transitions and on
// CR delete because cross-cluster owner refs do not GC. B12 (graceful drain
// on cluster removal) extends this to all member clusters in memberClusterClientsMap.
func (r *MongoDBSearchEnvoyReconciler) deleteEnvoyResources(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) {
	ns := search.Namespace

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentName(), Namespace: ns}}
	if err := r.kubeClient.Delete(ctx, dep); err != nil && !apierrors.IsNotFound(err) {
		log.Warnf("Failed to delete Envoy Deployment %s: %s", dep.Name, err)
	} else if err == nil {
		log.Infof("Deleted Envoy Deployment %s", dep.Name)
	}

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerConfigMapName(), Namespace: ns}}
	if err := r.kubeClient.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
		log.Warnf("Failed to delete Envoy ConfigMap %s: %s", cm.Name, err)
	} else if err == nil {
		log.Infof("Deleted Envoy ConfigMap %s", cm.Name)
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
		return buildShardRoutes(search, shardedSource.GetShardNames())
	}
	return []envoyRoute{buildReplicaSetRoute(search)}
}

// buildShardRoutes builds per-shard routing information from shard names.
func buildShardRoutes(search *searchv1.MongoDBSearch, shardNames []string) []envoyRoute {
	routes := make([]envoyRoute, 0, len(shardNames))
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	for _, shardName := range shardNames {
		sniServiceName := search.ProxyServiceNameForShard(shardName).Name
		mongotServiceName := search.MongotServiceForShard(shardName).Name

		sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", sniServiceName, namespace)
		if endpoint := search.GetManagedLBEndpointForShard(shardName); endpoint != "" {
			sniHostname = endpoint
		}

		routes = append(routes, envoyRoute{
			Name:         shardName,
			NameSafe:     strings.ReplaceAll(shardName, "-", "_"),
			SNIHostname:  sniHostname,
			UpstreamHost: fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace),
			UpstreamPort: mongotPort,
		})
	}

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
		Name:         "rs",
		NameSafe:     "rs",
		SNIHostname:  sniHostname,
		UpstreamHost: fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace),
		UpstreamPort: search.GetMongotGrpcPort(),
	}
}

// buildRoutesForCluster returns the Envoy routes for one member cluster (B16).
// When clusterName is empty, this is the single-cluster install path and
// behaves identically to buildRoutes (back-compat). clusterIndex matches the
// position of the cluster in spec.Clusters[] and is the source of truth for
// per-cluster proxy-svc naming via ProxyServiceNamespacedNameForCluster.
//
// In multi-cluster (clusterName != ""):
//   - The default SNI hostname is the per-cluster proxy-svc FQDN
//     (<name>-search-<idx>-proxy-svc.<ns>.svc.cluster.local), produced by
//     ProxyServiceNamespacedNameForCluster(clusterIndex). Because the index is
//     baked into the Service name, per-cluster SNIs are automatically distinct.
//   - When the user supplies an externalHostname template containing
//     {clusterName} (and, for sharded sources, {shardName}) the template wins
//     and substitutions are applied.
//   - The ClusterID field on each envoyRoute carries the member cluster name for
//     downstream consumers (Deployment naming, status writes).
func buildRoutesForCluster(search *searchv1.MongoDBSearch, source searchcontroller.SearchSourceDBResource, clusterIndex int, clusterName string) []envoyRoute {
	if clusterName == "" {
		return buildRoutes(search, source)
	}

	if shardedSource, ok := source.(searchcontroller.SearchSourceShardedDeployment); ok {
		return buildShardRoutesForCluster(search, shardedSource.GetShardNames(), clusterName)
	}
	return []envoyRoute{buildReplicaSetRouteForCluster(search, clusterIndex, clusterName)}
}

// buildReplicaSetRouteForCluster builds a single RS-mode route for one cluster.
// The SNI hostname is the per-cluster proxy-svc FQDN derived from
// ProxyServiceNamespacedNameForCluster(clusterIndex). When the user supplies a
// managed-LB externalHostname template with {clusterName}, it overrides the
// default FQDN.
func buildReplicaSetRouteForCluster(search *searchv1.MongoDBSearch, clusterIndex int, clusterName string) envoyRoute {
	mongotServiceName := search.SearchServiceNamespacedName().Name
	namespace := search.Namespace
	mongotPort := search.GetMongotGrpcPort()

	sniServiceName := search.ProxyServiceNamespacedNameForCluster(clusterIndex).Name
	sniHostname := fmt.Sprintf("%s.%s.svc.cluster.local", sniServiceName, namespace)
	if endpoint := search.GetManagedLBEndpoint(); endpoint != "" {
		sniHostname = applyClusterIDToSNI(endpoint, clusterName)
	}

	return envoyRoute{
		Name:         "rs",
		NameSafe:     "rs",
		ClusterID:    clusterName,
		SNIHostname:  sniHostname,
		UpstreamHost: fmt.Sprintf("%s.%s.svc.cluster.local", mongotServiceName, namespace),
		UpstreamPort: mongotPort,
	}
}

// buildShardRoutesForCluster builds per-shard routes for one cluster. SNI naming
// for sharded topologies still flows through ProxyServiceNameForShard +
// applyClusterIDToSNI for now (no per-(cluster, shard) Service helper exists);
// the {clusterName}/{shardName} externalHostname template is the supported MC
// path.
func buildShardRoutesForCluster(search *searchv1.MongoDBSearch, shardNames []string, clusterName string) []envoyRoute {
	base := buildShardRoutes(search, shardNames)
	templated := search.GetManagedLBEndpoint() != ""
	for i := range base {
		base[i].ClusterID = clusterName
		base[i].SNIHostname = applyShardClusterIDToSNI(base[i].SNIHostname, clusterName, templated)
	}
	return base
}

// applyClusterIDToSNI substitutes the {clusterName} placeholder when present.
// Used for the externalHostname-template path (RS mode); the default
// service-FQDN path is now produced directly by ProxyServiceNamespacedNameForCluster
// and does not flow through this helper.
func applyClusterIDToSNI(sni, clusterName string) string {
	if strings.Contains(sni, searchv1.ClusterNamePlaceholder) {
		return strings.ReplaceAll(sni, searchv1.ClusterNamePlaceholder, clusterName)
	}
	// User supplied an externalHostname template without {clusterName}; honour
	// it as-is. Multi-cluster users are expected (per B4) to include the
	// placeholder; a missing-placeholder admission rule lives in B13/B4.
	return sni
}

// applyShardClusterIDToSNI handles the sharded SNI rewrite. Until a
// per-(cluster, shard) proxy-svc helper exists, the default service-FQDN base
// is suffixed with -<clusterName> after the first DNS label to keep per-cluster
// SNIs distinct; templated paths (managed-LB externalHostname) get
// {clusterName} substituted when present.
func applyShardClusterIDToSNI(sni, clusterName string, templated bool) string {
	if strings.Contains(sni, searchv1.ClusterNamePlaceholder) {
		return strings.ReplaceAll(sni, searchv1.ClusterNamePlaceholder, clusterName)
	}
	if templated {
		return sni
	}
	idx := strings.Index(sni, ".")
	if idx == -1 {
		return sni + "-" + clusterName
	}
	return sni[:idx] + "-" + clusterName + sni[idx:]
}

// ensureConfigMap creates or updates the Envoy ConfigMap in the cluster
// indicated by clusterName ("" = central cluster, single-cluster path).
//
// Cross-cluster ownership note: Kubernetes garbage collection does not span
// clusters, so we only set an OwnerReference when writing into the central
// cluster (clusterName == ""). Cleanup of member-cluster objects is handled
// explicitly in deleteEnvoyResources.
func (r *MongoDBSearchEnvoyReconciler) ensureConfigMap(ctx context.Context, search *searchv1.MongoDBSearch, envoyJSON, clusterName string, log *zap.SugaredLogger) error {
	c := selectEnvoyClient(clusterName, r.kubeClient, r.memberClusterClientsMap)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerConfigMapNameForCluster(clusterName),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		cm.Labels = envoyLabelsForCluster(search, clusterName)
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
// See ensureConfigMap for the cross-cluster ownership rule.
func (r *MongoDBSearchEnvoyReconciler) ensureDeployment(ctx context.Context, search *searchv1.MongoDBSearch, envoyJSON, clusterName string, tlsCfg *searchcontroller.TLSSourceConfig, log *zap.SugaredLogger) error {
	c := selectEnvoyClient(clusterName, r.kubeClient, r.memberClusterClientsMap)
	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(envoyJSON)))
	replicas := envoyReplicasForCluster(search, clusterName)
	labels := envoyLabelsForCluster(search, clusterName)
	podLabels := envoyPodLabelsForCluster(search, clusterName)
	tlsEnabled := search.IsTLSConfigured()
	image, err := r.envoyContainerImage()
	if err != nil {
		return err
	}
	resources := envoyResourceRequirements(search)
	managedSecurityContext := envvar.ReadBool(podtemplatespec.ManagedSecurityContextEnv) // nolint:forbidigo

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerDeploymentNameForCluster(clusterName),
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
					},
				},
				Spec: buildEnvoyPodSpec(search, tlsCfg, tlsEnabled, image, resources, managedSecurityContext),
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
func buildEnvoyPodSpec(search *searchv1.MongoDBSearch, tlsCfg *searchcontroller.TLSSourceConfig, tlsEnabled bool, image string, resources corev1.ResourceRequirements, managedSecurityContext bool) corev1.PodSpec {
	volumes := []corev1.Volume{
		{
			Name: "envoy-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: search.LoadBalancerConfigMapName()},
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
// enqueue labels and an optional cluster-name label. In single-cluster (clusterID
// == "") the cluster-name label is omitted. Both stamp the cross-cluster enqueue
// labels so the label-based mapper can route Deployment/ConfigMap events back
// to the owning MongoDBSearch even when objects live in a member cluster (where
// owner refs don't GC).
func envoyLabelsForCluster(search *searchv1.MongoDBSearch, clusterID string) map[string]string {
	labels := map[string]string{
		"app":                          search.LoadBalancerDeploymentNameForCluster(clusterID),
		"component":                    labelName,
		envoyOwnerSearchNameLabel:      search.Name,
		envoyOwnerSearchNamespaceLabel: search.Namespace,
	}
	if clusterID != "" {
		labels[envoyClusterNameLabel] = clusterID
	}
	return labels
}

// selectEnvoyClient picks the right Kubernetes client for one member cluster.
// Mirrors B1's selectClusterClient: empty clusterName means single-cluster
// (return central); known member name returns the member client; unknown name
// silently falls back to central. The reconcile loop is responsible for
// surfacing unknown ClusterNames as Pending in per-cluster status.
func selectEnvoyClient(clusterName string, central kubernetesClient.Client, members map[string]kubernetesClient.Client) kubernetesClient.Client {
	if clusterName == "" {
		return central
	}
	if c, ok := members[clusterName]; ok {
		return c
	}
	return central
}

// envoyReplicasForCluster returns the desired Envoy replica count for one
// member cluster, applying the precedence:
//
//	clusters[i].loadBalancer.managed.replicas  >  spec.loadBalancer.managed.replicas  >  envoyReplicasDefault
//
// In single-cluster (clusterName == "") the per-cluster lookup is skipped.
func envoyReplicasForCluster(search *searchv1.MongoDBSearch, clusterName string) int32 {
	if clusterName != "" && search.Spec.Clusters != nil {
		for _, c := range *search.Spec.Clusters {
			if c.ClusterName != clusterName {
				continue
			}
			if c.LoadBalancer != nil && c.LoadBalancer.Managed != nil && c.LoadBalancer.Managed.Replicas != nil {
				return *c.LoadBalancer.Managed.Replicas
			}
			break
		}
	}
	if search.Spec.LoadBalancer != nil && search.Spec.LoadBalancer.Managed != nil && search.Spec.LoadBalancer.Managed.Replicas != nil {
		return *search.Spec.LoadBalancer.Managed.Replicas
	}
	return envoyReplicasDefault
}

// envoyPodLabelsForCluster returns Envoy pod-selection labels for one cluster.
// The "app" label uses the per-cluster Deployment name so Pods stay distinct
// per (cluster, namespace) — Pod names already carry the Deployment prefix.
func envoyPodLabelsForCluster(search *searchv1.MongoDBSearch, clusterID string) map[string]string {
	return map[string]string{
		"app": search.LoadBalancerDeploymentNameForCluster(clusterID),
	}
}

// mapEnvoyObjectToSearch maps an Envoy Deployment or ConfigMap (in any cluster)
// back to its owning MongoDBSearch. Cross-cluster owner refs do not GC, so the
// label-based mapper is the only path home for member-cluster watches.
func mapEnvoyObjectToSearch(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	name := labels[envoyOwnerSearchNameLabel]
	ns := labels[envoyOwnerSearchNamespaceLabel]
	if name == "" || ns == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}}
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

	// Per-member-cluster Envoy resource watches: use the label-based mapper because
	// cross-cluster owner refs do not GC. Mirrors mongodbmultireplicaset_controller.go:1170-1175.
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
