package operator

import (
	"context"
	"slices"
	"strings"

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
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
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

type appDBClusterItem struct {
	clusterName string
	client      client.Client
	stsName     string
}

// ReconcileAppDB validates the externalApplicationDatabaseRef, performs the one-time
// detach-and-adopt migration of any pre-existing internal AppDB (idempotent, no-op once
// complete), and establishes a watch on the referenced CR.
func (e *ReconcileExternalAppDBReplicaSet) ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error) {
	appDB, err := e.getExternalAppDBReference(ctx, opsManager)
	if err != nil {
		return e.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error validating externalApplicationDatabaseRef: %w", err)), e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
	}

	if status := e.ensureAppDBStatefulSetOwnership(ctx, opsManager, appDB); !status.IsOK() {
		return e.updateStatus(ctx, opsManager, status, e.log, mdbstatus.NewOMPartOption(mdbstatus.OpsManager))
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
		IsTLSEnabled:     refObject.isTLSEnabled(),
		CAConfigMapName:  refObject.getCAConfigMapName(),
		ConnectionString: connectionString,
	}, nil
}

// getExternalAppDBReference retrieves the opsManager's spec.externalApplicationDatabaseRef resource
func (e *ReconcileExternalAppDBReplicaSet) getExternalAppDBReference(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (externalAppDB, error) {
	ref := opsManager.Spec.ExternalAppDBRef
	if ref == nil {
		return nil, xerrors.Errorf("externalApplicationDatabaseRef is nil, must be set to a valid MongoDB reference")
	}

	refObject, err := e.fetchExternalAppDBRefObject(ctx, ref)
	if err != nil {
		return nil, xerrors.Errorf("failed to fetch externalApplicationDatabaseRef %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	role := refObject.GetRole()
	if role != mdbv1.RoleAppDB {
		return nil, xerrors.Errorf("externalApplicationDatabaseRef %s/%s must have spec.role set to %q", ref.Namespace, ref.Name, mdbv1.RoleAppDB)
	}

	return refObject, nil
}

// ensureAppDBStatefulSetOwnership arbitrates ownership of every member-cluster AppDB StatefulSet
// at the start of reconcile:
//   - absent: nothing to detach - Fresh Start, the referenced CR creates its own StatefulSet
//   - owned by this OM (resource-owner label): strip the OM's owner label and OwnerReference and set
//     util.AppDBMigrationReadyAnnotation, so the referenced MongoDB CR can adopt
//   - not owned by this OM (a MongoDB CR holds the label, or the StatefulSet is already detached): no-op
//
// Ownership is decided by the resource-owner label rather than by an ownerReference: a StatefulSet
// deployed to a member cluster must never carry a cross-cluster ownerReference. A legacy StatefulSet
// that predates the ownership labels is recognised by its ownerReference and backfilled in memory.
func (e *ReconcileExternalAppDBReplicaSet) ensureAppDBStatefulSetOwnership(ctx context.Context, opsManager *omv1.MongoDBOpsManager, appDB externalAppDB) workflow.Status {
	clusterList := appDB.getClusterList()

	existingStatefulSets, err := e.getExistingAppDBStatefulsets(ctx, opsManager, clusterList)
	if err != nil {
		return workflow.Failed(err)
	}

	// If any of the existing StatefulSets is still owned by this Ops Manager, we will need to enter forward migration
	isAnyStatefulsetOwnedByOpsManager := false
	for _, sts := range existingStatefulSets {
		if appDBStatefulSetOwnedByOpsManager(sts, opsManager) {
			isAnyStatefulsetOwnedByOpsManager = true
			break
		}
	}

	// Every expected AppDB StatefulSet must exist. While the referenced CR might be still creating
	// them, wait for the rollout instead of reporting a topology mismatch.
	for _, clusterItem := range clusterList {
		if _, exists := existingStatefulSets[clusterItem.clusterName]; exists {
			continue
		}

		if isAnyStatefulsetOwnedByOpsManager {
			return workflow.Failed(xerrors.Errorf("StatefulSet %s for cluster %s is missing: declared by the external AppDB reference but not part of the internal AppDB", clusterItem.stsName, clusterItem.clusterName))
		}

		return workflow.Pending("waiting for the external AppDB to create StatefulSet %s for cluster %s", clusterItem.stsName, clusterItem.clusterName)
	}

	// A multi-cluster external AppDB must declare every AppDB StatefulSet this Ops Manager still
	// owns: one left in an undeclared cluster would keep its Ops Manager ownership after the
	// handover. A single-cluster reference always carries the internal AppDB's name, so it cannot
	// leave one behind.
	if appDB.isMultiCluster() {
		for clusterName, memberClient := range e.memberClustersMap {
			if memberClient == nil {
				continue
			}

			opsManagerOwnedStatefulSets := appsv1.StatefulSetList{}
			if err := memberClient.List(ctx, &opsManagerOwnedStatefulSets,
				client.InNamespace(opsManager.Namespace),
				client.MatchingLabels{util.MongoDBOpsManagerResourceOwnerLabel: opsManager.GetName()},
			); err != nil {
				return workflow.Failed(xerrors.Errorf("failed to list AppDB StatefulSets in cluster %s: %w", clusterName, err))
			}

			for _, sts := range opsManagerOwnedStatefulSets.Items {
				// If the statefulset is not AppDB statefulset, continue
				if !isAppDBStatefulSetName(sts.Name, opsManager.AppDBName()) {
					continue
				}

				// If the AppDB statefulset exists in the cluster, continue
				if _, ok := existingStatefulSets[clusterName]; ok {
					continue
				}

				return workflow.Failed(xerrors.Errorf("StatefulSet %s in cluster %s is not declared by the external AppDB reference: the external AppDB cluster numbers do not match the internal AppDB", sts.Name, clusterName))
			}
		}
	}

	for _, clusterItem := range clusterList {
		sts := existingStatefulSets[clusterItem.clusterName]
		if appDBStatefulSetOwnedByOpsManager(sts, opsManager) {
			if err := requestAppDBForwardMigration(ctx, clusterItem.client, sts); err != nil {
				return workflow.Failed(xerrors.Errorf("failed to detach StatefulSet %s for cluster %s: %w", sts.Name, clusterItem.clusterName, err))
			}
		}
	}

	return workflow.OK()
}

func (e *ReconcileExternalAppDBReplicaSet) getExistingAppDBStatefulsets(ctx context.Context, opsManager *omv1.MongoDBOpsManager, clusterList []appDBClusterItem) (map[string]*appsv1.StatefulSet, error) {
	existingStatefulSets := make(map[string]*appsv1.StatefulSet)
	for _, clusterItem := range clusterList {
		if clusterItem.client == nil {
			return nil, xerrors.Errorf("member cluster %s client is not available", clusterItem.clusterName)
		}

		sts := appsv1.StatefulSet{}
		stsKey := kube.ObjectKey(opsManager.Namespace, clusterItem.stsName)
		if err := clusterItem.client.Get(ctx, stsKey, &sts); err != nil {
			if apiErrors.IsNotFound(err) {
				continue
			}

			return nil, xerrors.Errorf("failed to fetch StatefulSet %s for cluster %s: %w", stsKey.Name, clusterItem.clusterName, err)
		}

		existingStatefulSets[clusterItem.clusterName] = &sts
	}

	return existingStatefulSets, nil
}

// appDBStatefulSetOwnedByOpsManager reports whether the AppDB StatefulSet belongs to this Ops
// Manager, backfilling the ownership label for legacy StatefulSets that used the ownerReference.
func appDBStatefulSetOwnedByOpsManager(sts *appsv1.StatefulSet, opsManager *omv1.MongoDBOpsManager) bool {
	ownershipLabels := util.GetOwnershipLabels(sts.Labels)
	if slices.ContainsFunc(sts.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.UID == opsManager.UID
	}) && len(ownershipLabels) == 0 {
		ownershipLabels[util.MongoDBOpsManagerResourceOwnerLabel] = opsManager.GetName()
	}

	return ownershipLabels[util.MongoDBOpsManagerResourceOwnerLabel] == opsManager.GetName()
}

// isAppDBStatefulSetName reports whether name is one of this Ops Manager's internal AppDB
// StatefulSet names (<om>-db or <om>-db-<clusterNum>).
func isAppDBStatefulSetName(name, appDBName string) bool {
	return name == appDBName || strings.HasPrefix(name, appDBName+"-")
}

func requestAppDBForwardMigration(ctx context.Context, c client.Client, sts *appsv1.StatefulSet) error {
	sts.OwnerReferences = nil
	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	sts.Annotations[util.AppDBMigrationReadyAnnotation] = trueString
	delete(sts.Annotations, util.AppDBReverseMigrationReadyAnnotation)
	delete(sts.Labels, util.MongoDBOpsManagerResourceOwnerLabel)

	if err := c.Update(ctx, sts); err != nil {
		return xerrors.Errorf("failed to strip ownership and annotate StatefulSet %s: %w", sts.GetName(), err)
	}

	return nil
}

type externalAppDBRefObject struct {
	connectionstring.ConnectionStringBuilder
	mdbv1.DbCommonSpec
	clusterList    []appDBClusterItem
	isMulticluster bool
}

// getCAConfigMapName returns the name of the ConfigMap holding the CA certificate that OpsManager
// should trust when connecting to the external AppDB over TLS ("" if TLS is off).
func (o *externalAppDBRefObject) getCAConfigMapName() string {
	security := o.GetSecurity()
	if security.TLSConfig != nil {
		return security.TLSConfig.CA
	}
	return ""
}

// isTLSEnabled reports whether the referenced CR has TLS enabled.
func (o *externalAppDBRefObject) isTLSEnabled() bool {
	return o.IsSecurityTLSConfigEnabled()
}

func (o *externalAppDBRefObject) getClusterList() []appDBClusterItem {
	return o.clusterList
}

func (o *externalAppDBRefObject) isMultiCluster() bool {
	return o.isMulticluster
}

type externalAppDB interface {
	connectionstring.ConnectionStringBuilder
	GetRole() string
	getCAConfigMapName() string
	isTLSEnabled() bool
	getClusterList() []appDBClusterItem
	isMultiCluster() bool
}

func (e *ReconcileExternalAppDBReplicaSet) fetchExternalAppDBRefObject(ctx context.Context, ref *omv1.ExternalAppDBRef) (externalAppDB, error) {
	objectKey := kube.ObjectKey(ref.Namespace, ref.Name)

	switch ref.Kind {
	case omv1.ExternalAppDBRefKindMongoDB:
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
			clusterList:             []appDBClusterItem{{clusterName: multicluster.LegacyCentralClusterName, client: e.client, stsName: ref.Name}},
			isMulticluster:          false,
		}, nil
	case omv1.ExternalAppDBRefKindMongoDBMultiCluster:
		mdbm := &mdbmultiv1.MongoDBMultiCluster{}
		if err := e.client.Get(ctx, objectKey, mdbm); err != nil {
			if apiErrors.IsNotFound(err) {
				return nil, xerrors.Errorf("externalApplicationDatabaseRef points to MongoDBMultiCluster %s which does not exist", objectKey)
			}
			return nil, xerrors.Errorf("failed to fetch referenced MongoDBMultiCluster %s: %w", objectKey, err)
		}

		clusterList := make([]appDBClusterItem, 0, len(mdbm.Spec.ClusterSpecList))
		for _, clusterSpec := range mdbm.Spec.ClusterSpecList {
			clusterList = append(clusterList, appDBClusterItem{
				clusterName: clusterSpec.ClusterName,
				client:      e.memberClustersMap[clusterSpec.ClusterName],
				stsName:     mdbm.StatefulSetNameForCluster(clusterSpec.ClusterName),
			})
		}

		return &externalAppDBRefObject{
			ConnectionStringBuilder: mdbm,
			DbCommonSpec:            mdbm.Spec.DbCommonSpec,
			clusterList:             clusterList,
			isMulticluster:          true,
		}, nil
	}

	return nil, xerrors.Errorf("externalApplicationDatabaseRef.kind %q is not supported", ref.Kind)
}
