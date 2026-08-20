package operator

/*
Pseudo code for now. + maybe find a better name for the file

This controller watches:
- Directive CRD of all member cluster, to observe their status (poll vs informer TBD)
- The lease CRDs of all member clusters (poll vs informer TBD)
  Note (2026-08-19): checked kubernetes-sigs/multicluster-runtime -> not for us. It targets the hub topology
  (one manager reconciling many clusters, dynamic discovery via providers); we go the opposite way.
  If we want foreign informers later: main.go:246 already does cluster.New per member + mgr.Add (vanilla
  controller-runtime), WatchesRawSource on those caches gives cross-cluster watches with zero new deps.
  Lease reads/writes must bypass any cache regardless (CAS on a stale read is wrong by construction).
- The MDBMulticluster CRD, to trigger new changes observed

It writes to:
- Directive Spec of all clusters
- the automation config

Every write carries extra fields for the consumer to check:
- directive spec: leadership term + targetSpecHash (the member fences on both)
- AC: embed the leadership term in the payload -> audit trail + term floor for the majority-loss DR runbook.
  Where to store it, verified (2026-08-19): namespaced key inside the AC top-level "options" object -> schemaless map, round-trips PUT->store->GET. A new top-level field is REJECTED (strict Jackson). TBD: confirm the agent ignores the unknown options key.
  TODO 4 RESOLVED 2026-08-19: OM public API has NO client CAS. Payload version is ignored (validation commented out), server rebuilds from its own in-request read, stale writer wins ("later modification wins"). Internal 409 = same-instant races only.
  => the hold-off IS the only AC protection. API-side locking = CLOUDP-373090 (OM team, ride it).

Has the kubernetes permissions for foreign cluster for:
- Writing directive specs (in same namespace, TBD if we support cross namespace)
- Reading directive status
- Read/write the lease object



Role:
Any action is decided by collecting the state of the world, computing the current step with plan. Then it acts (state machine). one action is performed, then we return from the reconcile loop (with eventually a delay)

What goes in snapshot (feeds the plan function):
- Time
- Lease term
- Desired MDBMulti Spec + hash
- All directives (even removed clusters' ones)
- Current AC

- Validate the user written MDBMulti Spec
- Prepare OM connection
- publish OM config updates
- give directives to member clusters for making progress on kube resources updates:
  - compute next scaling (use the scaler object) and pass it via directive
  -
- compute automation config state, push it (must be the only writer) at the right time. based on its state machine. Re-uses "publishAutomationConfigFirst" boolean, to decide if it's before or after STS modifications in kube.
- set historical and current cluster indexes (in the directive), majority write

Checks:
- Ensuring it is the leader before anything
- Takeover hold-off: after winning an election, wait one lease DURATION (wall clock, ~10s) before the first guarded write.
  Not "a term": term = version number of leadership (44th, 45th president), duration = timeout (the parking meter). The wait is sized so a zombie's in-flight write lands before our first fresh read.

Leader election / renewal machinery: full protocol in .spike/poc/leader-election-protocol.md (2026-08-19).
Shape: elector as a separate manager.Runnable (own ticker — heartbeats never queue behind a slow OM PUT).
Talks to this controller via: elector.Current() (term, isLeader) read once at snapshot time (a term, never a bare bool),
GenericEvent into a source.Channel to wake us on transitions, optional ctx cancel on loss.
Renewal = re-prove majority every ~10s; on failure: stop guarded work, then step down.
Seam in code (2026-08-20): the Elector interface in mongodbmulticluster_elector.go; StaticElector
("am I the designated leader cluster?") stands in until the majority lease lands (roadmap M3.7).

*/

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// ReconcileMongoDBMultiClusterLeader plans a MongoDBMultiCluster deployment across clusters by
// writing directives and publishing the automation config; it never touches workload resources
// (that is the member controller's job, gated by its local directive).
type ReconcileMongoDBMultiClusterLeader struct {
	localClient             kubernetesClient.Client            // this cluster: the CR copy and its status
	memberClusterClientsMap map[string]kubernetesClient.Client // holds the client for each of the member clusters, including this one
	elector                 Elector
	omConnectionFactory     om.ConnectionFactory
	imageUrls               images.ImageUrls
	defaultArchitecture     architectures.DefaultArchitecture
}

var _ reconcile.Reconciler = &ReconcileMongoDBMultiClusterLeader{}

func newMongoDBMultiClusterLeaderReconciler(localClient client.Client, memberClustersMap map[string]client.Client, elector Elector, omConnectionFactory om.ConnectionFactory, imageUrls images.ImageUrls, defaultArchitecture architectures.DefaultArchitecture) *ReconcileMongoDBMultiClusterLeader {
	clientsMap := make(map[string]kubernetesClient.Client)
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	return &ReconcileMongoDBMultiClusterLeader{
		localClient:             kubernetesClient.NewClient(localClient),
		memberClusterClientsMap: clientsMap,
		elector:                 elector,
		omConnectionFactory:     omConnectionFactory,
		imageUrls:               imageUrls,
		defaultArchitecture:     defaultArchitecture,
	}
}

// MongoDBMultiCluster Resource (leader role)
// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbmulticluster,mongodbmulticluster/status},verbs=get;list;watch;patch;update,namespace=placeholder
// +kubebuilder:rbac:groups=operator.mongodb.com,resources=mongodbdirectives,verbs=get;list;watch;create;update,namespace=placeholder

// Reconcile plans one pass for a MongoDBMultiCluster deployment: assemble the snapshot, call the
// pure planner, execute exactly one decision, map it one-to-one to a workflow status.
func (r *ReconcileMongoDBMultiClusterLeader) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("MultiClusterLeader", request.NamespacedName)

	mrs := mdbmultiv1.MongoDBMultiCluster{}
	if err := r.localClient.Get(ctx, request.NamespacedName, &mrs); err != nil {
		if apiErrors.IsNotFound(err) {
			// TODO(decentralized-poc): directive cleanup on CR deletion is post-M1
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	term, isLeader := r.elector.Current(request.NamespacedName)
	if !isLeader {
		// no requeue: CR/directive events re-enqueue, and the real elector will wake us on
		// leadership transitions through a source.Channel
		log.Debug("Not the leader for this deployment, nothing to plan")
		return reconcile.Result{}, nil
	}
	log.Infof("-> MultiClusterLeader.Reconcile (term %d)", term)

	// same webhook validators the legacy path re-runs (warnings accumulate on the status in
	// memory and ride the status write); Failed instead of legacy's Invalid to keep one terminal
	// phase mapping with the planner's InvalidSpec
	if err := mrs.ProcessValidationsOnReconcile(nil); err != nil {
		return commoncontroller.UpdateStatus(ctx, r.localClient, &mrs, workflow.Failed(err), log)
	}

	specHash, err := multiClusterSpecHash(mrs.Spec)
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.localClient, &mrs, workflow.Failed(err), log)
	}

	conn, projectID, err := r.prepareReadOnlyConnection(ctx, &mrs, log)
	if err != nil {
		// transient: the pre-provisioned project ConfigMap/credentials/project are the
		// installer's contract; back off until they appear
		return reconcile.Result{}, err
	}

	snapshot := r.assembleSnapshot(ctx, &mrs, term, specHash, conn, projectID, log)
	decision := plan(snapshot)
	log.Infof("Planned decision %s: %s", decision.Kind, decision.Reason)
	for _, t := range snapshot.Targets {
		log.Debugf("Cluster %s: %s", t.ClusterName, classifyCluster(snapshot.Directives[t.ClusterName], snapshot.SpecHash))
	}

	return r.execute(ctx, &mrs, conn, decision, log)
}

// prepareReadOnlyConnection builds the leader's Ops Manager connection from the project
// ConfigMap and credentials Secret on its own cluster, discovering the pre-provisioned project
// read-only — never connection.PrepareOpsManagerConnection, which creates projects, retags them
// and generates agent keys.
func (r *ReconcileMongoDBMultiClusterLeader) prepareReadOnlyConnection(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger) (om.Connection, string, error) {
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.localClient, secrets.SecretClient{KubeClient: r.localClient}, mrs, log)
	if err != nil {
		return nil, "", err
	}
	omProject, conn, err := project.ReadProject(projectConfig, credsConfig, r.omConnectionFactory, log)
	if err != nil {
		return nil, "", err
	}
	return conn, omProject.ID, nil
}

// execute performs the one planned action and maps the decision one-to-one to a workflow status
// — never workflow.Merge: one decision, one status.
func (r *ReconcileMongoDBMultiClusterLeader) execute(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, conn om.Connection, decision planDecision, log *zap.SugaredLogger) (reconcile.Result, error) {
	switch decision.Kind {
	case decisionWriteDirective:
		memberClient, ok := r.memberClusterClientsMap[decision.TargetCluster]
		if !ok {
			return reconcile.Result{}, xerrors.Errorf("no client for member cluster %s", decision.TargetCluster)
		}
		if err := r.writeDirective(ctx, memberClient, kube.ObjectKey(mrs.Namespace, mrs.Name), decision.DirectiveSpec); err != nil {
			return reconcile.Result{}, xerrors.Errorf("failed writing the directive to cluster %s: %w", decision.TargetCluster, err)
		}
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Pending("%s", decision.Reason), log)
	case decisionWriteAC:
		if err := r.publishAutomationConfig(ctx, conn, mrs, *decision.AC, log); err != nil {
			return reconcile.Result{}, xerrors.Errorf("failed publishing the automation config: %w", err)
		}
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Pending("%s", decision.Reason), log)
	case decisionInvalidSpec:
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Failed(errors.New(decision.Reason)), log)
	case decisionNotProgressing:
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Pending("%s", decision.Reason), log)
	}
	return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.OK(), log)
}

// writeDirective puts the planned directive spec on one member cluster as a read-modify-write:
// the stored allocation map is unioned into the decision's (a stored entry the planner did not
// carry is preserved — a stale leader's single write must never regress a copy), AdvancedAt is
// persisted only when the instruction actually changed, and an unchanged spec skips the write
// entirely. A resourceVersion conflict (the member bumps it with status writes) is a transient
// error; controller-runtime retries. The directive carries no owner reference: it is
// leader-managed and usually lives on a foreign cluster, where GC would never fire.
func (r *ReconcileMongoDBMultiClusterLeader) writeDirective(ctx context.Context, memberClient kubernetesClient.Client, nsName types.NamespacedName, desired operatorv1.MongoDBDirectiveSpec) error {
	directive := operatorv1.MongoDBDirective{}
	err := memberClient.Get(ctx, nsName, &directive)
	if apiErrors.IsNotFound(err) {
		directive = operatorv1.MongoDBDirective{
			ObjectMeta: metav1.ObjectMeta{Name: nsName.Name, Namespace: nsName.Namespace},
			Spec:       desired,
		}
		return memberClient.Create(ctx, &directive)
	}
	if err != nil {
		return err
	}

	merged := maps.Clone(desired.IndexAllocations)
	for cluster, index := range directive.Spec.IndexAllocations {
		if _, ok := merged[cluster]; !ok {
			merged[cluster] = index
		}
	}
	desired.IndexAllocations = merged

	unchanged := desired
	unchanged.AdvancedAt = directive.Spec.AdvancedAt
	if reflect.DeepEqual(directive.Spec, unchanged) {
		return nil // write-quiescence; keeping the old AdvancedAt also keeps stuckness visible
	}

	directive.Spec = desired
	return memberClient.Update(ctx, &directive)
}

// publishAutomationConfig is the leader-only, non-blocking sibling of the legacy
// updateOmDeploymentRs (mongodbmultireplicaset_controller.go): the same composition — existing
// process ids reused by name, NewMultiClusterReplicaSetWithProcesses, ReconcileReplicaSetAC —
// minus the blocking waits, which the staged directive facts replace (agentRegistered gates this
// write, inGoalState gates the next step), and minus auth/TLS/log-rotation/backup (cut from the
// POC; a nil Security makes their AC hooks no-op anyway).
func (r *ReconcileMongoDBMultiClusterLeader) publishAutomationConfig(ctx context.Context, conn om.Connection, mrs *mdbmultiv1.MongoDBMultiCluster, payload acPayload, log *zap.SugaredLogger) error {
	existingDeployment, err := conn.ReadDeployment()
	if err != nil {
		return err
	}
	processIds := getReplicaSetProcessIdsFromReplicaSets(mrs.Name, existingDeployment)

	// the planner's allocation map fixes each cluster's index; counts are the planned membership
	allocations := map[string]int{}
	for _, view := range readDirectiveViews(ctx, r.memberClusterClientsMap, kube.ObjectKey(mrs.Namespace, mrs.Name), log) {
		for cluster, index := range view.Spec.IndexAllocations {
			allocations[cluster] = index
		}
	}
	counts := make([]process.ClusterProcessCount, 0, len(payload.MemberCounts))
	for cluster, memberCount := range payload.MemberCounts {
		counts = append(counts, process.ClusterProcessCount{ClusterName: cluster, ClusterIndex: allocations[cluster], MemberCount: memberCount})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].ClusterIndex < counts[j].ClusterIndex })

	// forceEnterprise is not wired in the POC (a main.go flag on the legacy path)
	processes := process.CreateMongodProcessesMultiFromCounts(r.imageUrls[util.MongodbImageEnv], false, *mrs, counts, "", r.defaultArchitecture)
	rs := om.NewMultiClusterReplicaSetWithProcesses(om.NewReplicaSet(mrs.Name, mrs.Spec.Version), processes, process.MemberOptionsFromCounts(*mrs, counts), processIds, mrs.Spec.Connectivity)

	caFilePath := fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)
	return conn.ReadUpdateDeployment(func(d om.Deployment) error {
		if err := ReconcileReplicaSetAC(ctx, d, mrs.Spec.DbCommonSpec, nil, mrs.Name, rs, caFilePath, "", nil, log); err != nil {
			return err
		}
		// the term piggybacks on a membership write we are making anyway, never standalone
		d.SetOperatorLeadershipTerm(payload.LeadershipTerm)
		return nil
	}, log)
}

// enqueueSameNameRequest maps an event on a coupled object to the same-name request: the
// directive of a deployment is named after its MongoDBMultiCluster CR, in the same namespace.
func enqueueSameNameRequest(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: kube.ObjectKeyFromApiObject(o)}}
}

// AddMongoDBMultiClusterLeaderController creates the leader controller and adds it to the Manager.
func AddMongoDBMultiClusterLeaderController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster, elector Elector, omConnectionFactory om.ConnectionFactory, imageUrls images.ImageUrls, defaultArchitecture architectures.DefaultArchitecture, maxConcurrentReconciles int) error {
	reconciler := newMongoDBMultiClusterLeaderReconciler(mgr.GetClient(), multicluster.ClustersMapToClientMap(memberClustersMap), elector, omConnectionFactory, imageUrls, defaultArchitecture)
	c, err := controller.New(util.MongoDbMultiClusterLeaderController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: maxConcurrentReconciles})
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbmultiv1.MongoDBMultiCluster{}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbmultiv1.MongoDBMultiCluster)
			newResource := e.ObjectNew.(*mdbmultiv1.MongoDBMultiCluster)

			oldSpecAnnotation := oldResource.GetAnnotations()[util.LastAchievedSpec]
			newSpecAnnotation := newResource.GetAnnotations()[util.LastAchievedSpec]

			// don't handle an update to just the previous spec annotation if they are not the same.
			// this prevents the operator triggering reconciliations on resource that it is updating itself.
			if !reflect.DeepEqual(oldSpecAnnotation, newSpecAnnotation) {
				return false
			}

			return reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		},
	}))
	if err != nil {
		return err
	}

	// directive statuses are the leader's planning input; watch them on every member cluster
	for clusterName, memberCluster := range memberClustersMap {
		err = c.Watch(source.Kind[client.Object](memberCluster.GetCache(), &operatorv1.MongoDBDirective{}, handler.EnqueueRequestsFromMapFunc(enqueueSameNameRequest)))
		if err != nil {
			return xerrors.Errorf("failed to set MongoDBDirective watch on member cluster %s: %w", clusterName, err)
		}
	}

	zap.S().Infof("Registered controller %s", util.MongoDbMultiClusterLeaderController)
	return nil
}
