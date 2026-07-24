package operator

import (
	"context"
	"slices"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ReconcileExternalAppDBReplicaSet implements AppDBReconciler for OpsManager resources using
// spec.externalApplicationDatabaseRef. It never reads opsManager.Spec.AppDB — all AppDB
// state comes from the referenced MongoDB/MongoDBMultiCluster CR instead.
type ReconcileExternalAppDBReplicaSet struct {
	*ReconcileCommonController
	log *zap.SugaredLogger
}

func (r *OpsManagerReconciler) createNewExternalAppDBReconciler(log *zap.SugaredLogger) *ReconcileExternalAppDBReplicaSet {
	return &ReconcileExternalAppDBReplicaSet{
		ReconcileCommonController: r.ReconcileCommonController,
		log:                       log,
	}
}

// ReconcileAppDB validates the externalApplicationDatabaseRef, performs the one-time
// detach-and-adopt migration of any pre-existing internal AppDB (idempotent, no-op once
// complete), and establishes a watch on the referenced CR.
func (e *ReconcileExternalAppDBReplicaSet) ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error) {
	if err := e.validateExternalAppDBReference(ctx, opsManager); err != nil {
		return e.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error validating externalApplicationDatabaseRef: %w", err)), e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
	}

	if err := e.ensureAppDBStatefulSetOwnership(ctx, opsManager); err != nil {
		return e.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error detaching internal AppDB: %w", err)), e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
	}

	return e.updateStatus(ctx, opsManager, workflow.OK(), e.log, mdbstatus.NewOMPartOption(mdbstatus.AppDb))
}

// BuildAppDBConnectionURL computes the AppDB connection string from the referenced MongoDB/MongoDBMultiCluster CR.
func (e *ReconcileExternalAppDBReplicaSet) BuildAppDBConnectionURL(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	return e.computeExternalAppDBConnectionString(ctx, opsManager)
}

// validateExternalAppDBReference validates that opsManager's spec.externalApplicationDatabaseRef
func (e *ReconcileExternalAppDBReplicaSet) validateExternalAppDBReference(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error {
	ref := opsManager.Spec.ExternalApplicationDatabaseRef
	if ref == nil {
		return xerrors.Errorf("externalApplicationDatabaseRef is nil, must be set to a valid MongoDB or MongoDBMultiCluster reference")
	}

	expectedName := opsManager.AppDBName()
	if ref.Name != expectedName {
		return xerrors.Errorf("externalApplicationDatabaseRef.name %q does not match required naming convention %q", ref.Name, expectedName)
	}

	objectKey := kube.ObjectKey(opsManager.Namespace, ref.Name)

	refObject, err := e.fetchExternalAppDBRefObject(ctx, ref, objectKey)
	if err != nil {
		return xerrors.Errorf("failed to fetch externalApplicationDatabaseRef %s: %w", objectKey, err)
	}

	role := refObject.GetRole()
	if role != mdbv1.RoleAppDB {
		return xerrors.Errorf("externalApplicationDatabaseRef %s must have spec.role set to %q", objectKey, mdbv1.RoleAppDB)
	}

	//TODO maybe other validations e.g. SCRAM, TLS?
	return nil
}

// ensureAppDBStatefulSetOwnership arbitrates ownership of the AppDB StatefulSet at the start of reconcile:
//   - absent: nothing to detach - Fresh Start, the referenced CR creates its own StatefulSet
//   - owned by this OM: strip OM's OwnerReference and set util.AppDBMigrationReadyAnnotation
//     so the referenced MongoDB CR can adopt
//   - foreign-owned (a MongoDB CR) or already detached: no-op - the CR owns the StatefulSet
func (e *ReconcileExternalAppDBReplicaSet) ensureAppDBStatefulSetOwnership(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error {
	sts := appsv1.StatefulSet{}
	stsKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.ExternalApplicationDatabaseRef.Name)
	if err := e.client.Get(ctx, stsKey, &sts); err != nil {
		if apiErrors.IsNotFound(err) {
			return nil // Fresh Start, nothing to detach
		}
		return xerrors.Errorf("failed to fetch StatefulSet %s: %w", stsKey.Name, err)
	}

	ownedByThisOM := slices.ContainsFunc(sts.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.UID == opsManager.UID
	})

	// If not owned by this Ops Manager, no-op
	if !ownedByThisOM {
		return nil
	}

	// Request forward migration if owned by this Ops Manager
	return e.requestAppDBForwardMigration(ctx, sts)
}

func (e *ReconcileExternalAppDBReplicaSet) requestAppDBForwardMigration(ctx context.Context, sts appsv1.StatefulSet) error {
	sts.OwnerReferences = nil

	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	sts.Annotations[util.AppDBMigrationReadyAnnotation] = trueString
	delete(sts.Annotations, util.AppDBReverseMigrationReadyAnnotation)

	if err := e.client.Update(ctx, &sts); err != nil {
		return xerrors.Errorf("failed to strip OwnerReferences and annotate StatefulSet %s: %w", sts.GetName(), err)
	}

	return nil
}

// computeExternalAppDBConnectionString fetches the referenced MongoDB/MongoDBMultiCluster CR and
// the shared mongodb-ops-manager password secret, computes the connection string directly via
// BuildConnectionString.
func (e *ReconcileExternalAppDBReplicaSet) computeExternalAppDBConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (string, error) {
	ref := opsManager.Spec.ExternalApplicationDatabaseRef

	passwordSecret := corev1.Secret{}
	if err := e.client.Get(ctx, kube.ObjectKey(opsManager.Namespace, omv1.OpsManagerUserPasswordSecretName(ref.Name)), &passwordSecret); err != nil {
		return "", xerrors.Errorf("failed to read shared password secret: %w", err)
	}
	password := string(passwordSecret.Data[util.OpsManagerPasswordKey])

	objectKey := kube.ObjectKey(opsManager.Namespace, ref.Name)
	refObject, err := e.fetchExternalAppDBRefObject(ctx, ref, objectKey)
	if err != nil {
		return "", xerrors.Errorf("failed to fetch externalApplicationDatabaseRef %s: %w", objectKey, err)
	}

	return refObject.BuildConnectionString(util.OpsManagerMongoDBUserName, password, connectionstring.SchemeMongoDB, nil), nil
}

type externalAppDBRefObject struct {
	connectionstring.ConnectionStringBuilder
	mdbv1.DbCommonSpec
}

type ExternalAppDB interface {
	connectionstring.ConnectionStringBuilder
	GetRole() string
}

func (e *ReconcileExternalAppDBReplicaSet) fetchExternalAppDBRefObject(ctx context.Context, ref *omv1.ExternalApplicationDatabaseRef, objectKey client.ObjectKey) (ExternalAppDB, error) {
	switch ref.Kind {
	case "MongoDB":
		mongodb := &mdbv1.MongoDB{}
		if err := e.client.Get(ctx, objectKey, mongodb); err != nil {
			if apiErrors.IsNotFound(err) {
				return nil, xerrors.Errorf("externalApplicationDatabaseRef points to MongoDB %s which does not exist", objectKey)
			}
			return nil, xerrors.Errorf("failed to fetch referenced MongoDB %s: %w", objectKey, err)
		}
		return &externalAppDBRefObject{
			ConnectionStringBuilder: mongodb,
			DbCommonSpec:            mongodb.Spec.DbCommonSpec,
		}, nil
	case "MongoDBMultiCluster":
		mongodbMulti := &mdbmulti.MongoDBMultiCluster{}
		if err := e.client.Get(ctx, objectKey, mongodbMulti); err != nil {
			if apiErrors.IsNotFound(err) {
				return nil, xerrors.Errorf("externalApplicationDatabaseRef points to MongoDBMultiCluster %s which does not exist", objectKey)
			}
			return nil, xerrors.Errorf("failed to fetch referenced MongoDBMultiCluster %s: %w", objectKey, err)
		}
		return &externalAppDBRefObject{
			ConnectionStringBuilder: mongodbMulti,
			DbCommonSpec:            mongodbMulti.Spec.DbCommonSpec,
		}, nil
	}

	return nil, xerrors.Errorf("externalApplicationDatabaseRef.kind %q is not supported", ref.Kind)
}
