package operator

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connection"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// AddStandbyClusterController creates a new MongoDBStandbyCluster Controller and adds it to the Manager.
func AddStandbyClusterController(ctx context.Context, mgr manager.Manager) error {
	reconciler := &ReconcileMongoDBStandbyCluster{
		ReconcileCommonController: NewReconcileCommonController(ctx, mgr.GetClient()),
		omConnectionFactory:       om.NewOpsManagerConnection,
	}
	c, err := controller.New("mongodbstandbycluster-controller", mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbv1.MongoDBStandbyCluster{}, &handler.EnqueueRequestForObject{})); err != nil {
		return err
	}

	zap.S().Info("Registered controller mongodbstandbycluster-controller")
	return nil
}

// ReconcileMongoDBStandbyCluster reconciles a MongoDBStandbyCluster object.
type ReconcileMongoDBStandbyCluster struct {
	*ReconcileCommonController
	omConnectionFactory om.ConnectionFactory
}

var _ reconcile.Reconciler = &ReconcileMongoDBStandbyCluster{}

func (r *ReconcileMongoDBStandbyCluster) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := zap.S().With("MongoDBStandbyCluster", request.NamespacedName)

	standby := &mdbv1.MongoDBStandbyCluster{}
	if result, err := r.prepareResourceForReconciliation(ctx, request, standby, log); err != nil {
		if k8serrors.IsNotFound(err) {
			return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
		}
		return result, err
	}

	log.Info("-> MongoDBStandbyCluster.Reconcile")

	// Fetch referenced MongoDB ReplicaSet.
	rs := &mdbv1.MongoDB{}
	if err := r.client.Get(ctx, kube.ObjectKey(request.Namespace, standby.Spec.MongoDBResourceRef.Name), rs); err != nil {
		return r.updateStatus(ctx, standby, workflow.Failed(xerrors.Errorf("failed to get referenced MongoDB %s: %w", standby.Spec.MongoDBResourceRef.Name, err)), log)
	}

	if rs.Spec.ResourceType != mdbv1.ReplicaSet {
		return r.updateStatus(ctx, standby, workflow.Invalid("referenced MongoDB resource must be of type ReplicaSet"), log)
	}

	if !architectures.IsRunningStaticArchitecture(rs.Annotations) {
		return r.updateStatus(ctx, standby, workflow.Invalid("referenced MongoDB resource must use static architecture for injector sidecar support"), log)
	}

	// Read S3 credentials.
	credSecret, err := r.client.GetSecret(ctx, kube.ObjectKey(request.Namespace, standby.Spec.Monarch.CredentialsSecretRef.Name))
	if err != nil {
		return r.updateStatus(ctx, standby, workflow.Failed(xerrors.Errorf("failed to read S3 credentials secret: %w", err)), log)
	}
	awsAccessKeyID := string(credSecret.Data["awsAccessKeyId"])
	awsSecretAccessKey := string(credSecret.Data["awsSecretAccessKey"])

	// Connect to OpsManager.
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.client, r.SecretClient, standby, log)
	if err != nil {
		return r.updateStatus(ctx, standby, workflow.Failed(xerrors.Errorf("failed to read project config: %w", err)), log)
	}

	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, request.Namespace, log)
	if err != nil {
		return r.updateStatus(ctx, standby, workflow.Failed(xerrors.Errorf("failed to prepare OpsManager connection: %w", err)), log)
	}

	// Compute RS member hostnames (pod DNS names).
	hostnames, _ := dns.GetDNSNames(rs.Name, rs.Name, rs.Namespace, rs.Spec.GetClusterDomain(), rs.Spec.Members, rs.Spec.GetExternalDomain())
	replSetHosts := strings.Join(hostnames, ",")

	// Update automation config with monarch sections.
	if err := conn.ReadUpdateDeployment(func(d om.Deployment) error {
		return applyMonarchConfig(d, standby, awsAccessKeyID, awsSecretAccessKey, rs.Name)
	}, log); err != nil {
		return r.updateStatus(ctx, standby, workflow.Failed(xerrors.Errorf("failed to update automation config: %w", err)), log)
	}

	// Before patching the StatefulSet, verify the agents have picked up the automation config.
	// There are two acceptable states to proceed:
	//   1. Goal state reached — all agents applied the config (votes/priority changes).
	//   2. All non-ready agents are blocked at WaitForInjectorReady — they are waiting for the
	//      injector sidecar health check, which only passes once we add it. Patching the STS is
	//      exactly what unblocks them; waiting for goal state here would be a deadlock.
	// Any other not-ready reason means the agents haven't applied the config yet — requeue.
	processNames := make([]string, len(hostnames))
	for i, h := range hostnames {
		processNames[i] = fmt.Sprintf("%s:%d", h, util.MongoDbDefaultPort)
	}
	if ready, msg := om.AllAgentsInGoalState(conn, processNames, log); !ready {
		if !om.AgentsBlockedOnInjector(conn, processNames, log) {
			return r.updateStatus(ctx, standby, workflow.Pending("waiting for agents to apply automation config before adding injector sidecar: %s", msg), log)
		}
		log.Infow("All agents blocked at WaitForInjectorReady — proceeding to add injector sidecar")
	}

	// Patch the MongoDB RS's StatefulSet to add the injector sidecar if not already present.
	sts, err := r.client.GetStatefulSet(ctx, kube.ObjectKey(request.Namespace, rs.Name))
	if err != nil {
		return r.updateStatus(ctx, standby, workflow.Failed(xerrors.Errorf("failed to get StatefulSet %s: %w", rs.Name, err)), log)
	}

	if !hasInjectorSidecar(sts.Spec.Template.Spec.Containers) {
		injectorCfg := &construct.InjectorSidecarConfig{
			Image:             standby.Spec.InjectorImage,
			ShardID:           standby.Spec.Monarch.ReplicaSetID,
			ReplSetName:       rs.Name,
			ReplSetHosts:      replSetHosts,
			ClusterPrefix:     standby.Spec.Monarch.ClusterPrefix,
			S3Bucket:          standby.Spec.Monarch.S3BucketName,
			S3Endpoint:        standby.Spec.Monarch.S3BucketEndpoint,
			S3PathStyleAccess: standby.Spec.Monarch.S3PathStyleAccess,
			AWSRegion:         standby.Spec.Monarch.AWSRegion,
			CredentialsSecret: standby.Spec.Monarch.CredentialsSecretRef.Name,
		}
		podtemplatespec.Apply(podtemplatespec.WithContainerByIndex(3, construct.InjectorSidecarModifications(injectorCfg)...))(&sts.Spec.Template)
		if _, err := r.client.UpdateStatefulSet(ctx, sts); err != nil {
			return r.updateStatus(ctx, standby, workflow.Failed(xerrors.Errorf("failed to patch StatefulSet with injector sidecar: %w", err)), log)
		}
	}

	return r.updateStatus(ctx, standby, workflow.OK(), log)
}

// applyMonarchConfig updates the Deployment with the maintainedMonarchComponents section and
// modifies RS members to set votes/priority=0, adding the injector as the sole voting member.
func applyMonarchConfig(d om.Deployment, standby *mdbv1.MongoDBStandbyCluster, awsAccessKeyID, awsSecretAccessKey, rsName string) error {
	mc := om.MaintainedMonarchComponents{
		// ReplicaSetID identifies the standby RS being managed (rsName), not the active shard.
		ReplicaSetID:       rsName,
		ClusterPrefix:      standby.Spec.Monarch.ClusterPrefix,
		AWSBucketName:      standby.Spec.Monarch.S3BucketName,
		AWSRegion:          standby.Spec.Monarch.AWSRegion,
		AWSAccessKeyID:     awsAccessKeyID,
		AWSSecretAccessKey: awsSecretAccessKey,
		S3BucketEndpoint:   standby.Spec.Monarch.S3BucketEndpoint,
		S3PathStyleAccess:  standby.Spec.Monarch.S3PathStyleAccess,
		InjectorConfig: om.InjectorConfig{
			Version: standby.Spec.Monarch.InjectorVersion,
			Shards: []om.InjectorShard{
				{
					// ShardID is the active RS identifier (--shardId for the Monarch shipper).
					ShardID:     standby.Spec.Monarch.ReplicaSetID,
					ReplSetName: rsName,
					Instances: []om.InjectorInstance{
						{
							ID:                 0,
							Hostname:           "localhost",
							Disabled:           false,
							Port:               9995,
							ExternallyManaged:  true,
							HealthAPIEndpoint:  "localhost:8080",
							MonarchAPIEndpoint: "localhost:1122",
						},
					},
				},
			},
		},
	}
	d.SetMaintainedMonarchComponents([]om.MaintainedMonarchComponents{mc})

	rs := d.GetReplicaSetByName(rsName)
	if rs == nil {
		return xerrors.Errorf("replica set %q not found in automation config deployment", rsName)
	}

	// Set all mongod members to votes=0, priority=0.
	members := rs.Members()
	for i := range members {
		members[i]["votes"] = 0
		members[i]["priority"] = float32(0)
	}
	// Append the injector as the sole voting member.
	injectorMember := om.ReplicaSetMember{
		"_id":      len(members),
		"host":     "localhost:9995",
		"votes":    1,
		"priority": float32(1),
		"tags":     map[string]string{"processType": "INJECTOR"},
	}
	rs["members"] = append(members, injectorMember)

	return nil
}

// hasInjectorSidecar returns true if the monarch-injector container is already present.
func hasInjectorSidecar(containers []corev1.Container) bool {
	for _, c := range containers {
		if c.Name == "monarch-injector" {
			return true
		}
	}
	return false
}
