package operator

import (
	"context"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileMongoDbMultiReplicaSet reconciles a MongoDB ReplicaSet across multiple Kubernetes clusters
type ReconcileMongoDbMultiReplicaSet struct {
	*ReconcileCommonController
	watch.ResourceWatcher
	omConnectionFactory om.ConnectionFactory
	// memberClusterClients []client.Client
}

var _ reconcile.Reconciler = &ReconcileMongoDbMultiReplicaSet{}

func newMultiClusterReplicaSetReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, memberClusterClients []cluster.Cluster) *ReconcileMongoDbMultiReplicaSet {
	return &ReconcileMongoDbMultiReplicaSet{
		ReconcileCommonController: newReconcileCommonController(mgr),
		ResourceWatcher:           watch.NewResourceWatcher(),
		omConnectionFactory:       omFunc,
		// TODO pass the cluster clients not the cluster object itself
		// memberClusterClients: memberClusterClients,
	}
}

// Reconcile reads that state of the cluster for a MongoDbMultiReplicaSet object and makes changes based on the state read
// and what is in the MongoDbMultiReplicaSet.Spec
func (r *ReconcileMongoDbMultiReplicaSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MultiReplicaSet", request.NamespacedName)
	log.Info("-> MultiReplicaSet.Reconcile")
	return reconcile.Result{}, nil
}

// AddMultiReplicaSetController creates a new MongoDbMultiReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddMultiReplicaSetController(mgr manager.Manager, memberClusters []cluster.Cluster) error {
	reconciler := newMultiClusterReplicaSetReconciler(mgr, om.NewOpsManagerConnection, memberClusters)

	// TODO: add events handler for MongoDBMulti CR
	eventHandler := MongoDBMultiResourceEventHandler{}

	c, err := ctrl.NewControllerManagedBy(mgr).For(&mdbmultiv1.MongoDBMulti{}).
		Watches(&source.Kind{Type: &mdbmultiv1.MongoDBMulti{}}, eventHandler).
		WithEventFilter(predicate.Funcs{}).Build(reconciler)
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, nil)

	// c, err := controller.New(util.MongoDbMultiReplicaSetController, mgr, controller.Options{Reconciler: reconciler})
	// if err != nil {
	// 	return err
	// }

	// Watch for changes to primary resource MongoDbReplicaSet
	// err = c.Watch(&source.Kind{Type: &mdbmultiv1.MongoDBMulti{}}, eventHandler, predicate.Funcs{})
	// if err != nil {
	// 	return err
	// }

	// TODO: add watch predicates for other objects like sts/secrets/configmaps while we implement the reconcile
	// logic for those objects
	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)
	return err
}
