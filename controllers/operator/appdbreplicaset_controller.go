package operator

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/stretchr/objx"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/host"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/create"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	mdbcv1_controllers "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers"
	mcoConstruct "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/agent"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/authentication/scram"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/generate"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/result"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/agentVersionManagement"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	mekoService "github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/placeholders"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/timeutil"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
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

type CommonDeploymentState struct {
	ClusterMapping map[string]int `json:"clusterMapping"`
}

type AppDBDeploymentState struct {
	CommonDeploymentState     `json:",inline"`
	LastAppliedMemberSpec     map[string]int `json:"lastAppliedMemberSpec"`
	LastAppliedMongoDBVersion string         `json:"lastAppliedMongoDBVersion"`
}

func NewAppDBDeploymentState() *AppDBDeploymentState {
	return &AppDBDeploymentState{
		CommonDeploymentState: CommonDeploymentState{ClusterMapping: map[string]int{}},
		LastAppliedMemberSpec: map[string]int{},
	}
}

// ReconcileAppDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileAppDbReplicaSet struct {
	*ReconcileCommonController
	omConnectionFactory om.ConnectionFactory

	centralClient kubernetesClient.Client
	// ordered list of member clusters; order in this list is preserved across runs using memberClusterIndex
	memberClusters  []multicluster.MemberCluster
	stateStore      *StateStore[AppDBDeploymentState]
	deploymentState *AppDBDeploymentState

	imageUrls        images.ImageUrls
	initAppdbVersion string
}

func NewAppDBReplicaSetReconciler(ctx context.Context, imageUrls images.ImageUrls, initAppdbVersion string, appDBSpec omv1.AppDBSpec, commonController *ReconcileCommonController, omConnectionFactory om.ConnectionFactory, omAnnotations map[string]string, globalMemberClustersMap map[string]client.Client, log *zap.SugaredLogger) (*ReconcileAppDbReplicaSet, error) {
	reconciler := &ReconcileAppDbReplicaSet{
		ReconcileCommonController: commonController,
		omConnectionFactory:       omConnectionFactory,
		centralClient:             commonController.client,
		imageUrls:                 imageUrls,
		initAppdbVersion:          initAppdbVersion,
	}

	if err := reconciler.initializeStateStore(ctx, appDBSpec, omAnnotations, log); err != nil {
		return nil, xerrors.Errorf("failed to initialize appdb state store: %w", err)
	}

	if err := reconciler.initializeMemberClusters(ctx, appDBSpec, globalMemberClustersMap, log); err != nil {
		return nil, xerrors.Errorf("failed to initialize appdb replicaset controller: %w", err)
	}

	return reconciler, nil
}

// initializeStateStore initializes the deploymentState field by reading it from a state config map.
// In case there is no state config map, the new state map is created and saved after performing migration of the existing state data (see migrateToNewDeploymentState).
func (r *ReconcileAppDbReplicaSet) initializeStateStore(ctx context.Context, appDBSpec omv1.AppDBSpec, omAnnotations map[string]string, log *zap.SugaredLogger) error {
	r.deploymentState = NewAppDBDeploymentState()

	r.stateStore = NewStateStore[AppDBDeploymentState](&appDBSpec, r.centralClient)
	if state, err := r.stateStore.ReadState(ctx); err != nil {
		if apiErrors.IsNotFound(err) {
			// If the deployment state config map is missing, then it might be either:
			//  - fresh deployment
			//  - existing deployment, but it's a first reconcile on the operator version with the new deployment state
			//  - existing deployment, but for some reason the deployment state config map has been deleted
			// In all cases, the deployment config map will be recreated from the state we're keeping and maintaining in
			// the old place (in annotations, spec.status, config maps) in order to allow for the downgrade of the operator.
			if err := r.migrateToNewDeploymentState(ctx, appDBSpec, omAnnotations); err != nil {
				return err
			}
			// This will migrate the deployment state to the new structure and this branch of code won't be executed again.
			// Here we don't use saveAppDBState wrapper, as we don't need to write the legacy state
			if err := r.stateStore.WriteState(ctx, r.deploymentState, log); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		r.deploymentState = state
	}

	return nil
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
func (r *ReconcileAppDbReplicaSet) initializeMemberClusters(ctx context.Context, appDBSpec omv1.AppDBSpec, globalMemberClustersMap map[string]client.Client, log *zap.SugaredLogger) error {
	if appDBSpec.IsMultiCluster() {
		if len(globalMemberClustersMap) == 0 {
			return xerrors.Errorf("member clusters have to be initialized for MultiCluster AppDB topology")
		}
		// here we access ClusterSpecList directly, as we have to check what's been defined in yaml
		if len(appDBSpec.ClusterSpecList) == 0 {
			return xerrors.Errorf("for appDBSpec.Topology = MultiCluster, clusterSpecList have to be non empty")
		}

		r.updateMemberClusterMapping(appDBSpec)

		getLastAppliedMemberCountFunc := func(memberClusterName string) int {
			return r.getLastAppliedMemberCount(appDBSpec, memberClusterName)
		}

		clusterSpecList := appDBSpec.GetClusterSpecList()
		r.memberClusters = createMemberClusterListFromClusterSpecList(clusterSpecList, globalMemberClustersMap, log, r.deploymentState.ClusterMapping, getLastAppliedMemberCountFunc, false)

		if err := r.saveAppDBState(ctx, appDBSpec, log); err != nil {
			return err
		}
	} else {
		// for SingleCluster member cluster list will contain one member  which will be the central (default) cluster
		r.memberClusters = []multicluster.MemberCluster{multicluster.GetLegacyCentralMemberCluster(appDBSpec.Members, 0, r.centralClient, r.SecretClient)}
	}

	log.Debugf("Initialized member cluster list: %+v", util.Transform(r.memberClusters, func(m multicluster.MemberCluster) string {
		return fmt.Sprintf("{Name: %s, Index: %d, Replicas: %d, Active: %t, Healthy: %t}", m.Name, m.Index, m.Replicas, m.Active, m.Healthy)
	}))

	return nil
}

// saveAppDBState is a wrapper method around WriteState, to ensure we keep updating the legacy Config Maps for downgrade
// compatibility
// This will write the legacy state to the cluster even for NEW deployments, created after upgrade of the operator.
// It is not incorrect and doesn't interfere with the logic, but it *could* be confusing for a user
// (this is also the case for OM controller)
func (r *ReconcileAppDbReplicaSet) saveAppDBState(ctx context.Context, spec omv1.AppDBSpec, log *zap.SugaredLogger) error {
	if err := r.stateStore.WriteState(ctx, r.deploymentState, log); err != nil {
		return err
	}
	if err := r.writeLegacyStateConfigMaps(ctx, spec, log); err != nil {
		return err
	}
	return nil
}

// writeLegacyStateConfigMaps converts the DeploymentState to legacy Config Maps and write them to the cluster
// LastAppliedMongoDBVersion is also part of the state, it is handled separately in the controller as it was an annotation
func (r *ReconcileAppDbReplicaSet) writeLegacyStateConfigMaps(ctx context.Context, spec omv1.AppDBSpec, log *zap.SugaredLogger) error {
	// ClusterMapping ConfigMap
	mappingConfigMapData := map[string]string{}
	for k, v := range r.deploymentState.ClusterMapping {
		mappingConfigMapData[k] = fmt.Sprintf("%d", v)
	}
	mappingConfigMap := configmap.Builder().
		SetName(spec.ClusterMappingConfigMapName()).
		SetLabels(spec.GetOwnerLabels()).
		SetNamespace(spec.Namespace).
		SetData(mappingConfigMapData).
		Build()
	if err := configmap.CreateOrUpdate(ctx, r.centralClient, mappingConfigMap); err != nil {
		return xerrors.Errorf("failed to update cluster mapping configmap %s: %w", spec.ClusterMappingConfigMapName(), err)
	}
	log.Debugf("Saving cluster mapping configmap %s: %v", spec.ClusterMappingConfigMapName(), mappingConfigMapData)

	// LastAppliedMemberSpec ConfigMap
	specConfigMapData := map[string]string{}
	for k, v := range r.deploymentState.LastAppliedMemberSpec {
		specConfigMapData[k] = fmt.Sprintf("%d", v)
	}
	specConfigMap := configmap.Builder().
		SetName(spec.LastAppliedMemberSpecConfigMapName()).
		SetLabels(spec.GetOwnerLabels()).
		SetNamespace(spec.Namespace).
		SetData(specConfigMapData).
		Build()
	if err := configmap.CreateOrUpdate(ctx, r.centralClient, specConfigMap); err != nil {
		return xerrors.Errorf("failed to update last applied member spec configmap %s: %w", spec.LastAppliedMemberSpecConfigMapName(), err)
	}
	log.Debugf("Saving last applied member spec configmap %s: %v", spec.LastAppliedMemberSpecConfigMapName(), specConfigMapData)

	return nil
}

func createMemberClusterListFromClusterSpecList(clusterSpecList mdbv1.ClusterSpecList, globalMemberClustersMap map[string]client.Client, log *zap.SugaredLogger, memberClusterMapping map[string]int, getLastAppliedMemberCountFunc func(memberClusterName string) int, legacyMemberCluster bool) []multicluster.MemberCluster {
	var memberClusters []multicluster.MemberCluster
	specClusterMap := map[string]struct{}{}
	for _, clusterSpecItem := range clusterSpecList {
		specClusterMap[clusterSpecItem.ClusterName] = struct{}{}

		var memberClusterKubeClient kubernetesClient.Client
		var memberClusterSecretClient secrets.SecretClient
		memberClusterClient, ok := globalMemberClustersMap[clusterSpecItem.ClusterName]
		if !ok {
			var clusterList []string
			for m := range globalMemberClustersMap {
				clusterList = append(clusterList, m)
			}
			log.Warnf("Member cluster %s specified in clusterSpecList is not found in the list of operator's member clusters: %+v. "+
				"Assuming the cluster is down. It will be ignored from reconciliation but its MongoDB processes will still be maintained in replicaset configuration.", clusterSpecItem.ClusterName, clusterList)
		} else {
			memberClusterKubeClient = kubernetesClient.NewClient(memberClusterClient)
			memberClusterSecretClient = secrets.SecretClient{
				VaultClient: nil, // Vault is not supported yet on multi cluster
				KubeClient:  memberClusterKubeClient,
			}
		}

		memberClusters = append(memberClusters, multicluster.MemberCluster{
			Name:         clusterSpecItem.ClusterName,
			Index:        memberClusterMapping[clusterSpecItem.ClusterName],
			Client:       memberClusterKubeClient,
			SecretClient: memberClusterSecretClient,
			Replicas:     getLastAppliedMemberCountFunc(clusterSpecItem.ClusterName),
			Active:       true,
			Healthy:      memberClusterKubeClient != nil,
			Legacy:       legacyMemberCluster,
		})
	}

	// add previous member clusters with last applied members. This is required for being able to scale down the appdb members one by one.
	for previousMember := range memberClusterMapping {
		// If the previous member is already present in the spec, skip it safely
		if _, ok := specClusterMap[previousMember]; ok {
			continue
		}

		previousMemberReplicas := getLastAppliedMemberCountFunc(previousMember)
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
				"Assuming the cluster is down. It will be ignored from reconciliation but it's MongoDB processes will be scaled down to 0 in replicaset configuration.", previousMember, clusterList)
		} else {
			memberClusterKubeClient = kubernetesClient.NewClient(memberClusterClient)
			memberClusterSecretClient = secrets.SecretClient{
				VaultClient: nil, // Vault is not supported yet on multi cluster
				KubeClient:  memberClusterKubeClient,
			}
		}

		memberClusters = append(memberClusters, multicluster.MemberCluster{
			Name:         previousMember,
			Index:        memberClusterMapping[previousMember],
			Client:       memberClusterKubeClient,
			SecretClient: memberClusterSecretClient,
			Replicas:     previousMemberReplicas,
			Active:       false,
			Healthy:      memberClusterKubeClient != nil,
		})
	}
	sort.Slice(memberClusters, func(i, j int) bool {
		return memberClusters[i].Index < memberClusters[j].Index
	})

	return memberClusters
}

func (r *ReconcileAppDbReplicaSet) getLastAppliedMemberCount(spec omv1.AppDBSpec, clusterName string) int {
	if !spec.IsMultiCluster() {
		panic(fmt.Errorf("the function cannot be used in SingleCluster topology)"))
	}
	specMapping := r.getLastAppliedMemberSpec(spec)
	return specMapping[clusterName]
}

func (r *ReconcileAppDbReplicaSet) getLastAppliedMemberSpec(spec omv1.AppDBSpec) map[string]int {
	if !spec.IsMultiCluster() {
		return nil
	}
	return r.deploymentState.LastAppliedMemberSpec
}

func (r *ReconcileAppDbReplicaSet) getLegacyLastAppliedMemberSpec(ctx context.Context, spec omv1.AppDBSpec) (map[string]int, error) {
	// read existing spec
	existingSpec := map[string]int{}
	existingConfigMap := corev1.ConfigMap{}
	err := r.centralClient.Get(ctx, kube.ObjectKey(spec.Namespace, spec.LastAppliedMemberSpecConfigMapName()), &existingConfigMap)
	if err != nil {
		return nil, xerrors.Errorf("failed to read last applied member spec config map %s: %w", spec.LastAppliedMemberSpecConfigMapName(), err)
	} else {
		for clusterName, replicasStr := range existingConfigMap.Data {
			replicas, err := strconv.Atoi(replicasStr)
			if err != nil {
				return nil, xerrors.Errorf("failed to read last applied member spec from config map %s (%+v): %w", spec.LastAppliedMemberSpecConfigMapName(), existingConfigMap.Data, err)
			}
			existingSpec[clusterName] = replicas
		}
	}

	return existingSpec, nil
}

// getLegacyMemberClusterMapping is reading the cluster mapping from the old config map where it has been stored before introducing the deployment state config map.
func getLegacyMemberClusterMapping(ctx context.Context, namespace string, configMapName string, centralClient kubernetesClient.Client) (map[string]int, error) {
	// read existing config map
	existingMapping := map[string]int{}
	existingConfigMap, err := centralClient.GetConfigMap(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace})
	if err != nil {
		return nil, xerrors.Errorf("failed to read cluster mapping config map %s: %w", configMapName, err)
	} else {
		for clusterName, indexStr := range existingConfigMap.Data {
			index, err := strconv.Atoi(indexStr)
			if err != nil {
				return nil, xerrors.Errorf("failed to read cluster mapping indexes from config map %s (%+v): %w", configMapName, existingConfigMap.Data, err)
			}
			existingMapping[clusterName] = index
		}
	}

	return existingMapping, nil
}

// updateMemberClusterMapping returns a map of member cluster name -> cluster index.
// Mapping is preserved in spec.ClusterMappingConfigMapName() config map. Config map is created if not exists.
// Subsequent executions will merge, update and store mappings from config map and from clusterSpecList and save back to config map.
func (r *ReconcileAppDbReplicaSet) updateMemberClusterMapping(spec omv1.AppDBSpec) {
	if !spec.IsMultiCluster() {
		return
	}

	r.deploymentState.ClusterMapping = multicluster.AssignIndexesForMemberClusterNames(r.deploymentState.ClusterMapping, util.Transform(spec.GetClusterSpecList(), func(clusterSpecItem mdbv1.ClusterSpecItem) string {
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

	var appdbSecretPath string
	if r.VaultClient != nil {
		appdbSecretPath = r.VaultClient.AppDBSecretPath()
	}

	agentCertSecretName := opsManager.Spec.AppDB.GetSecurity().AgentClientCertificateSecretName(opsManager.Spec.AppDB.GetName())
	_, agentCertPath := r.agentCertHashAndPath(ctx, log, opsManager.Namespace, agentCertSecretName, appdbSecretPath)

	podVars, err := r.tryConfigureMonitoringInOpsManager(ctx, opsManager, opsManagerUserPassword, agentCertPath, log)
	// it's possible that Ops Manager will not be available when we attempt to configure AppDB monitoring
	// in Ops Manager. This is not a blocker to continue with the rest of the reconciliation.
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
			if _, err := r.ensureAppDbAgentApiKey(ctx, opsManager, nil, podVars.ProjectID, log); err != nil {
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

	appdbOpts := construct.AppDBStatefulSetOptions{
		InitAppDBImage: images.ContainerImage(r.imageUrls, util.InitAppdbImageUrlEnv, r.initAppdbVersion),
		MongodbImage:   images.GetOfficialImage(r.imageUrls, opsManager.Spec.AppDB.Version, opsManager.GetAnnotations()),
	}
	if architectures.IsRunningStaticArchitecture(opsManager.Annotations) {
		if !rs.PodSpec.IsAgentImageOverridden() {
			// Because OM is not available when starting AppDB, we read the version from the mapping
			// We plan to change this in the future, but for the sake of simplicity we leave it that way for the moment
			// It avoids unnecessary reconciles, race conditions...
			agentVersion, err := r.getAgentVersion(nil, opsManager.Spec.Version, true, log)
			if err != nil {
				log.Errorf("Impossible to get agent version, please override the agent image by providing a pod template")
				return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Failed to get agent version: %w. Please use spec.statefulSet to supply proper Agent version", err)), log)
			}

			appdbOpts.AgentImage = images.ContainerImage(r.imageUrls, architectures.MdbAgentImageRepo, agentVersion)
		}
	} else {
		// instead of using a hard-coded monitoring version, we use the "newest" one based on the release.json.
		// This ensures we need to care less about CVEs compared to the prior older hardcoded versions.
		legacyMonitoringAgentVersion, err := r.getLegacyMonitoringAgentVersion(ctx, opsManager, log)
		if err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error reading monitoring agent version: %w", err)), log, appDbStatusOption)
		}

		appdbOpts.LegacyMonitoringAgentImage = images.ContainerImage(r.imageUrls, mcoConstruct.AgentImageEnv, legacyMonitoringAgentVersion)

		// AgentImageEnv contains the full container image uri e.g. quay.io/mongodb/mongodb-agent:107.0.0.8502-1
		// In non-static containers we don't ask OM for the correct version, therefore we just rely on the provided
		// environment variable.
		appdbOpts.AgentImage = r.imageUrls[mcoConstruct.AgentImageEnv]
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

	// We keep updating annotations for backward compatibility (e.g operator downgrade), so we write the
	// lastAppliedMongoDBVersion both in the state and in annotations below
	// here it doesn't matter for which cluster we'll generate the name - only AppDB's MongoDB version is used there, which is the same in all clusters
	verionedImplForMemberCluster := opsManager.GetVersionedImplForMemberCluster(r.getMemberClusterIndex(r.getNameOfFirstMemberCluster()))
	log.Debugf("Storing LastAppliedMongoDBVersion %s in annotations and deployment state", verionedImplForMemberCluster.GetMongoDBVersionForAnnotation())
	r.deploymentState.LastAppliedMongoDBVersion = verionedImplForMemberCluster.GetMongoDBVersionForAnnotation()
	if err := annotations.UpdateLastAppliedMongoDBVersion(ctx, verionedImplForMemberCluster, r.centralClient); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Could not save current state as an annotation: %w", err)), log, omStatusOption)
	}

	appDBScalers := []interfaces.MultiClusterReplicaSetScaler{}
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

	if err := r.saveAppDBState(ctx, opsManager.Spec.AppDB, log); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Could not save deployment state: %w", err)), log, omStatusOption)
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
	for _, memberCluster := range r.GetHealthyMemberClusters() {
		if memberCluster.Active {
			firstMemberClusterName = memberCluster.Name
			break
		}
	}
	return firstMemberClusterName
}

// getLegacyMonitoringAgentVersion partially duplicates the functionality in (r *ReconcileCommonController).getAgentVersion to avoid
// changing the signature of the general function that is used in static container setups as this method only considers the non-static
// OpsManager. This should also make it easier to remove when switching to one architecture for containers.
func (r *ReconcileAppDbReplicaSet) getLegacyMonitoringAgentVersion(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	m, err := agentVersionManagement.GetAgentVersionManager()
	if err != nil || m == nil {
		return "", xerrors.Errorf("not able to init agentVersionManager: %w", err)
	}
	if agentVersion, err := m.GetAgentVersion(nil, opsManager.Spec.Version, true); err != nil {
		log.Errorf("Failed to get the agent version from the Agent Version manager: %s", err)
		return "", xerrors.Errorf("Failed to get the agent version from the Agent Version manager: %w", err)
	} else {
		return agentVersion, nil
	}
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
	for _, memberCluster := range r.GetHealthyMemberClusters() {
		if configVersion, workflowStatus := r.deployAutomationConfig(ctx, opsManager, appdbOpts.PrometheusTLSCertHash, memberCluster, log); !workflowStatus.IsOK() {
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

func getAppDBPodService(appdb omv1.AppDBSpec, clusterNum int, podNum int) corev1.Service {
	svcLabels := map[string]string{
		appsv1.StatefulSetPodNameLabel: appdb.GetPodName(clusterNum, podNum),
		util.OperatorLabelName:         util.OperatorLabelValue,
	}
	svcLabels = merge.StringToStringMap(svcLabels, appdb.GetOwnerLabels())

	labelSelectors := map[string]string{
		appsv1.StatefulSetPodNameLabel: appdb.GetPodName(clusterNum, podNum),
		util.OperatorLabelName:         util.OperatorLabelValue,
	}
	additionalConfig := appdb.GetAdditionalMongodConfig()
	port := additionalConfig.GetPortOrDefault()
	svc := service.Builder().
		SetNamespace(appdb.Namespace).
		SetSelector(labelSelectors).
		SetLabels(svcLabels).
		SetPublishNotReadyAddresses(true).
		AddPort(&corev1.ServicePort{Port: port, Name: "mongodb"}).
		Build()
	return svc
}

func getAppDBExternalService(appdb omv1.AppDBSpec, clusterIdx int, clusterName string, podIdx int) corev1.Service {
	svc := getAppDBPodService(appdb, clusterIdx, podIdx)
	svc.Name = appdb.GetExternalServiceName(clusterIdx, podIdx)
	svc.Spec.Type = corev1.ServiceTypeLoadBalancer

	externalAccessConfig := appdb.GetExternalAccessConfigurationForMemberCluster(clusterName)
	if externalAccessConfig != nil {
		// first we override with the Service spec from the root and then from a specific cluster.
		if appdb.GetExternalAccessConfiguration() != nil {
			globalOverrideSpecWrapper := appdb.ExternalAccessConfiguration.ExternalService.SpecWrapper
			if globalOverrideSpecWrapper != nil {
				svc.Spec = merge.ServiceSpec(svc.Spec, globalOverrideSpecWrapper.Spec)
			}
			svc.Annotations = merge.StringToStringMap(svc.Annotations, appdb.GetExternalAccessConfiguration().ExternalService.Annotations)
		}
		clusterLevelOverrideSpec := externalAccessConfig.ExternalService.SpecWrapper
		additionalAnnotations := externalAccessConfig.ExternalService.Annotations
		if clusterLevelOverrideSpec != nil {
			svc.Spec = merge.ServiceSpec(svc.Spec, clusterLevelOverrideSpec.Spec)
		}
		svc.Annotations = merge.StringToStringMap(svc.Annotations, additionalAnnotations)
	}

	return svc
}

func getPlaceholderReplacer(appdb omv1.AppDBSpec, memberCluster multicluster.MemberCluster, podNum int) *placeholders.Replacer {
	if appdb.IsMultiCluster() {
		return create.GetMultiClusterMongoDBPlaceholderReplacer(
			appdb.Name(),
			appdb.Name(),
			appdb.Namespace,
			memberCluster.Name,
			memberCluster.Index,
			appdb.GetExternalDomainForMemberCluster(memberCluster.Name),
			appdb.GetClusterDomain(),
			podNum)
	}
	return create.GetSingleClusterMongoDBPlaceholderReplacer(
		appdb.Name(),
		appdb.Name(),
		appdb.Namespace,
		dns.GetServiceName(appdb.Name()),
		appdb.GetExternalDomain(),
		appdb.GetClusterDomain(),
		podNum,
		mdbv1.ReplicaSet)
}

func (r *ReconcileAppDbReplicaSet) publishAutomationConfigFirst(opsManager *omv1.MongoDBOpsManager, allStatefulSetsExist bool, log *zap.SugaredLogger) bool {
	// The only case when we push the StatefulSet first is when we are ensuring TLS for the already existing AppDB
	// TODO this feels insufficient. Shouldn't we check if there is actual change in TLS settings requiring to push sts first? Now it will always publish sts first when TLS enabled
	automationConfigFirst := !allStatefulSetsExist || !opsManager.Spec.AppDB.GetSecurity().IsTLSEnabled()

	if r.isChangingVersion(opsManager) {
		log.Info("Version change in progress, the StatefulSet must be updated first")
		automationConfigFirst = false
	}

	// if we are performing a force reconfigure we should change the automation config first
	if shouldPerformForcedReconfigure(opsManager.Annotations) {
		automationConfigFirst = true
	}

	return automationConfigFirst
}

func (r *ReconcileAppDbReplicaSet) isChangingVersion(opsManager *omv1.MongoDBOpsManager) bool {
	prevVersion := r.deploymentState.LastAppliedMongoDBVersion
	return prevVersion != "" && prevVersion != opsManager.Spec.AppDB.Version
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
		for _, memberCluster := range r.GetHealthyMemberClusters() {
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
		for _, memberCluster := range r.GetHealthyMemberClusters() {
			err = certs.CreateOrUpdatePEMSecretWithPreviousCert(ctx, memberCluster.SecretClient, kube.ObjectKey(om.Namespace, secretName), secretHash, data, nil, certs.AppDB)
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

	for _, memberCluster := range r.GetHealthyMemberClusters() {
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

	for _, memberCluster := range r.GetHealthyMemberClusters() {
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
func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, automationConfig automationconfig.AutomationConfig, secretName string, secretsClient secrets.SecretClient) (int, error) {
	ac, err := automationconfig.EnsureSecret(ctx, secretsClient, kube.ObjectKey(opsManager.Namespace, secretName), nil, automationConfig)
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
	for _, memberCluster := range r.GetHealthyMemberClusters() {
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

	fcVersion := opsManager.CalculateFeatureCompatibilityVersion()

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
		SetMongoDBVersion(rs.GetMongoDBVersion()).
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

			p.AuthSchemaVersion = om.CalculateAuthSchemaVersion()
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

func (r *ReconcileAppDbReplicaSet) generateProcessHostnames(opsManager *omv1.MongoDBOpsManager) []string {
	var hostnames []string
	// We want all clusters to generate stable process list in case of some clusters being down. Process list cannot change regardless of the cluster health.
	for _, memberCluster := range r.getAllMemberClusters() {
		hostnames = append(hostnames, r.generateProcessHostnamesForCluster(opsManager, memberCluster)...)
	}

	return hostnames
}

func (r *ReconcileAppDbReplicaSet) generateProcessHostnamesForCluster(opsManager *omv1.MongoDBOpsManager, memberCluster multicluster.MemberCluster) []string {
	members := scale.ReplicasThisReconciliation(scalers.GetAppDBScaler(opsManager, memberCluster.Name, r.getMemberClusterIndex(memberCluster.Name), r.memberClusters))

	if opsManager.Spec.AppDB.IsMultiCluster() {
		return dns.GetMultiClusterProcessHostnames(opsManager.Spec.AppDB.GetName(), opsManager.GetNamespace(), memberCluster.Index, members, opsManager.Spec.AppDB.GetClusterDomain(), opsManager.Spec.AppDB.GetExternalDomainForMemberCluster(memberCluster.Name))
	}

	hostnames, _ := dns.GetDNSNames(opsManager.Spec.AppDB.GetName(), opsManager.Spec.AppDB.ServiceName(), opsManager.GetNamespace(), opsManager.Spec.AppDB.GetClusterDomain(), members, opsManager.Spec.AppDB.GetExternalDomain())
	return hostnames
}

func (r *ReconcileAppDbReplicaSet) generateProcessList(opsManager *omv1.MongoDBOpsManager) []automationconfig.Process {
	var processList []automationconfig.Process
	// We want all clusters to generate stable process list in case of some clusters being down. Process list cannot change regardless of the cluster health.
	for _, memberCluster := range r.getAllMemberClusters() {
		hostnames := r.generateProcessHostnamesForCluster(opsManager, memberCluster)
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
		hostnames := r.generateProcessHostnamesForCluster(opsManager, memberCluster)
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
			log.Debugf("Host %s is already registered with group %s", hostname, conn.GroupID())
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

// addPreferredHostnames will add the hostnames as preferred in Ops Manager
// Ops Manager does not check for duplicates, so we need to treat it here.
func (r *ReconcileAppDbReplicaSet) addPreferredHostnames(ctx context.Context, conn om.Connection, opsManager *omv1.MongoDBOpsManager, agentApiKey string, hostnames []string) error {
	existingPreferredHostnames, err := conn.GetPreferredHostnames(agentApiKey)
	if err != nil {
		return err
	}

	existingPreferredHostnamesMap := make(map[string]om.PreferredHostname)
	for _, hostname := range existingPreferredHostnames {
		existingPreferredHostnamesMap[hostname.Value] = hostname
	}

	for _, hostname := range hostnames {
		if _, ok := existingPreferredHostnamesMap[hostname]; !ok {
			err := conn.AddPreferredHostname(agentApiKey, hostname, false)
			if err != nil {
				return err
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
func (r *ReconcileAppDbReplicaSet) ensureAppDbAgentApiKey(ctx context.Context, opsManager *omv1.MongoDBOpsManager, conn om.Connection, projectID string, log *zap.SugaredLogger) (string, error) {
	var appdbSecretPath string
	if r.VaultClient != nil {
		appdbSecretPath = r.VaultClient.AppDBSecretPath()
	}

	agentKey := ""
	for _, memberCluster := range r.GetHealthyMemberClusters() {
		if agentKeyFromSecret, err := agents.EnsureAgentKeySecretExists(ctx, memberCluster.SecretClient, conn, opsManager.Namespace, agentKey, projectID, appdbSecretPath, log); err != nil {
			return "", xerrors.Errorf("error ensuring agent key secret exists in cluster %s: %w", memberCluster.Name, err)
		} else if agentKey == "" {
			agentKey = agentKeyFromSecret
		}
	}

	return agentKey, nil
}

// tryConfigureMonitoringInOpsManager attempts to configure monitoring in Ops Manager. This might not be possible if Ops Manager
// has not been created yet, if that is the case, an empty PodVars will be returned.
func (r *ReconcileAppDbReplicaSet) tryConfigureMonitoringInOpsManager(ctx context.Context, opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string, agentCertPath string, log *zap.SugaredLogger) (env.PodEnvVars, error) {
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
		AutoPEMKeyFilePath: agentCertPath,
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

	hostnames := r.generateProcessHostnames(opsManager)
	if err != nil {
		return existingPodVars, xerrors.Errorf("error getting current appdb statefulset hostnames: %w", err)
	}

	if err := r.registerAppDBHostsWithProject(hostnames, conn, opsManagerUserPassword, log); err != nil {
		return existingPodVars, xerrors.Errorf("error registering hosts with project: %w", err)
	}

	agentApiKey, err := r.ensureAppDbAgentApiKey(ctx, opsManager, conn, conn.GroupID(), log)
	if err != nil {
		return existingPodVars, xerrors.Errorf("error ensuring AppDB agent api key: %w", err)
	}

	if err := markAppDBAsBackingProject(conn, log); err != nil {
		return existingPodVars, xerrors.Errorf("error marking project has backing db: %w", err)
	}

	if err := r.ensureProjectIDConfigMap(ctx, opsManager, conn.GroupID()); err != nil {
		return existingPodVars, xerrors.Errorf("error creating ConfigMap: %w", err)
	}

	if err := r.addPreferredHostnames(ctx, conn, opsManager, agentApiKey, hostnames); err != nil {
		return existingPodVars, xerrors.Errorf("error adding preferred hostnames: %w", err)
	}

	return env.PodEnvVars{User: conn.PublicKey(), ProjectID: conn.GroupID(), SSLProjectConfig: env.SSLProjectConfig{
		SSLMMSCAConfigMap: opsManager.Spec.GetOpsManagerCA(),
	}}, nil
}

func (r *ReconcileAppDbReplicaSet) ensureProjectIDConfigMap(ctx context.Context, opsManager *omv1.MongoDBOpsManager, projectID string) error {
	var errs error
	for _, memberCluster := range r.GetHealthyMemberClusters() {
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
		SetLabels(opsManager.GetOwnerLabels()).
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

func (r *ReconcileAppDbReplicaSet) publishACVersionAsConfigMap(ctx context.Context, cmName string, opsManager *omv1.MongoDBOpsManager, version int, memberCluster multicluster.MemberCluster) workflow.Status {
	labels := opsManager.GetOwnerLabels()

	acVersionConfigMap := configmap.Builder().
		SetLabels(labels).
		SetNamespace(opsManager.Namespace).
		SetName(cmName).
		SetDataField(appDBACConfigMapVersionField, fmt.Sprintf("%d", version)).
		Build()
	if err := configmap.CreateOrUpdate(ctx, memberCluster.Client, acVersionConfigMap); err != nil {
		return workflow.Failed(xerrors.Errorf("error creating automation config map in cluster %s: %w", memberCluster.Name, err))
	}

	return workflow.OK()
}

// deployAutomationConfig updates the Automation Config secret if necessary and waits for the pods to fall to "not ready"
// In this case the next StatefulSet update will be safe as the rolling upgrade will wait for the pods to get ready
func (r *ReconcileAppDbReplicaSet) deployAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, prometheusCertHash string, memberCluster multicluster.MemberCluster, log *zap.SugaredLogger) (int, workflow.Status) {
	rs := opsManager.Spec.AppDB

	config, err := r.buildAppDbAutomationConfig(ctx, opsManager, automation, prometheusCertHash, memberCluster.Name, log)
	if err != nil {
		return 0, workflow.Failed(err)
	}
	var configVersion int
	if configVersion, err = r.publishAutomationConfig(ctx, opsManager, config, rs.AutomationConfigSecretName(), memberCluster.SecretClient); err != nil {
		return 0, workflow.Failed(err)
	}

	if workflowStatus := r.publishACVersionAsConfigMap(ctx, opsManager.Spec.AppDB.AutomationConfigConfigMapName(), opsManager, configVersion, memberCluster); !workflowStatus.IsOK() {
		return 0, workflowStatus
	}

	monitoringAc, err := r.buildAppDbAutomationConfig(ctx, opsManager, monitoring, UnusedPrometheusConfiguration, memberCluster.Name, log)
	if err != nil {
		return 0, workflow.Failed(err)
	}

	if err := r.deployMonitoringAgentAutomationConfig(ctx, opsManager, memberCluster, log); err != nil {
		return 0, workflow.Failed(err)
	}

	if workflowStatus := r.publishACVersionAsConfigMap(ctx, opsManager.Spec.AppDB.MonitoringAutomationConfigConfigMapName(), opsManager, monitoringAc.Version, memberCluster); !workflowStatus.IsOK() {
		return 0, workflowStatus
	}

	return configVersion, workflow.OK()
}

// deployMonitoringAgentAutomationConfig deploys the monitoring agent's automation config.
func (r *ReconcileAppDbReplicaSet) deployMonitoringAgentAutomationConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, memberCluster multicluster.MemberCluster, log *zap.SugaredLogger) error {
	config, err := r.buildAppDbAutomationConfig(ctx, opsManager, monitoring, UnusedPrometheusConfiguration, memberCluster.Name, log)
	if err != nil {
		return err
	}
	if _, err = r.publishAutomationConfig(ctx, opsManager, config, opsManager.Spec.AppDB.MonitoringAutomationConfigSecretName(), memberCluster.SecretClient); err != nil {
		return err
	}
	return nil
}

// GetAppDBUpdateStrategyType returns the update strategy type the AppDB Statefulset needs to be configured with.
// This depends on whether a version change is in progress.
func (r *ReconcileAppDbReplicaSet) GetAppDBUpdateStrategyType(om *omv1.MongoDBOpsManager) appsv1.StatefulSetUpdateStrategyType {
	if !r.isChangingVersion(om) {
		return appsv1.RollingUpdateStatefulSetStrategyType
	}
	return appsv1.OnDeleteStatefulSetStrategyType
}

func (r *ReconcileAppDbReplicaSet) deployStatefulSet(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger, podVars env.PodEnvVars, appdbOpts construct.AppDBStatefulSetOptions) workflow.Status {
	if err := r.createServices(ctx, opsManager, log); err != nil {
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
		if err := r.deployMonitoringAgentAutomationConfig(ctx, opsManager, memberCluster, log); err != nil {
			return workflow.Failed(err)
		}

		updateStrategy := r.GetAppDBUpdateStrategyType(opsManager)

		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, appdbOpts, scaler, updateStrategy, log)
		if err != nil {
			return workflow.Failed(xerrors.Errorf("can't construct AppDB Statefulset: %w", err))
		}

		if workflowStatus := r.deployStatefulSetInMemberCluster(ctx, opsManager, appDbSts, memberCluster.Name, log); !workflowStatus.IsOK() {
			return workflowStatus
		}

		// we want to deploy all stateful sets the first time we're deploying stateful sets
		if scaler.ScalingFirstTime() {
			scalingFirstTime = true
			continue
		}

		if workflowStatus := statefulset.GetStatefulSetStatus(ctx, opsManager.Namespace, opsManager.Spec.AppDB.NameForCluster(memberCluster.Index), memberCluster.Client); !workflowStatus.IsOK() {
			return workflowStatus
		}

		if err := statefulset.ResetUpdateStrategy(ctx, opsManager.GetVersionedImplForMemberCluster(r.getMemberClusterIndex(memberCluster.Name)), memberCluster.Client); err != nil {
			return workflow.Failed(xerrors.Errorf("can't reset AppDB StatefulSet UpdateStrategyType: %w", err))
		}
	}

	// if this is the first time deployment, then we need to wait for all stateful sets to become ready after deploying all of them
	if scalingFirstTime {
		for _, memberCluster := range r.GetHealthyMemberClusters() {
			if workflowStatus := statefulset.GetStatefulSetStatus(ctx, opsManager.Namespace, opsManager.Spec.AppDB.NameForCluster(memberCluster.Index), memberCluster.Client); !workflowStatus.IsOK() {
				return workflowStatus
			}

			if err := statefulset.ResetUpdateStrategy(ctx, opsManager.GetVersionedImplForMemberCluster(r.getMemberClusterIndex(memberCluster.Name)), memberCluster.Client); err != nil {
				return workflow.Failed(xerrors.Errorf("can't reset AppDB StatefulSet UpdateStrategyType: %w", err))
			}
		}
	}

	for k, v := range currentClusterSpecs {
		r.deploymentState.LastAppliedMemberSpec[k] = v
	}

	return workflow.OK()
}

// This method creates the following services:
// - external services for Single Cluster deployments
// - external services for Multi Cluster deployments
// - pod services for Multi Cluster deployments
// Note that this does not create any non-external services for Single Cluster deployments
// Those services are created by the method create.AppDBInKubernetes
func (r *ReconcileAppDbReplicaSet) createServices(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	for _, memberCluster := range r.GetHealthyMemberClusters() {
		clusterSpecItem := opsManager.Spec.AppDB.GetMemberClusterSpecByName(memberCluster.Name)

		for podIdx := 0; podIdx < clusterSpecItem.Members; podIdx++ {

			// Configures external service for both single and multi cluster deployments
			// This will also delete external services if the externalAccess configuration is removed
			if opsManager.Spec.AppDB.GetExternalAccessConfigurationForMemberCluster(memberCluster.Name) != nil {
				svc := getAppDBExternalService(opsManager.Spec.AppDB, memberCluster.Index, memberCluster.Name, podIdx)
				placeholderReplacer := getPlaceholderReplacer(opsManager.Spec.AppDB, memberCluster, podIdx)

				if processedAnnotations, replacedFlag, err := placeholderReplacer.ProcessMap(svc.Annotations); err != nil {
					return xerrors.Errorf("failed to process annotations in external service %s in cluster %s: %w", svc.Name, memberCluster.Name, err)
				} else if replacedFlag {
					log.Debugf("Replaced placeholders in annotations in external service %s in cluster: %s. Annotations before: %+v, annotations after: %+v", svc.Name, memberCluster.Name, svc.Annotations, processedAnnotations)
					svc.Annotations = processedAnnotations
				}

				if err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil && !apiErrors.IsAlreadyExists(err) {
					return xerrors.Errorf("failed to create external service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
				}
			} else {
				svcName := opsManager.Spec.AppDB.GetExternalServiceName(memberCluster.Index, podIdx)
				namespacedName := kube.ObjectKey(opsManager.Spec.AppDB.Namespace, svcName)
				if err := mekoService.DeleteServiceIfItExists(ctx, memberCluster.Client, namespacedName); err != nil {
					return xerrors.Errorf("failed to remove external service %s in cluster: %s, err: %w", svcName, memberCluster.Name, err)
				}
			}

			// Configures pod services for multi cluster deployments
			if opsManager.Spec.AppDB.IsMultiCluster() && opsManager.Spec.AppDB.GetExternalDomainForMemberCluster(memberCluster.Name) == nil {
				svc := getAppDBPodService(opsManager.Spec.AppDB, memberCluster.Index, podIdx)
				svc.Name = dns.GetMultiServiceName(opsManager.Spec.AppDB.Name(), memberCluster.Index, podIdx)
				err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc)
				if err != nil && !apiErrors.IsAlreadyExists(err) {
					return xerrors.Errorf("failed to create service: %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
				}
			}
		}
	}

	return nil
}

// deployStatefulSetInMemberCluster updates the StatefulSet spec and returns its status (if it's ready or not)
func (r *ReconcileAppDbReplicaSet) deployStatefulSetInMemberCluster(ctx context.Context, opsManager *omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, memberClusterName string, log *zap.SugaredLogger) workflow.Status {
	workflowStatus := create.HandlePVCResize(ctx, r.getMemberCluster(memberClusterName).Client, &appDbSts, log)
	if !workflowStatus.IsOK() {
		return workflowStatus
	}

	if workflow.ContainsPVCOption(workflowStatus.StatusOptions()) {
		if _, err := r.updateStatus(ctx, opsManager, workflow.Pending(""), log, workflowStatus.StatusOptions()...); err != nil {
			return workflow.Failed(xerrors.Errorf("error updating status: %w", err))
		}
	}

	serviceSelectorLabel := opsManager.Spec.AppDB.HeadlessServiceSelectorAppLabel(r.getMemberCluster(memberClusterName).Index)
	if err := create.AppDBInKubernetes(ctx, r.getMemberCluster(memberClusterName).Client, opsManager, appDbSts, serviceSelectorLabel, log); err != nil {
		return workflow.Failed(err)
	}

	return workflow.OK()
}

func (r *ReconcileAppDbReplicaSet) allAgentsReachedGoalState(ctx context.Context, manager *omv1.MongoDBOpsManager, targetConfigVersion int, log *zap.SugaredLogger) workflow.Status {
	for _, memberCluster := range r.GetHealthyMemberClusters() {
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

func (r *ReconcileAppDbReplicaSet) GetHealthyMemberClusters() []multicluster.MemberCluster {
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
	for _, memberCluster := range r.GetHealthyMemberClusters() {
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

// migrateToNewDeploymentState reads old config maps with the deployment state and writes them to the new deploymentState structure.
// This function is intended to be called only in the absence of the new deployment state config map.
// In this case, if the legacy config maps are also missing, then it means is a completely fresh deployments and this function does nothing.
func (r *ReconcileAppDbReplicaSet) migrateToNewDeploymentState(ctx context.Context, spec omv1.AppDBSpec, omAnnotations map[string]string) error {
	if legacyMemberClusterMapping, err := getLegacyMemberClusterMapping(ctx, spec.Namespace, spec.ClusterMappingConfigMapName(), r.client); err != nil {
		if !apiErrors.IsNotFound(err) && spec.IsMultiCluster() {
			return err
		}
	} else {
		r.deploymentState.ClusterMapping = legacyMemberClusterMapping
	}

	if legacyLastAppliedMemberSpec, err := r.getLegacyLastAppliedMemberSpec(ctx, spec); err != nil {
		if !apiErrors.IsNotFound(err) {
			return err
		}
	} else {
		r.deploymentState.LastAppliedMemberSpec = legacyLastAppliedMemberSpec
	}

	if lastAppliedMongoDBVersion, found := omAnnotations[annotations.LastAppliedMongoDBVersion]; found {
		r.deploymentState.LastAppliedMongoDBVersion = lastAppliedMongoDBVersion
	}

	return nil
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
