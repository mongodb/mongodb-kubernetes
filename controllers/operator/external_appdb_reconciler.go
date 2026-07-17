package operator

import (
	"context"

	"github.com/blang/semver"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ReconcileExternalAppDBReplicaSet implements AppDBReconciler for OpsManager resources using
// spec.externalApplicationDatabaseRef. It never reads opsManager.Spec.AppDB — all AppDB
// state comes from the referenced MongoDB/MongoDBMultiCluster CR instead.
//
// It embeds *ReconcileCommonController (client, resourceWatcher, updateStatus) directly since
// that's all its own methods need. opsManagerReconciler is only used by detachInternalAppDB,
// which must construct the internal AppDB reconciler to enumerate the pre-existing internal
// AppDB's member clusters for cleanup — a one-time migration step that legitimately needs
// OpsManager-specific construction dependencies (image URLs, connection factory, etc.), not a
// violation of "ReconcileExternalAppDBReplicaSet never touches Spec.AppDB" (see detachInternalAppDB).
type ReconcileExternalAppDBReplicaSet struct {
	*ReconcileCommonController
	opsManagerReconciler *OpsManagerReconciler
	log                  *zap.SugaredLogger
}

func (r *OpsManagerReconciler) createNewExternalAppDBReconciler(log *zap.SugaredLogger) *ReconcileExternalAppDBReplicaSet {
	return &ReconcileExternalAppDBReplicaSet{
		ReconcileCommonController: r.ReconcileCommonController,
		opsManagerReconciler:      r,
		log:                       log,
	}
}

// ReconcileAppDB validates the externalApplicationDatabaseRef, performs the one-time
// detach-and-adopt migration of any pre-existing internal AppDB (idempotent, no-op once
// complete), and establishes a watch on the referenced CR.
func (e *ReconcileExternalAppDBReplicaSet) ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error) {
	// Ref validation/detach failures are OpsManager-level configuration errors (bad ref,
	// stuck detach), not AppDB health problems, so they're reported under the OpsManager
	// status part rather than AppDb — matching how these same failures were reported before
	// this logic moved into ReconcileExternalAppDBReplicaSet.
	if err := e.validateExternalAppDBReference(ctx, opsManager); err != nil {
		return e.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error validating externalApplicationDatabaseRef: %w", err)), e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
	}

	if err := e.detachInternalAppDB(ctx, opsManager, e.log); err != nil {
		return e.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error detaching internal AppDB: %w", err)), e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
	}

	e.watchExternalAppDBReference(opsManager)

	return e.updateStatus(ctx, opsManager, workflow.OK(), e.log, mdbstatus.NewOMPartOption(mdbstatus.AppDb))
}

// BuildAppDBConnectionURL computes the AppDB connection string from the referenced
// MongoDB/MongoDBMultiCluster CR. It is a pure computation — writing the result into each
// member cluster's connection-string secret is the caller's responsibility (Reconcile).
func (e *ReconcileExternalAppDBReplicaSet) BuildAppDBConnectionURL(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	return e.computeExternalAppDBConnectionString(ctx, opsManager)
}

// validateExternalAppDBReference validates that opsManager's spec.externalApplicationDatabaseRef, if set,
// refers to a resource which follows the naming convention, exists, has spec.role set to AppDB and has a
// MongoDB version >= 4.0.0.
func (e *ReconcileExternalAppDBReplicaSet) validateExternalAppDBReference(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error {
	ref := opsManager.Spec.ExternalApplicationDatabaseRef
	if ref == nil {
		return nil
	}

	expectedName := ExpectedAppDBResourceName(opsManager)
	if ref.Name != expectedName {
		return xerrors.Errorf("externalApplicationDatabaseRef.name %q does not match required naming convention %q", ref.Name, expectedName)
	}

	objectKey := kube.ObjectKey(opsManager.Namespace, ref.Name)

	var role string
	var version string

	switch ref.Kind {
	case "MongoDB":
		mongodb := &mdbv1.MongoDB{}
		if err := e.client.Get(ctx, objectKey, mongodb); err != nil {
			if apiErrors.IsNotFound(err) {
				return xerrors.Errorf("externalApplicationDatabaseRef points to MongoDB %s which does not exist", objectKey)
			}
			return err
		}
		role = mongodb.Spec.Role
		version = mongodb.Spec.GetMongoDBVersion()
	case "MongoDBMultiCluster":
		mongodbMulti := &mdbmulti.MongoDBMultiCluster{}
		if err := e.client.Get(ctx, objectKey, mongodbMulti); err != nil {
			if apiErrors.IsNotFound(err) {
				return xerrors.Errorf("externalApplicationDatabaseRef points to MongoDBMultiCluster %s which does not exist", objectKey)
			}
			return err
		}
		role = mongodbMulti.Spec.Role
		version = mongodbMulti.Spec.GetMongoDBVersion()
	default:
		return xerrors.Errorf("externalApplicationDatabaseRef.kind %q is not supported", ref.Kind)
	}

	if role != mdbv1.RoleAppDB {
		return xerrors.Errorf("externalApplicationDatabaseRef %s must have spec.role set to %q", objectKey, mdbv1.RoleAppDB)
	}

	v, err := semver.Make(version)
	if err != nil {
		return xerrors.Errorf("externalApplicationDatabaseRef %s has an invalid version %q: %w", objectKey, version, err)
	}
	fourZero := semver.MustParse("4.0.0")
	if v.LT(fourZero) {
		return xerrors.Errorf("externalApplicationDatabaseRef %s must have a MongoDB version >= 4.0.0, got %q", objectKey, version)
	}

	return nil
}

// detachInternalAppDB performs the one-time forward-migration detach: validate, strip
// OwnerReferences from the internal AppDB StatefulSet, password secret, and ConfigMaps, and
// annotate the StatefulSet ready for adoption. It only acts on a StatefulSet that still carries
// this OM's own OwnerReference — a StatefulSet without it either belongs to the referenced
// MongoDB CR (Fresh Start) or has already been detached, and touching it would strip the CR's
// ownership and leave a stale migration-ready annotation that lets the OM re-adopt prematurely
// on reverse migration. Idempotent — safe to call every reconcile while
// externalApplicationDatabaseRef is set.
//
// TODO(CLOUDP-TBD): this only fetches/annotates a single StatefulSet named after
// externalApplicationDatabaseRef.Name in the central cluster's client, which assumes the
// referenced resource is a single-cluster MongoDB. A MongoDBMultiCluster reference would have
// one StatefulSet per member cluster (each in its own client), none of which match this lookup
// — so detach silently no-ops for multi-cluster external refs instead of stripping/annotating
// every member cluster's StatefulSet. The "ready" annotation should also only be set once all
// StatefulSets across all member clusters have been stripped, not as each one completes.
// Tracked as a separate follow-up PR — not fixed here.
func (e *ReconcileExternalAppDBReplicaSet) detachInternalAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
		return nil
	}

	if err := e.validateExternalAppDBReference(ctx, opsManager); err != nil {
		return err
	}

	sts := appsv1.StatefulSet{}
	stsKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.ExternalApplicationDatabaseRef.Name)
	if err := e.client.Get(ctx, stsKey, &sts); err != nil {
		if apiErrors.IsNotFound(err) {
			return nil // Fresh Start, nothing to detach
		}
		return xerrors.Errorf("failed to fetch StatefulSet %s: %w", stsKey.Name, err)
	}

	ownedByThisOM := false
	for _, ref := range sts.OwnerReferences {
		if ref.UID == opsManager.UID {
			ownedByThisOM = true
			break
		}
	}
	if !ownedByThisOM {
		// Abort of an in-flight reverse migration: the internal reconciler requested a release
		// (and the CR may already have complied). Hand the StatefulSet to the CR by swapping the
		// annotations - removing the request alone would leave the CR's gate blocked forever.
		if sts.Annotations[util.AppDBReverseMigrationReadyAnnotation] == trueString {
			delete(sts.Annotations, util.AppDBReverseMigrationReadyAnnotation)
			if len(sts.OwnerReferences) == 0 {
				sts.Annotations[util.AppDBMigrationReadyAnnotation] = trueString
			}
			if err := e.client.Update(ctx, &sts); err != nil {
				return xerrors.Errorf("failed to clear reverse-migration request from StatefulSet %s: %w", stsKey.Name, err)
			}
		}
		return nil // Fresh Start (StatefulSet belongs to the referenced CR) or detach already completed
	}

	sts.OwnerReferences = nil
	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	sts.Annotations[util.AppDBMigrationReadyAnnotation] = trueString
	delete(sts.Annotations, util.AppDBReverseMigrationReadyAnnotation)
	if err := e.client.Update(ctx, &sts); err != nil {
		return xerrors.Errorf("failed to strip OwnerReferences and annotate StatefulSet %s: %w", stsKey.Name, err)
	}

	appDbHelper, err := NewReadOnlyAppDBReconcilerHelper(ctx, opsManager, e.opsManagerReconciler.ReconcileCommonController, e.opsManagerReconciler.memberClustersMap, log)
	if err != nil {
		return xerrors.Errorf("failed to initialize AppDB reconciler: %w", err)
	}

	return e.opsManagerReconciler.stripInternalAppDBOwnerReferencesFromSecretsAndConfigMaps(ctx, opsManager, appDbHelper.GetHealthyMemberClusters())
}

// computeExternalAppDBConnectionString fetches the referenced MongoDB/MongoDBMultiCluster CR and
// the shared mongodb-ops-manager password secret, computes the connection string directly via
// BuildConnectionString. No connection-string secret is ever created by the referenced CR itself.
func (e *ReconcileExternalAppDBReplicaSet) computeExternalAppDBConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (string, error) {
	ref := opsManager.Spec.ExternalApplicationDatabaseRef
	if ref == nil {
		return "", nil
	}

	// ref.Name is already required (validated by validateExternalAppDBReference) to equal
	// <om-name>-db, the exact same value AppDBSpec.Name() produces for internal AppDB — no
	// suffix-stripping or OM-name derivation needed here either.
	passwordSecret := corev1.Secret{}
	if err := e.client.Get(ctx, kube.ObjectKey(opsManager.Namespace, omv1.OpsManagerUserPasswordSecretName(ref.Name)), &passwordSecret); err != nil {
		return "", xerrors.Errorf("failed to read shared password secret: %w", err)
	}
	password := string(passwordSecret.Data[util.OpsManagerPasswordKey])

	var connectionString string
	switch ref.Kind {
	case "MongoDB":
		mdb := mdbv1.MongoDB{}
		if err := e.client.Get(ctx, kube.ObjectKey(opsManager.Namespace, ref.Name), &mdb); err != nil {
			return "", xerrors.Errorf("failed to fetch referenced MongoDB %s: %w", ref.Name, err)
		}
		connectionString = mdb.BuildConnectionString(util.OpsManagerMongoDBUserName, password, connectionstring.SchemeMongoDB, nil)
	case "MongoDBMultiCluster":
		mdbm := mdbmulti.MongoDBMultiCluster{}
		if err := e.client.Get(ctx, kube.ObjectKey(opsManager.Namespace, ref.Name), &mdbm); err != nil {
			return "", xerrors.Errorf("failed to fetch referenced MongoDBMultiCluster %s: %w", ref.Name, err)
		}
		connectionString = mdbm.BuildConnectionString(util.OpsManagerMongoDBUserName, password, connectionstring.SchemeMongoDB, nil)
	default:
		return "", xerrors.Errorf("externalApplicationDatabaseRef.kind %q is not supported", ref.Kind)
	}

	return connectionString, nil
}

// watchExternalAppDBReference establishes a dynamic watch on the referenced CR, mirroring
// the existing precedent in watchMongoDBResourcesReferencedByKmip — not a new mechanism, same
// call pointed at a different name.
func (e *ReconcileExternalAppDBReplicaSet) watchExternalAppDBReference(opsManager *omv1.MongoDBOpsManager) {
	ref := opsManager.Spec.ExternalApplicationDatabaseRef
	if ref == nil {
		return
	}
	// Both MongoDB and MongoDBMultiCluster route through the same watch.MongoDB type today.
	e.resourceWatcher.AddWatchedResourceIfNotAdded(ref.Name, opsManager.Namespace, watch.MongoDB, kube.ObjectKeyFromApiObject(opsManager))
}
