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
	"encoding/json"
	"errors"
	"fmt"
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

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// ReconcileMongoDBMultiClusterLeader plans a MongoDBMultiCluster deployment across clusters by
// writing directives and publishing the automation config; it never touches workload resources
// (that is the member controller's job, gated by its local directive).
type ReconcileMongoDBMultiClusterLeader struct {
	localClient         kubernetesClient.Client // this cluster: the CR copy and its status
	transport           directiveTransport      // delivers directives to member clusters, including this one
	elector             Elector
	omConnectionFactory om.ConnectionFactory
	imageUrls           images.ImageUrls
	defaultArchitecture architectures.DefaultArchitecture
	forceEnterprise     bool
}

var _ reconcile.Reconciler = &ReconcileMongoDBMultiClusterLeader{}

func newMongoDBMultiClusterLeaderReconciler(localClient client.Client, transport directiveTransport, elector Elector, omConnectionFactory om.ConnectionFactory, imageUrls images.ImageUrls, defaultArchitecture architectures.DefaultArchitecture, forceEnterprise bool) *ReconcileMongoDBMultiClusterLeader {
	return &ReconcileMongoDBMultiClusterLeader{
		localClient:         kubernetesClient.NewClient(localClient),
		transport:           transport,
		elector:             elector,
		omConnectionFactory: omConnectionFactory,
		imageUrls:           imageUrls,
		defaultArchitecture: defaultArchitecture,
		forceEnterprise:     forceEnterprise,
	}
}

// MongoDBMultiCluster Resource (leader role)
// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbmulticluster,mongodbmulticluster/status},verbs=get;list;watch;patch;update,namespace=placeholder
// +kubebuilder:rbac:groups=operator.mongodb.com,resources=mongodbdirectives,verbs=get;list;watch;create;update,namespace=placeholder
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update,namespace=placeholder

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
	if floor := observedTermFloor(snapshot); floor > 0 {
		// the elector cannot read Ops Manager or peer directives: push the highest term the
		// world carries as the candidacy floor (T16), so terms stay monotonic even when every
		// lease was lost — a new leader always outranks anything already written. The AC term
		// alone is not enough: directives are rewritten on every takeover, the AC only when it
		// changes, so after a total lease loss the directives usually carry the higher term
		// (found live: a wiped ensemble healed to the AC's floor and then wedged forever on a
		// directive from a later term).
		r.elector.ObserveTermFloor(request.NamespacedName, floor)
	}
	decision := plan(snapshot)
	log.Infof("Planned decision %s: %s", decision.Kind, decision.Reason)
	for _, t := range snapshot.Targets {
		log.Debugf("Cluster %s: %s", t.ClusterName, classifyCluster(snapshot.Directives[t.ClusterName], snapshot.SpecHash))
	}

	return r.execute(ctx, &mrs, conn, decision, log, clusterStatusOption(snapshot))
}

// observedTermFloor is the highest leadership term readable anywhere in the snapshot: the
// AC-stamped term and every visible directive's term. Zero when nothing readable carries one.
func observedTermFloor(s plannerSnapshot) int64 {
	var floor int64
	if s.AC.Read {
		floor = s.AC.LeadershipTerm
	}
	for _, d := range s.Directives {
		if d.Exists && d.Spec.LeadershipTerm > floor {
			floor = d.Spec.LeadershipTerm
		}
	}
	return floor
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
// — never workflow.Merge: one decision, one status. The statusOptions ride every write.
func (r *ReconcileMongoDBMultiClusterLeader) execute(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, conn om.Connection, decision planDecision, log *zap.SugaredLogger, statusOptions ...status.Option) (reconcile.Result, error) {
	switch decision.Kind {
	case decisionWriteDirective:
		if err := r.transport.WriteDirective(ctx, decision.TargetCluster, kube.ObjectKey(mrs.Namespace, mrs.Name), decision.DirectiveSpec); err != nil {
			return reconcile.Result{}, xerrors.Errorf("failed writing the directive to cluster %s: %w", decision.TargetCluster, err)
		}
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Pending("%s", decision.Reason), log, statusOptions...)
	case decisionWriteAC:
		if err := r.publishAutomationConfig(ctx, conn, mrs, *decision.AC, log); err != nil {
			return reconcile.Result{}, xerrors.Errorf("failed publishing the automation config: %w", err)
		}
		if err := r.saveLastAchievedSpec(ctx, mrs); err != nil {
			// the AC write already happened; the record is best-effort and the next pass retries
			log.Warnf("Failed saving the last achieved spec annotation: %s", err)
		}
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Pending("%s", decision.Reason), log, statusOptions...)
	case decisionInvalidSpec:
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Failed(errors.New(decision.Reason)), log, statusOptions...)
	case decisionNotProgressing:
		return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.Pending("%s", decision.Reason), log, statusOptions...)
	}
	return commoncontroller.UpdateStatus(ctx, r.localClient, mrs, workflow.OK(), log, statusOptions...)
}

// saveLastAchievedSpec records, on the leader's OWN cluster copy only, the spec whose
// additionalMongodConfig was just merged into the AC — the diff base that lets a later removal
// of an option actually un-merge it (GetLastAdditionalMongodConfig reads this annotation).
// Annotations survive GitOps applies; a leadership change loses the record, so the first removal
// after a failover is a one-time no-op. Reusing util.LastAchievedSpec also rides the watch
// predicate's self-write suppression. The legacy analog additionally stores achieved counts,
// member ids and roles; only additionalMongodConfig is consumed here.
func (r *ReconcileMongoDBMultiClusterLeader) saveLastAchievedSpec(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster) error {
	achievedSpecBytes, err := json.Marshal(mrs.Spec)
	if err != nil {
		return err
	}
	return annotations.SetAnnotations(ctx, mrs, map[string]string{util.LastAchievedSpec: string(achievedSpecBytes)}, r.localClient)
}

// publishAutomationConfig is the leader-only, non-blocking sibling of the legacy
// updateOmDeploymentRs (mongodbmultireplicaset_controller.go): the same composition — existing
// process ids reused by name, NewMultiClusterReplicaSetWithProcesses, ReconcileReplicaSetAC —
// minus the blocking waits, which the staged directive facts replace (agentRegistered gates this
// write, inGoalState gates the next step), and minus auth/backup (cut from the POC; an auth-less
// Security makes their AC hooks no-op anyway). TLS rides the legacy AC path unchanged:
// ConfigureTLS consumes the spec, and the cert path embeds the PEM hash computed from the
// leader's OWN pre-provisioned copy of the source secret — correct for every cluster because the
// installer contract makes all copies byte-identical.
func (r *ReconcileMongoDBMultiClusterLeader) publishAutomationConfig(ctx context.Context, conn om.Connection, mrs *mdbmultiv1.MongoDBMultiCluster, payload acPayload, log *zap.SugaredLogger) error {
	existingDeployment, err := conn.ReadDeployment()
	if err != nil {
		return err
	}
	processIds := getReplicaSetProcessIdsFromReplicaSets(mrs.Name, existingDeployment)

	// the planner's allocation map fixes each cluster's index; counts are the planned membership
	allocations := map[string]int{}
	for _, view := range r.transport.ReadDirectives(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name), log) {
		for cluster, index := range view.Spec.IndexAllocations {
			allocations[cluster] = index
		}
	}
	counts := make([]process.ClusterProcessCount, 0, len(payload.MemberCounts))
	for cluster, memberCount := range payload.MemberCounts {
		counts = append(counts, process.ClusterProcessCount{ClusterName: cluster, ClusterIndex: allocations[cluster], MemberCount: memberCount})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].ClusterIndex < counts[j].ClusterIndex })

	// ReadHashFromSecret falls back to "" silently (missing secret, wrong type, Vault miss);
	// publishing certificateKeyFile: "" with TLS on would be wrong-but-running, so it is an error
	tlsCertPath := ""
	if mrs.Spec.GetSecurity().IsTLSEnabled() {
		certSecretName := mrs.Spec.GetSecurity().MemberCertificateSecretName(mrs.Name)
		tlsCertHash := enterprisepem.ReadHashFromSecret(ctx, secrets.SecretClient{KubeClient: r.localClient}, mrs.Namespace, certSecretName, "", log)
		if tlsCertHash == "" {
			return xerrors.Errorf("TLS is enabled but no PEM hash could be read from the source cert secret %s on this cluster: the secret is missing or not of type kubernetes.io/tls", certSecretName)
		}
		tlsCertPath = fmt.Sprintf("%s/%s", util.TLSCertMountPath, tlsCertHash)
	}

	processes := process.CreateMongodProcessesMultiFromCounts(r.imageUrls[util.MongodbImageEnv], r.forceEnterprise, *mrs, counts, tlsCertPath, r.defaultArchitecture)
	rs := om.NewMultiClusterReplicaSetWithProcesses(om.NewReplicaSet(mrs.Name, mrs.Spec.Version), processes, process.MemberOptionsFromCounts(*mrs, counts), processIds, mrs.Spec.Connectivity)

	specHash, err := multiClusterSpecHash(mrs.Spec)
	if err != nil {
		return err
	}
	caFilePath := fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)
	err = conn.ReadUpdateDeployment(func(d om.Deployment) error {
		if err := ReconcileReplicaSetAC(ctx, d, mrs.Spec.DbCommonSpec, mrs.GetLastAdditionalMongodConfig(), mrs.Name, rs, caFilePath, "", nil, log); err != nil {
			return err
		}
		// the term and content hash piggyback on a write we are making anyway, never standalone
		d.SetOperatorLeadershipTerm(payload.LeadershipTerm)
		d.SetOperatorSpecHash(specHash)
		return nil
	}, log)
	if err != nil {
		return err
	}

	if _, err := ReconcileLogRotateSetting(conn, mrs.Spec.Agent, log); err != nil {
		return err
	}
	return nil
}

// enqueueSameNameRequest maps an event on a coupled object to the same-name request: the
// directive of a deployment is named after its MongoDBMultiCluster CR, in the same namespace.
func enqueueSameNameRequest(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: kube.ObjectKeyFromApiObject(o)}}
}

// AddMongoDBMultiClusterLeaderController creates the leader controller and adds it to the Manager.
func AddMongoDBMultiClusterLeaderController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster, elector Elector, omConnectionFactory om.ConnectionFactory, imageUrls images.ImageUrls, defaultArchitecture architectures.DefaultArchitecture, forceEnterprise bool, maxConcurrentReconciles int) error {
	transport := newAPIServerTransportFromClusters(memberClustersMap)
	reconciler := newMongoDBMultiClusterLeaderReconciler(mgr.GetClient(), transport, elector, omConnectionFactory, imageUrls, defaultArchitecture, forceEnterprise)
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

	// the elector wakes us on leadership transitions; a static elector has none to signal
	if events := elector.Events(); events != nil {
		if err := c.Watch(source.Channel[client.Object](events, &handler.EnqueueRequestForObject{})); err != nil {
			return xerrors.Errorf("failed to set the elector transition watch: %w", err)
		}
	}

	zap.S().Infof("Registered controller %s", util.MongoDbMultiClusterLeaderController)
	return nil
}
