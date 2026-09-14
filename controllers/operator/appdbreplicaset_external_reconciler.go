package operator

import (
	"context"
	"slices"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ReconcileExternalAppDBReplicaSet implements AppDBReconciler for OpsManager resources using
// spec.externalApplicationDatabaseRef. It never reads opsManager.Spec.AppDB — all AppDB
// state comes from the referenced MongoDB CR instead.
type ReconcileExternalAppDBReplicaSet struct {
	*ReconcileCommonController
	memberClustersMap map[string]client.Client
	log               *zap.SugaredLogger
}

func (r *OpsManagerReconciler) createNewExternalAppDBReconciler(log *zap.SugaredLogger) *ReconcileExternalAppDBReplicaSet {
	return &ReconcileExternalAppDBReplicaSet{
		ReconcileCommonController: r.ReconcileCommonController,
		memberClustersMap:         r.memberClustersMap,
		log:                       log,
	}
}

type appDBClusterWorkItem struct {
	clusterName         string
	client              client.Client
	stsName             string
	legacySingleCluster bool
}

// ReconcileAppDB validates the externalApplicationDatabaseRef, performs the one-time
// detach-and-adopt migration of any pre-existing internal AppDB (idempotent, no-op once
// complete), and establishes a watch on the referenced CR.
func (e *ReconcileExternalAppDBReplicaSet) ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error) {
	if err := e.validateExternalAppDBReference(ctx, opsManager); err != nil {
		return e.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error validating externalApplicationDatabaseRef: %w", err)), e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
	}

	if err := e.ensureAppDBStatefulSetOwnership(ctx, opsManager); err != nil {
		return e.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error detaching internal AppDB StatefulSet: %w", err)), e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
	}

	return e.updateStatus(ctx, opsManager, workflow.Disabled(), e.log, mdbstatus.NewOMPartOption(mdbstatus.AppDb))
}

// GetAppDBConfig computes the AppDB configuration including connection string and TLS settings from the referenced MongoDB CR.
func (e *ReconcileExternalAppDBReplicaSet) GetAppDBConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, _ *zap.SugaredLogger) (*AppDBConfig, error) {
	ref := opsManager.Spec.ExternalAppDBRef
	refObject, err := e.fetchExternalAppDBRefObject(ctx, ref)
	if err != nil {
		return nil, xerrors.Errorf("failed to fetch externalApplicationDatabaseRef %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	password, err := secret.ReadKey(ctx, e.SecretClient, util.OpsManagerPasswordKey, kube.ObjectKey(opsManager.Namespace, omv1.OpsManagerUserPasswordSecretName(ref.Name)))
	if err != nil {
		return nil, xerrors.Errorf("failed to read shared password secret: %w", err)
	}

	connectionString := refObject.BuildConnectionString(util.OpsManagerMongoDBUserName, password, "", connectionstring.SchemeMongoDB, map[string]string{"authMechanism": "SCRAM-SHA-256"})

	return &AppDBConfig{
		IsTLSEnabled:     refObject.IsTLSEnabled(),
		CAConfigMapName:  refObject.GetCAConfigMapName(),
		ConnectionString: connectionString,
	}, nil
}

// validateExternalAppDBReference validates that opsManager's spec.externalApplicationDatabaseRef
func (e *ReconcileExternalAppDBReplicaSet) validateExternalAppDBReference(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error {
	ref := opsManager.Spec.ExternalAppDBRef
	if ref == nil {
		return xerrors.Errorf("externalApplicationDatabaseRef is nil, must be set to a valid MongoDB reference")
	}

	refObject, err := e.fetchExternalAppDBRefObject(ctx, ref)
	if err != nil {
		return xerrors.Errorf("failed to fetch externalApplicationDatabaseRef %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	role := refObject.GetRole()
	if role != mdbv1.RoleAppDB {
		return xerrors.Errorf("externalApplicationDatabaseRef %s/%s must have spec.role set to %q", ref.Namespace, ref.Name, mdbv1.RoleAppDB)
	}

	if ref.Kind == omv1.ExternalAppDBRefKindMongoDBMultiCluster && opsManager.Spec.AppDB != nil && opsManager.Spec.AppDB.IsMultiCluster() {
		mdbm, ok := refObject.(*externalAppDBRefObject).ConnectionStringBuilder.(*mdbmultiv1.MongoDBMultiCluster)
		if !ok {
			return xerrors.Errorf("externalApplicationDatabaseRef %s/%s must reference a MongoDBMultiCluster", ref.Namespace, ref.Name)
		}

		if err := e.validateExternalAppDBTopology(ctx, opsManager, mdbm); err != nil {
			return err
		}
	}

	return nil
}

func (e *ReconcileExternalAppDBReplicaSet) validateExternalAppDBTopology(ctx context.Context, opsManager *omv1.MongoDBOpsManager, mdbm *mdbmultiv1.MongoDBMultiCluster) error {
	helper, err := NewReadOnlyAppDBReconcilerHelper(ctx, opsManager, e.ReconcileCommonController, e.memberClustersMap, e.log)
	if err != nil {
		return xerrors.Errorf("failed to initialize read-only AppDB helper: %w", err)
	}

	internalClusterNames := map[string]struct{}{}
	for _, clusterSpec := range opsManager.Spec.AppDB.ClusterSpecList {
		internalClusterNames[clusterSpec.ClusterName] = struct{}{}
	}

	externalClusterNames := map[string]struct{}{}
	for _, clusterSpec := range mdbm.Spec.ClusterSpecList {
		externalClusterNames[clusterSpec.ClusterName] = struct{}{}
	}

	for _, clusterSpec := range mdbm.Spec.ClusterSpecList {
		if _, ok := internalClusterNames[clusterSpec.ClusterName]; !ok {
			return xerrors.Errorf("cluster %s is not present in internal AppDB clusterSpecList", clusterSpec.ClusterName)
		}
	}

	for _, clusterSpec := range opsManager.Spec.AppDB.ClusterSpecList {
		if _, ok := externalClusterNames[clusterSpec.ClusterName]; !ok {
			return xerrors.Errorf("cluster %s is not present in external MongoDBMultiCluster clusterSpecList", clusterSpec.ClusterName)
		}

		internalIndex, ok := helper.deploymentState.ClusterMapping[clusterSpec.ClusterName]
		if !ok {
			return xerrors.Errorf("cluster %s has no persisted cluster mapping", clusterSpec.ClusterName)
		}

		externalIndex := mdbm.ClusterNum(clusterSpec.ClusterName)
		if internalIndex != externalIndex {
			return xerrors.Errorf("cluster %s index mismatch: internal %d external %d", clusterSpec.ClusterName, internalIndex, externalIndex)
		}
	}

	return nil
}

func (e *ReconcileExternalAppDBReplicaSet) ensureAppDBStatefulSetOwnership(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error {
	workList, err := e.buildAppDBClusterWorkList(ctx, opsManager)
	if err != nil {
		return err
	}

	var errs error
	for _, workItem := range workList {
		if workItem.client == nil {
			if workItem.legacySingleCluster {
				return xerrors.Errorf("failed to fetch StatefulSet %s: member cluster client is not available", workItem.stsName)
			}

			errs = multierror.Append(errs, xerrors.Errorf("member cluster %s client is not available", workItem.clusterName))
			continue
		}

		sts := appsv1.StatefulSet{}
		stsKey := kube.ObjectKey(opsManager.Namespace, workItem.stsName)
		if err := workItem.client.Get(ctx, stsKey, &sts); err != nil {
			if apiErrors.IsNotFound(err) {
				continue
			}

			if workItem.legacySingleCluster {
				return xerrors.Errorf("failed to fetch StatefulSet %s: %w", stsKey.Name, err)
			}

			errs = multierror.Append(errs, xerrors.Errorf("failed to fetch StatefulSet %s for cluster %s: %w", stsKey.Name, workItem.clusterName, err))
			continue
		}

		if !slices.ContainsFunc(sts.OwnerReferences, func(ref metav1.OwnerReference) bool {
			return ref.UID == opsManager.UID
		}) {
			continue
		}

		if err := e.requestAppDBForwardMigration(ctx, workItem.client, sts); err != nil {
			if workItem.legacySingleCluster {
				return err
			}

			errs = multierror.Append(errs, xerrors.Errorf("failed to detach StatefulSet %s for cluster %s: %w", stsKey.Name, workItem.clusterName, err))
		}
	}

	return errs
}

func (e *ReconcileExternalAppDBReplicaSet) requestAppDBForwardMigration(ctx context.Context, c client.Client, sts appsv1.StatefulSet) error {
	sts.OwnerReferences = nil

	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	sts.Annotations[util.AppDBMigrationReadyAnnotation] = trueString
	delete(sts.Annotations, util.AppDBReverseMigrationReadyAnnotation)

	if err := c.Update(ctx, &sts); err != nil {
		return xerrors.Errorf("failed to strip OwnerReferences and annotate StatefulSet %s: %w", sts.GetName(), err)
	}

	return nil
}

func (e *ReconcileExternalAppDBReplicaSet) buildAppDBClusterWorkList(ctx context.Context, opsManager *omv1.MongoDBOpsManager) ([]appDBClusterWorkItem, error) {
	ref := opsManager.Spec.ExternalAppDBRef
	if ref == nil {
		return nil, xerrors.Errorf("externalApplicationDatabaseRef is nil, must be set to a valid MongoDB reference")
	}

	switch ref.Kind {
	case "MongoDB":
		return []appDBClusterWorkItem{{client: e.client, stsName: ref.Name, legacySingleCluster: true}}, nil
	case omv1.ExternalAppDBRefKindMongoDBMultiCluster:
		mdbm := &mdbmultiv1.MongoDBMultiCluster{}
		objectKey := kube.ObjectKey(ref.Namespace, ref.Name)
		if err := e.client.Get(ctx, objectKey, mdbm); err != nil {
			if apiErrors.IsNotFound(err) {
				return nil, xerrors.Errorf("externalApplicationDatabaseRef points to MongoDBMultiCluster %s which does not exist", objectKey)
			}

			return nil, xerrors.Errorf("failed to fetch referenced MongoDBMultiCluster %s: %w", objectKey, err)
		}

		workList := make([]appDBClusterWorkItem, 0, len(mdbm.Spec.ClusterSpecList))
		for _, clusterSpec := range mdbm.Spec.ClusterSpecList {
			workList = append(workList, appDBClusterWorkItem{
				clusterName: clusterSpec.ClusterName,
				client:      e.memberClustersMap[clusterSpec.ClusterName],
				stsName:     mdbm.StatefulSetNameForCluster(clusterSpec.ClusterName),
			})
		}

		return workList, nil
	}

	return nil, xerrors.Errorf("externalApplicationDatabaseRef.kind %q is not supported", ref.Kind)
}

type externalAppDBRefObject struct {
	connectionstring.ConnectionStringBuilder
	mdbv1.DbCommonSpec
}

// GetCAConfigMapName returns the name of the ConfigMap holding the CA certificate that OpsManager
// should trust when connecting to the external AppDB over TLS ("" if TLS is off).
func (o *externalAppDBRefObject) GetCAConfigMapName() string {
	security := o.GetSecurity()
	if security.TLSConfig != nil {
		return security.TLSConfig.CA
	}
	return ""
}

// IsTLSEnabled reports whether the referenced CR has TLS enabled.
func (o *externalAppDBRefObject) IsTLSEnabled() bool {
	return o.IsSecurityTLSConfigEnabled()
}

type ExternalAppDB interface {
	connectionstring.ConnectionStringBuilder
	GetRole() string
	GetCAConfigMapName() string
	IsTLSEnabled() bool
}

func (e *ReconcileExternalAppDBReplicaSet) fetchExternalAppDBRefObject(ctx context.Context, ref *omv1.ExternalAppDBRef) (ExternalAppDB, error) {
	switch ref.Kind {
	case omv1.ExternalAppDBRefKindMongoDB:
		mongodb := &mdbv1.MongoDB{}
		objectKey := kube.ObjectKey(ref.Namespace, ref.Name)
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
	case omv1.ExternalAppDBRefKindMongoDBMultiCluster:
		mdbm := &mdbmultiv1.MongoDBMultiCluster{}
		objectKey := kube.ObjectKey(ref.Namespace, ref.Name)
		if err := e.client.Get(ctx, objectKey, mdbm); err != nil {
			if apiErrors.IsNotFound(err) {
				return nil, xerrors.Errorf("externalApplicationDatabaseRef points to MongoDBMultiCluster %s which does not exist", objectKey)
			}
			return nil, xerrors.Errorf("failed to fetch referenced MongoDBMultiCluster %s: %w", objectKey, err)
		}
		return &externalAppDBRefObject{
			ConnectionStringBuilder: mdbm,
			DbCommonSpec:            mdbm.Spec.DbCommonSpec,
		}, nil
	}

	return nil, xerrors.Errorf("externalApplicationDatabaseRef.kind %q is not supported", ref.Kind)
}
