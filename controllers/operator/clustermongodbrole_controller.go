package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

	role := &rolev1.ClusterMongoDBRole{}
	reconcileResult, err := r.prepareResourceForReconciliation(ctx, request, role, log)
	if err != nil {
		if errors.IsNotFound(err) {
			return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
		}
		return reconcileResult, err
	}

	log.Infow("ClusterMongoDBRole.Spec", "spec", role.Spec)

	if err := role.ProcessValidationsOnReconcile(nil); err != nil {
		return r.updateStatus(ctx, role, workflow.Invalid("%s", err.Error()), log)
	}

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
	r := newClusterMongoDBRoleReconciler(ctx, mgr.GetClient())

	if err := mgr.GetFieldIndexer().IndexField(ctx, &mdbv1.MongoDB{}, ClusterMongoDBRoleIndexForMdb, findRolesForMongoDB); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(ctx, &mdbmultiv1.MongoDBMultiCluster{}, ClusterMongoDBRoleIndexForMdbMulti, findRolesForMongoDBMultiCluster); err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.ClusterMongoDbRoleController)

	return ctrl.NewControllerManagedBy(mgr).
		Named(util.ClusterMongoDbRoleController).
		For(&rolev1.ClusterMongoDBRole{}, builder.WithPredicates(watch.PredicatesForClusterRole())).
		WithOptions(controller.Options{MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}). // nolint:forbidigo
		Watches(&mdbv1.MongoDB{}, handler.EnqueueRequestsFromMapFunc(getReconcileRequestsForMongoDB)).
		Watches(&mdbmultiv1.MongoDBMultiCluster{}, handler.EnqueueRequestsFromMapFunc(getReconcileRequestsForMongoDBMultiCluster)).
		Complete(r)
}

func newClusterMongoDBRoleReconciler(ctx context.Context, kubeClient client.Client) *ClusterMongoDBRoleReconciler {
	return &ClusterMongoDBRoleReconciler{
		ReconcileCommonController: NewReconcileCommonController(ctx, kubeClient),
	}
}

// Delete handles the deletion of the ClusterMongoDBRole resource.
// It ensures that no MongoDB or MongoDBMultiCluster resources are referencing this role.
// If there are references, it moves the resource in a Pending phase with an appropriate message.
// If there are no references, it removes the finalizer from the role.
func (r *ClusterMongoDBRoleReconciler) Delete(ctx context.Context, role *rolev1.ClusterMongoDBRole, log *zap.SugaredLogger) (reconcile.Result, error) {
	log.Info("Attempting to remove ClusterMongoDBRole")

	resources, err := r.getDependentResources(ctx, role)
	if err != nil {
		return r.updateStatus(ctx, role, workflow.Failed(xerrors.Errorf("Failed to lookup dependent deployments: %s", err)), log)
	} else if len(resources) > 0 {
		return r.updateStatus(
			ctx,
			role,
			workflow.Pending("%s", fmt.Sprintf("Role deletion blocked, it is still referenced by: %s", strings.Join(resources, ","))),
			log)
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

func (r *ClusterMongoDBRoleReconciler) ensureFinalizer(ctx context.Context, role *rolev1.ClusterMongoDBRole, log *zap.SugaredLogger) error {
	log.Info("Adding finalizer to the ClusterMongoDBRole resource")

	if finalizerAdded := controllerutil.AddFinalizer(role, util.RoleFinalizer); finalizerAdded {
		if err := r.client.Update(ctx, role); err != nil {
			return err
		}
	}

	return nil
}

// getDependentResources checks if the ClusterMongoDBRole is referenced by any MongoDB or MongoDBMultiCluster resources.
// If it is referenced, it returns the list of resources by namespaced name
// If it is not referenced, it returns nil.
// This method uses the indexes set up in the AddClusterMongoDBRoleController function to find the resources that reference the role.
func (r *ClusterMongoDBRoleReconciler) getDependentResources(ctx context.Context, role *rolev1.ClusterMongoDBRole) ([]string, error) {
	mdbList := &mdbv1.MongoDBList{}
	err := r.client.List(ctx, mdbList, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(ClusterMongoDBRoleIndexForMdb, role.Name),
	})
	if err != nil {
		return nil, err
	}

	multiList := &mdbmultiv1.MongoDBMultiClusterList{}
	err = r.client.List(ctx, multiList, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(ClusterMongoDBRoleIndexForMdbMulti, role.Name),
	})
	if err != nil {
		return nil, err
	}

	if len(mdbList.Items) > 0 || len(multiList.Items) > 0 {
		resources := make([]string, 0)
		for _, mdb := range mdbList.Items {
			resources = append(resources, fmt.Sprintf("%s/%s", mdb.Namespace, mdb.Name))
		}
		for _, mdbmc := range multiList.Items {
			resources = append(resources, fmt.Sprintf("%s/%s", mdbmc.Namespace, mdbmc.Name))
		}
		return resources, nil
	}

	return nil, nil
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

// findRolesForMongoDB finds the roles referenced from MongoDB resources
// This is used to index the MongoDB resources by the ClusterMongoDBRole they reference.
func findRolesForMongoDB(rawObj client.Object) []string {
	mdb, ok := rawObj.(*mdbv1.MongoDB)
	roles := make([]string, 0)

	if !ok {
		return roles
	}

	if mdb.Spec.Security == nil {
		return roles
	}

	for _, roleRef := range mdb.Spec.Security.RoleRefs {
		if roleRef.Kind == util.ClusterMongoDBRoleKind {
			roles = append(roles, roleRef.Name)
		}
	}
	return roles
}

// findRolesForMongoDBMultiCluster finds the roles referenced from MongoDBMultiCluster resources
// This is used to index the MongoDBMultiCluster resources by the ClusterMongoDBRole they reference.
func findRolesForMongoDBMultiCluster(rawObj client.Object) []string {
	mdb, ok := rawObj.(*mdbmultiv1.MongoDBMultiCluster)
	roles := make([]string, 0)

	if !ok {
		return roles
	}

	if mdb.Spec.Security == nil {
		return roles
	}

	for _, roleRef := range mdb.Spec.Security.RoleRefs {
		if roleRef.Kind == util.ClusterMongoDBRoleKind {
			roles = append(roles, roleRef.Name)
		}
	}
	return roles
}

// getReconcileRequestsForMongoDB returns reconcile requests for ClusterMongoDBRole resources based on the roles referenced in MongoDB resources.
func getReconcileRequestsForMongoDB(ctx context.Context, rawObj client.Object) []reconcile.Request {
	roles := findRolesForMongoDB(rawObj)
	requests := []reconcile.Request{}

	for _, role := range roles {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: role}})
	}
	return requests
}

// getReconcileRequestsForMongoDBMultiCluster returns reconcile requests for ClusterMongoDBRole resources based on the roles referenced in MongoDBMultiCluster resources.
func getReconcileRequestsForMongoDBMultiCluster(ctx context.Context, rawObj client.Object) []reconcile.Request {
	roles := findRolesForMongoDBMultiCluster(rawObj)
	requests := []reconcile.Request{}

	for _, role := range roles {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: role}})
	}
	return requests
}
