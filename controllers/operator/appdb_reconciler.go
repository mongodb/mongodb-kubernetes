package operator

import (
	"context"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
)

// AppDBReconciler is implemented by both the internal AppDB reconciler
// (*ReconcileAppDbReplicaSet, backed by opsManager.Spec.AppDB) and the
// ReconcileExternalAppDBReplicaSet (backed by opsManager.Spec.ExternalApplicationDatabaseRef).
// MongoDBOpsManager.Reconcile selects exactly one implementation per reconcile and
// drives it through this interface, so callers never need to branch on which AppDB
// mode is active.
type AppDBReconciler interface {
	// ReconcileAppDB brings the AppDB (internal StatefulSet, or external CR reference)
	// to the desired state. Returns the same (reconcile.Result, error) contract as the
	// rest of the controller's workflow.Status.ReconcileResult() calls.
	ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error)

	// BuildAppDBConnectionURL returns the MongoDB connection string OpsManager/BackupDaemon
	// should use to reach the AppDB. It is a pure computation — callers are responsible
	// for writing the result into each member cluster's connection-string secret.
	BuildAppDBConnectionURL(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error)
}
