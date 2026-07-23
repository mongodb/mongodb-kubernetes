package operator

import (
	"context"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
)

// AppDBReconciler is implemented by both the ReconcileAppDbReplicaSet (internal AppDB reconciler
// backed by opsManager.Spec.AppDB) and the ReconcileExternalAppDBReplicaSet (backed by
// opsManager.Spec.ExternalApplicationDatabaseRef).
type AppDBReconciler interface {
	// ReconcileAppDB brings the AppDB (internal StatefulSet, or external CR reference)
	// to the desired state. Returns the same (reconcile.Result, error) contract as the
	// rest of the controller's workflow.Status.ReconcileResult() calls.
	ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error)

	// BuildAppDBConnectionURL returns the MongoDB connection string OpsManager/BackupDaemon
	// should use to reach the AppDB.
	BuildAppDBConnectionURL(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error)
}
