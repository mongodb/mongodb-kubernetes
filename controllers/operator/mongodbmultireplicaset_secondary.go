package operator

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connection"
	mconstruct "github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/multicluster"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// reconcileSecondary runs on every non-leader cluster's operator. It creates
// only the local cluster's Services, hostname-override ConfigMap, and
// StatefulSet — it does NOT open an OM connection, does NOT update the CR
// status, and does NOT touch saveLastAchievedSpec / failover annotations.
// The leader is the sole writer for those things.
func (r *ReconcileMongoDbMultiReplicaSet) reconcileSecondary(ctx context.Context, request reconcile.Request, log *zap.SugaredLogger) (reconcile.Result, error) {
	log = log.With("mode", "secondary")
	mrs := mdbmultiv1.MongoDBMultiCluster{}
	if err := r.client.Get(ctx, request.NamespacedName, &mrs); err != nil {
		if apiErrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	localSpec, found := findLocalClusterSpec(mrs.Spec.ClusterSpecList, r.localClusterName)
	if !found {
		log.Debugf("local cluster %q not in spec — nothing to reconcile", r.localClusterName)
		return reconcile.Result{}, nil
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.client, r.SecretClient, &mrs, log)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("secondary: read project/credentials: %w", err)
	}
	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("secondary: prepare OM connection: %w", err)
	}

	if err := r.reconcileServices(ctx, log, &mrs); err != nil {
		return reconcile.Result{}, fmt.Errorf("secondary: reconcileServices: %w", err)
	}
	if err := r.reconcileHostnameOverrideConfigMap(ctx, log, mrs); err != nil {
		return reconcile.Result{}, fmt.Errorf("secondary: reconcileHostnameOverrideConfigMap: %w", err)
	}
	if err := r.reconcileLocalStatefulSet(ctx, &mrs, localSpec, conn, projectConfig, log); err != nil {
		return reconcile.Result{}, fmt.Errorf("secondary: reconcileLocalStatefulSet: %w", err)
	}
	log.Infof("secondary reconcile complete for cluster %q", r.localClusterName)
	return reconcile.Result{}, nil
}

// findLocalClusterSpec returns the cluster spec entry for clusterName, or false
// if the local cluster is not represented in the CR.
func findLocalClusterSpec(list mdb.ClusterSpecList, clusterName string) (mdb.ClusterSpecItem, bool) {
	for _, item := range list {
		if item.ClusterName == clusterName {
			return item, true
		}
	}
	return mdb.ClusterSpecItem{}, false
}

// reconcileLocalStatefulSet builds and applies the local cluster's StatefulSet
// using the same MultiClusterStatefulSet builder the leader uses, with
// OM-derived fields left empty (cert hashes, auth mode). The leader's reconcile
// loop fills in those fields on its OM-side automation config; the StatefulSet
// here is the K8s skeleton that hosts the agent pods.
func (r *ReconcileMongoDbMultiReplicaSet) reconcileLocalStatefulSet(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, item mdb.ClusterSpecItem, conn om.Connection, projectConfig mdb.ProjectConfig, log *zap.SugaredLogger) error {
	clusterNum := mrs.ClusterNum(item.ClusterName)
	replicas, err := getMembersForClusterSpecItemThisReconciliation(mrs, item)
	if err != nil {
		return err
	}

	stsOverride := appsv1.StatefulSetSpec{}
	if item.StatefulSetConfiguration != nil {
		stsOverride = item.StatefulSetConfiguration.SpecWrapper.Spec
	}

	opts := mconstruct.MultiClusterReplicaSetOptions(
		mconstruct.WithClusterNum(clusterNum),
		Replicas(replicas),
		mconstruct.WithStsOverride(&stsOverride),
		mconstruct.WithAnnotations(mrs.Name),
		mconstruct.WithServiceName(mrs.MultiHeadlessServiceName(clusterNum)),
		PodEnvVars(newPodVars(conn, projectConfig, mrs.Spec.LogLevel)),
		WithLabels(mrs.GetOwnerLabels()),
		WithAdditionalMongodConfig(mrs.Spec.GetAdditionalMongodConfig()),
		WithInitDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.InitDatabaseImageUrlEnv, r.initDatabaseNonStaticImageVersion)),
		WithDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.NonStaticDatabaseEnterpriseImage, r.databaseNonStaticImageVersion)),
		WithMongodbImage(images.GetOfficialImage(r.imageUrls, mrs.Spec.Version, mrs.GetAnnotations())),
		WithAgentDebug(r.agentDebug),
		WithAgentDebugImage(r.agentDebugImage),
	)

	sts := mconstruct.MultiClusterStatefulSet(*mrs, opts)
	if _, err := statefulset.CreateOrUpdateStatefulset(ctx, r.client, mrs.Namespace, log, &sts); err != nil {
		return fmt.Errorf("create/update StatefulSet for cluster %q: %w", item.ClusterName, err)
	}
	return nil
}
