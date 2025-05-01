package operator

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
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
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// MongoDBCustomRoleReconciler reconciles a MongoDBCustomRole object
type MongoDBCustomRoleReconciler struct {
	*ReconcileCommonController
	memberClusterClientsMap map[string]kubernetesClient.Client
}

// MongoDBCustomRole Resource
// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbcustomroles,mongodbcustomroles/status,mongodbcustomroles/finalizers},verbs=*,namespace=placeholder

func (r *MongoDBCustomRoleReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	log := zap.S().With("MongoDBCustomRole", request.NamespacedName)
	log.Info("-> MongoDBCustomRole.Reconcile")

	role, err := r.getRole(ctx, request, log)
	if err != nil {
		log.Warnf("error getting custom role %s", err)
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, nil
	}

	log.Infow("MongoDBCustomRole.Spec", "spec", role.Spec)

	if !role.DeletionTimestamp.IsZero() {
		log.Info("MongoDBCustomRole is being deleted")

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

	log.Infof("Finished reconciliation for MongoDBCustomRole!")
	return r.updateStatus(ctx, role, workflow.OK(), log)
}

func AddMongoDBCustomRoleController(ctx context.Context, mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	reconciler := newMongoDBCustomRoleReconciler(ctx, mgr.GetClient(), multicluster.ClustersMapToClientMap(memberClustersMap))
	c, err := controller.New(util.MongoDbCustomRoleController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}) // nolint:forbidigo
	if err != nil {
		return err
	}

	// Watch for changes to MongoDBCustomRole resources
	// We don't need a Delete handler as there is nothing to do after removing the finalizer
	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &rolev1.MongoDBCustomRole{}, &handler.EnqueueRequestForObject{}, watch.PredicatesForCustomRole()))
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbCustomRoleController)
	return nil
}

func newMongoDBCustomRoleReconciler(ctx context.Context, kubeClient client.Client, memberClustersMap map[string]client.Client) *MongoDBCustomRoleReconciler {
	clientsMap := make(map[string]kubernetesClient.Client)

	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}

	return &MongoDBCustomRoleReconciler{
		ReconcileCommonController: NewReconcileCommonController(ctx, kubeClient),
		memberClusterClientsMap:   clientsMap,
	}
}

func (r *MongoDBCustomRoleReconciler) getRole(ctx context.Context, request reconcile.Request, log *zap.SugaredLogger) (*rolev1.MongoDBCustomRole, error) {
	role := &rolev1.MongoDBCustomRole{}
	if _, err := r.GetResource(ctx, request, role, log); err != nil {
		return nil, err
	}

	return role, nil
}

func (r *MongoDBCustomRoleReconciler) Delete(ctx context.Context, role *rolev1.MongoDBCustomRole, log *zap.SugaredLogger) (reconcile.Result, error) {
	log.Info("Attempting to remove MongoDBCustomRole")

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

	log.Info("MongoDBCustomRole has been removed!")
	return r.updateStatus(ctx, role, workflow.OK(), log)
}

func (r *MongoDBCustomRoleReconciler) ensureFinalizer(ctx context.Context, role *rolev1.MongoDBCustomRole, log *zap.SugaredLogger) error {
	log.Info("Adding finalizer to the MongoDBCustomRole resource")

	if finalizerAdded := controllerutil.AddFinalizer(role, util.RoleFinalizer); finalizerAdded {
		if err := r.client.Update(ctx, role); err != nil {
			return err
		}
	}

	return nil
}

func (r *MongoDBCustomRoleReconciler) ensureNoReferences(ctx context.Context, role *rolev1.MongoDBCustomRole) error {
	mdbList := &mdbv1.MongoDBList{}
	err := r.client.List(ctx, mdbList)
	if err != nil {
		return err
	}
	for _, m := range mdbList.Items {
		for _, roleRef := range m.Spec.Security.RoleRefs {
			if roleRef.Name == role.Name {
				return xerrors.Errorf("Resource %s is still referencing this role", m.Name)
			}
		}
	}

	multiList := &mdbmultiv1.MongoDBMultiClusterList{}
	err = r.client.List(ctx, multiList)
	if err != nil {
		return err
	}
	for _, m := range multiList.Items {
		for _, roleRef := range m.Spec.Security.RoleRefs {
			if roleRef.Name == role.Name {
				return xerrors.Errorf("Resource %s is still referencing this role", m.Name)
			}
		}
	}

	return nil
}

func getAnnotationsForCustomRoleResource(role *rolev1.MongoDBCustomRole) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(role.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)
	return finalAnnotations, nil
}
