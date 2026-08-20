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
	"reflect"

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

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ReconcileMongoDBMultiClusterLeader plans a MongoDBMultiCluster deployment across clusters by
// writing directives; it never touches workload resources (that is the member controller's job,
// gated by its local directive).
type ReconcileMongoDBMultiClusterLeader struct {
	localClient             kubernetesClient.Client            // this cluster: the CR copy and its status
	memberClusterClientsMap map[string]kubernetesClient.Client // holds the client for each of the member clusters, including this one
	elector                 Elector
}

var _ reconcile.Reconciler = &ReconcileMongoDBMultiClusterLeader{}

func newMongoDBMultiClusterLeaderReconciler(localClient client.Client, memberClustersMap map[string]client.Client, elector Elector) *ReconcileMongoDBMultiClusterLeader {
	clientsMap := make(map[string]kubernetesClient.Client)
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	return &ReconcileMongoDBMultiClusterLeader{
		localClient:             kubernetesClient.NewClient(localClient),
		memberClusterClientsMap: clientsMap,
		elector:                 elector,
	}
}

// MongoDBMultiCluster Resource (leader role)
// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbmulticluster,mongodbmulticluster/status},verbs=get;list;watch;patch;update,namespace=placeholder
// +kubebuilder:rbac:groups=operator.mongodb.com,resources=mongodbdirectives,verbs=get;list;watch;create;update,namespace=placeholder

// Reconcile plans one pass for a MongoDBMultiCluster deployment. M1 shape: naive full grant —
// every cluster in the spec gets its directive advanced straight to the desired state. The
// planner milestone replaces the grant with plan(snapshot) → one decision per pass.
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

	specHash, err := multiClusterSpecHash(mrs.Spec)
	if err != nil {
		return commoncontroller.UpdateStatus(ctx, r.localClient, &mrs, workflow.Failed(err), log)
	}

	// deliberately the raw list, not GetClusterSpecItems(): its last-achieved sequencing belongs
	// to the legacy single-reconciler path; here sequencing is the planner milestone's job
	clusterSpecList := mrs.Spec.ClusterSpecList
	indexAllocations := make(map[string]int, len(clusterSpecList))
	for i, item := range clusterSpecList {
		indexAllocations[item.ClusterName] = i
	}

	var errs error
	for i, item := range clusterSpecList {
		memberClient, ok := r.memberClusterClientsMap[item.ClusterName]
		if !ok {
			errs = errors.Join(errs, xerrors.Errorf("no client for member cluster %s", item.ClusterName))
			continue
		}
		directiveSpec := operatorv1.MongoDBDirectiveSpec{
			ClusterName:      item.ClusterName,
			LeadershipTerm:   term,
			TargetSpecHash:   specHash,
			MemberCount:      item.Members,
			ClusterIndex:     i,
			IndexAllocations: indexAllocations,
		}
		if err := r.createOrUpdateDirective(ctx, memberClient, &mrs, directiveSpec); err != nil {
			errs = errors.Join(errs, xerrors.Errorf("failed writing the directive to cluster %s: %w", item.ClusterName, err))
		}
	}
	if errs != nil {
		return reconcile.Result{}, errs
	}

	return commoncontroller.UpdateStatus(ctx, r.localClient, &mrs, workflow.Pending("Directives written (term %d); waiting for member clusters to reach goal state", term), log)
}

// createOrUpdateDirective puts the desired directive spec on one member cluster. The directive
// carries no owner reference: it is leader-managed and usually lives on a foreign cluster, where
// an owner reference would never be garbage-collected.
func (r *ReconcileMongoDBMultiClusterLeader) createOrUpdateDirective(ctx context.Context, memberClient kubernetesClient.Client, mrs *mdbmultiv1.MongoDBMultiCluster, directiveSpec operatorv1.MongoDBDirectiveSpec) error {
	directive := operatorv1.MongoDBDirective{}
	err := memberClient.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name), &directive)
	if apiErrors.IsNotFound(err) {
		directive = operatorv1.MongoDBDirective{
			ObjectMeta: metav1.ObjectMeta{Name: mrs.Name, Namespace: mrs.Namespace},
			Spec:       directiveSpec,
		}
		return memberClient.Create(ctx, &directive)
	}
	if err != nil {
		return err
	}

	directive.Spec = directiveSpec
	return memberClient.Update(ctx, &directive)
}

// enqueueSameNameRequest maps an event on a coupled object to the same-name request: the
// directive of a deployment is named after its MongoDBMultiCluster CR, in the same namespace.
func enqueueSameNameRequest(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: kube.ObjectKeyFromApiObject(o)}}
}

// AddMongoDBMultiClusterLeaderController creates the leader controller and adds it to the Manager.
func AddMongoDBMultiClusterLeaderController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster, elector Elector, maxConcurrentReconciles int) error {
	reconciler := newMongoDBMultiClusterLeaderReconciler(mgr.GetClient(), multicluster.ClustersMapToClientMap(memberClustersMap), elector)
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
