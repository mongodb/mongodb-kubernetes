package operator

import (
	"context"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
)

// AppDBReconciler abstracts the AppDB reconciliation strategy. The internal
// AppDB reconciler (ReconcileAppDbReplicaSet) is the sole implementation today;
// future implementations can use alternative AppDB backends without changing the
// Ops Manager controller.
type AppDBReconciler interface {
	// ReconcileAppDB brings the AppDB to the desired state.
	ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error)

	// BuildAppDBConnectionURL returns the MongoDB connection string OpsManager/BackupDaemon
	// should use to reach the AppDB.
	BuildAppDBConnectionURL(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error)
}
