package operator

import (
	"context"
	"crypto/sha1" //nolint //Used to derive a stable host identifier, not for security.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/blang/semver"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
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
	omapi "github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	kubeSecret "github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

const (
	metricsForwarderConfigPath           = "/etc/otelcol"
	metricsForwarderConfigFileName       = "config.yaml"
	metricsForwarderCACertVolumeName     = "mms-ca-cert"
	metricsForwarderCACertMountPath      = "/mongodb-automation/certs"
	metricsForwarderConfigHashAnnotation = "mongodb.com/metrics-forwarder-config-hash"
	metricsForwarderLabelName            = "search-metrics-forwarder"

	// metricsForwarderMinOpsManagerVersion is the minimum self-hosted Ops Manager version that
	// supports the metrics forwarding ingest endpoint. Versions below this do not expose the
	// endpoint, so the forwarder cannot function correctly against them.
	metricsForwarderMinOpsManagerVersion = "8.0.25"

	// how long to wait before re-checking whether removed mongot
	// pods have terminated so their Ops Manager hosts can be safely deregistered.
	mongotPodTerminationRequeueIntervalSeconds = 15

	// prometheusSDRefreshInterval is the dns_sd_configs.refresh_interval in the
	// forwarder OTel config template.
	prometheusSDRefreshInterval = 30 * time.Second

	// prometheusDefaultScrapeInterval is the global scrape_interval set in the
	// forwarder OTel config template.
	prometheusDefaultScrapeInterval = 60 * time.Second

	// hostDeletionDeferralWindow is the minimum time to wait after a mongot pod
	// disappears before issuing the Ops Manager host deletion. It covers
	// SD refresh + 2 scrape intervals to handle the worst-case where the pod
	// disappears from DNS just before a scrape cycle: the forwarder can still
	// push metrics for up to one full scrape interval after SD drops the target,
	// and a second interval provides a safety margin.
	hostDeletionDeferralWindow = prometheusSDRefreshInterval + 2*prometheusDefaultScrapeInterval
)

// MongoDBSearchMetricsForwarderReconciler reconciles the metrics forwarder Deployment
// that forwards mongot Prometheus metrics to Ops Manager.
type MongoDBSearchMetricsForwarderReconciler struct {
	kubeClient         kubernetesClient.Client
	secretClient       secrets.SecretClient
	watch              *watch.ResourceWatcher
	defaultImage       string
	omRequester        omAgentRequester
	otelConfigTemplate searchcontroller.MetricsForwarderOTelConfigTemplate

	prepareSearch prepareSearchFunc
	// clientForCluster resolves the client for one cluster name; nil = cluster not
	// registered with the operator (hub-and-spoke only).
	clientForCluster func(clusterName string) kubernetesClient.Client
}

func newMongoDBSearchMetricsForwarderReconciler(c client.Client, defaultImage string, memberClusterMap map[string]client.Client, operatorClusterName string) *MongoDBSearchMetricsForwarderReconciler {
	clientsMap := make(map[string]kubernetesClient.Client, len(memberClusterMap))
	for k, v := range memberClusterMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	r := &MongoDBSearchMetricsForwarderReconciler{
		kubeClient:         kubernetesClient.NewClient(c),
		secretClient:       secrets.SecretClient{KubeClient: kubernetesClient.NewClient(c)},
		watch:              watch.NewResourceWatcher(),
		defaultImage:       defaultImage,
		omRequester:        omHTTPAgentRequester{},
		otelConfigTemplate: searchcontroller.NewMetricsForwarderOTelConfigTemplate(),
		prepareSearch:      newPrepareSearch(operatorClusterName),
	}
	if len(clientsMap) == 0 {
		// Single-cluster and per-cluster-operator installs render everything locally.
		r.clientForCluster = func(string) kubernetesClient.Client { return r.kubeClient }
	} else {
		// Empty clusterName is the central/local cluster (single-cluster sharded
		// search in a hub-and-spoke install); only named entries are member clusters.
		r.clientForCluster = func(clusterName string) kubernetesClient.Client {
			if clusterName == "" {
				return r.kubeClient
			}
			return clientsMap[clusterName]
		}
	}
	return r
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbsearch,mongodbsearch/status,mongodbsearch/finalizers},verbs=*,namespace=placeholder
// +kubebuilder:rbac:groups=mongodb.com,resources=mongodb,verbs=get;list;watch,namespace=placeholder
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete,namespace=placeholder
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch,namespace=placeholder
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch,namespace=placeholder
func (r *MongoDBSearchMetricsForwarderReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("MongoDBSearchMetricsForwarder", request.NamespacedName)
	log.Info("-> MongoDBSearchMetricsForwarder.Reconcile")

	mdbSearch := &searchv1.MongoDBSearch{}
	if result, err := commoncontroller.GetResource(ctx, r.kubeClient, request, mdbSearch, log); err != nil {
		return result, err
	}

	if skip, result, err := r.prepareSearch(mdbSearch, log,
		func(st workflow.Status) (reconcile.Result, error) {
			if !mdbSearch.IsMetricsForwarderEnabled() {
				return st.ReconcileResult()
			}
			return r.updateMetricsForwarderStatus(ctx, mdbSearch, st, log)
		}); skip {
		return result, err
	}

	// A resource being deleted must always run cleanup, regardless of the configured forwarder mode.
	// The finalizer is only added while the forwarder is enabled, so its presence means hosts may be
	// registered in Ops Manager. Without handling deletion here, disabling the forwarder (which takes
	// the mode out of the auto/enabled branch that owns deletion handling) before deleting the
	// MongoDBSearch would leak monitored hosts in Ops Manager and leave the finalizer in place,
	// blocking deletion. reconcileCore performs the DeletionTimestamp/finalizer cleanup once the Ops
	// Manager connection context is resolved.
	if !mdbSearch.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(mdbSearch, util.SearchMetricsForwarderFinalizer) {
			return reconcile.Result{}, nil
		}
		st := r.reconcileCore(ctx, mdbSearch, log)
		if st.IsOK() {
			// Cleanup succeeded and the finalizer was removed, so the resource is now gone; there is
			// no status left to update.
			return reconcile.Result{}, nil
		}
		return r.updateMetricsForwarderStatus(ctx, mdbSearch, st, log)
	}

	mode := mdbSearch.Spec.Observability.MetricsForwarder.Mode
	switch mode {
	case searchv1.MetricsForwarderModeAuto, "":
		if !mdbSearch.IsMetricsForwarderEnabled() {
			r.deleteMetricsForwarderResourcesFromState(ctx, mdbSearch, log)
			return r.clearMetricsForwarderStatus(ctx, mdbSearch, log)
		}

		fallthrough
	case searchv1.MetricsForwarderModeEnabled:
		if !mdbSearch.Spec.Observability.Prometheus.IsEnabled() {
			return r.updateMetricsForwarderStatus(ctx, mdbSearch, workflow.Invalid("metrics forwarder requires Prometheus; set spec.observability.prometheus.mode: enabled to enable it, or set spec.observability.metricsForwarder.mode: disabled to silence this message"), log)
		}
		return r.updateMetricsForwarderStatus(ctx, mdbSearch, r.reconcileCore(ctx, mdbSearch, log), log)
	case searchv1.MetricsForwarderModeDisabled:
		r.deleteMetricsForwarderResourcesFromState(ctx, mdbSearch, log)
		return r.updateMetricsForwarderStatus(ctx, mdbSearch, workflow.Disabled(), log)
	default:
		return r.updateMetricsForwarderStatus(ctx, mdbSearch, workflow.Invalid("unknown metrics forwarder mode %q", mode), log)
	}
}

// deleteMetricsForwarderResourcesFromState builds the cluster work list from spec.clusters and
// deletes all metrics forwarder resources.
func (r *MongoDBSearchMetricsForwarderReconciler) deleteMetricsForwarderResourcesFromState(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) {
	workList := r.buildClusterWorkList(search)
	r.deleteMetricsForwarderResources(ctx, search, workList, log)
}

func (r *MongoDBSearchMetricsForwarderReconciler) reconcileCore(ctx context.Context, mdbSearch *searchv1.MongoDBSearch, log *zap.SugaredLogger) workflow.Status {
	if r.defaultImage == "" {
		return workflow.Invalid("%s environment variable must be set on the operator to use metrics forwarder", util.MetricsForwarderImageEnv)
	}

	if mdbSearch.Status.Version == "" {
		return workflow.Pending("Waiting for MongoDBSearch version to be reconciled")
	}

	searchSource, err := getSearchSource(ctx, r.kubeClient, r.watch, mdbSearch, log)
	if err != nil {
		return workflow.Pending("Waiting for search source: %s", err)
	}

	fwdCtx, groupId, fwdStatus, supported := r.resolveForwarderContext(mdbSearch, searchSource)
	if !supported {
		r.deleteMetricsForwarderResourcesFromState(ctx, mdbSearch, log)
		if mdbSearch.Spec.Observability.MetricsForwarder.Mode == searchv1.MetricsForwarderModeAuto {
			mdbSearch.Status.MetricsForwarder = nil
			return workflow.OK()
		}
		return workflow.Invalid("The metrics forwarder is not supported for MongoDBCommunity sources")
	}
	if !fwdStatus.IsOK() {
		return fwdStatus
	}

	projectCMKey := kube.ObjectKey(mdbSearch.Namespace, fwdCtx.projectConfigMapRef.Name)
	projectConfig, err := project.ReadProjectConfig(ctx, r.kubeClient, projectCMKey, "")
	if err != nil {
		return workflow.Failed(fmt.Errorf("failed to read project config: %w", err))
	}

	if groupId == "" {
		if groupId, err = r.resolveGroupFromAgentKey(ctx, projectConfig, kube.ObjectKey(mdbSearch.Namespace, fwdCtx.agentApiKeySecret.Name), log); err != nil {
			return workflow.Failed(fmt.Errorf("failed to resolve Project ID from Agent credentials: %w", err))
		}
	}

	workList := r.buildClusterWorkList(mdbSearch)

	if !mdbSearch.DeletionTimestamp.IsZero() {
		log.Info("MongoDBSearch is being deleted")
		if controllerutil.ContainsFinalizer(mdbSearch, util.SearchMetricsForwarderFinalizer) {
			return r.preDeletionCleanup(ctx, mdbSearch, groupId, projectConfig, fwdCtx.agentApiKeySecret.Name, workList, log)
		}
		return workflow.OK()
	}

	r.watch.AddWatchedResourceIfNotAdded(fwdCtx.projectConfigMapRef.Name, mdbSearch.Namespace, watch.ConfigMap, mdbSearch.NamespacedName())

	if supported, st := r.checkOMVersionForMetricsEndpoint(mdbSearch, projectConfig, log); !supported {
		r.deleteMetricsForwarderResourcesFromState(ctx, mdbSearch, log)
		return st
	}

	if err := r.ensureFinalizer(ctx, mdbSearch, log); err != nil {
		return workflow.Failed(fmt.Errorf("failed to add finalizer: %w", err))
	}

	var shardNames []string
	if shardedSource, ok := searchSource.(searchcontroller.SearchSourceShardedDeployment); ok {
		shardNames = shardedSource.GetShardNames()
	}

	var firstFailure error
	var worstPhase status.Phase
	pendingPodTerminations := false

	for _, w := range workList {
		var st workflow.Status
		switch w.Client {
		case nil:
			st = workflow.Pending("Member cluster %q not registered with the operator", w.ClusterName)
		default:
			var pending bool
			pending, st = r.reconcileForCluster(ctx, mdbSearch, shardNames, groupId, projectConfig, fwdCtx.agentApiKeySecret.Name, w, log)
			if pending {
				pendingPodTerminations = true
			}
		}
		worstPhase = searchv1.WorstOfPhase(worstPhase, st.Phase())
		if !st.IsOK() && firstFailure == nil {
			firstFailure = fmt.Errorf("cluster %q: %s", w.ClusterName, searchcontroller.MessageFromStatus(st))
		}
	}

	if firstFailure != nil {
		if worstPhase == status.PhaseFailed {
			return workflow.Failed(firstFailure)
		}
		return workflow.Pending("%s", firstFailure)
	}

	log.Info("MongoDBSearchMetricsForwarder reconciliation complete")
	if pendingPodTerminations {
		return workflow.OK().WithRetry(mongotPodTerminationRequeueIntervalSeconds)
	}
	return workflow.OK()
}

// buildClusterWorkList builds the per-reconcile work list from spec.clusters, using the CRD
// cluster index pin (clusters[].index). Single-cluster: one item with ClusterName="" and
// ClusterIndex=0. spec.clusters is validated non-empty, so the empty-clusters branch is a
// defensive backstop only.
func (r *MongoDBSearchMetricsForwarderReconciler) buildClusterWorkList(search *searchv1.MongoDBSearch) []clusterWorkItem {
	if len(search.Spec.Clusters) == 0 {
		return []clusterWorkItem{{ClusterName: "", ClusterIndex: 0, Client: r.kubeClient}}
	}
	work := make([]clusterWorkItem, 0, len(search.Spec.Clusters))
	for _, c := range search.Spec.Clusters {
		work = append(work, clusterWorkItem{ClusterName: c.Name, ClusterIndex: c.ResolveIndex(), Client: r.clientForCluster(c.Name)})
	}
	return work
}

// reconcileForCluster runs the per-cluster reconcile: replicate dependencies, reconcile topology
// state, render the OTel config, and ensure the ConfigMap and Deployment. Returns whether any
// host deletions are still pending (triggering a requeue) and the workflow status.
func (r *MongoDBSearchMetricsForwarderReconciler) reconcileForCluster(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	shardNames []string,
	groupID string,
	projectConfig mdbv1.ProjectConfig,
	agentSecretName string,
	w clusterWorkItem,
	log *zap.SugaredLogger,
) (pendingTerminations bool, st workflow.Status) {
	if err := r.replicateForwarderDependencies(ctx, search, agentSecretName, projectConfig.SSLMMSCAConfigMap, w, log); err != nil {
		return false, workflow.Failed(fmt.Errorf("cluster=%q: failed to replicate dependencies: %w", w.ClusterName, err))
	}

	pending, err := r.reconcileTopologyState(ctx, search, shardNames, groupID, projectConfig, agentSecretName, w, log)
	if err != nil {
		return false, workflow.Failed(fmt.Errorf("cluster=%q: %w", w.ClusterName, err))
	}

	agentKeySecretName := search.MetricsForwarderAgentKeySecretNameForCluster(w.ClusterIndex)
	caConfigMapName := ""
	if projectConfig.SSLMMSCAConfigMap != "" {
		caConfigMapName = search.MetricsForwarderCACertConfigMapNameForCluster(w.ClusterIndex)
	}

	configYAML, err := r.otelConfigTemplate.Execute(searchcontroller.MetricsForwarderConfigParams{
		OMBaseURL:                         projectConfig.BaseURL,
		HasOMCaCert:                       caConfigMapName != "",
		RequireValidMMSServerCertificates: projectConfig.SSLRequireValidMMSServerCertificates,
		ShardNames:                        shardNames,
		ClusterIndex:                      w.ClusterIndex,
		ClusterName:                       w.ClusterName,
		GroupID:                           groupID,
		MongotVersion:                     search.Status.Version,
		MongotName:                        search.Name,
		MongotGRPCPort:                    int(search.GetMongotGrpcPort()),
		ScrapeInterval:                    prometheusDefaultScrapeInterval,
	})
	if err != nil {
		return false, workflow.Failed(fmt.Errorf("cluster=%q: failed to generate metrics forwarder config: %w", w.ClusterName, err))
	}

	if err := r.ensureMetricsForwarderConfigMap(ctx, search, configYAML, w.ClusterName, w.ClusterIndex, w.Client, log); err != nil {
		return false, workflow.Failed(fmt.Errorf("cluster=%q: %w", w.ClusterName, err))
	}

	if err := r.ensureMetricsForwarderDeployment(ctx, search, configYAML, groupID, agentKeySecretName, caConfigMapName, w.ClusterName, w.ClusterIndex, w.Client, log); err != nil {
		return false, workflow.Failed(fmt.Errorf("cluster=%q: %w", w.ClusterName, err))
	}

	return pending, workflow.OK()
}

// replicateForwarderDependencies copies the agent-key Secret and (when present) the OM CA ConfigMap
// into forwarder-owned, labeled, cluster-index-suffixed copies in the target cluster.
func (r *MongoDBSearchMetricsForwarderReconciler) replicateForwarderDependencies(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	agentSecretName, caConfigMapName string,
	w clusterWorkItem,
	log *zap.SugaredLogger,
) error {
	ns := search.Namespace
	labels := metricsForwarderLabelsForCluster(search, w.ClusterName, w.ClusterIndex)

	// Replicate agent-key Secret.
	srcSecret, err := r.kubeClient.GetSecret(ctx, kube.ObjectKey(ns, agentSecretName))
	if err != nil {
		return fmt.Errorf("failed to read agent-key Secret %s: %w", agentSecretName, err)
	}
	destSecret := kubeSecret.Builder().
		SetName(search.MetricsForwarderAgentKeySecretNameForCluster(w.ClusterIndex)).
		SetNamespace(ns).
		SetByteData(srcSecret.Data).
		SetDataType(srcSecret.Type).
		SetLabels(labels).
		Build()
	if err := kubeSecret.CreateOrUpdate(ctx, w.Client, destSecret); err != nil {
		return fmt.Errorf("failed to replicate agent-key Secret to cluster %q: %w", w.ClusterName, err)
	}
	log.Debugf("Replicated agent-key Secret to cluster=%q", w.ClusterName)

	if caConfigMapName == "" {
		return nil
	}

	// Replicate OM CA ConfigMap.
	caData, err := configmap.ReadData(ctx, r.kubeClient, kube.ObjectKey(ns, caConfigMapName))
	if err != nil {
		return fmt.Errorf("failed to read OM CA ConfigMap %s: %w", caConfigMapName, err)
	}
	destCM := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.MetricsForwarderCACertConfigMapNameForCluster(w.ClusterIndex),
			Namespace: ns,
			Labels:    labels,
		},
		Data: caData,
	}
	if err := configmap.CreateOrUpdate(ctx, w.Client, destCM); err != nil {
		return fmt.Errorf("failed to replicate OM CA ConfigMap to cluster %q: %w", w.ClusterName, err)
	}
	log.Debugf("Replicated OM CA ConfigMap to cluster=%q", w.ClusterName)
	return nil
}

type forwarderContext struct {
	projectConfigMapRef corev1.LocalObjectReference
	agentApiKeySecret   corev1.LocalObjectReference
}

// resolveForwarderContext maps a supported search source to the Ops Manager connection details the
// forwarder needs. The second return is a pre-resolved Ops Manager group id, empty when it must be
// looked up later from the agent key. The final return is false for sources that don't support the
// forwarder (e.g. MongoDBCommunity), in which case the other returns are unset.
func (r *MongoDBSearchMetricsForwarderReconciler) resolveForwarderContext(mdbSearch *searchv1.MongoDBSearch, searchSource searchcontroller.SearchSourceDBResource) (forwarderContext, string, workflow.Status, bool) {
	// External sources, and any source with an explicit project config, resolve from that config.
	if mdbSearch.IsExternalMongoDBSource() || mdbSearch.MetricsForwarderHasExplicitProjectConfig() {
		fwdCtx, status := r.resolveFromExplicitProjectConfig(mdbSearch)
		return fwdCtx, "", status, true
	}

	// Internal enterprise sources (replica set or sharded) resolve from the MongoDB resource.
	switch source := searchSource.(type) {
	case searchcontroller.EnterpriseResourceSearchSource:
		fwdCtx, groupId, status := r.resolveFromEnterpriseSource(source.MongoDB)
		return fwdCtx, groupId, status, true
	case *searchcontroller.ShardedInternalSearchSource:
		fwdCtx, groupId, status := r.resolveFromEnterpriseSource(source.MongoDB)
		return fwdCtx, groupId, status, true
	default:
		return forwarderContext{}, "", workflow.OK(), false
	}
}

func (r *MongoDBSearchMetricsForwarderReconciler) resolveFromExplicitProjectConfig(search *searchv1.MongoDBSearch) (forwarderContext, workflow.Status) {
	config := search.Spec.Observability.MetricsForwarder
	if config.OpsManager == nil {
		return forwarderContext{}, workflow.Invalid(".spec.observability.metricsForwarder.opsManager must be provided for external MongoDB sources")
	}

	return forwarderContext{
		projectConfigMapRef: config.OpsManager.ProjectConfigMapRef,
		agentApiKeySecret:   config.OpsManager.AgentCredentials,
	}, workflow.OK()
}

func (r *MongoDBSearchMetricsForwarderReconciler) resolveFromEnterpriseSource(source *mdbv1.MongoDB) (forwarderContext, string, workflow.Status) {
	groupId := source.Status.ProjectId
	if groupId == "" {
		return forwarderContext{}, "", workflow.Pending("Waiting for project to be reconciled")
	}

	return forwarderContext{
		projectConfigMapRef: corev1.LocalObjectReference{Name: source.GetProjectConfigMapName()},
		agentApiKeySecret:   corev1.LocalObjectReference{Name: agents.ApiKeySecretName(groupId)},
	}, groupId, workflow.OK()
}

func (r *MongoDBSearchMetricsForwarderReconciler) resolveGroupFromAgentKey(ctx context.Context, projectConfig mdbv1.ProjectConfig, agentSecretKey types.NamespacedName, log *zap.SugaredLogger) (string, error) {
	agentKey, err := r.readAgentApiKey(ctx, agentSecretKey)
	if err != nil {
		return "", err
	}

	// this specific endpoint doesn't take a groupId as the username, since calling it implies we don't have a groupId to begin with.
	// that's why it breaks from the convention of other agent endpoints which user the groupId as a username, it's by design.
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("apiKey:"+agentKey))

	body, err := r.omRequester.RequestWithAgentAuth(projectConfig, "GET", "/agents/api/group/v1", authHeader, nil)
	if err != nil {
		return "", fmt.Errorf("failed to call agents group API: %w", err)
	}

	var response struct {
		GroupId string `json:"groupId"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to parse group API response: %w", err)
	}

	if response.GroupId == "" {
		return "", fmt.Errorf("groupId not found in agents group API response")
	}

	log.Debugf("Resolved group ID %s from agent key", response.GroupId)
	return response.GroupId, nil
}

// readAgentApiKey reads the Ops Manager agent API key from the given secret.
func (r *MongoDBSearchMetricsForwarderReconciler) readAgentApiKey(ctx context.Context, agentSecretKey types.NamespacedName) (string, error) {
	secretData, err := r.secretClient.ReadSecret(ctx, agentSecretKey, "")
	if err != nil {
		return "", fmt.Errorf("failed to read agent key secret %s: %w", agentSecretKey.Name, err)
	}

	agentKey, ok := secretData[util.OmAgentApiKey]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s", util.OmAgentApiKey, agentSecretKey.Name)
	}

	return agentKey, nil
}

// omAgentRequester performs Ops Manager agent-authenticated HTTP requests. It is a reconciler
// dependency so tests can stub the network without a live Ops Manager. The default implementation,
// omHTTPAgentRequester, wraps the real Ops Manager HTTP client.
type omAgentRequester interface {
	RequestWithAgentAuth(projectConfig mdbv1.ProjectConfig, method, path, authHeader string, body any) ([]byte, error)
	// GetOMVersion fetches the Ops Manager version by calling the unauthenticated /unauth/versionManifest
	// endpoint and extracting the version string from the X-MongoDB-Service-Version response header.
	GetOMVersion(projectConfig mdbv1.ProjectConfig) (versionutil.OpsManagerVersion, error)
}

type omHTTPAgentRequester struct{}

func (r omHTTPAgentRequester) RequestWithAgentAuth(projectConfig mdbv1.ProjectConfig, method, path, authHeader string, body any) ([]byte, error) {
	respBody, _, err := newOMHTTPClient(projectConfig).RequestWithAgentAuth(method, projectConfig.BaseURL, path, authHeader, body)
	return respBody, err
}

func (r omHTTPAgentRequester) GetOMVersion(projectConfig mdbv1.ProjectConfig) (versionutil.OpsManagerVersion, error) {
	_, headers, err := newOMHTTPClient(projectConfig).Request("GET", projectConfig.BaseURL, "/api/public/v1.0/unauth/versionManifest", nil)
	if err != nil {
		return versionutil.OpsManagerVersion{}, fmt.Errorf("failed to fetch Ops Manager version manifest: %w", err)
	}
	versionStr := versionutil.GetVersionFromOpsManagerApiHeader(headers.Get("X-MongoDB-Service-Version"))
	return versionutil.OpsManagerVersion{VersionString: versionStr}, nil
}

// checkOMVersionForMetricsEndpoint verifies that the Ops Manager instance reachable via projectConfig
// supports the metrics forwarding ingest endpoint (requires >= 8.0.25). It returns (false, status)
// when the forwarder must not proceed, and (true, workflow.OK()) when it may.
//
// The connection is "explicit" when the user directly populated .spec.observability.metricsForwarder.opsManager
// (including all external MongoDB sources, which always require it). It is "implicit" when the
// connection details are inferred from the underlying MongoDB resource's Ops Manager configmap.
//
// Failure semantics (implicit = inferred from MongoDB resource; explicit = user set .opsManager):
//   - OM unreachable (fetch error):          Pending — retries on the next reconcile.
//   - Unknown version or unparseable semver: Implicit → Unsupported; Explicit → Failed.
//   - Cloud Manager connection:              Implicit → Unsupported; Explicit → Failed.
//   - Self-hosted OM version < 8.0.25:      Implicit → Unsupported; Explicit → Failed.
func (r *MongoDBSearchMetricsForwarderReconciler) checkOMVersionForMetricsEndpoint(mdbSearch *searchv1.MongoDBSearch, projectConfig mdbv1.ProjectConfig, log *zap.SugaredLogger) (bool, workflow.Status) {
	explicit := mdbSearch.MetricsForwarderHasExplicitProjectConfig()

	const disableHint = " Set '.spec.observability.metricsForwarder.mode: disabled' to silence this message"

	omVersion, err := r.omRequester.GetOMVersion(projectConfig)
	if err != nil {
		log.Warnf("Failed to fetch Ops Manager version for metrics endpoint check: %v", err)
		return false, workflow.Pending("Checking Ops Manager version compatibility.")
	}

	const unknownVersionMsg = "Could not determine Ops Manager version; the metrics forwarder may not function correctly." + disableHint

	if omVersion.IsUnknown() {
		if explicit {
			return false, workflow.Failed(fmt.Errorf("%s", unknownVersionMsg))
		}
		return false, workflow.Unsupported(unknownVersionMsg)
	}

	const cmMsg = "Ops Manager metrics forwarding endpoint is not available for Cloud Manager connections." + disableHint

	if omVersion.IsCloudManager() {
		log.Infof("Metrics forwarder is not supported for Cloud Manager connections (version %s)", omVersion)
		if explicit {
			return false, workflow.Failed(fmt.Errorf("%s", cmMsg))
		}
		return false, workflow.Unsupported(cmMsg)
	}

	sv, err := omVersion.Semver()
	if err != nil {
		log.Warnf("Failed to parse Ops Manager version %q: %v", omVersion, err)
		if explicit {
			return false, workflow.Failed(fmt.Errorf("%s", unknownVersionMsg))
		}
		return false, workflow.Unsupported(unknownVersionMsg)
	}

	minVersion := semver.MustParse(metricsForwarderMinOpsManagerVersion)
	if sv.GTE(minVersion) {
		return true, workflow.OK()
	}

	const message = "Ops Manager version %s does not support the metrics forwarding endpoint (minimum: %s)." + disableHint

	if explicit {
		return false, workflow.Failed(fmt.Errorf(message, omVersion, metricsForwarderMinOpsManagerVersion)) //nolint:staticcheck // ST1005: "Ops Manager" is a proper product name
	}
	return false, workflow.Unsupported(message, omVersion, metricsForwarderMinOpsManagerVersion)
}

// newOMHTTPClient builds an Ops Manager HTTP client honouring the project's TLS configuration.
func newOMHTTPClient(projectConfig mdbv1.ProjectConfig) *omapi.Client {
	opts := omapi.NewHTTPOptions()
	if projectConfig.SSLMMSCAConfigMapContents != "" {
		opts = append(opts, omapi.OptionCAValidate(projectConfig.SSLMMSCAConfigMapContents))
	}
	if !projectConfig.SSLRequireValidMMSServerCertificates {
		opts = append(opts, omapi.OptionSkipVerify)
	}

	// ignore the error return because the two options we add above never return an error during setup
	client, _ := omapi.NewHTTPClient(opts...)
	return client
}

// clusterTopologyState captures the MongoDBSearch topology for one member cluster persisted between reconciles.
type clusterTopologyState struct {
	Replicas             int            `json:"replicas"`
	ShardReplicas        map[string]int `json:"shardReplicas,omitempty"`
	PendingHostDeletions []string       `json:"pendingHostDeletions,omitempty"`
	// HostDeletionReadyAfter maps pod names to the Unix nanosecond timestamp after
	// which their OM host may be safely deregistered. Pods enter this map once
	// confirmed gone from Kubernetes; cleanup is deferred by hostDeletionDeferralWindow
	// (SD refresh + 2× scrape interval) to ensure the forwarder's Prometheus scrape
	// manager has fully stopped sending metrics before the host is removed from OM.
	HostDeletionReadyAfter map[string]int64 `json:"hostDeletionReadyAfter,omitempty"`
}

// searchTopologyState captures per-cluster topology persisted between reconciles, keyed by cluster name.
// Empty string key is the single-cluster (central) case.
type searchTopologyState struct {
	Clusters map[string]clusterTopologyState `json:"clusters,omitempty"`
}

// metricsForwarderStateOwner adapts a MongoDBSearch to the v1.ResourceOwner interface required by
// StateStore. It uses a dedicated name so the topology state is persisted in a config map distinct
// from any state owned by the main MongoDBSearch controller.
type metricsForwarderStateOwner struct {
	*searchv1.MongoDBSearch
}

func (o metricsForwarderStateOwner) GetName() string {
	return o.Name + "-metrics-forwarder"
}

func (o metricsForwarderStateOwner) GetNamespace() string {
	return o.Namespace
}

func (o metricsForwarderStateOwner) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(o.GetNamespace(), o.GetName())
}

func (o metricsForwarderStateOwner) GetOwnerLabels() map[string]string {
	return metricsForwarderLabels(o.MongoDBSearch)
}

func (r *MongoDBSearchMetricsForwarderReconciler) openTopologyStateStore(search *searchv1.MongoDBSearch) *StateStore[searchTopologyState] {
	opts := []StateStoreOption{}
	if search.UID != "" {
		opts = append(opts, WithStateStoreOwnerUID(string(search.UID)))
	}
	return NewStateStore[searchTopologyState](metricsForwarderStateOwner{MongoDBSearch: search}, nil, r.kubeClient, opts...)
}

// reconcileTopologyState reconciles the topology state for one cluster work item: it detects
// removed mongot pods, advances each through the deletion state machine, deregisters hosts in Ops
// Manager at the right moment, and persists the updated state. Returns true while any deletion is
// still in flight, which causes the caller to requeue.
//
// # Why a state machine
//
// Deleting a MongoDBSearch pod while the metrics forwarder is running creates a race: the OTel
// collector's Prometheus SD cache and in-flight scrape cycle may still reference the pod for up to
// (SDRefreshInterval + 2×ScrapeInterval) after the pod disappears from DNS. If the OM host is
// deregistered before that window closes, the forwarder will push metrics for the host and OM will
// implicitly re-add it, making the deregistration a no-op. The state machine therefore defers the
// OM deregistration call until both (a) the pod is fully gone from Kubernetes and (b) the deferral
// window has elapsed, ensuring the deregistration sticks.
//
// # Pod lifecycle through the state machine
//
//	Active (in current topology)
//	  │  replica count decreases, shard removed, or CR deleted
//	  ▼
//	PendingHostDeletions  ─── pod.DeletionTimestamp is set; pod still running (grace period)
//	  │  pod disappears from the API (NotFound or no DeletionTimestamp)
//	  ▼
//	HostDeletionReadyAfter  ── pod is gone; timer set to now + hostDeletionDeferralWindow
//	  │  time.Now() >= readyAt
//	  ▼
//	Cleaned  ────────────── OM host deregistered via agents/api/hosts delete endpoint
//
// # State persistence
//
// The full state for all clusters under a MongoDBSearch CR is stored in a single ConfigMap keyed
// by metricsForwarderStateOwner. Each reconcile reads that map, updates the entry for w.ClusterName,
// and writes it back. Two fields carry pods across reconcile boundaries:
//   - PendingHostDeletions: pods with an active DeletionTimestamp (terminating).
//   - HostDeletionReadyAfter: pods confirmed gone, mapped to their earliest-safe-deregister timestamp.
//
// computeDeletedMongotPods feeds newly detected removals into the candidate set on each reconcile,
// so pods deleted between reconciles are never missed regardless of how many times the loop runs.
func (r *MongoDBSearchMetricsForwarderReconciler) reconcileTopologyState(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	shardNames []string,
	groupID string,
	projectConfig mdbv1.ProjectConfig,
	agentSecretName string,
	w clusterWorkItem,
	log *zap.SugaredLogger,
) (bool, error) {
	current := clusterTopologyState{}
	if len(shardNames) > 0 {
		current.ShardReplicas = make(map[string]int, len(shardNames))
		for _, shardName := range shardNames {
			c, err := search.ResolveSizingForClusterShard(w.ClusterName, shardName)
			if err != nil {
				return false, fmt.Errorf("failed to resolve sizing for cluster %q shard %q: %w", w.ClusterName, shardName, err)
			}
			current.ShardReplicas[shardName] = c.ReplicasOrDefault()
		}
	} else {
		c, err := search.ResolveSizingForClusterShard(w.ClusterName, "")
		if err != nil {
			return false, fmt.Errorf("failed to resolve sizing for cluster %q: %w", w.ClusterName, err)
		}
		current.Replicas = c.ReplicasOrDefault()
	}

	stateStore := r.openTopologyStateStore(search)

	var fullState searchTopologyState
	prevFull, err := stateStore.ReadState(ctx)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("failed to read topology state: %w", err)
		}
	} else {
		fullState = *prevFull
	}
	if fullState.Clusters == nil {
		fullState.Clusters = make(map[string]clusterTopologyState)
	}

	previous := fullState.Clusters[w.ClusterName]

	candidates := map[string]struct{}{}
	for _, podName := range previous.PendingHostDeletions {
		candidates[podName] = struct{}{}
	}
	for podName := range previous.HostDeletionReadyAfter {
		candidates[podName] = struct{}{}
	}
	for _, podName := range computeDeletedMongotPods(search, w.ClusterIndex, previous, current) {
		candidates[podName] = struct{}{}
	}

	now := time.Now()
	var readyToClean, stillTerminating []string
	hostDeletionReadyAfter := make(map[string]int64)

	for podName := range candidates {
		terminating, err := r.mongotPodIsTerminating(ctx, search.Namespace, podName, w.Client)
		if err != nil {
			return false, fmt.Errorf("failed to check whether mongot pod %s has a deletion timestamp: %w", podName, err)
		}
		if terminating {
			// Pod has a DeletionTimestamp — keep pending until it is fully gone.
			stillTerminating = append(stillTerminating, podName)
			continue
		}
		// Pod is gone (NotFound) or running without DeletionTimestamp.
		// Defer cleanup until SD cache + scrape cache have both expired.
		var readyAtNano int64
		if prev, ok := previous.HostDeletionReadyAfter[podName]; ok {
			readyAtNano = prev
		} else {
			readyAtNano = now.Add(hostDeletionDeferralWindow).UnixNano()
		}
		if now.UnixNano() >= readyAtNano {
			readyToClean = append(readyToClean, podName)
		} else {
			hostDeletionReadyAfter[podName] = readyAtNano
		}
	}

	if len(readyToClean) > 0 {
		sort.Strings(readyToClean)
		if err := r.cleanupRemovedMongotPods(ctx, search, readyToClean, groupID, projectConfig, agentSecretName, log); err != nil {
			return false, err
		}
	}

	sort.Strings(stillTerminating)
	current.PendingHostDeletions = stillTerminating
	if len(hostDeletionReadyAfter) > 0 {
		current.HostDeletionReadyAfter = hostDeletionReadyAfter
	}
	fullState.Clusters[w.ClusterName] = current

	if err := stateStore.WriteState(ctx, &fullState, log); err != nil {
		return false, fmt.Errorf("failed to write topology state: %w", err)
	}

	pending := len(stillTerminating) > 0 || len(hostDeletionReadyAfter) > 0
	return pending, nil
}

// mongotPodIsTerminating reports whether the named pod in the given cluster exists and is actively
// being deleted (DeletionTimestamp is set).
func (r *MongoDBSearchMetricsForwarderReconciler) mongotPodIsTerminating(ctx context.Context, namespace, podName string, c kubernetesClient.Client) (bool, error) {
	pod := &corev1.Pod{}
	if err := c.Get(ctx, kube.ObjectKey(namespace, podName), pod); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return !pod.DeletionTimestamp.IsZero(), nil
}

// computeDeletedMongotPods returns the flat list of mongot pod names that exist in the previous
// cluster topology but not in the current one.
func computeDeletedMongotPods(search *searchv1.MongoDBSearch, clusterIndex int, previous, current clusterTopologyState) []string {
	var deletedPods []string

	stsName := search.StatefulSetNamespacedNameForCluster(clusterIndex).Name
	for i := current.Replicas; i < previous.Replicas; i++ {
		deletedPods = append(deletedPods, fmt.Sprintf("%s-%d", stsName, i))
	}

	// Sharded: removed shards (current replica count is 0) and per-shard scale-down.
	for shardName, prevReplicas := range previous.ShardReplicas {
		shardStsName := search.MongotStatefulSetForClusterShard(clusterIndex, shardName).Name
		for i := current.ShardReplicas[shardName]; i < prevReplicas; i++ {
			deletedPods = append(deletedPods, fmt.Sprintf("%s-%d", shardStsName, i))
		}
	}

	return deletedPods
}

type deleteHostsRequest struct {
	HostIds []string `json:"hostIds"`
}

type deleteHostResult struct {
	HostId string `json:"hostId"`
	Status string `json:"status"`
}

type deleteHostsResponse struct {
	Results []deleteHostResult `json:"results"`
}

// cleanupRemovedMongotPods deletes the Ops Manager monitored hosts that correspond to mongot pods
// removed from the topology since the previous reconcile. Each host id matches the
// mongodb.opsmanager.host.id resource attribute the metrics forwarder assigns during OTLP ingestion.
// Deletion is scoped to the project's groupID via agent authentication; ids belonging to other
// projects or already-deleted hosts are reported NOT_FOUND and treated as success.
func (r *MongoDBSearchMetricsForwarderReconciler) cleanupRemovedMongotPods(ctx context.Context, search *searchv1.MongoDBSearch, podNames []string, groupID string, projectConfig mdbv1.ProjectConfig, agentSecretName string, log *zap.SugaredLogger) error {
	if len(podNames) == 0 {
		return nil
	}

	hostIDs := make([]string, len(podNames))
	for i, podName := range podNames {
		hostIDs[i] = mongotHostID(groupID, search.Namespace, podName)
	}

	agentKey, err := r.readAgentApiKey(ctx, kube.ObjectKey(search.Namespace, agentSecretName))
	if err != nil {
		return err
	}

	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(groupID+":"+agentKey))
	path := fmt.Sprintf("/agents/api/hosts/%s/v1/delete", groupID)

	body, err := r.omRequester.RequestWithAgentAuth(projectConfig, "POST", path, authHeader, deleteHostsRequest{HostIds: hostIDs})
	if err != nil {
		return fmt.Errorf("failed to call delete hosts API: %w", err)
	}

	var response deleteHostsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("failed to parse delete hosts API response: %w", err)
	}

	var failed []string
	for _, result := range response.Results {
		switch result.Status {
		case "DELETED", "NOT_FOUND":
			log.Debugf("Removed mongot host %s from Ops Manager (status: %s)", result.HostId, result.Status)
		case "SKIPPED_MANAGED":
			log.Warnf("Ops Manager skipped deleting Automation-managed mongot host %s", result.HostId)
		default: // ERROR or any unexpected status
			log.Warnf("Ops Manager failed to delete mongot host %s (status: %s)", result.HostId, result.Status)
			failed = append(failed, result.HostId)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("failed to delete mongot hosts from Ops Manager: %v", failed)
	}

	log.Infof("Cleaned up %d removed mongot host(s) from Ops Manager", len(hostIDs))
	return nil
}

// mongotHostID returns the stable Ops Manager host id for a mongot pod. It matches the
// mongodb.opsmanager.host.id resource attribute the metrics forwarder assigns during OTLP ingestion:
// sha1(groupID + "-" + namespace + "-" + podName). The groupID is folded in so the id is globally
// unique across all projects.
func mongotHostID(groupID, namespace, podName string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s-%s-%s", groupID, namespace, podName))) //nolint //Used to derive a stable host identifier, not for security.
	return hex.EncodeToString(sum[:])
}

// ensureMetricsForwarderConfigMap creates or updates the metrics forwarder ConfigMap for one cluster.
// OwnerReference is set only for the central cluster (clusterName == ""); member-cluster objects
// use labels for cross-cluster GC.
func (r *MongoDBSearchMetricsForwarderReconciler) ensureMetricsForwarderConfigMap(ctx context.Context, search *searchv1.MongoDBSearch, configYAML []byte, clusterName string, clusterIndex int, c kubernetesClient.Client, log *zap.SugaredLogger) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.MetricsForwarderConfigMapNameForCluster(clusterIndex),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		cm.Labels = metricsForwarderLabelsForCluster(search, clusterName, clusterIndex)
		cm.Data = map[string]string{metricsForwarderConfigFileName: string(configYAML)}
		if clusterName == "" {
			return controllerutil.SetOwnerReference(search, cm, c.Scheme())
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure metrics forwarder ConfigMap: %w", err)
	}
	log.Infof("metrics forwarder ConfigMap created/updated (cluster=%q)", clusterName)
	return nil
}

// ensureMetricsForwarderDeployment creates or updates the metrics forwarder Deployment for one cluster.
// OwnerReference is set only for the central cluster (clusterName == "").
func (r *MongoDBSearchMetricsForwarderReconciler) ensureMetricsForwarderDeployment(ctx context.Context, search *searchv1.MongoDBSearch, configYAML []byte, groupID, agentKeySecretName, caConfigMapName, clusterName string, clusterIndex int, c kubernetesClient.Client, log *zap.SugaredLogger) error {
	configHash := fmt.Sprintf("%x", sha256.Sum256(configYAML))
	labels := metricsForwarderLabelsForCluster(search, clusterName, clusterIndex)
	podLabels := metricsForwarderPodLabelsForCluster(search, clusterIndex)
	resources := metricsForwarderResourceRequirements(search)
	managedSecurityContext := env.ReadBoolOrDefault(podtemplatespec.ManagedSecurityContextEnv, false) // nolint:forbidigo

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.MetricsForwarderDeploymentNameForCluster(clusterIndex),
			Namespace: search.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, c, dep, func() error {
		dep.Labels = labels

		dep.Spec = appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						metricsForwarderConfigHashAnnotation: configHash,
					},
				},
				Spec: buildMetricsForwarderPodSpec(search, agentKeySecretName, caConfigMapName, clusterIndex, r.defaultImage, resources, managedSecurityContext),
			},
		}

		deploymentOverride := search.Spec.Observability.MetricsForwarder.Deployment
		if deploymentOverride != nil {
			dep.Spec = merge.DeploymentSpecs(dep.Spec, deploymentOverride.SpecWrapper.Spec)
			dep.Labels = merge.StringToStringMap(dep.Labels, deploymentOverride.MetadataWrapper.Labels)
			dep.Annotations = merge.StringToStringMap(dep.Annotations, deploymentOverride.MetadataWrapper.Annotations)
		}

		if clusterName == "" {
			return controllerutil.SetOwnerReference(search, dep, c.Scheme())
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure metrics forwarder Deployment: %w", err)
	}
	log.Infof("metrics forwarder Deployment created/updated (cluster=%q)", clusterName)
	return nil
}

func buildMetricsForwarderPodSpec(search *searchv1.MongoDBSearch, agentKeySecretName, caConfigMapName string, clusterIndex int, image string, resources corev1.ResourceRequirements, managedSecurityContext bool) corev1.PodSpec {
	volumes := []corev1.Volume{
		{
			Name: "metrics-forwarder-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: search.MetricsForwarderConfigMapNameForCluster(clusterIndex)},
				},
			},
		},
		{
			Name: construct.AgentAPIKeyVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: agentKeySecretName,
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "metrics-forwarder-config", MountPath: metricsForwarderConfigPath, ReadOnly: true},
		{Name: construct.AgentAPIKeyVolumeName, MountPath: construct.AgentAPIKeySecretPath, ReadOnly: true},
	}

	// Mount CA cert if configured
	if caConfigMapName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: metricsForwarderCACertVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: caConfigMapName},
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: metricsForwarderCACertVolumeName, MountPath: metricsForwarderCACertMountPath, ReadOnly: true,
		})
	}

	envVars := []corev1.EnvVar{
		// Namespace the forwarder runs in; sourced from the pod's own metadata at runtime.
		{Name: "MONGOT_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
		}},
		// OTel collector tuning knobs. Headroom (as a percentage of the memory limit) the memory_limiter
		// keeps free before it starts refusing data to avoid OOM kills.
		{Name: "MEMORY_LIMITER_SPIKE_PERCENTAGE", Value: "20"},
		// Number of metric data points the batch processor groups before exporting.
		{Name: "BATCH_SIZE", Value: "8192"},
		// Maximum time the batch processor waits before flushing a partial batch.
		{Name: "BATCH_TIMEOUT", Value: "30s"},
		// Number of batches the exporter can buffer when the Ops Manager endpoint is slow or unavailable.
		{Name: "SENDING_QUEUE_SIZE", Value: "1000"},
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
				Name:            "metrics-forwarder",
				Image:           image,
				Args:            []string{"--config", metricsForwarderConfigPath + "/" + metricsForwarderConfigFileName},
				Env:             envVars,
				Resources:       resources,
				SecurityContext: containerSecurityContext,
				VolumeMounts:    volumeMounts,
			},
		},
		Volumes: volumes,
	}
}

func metricsForwarderResourceRequirements(search *searchv1.MongoDBSearch) corev1.ResourceRequirements {
	reqs := search.Spec.Observability.MetricsForwarder.ResourceRequirements
	if len(reqs.Requests) == 0 && len(reqs.Limits) == 0 {
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
	}
	return reqs
}

// metricsForwarderLabelsForCluster returns resource labels including cross-cluster enqueue labels.
// clusterName=="" (single-cluster/central): cluster-name label is omitted.
func metricsForwarderLabelsForCluster(search *searchv1.MongoDBSearch, clusterName string, clusterIndex int) map[string]string {
	labels := map[string]string{
		"app":                                search.MetricsForwarderDeploymentNameForCluster(clusterIndex),
		"component":                          metricsForwarderLabelName,
		khandler.MongoDBSearchOwnerNameLabel: search.Name,
		khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
	}
	if clusterName != "" {
		labels[khandler.MongoDBSearchClusterNameLabel] = clusterName
	}
	return labels
}

func metricsForwarderPodLabelsForCluster(search *searchv1.MongoDBSearch, clusterIndex int) map[string]string {
	return map[string]string{
		"app": search.MetricsForwarderDeploymentNameForCluster(clusterIndex),
	}
}

func metricsForwarderLabels(search *searchv1.MongoDBSearch) map[string]string {
	return metricsForwarderLabelsForCluster(search, "", 0)
}

func metricsForwarderPodLabels(search *searchv1.MongoDBSearch) map[string]string {
	return metricsForwarderPodLabelsForCluster(search, 0)
}

// updateMetricsForwarderStatus patches the metricsForwarder sub-status.
func (r *MongoDBSearchMetricsForwarderReconciler) updateMetricsForwarderStatus(ctx context.Context, search *searchv1.MongoDBSearch, st workflow.Status, log *zap.SugaredLogger) (reconcile.Result, error) {
	partOption := searchv1.NewSearchPartOption(searchv1.SearchPartMetricsForwarder)
	return commoncontroller.UpdateStatus(ctx, r.kubeClient, search, st, log, partOption)
}

// clearMetricsForwarderStatus removes the metricsForwarder substatus.
func (r *MongoDBSearchMetricsForwarderReconciler) clearMetricsForwarderStatus(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) (reconcile.Result, error) {
	search.Status.MetricsForwarder = nil
	partOption := searchv1.NewSearchPartOption(searchv1.SearchPartMetricsForwarder)
	return commoncontroller.UpdateStatus(ctx, r.kubeClient, search, workflow.OK(), log, partOption)
}

func (r *MongoDBSearchMetricsForwarderReconciler) ensureFinalizer(ctx context.Context, search *searchv1.MongoDBSearch, log *zap.SugaredLogger) error {
	if finalizerAdded := controllerutil.AddFinalizer(search, util.SearchMetricsForwarderFinalizer); finalizerAdded {
		log.Info("Adding finalizer to the MongoDBSearch resource")
		if err := r.kubeClient.Update(ctx, search); err != nil {
			return err
		}
	}
	return nil
}

func (r *MongoDBSearchMetricsForwarderReconciler) preDeletionCleanup(ctx context.Context, search *searchv1.MongoDBSearch, groupID string, projectConfig mdbv1.ProjectConfig, agentSecretName string, workList []clusterWorkItem, log *zap.SugaredLogger) workflow.Status {
	log.Info("Performing pre deletion cleanup before deleting MongoDBSearch metrics forwarder")

	stateStore := r.openTopologyStateStore(search)
	topologyState, err := stateStore.ReadState(ctx)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return workflow.Failed(fmt.Errorf("failed to read the topology state: %w", err))
		}
		topologyState = &searchTopologyState{}
	}

	// Check for running Deployments. If any exist, delete them and requeue. We must
	// not deregister OM hosts while any forwarder pod is still running — a live collector would
	// push metrics for those hosts, causing OM to implicitly re-add them and making the
	// deregistration a no-op. We gate on the Deployments being fully gone (no running pods)
	// rather than a fixed timer: once the Deployment is deleted and all its pods have terminated,
	// there is no collector left to scrape and forward metrics.
	//
	// Checking before deleting ensures the requeue happens: if we deleted first and then checked,
	// the check could find the object already gone (e.g. fake client in tests) and skip the wait.
	ns := search.Namespace
	anyDepFound := false
	for _, w := range workList {
		if w.Client == nil {
			continue
		}
		depName := search.MetricsForwarderDeploymentNameForCluster(w.ClusterIndex)
		dep := &appsv1.Deployment{}
		if err := w.Client.Get(ctx, kube.ObjectKey(ns, depName), dep); err == nil {
			anyDepFound = true
			log.Infof("Deleting metrics forwarder Deployment %s (cluster=%q) before deregistering Ops Manager hosts", depName, w.ClusterName)
			if delErr := w.Client.Delete(ctx, dep); delErr != nil && !apierrors.IsNotFound(delErr) {
				log.Warnf("Failed to delete metrics forwarder Deployment %s (cluster=%q): %s", depName, w.ClusterName, delErr)
			}
		} else if !apierrors.IsNotFound(err) {
			return workflow.Failed(fmt.Errorf("failed to check metrics forwarder Deployment %s: %w", depName, err))
		}
	}
	if anyDepFound {
		return workflow.Pending("Waiting for metrics forwarder Deployment to be fully deleted before deregistering Ops Manager hosts")
	}

	// All Deployments are gone: deregister hosts, clean up remaining resources, and remove finalizer.
	for _, w := range workList {
		if w.Client == nil {
			continue
		}
		clusterState := topologyState.Clusters[w.ClusterName]
		mongotHostsToDelete := computeDeletedMongotPods(search, w.ClusterIndex, clusterState, clusterTopologyState{})
		if err := r.cleanupRemovedMongotPods(ctx, search, mongotHostsToDelete, groupID, projectConfig, agentSecretName, log); err != nil {
			return workflow.Failed(err)
		}
	}

	r.deleteMetricsForwarderResources(ctx, search, workList, log)

	if finalizerRemoved := controllerutil.RemoveFinalizer(search, util.SearchMetricsForwarderFinalizer); !finalizerRemoved {
		return workflow.Failed(fmt.Errorf("failed to remove finalizer"))
	}

	if err := r.kubeClient.Update(ctx, search); err != nil {
		return workflow.Failed(fmt.Errorf("failed to update resource with removed finalizer: %w", err))
	}

	return workflow.OK()
}

// deleteMetricsForwarderResources removes per-cluster metrics forwarder resources.
// Clusters not registered with the operator (Client==nil) are skipped.
func (r *MongoDBSearchMetricsForwarderReconciler) deleteMetricsForwarderResources(ctx context.Context, search *searchv1.MongoDBSearch, workList []clusterWorkItem, log *zap.SugaredLogger) {
	ns := search.Namespace
	for _, w := range workList {
		if w.Client == nil {
			continue
		}
		depName := search.MetricsForwarderDeploymentNameForCluster(w.ClusterIndex)
		cmName := search.MetricsForwarderConfigMapNameForCluster(w.ClusterIndex)
		secretName := search.MetricsForwarderAgentKeySecretNameForCluster(w.ClusterIndex)

		dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: depName, Namespace: ns}}
		if err := w.Client.Delete(ctx, dep); err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Failed to delete metrics forwarder Deployment %s (cluster=%q): %s", depName, w.ClusterName, err)
		} else if err == nil {
			log.Infof("Deleted metrics forwarder Deployment %s (cluster=%q)", depName, w.ClusterName)
		}

		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns}}
		if err := w.Client.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Failed to delete metrics forwarder ConfigMap %s (cluster=%q): %s", cmName, w.ClusterName, err)
		} else if err == nil {
			log.Infof("Deleted metrics forwarder ConfigMap %s (cluster=%q)", cmName, w.ClusterName)
		}

		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns}}
		if err := w.Client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Failed to delete metrics forwarder agent-key Secret %s (cluster=%q): %s", secretName, w.ClusterName, err)
		} else if err == nil {
			log.Infof("Deleted metrics forwarder agent-key Secret %s (cluster=%q)", secretName, w.ClusterName)
		}

		caName := search.MetricsForwarderCACertConfigMapNameForCluster(w.ClusterIndex)
		caCM := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: caName, Namespace: ns}}
		if err := w.Client.Delete(ctx, caCM); err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Failed to delete metrics forwarder CA ConfigMap %s (cluster=%q): %s", caName, w.ClusterName, err)
		} else if err == nil {
			log.Infof("Deleted metrics forwarder CA ConfigMap %s (cluster=%q)", caName, w.ClusterName)
		}
	}
}

// AddMongoDBSearchMetricsForwarderController registers the metrics forwarder controller with the manager.
func AddMongoDBSearchMetricsForwarderController(ctx context.Context, mgr manager.Manager, defaultImage string, memberClusterObjectsMap map[string]runtimeCluster.Cluster, operatorClusterName string) error {
	r := newMongoDBSearchMetricsForwarderReconciler(mgr.GetClient(), defaultImage, multicluster.ClustersMapToClientMap(memberClusterObjectsMap), operatorClusterName)

	c, err := controller.New("mongodbsearchmetricsforwarder", mgr, controller.Options{
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
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{}, &watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: r.watch})); err != nil {
		return err
	}

	// Per-member-cluster resource watches: label-based mapper, since cross-cluster owner refs don't GC.
	mapper := handler.EnqueueRequestsFromMapFunc(khandler.EnqueueMemberClusterObjectToSearch)
	for k, v := range memberClusterObjectsMap {
		if err := c.Watch(source.Kind[client.Object](v.GetCache(), &appsv1.Deployment{}, mapper)); err != nil {
			return fmt.Errorf("failed to set metrics forwarder Deployment watch on member cluster %s: %w", k, err)
		}
		if err := c.Watch(source.Kind[client.Object](v.GetCache(), &corev1.ConfigMap{}, mapper)); err != nil {
			return fmt.Errorf("failed to set metrics forwarder ConfigMap watch on member cluster %s: %w", k, err)
		}
	}

	return nil
}
