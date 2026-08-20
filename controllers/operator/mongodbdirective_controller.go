package operator

/*
Pseudo code for now. + maybe find a better name for the file

This controller watches:
- the directive CRD, the main thing it reacts to, it gates changes to when the member actually has to act
- the MDBMulticluster CRD, to hash it when it changes, and to source the actual changes it has to make -> not the timing, but the content of what it must deploy (the name of the resource, the services configuration, TLS etc...). Ignores the member fields in here, instead they come from directive
-

This controller *can* run on the same cluster that is the leader. But the leader will be a completely separate loop that is doing planning only.

Order per reconcile: FIRST echo + report facts (unconditional, even if fences fail), THEN fences, THEN act or hold.
The echo = copy directive metadata.generation into status.observedGeneration. Means "I have SEEN instruction #N", not "I obeyed".
Without it, the leader can't tell "member holds deliberately" from "member never saw it".

Fences (before acting):
- directive targetSpecHash == hash of my local CR copy, else hold (spec fence)
- directive leadership term >= my local lease term, else hold (term fence)
  NOT strict equality: a newer term = legit leader elected without my cluster (I was the minority, my lease is just stale, next renewal catches it up). The fence rejects the past, not the future.

It writes to:
- STS, secrets, configmaps, services... all resources that are the actual workloads, or their configurations
- maybe certs
- the directive status (only the local one)
- the MDBMulticluster CRD status, according to what the leader instructs it to write, via the directive

- report progress in its directive status:
  - hash it observes
  - directive generation
  - was the sts applied ?
  - is the agent registered ?
  - am i in goal state ?

It needs to talk to OM for some status reporting (agent registered ? goal state ?), but *read only*. Never writes OM: single AC writer = the leader.

Has the kubernetes permissions to act on everything local. Must not write to directive *specs*



*/

import (
	"context"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// directiveHoldRetry is how long a holding member waits before re-observing the world.
const directiveHoldRetry = 10 * time.Second

// ReconcileMongoDBDirective is the member-side reconciler: it acts only on its local cluster,
// gated by the local directive. It writes the directive's status (the staged facts) and never
// its spec (the leader's channel). Ops Manager is observed read-only for the agent facts; the
// member never creates projects or agent keys (pre-provisioned).
type ReconcileMongoDBDirective struct {
	localClient kubernetesClient.Client
	elector     Elector

	imageUrls                         images.ImageUrls
	initDatabaseNonStaticImageVersion string
	databaseNonStaticImageVersion     string
	defaultArchitecture               architectures.DefaultArchitecture
	omConnectionFactory               om.ConnectionFactory
}

var _ reconcile.Reconciler = &ReconcileMongoDBDirective{}

func newMongoDBDirectiveReconciler(localClient client.Client, elector Elector, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, defaultArchitecture architectures.DefaultArchitecture, omConnectionFactory om.ConnectionFactory) *ReconcileMongoDBDirective {
	return &ReconcileMongoDBDirective{
		localClient:                       kubernetesClient.NewClient(localClient),
		elector:                           elector,
		imageUrls:                         imageUrls,
		initDatabaseNonStaticImageVersion: initDatabaseNonStaticImageVersion,
		databaseNonStaticImageVersion:     databaseNonStaticImageVersion,
		defaultArchitecture:               defaultArchitecture,
		omConnectionFactory:               omConnectionFactory,
	}
}

// MongoDBDirective Resource (member role)
// +kubebuilder:rbac:groups=operator.mongodb.com,resources={mongodbdirectives,mongodbdirectives/status},verbs=get;list;watch;patch;update,namespace=placeholder
// +kubebuilder:rbac:groups=mongodb.com,resources=mongodbmulticluster,verbs=get;list;watch,namespace=placeholder

// Reconcile runs one member pass, strictly ordered: echo the observations FIRST (unconditional,
// even when the fences fail), THEN fence, THEN act or hold.
func (r *ReconcileMongoDBDirective) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("MongoDBDirective", request.NamespacedName)

	directive := operatorv1.MongoDBDirective{}
	if err := r.localClient.Get(ctx, request.NamespacedName, &directive); err != nil {
		if apiErrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	log.Info("-> MongoDBDirective.Reconcile")

	// the local CR copy is the content source (what to deploy); the directive is the timing
	// gate. GitOps may not have delivered the matching copy yet.
	crFound := true
	localHash := ""
	mrs := mdbmultiv1.MongoDBMultiCluster{}
	if err := r.localClient.Get(ctx, request.NamespacedName, &mrs); err != nil {
		if !apiErrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		crFound = false
	} else {
		var err error
		if localHash, err = multiClusterSpecHash(mrs.Spec); err != nil {
			return reconcile.Result{}, err
		}
	}

	// the echo means "I have SEEN instruction #N", not "I obeyed"; without it the leader cannot
	// tell "member holds deliberately" from "member never saw it"
	if err := r.echoObservations(ctx, &directive, localHash); err != nil {
		return reconcile.Result{}, err
	}

	// term fence: reject the past, not the future — a NEWER term is a legitimate leader elected
	// while our locally observed term was stale
	term, _ := r.elector.Current(request.NamespacedName)
	if directive.Spec.LeadershipTerm < term {
		log.Debugf("Holding: directive term %d is older than the locally observed term %d", directive.Spec.LeadershipTerm, term)
		return r.holdAndRepair(ctx, &directive, log)
	}

	// spec fence: act only on an instruction planned from the exact spec we hold locally
	if !crFound || directive.Spec.TargetSpecHash != localHash {
		log.Debugf("Holding: the local spec copy (hash %q) does not match the directive's target spec hash %q", localHash, directive.Spec.TargetSpecHash)
		return r.holdAndRepair(ctx, &directive, log)
	}

	return r.act(ctx, &directive, &mrs, log)
}

// echoObservations copies the directive's generation and the local spec hash into the directive
// status. Equality-guarded so the unfiltered directive watch quiesces instead of looping on its
// own status writes.
func (r *ReconcileMongoDBDirective) echoObservations(ctx context.Context, directive *operatorv1.MongoDBDirective, localHash string) error {
	if directive.Status.ObservedGeneration == directive.Generation && directive.Status.ObservedSpecHash == localHash {
		return nil
	}
	directive.Status.ObservedGeneration = directive.Generation
	directive.Status.ObservedSpecHash = localHash
	return r.localClient.Status().Update(ctx, directive)
}

// AddMongoDBDirectiveController creates the member controller and adds it to the Manager.
func AddMongoDBDirectiveController(mgr manager.Manager, elector Elector, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, defaultArchitecture architectures.DefaultArchitecture, maxConcurrentReconciles int) error {
	reconciler := newMongoDBDirectiveReconciler(mgr.GetClient(), elector, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, defaultArchitecture, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbDirectiveController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: maxConcurrentReconciles})
	if err != nil {
		return err
	}

	// unfiltered: the echo is equality-guarded, so status self-writes quiesce
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &operatorv1.MongoDBDirective{}, &handler.EnqueueRequestForObject{})); err != nil {
		return err
	}

	// a local CR change means GitOps delivered a new spec copy: re-hash against the directive
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbmultiv1.MongoDBMultiCluster{}, handler.EnqueueRequestsFromMapFunc(enqueueSameNameRequest))); err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbDirectiveController)
	return nil
}
