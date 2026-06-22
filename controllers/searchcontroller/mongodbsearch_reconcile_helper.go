package searchcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/ghodss/yaml"
	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
)

const (
	unsupportedSearchVersion         = "1.47.0"
	unsupportedSearchVersionErrorFmt = "MongoDBSearch version %s is not supported because of breaking changes. " +
		"The operator will ignore this resource: it will not reconcile or reconfigure the workload. " +
		"Existing deployments will continue to run, but cannot be managed by the operator. " +
		"To regain operator management, you must delete and recreate the MongoDBSearch resource."

	// embeddingKeyFilePath is the path that is used in mongot config to specify the api keys
	// this where query and index keys would be available.
	embeddingKeyFilePath   = "/etc/mongot/secrets"
	embeddingKeyVolumeName = "auto-embedding-api-keys"

	indexingKeyName = "indexing-key"
	queryKeyName    = "query-key"

	apiKeysTempVolumeName = "api-keys-config" //nolint:gosec // volume name, not a credential
	// To overcome the strict requirement of api keys having 0400 permission we mount the api keys
	// to a temp location apiKeysTempVolumeMount and then copy it to correct location embeddingKeyFilePath,
	// changing the permission to 0400.
	apiKeysTempVolumeMount = "/tmp/auto-embedding-api-keys" //nolint:gosec // mount path, not a credential

	// is the minimum search image version that is required to enable the auto embeddings for vector search
	minSearchImageVersionForEmbedding = "0.60.0"

	// autoEmbeddingDetailsAnnKey has the annotation key that would be added to search pod with emebdding API Key secret hash
	autoEmbeddingDetailsAnnKey = "autoEmbeddingDetailsHash"
)

type OperatorSearchConfig struct {
	SearchRepo    string
	SearchName    string
	SearchVersion string
}

type MongoDBSearchReconcileHelper struct {
	client               kubernetesClient.Client
	mdbSearch            *searchv1.MongoDBSearch
	db                   SearchSourceDBResource
	operatorSearchConfig OperatorSearchConfig

	// memberClusterClients holds per-member-cluster Kubernetes clients keyed by
	// spec.clusters[i].clusterName. Empty in single-cluster installs.
	memberClusterClients map[string]kubernetesClient.Client

	// state is the per-CR persisted state from the search state ConfigMap:
	// the clusterName → clusterIndex mapping (per-cluster resource names use
	// these indexes so spec.clusters[] reorders don't rename resources) and the
	// routing-ready switch. Refreshed after every successful switch write.
	state *SearchDeploymentState
}

// NewMongoDBSearchReconcileHelper constructs a reconcile helper. Pass nil for
// memberClusterClients on single-cluster installs; nil state is treated as fresh.
func NewMongoDBSearchReconcileHelper(
	client kubernetesClient.Client,
	mdbSearch *searchv1.MongoDBSearch,
	db SearchSourceDBResource,
	operatorSearchConfig OperatorSearchConfig,
	memberClusterClients map[string]kubernetesClient.Client,
	state *SearchDeploymentState,
) *MongoDBSearchReconcileHelper {
	if state == nil {
		state = NewSearchDeploymentState()
	}
	return &MongoDBSearchReconcileHelper{
		client:               client,
		operatorSearchConfig: operatorSearchConfig,
		mdbSearch:            mdbSearch,
		db:                   db,
		memberClusterClients: memberClusterClients,
		state:                state,
	}
}

// searchOwnerLabels returns the cross-cluster enqueue labels that every
// member-cluster resource owned by this MongoDBSearch must carry. Owner refs
// don't cross cluster boundaries; labels are the only path back from a member
// resource to its central CR (for watch routing and label-based GC).
func searchOwnerLabels(search *searchv1.MongoDBSearch, clusterName string) map[string]string {
	labels := map[string]string{
		khandler.MongoDBSearchOwnerNameLabel:      search.Name,
		khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
	}
	if clusterName != "" {
		labels[khandler.MongoDBSearchClusterNameLabel] = clusterName
	}
	return labels
}

// withSearchOwnerLabels merges the search-owner labels into the STS ObjectMeta
// labels. It does NOT touch the selector/pod-template (which is immutable on
// existing STS); the pod-routing labels stay in the unit's podLabels.
func withSearchOwnerLabels(search *searchv1.MongoDBSearch, clusterName string) statefulset.Modification {
	return func(set *appsv1.StatefulSet) {
		if set.Labels == nil {
			set.Labels = map[string]string{}
		}
		for k, v := range searchOwnerLabels(search, clusterName) {
			set.Labels[k] = v
		}
	}
}

// clientForCluster returns the Kubernetes client for a unit's member cluster.
// Empty clusterName / empty memberClusterClients map fall back to the central
// client (single-cluster install). Unknown clusterName is an error.
func (r *MongoDBSearchReconcileHelper) clientForCluster(clusterName string) (kubernetesClient.Client, error) {
	if clusterName == "" || len(r.memberClusterClients) == 0 {
		return r.client, nil
	}
	c, ok := r.memberClusterClients[clusterName]
	if !ok {
		return nil, xerrors.Errorf("no Kubernetes client registered for cluster %q", clusterName)
	}
	return c, nil
}

// reconcileUnit captures all per-unit (per-shard or single-RS) resource names,
// labels, and config. Topology-free: every per-shard vs. per-RS difference is
// encoded by the factory, so downstream code never branches on "am I sharded?".
type reconcileUnit struct {
	stsName             types.NamespacedName
	headlessSvc         types.NamespacedName
	proxySvc            types.NamespacedName
	configMapName       types.NamespacedName
	podLabels           map[string]string
	additionalSvcLabels map[string]string
	publishNotReady     bool
	extraHeadlessPorts  []corev1.ServicePort
	logFields           []any // k/v fields attached to the per-unit logger; nil for single-unit topologies
	tlsResource         tls.TLSConfigurableResource
	mongotConfigFn      mongot.Modification
	clusterName         string // "" routes to the central client (single-cluster)
	clusterIndex        int
	shardName           string               // shard name for sharded topologies; "" for RS
	sizing              searchv1.ClusterSpec // resolved per-(cluster, shard) sizing, see ResolveSizingForClusterShard
}

// SearchSourceReplicaSet is the subset of SearchSourceDBResource the RS plan
// needs; tests can stub it.
type SearchSourceReplicaSet interface {
	HostSeeds(shardName string) ([]string, error)
}

// clusterLevelResource describes the shard-agnostic proxy Service that mongos
// connects to. One entry per cluster in sharded deployments when the operator
// manages proxy Services.
// fallbackPodLabel is the first shard's mongot STS label; used when no managed
// LB is configured (pre-MC behaviour: mongos hits the first shard directly).
type clusterLevelResource struct {
	clusterName      string
	clusterIndex     int
	svcName          types.NamespacedName
	fallbackPodLabel string
}

// reconcilePlan is the full per-reconcile work description: a list of units plus the
// topology-wide knobs and hooks that surround the loop. Everything sharded-specific lives
// here in hook closures so the reconcile body stays a straight, unbranched sequence.
type reconcilePlan struct {
	units                 []reconcileUnit
	clusterLevelResources []clusterLevelResource
	manageProxySvc        bool                                                      // topology-wide: true when the operator owns the proxy Service lifecycle (i.e. the LB is not user-managed)
	preflight             func(context.Context, *zap.SugaredLogger) workflow.Status // runs before the loop; must return workflow.OK() to proceed
	cleanup               func(context.Context, *zap.SugaredLogger)                 // runs after the loop; best-effort, errors logged
}

// buildReconcilePlan returns the full reconcile plan for the current topology.
// All sharded-vs-RS differences are resolved here so the reconcile body is topology-agnostic.
func (r *MongoDBSearchReconcileHelper) buildReconcilePlan(log *zap.SugaredLogger) (reconcilePlan, error) {
	if shardedSource, ok := r.db.(SearchSourceShardedDeployment); ok {
		log.Infof("Reconciling MongoDBSearch for sharded source deployment with %d shards", shardedSource.GetShardCount())
		return r.buildShardedPlan(shardedSource)
	}
	return r.buildReplicaSetPlan(r.db)
}

// buildReplicaSetPlan returns one reconcileUnit per cluster. Single-cluster is a
// 1-element work list with unindexed names; MC indexes via state.ClusterMapping.
func (r *MongoDBSearchReconcileHelper) buildReplicaSetPlan(rsSource SearchSourceReplicaSet) (reconcilePlan, error) {
	hostSeeds, err := rsSource.HostSeeds("")
	if err != nil {
		return reconcilePlan{}, err
	}

	var extraPorts []corev1.ServicePort
	if r.mdbSearch.IsWireprotoEnabled() {
		extraPorts = []corev1.ServicePort{{
			Name:       "mongot-wireproto",
			Protocol:   corev1.ProtocolTCP,
			Port:       r.mdbSearch.GetMongotWireprotoPort(),
			TargetPort: intstr.FromInt32(r.mdbSearch.GetMongotWireprotoPort()),
		}}
	}

	work := r.buildRSWorkList()
	units := make([]reconcileUnit, 0, len(work))
	for _, w := range work {
		sizing, err := r.mdbSearch.ResolveSizingForClusterShard(w.ClusterName, "")
		if err != nil {
			return reconcilePlan{}, err
		}
		stsName, headlessSvc, proxySvc, configMapName := r.rsResourceNames(w)
		mongotConfigFn := mongot.Apply(
			baseMongotConfig(r.mdbSearch, hostSeeds),
			wireprotoMongotMod(r.mdbSearch),
			featureFlagsMongotMod(r.mdbSearch),
			replicationReaderTagSetsMod(w.SyncSourceSelector))
		units = append(units, reconcileUnit{
			stsName:            stsName,
			headlessSvc:        headlessSvc,
			proxySvc:           proxySvc,
			configMapName:      configMapName,
			podLabels:          map[string]string{appLabelKey: headlessSvc.Name},
			extraHeadlessPorts: extraPorts,
			tlsResource:        r.mdbSearch,
			mongotConfigFn:     mongotConfigFn,
			clusterName:        w.ClusterName,
			clusterIndex:       w.ClusterIndex,
			sizing:             sizing,
		})
	}

	return reconcilePlan{
		units:          units,
		manageProxySvc: !r.mdbSearch.IsReplicaSetUnmanagedLB(),
		preflight:      func(context.Context, *zap.SugaredLogger) workflow.Status { return workflow.OK() },
		cleanup:        func(context.Context, *zap.SugaredLogger) {},
	}, nil
}

// rsWorkItem is the (clusterName, clusterIndex) pair the RS plan iterates over,
// plus the cluster's syncSourceSelector for per-cluster mongot tag selection.
type rsWorkItem struct {
	ClusterName        string
	ClusterIndex       int
	SyncSourceSelector *searchv1.SyncSourceSelector
}

// buildRSWorkList returns one item per spec.clusters[i] with clusterIndex
// resolved from the persisted state.ClusterMapping. A single-cluster spec has
// one entry with an empty clusterName, which maps to index 0 and routes to the
// central client.
func (r *MongoDBSearchReconcileHelper) buildRSWorkList() []rsWorkItem {
	clusters := r.mdbSearch.Spec.Clusters
	work := make([]rsWorkItem, 0, len(clusters))
	for _, c := range clusters {
		work = append(work, rsWorkItem{
			ClusterName:        c.Name,
			ClusterIndex:       r.state.ClusterMapping[c.Name],
			SyncSourceSelector: c.SyncSourceSelector,
		})
	}
	return work
}

// shardedWorkItem is the (clusterName, clusterIndex, shardName, shardIndex) tuple the sharded plan iterates over.
// Single-cluster uses ClusterName "" and ClusterIndex 0 so naming matches the pre-MC layout.
type shardedWorkItem struct {
	ClusterName        string
	ClusterIndex       int
	ShardName          string
	ShardIndex         int
	SyncSourceSelector *searchv1.SyncSourceSelector
}

// buildShardedWorkList returns one item per (cluster, shard) combination.
// A single-cluster spec produces one cluster entry with ClusterName "" and ClusterIndex 0.
func (r *MongoDBSearchReconcileHelper) buildShardedWorkList(shardNames []string) []shardedWorkItem {
	clusterItems := r.buildRSWorkList()

	work := make([]shardedWorkItem, 0, len(clusterItems)*len(shardNames))
	for _, cl := range clusterItems {
		for shardIdx, shardName := range shardNames {
			work = append(work, shardedWorkItem{
				ClusterName:        cl.ClusterName,
				ClusterIndex:       cl.ClusterIndex,
				ShardName:          shardName,
				ShardIndex:         shardIdx,
				SyncSourceSelector: cl.SyncSourceSelector,
			})
		}
	}
	return work
}

// rsResourceNames returns (sts, headlessSvc, proxySvc, configMap) names for one
// RS work item. Always indexed; single-cluster is index 0.
func (r *MongoDBSearchReconcileHelper) rsResourceNames(w rsWorkItem) (types.NamespacedName, types.NamespacedName, types.NamespacedName, types.NamespacedName) {
	return r.mdbSearch.StatefulSetNamespacedNameForCluster(w.ClusterIndex),
		r.mdbSearch.SearchServiceNamespacedNameForCluster(w.ClusterIndex),
		r.mdbSearch.ProxyServiceNamespacedNameForCluster(w.ClusterIndex),
		r.mdbSearch.MongotConfigConfigMapNameForCluster(w.ClusterIndex)
}

func (r *MongoDBSearchReconcileHelper) buildShardedPlan(shardedSource SearchSourceShardedDeployment) (reconcilePlan, error) {
	shardNames := shardedSource.GetShardNames()
	work := r.buildShardedWorkList(shardNames)

	// HostSeeds is invariant in cluster — resolve once per shard, then reuse
	// across every (cluster, shard) unit. The pre-hoist form called it
	// clusters×shards times.
	hostSeedsByShard := make(map[string][]string, len(shardNames))
	for _, shardName := range shardNames {
		seeds, err := shardedSource.HostSeeds(shardName)
		if err != nil {
			return reconcilePlan{}, err
		}
		hostSeedsByShard[shardName] = seeds
	}

	units := make([]reconcileUnit, 0, len(work))
	// Track one cluster-level resource per unique cluster index.
	seenClusters := map[int]bool{}
	var clusterLevelResources []clusterLevelResource

	for _, w := range work {
		hostSeeds := hostSeedsByShard[w.ShardName]

		sizing, err := r.mdbSearch.ResolveSizingForClusterShard(w.ClusterName, w.ShardName)
		if err != nil {
			return reconcilePlan{}, err
		}

		stsName := r.mdbSearch.MongotStatefulSetForClusterShard(w.ClusterIndex, w.ShardName)

		var logFields []any
		if w.ClusterName != "" {
			logFields = []any{"cluster", w.ClusterName, "shard", w.ShardName, "shardIdx", w.ShardIndex}
		} else {
			logFields = []any{"shard", w.ShardName, "shardIdx", w.ShardIndex}
		}

		mongotConfigFn := mongot.Apply(baseMongotConfig(
			r.mdbSearch, hostSeeds),
			routerMongotMod(r.mdbSearch, shardedSource),
			featureFlagsMongotMod(r.mdbSearch),
			replicationReaderTagSetsMod(w.SyncSourceSelector))
		units = append(units, reconcileUnit{
			stsName:             stsName,
			headlessSvc:         r.mdbSearch.MongotServiceForClusterShard(w.ClusterIndex, w.ShardName),
			proxySvc:            r.mdbSearch.ProxyServiceNameForClusterShard(w.ClusterIndex, w.ShardName),
			configMapName:       r.mdbSearch.MongotConfigMapForClusterShard(w.ClusterIndex, w.ShardName),
			podLabels:           map[string]string{appLabelKey: stsName.Name, shardLabelKey: w.ShardName},
			additionalSvcLabels: map[string]string{shardLabelKey: w.ShardName},
			publishNotReady:     true,
			logFields:           logFields,
			tlsResource:         &perShardTLSResource{MongoDBSearch: r.mdbSearch, clusterIndex: w.ClusterIndex, shardName: w.ShardName},
			mongotConfigFn:      mongotConfigFn,
			clusterName:         w.ClusterName,
			clusterIndex:        w.ClusterIndex,
			shardName:           w.ShardName,
			sizing:              sizing,
		})

		if !seenClusters[w.ClusterIndex] {
			seenClusters[w.ClusterIndex] = true
			clusterLevelResources = append(clusterLevelResources, clusterLevelResource{
				clusterName:      w.ClusterName,
				clusterIndex:     w.ClusterIndex,
				svcName:          r.mdbSearch.ProxyServiceNamespacedNameForCluster(w.ClusterIndex),
				fallbackPodLabel: r.mdbSearch.MongotStatefulSetForClusterShard(w.ClusterIndex, shardNames[0]).Name,
			})
		}
	}

	manageProxySvc := !r.mdbSearch.IsShardedUnmanagedLB()
	plan := reconcilePlan{
		units:          units,
		manageProxySvc: manageProxySvc,
		preflight: func(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
			return r.validatePerShardTLSSecrets(ctx, log, shardNames)
		},
		cleanup: func(ctx context.Context, log *zap.SugaredLogger) {
			if err := r.cleanupStaleShardResources(ctx, log, shardNames); err != nil {
				log.Warnf("Failed to cleanup stale shard resources: %s", err)
			}
			if r.mdbSearch.IsLBModeManaged() {
				if err := r.pruneRoutingReady(ctx, shardNames); err != nil {
					log.Warnf("Failed to prune routing-ready switch entries: %s", err)
				}
			}
		},
	}
	if manageProxySvc {
		plan.clusterLevelResources = clusterLevelResources
	}
	return plan, nil
}

func (r *MongoDBSearchReconcileHelper) Reconcile(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	workflowStatus := r.reconcile(ctx, log)

	if _, err := commoncontroller.UpdateStatus(ctx, r.client, r.mdbSearch, workflowStatus, log); err != nil {
		return workflow.Failed(err)
	}
	return workflowStatus
}

// MessageFromStatus extracts the user-visible message from a workflow.Status.
// workflow.Status does not expose Message() directly; the message is carried
// in StatusOptions() as a MessageOption.
func MessageFromStatus(st workflow.Status) string {
	if st == nil {
		return ""
	}
	if opt, ok := status.GetOption(st.StatusOptions(), status.MessageOption{}); ok {
		return opt.(status.MessageOption).Message
	}
	return ""
}

func (r *MongoDBSearchReconcileHelper) reconcile(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	log = log.With("MongoDBSearch", r.mdbSearch.NamespacedName())
	log.Infof("Reconciling MongoDBSearch")

	if err := r.mdbSearch.ValidateSpec(); err != nil {
		return workflow.Invalid("%s", err.Error())
	}

	if err := r.db.Validate(); err != nil {
		return workflow.Failed(err)
	}

	version := r.getMongotVersion()

	if err := r.ValidateSearchImageVersion(version); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateSingleMongoDBSearchForSearchSource(ctx); err != nil {
		return workflow.Failed(err)
	}

	// This validation lives at reconcile level (not spec validations level) because for internal MongoDB sources, the
	// sharded topology is only known after fetching the referenced MongoDB resource.
	// It's not part of the MongoDBSearch spec itself.
	if err := r.ValidateManagedLBShardedTLS(); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateMultipleReplicasUnmanagedLBTopology(); err != nil {
		return workflow.Failed(err)
	}

	plan, err := r.buildReconcilePlan(log)
	if err != nil {
		return workflow.Failed(err)
	}

	if status := plan.preflight(ctx, log); !status.IsOK() {
		return status
	}

	keyfileStsModification, st, ok := r.ensureKeyfileModification(ctx, log)
	if !ok {
		return st
	}

	// Topology-agnostic modifications (computed once, applied to every unit).
	// Ordering note: egress TLS must be applied after ingress TLS because it toggles mTLS
	// based on the mode set by ingress. x509 must run after baseMongotConfig (which sets
	// username/password) so it can clear them. The loop below preserves this order when
	// passing modifications to ensureMongotConfig and createOrUpdateStatefulSet.
	passwordAuthStsModification := statefulset.NOOP()
	if !r.mdbSearch.IsX509Auth() {
		passwordAuthStsModification = PasswordAuthModification(r.mdbSearch)
	}

	embeddingConfigMongotModification, embeddingConfigStsModification, err := r.ensureEmbeddingConfig(ctx, log)
	if err != nil {
		return workflow.Failed(err)
	}

	usePerPodConfig := r.mdbSearch.HasAutoEmbedding()
	image, imageVersion := r.searchImageAndVersion()
	searchImage := fmt.Sprintf("%s:%s", image, imageVersion)

	mods := reconcileUnitMods{
		passwordAuthSts:       passwordAuthStsModification,
		embeddingConfigMongot: embeddingConfigMongotModification,
		embeddingConfigSts:    embeddingConfigStsModification,
		keyfileSts:            keyfileStsModification,
		searchImage:           searchImage,
		usePerPodConfig:       usePerPodConfig,
	}

	// Apply all units before any readiness check — see TestReconcileShardedMC_AllUnitsAppliedBeforeReadinessCheck.
	type unitApplyResult struct {
		unit               reconcileUnit
		unitClient         kubernetesClient.Client
		expectedGeneration int64
	}
	applied := make([]unitApplyResult, 0, len(plan.units))
	for _, unit := range plan.units {
		unitLog := log.With(unit.logFields...)

		mutatedSts, unitClient, err := r.applyReconcileUnit(ctx, unitLog, plan, unit, mods)
		if err != nil {
			return workflow.Failed(err)
		}
		applied = append(applied, unitApplyResult{
			unit:               unit,
			unitClient:         unitClient,
			expectedGeneration: mutatedSts.GetGeneration(),
		})
	}

	for _, res := range plan.clusterLevelResources {
		// Wait for the managed LB: with the fallback selector this Service would
		// route mongos directly at mongot, bypassing Envoy SNI fan-out.
		if r.mdbSearch.IsLBModeManaged() && !r.mdbSearch.IsLoadBalancerReady() {
			continue
		}
		clusterClient, err := r.clientForCluster(res.clusterName)
		if err != nil {
			return workflow.Failed(err)
		}
		if err := r.ensureSearchService(ctx, log, clusterClient, res.svcName, buildClusterLevelProxyService(r.mdbSearch, res)); err != nil {
			return workflow.Failed(err)
		}
	}

	plan.cleanup(ctx, log)

	// Mark routing-ready shards across ALL units (a one-way switch persisted in the
	// state CM) before the worst-of readiness return: one not-ready or failing unit
	// must not block the others, so per-unit errors are aggregated instead of
	// failing fast.
	var switchErrs error
	for _, res := range applied {
		if err := r.markRoutingReadyIfThresholdMet(ctx, log, res.unit, res.unitClient); err != nil {
			switchErrs = multierror.Append(switchErrs, err)
		}
	}
	if switchErrs != nil {
		return workflow.Failed(switchErrs)
	}

	// Worst-of readiness check — first non-OK status wins.
	for _, res := range applied {
		if statefulSetStatus := statefulset.GetStatefulSetStatus(ctx, r.mdbSearch.Namespace, res.unit.stsName.Name, res.expectedGeneration, res.unitClient); !statefulSetStatus.IsOK() {
			return statefulSetStatus
		}
	}

	if !r.mdbSearch.IsLoadBalancerReady() {
		return workflow.Pending("Waiting for managed load balancer to be ready").
			WithAdditionalOptions(searchv1.NewMongoDBSearchVersionOption(imageVersion))
	}
	return workflow.OK().WithAdditionalOptions(searchv1.NewMongoDBSearchVersionOption(imageVersion))
}

// reconcileUnitMods bundles the topology-agnostic modifications applied to
// every unit. Nil fields are replaced with NOOPs in withDefaults.
type reconcileUnitMods struct {
	passwordAuthSts       statefulset.Modification
	embeddingConfigMongot mongot.Modification
	embeddingConfigSts    statefulset.Modification
	keyfileSts            statefulset.Modification
	searchImage           string
	usePerPodConfig       bool
}

// withDefaults returns a copy with nil modifications replaced by NOOPs.
func (m reconcileUnitMods) withDefaults() reconcileUnitMods {
	if m.passwordAuthSts == nil {
		m.passwordAuthSts = statefulset.NOOP()
	}
	if m.embeddingConfigMongot == nil {
		m.embeddingConfigMongot = mongot.NOOP()
	}
	if m.embeddingConfigSts == nil {
		m.embeddingConfigSts = statefulset.NOOP()
	}
	if m.keyfileSts == nil {
		m.keyfileSts = statefulset.NOOP()
	}
	return m
}

// applyReconcileUnit reconciles all per-unit resources against the client
// resolved from unit.clusterName (central client when clusterName == "").
// Returns the mutated StatefulSet and resolved client for the readiness check.
func (r *MongoDBSearchReconcileHelper) applyReconcileUnit(
	ctx context.Context,
	log *zap.SugaredLogger,
	plan reconcilePlan,
	unit reconcileUnit,
	mods reconcileUnitMods,
) (*appsv1.StatefulSet, kubernetesClient.Client, error) {
	mods = mods.withDefaults()

	unitClient, err := r.clientForCluster(unit.clusterName)
	if err != nil {
		return nil, nil, err
	}

	if err := r.ensureSearchService(ctx, log, unitClient, unit.headlessSvc, buildHeadlessService(r.mdbSearch, unit)); err != nil {
		return nil, nil, err
	}

	if plan.manageProxySvc {
		if err := r.ensureSearchService(ctx, log, unitClient, unit.proxySvc, buildProxyService(r.mdbSearch, unit)); err != nil {
			return nil, nil, err
		}
	}

	// Per-unit ingress TLS: each shard may have its own secret, so this cannot
	// be hoisted out of the loop.
	ingressTlsMongotModification, ingressTlsStsModification, err := r.ensureIngressTlsConfig(ctx, unitClient, unit.tlsResource)
	if err != nil {
		return nil, nil, err
	}

	// Per-unit egress TLS (SCRAM CA + optional client cert): uses unitClient so the
	// operator-managed secret is created on the cluster where mongot pods run.
	egressTlsMongotModification, egressTlsStsModification, err := r.ensureEgressTlsConfig(ctx, unitClient)
	if err != nil {
		return nil, nil, err
	}

	// Per-unit x509 client cert: uses unitClient for the same reason as egress TLS.
	x509MongotModification, x509StsModification, err := r.ensureX509ClientCertConfig(ctx, unitClient)
	if err != nil {
		return nil, nil, err
	}

	configHash, err := r.ensureMongotConfig(ctx,
		log,
		unitClient,
		unit.configMapName,
		unit.stsName.Name,
		unit.clusterName,
		unit.sizing.ReplicasOrDefault(),
		unit.mongotConfigFn,
		ingressTlsMongotModification,
		egressTlsMongotModification,
		x509MongotModification,
		mods.embeddingConfigMongot,
	)
	if err != nil {
		return nil, nil, err
	}

	configHashModification := statefulset.WithPodSpecTemplate(podtemplatespec.WithAnnotations(
		map[string]string{
			"mongotConfigHash": configHash,
		},
	))

	stsFunc := CreateSearchStatefulSetFunc(r.mdbSearch, unit.sizing, unit.stsName.Name, r.mdbSearch.Namespace, unit.headlessSvc.Name, unit.configMapName.Name, unit.podLabels, mods.searchImage, mods.usePerPodConfig)
	stsOverride := StatefulSetOverrideModification(unit.sizing.StatefulSetConfiguration)
	mutatedSts, err := r.createOrUpdateStatefulSet(ctx,
		log,
		unitClient,
		unit.stsName,
		stsFunc,
		withSearchOwnerLabels(r.mdbSearch, unit.clusterName),
		mods.passwordAuthSts,
		configHashModification,
		mods.keyfileSts,
		ingressTlsStsModification,
		egressTlsStsModification,
		x509StsModification,
		mods.embeddingConfigSts,
		stsOverride, // must be last: see StatefulSetOverrideModification
	)
	if err != nil {
		return nil, nil, err
	}

	return mutatedSts, unitClient, nil
}

// markRoutingReadyIfThresholdMet flips a shard's routing-ready switch once its
// mongot STS first meets the routing-readiness threshold. The switch is one-way:
// a later STS delete/recreate does not put the shard back into fallback routing.
func (r *MongoDBSearchReconcileHelper) markRoutingReadyIfThresholdMet(ctx context.Context, log *zap.SugaredLogger, unit reconcileUnit, unitClient kubernetesClient.Client) error {
	if unit.shardName == "" || !r.mdbSearch.IsLBModeManaged() || slices.Contains(r.state.RoutingReadyMongotGroups, unit.shardName) {
		return nil
	}

	sts, err := unitClient.GetStatefulSet(ctx, unit.stsName)
	if err != nil {
		// Just-created STS not yet in the informer cache: not ready to flip the
		// switch this pass, but not a failure — re-evaluated every reconcile.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	minReady := r.mdbSearch.GetMinMongotReadyReplicasForRouting()
	if sts.Status.ReadyReplicas < minReady {
		return nil
	}
	if err := r.markRoutingReady(ctx, unit.shardName); err != nil {
		return err
	}
	log.Infof("Marked shard %q routing-ready: %d of %d replicas ready, min required %d",
		unit.shardName, sts.Status.ReadyReplicas, ptr.Deref(sts.Spec.Replicas, 1), minReady)
	return nil
}

// markRoutingReady appends the shard to the routing-ready switch in the state
// ConfigMap. Only the switch slice is refreshed from the write's result — the
// ClusterMapping snapshot loaded at reconcile start stays authoritative for
// resource naming within this reconcile.
func (r *MongoDBSearchReconcileHelper) markRoutingReady(ctx context.Context, shardName string) error {
	state, err := MutateSearchState(ctx, r.client, r.mdbSearch, func(s *SearchDeploymentState) bool {
		if slices.Contains(s.RoutingReadyMongotGroups, shardName) {
			return false
		}
		s.RoutingReadyMongotGroups = append(s.RoutingReadyMongotGroups, shardName)
		return true
	})
	if err != nil {
		return err
	}
	r.state.RoutingReadyMongotGroups = state.RoutingReadyMongotGroups
	return nil
}

// pruneRoutingReady drops switch entries for shards that no longer exist; live
// shards are never removed (the switch is one-way).
func (r *MongoDBSearchReconcileHelper) pruneRoutingReady(ctx context.Context, liveShardNames []string) error {
	state, err := MutateSearchState(ctx, r.client, r.mdbSearch, func(s *SearchDeploymentState) bool {
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
	r.state.RoutingReadyMongotGroups = state.RoutingReadyMongotGroups
	return nil
}

// isOwnedBy returns true if the object has an owner reference pointing to the given owner.
// Unlike metav1.IsControlledBy, this does not require the controller: true field,
// which is not set by controllerutil.SetOwnerReference.
func isOwnedBy(obj client.Object, owner client.Object) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}

// cleanupStaleShardResources deletes per-shard proxy Services for shards that no
// longer exist. In MC the proxy Services live on the member clusters, so we
// iterate the central client plus every member-cluster client and only touch
// Services we own (by UID on central, by search-owner label on members —
// cross-cluster owner refs don't exist).
//
// STSs / ConfigMaps / headless Services for stale shards are not handled here:
// in MC their owners live cross-cluster and aren't GC'd by Kubernetes.
func (r *MongoDBSearchReconcileHelper) cleanupStaleShardResources(ctx context.Context, log *zap.SugaredLogger, currentShardNames []string) error {
	if r.mdbSearch.IsShardedUnmanagedLB() {
		return nil
	}

	expectedNames := make(map[string]bool)
	seenClusters := map[int]bool{}
	for _, w := range r.buildShardedWorkList(currentShardNames) {
		expectedNames[r.mdbSearch.ProxyServiceNameForClusterShard(w.ClusterIndex, w.ShardName).Name] = true
		if !seenClusters[w.ClusterIndex] {
			seenClusters[w.ClusterIndex] = true
			expectedNames[r.mdbSearch.ProxyServiceNamespacedNameForCluster(w.ClusterIndex).Name] = true
		}
	}

	clients := map[string]kubernetesClient.Client{"": r.client}
	for name, c := range r.memberClusterClients {
		clients[name] = c
	}

	for clusterName, c := range clients {
		serviceList := &corev1.ServiceList{}
		if err := c.List(ctx, serviceList,
			client.InNamespace(r.mdbSearch.Namespace),
			client.MatchingLabels{"component": proxyServiceComponent},
		); err != nil {
			return xerrors.Errorf("failed to list proxy services on cluster %q: %w", clusterName, err)
		}

		for i := range serviceList.Items {
			svc := &serviceList.Items[i]
			if expectedNames[svc.Name] {
				continue
			}
			// Owner refs don't cross clusters, so for member-cluster Services the
			// owner check is by search-owner label instead of UID. Same intent:
			// only touch Services we created.
			if clusterName == "" {
				if !isOwnedBy(svc, r.mdbSearch) {
					continue
				}
			} else {
				if svc.Labels[khandler.MongoDBSearchOwnerNameLabel] != r.mdbSearch.Name ||
					svc.Labels[khandler.MongoDBSearchOwnerNamespaceLabel] != r.mdbSearch.Namespace {
					continue
				}
			}
			log.Infof("Deleting stale proxy Service %s on cluster %q", svc.Name, clusterName)
			if err := c.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
				return xerrors.Errorf("failed to delete stale proxy Service %s on cluster %q: %w", svc.Name, clusterName, err)
			}
		}
	}
	return nil
}

// ensureKeyfileModification returns the keyfile StatefulSet modification if wireproto is enabled.
// Returns (NOOP, nil, true) if wireproto is disabled.
// Returns (nil, status, false) if the keyfile is not ready and reconciliation should stop.
func (r *MongoDBSearchReconcileHelper) ensureKeyfileModification(ctx context.Context, log *zap.SugaredLogger) (statefulset.Modification, workflow.Status, bool) {
	if !r.mdbSearch.IsWireprotoEnabled() {
		return statefulset.NOOP(), nil, true
	}
	mod, err := r.ensureSourceKeyfile(ctx, log)
	if apierrors.IsNotFound(err) {
		return nil, workflow.Pending("Waiting for keyfile secret to be created"), false
	} else if err != nil {
		return nil, workflow.Failed(err), false
	}
	return mod, nil, true
}

// ensureSourceKeyfile is called only if the wireproto server is enabled, to set up the keyfile necessary for authentication.
func (r *MongoDBSearchReconcileHelper) ensureSourceKeyfile(ctx context.Context, log *zap.SugaredLogger) (statefulset.Modification, error) {
	keyfileSecretName := kube.ObjectKey(r.mdbSearch.GetNamespace(), r.db.KeyfileSecretName())
	keyfileSecret := &corev1.Secret{}
	if err := r.client.Get(ctx, keyfileSecretName, keyfileSecret); err != nil {
		return nil, err
	}

	return statefulset.Apply(
		// make sure mongot pods get restarted if the keyfile changes
		statefulset.WithPodSpecTemplate(podtemplatespec.WithAnnotations(
			map[string]string{
				"keyfileHash": hashBytes(keyfileSecret.Data[MongotKeyfileFilename]),
			},
		)),
		CreateKeyfileModificationFunc(r.db.KeyfileSecretName()),
	), nil
}

// validatePerShardTLSSecrets validates that all per-(cluster, shard) TLS source secrets exist.
// Returns workflow.OK() if TLS is not configured, in shared mode, or all secrets exist.
// Returns workflow.Pending if any secret is missing (expected to be created).
// Returns workflow.Failed on other errors.
func (r *MongoDBSearchReconcileHelper) validatePerShardTLSSecrets(ctx context.Context, log *zap.SugaredLogger, shardNames []string) workflow.Status {
	if r.mdbSearch.Spec.Security.TLS == nil {
		return workflow.OK()
	}

	if r.mdbSearch.CertificateKeySecretName() {
		return workflow.Failed(xerrors.New("spec.security.tls.certificateKeySecretRef is not supported for sharded clusters, use spec.security.tls.certsSecretPrefix instead"))
	}

	for _, w := range r.buildShardedWorkList(shardNames) {
		// A -1 sentinel index means the cluster isn't yet in the state mapping.
		// Computing a secret name from it would point at the wrong file.
		if w.ClusterName != "" && w.ClusterIndex < 0 {
			return workflow.Pending("Waiting for cluster %q to be registered in search state", w.ClusterName)
		}
		clusterClient, err := r.clientForCluster(w.ClusterName)
		if err != nil {
			return workflow.Failed(xerrors.Errorf("no client for cluster %q: %w", w.ClusterName, err))
		}
		secretNsName := r.mdbSearch.TLSSecretForClusterShard(w.ClusterIndex, w.ShardName)
		tlsSecret := &corev1.Secret{}
		err = clusterClient.Get(ctx, secretNsName, tlsSecret)
		if apierrors.IsNotFound(err) {
			log.Infof("Waiting for per-shard TLS secret %s to be created", secretNsName)
			return workflow.Pending("Waiting for TLS secret %s for shard %s to be created", secretNsName.Name, w.ShardName)
		} else if err != nil {
			return workflow.Failed(xerrors.Errorf("failed to get TLS secret %s for shard %s: %w", secretNsName.Name, w.ShardName, err))
		}
	}

	return workflow.OK()
}

func (r *MongoDBSearchReconcileHelper) searchImageAndVersion() (string, string) {
	imageVersion := r.mdbSearch.Spec.Version
	if imageVersion == "" {
		imageVersion = r.operatorSearchConfig.SearchVersion
	}
	return fmt.Sprintf("%s/%s", r.operatorSearchConfig.SearchRepo, r.operatorSearchConfig.SearchName), imageVersion
}

func (r *MongoDBSearchReconcileHelper) createOrUpdateStatefulSet(ctx context.Context, log *zap.SugaredLogger, kubeClient kubernetesClient.Client, stsName types.NamespacedName, modifications ...statefulset.Modification) (*appsv1.StatefulSet, error) {
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName.Name, Namespace: stsName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, kubeClient, sts, func() error {
		statefulset.Apply(modifications...)(sts)
		return controllerutil.SetOwnerReference(r.mdbSearch, sts, kubeClient.Scheme())
	})
	if err != nil {
		return nil, xerrors.Errorf("error creating/updating search statefulset %v: %w", stsName, err)
	}

	log.Debugf("Search statefulset %s CreateOrUpdate result: %s", stsName, op)

	return sts, nil
}

func (r *MongoDBSearchReconcileHelper) ensureSearchService(ctx context.Context, log *zap.SugaredLogger, kubeClient kubernetesClient.Client, svcName types.NamespacedName, desired corev1.Service) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName.Name, Namespace: svcName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, kubeClient, svc, func() error {
		resourceVersion := svc.ResourceVersion
		existingClusterIP := svc.Spec.ClusterIP
		*svc = desired
		svc.ResourceVersion = resourceVersion
		// Preserve the assigned ClusterIP for non-headless Services.
		// Kubernetes assigns a ClusterIP on creation and it is immutable.
		// For headless Services (desired.Spec.ClusterIP == "None"), this is a no-op.
		if desired.Spec.ClusterIP == "" && existingClusterIP != "" {
			svc.Spec.ClusterIP = existingClusterIP
		}
		return controllerutil.SetOwnerReference(r.mdbSearch, svc, kubeClient.Scheme())
	})
	if err != nil {
		return xerrors.Errorf("error creating/updating search service %v: %w", svcName, err)
	}

	log.Debugf("Updated search service %v: %s", svcName, op)

	return nil
}

// ensureMongotConfig creates or updates the mongot ConfigMap. When
// auto-embedding is configured, generates leader/follower config files plus
// pod-name role keys.
func (r *MongoDBSearchReconcileHelper) ensureMongotConfig(ctx context.Context, log *zap.SugaredLogger, kubeClient kubernetesClient.Client, cmName types.NamespacedName, stsName, clusterName string, replicas int, modifications ...mongot.Modification) (string, error) {
	usePerPodConfig := r.mdbSearch.HasAutoEmbedding()

	mongotConfig := mongot.Config{}
	mongot.Apply(modifications...)(&mongotConfig)

	configEntries, keysToRemove, err := buildMongotConfigMapEntries(mongotConfig, usePerPodConfig, stsName, replicas)
	if err != nil {
		return "", err
	}

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName.Name, Namespace: cmName.Namespace}, Data: map[string]string{}}
	op, err := controllerutil.CreateOrUpdate(ctx, kubeClient, cm, func() error {
		resourceVersion := cm.ResourceVersion
		for key, data := range configEntries {
			cm.Data[key] = string(data)
		}
		// Remove stale keys left over from config mode transitions (single↔per-pod).
		// When switching from per-pod to single config, the leader/follower files and
		// pod-name role keys are no longer needed (and vice versa for config.yml).
		for _, key := range keysToRemove {
			delete(cm.Data, key)
		}
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		for k, v := range searchOwnerLabels(r.mdbSearch, clusterName) {
			cm.Labels[k] = v
		}
		cm.ResourceVersion = resourceVersion
		return controllerutil.SetOwnerReference(r.mdbSearch, cm, kubeClient.Scheme())
	})
	if err != nil {
		return "", err
	}

	configHash := computeConfigHash(configEntries)
	log.Debugf("Updated mongot config ConfigMap %v (%s) with keys: %v", cmName, op, configEntryKeys(configEntries))

	return configHash, nil
}

// buildMongotConfigMapEntries builds the ConfigMap data entries for mongot.
//
// Single-config mode (no auto-embedding):
//
//	config.yml: <mongot config>
//
// Per-pod config mode (auto-embedding enabled):
//
//	config-leader.yml:   <mongot config with IsAutoEmbeddingViewWriter: true>
//	config-follower.yml: <mongot config with IsAutoEmbeddingViewWriter: false>
//	{stsName}-0:         "leader"
//	{stsName}-1:         "follower"
//	{stsName}-N:         "follower"
func buildMongotConfigMapEntries(config mongot.Config, usePerPodConfig bool, stsName string, replicas int) (map[string][]byte, []string, error) {
	if usePerPodConfig {
		return buildPerPodConfigEntries(config, stsName, replicas)
	}
	return buildSingleConfigEntry(config, stsName, replicas)
}

// buildPerPodConfigEntries creates leader (pod-0) and follower configs with pod-name role keys.
func buildPerPodConfigEntries(config mongot.Config, stsName string, replicas int) (map[string][]byte, []string, error) {
	leaderData, err := yaml.Marshal(config)
	if err != nil {
		return nil, nil, err
	}

	followerConfig := config
	if config.Embedding != nil {
		embeddingCopy := *config.Embedding
		embeddingCopy.IsAutoEmbeddingViewWriter = ptr.To(false)
		followerConfig.Embedding = &embeddingCopy
	}
	followerData, err := yaml.Marshal(followerConfig)
	if err != nil {
		return nil, nil, err
	}

	entries := map[string][]byte{
		MongotConfigLeaderFilename:   leaderData,
		MongotConfigFollowerFilename: followerData,
	}

	for i := 0; i < replicas; i++ {
		podName := fmt.Sprintf("%s-%d", stsName, i)
		if i == 0 {
			entries[podName] = []byte("leader")
		} else {
			entries[podName] = []byte("follower")
		}
	}

	keysToRemove := []string{MongotConfigFilename}
	return entries, keysToRemove, nil
}

func buildSingleConfigEntry(config mongot.Config, stsName string, replicas int) (map[string][]byte, []string, error) {
	data, err := yaml.Marshal(config)
	if err != nil {
		return nil, nil, err
	}

	entries := map[string][]byte{MongotConfigFilename: data}
	keysToRemove := []string{MongotConfigLeaderFilename, MongotConfigFollowerFilename}
	for i := 0; i < replicas; i++ {
		keysToRemove = append(keysToRemove, fmt.Sprintf("%s-%d", stsName, i))
	}
	return entries, keysToRemove, nil
}

// computeConfigHash hashes config file contents only; pod-name keys are excluded
// since scaling changes don't require existing pods to restart.
func computeConfigHash(entries map[string][]byte) string {
	var allData []byte
	for _, key := range []string{MongotConfigFilename, MongotConfigLeaderFilename, MongotConfigFollowerFilename} {
		if data, ok := entries[key]; ok {
			allData = append(allData, data...)
		}
	}
	return hashBytes(allData)
}

func configEntryKeys(entries map[string][]byte) []string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	return keys
}

// mongotServicePorts returns the common service ports (grpc, prometheus, healthcheck) for any mongot deployment.
func mongotServicePorts(search *searchv1.MongoDBSearch) []corev1.ServicePort {
	ports := []corev1.ServicePort{
		{
			Name:       "mongot-grpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       search.GetMongotGrpcPort(),
			TargetPort: intstr.FromInt32(search.GetMongotGrpcPort()),
		},
	}

	if prometheus := search.GetPrometheus(); prometheus != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "prometheus",
			Protocol:   corev1.ProtocolTCP,
			Port:       prometheus.GetPort(),
			TargetPort: intstr.FromInt32(prometheus.GetPort()),
		})
	}

	ports = append(ports, corev1.ServicePort{
		Name:       "healthcheck",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotHealthCheckPort(),
		TargetPort: intstr.FromInt32(search.GetMongotHealthCheckPort()),
	})

	return ports
}

const (
	appLabelKey           = "app"
	shardLabelKey         = "shard"
	proxyServiceComponent = "search-proxy"

	nameLabelKey       = "app.kubernetes.io/name"
	managedByLabelKey  = "app.kubernetes.io/managed-by"
	voyageAILabelValue = "voyageai"
	operatorLabelValue = "mongodb-kubernetes-operator"
)

// buildHeadlessService builds a headless Service for a reconcile unit. All topology-specific
// behavior comes from the unit's explicit fields — no branching on "is this a shard?".
func buildHeadlessService(search *searchv1.MongoDBSearch, unit reconcileUnit) corev1.Service {
	svcLabels := map[string]string{appLabelKey: unit.headlessSvc.Name}
	for k, v := range unit.additionalSvcLabels {
		svcLabels[k] = v
	}
	for k, v := range searchOwnerLabels(search, unit.clusterName) {
		svcLabels[k] = v
	}

	serviceBuilder := service.Builder().
		SetName(unit.headlessSvc.Name).
		SetNamespace(unit.headlessSvc.Namespace).
		SetLabels(svcLabels).
		SetSelector(map[string]string{appLabelKey: unit.podLabels[appLabelKey]}).
		SetClusterIP("None").
		SetServiceType(corev1.ServiceTypeClusterIP).
		SetOwnerReferences(search.GetOwnerReferences())

	for i := range unit.extraHeadlessPorts {
		serviceBuilder.AddPort(&unit.extraHeadlessPorts[i])
	}

	for _, port := range mongotServicePorts(search) {
		serviceBuilder.AddPort(&port)
	}

	return serviceBuilder.Build()
}

// buildProxyService builds the stable proxy Service for a reconcile unit (RS or per-shard).
// The selector flips to Envoy only after the LB substatus reports Ready, so
// traffic keeps flowing to mongot directly while Envoy is deploying. Selector
// uses LoadBalancerDeploymentNameForCluster(unit.clusterIndex) to match
// per-cluster Envoy pod labels.
func buildProxyService(search *searchv1.MongoDBSearch, unit reconcileUnit) corev1.Service {
	var selector map[string]string
	if search.IsLBModeManaged() && search.IsLoadBalancerReady() {
		selector = map[string]string{appLabelKey: search.LoadBalancerDeploymentNameForCluster(unit.clusterIndex)}
	} else {
		selector = map[string]string{appLabelKey: unit.podLabels[appLabelKey]}
	}

	labels := map[string]string{
		appLabelKey: unit.proxySvc.Name,
		"component": proxyServiceComponent,
	}
	for k, v := range unit.additionalSvcLabels {
		labels[k] = v
	}
	for k, v := range searchOwnerLabels(search, unit.clusterName) {
		labels[k] = v
	}

	targetPort := search.GetEffectiveMongotPort()

	serviceBuilder := service.Builder().
		SetName(unit.proxySvc.Name).
		SetNamespace(unit.proxySvc.Namespace).
		SetLabels(labels).
		SetSelector(selector).
		SetServiceType(corev1.ServiceTypeClusterIP).
		SetOwnerReferences(search.GetOwnerReferences())

	serviceBuilder.AddPort(&corev1.ServicePort{
		// Named "mongot-grpc" (not "grpc") so Istio classifies the port as opaque
		// TCP rather than HTTP/2: mongod speaks application-level (m)TLS gRPC to
		// Envoy here, and a "grpc"-prefixed name makes the sidecar route it through
		// its HTTP/2 path, breaking the TLS handshake. Matches mongotServicePorts.
		Name:       "mongot-grpc",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetEffectiveMongotPort(),
		TargetPort: intstr.FromInt32(targetPort),
	})

	return serviceBuilder.Build()
}

// buildClusterLevelProxyService builds the shard-agnostic proxy Service used by mongos.
// Selector: Envoy pool when managed LB is ready, else the first shard's mongot pool.
// Callers must skip when managed LB is configured but not yet ready.
func buildClusterLevelProxyService(search *searchv1.MongoDBSearch, res clusterLevelResource) corev1.Service {
	var selector map[string]string
	if search.IsLBModeManaged() && search.IsLoadBalancerReady() {
		selector = map[string]string{appLabelKey: search.LoadBalancerDeploymentNameForCluster(res.clusterIndex)}
	} else {
		selector = map[string]string{appLabelKey: res.fallbackPodLabel}
	}

	labels := map[string]string{
		appLabelKey: res.svcName.Name,
		"component": proxyServiceComponent,
	}
	for k, v := range searchOwnerLabels(search, res.clusterName) {
		labels[k] = v
	}

	targetPort := search.GetEffectiveMongotPort()

	svcBuilder := service.Builder().
		SetName(res.svcName.Name).
		SetNamespace(res.svcName.Namespace).
		SetLabels(labels).
		SetSelector(selector).
		SetServiceType(corev1.ServiceTypeClusterIP).
		SetOwnerReferences(search.GetOwnerReferences())

	svcBuilder.AddPort(&corev1.ServicePort{
		// "mongot-grpc" keeps Istio from L7-classifying this port (see buildProxyService).
		Name:       "mongot-grpc",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetEffectiveMongotPort(),
		TargetPort: intstr.FromInt32(targetPort),
	})

	return svcBuilder.Build()
}

// EnsureEmbeddingAPIKeySecret makes sure that the scret that is provided in MDBSearch resource
// for embedding model's keys is present and has expected keys.
func ensureEmbeddingAPIKeySecret(ctx context.Context, client secret.Getter, secretObj client.ObjectKey) (string, error) {
	data, err := secret.ReadByteData(ctx, client, secretObj)
	if err != nil {
		return "", err
	}

	if _, ok := data[indexingKeyName]; !ok {
		return "", fmt.Errorf(`required key "%s" is not present in the Secret %s/%s`, indexingKeyName, secretObj.Namespace, secretObj.Name)
	}
	if _, ok := data[queryKeyName]; !ok {
		return "", fmt.Errorf(`required key "%s" is not present in the Secret %s/%s`, queryKeyName, secretObj.Namespace, secretObj.Name)
	}

	d, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	return hashBytes(d), nil
}

func validateSearchVesionForEmbedding(version string, log *zap.SugaredLogger) error {
	searchVersion, err := semver.NewVersion(version)
	if err != nil {
		log.Debugf("Failed getting semver of search image version. Version %s doesn't seem to be valid semver.", version)
		return nil
	}
	minAllowedVersion, _ := semver.NewVersion(minSearchImageVersionForEmbedding)

	if a := searchVersion.Compare(minAllowedVersion); a == -1 {
		return xerrors.Errorf("The MongoDB search version %s doesn't support auto embeddings. Please use version %s or newer.", version, minSearchImageVersionForEmbedding)
	}
	return nil
}

// ensureEmbeddingConfig returns the mongot config and stateful set modification function based on the values provided in the search CR, it
// also returns the hash of the secret that has the embedding API keys so that if the keys are changed the search pod is automatically restarted.
func (r *MongoDBSearchReconcileHelper) ensureEmbeddingConfig(ctx context.Context, log *zap.SugaredLogger) (mongot.Modification, statefulset.Modification, error) {
	ae := r.mdbSearch.Spec.AutoEmbedding
	if ae == nil {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	// The API key secret is optional only when the provider endpoint refers to an
	// in-cluster VoyageAI service (ai.mongodb.com), which authenticates at the
	// network layer rather than via API keys. For any external provider it is required.
	// The endpoint detection (a Service lookup) only matters when no secret is given,
	// so it is skipped when one is — keeping the secret path free of Service lookups.
	hasAPIKeySecret := ae.EmbeddingModelAPIKeySecret.Name != ""

	apiKeySecretHash := ""
	if hasAPIKeySecret {
		var err error
		apiKeySecretHash, err = ensureEmbeddingAPIKeySecret(ctx, r.client, client.ObjectKey{
			Name:      ae.EmbeddingModelAPIKeySecret.Name,
			Namespace: r.mdbSearch.Namespace,
		})
		if err != nil {
			return nil, nil, err
		}
	} else {
		internal, err := r.isInternalVoyageAIEndpoint(ctx, ae.ProviderEndpoint)
		if err != nil {
			return nil, nil, err
		}
		if !internal {
			return nil, nil, xerrors.Errorf("spec.autoEmbedding.embeddingModelAPIKeySecret is required unless spec.autoEmbedding.providerEndpoint refers to an in-cluster VoyageAI service")
		}
	}

	_, version := r.searchImageAndVersion()
	if err := validateSearchVesionForEmbedding(version, log); err != nil {
		return nil, nil, err
	}

	autoEmbeddingViewWriterTrue := true
	mongotModification := func(config *mongot.Config) {
		config.Embedding = &mongot.EmbeddingConfig{
			// mongot mandates the key files at bootstrap even when the provider ignores
			// them, so the paths are always set. For the internal VoyageAI case the
			// operator writes placeholder files (see below).
			IndexingKeyFile: fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName),
			QueryKeyFile:    fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName),
		}

		// Since MCK right now installs search with one replica only it's safe to alway set IsAutoEmbeddingViewWriter to true.
		// Once we start supporting multiple mongot instances, we need to figure this out and then set here.
		config.Embedding.IsAutoEmbeddingViewWriter = &autoEmbeddingViewWriterTrue

		if ae.ProviderEndpoint != "" {
			config.Embedding.ProviderEndpoint = ae.ProviderEndpoint
		}
	}

	emptyDirVolume := statefulset.CreateVolumeFromEmptyDir(apiKeysTempVolumeName)
	emptyDirVolumeMount := statefulset.CreateVolumeMount(apiKeysTempVolumeName, embeddingKeyFilePath)

	if !hasAPIKeySecret {
		// Internal VoyageAI endpoint: no user secret. The in-cluster VoyageAI server
		// authenticates at the network layer and ignores API keys, but mongot still
		// requires the key files to exist. Write placeholder files into the emptyDir.
		stsModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
			podtemplatespec.WithVolume(emptyDirVolume),
			podtemplatespec.WithVolumeMounts(MongotContainerName, emptyDirVolumeMount),
			podtemplatespec.WithContainer(MongotContainerName, setupMongotContainerArgsForFakeAPIKeys()),
		))
		return mongotModification, stsModification, nil
	}

	readOnlyByOwnerPermission := int32(400)
	apiKeyVolume := statefulset.CreateVolumeFromSecret(embeddingKeyVolumeName, ae.EmbeddingModelAPIKeySecret.Name, statefulset.WithSecretDefaultMode(&readOnlyByOwnerPermission))
	apiKeyVolumeMount := statefulset.CreateVolumeMount(embeddingKeyVolumeName, apiKeysTempVolumeMount, statefulset.WithReadOnly(true))

	stsModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		podtemplatespec.WithVolume(apiKeyVolume),
		podtemplatespec.WithVolumeMounts(MongotContainerName, apiKeyVolumeMount),
		podtemplatespec.WithVolume(emptyDirVolume),
		podtemplatespec.WithVolumeMounts(MongotContainerName, emptyDirVolumeMount),
		podtemplatespec.WithContainer(MongotContainerName, setupMongotContainerArgsForAPIKeys()),
		podtemplatespec.WithAnnotations(map[string]string{
			autoEmbeddingDetailsAnnKey: apiKeySecretHash,
		}),
	))
	return mongotModification, stsModification, nil
}

// isInternalVoyageAIEndpoint reports whether the given provider endpoint URL points
// at an in-cluster VoyageAI Service (ai.mongodb.com) managed by this operator. The
// host is parsed into a Service name/namespace (<svc>[.<ns>[.svc...]]) and the
// backing Service is checked for the VoyageAI operator labels. A non-cluster host,
// a missing Service, or a Service without those labels is treated as external.
func (r *MongoDBSearchReconcileHelper) isInternalVoyageAIEndpoint(ctx context.Context, endpoint string) (bool, error) {
	if endpoint == "" {
		return false, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return false, nil
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return false, nil
	}

	// The host is assumed to be cluster-internal Service DNS of the form
	// <svc>[.<ns>[.svc[.cluster.local]]]. A bare name resolves to this resource's
	// namespace. External hostnames (e.g. api.voyageai.com) fall through to the
	// Service lookup below and are rejected as not-found / not VoyageAI-labelled.
	parts := strings.Split(host, ".")
	svcName := parts[0]
	svcNamespace := r.mdbSearch.Namespace
	if len(parts) >= 2 {
		svcNamespace = parts[1]
	}

	svc := &corev1.Service{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: svcName, Namespace: svcNamespace}, svc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, xerrors.Errorf("failed to resolve embedding provider Service %s/%s: %w", svcNamespace, svcName, err)
	}

	return svc.Labels[nameLabelKey] == voyageAILabelValue &&
		svc.Labels[managedByLabelKey] == operatorLabelValue, nil
}

// setupMongotContainerArgsForFakeAPIKeys writes placeholder embedding key files
// for the internal-VoyageAI case. The in-cluster VoyageAI server ignores API keys
// (it authenticates at the network layer), but mongot fails to bootstrap unless the
// key files exist, so the operator fabricates them with 0400 permissions.
func setupMongotContainerArgsForFakeAPIKeys() container.Modification {
	return prependCommand(fakeAPIKeysCommand(embeddingKeyFilePath))
}

func fakeAPIKeysCommand(destFilePath string) string {
	return fmt.Sprintf(`
printf 'internal' > %[1]s/%[2]s
printf 'internal' > %[1]s/%[3]s
chmod 0400 %[1]s/%[2]s %[1]s/%[3]s
`, destFilePath, queryKeyName, indexingKeyName)
}

func setupMongotContainerArgsForAPIKeys() container.Modification {
	// Since API keys are expected to have 0400 permission, add the arg into the search container to make
	// sure we copy the api keys from temp location (apiKeysTempVolumeMount) to correct location (embeddingKeyFilePath)
	// with correct permissions.
	// Directly setting the permission in the volume doesn't work because volumes are mounted as symlinks and they would have diff permissions,
	// using subpath kind of resolves the probelm but because of fsGroup that we set K8s makes sure that the file is group readable,
	// and that's why the file permissions still don't become 0400 (it's -r--r-----). That's why copying is necessary.
	return prependCommand(sensitiveFilePermissionsForAPIKeys(apiKeysTempVolumeMount, embeddingKeyFilePath, "0400"))
}

// ensureIngressTlsConfig processes TLS configuration for any mongot deployment.
// For non-sharded deployments, pass r.mdbSearch as the tlsResource.
// For sharded deployments, pass a perShardTLSResource adapter.
func (r *MongoDBSearchReconcileHelper) ensureIngressTlsConfig(ctx context.Context, kubeClient kubernetesClient.Client, tlsResource tls.TLSConfigurableResource) (mongot.Modification, statefulset.Modification, error) {
	if r.mdbSearch.Spec.Security.TLS == nil {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	certFileName, err := tls.EnsureTLSSecret(ctx, kubeClient, tlsResource)
	if err != nil {
		return nil, nil, err
	}

	// Check if the user-provided TLS secret contains an optional key password
	hasKeyPassword := false
	userProvidedTLSSecret := tlsResource.TLSSecretNamespacedName()
	_, keyPasswordErr := secret.ReadKey(ctx, kubeClient, GrpcKeyPasswordSecretKey, userProvidedTLSSecret)
	if keyPasswordErr == nil {
		hasKeyPassword = true
	}

	mongotModification := func(config *mongot.Config) {
		certPath := tls.OperatorSecretMountPath + certFileName
		config.Server.Grpc.TLS.Mode = mongot.ConfigTLSModeTLS
		config.Server.Grpc.TLS.CertificateKeyFile = ptr.To(certPath)
		if hasKeyPassword {
			config.Server.Grpc.TLS.CertificateKeyFilePasswordFile = ptr.To(TempGrpcKeyPasswordPath)
		}
		if config.Server.Wireproto != nil {
			config.Server.Wireproto.TLS.Mode = mongot.ConfigTLSModeTLS
			config.Server.Wireproto.TLS.CertificateKeyFile = ptr.To(certPath)
		}
	}

	tlsSecret := tlsResource.TLSOperatorSecretNamespacedName()
	tlsVolume := statefulset.CreateVolumeFromSecret("tls", tlsSecret.Name)
	tlsVolumeMount := statefulset.CreateVolumeMount("tls", tls.OperatorSecretMountPath, statefulset.WithReadOnly(true))

	volumeMounts := []corev1.VolumeMount{tlsVolumeMount}
	volumes := []podtemplatespec.Modification{podtemplatespec.WithVolume(tlsVolume)}
	var containerMods []container.Modification

	if hasKeyPassword {
		keyPasswordVolume := statefulset.CreateVolumeFromSecret("grpc-key-password", userProvidedTLSSecret.Name)
		keyPasswordVolumeMount := statefulset.CreateVolumeMount("grpc-key-password", GrpcKeyPasswordMountPath,
			statefulset.WithReadOnly(true), statefulset.WithSubPath(GrpcKeyPasswordSecretKey))

		volumeMounts = append(volumeMounts, keyPasswordVolumeMount)
		volumes = append(volumes, podtemplatespec.WithVolume(keyPasswordVolume))
		containerMods = append(containerMods, prependCommand(sensitiveFilePermissionsWorkaround(GrpcKeyPasswordMountPath, TempGrpcKeyPasswordPath, "0600")))
	}

	containerMods = append(containerMods, container.WithVolumeMounts(volumeMounts))

	statefulsetModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		append(volumes, podtemplatespec.WithContainer(MongotContainerName, container.Apply(
			containerMods...,
		)))...,
	))

	return mongotModification, statefulsetModification, nil
}

// x509AuthResource adapts MongoDBSearch to provide x509 client cert secret names.
// It implements the tls.TLSConfigurableResource interface for use with tls.EnsureTLSSecret.
type x509AuthResource struct {
	*searchv1.MongoDBSearch
}

func (x *x509AuthResource) TLSSecretNamespacedName() types.NamespacedName {
	return x.X509ClientCertSecret()
}

func (x *x509AuthResource) TLSOperatorSecretNamespacedName() types.NamespacedName {
	return x.X509OperatorManagedSecret()
}

// ensureX509ClientCertConfig processes x509 client certificate configuration for the sync source in case of mongot to mongod communication.
// When x509 is configured, it replaces username/password auth with x509 certificate auth.
func (r *MongoDBSearchReconcileHelper) ensureX509ClientCertConfig(ctx context.Context, kubeClient kubernetesClient.Client) (mongot.Modification, statefulset.Modification, error) {
	if !r.mdbSearch.IsX509Auth() {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	tlsSourceConfig := r.db.TLSConfig()
	// in this https://docs.google.com/document/d/11xdolqdUR2Ht107AbxO5VKW658ytl6rPoJlYYc36ufE/edit?tab=t.0#heading=h.xpj7eo2nhgir document
	// it's mentioned that tls=true is required for x509 auth.
	if tlsSourceConfig == nil {
		return nil, nil, xerrors.New("tls must be enabled for syncSource to enable x509 auth for search resource")
	}

	x509Resource := &x509AuthResource{MongoDBSearch: r.mdbSearch}
	certFileName, err := tls.EnsureTLSSecret(ctx, kubeClient, x509Resource)
	if err != nil {
		return nil, nil, err
	}

	certPath := X509ClientCertOperatorMountPath + certFileName

	// Check if the user secret contains an optional key password
	hasKeyPassword := false
	userProvidedClientSecret := r.mdbSearch.X509ClientCertSecret()
	_, keyPasswordErr := secret.ReadKey(ctx, kubeClient, X509KeyPasswordSecretKey, userProvidedClientSecret)
	if keyPasswordErr == nil {
		hasKeyPassword = true
	}

	mongotModification := func(config *mongot.Config) {
		config.SyncSource.ReplicaSet.ScramAuth = nil
		config.SyncSource.ReplicaSet.X509 = &mongot.ConfigX509{
			TLSCertificateKeyFile:    ptr.To(certPath),
			CertificateAuthorityFile: ptr.To(tls.CAMountPath + tlsSourceConfig.CAFileName),
		}
		if hasKeyPassword {
			config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFilePasswordFile = ptr.To(TempX509KeyPasswordPath)
		}

		if config.SyncSource.Router != nil {
			config.SyncSource.Router.ScramAuth = nil
			config.SyncSource.Router.X509 = &mongot.ConfigX509{
				TLSCertificateKeyFile:    ptr.To(certPath),
				CertificateAuthorityFile: ptr.To(tls.CAMountPath + tlsSourceConfig.CAFileName),
			}
			if hasKeyPassword {
				config.SyncSource.Router.X509.TLSCertificateKeyFilePasswordFile = ptr.To(TempX509KeyPasswordPath)
			}
		}
	}

	// Build volume/mount modifications for the x509 client cert
	operatorSecret := x509Resource.TLSOperatorSecretNamespacedName()
	x509Volume := statefulset.CreateVolumeFromSecret("x509-client-cert", operatorSecret.Name)
	x509VolumeMount := statefulset.CreateVolumeMount("x509-client-cert", X509ClientCertOperatorMountPath, statefulset.WithReadOnly(true))

	volumeMounts := []corev1.VolumeMount{x509VolumeMount}
	volumes := []podtemplatespec.Modification{podtemplatespec.WithVolume(x509Volume)}
	var prependCommands []string

	// If the key password is present, reads the key password directly from the user-provided secret (not the operator-managed one). And mount that user secret as a separate volume
	// (x509-key-password) with subPath: tls.keyFilePassword, so only that one key is exposed at /mongot/x509-key-password. After file permissions workaround we copy it to
	// /tmp/x509-key-password.
	if hasKeyPassword {
		keyPasswordVolume := statefulset.CreateVolumeFromSecret("x509-key-password", userProvidedClientSecret.Name)
		keyPasswordVolumeMount := statefulset.CreateVolumeMount("x509-key-password", X509KeyPasswordMountPath,
			statefulset.WithReadOnly(true), statefulset.WithSubPath(X509KeyPasswordSecretKey))

		volumeMounts = append(volumeMounts, keyPasswordVolumeMount)
		volumes = append(volumes, podtemplatespec.WithVolume(keyPasswordVolume))
		prependCommands = append(prependCommands, sensitiveFilePermissionsWorkaround(X509KeyPasswordMountPath, TempX509KeyPasswordPath, "0600"))
	}

	containerModifications := []container.Modification{
		container.WithVolumeMounts(volumeMounts),
	}
	for _, cmd := range prependCommands {
		containerModifications = append(containerModifications, prependCommand(cmd))
	}

	stsModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		append(volumes, podtemplatespec.WithContainer(MongotContainerName, container.Apply(
			containerModifications...,
		)))...,
	))

	return mongotModification, stsModification, nil
}

// perShardTLSResource wraps MongoDBSearch to provide per-(cluster, shard) TLS secret names.
// It implements the tls.TLSConfigurableResource interface for use with tls.EnsureTLSSecret.
type perShardTLSResource struct {
	*searchv1.MongoDBSearch
	clusterIndex int
	shardName    string
}

// TLSSecretNamespacedName returns the per-(cluster, shard) source secret name.
func (p *perShardTLSResource) TLSSecretNamespacedName() types.NamespacedName {
	return p.TLSSecretForClusterShard(p.clusterIndex, p.shardName)
}

// TLSOperatorSecretNamespacedName returns the per-(cluster, shard) operator-managed secret name.
func (p *perShardTLSResource) TLSOperatorSecretNamespacedName() types.NamespacedName {
	return p.TLSOperatorSecretForClusterShard(p.clusterIndex, p.shardName)
}

func (r *MongoDBSearchReconcileHelper) ensureEgressTlsConfig(ctx context.Context, kubeClient kubernetesClient.Client) (mongot.Modification, statefulset.Modification, error) {
	tlsSourceConfig := r.db.TLSConfig()
	if tlsSourceConfig == nil {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	// Process optional SCRAM client certificate for mTLS
	var scramCertPath string
	hasScramKeyPassword := false
	if r.mdbSearch.HasScramClientCert() {
		scramCertResource := &scramClientCertResource{MongoDBSearch: r.mdbSearch}
		certFileName, err := tls.EnsureTLSSecret(ctx, kubeClient, scramCertResource)
		if err != nil {
			return nil, nil, err
		}
		scramCertPath = ScramClientCertOperatorMountPath + certFileName

		// Check if the user-provided secret contains an optional key password
		userProvidedSecret := r.mdbSearch.ScramClientCertSecret()
		_, keyPasswordErr := secret.ReadKey(ctx, kubeClient, ScramKeyPasswordSecretKey, userProvidedSecret)
		if keyPasswordErr == nil {
			hasScramKeyPassword = true
		}
	}

	mongotModification := func(config *mongot.Config) {
		scramTLS := &mongot.ScramAuthTLS{
			Enabled:                  true,
			CertificateAuthorityFile: ptr.To(tls.CAMountPath + tlsSourceConfig.CAFileName),
		}
		if scramCertPath != "" {
			scramTLS.TLSCertificateKeyFile = ptr.To(scramCertPath)
			if hasScramKeyPassword {
				scramTLS.TLSCertificateKeyFilePasswordFile = ptr.To(TempScramKeyPasswordPath)
			}
		}

		config.SyncSource.ReplicaSet.ScramAuth.TLS = scramTLS

		// For sharded clusters, also enable TLS for the Router (mongos) connection
		if config.SyncSource.Router != nil && config.SyncSource.Router.ScramAuth != nil {
			routerScramTLS := &mongot.ScramAuthTLS{
				Enabled:                  true,
				CertificateAuthorityFile: ptr.To(tls.CAMountPath + tlsSourceConfig.CAFileName),
			}
			if scramCertPath != "" {
				routerScramTLS.TLSCertificateKeyFile = ptr.To(scramCertPath)
				if hasScramKeyPassword {
					routerScramTLS.TLSCertificateKeyFilePasswordFile = ptr.To(TempScramKeyPasswordPath)
				}
			}
			config.SyncSource.Router.ScramAuth.TLS = routerScramTLS
		}

		// if the gRPC server is configured to accept TLS connections then toggle mTLS as well
		if config.Server.Grpc.TLS.Mode == mongot.ConfigTLSModeTLS {
			config.Server.Grpc.TLS.Mode = mongot.ConfigTLSModeMTLS
			config.Server.Grpc.TLS.CertificateAuthorityFile = ptr.To(tls.CAMountPath + tlsSourceConfig.CAFileName)
		}
	}

	caVolume := tlsSourceConfig.CAVolume
	volumeMounts := []corev1.VolumeMount{
		statefulset.CreateVolumeMount(caVolume.Name, tls.CAMountPath, statefulset.WithReadOnly(true)),
	}
	volumes := []podtemplatespec.Modification{podtemplatespec.WithVolume(caVolume)}
	var containerMods []container.Modification

	if scramCertPath != "" {
		operatorSecret := r.mdbSearch.ScramClientCertOperatorManagedSecret()
		scramCertVolume := statefulset.CreateVolumeFromSecret("scram-client-cert", operatorSecret.Name)
		scramCertVolumeMount := statefulset.CreateVolumeMount("scram-client-cert", ScramClientCertOperatorMountPath, statefulset.WithReadOnly(true))

		volumeMounts = append(volumeMounts, scramCertVolumeMount)
		volumes = append(volumes, podtemplatespec.WithVolume(scramCertVolume))

		if hasScramKeyPassword {
			userProvidedSecret := r.mdbSearch.ScramClientCertSecret()
			keyPasswordVolume := statefulset.CreateVolumeFromSecret("scram-key-password", userProvidedSecret.Name)
			keyPasswordVolumeMount := statefulset.CreateVolumeMount("scram-key-password", ScramKeyPasswordMountPath,
				statefulset.WithReadOnly(true), statefulset.WithSubPath(ScramKeyPasswordSecretKey))

			volumeMounts = append(volumeMounts, keyPasswordVolumeMount)
			volumes = append(volumes, podtemplatespec.WithVolume(keyPasswordVolume))
			containerMods = append(containerMods, prependCommand(sensitiveFilePermissionsWorkaround(ScramKeyPasswordMountPath, TempScramKeyPasswordPath, "0600")))
		}
	}

	containerMods = append(containerMods, container.WithVolumeMounts(volumeMounts))

	statefulsetModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		append(volumes, podtemplatespec.WithContainer(MongotContainerName, container.Apply(
			containerMods...,
		)))...,
	))

	return mongotModification, statefulsetModification, nil
}

// scramClientCertResource adapts MongoDBSearch to provide SCRAM client cert secret names.
// It implements the tls.TLSConfigurableResource interface for use with tls.EnsureTLSSecret.
type scramClientCertResource struct {
	*searchv1.MongoDBSearch
}

func (s *scramClientCertResource) TLSSecretNamespacedName() types.NamespacedName {
	return s.ScramClientCertSecret()
}

func (s *scramClientCertResource) TLSOperatorSecretNamespacedName() types.NamespacedName {
	return s.ScramClientCertOperatorManagedSecret()
}

func hashBytes(bytes []byte) string {
	hashBytes := sha256.Sum256(bytes)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:])
}

// baseMongotConfig sets up the common mongot configuration fields shared by all deployment types:
// SyncSource.ReplicaSet, Storage, Server.Grpc, Prometheus metrics, HealthCheck, and Logging.
func baseMongotConfig(search *searchv1.MongoDBSearch, hostAndPorts []string) mongot.Modification {
	return func(config *mongot.Config) {
		config.SyncSource = mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				HostAndPort: hostAndPorts,
				ScramAuth: &mongot.ConfigScramAuth{
					Username:     search.SourceUsername(),
					PasswordFile: TempSourceUserPasswordPath,
					TLS: &mongot.ScramAuthTLS{
						Enabled: false,
					},
					AuthSource: ptr.To("admin"),
				},
			},
			ReplicationReader: &mongot.ConfigReplicationReader{
				ReadPreference: ptr.To("secondaryPreferred"),
			},
		}
		config.Storage = mongot.ConfigStorage{
			DataPath: MongotDataPath,
		}
		config.Server = mongot.ConfigServer{
			Name: ServerNamePlaceholder,
			Grpc: &mongot.ConfigGrpc{
				Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotGrpcPort()),
				TLS: &mongot.ConfigGrpcTLS{
					Mode: mongot.ConfigTLSModeDisabled,
				},
			},
		}

		if prometheus := search.GetPrometheus(); prometheus != nil {
			config.Metrics = mongot.ConfigMetrics{
				Enabled: true,
				Address: fmt.Sprintf("0.0.0.0:%d", prometheus.GetPort()),
			}
		}

		config.HealthCheck = mongot.ConfigHealthCheck{
			Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotHealthCheckPort()),
		}
		config.Logging = mongot.ConfigLogging{
			Verbosity: string(search.GetLogLevel()),
			LogPath:   nil,
		}
	}
}

// wireprotoMongotMod appends a Wireproto server config when enabled, otherwise a no-op.
// Used only by ReplicaSet units.
func wireprotoMongotMod(search *searchv1.MongoDBSearch) mongot.Modification {
	if !search.IsWireprotoEnabled() {
		return func(*mongot.Config) {}
	}
	return func(config *mongot.Config) {
		config.Server.Wireproto = &mongot.ConfigWireproto{
			Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotWireprotoPort()),
			Authentication: &mongot.ConfigAuthentication{
				Mode:    "keyfile",
				KeyFile: TempKeyfilePath,
			},
			TLS: &mongot.ConfigWireprotoTLS{
				Mode: mongot.ConfigTLSModeDisabled,
			},
		}
	}
}

// routerMongotMod appends the mongos Router config. Used only by sharded units.
func routerMongotMod(search *searchv1.MongoDBSearch, shardedSource SearchSourceShardedDeployment) mongot.Modification {
	return func(config *mongot.Config) {
		config.SyncSource.Router = &mongot.ConfigRouter{
			HostAndPort: shardedSource.MongosHostsAndPorts(),
			ScramAuth: &mongot.ConfigScramAuth{
				Username:     search.SourceUsername(),
				PasswordFile: TempSourceUserPasswordPath,
				TLS: &mongot.ScramAuthTLS{
					Enabled: false,
				},
			},
		}
	}
}

// featureFlagsMongotMod sets mongot feature flags in the config.
// EnableOverloadRetrySignal defaults to true (load shedding enabled) unless
// the user explicitly sets it to false in the CR.
func featureFlagsMongotMod(search *searchv1.MongoDBSearch) mongot.Modification {
	return func(config *mongot.Config) {
		if ptr.Deref(retrySigFromFeatureFlags(search.Spec.FeatureFlags), true) {
			config.FeatureFlags = &mongot.ConfigFeatureFlags{
				OverloadRetrySignal: new(true),
			}
		}
	}
}

func retrySigFromFeatureFlags(ff *searchv1.FeatureFlags) *bool {
	if ff == nil {
		return nil
	}
	return ff.EnableOverloadRetrySignal
}

// replicationReaderTagSetsMod maps a cluster's syncSourceSelector.matchTagSets to
// syncSource.replicationReader.tagSets so mongot reads from tag-matched sync-source
// members. Each map becomes one tag set (AND across keys), key-sorted for a stable
// config hash. Without matchTagSets it leaves the base config's match-any default
// (no tagSets) untouched. secondaryPreferred is required: mongot rejects tagSets
// when readPreference is primary.
func replicationReaderTagSetsMod(selector *searchv1.SyncSourceSelector) mongot.Modification {
	return func(config *mongot.Config) {
		if selector == nil || len(selector.MatchTagSets) == 0 {
			return
		}
		tagSets := make([][]mongot.ConfigTag, 0, len(selector.MatchTagSets))
		for _, tags := range selector.MatchTagSets {
			tagSet := make([]mongot.ConfigTag, 0, len(tags))
			for _, name := range slices.Sorted(maps.Keys(tags)) {
				tagSet = append(tagSet, mongot.ConfigTag{Name: name, Value: tags[name]})
			}
			tagSets = append(tagSets, tagSet)
		}
		// baseMongotConfig always sets ReplicationReader (secondaryPreferred) first in the
		// Apply chain; this mod only populates its tagSets.
		config.SyncSource.ReplicationReader.TagSets = tagSets
	}
}

func GetMongodConfigParameters(search *searchv1.MongoDBSearch, clusterDomain string) map[string]any {
	return buildSearchSetParameters(mongotHostAndPort(search, clusterDomain), searchTLSMode(search), !search.IsWireprotoEnabled())
}

// mongotEndpointForShard resolves the per-shard mongot endpoint that the source
// MongoDB's shard mongods point at via mongotHost. Single-cluster source only —
// the cluster index is fixed to 0. For an MC sharded source, callers must set
// per-cluster mongotHost via spec.shardOverrides on the source MongoDB.
func mongotEndpointForShard(search *searchv1.MongoDBSearch, shardName string, clusterDomain string) string {
	if search.IsShardedUnmanagedLB() {
		return search.GetEndpointForShard(shardName)
	}
	if search.IsLBModeManaged() {
		return proxyServiceHostAndPortForShard(search, 0, shardName, clusterDomain)
	}
	stsName := search.MongotStatefulSetForClusterShard(0, shardName)
	svcName := search.MongotServiceForClusterShard(0, shardName)
	port := search.GetEffectiveMongotPort()
	return fmt.Sprintf("%s-0.%s.%s.svc.%s:%d", stsName.Name, svcName.Name, svcName.Namespace, clusterDomain, port)
}

// GetMongodConfigParametersForShard returns the mongod configuration parameters for a specific shard
// in a sharded cluster.
func GetMongodConfigParametersForShard(search *searchv1.MongoDBSearch, shardName string, clusterDomain string) map[string]any {
	return buildSearchSetParameters(mongotEndpointForShard(search, shardName, clusterDomain), searchTLSMode(search), !search.IsWireprotoEnabled())
}

// GetMongosConfigParametersForSharded picks the mongos→mongot endpoint by topology. No-LB targets the
// first shard's per-shard proxy svc FQDN (the only sharded mongot hostname per-shard cert SANs cover);
// the cluster-level Service would route to the same pod but isn't in SANs.
// clusterIndex (from the persisted ClusterMapping) is for resource naming only;
// clusterName resolves the cluster's LB config (empty = first cluster).
func GetMongosConfigParametersForSharded(search *searchv1.MongoDBSearch, clusterIndex int, clusterName string, shardNames []string, clusterDomain string) map[string]any {
	var endpoint string
	// Three branches: explicit unmanaged LB, no loadBalancer (pre-MVP
	// single-cluster shape), and managed LB. The TD lists no-LB as RS-only at
	// GA, so case B should die when admission tightens; kept for now to avoid
	// regressing pre-MVP.
	switch {
	case search.IsShardedUnmanagedLB() && len(shardNames) > 0:
		endpoint = mongotEndpointForShard(search, shardNames[0], clusterDomain)
	case !search.IsLBModeManaged() && len(shardNames) > 0:
		endpoint = proxyServiceHostAndPortForShard(search, clusterIndex, shardNames[0], clusterDomain)
	default:
		endpoint = mongotEndpointForClusterLevel(search, clusterIndex, clusterName, clusterDomain)
	}
	return buildSearchSetParameters(endpoint, searchTLSMode(search), true) // useGrpc must be true for mongos-to-mongot communication
}

// mongotEndpointForClusterLevel resolves the shard-agnostic mongot endpoint for a cluster's mongos.
// For managed LB with an externalHostname, returns the cluster-level external form (template with
// leading `{shardName}.` stripped). Otherwise (managed LB without externalHostname, or no LB) returns
// the cluster-level proxy Service in-cluster FQDN.
func mongotEndpointForClusterLevel(search *searchv1.MongoDBSearch, clusterIndex int, clusterName string, clusterDomain string) string {
	if search.IsLBModeManaged() {
		if endpoint := search.GetManagedLBEndpointForClusterLevel(clusterName); endpoint != "" {
			return endpoint
		}
	}
	svcName := search.ProxyServiceNamespacedNameForCluster(clusterIndex)
	port := search.GetEffectiveMongotPort()
	return fmt.Sprintf("%s.%s.svc.%s:%d", svcName.Name, svcName.Namespace, clusterDomain, port)
}

func searchTLSMode(search *searchv1.MongoDBSearch) automationconfig.TLSMode {
	if search.Spec.Security.TLS != nil {
		return automationconfig.TLSModeRequired
	}
	return automationconfig.TLSModeDisabled
}

func buildSearchSetParameters(mongotEndpoint string, tlsMode automationconfig.TLSMode, useGrpc bool) map[string]any {
	return map[string]any{
		"setParameter": map[string]any{
			"mongotHost":                                      mongotEndpoint,
			"searchIndexManagementHostAndPort":                mongotEndpoint,
			"skipAuthenticationToSearchIndexManagementServer": false,
			"skipAuthenticationToMongot":                      false,
			"searchTLSMode":                                   string(tlsMode),
			"useGrpcForSearch":                                useGrpc,
		},
	}
}

// mongotHostAndPort returns the mongotHost endpoint for ReplicaSet topologies.
// For unmanaged LB, the user-provided endpoint is returned.
// For managed LB, the stable proxy service FQDN is returned (selector flips between mongot/envoy).
// For no LB (single mongot), the first pod's headless FQDN is returned (pod-0.svc).
func mongotHostAndPort(search *searchv1.MongoDBSearch, clusterDomain string) string {
	if search.IsReplicaSetUnmanagedLB() {
		return search.GetUnmanagedLBEndpoint()
	}
	port := search.GetEffectiveMongotPort()
	if search.IsLBModeManaged() {
		proxyName := search.ProxyServiceNamespacedNameForCluster(0)
		return fmt.Sprintf("%s.%s.svc.%s:%d", proxyName.Name, proxyName.Namespace, clusterDomain, port)
	}
	stsName := search.StatefulSetNamespacedNameForCluster(0)
	svcName := search.SearchServiceNamespacedNameForCluster(0)
	return fmt.Sprintf("%s-0.%s.%s.svc.%s:%d", stsName.Name, svcName.Name, svcName.Namespace, clusterDomain, port)
}

func proxyServiceHostAndPortForShard(search *searchv1.MongoDBSearch, clusterIndex int, shardName string, clusterDomain string) string {
	proxyName := search.ProxyServiceNameForClusterShard(clusterIndex, shardName)
	port := search.GetEffectiveMongotPort()
	return fmt.Sprintf("%s.%s.svc.%s:%d", proxyName.Name, proxyName.Namespace, clusterDomain, port)
}

func (r *MongoDBSearchReconcileHelper) ValidateSingleMongoDBSearchForSearchSource(ctx context.Context) error {
	if r.mdbSearch.Spec.Source != nil && r.mdbSearch.Spec.Source.ExternalMongoDBSource != nil {
		return nil
	}

	ref := r.mdbSearch.GetMongoDBResourceRef()
	searchList := &searchv1.MongoDBSearchList{}
	if err := r.client.List(ctx, searchList, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(searchv1.MongoDBSearchIndexFieldName, ref.Namespace+"/"+ref.Name),
	}); err != nil {
		return xerrors.Errorf("Error listing MongoDBSearch resources for search source '%s': %w", ref.Name, err)
	}

	if len(searchList.Items) > 1 {
		resourceNames := make([]string, len(searchList.Items))
		for i, search := range searchList.Items {
			resourceNames[i] = search.Name
		}
		return xerrors.Errorf(
			"Found multiple MongoDBSearch resources for search source '%s': %s", ref.Name,
			strings.Join(resourceNames, ", "),
		)
	}

	return nil
}

func (r *MongoDBSearchReconcileHelper) ValidateSearchImageVersion(version string) error {
	if strings.Contains(version, unsupportedSearchVersion) {
		return xerrors.Errorf(unsupportedSearchVersionErrorFmt, unsupportedSearchVersion)
	}

	return nil
}

// ValidateMultipleReplicasUnmanagedLBTopology validates that an operator-managed
// (internal) MongoDB source running multiple mongot replicas behind an unmanaged
// load balancer uses an endpoint template that matches the resolved source topology:
// a sharded source needs the per-shard {shardName} template, a replica set source
// must not carry it. It lives at reconcile level because internal sources only
// reveal their sharded-ness after the referenced MongoDB resource is fetched;
// external sources are fully covered at spec-validation time by
// validateUnmanagedEndpointTemplate, and the no-load-balancer case is covered by
// validateMultipleReplicasRequireLB. Without this check a mismatched endpoint is
// silently ignored (see GetEndpointForShard / mongotHostAndPort) and all mongot
// traffic pins to pod 0, defeating the load balancer the user configured.
func (r *MongoDBSearchReconcileHelper) ValidateMultipleReplicasUnmanagedLBTopology() error {
	if !r.mdbSearch.HasMultipleReplicas() || !r.mdbSearch.IsLBModeUnmanaged() {
		return nil
	}

	// External sources are validated at spec time, where their topology is known.
	if r.mdbSearch.IsExternalMongoDBSource() {
		return nil
	}

	if _, ok := r.db.(SearchSourceShardedDeployment); ok {
		if !r.mdbSearch.IsShardedUnmanagedLB() {
			return xerrors.Errorf(
				"spec.clusters[].loadBalancer.unmanaged.endpoint must contain a %s placeholder for a sharded source with multiple mongot replicas; "+
					"without it the endpoint cannot differentiate shards and traffic pins to a single mongot",
				searchv1.ShardNamePlaceholder,
			)
		}
		return nil
	}

	if !r.mdbSearch.IsReplicaSetUnmanagedLB() {
		return xerrors.Errorf(
			"spec.clusters[].loadBalancer.unmanaged.endpoint must not contain a %s placeholder for a replica set source with multiple mongot replicas",
			searchv1.ShardNamePlaceholder,
		)
	}

	return nil
}

// ValidateManagedLBShardedTLS validates that TLS is configured when using managed LB
// with a sharded cluster (internal or external). Envoy's SNI-based filter-chain routing
// requires a TLS ClientHello; without it, traffic cannot be routed to the correct shard.
func (r *MongoDBSearchReconcileHelper) ValidateManagedLBShardedTLS() error {
	if !r.mdbSearch.IsLBModeManaged() {
		return nil
	}

	if _, ok := r.db.(SearchSourceShardedDeployment); !ok {
		return nil
	}

	if !r.mdbSearch.IsTLSConfigured() {
		return xerrors.Errorf(
			"TLS (spec.security.tls) is required when using managed load balancer with a sharded cluster; " +
				"Envoy uses SNI-based routing which depends on the TLS ClientHello to route traffic to the correct shard",
		)
	}

	return nil
}

func (r *MongoDBSearchReconcileHelper) getMongotVersion() string {
	version := strings.TrimSpace(r.mdbSearch.Spec.Version)
	if version != "" {
		return version
	}

	version = strings.TrimSpace(r.operatorSearchConfig.SearchVersion)
	if version != "" {
		return version
	}

	effective := r.mdbSearch.EffectiveClusters()
	if len(effective) == 0 || effective[0].StatefulSetConfiguration == nil {
		return ""
	}

	for _, container := range effective[0].StatefulSetConfiguration.SpecWrapper.Spec.Template.Spec.Containers {
		if container.Name == MongotContainerName {
			return extractImageTag(container.Image)
		}
	}

	return ""
}

func extractImageTag(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}

	if at := strings.Index(image, "@"); at != -1 {
		image = image[:at]
	}

	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon > lastSlash {
		return image[lastColon+1:]
	}

	return ""
}
