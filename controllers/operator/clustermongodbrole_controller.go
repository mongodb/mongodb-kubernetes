package operator

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	ctrl "sigs.k8s.io/controller-runtime"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	rolev1 "github.com/mongodb/mongodb-kubernetes/api/v1/role"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

const (
	ClusterMongoDBRoleIndexForMdb      = "mongodb.spec.security.roleRefs"
	ClusterMongoDBRoleIndexForMdbMulti = "mdbmulticluster.spec.security.roleRefs"
)

// ClusterMongoDBRoleReconciler reconciles a ClusterMongoDBRole object
type ClusterMongoDBRoleReconciler struct {
	*ReconcileCommonController
}

// ClusterMongoDBRole Resource
// +kubebuilder:rbac:groups=mongodb.com,resources={clustermongodbroles,clustermongodbroles/status,clustermongodbroles/finalizers},verbs=*

func (r *ClusterMongoDBRoleReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	log := zap.S().With("ClusterMongoDBRole", request.NamespacedName)
	log.Info("-> ClusterMongoDBRole.Reconcile")

	role, err := r.getRole(ctx, request, log)
	if err != nil {
		log.Warnf("error getting custom role %s", err)
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, nil
	}

	log.Infow("ClusterMongoDBRole.Spec", "spec", role.Spec)

	if !role.DeletionTimestamp.IsZero() {
		log.Info("ClusterMongoDBRole is being deleted")

		if controllerutil.ContainsFinalizer(role, util.RoleFinalizer) {
			return r.Delete(ctx, role, log)
		}
	}

	if err := r.ensureFinalizer(ctx, role, log); err != nil {
		return r.updateStatus(ctx, role, workflow.Failed(xerrors.Errorf("Failed to add finalizer: %w", err)), log)
	}

	annotationsToAdd, err := getAnnotationsForCustomRoleResource(role)
	if err != nil {
		return r.updateStatus(ctx, role, workflow.Failed(err), log)
	}

	if err := annotations.SetAnnotations(ctx, role, annotationsToAdd, r.client); err != nil {
		return r.updateStatus(ctx, role, workflow.Failed(err), log)
	}

	log.Infof("Finished reconciliation for ClusterMongoDBRole!")
	return r.updateStatus(ctx, role, workflow.OK(), log)
}

func AddClusterMongoDBRoleController(ctx context.Context, mgr manager.Manager) error {
	reconciler := newClusterMongoDBRoleReconciler(ctx, mgr.GetClient())
	c, err := controller.New(util.ClusterMongoDbRoleController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}) // nolint:forbidigo
	if err != nil {
		return err
	}

	// Watch for changes to ClusterMongoDBRole resources
	// We don't need a Delete handler as there is nothing to do after removing the finalizer
	// To not get into an infinite reconcile loop, we ignore the delete event since the cleanup was already performed
	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &rolev1.ClusterMongoDBRole{}, &handler.EnqueueRequestForObject{}, watch.PredicatesForClusterRole()))
	if err != nil {
		return err
	}

	if err = mgr.GetFieldIndexer().IndexField(ctx, &mdbv1.MongoDB{}, ClusterMongoDBRoleIndexForMdb, findRolesForMongoDB); err != nil {
		return err
	}

	if err = mgr.GetFieldIndexer().IndexField(ctx, &mdbmultiv1.MongoDBMultiCluster{}, ClusterMongoDBRoleIndexForMdbMulti, findRolesForMongoDBMultiCluster); err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.ClusterMongoDbRoleController)
	return nil
}

func newClusterMongoDBRoleReconciler(ctx context.Context, kubeClient client.Client) *ClusterMongoDBRoleReconciler {
	return &ClusterMongoDBRoleReconciler{
		ReconcileCommonController: NewReconcileCommonController(ctx, kubeClient),
	}
}

func (r *ClusterMongoDBRoleReconciler) getRole(ctx context.Context, request reconcile.Request, log *zap.SugaredLogger) (*rolev1.ClusterMongoDBRole, error) {
	role := &rolev1.ClusterMongoDBRole{}
	if _, err := r.GetResource(ctx, request, role, log); err != nil {
		return nil, err
	}

	return role, nil
}

func (r *ClusterMongoDBRoleReconciler) Delete(ctx context.Context, role *rolev1.ClusterMongoDBRole, log *zap.SugaredLogger) (reconcile.Result, error) {
	log.Info("Attempting to remove ClusterMongoDBRole")

	err := r.ensureNoReferences(ctx, role)
	if err != nil {
		return r.updateStatus(ctx, role, workflow.Failed(xerrors.Errorf("Failed to remove role: %w", err)), log)
	}

	if finalizerRemoved := controllerutil.RemoveFinalizer(role, util.RoleFinalizer); !finalizerRemoved {
		return r.updateStatus(ctx, role, workflow.Failed(xerrors.Errorf("Failed to remove finalizer")), log)
	}

	if err := r.client.Update(ctx, role); err != nil {
		return r.updateStatus(ctx, role, workflow.Failed(xerrors.Errorf("Failed to update the role with the removed finalizer: %w", err)), log)
	}

	log.Info("ClusterMongoDBRole has been removed!")
	return r.updateStatus(ctx, role, workflow.OK(), log)
}

func (r *ClusterMongoDBRoleReconciler) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	log.Info("deleted")
	return nil
}

func (r *ClusterMongoDBRoleReconciler) ensureFinalizer(ctx context.Context, role *rolev1.ClusterMongoDBRole, log *zap.SugaredLogger) error {
	log.Info("Adding finalizer to the ClusterMongoDBRole resource")

	if finalizerAdded := controllerutil.AddFinalizer(role, util.RoleFinalizer); finalizerAdded {
		if err := r.client.Update(ctx, role); err != nil {
			return err
		}
	}

	return nil
}

func (r *ClusterMongoDBRoleReconciler) ensureNoReferences(ctx context.Context, role *rolev1.ClusterMongoDBRole) error {
	mdbList := &mdbv1.MongoDBList{}
	listOpts := &client.ListOptions{FieldSelector: fields.OneTermEqualSelector(ClusterMongoDBRoleIndexForMdb, role.Name)}
	err := r.client.List(ctx, mdbList, listOpts)
	if err != nil {
		return err
	}

	if len(mdbList.Items) > 0 {
		return xerrors.Errorf("Resources are still referencing this role")
	}

	multiList := &mdbmultiv1.MongoDBMultiClusterList{}
	listOpts = &client.ListOptions{FieldSelector: fields.OneTermEqualSelector(ClusterMongoDBRoleIndexForMdbMulti, role.Name)}
	err = r.client.List(ctx, multiList, listOpts)
	if err != nil {
		return err
	}

	if len(mdbList.Items) > 0 {
		// TODO print which resources
		return xerrors.Errorf("Resources are still referencing this role")
	}

	return nil
}

func getAnnotationsForCustomRoleResource(role *rolev1.ClusterMongoDBRole) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(role.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)
	return finalAnnotations, nil
}

func findRolesForMongoDB(rawObj client.Object) []string {
	mdb := rawObj.(*mdbv1.MongoDB)
	roles := make([]string, 0)
	for _, roleRef := range mdb.Spec.Security.RoleRefs {
		if roleRef.Kind == util.ClusterMongoDBRoleKind {
			roles = append(roles, roleRef.Name)
		}
	}
	return roles
}

func findRolesForMongoDBMultiCluster(rawObj client.Object) []string {
	mdb := rawObj.(*mdbmultiv1.MongoDBMultiCluster)
	roles := make([]string, 0)
	for _, roleRef := range mdb.Spec.Security.RoleRefs {
		if roleRef.Kind == util.ClusterMongoDBRoleKind {
			roles = append(roles, roleRef.Name)
		}
	}
	return roles
}
