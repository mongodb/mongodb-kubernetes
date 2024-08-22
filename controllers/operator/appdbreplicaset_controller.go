package operator

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"

	mekoService "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"

	"github.com/hashicorp/go-multierror"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"

	mdbcv1_controllers "github.com/mongodb/mongodb-kubernetes-operator/controllers"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/result"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	intp "github.com/10gen/ops-manager-kubernetes/pkg/util/int"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/timeutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/agent"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scram"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/generate"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"
	"github.com/stretchr/objx"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"go.uber.org/zap"
	"k8s.io/utils/ptr"
)

type agentType string

const (
	appdbCAFilePath              = "/var/lib/mongodb-automation/secrets/ca/ca-pem"
	appDBACConfigMapVersionField = "version"

	monitoring agentType = "MONITORING"
	automation agentType = "AUTOMATION"

	// Used to note that for this particular case it is not necessary to pass
	// the hash of the Prometheus certificate. This is to avoid having to
	// calculate and pass the Prometheus Cert Hash when it is not needed.
	UnusedPrometheusConfiguration string = ""

	// Used to convey to the operator to force reconfigure agent. At the moment
	// it is used for DR in case of Multi-Cluster AppDB when after a cluster outage
	// there is no primary in the AppDB deployment.
	ForceReconfigureAnnotation = "mongodb.com/v1.forceReconfigure"

	ForcedReconfigureAlreadyPerformedAnnotation = "mongodb.com/v1.forceReconfigurePerformed"
)

// ReconcileAppDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileAppDbReplicaSet struct {
	*ReconcileCommonController
	omConnectionFactory om.ConnectionFactory

	centralClient kubernetesClient.Client
	// ordered list of member clusters; order in this list is preserved across runs using memberClusterIndex
	memberClusters      []multicluster.MemberCluster
	currentClusterSpecs map[string]int
}

func newAppDBReplicaSetReconciler(ctx context.Context, appDBSpec omv1.AppDBSpec, commonController *ReconcileCommonController, omConnectionFactory om.ConnectionFactory, globalMemberClustersMap map[string]cluster.Cluster, log *zap.SugaredLogger) (*ReconcileAppDbReplicaSet, error) {
	reconciler := &ReconcileAppDbReplicaSet{
		ReconcileCommonController: commonController,
		omConnectionFactory:       omConnectionFactory,
		currentClusterSpecs:       make(map[string]int),
	}

	if err := reconciler.initializeMemberClusters(ctx, appDBSpec, globalMemberClustersMap, log); err != nil {
		return nil, xerrors.Errorf("failed to initialize appdb replicaset controller: %w", err)
	}

	return reconciler, nil
}

// initializeMemberClusters main goal is to initialise memberClusterList field with the ordered list of member clusters to iterate over.
//
// When in single-cluster topology it initializes memberClusterList with a dummy "central" cluster
// containing the number of members from appDBSpec.Members field. T
// Thanks to that all code in reconcile loop is always looping over member cluster.

// For multi-cluster topology, this function maintains (updates or creates if doesn't exist yet) -cluster-mapping config map, to preserve
// mapping between clusterName from clusterSpecList and the assigned cluster index.
// For example, when user declares in CR:
//
//		clusterSpecList:
//		  - clusterName: cluster-1
//	     members: 1
//		  - clusterName: cluster-2
//	     members: 2
//		  - clusterName: cluster-3
//	     members: 3
//
// The function will assign the following indexes when first deploying resources:
//   - cluster-1, idx=0, members=1 (no index in map, get first next available index)
//   - cluster-2: idx=1, members=2 (same as above)
//   - cluster-3: idx=2, members=3 (same as above)
//
// Those indexes are crucial to maintain resources in member cluster and have to be preserved for the given cluster name. Cluster indexes are contained in
// statefulset names, process names, etc.
//
// If in the subsequent reconciliations the clusterSpecList is changed, this function guarantees that no matter what, assigned first cluster index will
// allways be preserved.
// For example, the user reorders clusterSpecList, removes cluster-1 and cluster-3 and adds two other cluster in random places:
//
//		clusterSpecList:
//		  - clusterName: cluster-10
//	     members: 10
//		  - clusterName: cluster-2
//	     members: 2
//		  - clusterName: cluster-5
//	     members: 5
//
// initializeMemberClusters will then read existing cluster mapping from config map and create list of member clusters in the following order:
//   - cluster-2, idx=1 as it was saved to map before
//   - cluster-10, idx=3, assigns a new index that is the next available index (0,1,2 are taken)
//   - cluster-5, idx=4, assigns a new index that is the next available index (0,1,2,3 are taken)
//
// On top of that, for all removed member clusters, if they previously contained more than one member (haven't been scaled to zero),
// the function will add them back with preserved indexes and saved previously member counts:
// In the end the function will contain the following list of member clusters to iterate on:
//   - cluster-1, idx=0, members=1 (removed cluster, idx and previous members from map)
//   - cluster-2, idx=1, members=2 (idx from map, members from clusterSpecList)
//   - cluster-3, idx=2, members=3 (removed cluster, idx and previous members from map)
//   - cluster-10, idx=3, members=10 (assigns a new index that is the next available index (0,1,2 are taken))
//   - cluster-5, idx=4, members=5 (assigns a new index that is the next available index (0,1,2,3 are taken))
func (r *ReconcileAppDbReplicaSet) initializeMemberClusters(ctx context.Context, appDBSpec omv1.AppDBSpec, globalMemberClustersMap map[string]cluster.Cluster, log *zap.SugaredLogger) error {
	r.centralClient = r.client

	if appDBSpec.IsMultiCluster() {
		if len(globalMemberClustersMap) == 0 {
			return xerrors.Errorf("member clusters have to be initialized for MultiCluster AppDB topology")
		}
		// here we access ClusterSpecList directly, as we have to check what's been defined in yaml
		if len(appDBSpec.ClusterSpecList) == 0 {
			return xerrors.Errorf("for appDBSpec.Topology = MultiCluster, clusterSpecList have to be non empty")
		}

		memberClusterMapping, err := r.updateMemberClusterMapping(ctx, appDBSpec)
		if err != nil {
			return xerrors.Errorf("failed to initialize clustermapping: %e", err)
		}
		specClusterMap := map[string]struct{}{}

		// extract client from each cluster object.
		for _, clusterSpecItem := range appDBSpec.GetClusterSpecList() {
			specClusterMap[clusterSpecItem.ClusterName] = struct{}{}

			var memberClusterKubeClient kubernetesClient.Client
			var memberClusterSecretClient secrets.SecretClient
			memberClusterClient, ok := globalMemberClustersMap[clusterSpecItem.ClusterName]
			if !ok {
				var clusterList []string
				for m := range globalMemberClustersMap {
					clusterList = append(clusterList, m)
				}
				log.Warnf("Member cluster %s specified in appDBSpec.clusterSpecList is not found in the list of operator's member clusters: %+v. "+
					"Assuming the cluster is down. It will be ignored from reconciliation but its MongoDB processes will still be maintained in replicaset configuration.", clusterSpecItem.ClusterName, clusterList)
			} else {
				memberClusterKubeClient = kubernetesClient.NewClient(memberClusterClient.GetClient())
				memberClusterSecretClient = secrets.SecretClient{
					VaultClient: nil, // Vault is not supported yet on multicluster
					KubeClient:  memberClusterKubeClient,
				}
			}

			r.memberClusters = append(r.memberClusters, multicluster.MemberCluster{
				Name:         clusterSpecItem.ClusterName,
				Index:        memberClusterMapping[clusterSpecItem.ClusterName],
				Client:       memberClusterKubeClient,
				SecretClient: memberClusterSecretClient,
				Replicas:     r.getLastAppliedMemberCount(ctx, appDBSpec, clusterSpecItem.ClusterName, log),
				Active:       true,
				Healthy:      memberClusterKubeClient != nil,
			})
		}

		// add previous member clusters with last applied members. This is required for being able to scale down the appdb members one by one.
		for previousMember := range memberClusterMapping {
			// If the previous member is already present in the spec, skip it safely
			if _, ok := specClusterMap[previousMember]; ok {
				continue
			}

			previousMemberReplicas := r.getLastAppliedMemberCount(ctx, appDBSpec, previousMember, log)
			// If the previous member was already scaled down to 0 members, skip it safely
			if previousMemberReplicas == 0 {
				continue
			}

			var memberClusterKubeClient kubernetesClient.Client
			var memberClusterSecretClient secrets.SecretClient
			memberClusterClient, ok := globalMemberClustersMap[previousMember]
			if !ok {
				var clusterList []string
				for m := range globalMemberClustersMap {
					clusterList = append(clusterList, m)
				}
				log.Warnf("Member cluster %s that has to be scaled to 0 replicas is not found in the list of operator's member clusters: %+v. "+
					"Assuming the cluster is down. It will be ignored from reconciliation but and it's MongoDB processes will be scaled down to 0 in replicaset configuration.", previousMember, clusterList)
			} else {
				memberClusterKubeClient = kubernetesClient.NewClient(memberClusterClient.GetClient())
				memberClusterSecretClient = secrets.SecretClient{
					VaultClient: nil, // Vault is not supported yet on multicluster
					KubeClient:  memberClusterKubeClient,
				}
			}

			r.memberClusters = append(r.memberClusters, multicluster.MemberCluster{
				Name:         previousMember,
				Index:        memberClusterMapping[previousMember],
				Client:       memberClusterKubeClient,
				SecretClient: memberClusterSecretClient,
				Replicas:     previousMemberReplicas,
				Active:       false,
				Healthy:      memberClusterKubeClient != nil,
			})
		}
		sort.Slice(r.memberClusters, func(i, j int) bool {
			return r.memberClusters[i].Index < r.memberClusters[j].Index
		})
	} else {
		// for SingleCluster member cluster list will contain one member  which will be the central (default) cluster
		r.memberClusters = []multicluster.MemberCluster{multicluster.GetLegacyCentralMemberCluster(appDBSpec.Members, 0, r.centralClient, r.SecretClient)}
	}

	log.Debugf("Initialized member cluster list: %+v", util.Transform(r.memberClusters, func(m multicluster.MemberCluster) string {
		return fmt.Sprintf("{Name: %s, Index: %d, Replicas: %d, Active: %t, Healthy: %t}", m.Name, m.Index, m.Replicas, m.Active, m.Healthy)
	}))

	return nil
}

func (r *ReconcileAppDbReplicaSet) getLastAppliedMemberCount(ctx context.Context, spec omv1.AppDBSpec, clusterName string, log *zap.SugaredLogger) int {
	if !spec.IsMultiCluster() {
		return 0
	}
	specMapping, err := r.getLastAppliedMemberSpec(ctx, spec, log)
	if err != nil {
		return 0
	}
	return specMapping[clusterName]
}

func (r *ReconcileAppDbReplicaSet) getLastAppliedMemberSpec(ctx context.Context, spec omv1.AppDBSpec, log *zap.SugaredLogger) (map[string]int, error) {
	if !spec.IsMultiCluster() {
		return nil, nil
	}
	specMapping := map[string]int{}
	existingConfigMap, err := r.centralClient.GetConfigMap(ctx, types.NamespacedName{Name: spec.LastAppliedMemberSpecConfigMapName(), Namespace: spec.Namespace})
	existingConfigMapNotFound := false
	if err != nil {
		if apiErrors.IsNotFound(err) {
			existingConfigMapNotFound = true
		} else {
			return nil, xerrors.Errorf("failed to read last applied member spec config map %s: %w", spec.LastAppliedMemberSpecConfigMapName(), err)
		}
	} else {
		for clusterName, replicasStr := range existingConfigMap.Data {
			replicas, err := strconv.Atoi(replicasStr)
			if err != nil {
				return nil, xerrors.Errorf("failed to read last applied member spec from configmap %s (%+v): %w", spec.LastAppliedMemberSpecConfigMapName(), existingConfigMap.Data, err)
			}
			specMapping[clusterName] = replicas
		}
	}

	configMapData := map[string]string{}
	for k, v := range specMapping {
		configMapData[k] = fmt.Sprintf("%d", v)
	}
	specConfigMap := configmap.Builder().SetName(spec.LastAppliedMemberSpecConfigMapName()).SetNamespace(spec.Namespace).SetData(configMapData).Build()
	if existingConfigMapNotFound {
		if err := r.centralClient.CreateConfigMap(ctx, specConfigMap); err != nil {
			return nil, xerrors.Errorf("failed to create last applied member spec configmap %s: %w", spec.LastAppliedMemberSpecConfigMapName(), err)
		}
	}

	log.Debugf("Read last applied member spec configmap %s: %v", spec.LastAppliedMemberSpecConfigMapName(), specConfigMap.Data)
	return specMapping, nil
}

func (r *ReconcileAppDbReplicaSet) updateLastAppliedMemberSpec(ctx context.Context, spec omv1.AppDBSpec, memberSpec map[string]int, log *zap.SugaredLogger) error {
	// read existing spec
	existingSpec := map[string]int{}
	existingConfigMap, err := r.centralClient.GetConfigMap(ctx, types.NamespacedName{Name: spec.LastAppliedMemberSpecConfigMapName(), Namespace: spec.Namespace})
	existingConfigMapNotFound := false
	if err != nil {
		if apiErrors.IsNotFound(err) {
			existingConfigMapNotFound = true
		} else {
			return xerrors.Errorf("failed to read last applied member spec config map %s: %w", spec.LastAppliedMemberSpecConfigMapName(), err)
		}
	} else {
		for clusterName, replicasStr := range existingConfigMap.Data {
			replicas, err := strconv.Atoi(replicasStr)
			if err != nil {
				return xerrors.Errorf("failed to read last applied member spec from config map %s (%+v): %w", spec.LastAppliedMemberSpecConfigMapName(), existingConfigMap.Data, err)
			}
			existingSpec[clusterName] = replicas
		}
	}
	configMapData := map[string]string{}
	for k, v := range memberSpec {
		configMapData[k] = fmt.Sprintf("%d", v)
	}
	specConfigMap := configmap.Builder().SetName(spec.LastAppliedMemberSpecConfigMapName()).SetNamespace(spec.Namespace).SetData(configMapData).Build()
	if existingConfigMapNotFound {
		if err := r.centralClient.CreateConfigMap(ctx, specConfigMap); err != nil {
			return xerrors.Errorf("failed to create last applied member spec configmap %s: %w", spec.LastAppliedMemberSpecConfigMapName(), err)
		}
	} else if !reflect.DeepEqual(memberSpec, existingSpec) {
		if err := r.centralClient.UpdateConfigMap(ctx, specConfigMap); err != nil {
			return xerrors.Errorf("failed to update last applied member spec configmap %s: %w", spec.LastAppliedMemberSpecConfigMapName(), err)
		}
	}
	log.Debugf("Storing last applied member spec configmap %s: %v", spec.LastAppliedMemberSpecConfigMapName(), configMapData)
	return nil
}

// updateMemberClusterMapping is maintains mapping of member cluster names to its indexes
// TODO: it's a first step towards extracting this into a class to maintain a generic deployment state + replication (CLOUDP-199499)
func updateMemberClusterMapping(ctx context.Context, namespace string, configMapName string, centralClient kubernetesClient.Client, memberClusterNames []string) (map[string]int, error) {
	// read existing config map
	existingMapping := map[string]int{}
	existingConfigMap, err := centralClient.GetConfigMap(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace})
	existingConfigMapNotFound := false
	if err != nil {
		if apiErrors.IsNotFound(err) {
			existingConfigMapNotFound = true
		} else {
			return nil, xerrors.Errorf("failed to read cluster mapping config map %s: %w", configMapName, err)
		}
	} else {
		for clusterName, indexStr := range existingConfigMap.Data {
			index, err := strconv.Atoi(indexStr)
			if err != nil {
				return nil, xerrors.Errorf("failed to read cluster mapping indexes from config map %s (%+v): %w", configMapName, existingConfigMap.Data, err)
			}
			existingMapping[clusterName] = index
		}
	}

	newMapping := map[string]int{}
	for k, v := range existingMapping {
		newMapping[k] = v
	}

	// merge existing config map with cluster spec list
	for _, clusterName := range memberClusterNames {
		if _, ok := newMapping[clusterName]; !ok {
			newMapping[clusterName] = getNextIndex(newMapping)
		}
	}

	configMapData := map[string]string{}
	for k, v := range newMapping {
		configMapData[k] = fmt.Sprintf("%d", v)
	}

	// save config map if needed
	mappingConfigMap := configmap.Builder().SetName(configMapName).SetNamespace(namespace).SetData(configMapData).Build()
	if existingConfigMapNotFound {
		if err := centralClient.CreateConfigMap(ctx, mappingConfigMap); err != nil {
			return nil, xerrors.Errorf("failed to create cluster mapping config map %s: %w", configMapName, err)
		}
	} else if !reflect.DeepEqual(newMapping, existingMapping) {
		// update only changed
		if err := centralClient.UpdateConfigMap(ctx, mappingConfigMap); err != nil {
			return nil, xerrors.Errorf("failed to update cluster mapping config map %s: %w", configMapName, err)
		}
	}

	return newMapping, nil
}

func getNextIndex(m map[string]int) int {
	maxi := -1

	for _, val := range m {
		maxi = intp.Max(maxi, val)
	}
	return maxi + 1
}

// updateMemberClusterMapping returns a map of member cluster name -> cluster index.
// Mapping is preserved in spec.ClusterMappingConfigMapName() config map. Config map is created if not exists.
// Subsequent executions will merge, update and store mappings from config map and from clusterSpecList and save back to config map.
func (r *ReconcileAppDbReplicaSet) updateMemberClusterMapping(ctx context.Context, spec omv1.AppDBSpec) (map[string]int, error) {
	if !spec.IsMultiCluster() {
		return nil, nil
	}

	return updateMemberClusterMapping(ctx, spec.Namespace, spec.ClusterMappingConfigMapName(), r.centralClient, util.Transform(spec.GetClusterSpecList(), func(clusterSpecItem mdbv1.ClusterSpecItem) string {
		return clusterSpecItem.ClusterName
	}))
}

// shouldReconcileAppDB returns a boolean indicating whether or not the reconciliation for this set of processes should occur.
func (r *ReconcileAppDbReplicaSet) shouldReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (bool, error) {
	memberCluster := r.getMemberCluster(r.getNameOfFirstMemberCluster())
	currentAc, err := automationconfig.ReadFromSecret(ctx, memberCluster.Client, types.NamespacedName{
		Namespace: opsManager.GetNamespace(),
		Name:      opsManager.Spec.AppDB.AutomationConfigSecretName(),
	})
	if err != nil {
		return false, xerrors.Errorf("error reading AppDB Automation Config: %w", err)
	}

	// there is no automation config yet,0 we can safely reconcile.
	if currentAc.Processes == nil {
		return true, nil
	}

	desiredAc, err := r.buildAppDbAutomationConfig(ctx, opsManager, automation, UnusedPrometheusConfiguration, memberCluster.Name, log)
	if err != nil {
		return false, xerrors.Errorf("error building AppDB Automation Config: %w", err)
	}

	currentProcessesAreDisabled := false
	for _, p := range currentAc.Processes {
		if p.Disabled {
			currentProcessesAreDisabled = true
			break
		}
	}

	desiredProcessesAreDisabled := false
	for _, p := range desiredAc.Processes {
		if p.Disabled {
			desiredProcessesAreDisabled = true
			break
		}
	}

	// skip the reconciliation as there are disabled processes, and we are not attempting to re-enable them.
	if currentProcessesAreDisabled && desiredProcessesAreDisabled {
		return false, nil
	}

	return true, nil
}

// ReconcileAppDB deploys the "headless" agent, and wait until it reaches the goal state
func (r *ReconcileAppDbReplicaSet) ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (res reconcile.Result, e error) {
	rs := opsManager.Spec.AppDB
	log := zap.S().With("ReplicaSet (AppDB)", kube.ObjectKey(opsManager.Namespace, rs.Name()))

	appDbStatusOption := status.NewOMPartOption(status.AppDb)
	omStatusOption := status.NewOMPartOption(status.OpsManager)

	log.Info("AppDB ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs)
	log.Infow("ReplicaSet.Status", "status", opsManager.Status.AppDbStatus)

	opsManagerUserPassword, err := r.ensureAppDbPassword(ctx, opsManager, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error ensuring Ops Manager user password: %w", err)), log, appDbStatusOption)
	}

	// if any of the processes have been marked as disabled, we don't reconcile the AppDB.
	// This could be the case if we want to disable a process to perform a manual backup of the AppDB.
	shouldReconcile, err := r.shouldReconcileAppDB(ctx, opsManager, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error determining AppDB reconciliation state: %w", err)), log, appDbStatusOption)
	}
	if !shouldReconcile {
		log.Info("Skipping reconciliation for AppDB because at least one of the processes has been disabled. To reconcile the AppDB all process need to be enabled in automation config")
		return result.OK()
	}

	podVars, err := r.tryConfigureMonitoringInOpsManager(ctx, opsManager, opsManagerUserPassword, log)
	// it's possible that Ops Manager will not be available when we attempt to configure AppDB monitoring
	// in Ops Manager. This is not a blocker to continue with the reset of the reconciliation.
	if err != nil {
		log.Errorf("Unable to configure monitoring of AppDB: %s, configuration will be attempted next reconciliation.", err)

		if podVars.ProjectID != "" {
			// when there is an error, but projectID is configured, then that means OM has been configured before but might be down
			// in that case, we need to ensure that all member clusters have all the secrets to be mounted properly
			// newly added member clusters will not contain them otherwise until OM is recreated and running
			if err := r.ensureProjectIDConfigMap(ctx, opsManager, podVars.ProjectID); err != nil {
				// we ignore the error here and let reconciler continue
				log.Warnf("ignoring ensureProjectIDConfigMap error: %v", err)
			}
			// OM connection is passed as nil as it's used only for generating agent api key. Here we have it already
			if err := r.ensureAppDbAgentApiKey(ctx, opsManager, nil, podVars.ProjectID, log); err != nil {
				// we ignore the error here and let reconciler continue
				log.Warnf("ignoring ensureAppDbAgentApiKey error: %v", err)
			}
		}

		// errors returned from "tryConfigureMonitoringInOpsManager" could be either transient or persistent. Transient errors could be when the ops-manager pods
		// are not ready and trying to connect to the ops-manager service timeout, a persistent error is when the "ops-manager-admin-key" is corrupted, in this case
		// any API call to ops-manager will fail(including the configuration of AppDB monitoring), this error should be reflected to the user in the "OPSMANAGER" status.
		if strings.Contains(err.Error(), "401 (Unauthorized)") {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("The admin-key secret might be corrupted: %w", err)), log, omStatusOption)
		}
	}

	appdbOpts := construct.AppDBStatefulSetOptions{}
	if architectures.IsRunningStaticArchitecture(opsManager.Annotations) {
		if !rs.PodSpec.IsAgentImageOverridden() {
			// Because OM is not available when starting AppDB, we read the version from the mapping
			// We plan to change this in the future, but for the sake of simplicity we leave it that way for the moment
			// It avoids unnecessary reconciles, race conditions...
			appdbOpts.AgentVersion, err = r.getAgentVersion(nil, opsManager.Spec.Version, true, log)
			if err != nil {
				log.Errorf("Impossible to get agent version, please override the agent image by providing a pod template")
				return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Failed to get agent version: %w. Please use spec.statefulSet to supply proper Agent version", err)), log)
			}
		}
	} else {
		appdbOpts.LegacyMonitoringAgent, err = r.getAgentVersion(nil, opsManager.Spec.Version, true, log)
		if err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error reading monitoring agent version: %w", err)), log, appDbStatusOption)
		}
	}

	workflowStatus := r.ensureTLSSecretAndCreatePEMIfNeeded(ctx, opsManager, log)
	if !workflowStatus.IsOK() {
		return r.updateStatus(ctx, opsManager, workflowStatus, log, appDbStatusOption)
	}

	if workflowStatus := r.replicateTLSCAConfigMap(ctx, opsManager, log); !workflowStatus.IsOK() {
		return r.updateStatus(ctx, opsManager, workflowStatus, log, appDbStatusOption)
	}

	if workflowStatus := r.replicateSSLMMSCAConfigMap(ctx, opsManager, &podVars, log); !workflowStatus.IsOK() {
		return r.updateStatus(ctx, opsManager, workflowStatus, log, appDbStatusOption)
	}

	var appdbSecretPath string
	if r.VaultClient != nil {
		appdbSecretPath = r.VaultClient.AppDBSecretPath()
	}
	tlsSecretName := opsManager.Spec.AppDB.GetSecurity().MemberCertificateSecretName(opsManager.Spec.AppDB.Name())
	certHash := enterprisepem.ReadHashFromSecret(ctx, r.SecretClient, opsManager.Namespace, tlsSecretName, appdbSecretPath, log)

	appdbOpts.CertHash = certHash

	var vaultConfig vault.VaultConfiguration
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
	}
	appdbOpts.VaultConfig = vaultConfig

	prometheusCertHash, err := certs.EnsureTLSCertsForPrometheus(ctx, r.SecretClient, opsManager.GetNamespace(), opsManager.Spec.AppDB.Prometheus, certs.AppDB, log)
	if err != nil {
		// Do not fail on errors generating certs for Prometheus
		log.Errorf("can't create a PEM-Format Secret for Prometheus certificates: %s", err)
	}
	appdbOpts.PrometheusTLSCertHash = prometheusCertHash

	allStatefulSetsExist, err := r.allStatefulSetsExist(ctx, opsManager, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("failed to check the state of all stateful sets: %w", err)), log, appDbStatusOption)
	}

	publishAutomationConfigFirst := r.publishAutomationConfigFirst(opsManager, allStatefulSetsExist, log)

	workflowStatus = workflow.RunInGivenOrder(publishAutomationConfigFirst,
		func() workflow.Status {
			return r.deployAutomationConfigAndWaitForAgentsReachGoalState(ctx, log, opsManager, allStatefulSetsExist, appdbOpts)
		},
		func() workflow.Status {
			return r.deployStatefulSet(ctx, opsManager, log, podVars, appdbOpts)
		},
	)

	if !workflowStatus.IsOK() {
		return r.updateStatus(ctx, opsManager, workflowStatus, log, appDbStatusOption)
	}

	// here it doesn't matter for which cluster we'll generate the name - only AppDB's MongoDB version is used there, which is the same in all clusters
	if err := annotations.UpdateLastAppliedMongoDBVersion(ctx, opsManager.GetVersionedImplForMemberCluster(r.getMemberClusterIndex(r.getNameOfFirstMemberCluster())), r.centralClient); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Could not save current state as an annotation: %w", err)), log, omStatusOption)
	}

	appDBScalers := []interfaces.AppDBScaler{}
	achievedDesiredScaling := true
	for _, member := range r.getAllMemberClusters() {
		scaler := scalers.GetAppDBScaler(opsManager, member.Name, r.getMemberClusterIndex(member.Name), r.memberClusters)
		appDBScalers = append(appDBScalers, scaler)
		replicasThisReconcile := scale.ReplicasThisReconciliation(scaler)
		specReplicas := opsManager.Spec.AppDB.GetMemberClusterSpecByName(member.Name).Members
		if opsManager.Spec.AppDB.IsMultiCluster() && replicasThisReconcile != specReplicas {
			achievedDesiredScaling = false
		}
		log.Debugf("Scaling status for memberCluster: %s, replicasThisReconcile=%d, specReplicas=%d, achievedDesiredScaling=%t", member.Name, replicasThisReconcile, specReplicas, achievedDesiredScaling)
	}

	if err := r.updateLastAppliedMemberSpec(ctx, opsManager.Spec.AppDB, r.currentClusterSpecs, log); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Could not save last applied member spec: %w", err)), log, omStatusOption)
	}

	if podVars.ProjectID == "" {
		// this doesn't requeue the reconciliation immediately, the calling OM controller
		// requeues after Ops Manager has been fully configured.
		log.Infof("Requeuing reconciliation to configure Monitoring in Ops Manager.")

		return r.updateStatus(ctx, opsManager, workflow.Pending("Enabling monitoring").Requeue(), log, appDbStatusOption, status.AppDBMemberOptions(appDBScalers...))
	}

	// We need to check for status compared to the spec because the scaler will report desired replicas to be different than what's present in the spec when the
	// reconciler is not handling that specific cluster.
	rsScalers := []scale.ReplicaSetScaler{}
	for _, scaler := range appDBScalers {
		rsScaler := scaler.(scale.ReplicaSetScaler)
		rsScalers = append(rsScalers, rsScaler)
	}

	if !achievedDesiredScaling || scale.AnyAreStillScaling(rsScalers...) {
		return r.updateStatus(ctx, opsManager, workflow.Pending("Continuing scaling operation on AppDB %d", 1), log, appDbStatusOption, status.AppDBMemberOptions(appDBScalers...))
	}

	// set the annotation to AppDB that forced reconfigure is performed to indicate to customers
	if opsManager.Annotations == nil {
		opsManager.Annotations = map[string]string{}
	}
	if val, ok := opsManager.Annotations[ForceReconfigureAnnotation]; ok && val == "true" {
		annotationsToAdd := map[string]string{ForcedReconfigureAlreadyPerformedAnnotation: timeutil.Now()}

		err := annotations.SetAnnotations(ctx, opsManager, annotationsToAdd, r.client)
		if err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Failed to save force reconfigure annotation err: %s", err)), log, omStatusOption)
		}
	}

	log.Infof("Finished reconciliation for AppDB ReplicaSet!")

	return r.updateStatus(ctx, opsManager, workflow.OK(), log, appDbStatusOption, status.AppDBMemberOptions(appDBScalers...))
}

func (r *ReconcileAppDbReplicaSet) getNameOfFirstMemberCluster() string {
	firstMemberClusterName := ""
	for _, memberCluster := range r.getHealthyMemberClusters() {
		if memberCluster.Active {
			firstMemberClusterName = memberCluster.Name
			break
		}
	}
	return firstMemberClusterName
}

func (r *ReconcileAppDbReplicaSet) deployAutomationConfigAndWaitForAgentsReachGoalState(ctx context.Context, log *zap.SugaredLogger, opsManager *omv1.MongoDBOpsManager, allStatefulSetsExist bool, appdbOpts construct.AppDBStatefulSetOptions) workflow.Status {
	configVersion, workflowStatus := r.deployAutomationConfigOnHealthyClusters(ctx, log, opsManager, appdbOpts)
	if !workflowStatus.IsOK() {
		return workflowStatus
	}
	if !allStatefulSetsExist {
		log.Infof("Skipping waiting for all agents to reach the goal state because not all stateful sets are created yet.")
		return workflow.OK()
	}
	// We have to separate automation config deployment from agent goal checks.
	// Waiting for agents' goal state without updating config in other clusters could end up with a deadlock situation.
	return r.allAgentsReachedGoalState(ctx, opsManager, configVersion, log)
}

func (r *ReconcileAppDbReplicaSet) deployAutomationConfigOnHealthyClusters(ctx context.Context, log *zap.SugaredLogger, opsManager *omv1.MongoDBOpsManager, appdbOpts construct.AppDBStatefulSetOptions) (int, workflow.Status) {
	configVersions := map[int]struct{}{}
	for _, memberCluster := range r.getHealthyMemberClusters() {
		if configVersion, workflowStatus := r.deployAutomationConfig(ctx, opsManager, appdbOpts.PrometheusTLSCertHash, memberCluster.Name, log); !workflowStatus.IsOK() {
			return 0, workflowStatus
		} else {
			log.Infof("Deployed Automation Config version: %d in cluster: %s", configVersion, memberCluster.Name)
			configVersions[configVersion] = struct{}{}
		}
	}

	if len(configVersions) > 1 {
		// automation config versions have diverged on different clusters, we need to align them.
		// they potentially can diverge, because the version is determined at the time when the secret is published.
		// We create ac with our builder and increment version, but then the config is compared with the one read from secret
		// if they are equal (ignoring version), then the version from the secret is chosen.
		// TODO CLOUDP-179139
		return 0, workflow.Failed(xerrors.Errorf("Automation config versions have diverged: %+v", configVersions))
	}

	// at this point there is exactly one "configVersion", so we just return it
	for configVersion := range configVersions {
		return configVersion, workflow.OK()
	}

	// shouldn't happen because we should always have at least one member cluster
	return 0, workflow.Failed(xerrors.Errorf("Failed to deploy automation configs"))
}

func getMultiClusterAppDBService(appdb omv1.AppDBSpec, clusterNum int, podNum int) corev1.Service {
	svcLabels := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(appdb.Name(), clusterNum, podNum),
		"controller":                         "mongodb-enterprise-operator",
	}
	labelSelectors := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(appdb.Name(), clusterNum, podNum),
		"controller":                         "mongodb-enterprise-operator",
	}
	additionalConfig := appdb.GetAdditionalMongodConfig()
	port := additionalConfig.GetPortOrDefault()
	svc := service.Builder().
		SetName(dns.GetMultiServiceName(appdb.Name(), clusterNum, podNum)).
		SetNamespace(appdb.Namespace).
		SetSelector(labelSelectors).
		SetLabels(svcLabels).
		SetPublishNotReadyAddresses(true).
		AddPort(&corev1.ServicePort{Port: port, Name: "mongodb"}).
		Build()
	return svc
}

func (r *ReconcileAppDbReplicaSet) publishAutomationConfigFirst(opsManager *omv1.MongoDBOpsManager, allStatefulSetsExist bool, log *zap.SugaredLogger) bool {
	automationConfigFirst := true

	// The only case when we push the StatefulSet first is when we are ensuring TLS for the already existing AppDB
	// TODO this feels insufficient. Shouldn't we check if there is actual change in TLS settings requiring to push sts first? Now it will always publish sts first when TLS enabled
	if allStatefulSetsExist && opsManager.Spec.AppDB.GetSecurity().IsTLSEnabled() {
		automationConfigFirst = false
	}

	if opsManager.IsChangingVersion() {
		log.Info("Version change in progress, the StatefulSet must be updated first")
		automationConfigFirst = false
	}
	// if we are performing a force reconfigure we should change the automation config first
	if shouldPerformForcedReconfigure(opsManager.Annotations) {
		automationConfigFirst = true
	}

	return automationConfigFirst
}

func getDomain(service, namespace, clusterName string) string {
	if clusterName == "" {
		clusterName = "cluster.local"
	}
	return fmt.Sprintf("%s.%s.svc.%s", service, namespace, clusterName)
}

// ensureTLSSecretAndCreatePEMIfNeeded checks that the needed TLS secrets are present, and creates the concatenated PEM if needed.
// This means that the secret referenced can either already contain a concatenation of certificate and private key
// or it can be of type kubernetes.io/tls. In this case the operator will read the tls.crt and tls.key entries, and it will
// generate a new secret containing their concatenation
func (r *ReconcileAppDbReplicaSet) ensureTLSSecretAndCreatePEMIfNeeded(ctx context.Context, om *omv1.MongoDBOpsManager, log *zap.SugaredLogger) workflow.Status {
	rs := om.Spec.AppDB
	if !rs.IsSecurityTLSConfigEnabled() {
		return workflow.OK()
	}
	secretName := rs.Security.MemberCertificateSecretName(rs.Name())

	needToCreatePEM := false
	var err error
	var secretData map[string][]byte
	var s corev1.Secret

	if vault.IsVaultSecretBackend() {
		needToCreatePEM = true
		path := fmt.Sprintf("%s/%s/%s", r.VaultClient.AppDBSecretPath(), om.Namespace, secretName)
		secretData, err = r.VaultClient.ReadSecretBytes(path)
		if err != nil {
			return workflow.Failed(xerrors.Errorf("can't read current certificate secret from vault: %w", err))
		}
	} else {
		s, err = r.KubeClient.GetSecret(ctx, kube.ObjectKey(om.Namespace, secretName))
		if err != nil {
			return workflow.Failed(xerrors.Errorf("can't read current certificate secret %s: %w", secretName, err))
		}

		// SecretTypeTLS is kubernetes.io/tls
		// This is the standard way in K8S to have secrets that hold TLS certs
		// And it is the one generated by cert manager
		// These type of secrets contain tls.crt and tls.key entries
		if s.Type == corev1.SecretTypeTLS {
			needToCreatePEM = true
			secretData = s.Data
		}
	}

	if needToCreatePEM {
		var data string
		for _, memberCluster := range r.getHealthyMemberClusters() {
			if om.Spec.AppDB.IsMultiCluster() {
				data, err = certs.VerifyTLSSecretForStatefulSet(secretData, certs.AppDBMultiClusterReplicaSetConfig(om, scalers.GetAppDBScaler(om, memberCluster.Name, r.getMemberClusterIndex(memberCluster.Name), r.memberClusters)))
			} else {
				data, err = certs.VerifyTLSSecretForStatefulSet(secretData, certs.AppDBReplicaSetConfig(om))
			}
			if err != nil {
				return workflow.Failed(xerrors.Errorf("certificate for appdb is not valid: %w", err))
			}
		}

		var appdbSecretPath string
		if r.VaultClient != nil {
			appdbSecretPath = r.VaultClient.AppDBSecretPath()
		}

		secretHash := enterprisepem.ReadHashFromSecret(ctx, r.SecretClient, om.Namespace, secretName, appdbSecretPath, log)

		var errs error
		for _, memberCluster := range r.getHealthyMemberClusters() {
			err = certs.CreatePEMSecretClient(ctx, memberCluster.SecretClient, kube.ObjectKey(om.Namespace, secretName), map[string]string{secretHash: data}, nil, certs.AppDB)
			if err != nil {
				errs = multierror.Append(errs, xerrors.Errorf("can't create concatenated PEM certificate in cluster %s: %w", memberCluster.Name, err))
				continue
			}
		}
		if errs != nil {
			return workflow.Failed(errs)
		}
	}

	return workflow.OK()
}

func (r *ReconcileAppDbReplicaSet) replicateTLSCAConfigMap(ctx context.Context, om *omv1.MongoDBOpsManager, log *zap.SugaredLogger) workflow.Status {
	appDBSpec := om.Spec.AppDB
	if !appDBSpec.IsMultiCluster() || !appDBSpec.IsSecurityTLSConfigEnabled() {
		return workflow.OK()
	}

	caConfigMapName := construct.CAConfigMapName(om.Spec.AppDB, log)

	cm, err := r.client.GetConfigMap(ctx, kube.ObjectKey(appDBSpec.Namespace, caConfigMapName))
	if err != nil {
		return workflow.Failed(xerrors.Errorf("Expected CA ConfigMap not found on central cluster: %s", caConfigMapName))
	}

	for _, memberCluster := range r.getHealthyMemberClusters() {
		memberCm := configmap.Builder().SetName(caConfigMapName).SetNamespace(appDBSpec.Namespace).SetData(cm.Data).Build()
		err = configmap.CreateOrUpdate(ctx, memberCluster.Client, memberCm)

		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return workflow.Failed(xerrors.Errorf("Failed to sync CA ConfigMap in cluster: %s, err: %w", memberCluster.Name, err))
		}
	}

	return workflow.OK()
}

func (r *ReconcileAppDbReplicaSet) replicateSSLMMSCAConfigMap(ctx context.Context, om *omv1.MongoDBOpsManager, podVars *env.PodEnvVars, log *zap.SugaredLogger) workflow.Status {
	appDBSpec := om.Spec.AppDB
	if !appDBSpec.IsMultiCluster() || !construct.ShouldMountSSLMMSCAConfigMap(podVars) {
		log.Debug("Skipping replication of SSLMMSCAConfigMap.")
		return workflow.OK()
	}

	caConfigMapName := podVars.SSLMMSCAConfigMap

	cm, err := r.client.GetConfigMap(ctx, kube.ObjectKey(appDBSpec.Namespace, caConfigMapName))
	if err != nil {
		return workflow.Failed(xerrors.Errorf("Expected SSLMMSCAConfigMap not found on central cluster: %s", caConfigMapName))
	}

	for _, memberCluster := range r.getHealthyMemberClusters() {
		memberCm := configmap.Builder().SetName(caConfigMapName).SetNamespace(appDBSpec.Namespace).SetData(cm.Data).Build()
		err = configmap.CreateOrUpdate(ctx, memberCluster.Client, memberCm)

		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return workflow.Failed(xerrors.Errorf("Failed to sync SSLMMSCAConfigMap in cluster: %s, err: %w", memberCluster.Name, err))
		}
	}

	return workflow.OK()
}

// publishAutomationConfig publishes the automation config to the Secret if necessary. Note that it's done only
// if the automation config has changed - the version is incremented in this case.
// Method returns the version of the automation config.
// No optimistic concurrency control is done - there cannot be a concurrent reconciliation for the same Ops Manager
// object and the probability that the user will edit the config map manually in the same time is extremely low
// returns the version of AutomationConfig just published
func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, automationConfig automationconfig.AutomationConfig, secretName string, memberClusterName string) (int, error) {
	ac, err := automationconfig.EnsureSecret(ctx, r.getMemberCluster(memberClusterName).SecretClient, kube.ObjectKey(opsManager.Namespace, secretName), nil, automationConfig)
	if err != nil {
		return -1, err
	}
	return ac.Version, err
}

// getExistingAutomationConfig retrieves the existing automation config from the member clusters.
// This method retrieves the most recent automation config version to handle the case when adding a new cluster from scratch.
// This is required to avoid a situation where adding a new cluster assumes the automation is created from scratch.
func (r *ReconcileAppDbReplicaSet) getExistingAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, secretName string) (automationconfig.AutomationConfig, error) {
	latestVersion := -1
	latestAc := automationconfig.AutomationConfig{}
	for _, memberCluster := range r.getHealthyMemberClusters() {
		ac, err := automationconfig.ReadFromSecret(ctx, memberCluster.Client, types.NamespacedName{Name: secretName, Namespace: opsManager.Namespace})
		if err != nil {
			return automationconfig.AutomationConfig{}, err
		}
		if ac.Version > latestVersion {
			latestVersion = ac.Version
			latestAc = ac
		}
	}
	return latestAc, nil
}

func (r *ReconcileAppDbReplicaSet) buildAppDbAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, acType agentType, prometheusCertHash string, memberClusterName string, log *zap.SugaredLogger) (automationconfig.AutomationConfig, error) {
	rs := opsManager.Spec.AppDB
	domain := getDomain(rs.ServiceName(), opsManager.Namespace, opsManager.Spec.GetClusterDomain())

	auth := automationconfig.Auth{}
	appDBConfigurable := omv1.AppDBConfigurable{AppDBSpec: rs, OpsManager: *opsManager}

	if err := scram.Enable(ctx, &auth, r.SecretClient, &appDBConfigurable); err != nil {
		return automationconfig.AutomationConfig{}, err
	}

	// the existing automation config is required as we compare it against what we build to determine
	// if we need to increment the version.
	secretName := rs.AutomationConfigSecretName()
	if acType == monitoring {
		secretName = rs.MonitoringAutomationConfigSecretName()
	}

	existingAutomationConfig, err := r.getExistingAutomationConfig(ctx, opsManager, secretName)
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}

	fcVersion := ""
	if rs.FeatureCompatibilityVersion != nil {
		fcVersion = *rs.FeatureCompatibilityVersion
	}

	tlsSecretName := opsManager.Spec.AppDB.GetSecurity().MemberCertificateSecretName(opsManager.Spec.AppDB.Name())
	var appdbSecretPath string
	if r.VaultClient != nil {
		appdbSecretPath = r.VaultClient.AppDBSecretPath()
	}
	certHash := enterprisepem.ReadHashFromSecret(ctx, r.SecretClient, opsManager.Namespace, tlsSecretName, appdbSecretPath, log)

	prometheusModification := automationconfig.NOOP()
	if acType == automation {
		// There are 2 agents running in the AppDB Pods, we will configure Prometheus
		// only on the Automation Agent.
		prometheusModification, err = buildPrometheusModification(ctx, r.SecretClient, opsManager, prometheusCertHash)
		if err != nil {
			log.Errorf("Could not enable Prometheus: %s", err)
		}

	}

	processList := r.generateProcessList(opsManager)
	existingAutomationMembers, nextId := getExistingAutomationReplicaSetMembers(existingAutomationConfig)
	memberOptions := r.generateMemberOptions(opsManager, existingAutomationMembers)
	replicasThisReconciliation := 0
	// we want to use all member clusters to maintain the same process list despite having some clusters down
	for _, memberCluster := range r.getAllMemberClusters() {
		replicasThisReconciliation += scale.ReplicasThisReconciliation(scalers.GetAppDBScaler(opsManager, memberCluster.Name, memberCluster.Index, r.memberClusters))
	}

	builder := automationconfig.NewBuilder().
		SetTopology(automationconfig.ReplicaSetTopology).
		SetMemberOptions(memberOptions).
		SetMembers(replicasThisReconciliation).
		SetName(rs.Name()).
		SetDomain(domain).
		SetAuth(auth).
		SetFCV(fcVersion).
		AddVersions(existingAutomationConfig.Versions).
		IsEnterprise(construct.IsEnterprise()).
		SetMongoDBVersion(rs.GetMongoDBVersion(nil)).
		SetOptions(automationconfig.Options{DownloadBase: util.AgentDownloadsDir}).
		SetPreviousAutomationConfig(existingAutomationConfig).
		SetTLSConfig(
			automationconfig.TLS{
				CAFilePath:            appdbCAFilePath,
				ClientCertificateMode: automationconfig.ClientCertificateModeOptional,
			}).
		AddProcessModification(func(i int, p *automationconfig.Process) {
			p.Name = processList[i].Name
			p.HostName = processList[i].HostName

			p.AuthSchemaVersion = om.CalculateAuthSchemaVersion(rs.GetMongoDBVersion(nil))
			p.Args26 = objx.New(rs.AdditionalMongodConfig.ToMap())
			p.SetPort(int(rs.AdditionalMongodConfig.GetPortOrDefault()))
			p.SetReplicaSetName(rs.Name())
			p.SetStoragePath(automationconfig.DefaultMongoDBDataDir)
			if rs.Security.IsTLSEnabled() {

				certFileName := certHash
				if certFileName == "" {
					certFileName = fmt.Sprintf("%s-pem", p.Name)
				}
				certFile := fmt.Sprintf("%s/certs/%s", util.SecretVolumeMountPath, certFileName)

				p.Args26.Set("net.tls.mode", string(tls.Require))

				p.Args26.Set("net.tls.certificateKeyFile", certFile)

			}
			systemLog := &automationconfig.SystemLog{
				Destination: automationconfig.File,
				Path:        path.Join(util.PvcMountPathLogs, "mongodb.log"),
			}

			if opsManager.Spec.AppDB.AutomationAgent.SystemLog != nil {
				systemLog = opsManager.Spec.AppDB.AutomationAgent.SystemLog
			}

			// This setting takes precedence, above has been deprecated, and we should favor the one after mongod
			if opsManager.Spec.AppDB.AutomationAgent.Mongod.SystemLog != nil {
				systemLog = opsManager.Spec.AppDB.AutomationAgent.Mongod.SystemLog
			}

			if acType == automation {
				if opsManager.Spec.AppDB.AutomationAgent.Mongod.HasLoggingConfigured() {
					automationconfig.ConfigureAgentConfiguration(systemLog, opsManager.Spec.AppDB.AutomationAgent.Mongod.LogRotate, opsManager.Spec.AppDB.AutomationAgent.Mongod.AuditLogRotate, p)
				} else {
					automationconfig.ConfigureAgentConfiguration(systemLog, opsManager.Spec.AppDB.AutomationAgent.LogRotate, opsManager.Spec.AppDB.AutomationAgent.Mongod.AuditLogRotate, p)
				}
			}
		}).
		AddModifications(func(automationConfig *automationconfig.AutomationConfig) {
			if acType == monitoring {
				addMonitoring(automationConfig, log, rs.GetSecurity().IsTLSEnabled())
				automationConfig.ReplicaSets = []automationconfig.ReplicaSet{}
				automationConfig.Processes = []automationconfig.Process{}
			}
			setBaseUrlForAgents(automationConfig, opsManager.CentralURL())
		}).
		AddModifications(func(automationConfig *automationconfig.AutomationConfig) {
			if len(automationConfig.ReplicaSets) == 1 {
				for idx, member := range automationConfig.ReplicaSets[0].Members {
					if existingMember, ok := existingAutomationMembers[member.Host]; ok {
						automationConfig.ReplicaSets[0].Members[idx].Id = existingMember.Id
					} else {
						automationConfig.ReplicaSets[0].Members[idx].Id = nextId
						nextId = nextId + 1
					}
				}
			}
		}).
		AddModifications(prometheusModification)

	if opsManager.Spec.AppDB.IsMultiCluster() {
		builder.SetDomain(fmt.Sprintf("%s.svc.%s", opsManager.Namespace, opsManager.Spec.GetClusterDomain()))
	}
	ac, err := builder.Build()
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}

	if acType == automation && opsManager.Spec.AppDB.AutomationConfigOverride != nil {
		acToMerge := mdbcv1_controllers.OverrideToAutomationConfig(*opsManager.Spec.AppDB.AutomationConfigOverride)
		ac = merge.AutomationConfigs(ac, acToMerge)
	}

	// this is for logging automation config, ignoring monitoring as it doesn't contain any processes)
	if acType == automation {
		processHostnames := util.Transform(ac.Processes, func(obj automationconfig.Process) string {
			return obj.HostName
		})

		var replicaSetMembers []string
		if len(ac.ReplicaSets) > 0 {
			replicaSetMembers = util.Transform(ac.ReplicaSets[0].Members, func(member automationconfig.ReplicaSetMember) string {
				return fmt.Sprintf("{Id=%d, Host=%s}", member.Id, member.Host)
			})
		}
		log.Debugf("Created automation config object (in-memory) for cluster=%s, total process count=%d, process hostnames=%+v, replicaset config=%+v", memberClusterName, replicasThisReconciliation, processHostnames, replicaSetMembers)
	}

	// this is for force reconfigure. This sets "currentVersion: -1" in automation config
	// when forceReconfig is triggered.
	if acType == automation {
		if shouldPerformForcedReconfigure(opsManager.Annotations) {
			log.Debug("Performing forced reconfigure of AppDB")
			builder.SetForceReconfigureToVersion(-1)

			ac, err = builder.Build()
			if err != nil {
				log.Errorf("failed to build AC: %w", err)
				return ac, err
			}
		}
	}

	return ac, nil
}

// shouldPerformForcedReconfigure checks whether forced reconfigure of the automation config needs to be performed or not
// it checks this with the user provided annotation and if the operator has actually performed a force reconfigure already
func shouldPerformForcedReconfigure(annotations map[string]string) bool {
	if val, ok := annotations[ForceReconfigureAnnotation]; ok {
		if val == "true" {
			if _, ok := annotations[ForcedReconfigureAlreadyPerformedAnnotation]; !ok {
				return true
			}
		}
	}
	return false
}

func getExistingAutomationReplicaSetMembers(automationConfig automationconfig.AutomationConfig) (map[string]automationconfig.ReplicaSetMember, int) {
	nextId := 0
	existingMembers := map[string]automationconfig.ReplicaSetMember{}
	if len(automationConfig.ReplicaSets) != 1 {
		return existingMembers, nextId
	}
	for _, member := range automationConfig.ReplicaSets[0].Members {
		existingMembers[member.Host] = member
		if member.Id >= nextId {
			nextId = member.Id + 1
		}
	}
	return existingMembers, nextId
}

func (r *ReconcileAppDbReplicaSet) generateProcessHostnames(opsManager *omv1.MongoDBOpsManager, memberCluster multicluster.MemberCluster) []string {
	members := scale.ReplicasThisReconciliation(scalers.GetAppDBScaler(opsManager, memberCluster.Name, r.getMemberClusterIndex(memberCluster.Name), r.memberClusters))
	var hostnames []string
	if opsManager.Spec.AppDB.IsMultiCluster() {
		hostnames = dns.GetMultiClusterProcessHostnames(opsManager.Spec.AppDB.GetName(), opsManager.GetNamespace(), memberCluster.Index, members, opsManager.Spec.GetClusterDomain(), nil)
	} else {
		hostnames, _ = dns.GetDNSNames(opsManager.Spec.AppDB.GetName(), opsManager.Spec.AppDB.ServiceName(), opsManager.GetNamespace(), opsManager.Spec.GetClusterDomain(), members, nil)
	}
	return hostnames
}

func (r *ReconcileAppDbReplicaSet) generateProcessList(opsManager *omv1.MongoDBOpsManager) []automationconfig.Process {
	var processList []automationconfig.Process
	// We want all clusters to generate stable process list in case of some clusters being down. Process list cannot change regardless of the cluster health.
	for _, memberCluster := range r.getAllMemberClusters() {
		hostnames := r.generateProcessHostnames(opsManager, memberCluster)
		for idx, hostname := range hostnames {
			process := automationconfig.Process{
				Name:     fmt.Sprintf("%s-%d", opsManager.Spec.AppDB.NameForCluster(memberCluster.Index), idx),
				HostName: hostname,
			}
			processList = append(processList, process)
		}
	}
	return processList
}

func (r *ReconcileAppDbReplicaSet) generateMemberOptions(opsManager *omv1.MongoDBOpsManager, previousMembers map[string]automationconfig.ReplicaSetMember) []automationconfig.MemberOptions {
	var memberOptionsList []automationconfig.MemberOptions
	for _, memberCluster := range r.getAllMemberClusters() {
		hostnames := r.generateProcessHostnames(opsManager, memberCluster)
		memberConfig := make([]automationconfig.MemberOptions, 0)
		if memberCluster.Active {
			memberConfigForCluster := opsManager.Spec.AppDB.GetMemberClusterSpecByName(memberCluster.Name).MemberConfig
			if memberConfigForCluster != nil {
				memberConfig = append(memberConfig, memberConfigForCluster...)
			}
		}
		for idx, hostname := range hostnames {
			memberOptions := automationconfig.MemberOptions{}
			if idx < len(memberConfig) { // There are member options configured in the spec
				memberOptions.Votes = memberConfig[idx].Votes
				memberOptions.Priority = memberConfig[idx].Priority
				memberOptions.Tags = memberConfig[idx].Tags
			} else {
				// There are three cases we might not have memberOptions in spec:
				//   1. user never specified member config in the spec
				//   2. user scaled down members e.g. from 5 to 2 removing memberConfig elements at the same time
				//   3. user removed whole clusterSpecItem from the list (removing cluster entirely)
				// For 2. and 3. we should have those members in existing AC
				if replicaSetMember, ok := previousMembers[hostname]; ok {
					memberOptions.Votes = replicaSetMember.Votes
					if replicaSetMember.Priority != nil {
						memberOptions.Priority = ptr.To(fmt.Sprintf("%f", *replicaSetMember.Priority))
					}
					memberOptions.Tags = replicaSetMember.Tags

				} else {
					// If the member does not exist in the previous automation config, we populate the member options with defaults
					memberOptions.Votes = ptr.To(1)
					memberOptions.Priority = ptr.To("1.0")
				}
			}
			memberOptionsList = append(memberOptionsList, memberOptions)
		}

	}
	return memberOptionsList
}

func (r *ReconcileAppDbReplicaSet) generateHeadlessHostnamesForMonitoring(opsManager *omv1.MongoDBOpsManager) []string {
	var hostnames []string
	// We want all clusters to generate stable process list in case of some clusters being down. Process list cannot change regardless of the cluster health.
	for _, memberCluster := range r.getAllMemberClusters() {
		members := scale.ReplicasThisReconciliation(scalers.GetAppDBScaler(opsManager, memberCluster.Name, r.getMemberClusterIndex(memberCluster.Name), r.memberClusters))
		if opsManager.Spec.AppDB.IsMultiCluster() {
			hostnames = append(hostnames, dns.GetMultiClusterHostnamesForMonitoring(opsManager.Spec.AppDB.GetName(), opsManager.GetNamespace(), memberCluster.Index, members)...)
		} else {
			dnsHostnames, _ := dns.GetDNSNames(opsManager.Spec.AppDB.GetName(), opsManager.Spec.AppDB.ServiceName(), opsManager.GetNamespace(), opsManager.Spec.GetClusterDomain(), members, nil)
			hostnames = append(hostnames, dnsHostnames...)
		}
	}
	return hostnames
}

// buildPrometheusModification returns a `Modification` function that will add a
// `prometheus` entry to the Automation Config if Prometheus has been enabled on
// the Application Database (`spec.applicationDatabase.Prometheus`).
func buildPrometheusModification(ctx context.Context, sClient secrets.SecretClient, om *omv1.MongoDBOpsManager, prometheusCertHash string) (automationconfig.Modification, error) {
	if om.Spec.AppDB.Prometheus == nil {
		return automationconfig.NOOP(), nil
	}

	prom := om.Spec.AppDB.Prometheus

	var err error
	var password string
	prometheus := om.Spec.AppDB.Prometheus

	secretName := prometheus.PasswordSecretRef.Name
	if vault.IsVaultSecretBackend() {
		operatorSecretPath := sClient.VaultClient.OperatorSecretPath()
		passwordString := fmt.Sprintf("%s/%s/%s", operatorSecretPath, om.GetNamespace(), secretName)
		keyedPassword, err := sClient.VaultClient.ReadSecretString(passwordString)
		if err != nil {
			return automationconfig.NOOP(), err
		}

		var ok bool
		password, ok = keyedPassword[prometheus.GetPasswordKey()]
		if !ok {
			errMsg := fmt.Sprintf("Prometheus password %s not in Secret %s", prometheus.GetPasswordKey(), passwordString)
			return automationconfig.NOOP(), xerrors.Errorf(errMsg)
		}
	} else {
		secretNamespacedName := types.NamespacedName{Name: secretName, Namespace: om.Namespace}
		password, err = secret.ReadKey(ctx, sClient, prometheus.GetPasswordKey(), secretNamespacedName)
		if err != nil {
			return automationconfig.NOOP(), err
		}
	}

	return func(config *automationconfig.AutomationConfig) {
		promConfig := automationconfig.NewDefaultPrometheus(prom.Username)

		if prometheusCertHash != "" {
			promConfig.TLSPemPath = util.SecretVolumeMountPathPrometheus + "/" + prometheusCertHash
			promConfig.Scheme = "https"
		} else {
			promConfig.Scheme = "http"
		}

		promConfig.Password = password

		if prom.Port > 0 {
			promConfig.ListenAddress = fmt.Sprintf("%s:%d", mdbcv1_controllers.ListenAddress, prom.Port)
		}

		if prom.MetricsPath != "" {
			promConfig.MetricsPath = prom.MetricsPath
		}

		config.Prometheus = &promConfig
	}, nil
}

// setBaseUrlForAgents will update the baseUrl for all backup and monitoring versions to the provided url.
func setBaseUrlForAgents(ac *automationconfig.AutomationConfig, url string) {
	for i := range ac.MonitoringVersions {
		ac.MonitoringVersions[i].BaseUrl = url
	}
	for i := range ac.BackupVersions {
		ac.BackupVersions[i].BaseUrl = url
	}
}

func addMonitoring(ac *automationconfig.AutomationConfig, log *zap.SugaredLogger, tls bool) {
	if len(ac.Processes) == 0 {
		return
	}
	monitoringVersions := ac.MonitoringVersions
	for _, p := range ac.Processes {
		found := false
		for _, m := range monitoringVersions {
			if m.Hostname == p.HostName {
				found = true
				break
			}
		}
		if !found {
			monitoringVersion := automationconfig.MonitoringVersion{
				Hostname: p.HostName,
				Name:     om.MonitoringAgentDefaultVersion,
			}
			if tls {
				additionalParams := map[string]string{
					"useSslForAllConnections":      "true",
					"sslTrustedServerCertificates": appdbCAFilePath,
				}
				pemKeyFile := p.Args26.Get("net.tls.certificateKeyFile")
				if pemKeyFile != nil {
					additionalParams["sslClientCertificate"] = pemKeyFile.String()
				}
				monitoringVersion.AdditionalParams = additionalParams
			}
			log.Debugw("Added monitoring agent configuration", "host", p.HostName, "tls", tls)
			monitoringVersions = append(monitoringVersions, monitoringVersion)
		}
	}
	ac.MonitoringVersions = monitoringVersions
}

// registerAppDBHostsWithProject uses the Hosts API to add each process in the AppDB to the project
func (r *ReconcileAppDbReplicaSet) registerAppDBHostsWithProject(hostnames []string, conn om.Connection, opsManagerPassword string, log *zap.SugaredLogger) error {
	getHostsResult, err := conn.GetHosts()
	if err != nil {
		return xerrors.Errorf("error fetching existing hosts: %w", err)
	}

	hostMap := make(map[string]host.Host)
	for _, host := range getHostsResult.Results {
		hostMap[host.Hostname] = host
	}

	for _, hostname := range hostnames {
		appDbHost := host.Host{
			Port:              util.MongoDbDefaultPort,
			Username:          util.OpsManagerMongoDBUserName,
			Password:          opsManagerPassword,
			Hostname:          hostname,
			AuthMechanismName: "MONGODB_CR",
		}

		if currentHost, ok := hostMap[hostname]; ok {
			// Host is already on the list, we need to update it.
			log.Debugf("Host %s is already registred with group %s", hostname, conn.GroupID())
			// Need to se the Id first
			appDbHost.Id = currentHost.Id

			if err := conn.UpdateHost(appDbHost); err != nil {
				return xerrors.Errorf("error updating appdb host %w", err)
			}
		} else {
			// This is a new host.
			log.Debugf("Registering AppDB host %s with project %s", hostname, conn.GroupID())
			if err := conn.AddHost(appDbHost); err != nil {
				return xerrors.Errorf("*** error adding appdb host %w", err)
			}
		}
	}
	return nil
}

func (r *ReconcileAppDbReplicaSet) generatePasswordAndCreateSecret(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	// create the password
	password, err := generate.RandomFixedLengthStringOfSize(12)
	if err != nil {
		return "", err
	}

	passwordData := map[string]string{
		util.OpsManagerPasswordKey: password,
	}

	secretObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())

	log.Infof("Creating mongodb-ops-manager password in secret/%s in namespace %s", secretObjectKey.Name, secretObjectKey.Namespace)

	appDbPasswordSecret := secret.Builder().
		SetName(secretObjectKey.Name).
		SetNamespace(secretObjectKey.Namespace).
		SetStringMapToData(passwordData).
		SetOwnerReferences(kube.BaseOwnerReference(opsManager)).
		Build()

	if err := r.CreateSecret(ctx, appDbPasswordSecret); err != nil {
		return "", err
	}

	return password, nil
}

// ensureAppDbPassword will return the password that was specified by the user, or the auto generated password stored in
// the secret (generate it and store in secret otherwise)
func (r *ReconcileAppDbReplicaSet) ensureAppDbPassword(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	passwordRef := opsManager.Spec.AppDB.PasswordSecretKeyRef
	if passwordRef != nil && passwordRef.Name != "" { // there is a secret specified for the Ops Manager user
		if passwordRef.Key == "" {
			passwordRef.Key = "password"
		}
		password, err := secret.ReadKey(ctx, r.SecretClient, passwordRef.Key, kube.ObjectKey(opsManager.Namespace, passwordRef.Name))
		if err != nil {
			if secrets.SecretNotExist(err) {
				log.Debugf("Generated AppDB password and storing in secret/%s", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
				return r.generatePasswordAndCreateSecret(ctx, opsManager, log)
			}
			return "", err
		}

		log.Debugf("Reading password from secret/%s", passwordRef.Name)

		// watch for any changes on the user provided password
		r.resourceWatcher.AddWatchedResourceIfNotAdded(
			passwordRef.Name,
			opsManager.Namespace,
			watch.Secret,
			kube.ObjectKeyFromApiObject(opsManager),
		)

		// delete the auto generated password, we don't need it anymore. We can just generate a new one if
		// the user password is deleted
		log.Debugf("Deleting Operator managed password secret/%s from namespace %s", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName(), opsManager.Namespace)
		if err := r.DeleteSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())); err != nil && !secrets.SecretNotExist(err) {
			return "", err
		}
		return password, nil
	}

	// otherwise we'll ensure the auto generated password exists
	secretObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
	appDbPasswordSecretStringData, err := secret.ReadStringData(ctx, r.SecretClient, secretObjectKey)

	if secrets.SecretNotExist(err) {
		// create the password
		if password, err := r.generatePasswordAndCreateSecret(ctx, opsManager, log); err != nil {
			return "", err
		} else {
			log.Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
			return password, nil
		}
	} else if err != nil {
		// any other error
		return "", err
	}
	log.
		Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
	return appDbPasswordSecretStringData[util.OpsManagerPasswordKey], nil
}

// ensureAppDbAgentApiKey makes sure there is an agent API key for the AppDB automation agent
func (r *ReconcileAppDbReplicaSet) ensureAppDbAgentApiKey(ctx context.Context, opsManager *omv1.MongoDBOpsManager, conn om.Connection, projectID string, log *zap.SugaredLogger) error {
	var appdbSecretPath string
	if r.VaultClient != nil {
		appdbSecretPath = r.VaultClient.AppDBSecretPath()
	}

	agentKey := ""
	for _, memberCluster := range r.getHealthyMemberClusters() {
		if agentKeyFromSecret, err := agents.EnsureAgentKeySecretExists(ctx, memberCluster.SecretClient, conn, opsManager.Namespace, agentKey, projectID, appdbSecretPath, log); err != nil {
			return xerrors.Errorf("error ensuring agent key secret exists in cluster %s: %w", memberCluster.Name, err)
		} else if agentKey == "" {
			agentKey = agentKeyFromSecret
		}
	}

	return nil
}

// tryConfigureMonitoringInOpsManager attempts to configure monitoring in Ops Manager. This might not be possible if Ops Manager
// has not been created yet, if that is the case, an empty PodVars will be returned.
func (r *ReconcileAppDbReplicaSet) tryConfigureMonitoringInOpsManager(ctx context.Context, opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) (env.PodEnvVars, error) {
	var operatorVaultSecretPath string
	if r.VaultClient != nil {
		operatorVaultSecretPath = r.VaultClient.OperatorSecretPath()
	}

	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, r.SecretClient, operatorVaultSecretPath)
	if err != nil {
		return env.PodEnvVars{}, xerrors.Errorf("error getting opsManager secret name: %w", err)
	}

	cred, err := project.ReadCredentials(ctx, r.SecretClient, kube.ObjectKey(operatorNamespace(), APIKeySecretName), log)
	if err != nil {
		log.Debugf("Ops Manager has not yet been created, not configuring monitoring: %s", err)
		return env.PodEnvVars{}, nil
	}
	log.Debugf("Ensuring monitoring of AppDB is configured in Ops Manager")

	existingPodVars, err := r.readExistingPodVars(ctx, opsManager, log)
	if client.IgnoreNotFound(err) != nil {
		return env.PodEnvVars{}, xerrors.Errorf("error reading existing podVars: %w", err)
	}

	projectConfig, err := opsManager.GetAppDBProjectConfig(ctx, r.SecretClient, r.client)
	if err != nil {
		return existingPodVars, xerrors.Errorf("error getting existing project config: %w", err)
	}

	_, conn, err := project.ReadOrCreateProject(projectConfig, cred, r.omConnectionFactory, log)
	if err != nil {
		return existingPodVars, xerrors.Errorf("error reading/creating project: %w", err)
	}

	// Configure Authentication Options.
	opts := authentication.Options{
		AgentMechanism:     util.SCRAM,
		Mechanisms:         []string{util.SCRAM},
		ClientCertificates: util.OptionalClientCertficates,
		AutoUser:           util.AutomationAgentUserName,
		CAFilePath:         util.CAFilePathInContainer,
	}
	err = authentication.Configure(conn, opts, false, log)
	if err != nil {
		log.Errorf("Could not set Automation Authentication options in Ops/Cloud Manager for the Application Database. "+
			"Application Database is always configured with authentication enabled, but this will not be "+
			"visible from Ops/Cloud Manager UI. %s", err)
	}

	err = conn.ReadUpdateDeployment(func(d om.Deployment) error {
		d.ConfigureTLS(opsManager.Spec.AppDB.GetSecurity(), util.CAFilePathInContainer)
		return nil
	}, log)
	if err != nil {
		log.Errorf("Could not set TLS configuration in Ops/Cloud Manager for the Application Database. "+
			"Application Database has been configured with TLS enabled, but this will not be "+
			"visible from Ops/Cloud Manager UI. %s", err)
	}

	hostnames := r.generateHeadlessHostnamesForMonitoring(opsManager)
	if err != nil {
		return existingPodVars, xerrors.Errorf("error getting current appdb statefulset hostnames: %w", err)
	}

	if err := r.registerAppDBHostsWithProject(hostnames, conn, opsManagerUserPassword, log); err != nil {
		return existingPodVars, xerrors.Errorf("error registering hosts with project: %w", err)
	}

	if err := r.ensureAppDbAgentApiKey(ctx, opsManager, conn, conn.GroupID(), log); err != nil {
		return existingPodVars, xerrors.Errorf("error ensuring AppDB agent api key: %w", err)
	}

	if err := markAppDBAsBackingProject(conn, log); err != nil {
		return existingPodVars, xerrors.Errorf("error marking project has backing db: %w", err)
	}

	if err := r.ensureProjectIDConfigMap(ctx, opsManager, conn.GroupID()); err != nil {
		return existingPodVars, xerrors.Errorf("error creating ConfigMap: %w", err)
	}

	return env.PodEnvVars{User: conn.PublicKey(), ProjectID: conn.GroupID(), SSLProjectConfig: env.SSLProjectConfig{
		SSLMMSCAConfigMap: opsManager.Spec.GetOpsManagerCA(),
	}}, nil
}

func (r *ReconcileAppDbReplicaSet) ensureProjectIDConfigMap(ctx context.Context, opsManager *omv1.MongoDBOpsManager, projectID string) error {
	var errs error
	for _, memberCluster := range r.getHealthyMemberClusters() {
		if err := r.ensureProjectIDConfigMapForCluster(ctx, opsManager, projectID, memberCluster.Client); err != nil {
			errs = multierror.Append(errs, xerrors.Errorf("error creating ConfigMap in cluster %s: %w", memberCluster.Name, err))
			continue
		}
	}

	return errs
}

func (r *ReconcileAppDbReplicaSet) ensureProjectIDConfigMapForCluster(ctx context.Context, opsManager *omv1.MongoDBOpsManager, projectID string, k8sClient kubernetesClient.Client) error {
	cm := configmap.Builder().
		SetName(opsManager.Spec.AppDB.ProjectIDConfigMapName()).
		SetNamespace(opsManager.Namespace).
		SetDataField(util.AppDbProjectIdKey, projectID).
		Build()

	// Saving the "backup" ConfigMap which contains the project id
	if err := configmap.CreateOrUpdate(ctx, k8sClient, cm); err != nil {
		return xerrors.Errorf("error creating ConfigMap: %w", err)
	}
	return nil
}

// readExistingPodVars is a backup function which provides the required podVars for the AppDB
// in the case of Ops Manager not being reachable. An example of when this is used is:
// 1. The AppDB starts as normal
// 2. Ops Manager starts as normal
// 3. The AppDB password was configured mid-reconciliation
// 4. AppDB reconciles and attempts to configure monitoring, but this is not possible
// as OM cannot currently connect to the AppDB as it has not yet been provided the updated password.
// In such a case, we cannot read the groupId from OM, so we fall back to the ConfigMap we created
// before hand. This is required as with empty PodVars this would trigger an unintentional
// rolling restart of the AppDB.
func (r *ReconcileAppDbReplicaSet) readExistingPodVars(ctx context.Context, om *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (env.PodEnvVars, error) {
	memberClient := r.getMemberCluster(r.getNameOfFirstMemberCluster()).Client
	cm, err := memberClient.GetConfigMap(ctx, kube.ObjectKey(om.Namespace, om.Spec.AppDB.ProjectIDConfigMapName()))
	if err != nil {
		return env.PodEnvVars{}, err
	}
	var projectId string
	if projectId = cm.Data[util.AppDbProjectIdKey]; projectId == "" {
		return env.PodEnvVars{}, xerrors.Errorf("ConfigMap %s did not have the key %s", om.Spec.AppDB.ProjectIDConfigMapName(), util.AppDbProjectIdKey)
	}

	var operatorVaultSecretPath string
	if r.VaultClient != nil {
		operatorVaultSecretPath = r.VaultClient.OperatorSecretPath()
	}
	APISecretName, err := om.APIKeySecretName(ctx, r.SecretClient, operatorVaultSecretPath)
	if err != nil {
		return env.PodEnvVars{}, xerrors.Errorf("error getting ops-manager API secret name: %w", err)
	}

	cred, err := project.ReadCredentials(ctx, r.SecretClient, kube.ObjectKey(operatorNamespace(), APISecretName), log)
	if err != nil {
		return env.PodEnvVars{}, xerrors.Errorf("error reading credentials: %w", err)
	}

	return env.PodEnvVars{
		User:      cred.PublicAPIKey,
		ProjectID: projectId,
		SSLProjectConfig: env.SSLProjectConfig{
			SSLMMSCAConfigMap: om.Spec.GetOpsManagerCA(),
		},
	}, nil
}

func (r *ReconcileAppDbReplicaSet) publishACVersionAsConfigMap(ctx context.Context, cmName string, namespace string, version int, memberClusterName string) workflow.Status {
	acVersionConfigMap := configmap.Builder().
		SetNamespace(namespace).
		SetName(cmName).
		SetDataField(appDBACConfigMapVersionField, fmt.Sprintf("%d", version)).
		Build()
	if err := configmap.CreateOrUpdate(ctx, r.getMemberCluster(memberClusterName).Client, acVersionConfigMap); err != nil {
		return workflow.Failed(xerrors.Errorf("error creating automation config map in cluster %s: %w", memberClusterName, err))
	}

	return workflow.OK()
}

// deployAutomationConfig updates the Automation Config secret if necessary and waits for the pods to fall to "not ready"
// In this case the next StatefulSet update will be safe as the rolling upgrade will wait for the pods to get ready
func (r *ReconcileAppDbReplicaSet) deployAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, prometheusCertHash string, memberClusterName string, log *zap.SugaredLogger) (int, workflow.Status) {
	rs := opsManager.Spec.AppDB

	config, err := r.buildAppDbAutomationConfig(ctx, opsManager, automation, prometheusCertHash, memberClusterName, log)
	if err != nil {
		return 0, workflow.Failed(err)
	}
	var configVersion int
	if configVersion, err = r.publishAutomationConfig(ctx, opsManager, config, rs.AutomationConfigSecretName(), memberClusterName); err != nil {
		return 0, workflow.Failed(err)
	}

	if workflowStatus := r.publishACVersionAsConfigMap(ctx, opsManager.Spec.AppDB.AutomationConfigConfigMapName(), opsManager.Namespace, configVersion, memberClusterName); !workflowStatus.IsOK() {
		return 0, workflowStatus
	}

	monitoringAc, err := r.buildAppDbAutomationConfig(ctx, opsManager, monitoring, UnusedPrometheusConfiguration, memberClusterName, log)
	if err != nil {
		return 0, workflow.Failed(err)
	}

	if err := r.deployMonitoringAgentAutomationConfig(ctx, opsManager, memberClusterName, log); err != nil {
		return 0, workflow.Failed(err)
	}

	if workflowStatus := r.publishACVersionAsConfigMap(ctx, opsManager.Spec.AppDB.MonitoringAutomationConfigConfigMapName(), opsManager.Namespace, monitoringAc.Version, memberClusterName); !workflowStatus.IsOK() {
		return 0, workflowStatus
	}

	return configVersion, workflow.OK()
}

// deployMonitoringAgentAutomationConfig deploys the monitoring agent's automation config.
func (r *ReconcileAppDbReplicaSet) deployMonitoringAgentAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, memberClusterName string, log *zap.SugaredLogger) error {
	config, err := r.buildAppDbAutomationConfig(ctx, opsManager, monitoring, UnusedPrometheusConfiguration, memberClusterName, log)
	if err != nil {
		return err
	}
	if _, err = r.publishAutomationConfig(ctx, opsManager, config, opsManager.Spec.AppDB.MonitoringAutomationConfigSecretName(), memberClusterName); err != nil {
		return err
	}
	return nil
}

func (r *ReconcileAppDbReplicaSet) deployStatefulSet(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger, podVars env.PodEnvVars, appdbOpts construct.AppDBStatefulSetOptions) workflow.Status {
	if err := r.createMultiClusterServices(ctx, opsManager); err != nil {
		return workflow.Failed(err)
	}
	currentClusterSpecs := map[string]int{}
	scalingFirstTime := false
	// iterate over all clusters to scale even unhealthy ones
	// currentClusterSpecs map is maintained for scaling therefore we need to update it here
	for _, memberCluster := range r.getAllMemberClusters() {
		scaler := scalers.GetAppDBScaler(opsManager, memberCluster.Name, r.getMemberClusterIndex(memberCluster.Name), r.memberClusters)
		replicasThisReconciliation := scale.ReplicasThisReconciliation(scaler)
		currentClusterSpecs[memberCluster.Name] = replicasThisReconciliation

		if !memberCluster.Healthy {
			// do not proceed if this is unhealthy cluster
			continue
		}

		// in the case of an upgrade from the 1 to 3 container architecture, when the stateful set is updated before the agent automation config
		// the monitoring agent automation config needs to exist for the volumes to mount correctly.
		if err := r.deployMonitoringAgentAutomationConfig(ctx, opsManager, memberCluster.Name, log); err != nil {
			return workflow.Failed(err)
		}

		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, appdbOpts, scaler, log)
		if err != nil {
			return workflow.Failed(xerrors.Errorf("can't construct AppDB Statefulset: %w", err))
		}

		if workflowStatus := r.deployStatefulSetInMemberCluster(ctx, opsManager, appDbSts, memberCluster.Name, log); !workflowStatus.IsOK() {
			return workflowStatus.Merge(workflow.Failed(xerrors.Errorf("cannot deploy stateful set in cluster %s", memberCluster.Name)))
		}

		if appdbMultiScaler, ok := scaler.(*scalers.AppDBMultiClusterScaler); ok {
			// we want to deploy all stateful sets the first time we're deploying stateful sets
			if appdbMultiScaler.ScalingFirstTime() {
				scalingFirstTime = true
				continue
			}
		}
		if workflowStatus := getStatefulSetStatus(ctx, opsManager.Namespace, opsManager.Spec.AppDB.NameForCluster(memberCluster.Index), memberCluster.Client); !workflowStatus.IsOK() {
			return workflowStatus
		}

		if err := statefulset.ResetUpdateStrategy(ctx, opsManager.GetVersionedImplForMemberCluster(r.getMemberClusterIndex(memberCluster.Name)), memberCluster.Client); err != nil {
			return workflow.Failed(xerrors.Errorf("can't reset AppDB StatefulSet UpdateStrategyType: %w", err))
		}
	}

	// if this is the first time deployment, then we need to wait for all stateful sets to become ready after deploying all of them
	if scalingFirstTime {
		for _, memberCluster := range r.getHealthyMemberClusters() {
			if workflowStatus := getStatefulSetStatus(ctx, opsManager.Namespace, opsManager.Spec.AppDB.NameForCluster(memberCluster.Index), memberCluster.Client); !workflowStatus.IsOK() {
				return workflowStatus
			}

			if err := statefulset.ResetUpdateStrategy(ctx, opsManager.GetVersionedImplForMemberCluster(r.getMemberClusterIndex(memberCluster.Name)), memberCluster.Client); err != nil {
				return workflow.Failed(xerrors.Errorf("can't reset AppDB StatefulSet UpdateStrategyType: %w", err))
			}
		}
	}

	for k, v := range currentClusterSpecs {
		r.currentClusterSpecs[k] = v
	}

	return workflow.OK()
}

func (r *ReconcileAppDbReplicaSet) createMultiClusterServices(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error {
	if !opsManager.Spec.AppDB.IsMultiCluster() {
		return nil
	}

	for _, memberCluster := range r.getHealthyMemberClusters() {
		clusterSpecItem := opsManager.Spec.AppDB.GetMemberClusterSpecByName(memberCluster.Name)
		for podNum := 0; podNum < clusterSpecItem.Members; podNum++ {
			svc := getMultiClusterAppDBService(opsManager.Spec.AppDB, r.getMemberClusterIndex(memberCluster.Name), podNum)
			err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create service: %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
		}
	}

	return nil
}

// deployStatefulSetInMemberCluster updates the StatefulSet spec and returns its status (if it's ready or not)
func (r *ReconcileAppDbReplicaSet) deployStatefulSetInMemberCluster(ctx context.Context, opsManager *omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, memberClusterName string, log *zap.SugaredLogger) workflow.Status {
	serviceSelectorLabel := opsManager.Spec.AppDB.HeadlessServiceSelectorAppLabel(r.getMemberCluster(memberClusterName).Index)
	if err := create.AppDBInKubernetes(ctx, r.getMemberCluster(memberClusterName).Client, opsManager, appDbSts, serviceSelectorLabel, log); err != nil {
		return workflow.Failed(err)
	}

	return workflow.OK()
}

func (r *ReconcileAppDbReplicaSet) allAgentsReachedGoalState(ctx context.Context, manager *omv1.MongoDBOpsManager, targetConfigVersion int, log *zap.SugaredLogger) workflow.Status {
	for _, memberCluster := range r.getHealthyMemberClusters() {
		var workflowStatus workflow.Status
		if manager.Spec.AppDB.IsMultiCluster() {
			workflowStatus = r.allAgentsReachedGoalStateMultiCluster(ctx, manager, targetConfigVersion, memberCluster.Name, log)
		} else {
			workflowStatus = r.allAgentsReachedGoalStateSingleCluster(ctx, manager, targetConfigVersion, memberCluster.Name, log)
		}

		if !workflowStatus.IsOK() {
			return workflowStatus
		}
	}

	return workflow.OK()
}

func (r *ReconcileAppDbReplicaSet) allAgentsReachedGoalStateMultiCluster(ctx context.Context, manager *omv1.MongoDBOpsManager, targetConfigVersion int, memberClusterName string, log *zap.SugaredLogger) workflow.Status {
	memberClusterClient := r.getMemberCluster(memberClusterName).Client
	set, err := memberClusterClient.GetStatefulSet(ctx, manager.AppDBStatefulSetObjectKey(r.getMemberClusterIndex(memberClusterName)))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return workflow.OK()
		}
		return workflow.Failed(err)
	}

	appDBSize := int(set.Status.Replicas)
	goalState, err := agent.AllReachedGoalState(ctx, set, memberClusterClient, appDBSize, targetConfigVersion, log)
	if err != nil {
		return workflow.Failed(err)
	}
	if goalState {
		return workflow.OK()
	}
	return workflow.Pending("Application Database Agents haven't reached Running state yet")
}

// allAgentsReachedGoalState checks if all the AppDB Agents have reached the goal state.
func (r *ReconcileAppDbReplicaSet) allAgentsReachedGoalStateSingleCluster(ctx context.Context, manager *omv1.MongoDBOpsManager, targetConfigVersion int, memberClusterName string, log *zap.SugaredLogger) workflow.Status {
	// We need to read the current StatefulSet to find the real number of pods - we cannot rely on OpsManager resource
	set, err := r.client.GetStatefulSet(ctx, manager.AppDBStatefulSetObjectKey(r.getMemberClusterIndex(memberClusterName)))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// If the StatefulSet could not be found, do not check agents during this reconcile.
			// It means - we didn't deploy statefulset yet, and we should proceed.
			return workflow.OK()
		}
		return workflow.Failed(err)
	}

	appdbSize := int(set.Status.Replicas)
	goalState, err := agent.AllReachedGoalState(ctx, set, r.client, appdbSize, targetConfigVersion, log)
	if err != nil {
		return workflow.Failed(err)
	}
	if goalState {
		return workflow.OK()
	}
	return workflow.Pending("Application Database Agents haven't reached Running state yet")
}

func (r *ReconcileAppDbReplicaSet) getAllMemberClusters() []multicluster.MemberCluster {
	return r.memberClusters
}

func (r *ReconcileAppDbReplicaSet) getHealthyMemberClusters() []multicluster.MemberCluster {
	var healthyMemberClusters []multicluster.MemberCluster
	for i := 0; i < len(r.memberClusters); i++ {
		if r.memberClusters[i].Healthy {
			healthyMemberClusters = append(healthyMemberClusters, r.memberClusters[i])
		}
	}

	return healthyMemberClusters
}

func (r *ReconcileAppDbReplicaSet) getMemberCluster(name string) multicluster.MemberCluster {
	for i := 0; i < len(r.memberClusters); i++ {
		if r.memberClusters[i].Name == name {
			return r.memberClusters[i]
		}
	}

	panic(xerrors.Errorf("member cluster %s not found", name))
}

func (r *ReconcileAppDbReplicaSet) getMemberClusterIndex(clusterName string) int {
	return r.getMemberCluster(clusterName).Index
}

func (r *ReconcileAppDbReplicaSet) getCurrentStatefulsetHostnames(opsManager *omv1.MongoDBOpsManager) []string {
	return util.Transform(r.generateProcessList(opsManager), func(process automationconfig.Process) string {
		return process.HostName
	})
}

func (r *ReconcileAppDbReplicaSet) allStatefulSetsExist(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (bool, error) {
	allStsExist := true
	for _, memberCluster := range r.getHealthyMemberClusters() {
		stsName := opsManager.Spec.AppDB.NameForCluster(r.getMemberClusterIndex(memberCluster.Name))
		_, err := memberCluster.Client.GetStatefulSet(ctx, kube.ObjectKey(opsManager.Namespace, stsName))
		if err != nil {
			if apiErrors.IsNotFound(err) {
				// we do not return immediately here to check all clusters and also leave the information on other sts in the debug logs
				log.Debugf("Statefulset %s/%s does not exist.", memberCluster.Name, stsName)
				allStsExist = false
			} else {
				return false, err
			}
		}
	}

	return allStsExist, nil
}

// markAppDBAsBackingProject will configure the AppDB project to be read only. Errors are ignored
// if the OpsManager version does not support this feature.
func markAppDBAsBackingProject(conn om.Connection, log *zap.SugaredLogger) error {
	log.Debugf("Configuring the project as a backing database project.")
	err := conn.MarkProjectAsBackingDatabase(om.AppDBDatabaseType)
	if err != nil {
		if apiErr, ok := err.(*apierror.Error); ok {
			opsManagerDoesNotSupportApi := apiErr.Status != nil && *apiErr.Status == 404 && apiErr.ErrorCode == "RESOURCE_NOT_FOUND"
			if opsManagerDoesNotSupportApi {
				msg := "This version of Ops Manager does not support the markAsBackingDatabase API."
				if !conn.OpsManagerVersion().IsUnknown() {
					msg += fmt.Sprintf(" Version=%s", conn.OpsManagerVersion())
				}
				log.Debug(msg)
				return nil
			}
		}
		return err
	}
	return nil
}
