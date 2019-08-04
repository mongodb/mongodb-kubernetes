package operator

import (
	"fmt"
	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ReconcileAppDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileAppDbReplicaSet struct {
	*ReconcileCommonController
}

func newAppDBReplicaSetReconciler(commonController *ReconcileCommonController) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{commonController}
}

// Reconcile reads that state of the cluster for a MongoDbReplicaSet object and makes changes based on the state read
// and what is in the MongoDbReplicaSet.Spec
func (r *ReconcileAppDbReplicaSet) Reconcile(opsManager *mongodb.MongoDBOpsManager, rs *mongodb.AppDB, credentials *Credentials) (res reconcile.Result, e error) {
	baseUrl := centralURL(opsManager)
	log := zap.S().With("ReplicaSet (AppDB)", objectKey(opsManager.Namespace, rs.Name()))

	err := r.updateStatus(opsManager, func(fresh Updatable) {
		(fresh.(*mongodb.MongoDBOpsManager)).UpdateReconcilingAppDb()
	})
	if err != nil {
		log.Errorf("Error setting state to reconciling: %s", err)
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}

	log.Info("AppDB ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs)
	log.Infow("ReplicaSet.Status", "status", opsManager.Status.AppDbStatus)

	conn := r.createOmConnection(baseUrl, credentials.PublicAPIKey, credentials.User)
	agentAPIKey, err := r.ensureAgentKeySecretExists(conn, opsManager.Namespace, "", log)
	if err != nil {
		return r.updateStatusFailedAppDb(opsManager, err.Error(), log)
	}
	podVars := createPodVars(rs, conn, agentAPIKey)

	// It's ok to pass 'opsManager' instance to statefulset constructor as it will be the owner for the appdb statefulset
	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(opsManager).
		SetName(rs.Name()).
		SetService(rs.ServiceName()).
		SetReplicas(rs.Members).
		SetPersistence(rs.Persistent).
		SetPodSpec(NewDefaultPodSpecWrapper(*rs.PodSpec)).
		SetPodVars(podVars).
		SetExposedExternally(rs.ExposedExternally).
		SetLogger(log).
		SetClusterName(rs.ClusterName)

	statefulSetObject := replicaBuilder.BuildStatefulSet()

	if rs.Members < opsManager.Status.AppDbStatus.Members {
		if err := prepareScaleDownReplicaSetAppDb(conn, statefulSetObject, opsManager.Status.AppDbStatus.Members, rs, log); err != nil {
			return r.updateStatusFailedAppDb(opsManager, fmt.Sprintf("Failed to prepare Replica Set for scaling down: %s", err), log)
		}
	}

	err = replicaBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return r.updateStatusFailedAppDb(opsManager, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	log.Info("Updated statefulset for replica set")

	if err := r.updateOmDeploymentRs(conn, opsManager, rs, statefulSetObject, log); err != nil {
		return r.updateStatusFailedAppDb(opsManager, fmt.Sprintf("Failed to create/update replica set in Ops Manager: %s", err), log)
	}

	log.Infof("Finished reconciliation for MongoDbReplicaSet! %s", completionMessage(conn.BaseURL(), conn.GroupID()))

	err = r.updateStatus(opsManager, func(fresh Updatable) {
		fresh.(*mongodb.MongoDBOpsManager).UpdateSuccessfulAppDb(rs, DeploymentLink(conn.BaseURL(), conn.GroupID()))
	})
	if err != nil {
		log.Errorf("Failed to update status for resource to successful: %s", err)
	} else {
		log.Infow("Successful update", "spec", rs)
	}
	return reconcile.Result{}, nil
}

func (r *ReconcileAppDbReplicaSet) createOmConnection(baseUrl string, apiKey string, adminUser string) om.Connection {
	omContext := om.OMContext{
		GroupID:      BackingGroupId,
		GroupName:    "BackingDb Group",
		OrgID:        "", // don't need the org id - it's only used to create mutex
		BaseURL:      baseUrl,
		PublicAPIKey: apiKey,
		User:         adminUser,
	}
	conn := r.omConnectionFactory(&omContext)
	return conn
}

func createPodVars(appDb *mongodb.AppDB, conn om.Connection, agentKey string) *PodVars {
	return &PodVars{BaseURL: conn.BaseURL(),
		ProjectID:   conn.GroupID(),
		User:        conn.User(),
		AgentAPIKey: agentKey,
		LogLevel:    appDb.LogLevel,
	}
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (r *ReconcileAppDbReplicaSet) updateOmDeploymentRs(conn om.Connection, opsManager *mongodb.MongoDBOpsManager, newResource *mongodb.AppDB, set *appsv1.StatefulSet, log *zap.SugaredLogger) error {

	err := waitForRsAgentsToRegister(set, newResource.ClusterName, conn, log)
	if err != nil {
		return err
	}
	replicaSet := buildReplicaSetFromStatefulSetAppDb(set, newResource, log)

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.MergeReplicaSet(replicaSet, nil)
			d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)
			processNames = d.GetProcessNames(om.ReplicaSet{}, replicaSet.Rs.Name())
			return nil
		},
		getMutex(conn.GroupName(), conn.OrgID()),
		log,
	)
	if err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return err
	}

	hostsBefore := getAllHostsRs(set, newResource.ClusterName, opsManager.Status.AppDbStatus.Members)
	hostsAfter := getAllHostsRs(set, newResource.ClusterName, newResource.Members)
	return calculateDiffAndStopMonitoringHosts(conn, hostsBefore, hostsAfter, log)
}

func (c *ReconcileAppDbReplicaSet) updateStatusFailedAppDb(resource *mongodb.MongoDBOpsManager, msg string, log *zap.SugaredLogger) (reconcile.Result, error) {
	msg = util.UpperCaseFirstChar(msg)

	log.Error(msg)
	// Resource may be nil if the reconciliation failed very early (on fetching the resource) and panic handling function
	// took over
	if resource != nil {
		err := c.updateStatus(resource, func(fresh Updatable) {
			fresh.(*mongodb.MongoDBOpsManager).UpdateErrorAppDb(msg)
		})
		if err != nil {
			log.Errorf("Failed to update resource status: %s", err)
		}
	}
	return retry()
}

func prepareScaleDownReplicaSetAppDb(omClient om.Connection, statefulSet *appsv1.StatefulSet, oldMembersCount int, new *mongodb.AppDB, log *zap.SugaredLogger) error {
	_, podNames := GetDnsForStatefulSetReplicasSpecified(statefulSet, new.ClusterName, oldMembersCount)
	podNames = podNames[new.Members:oldMembersCount]

	return prepareScaleDown(omClient, map[string][]string{new.Name(): podNames}, log)
}

func buildReplicaSetFromStatefulSetAppDb(set *appsv1.StatefulSet, mdb *mongodb.AppDB, log *zap.SugaredLogger) om.ReplicaSetWithProcesses {
	members := createProcessesAppDb(set, om.ProcessTypeMongod, mdb)
	rsWithProcesses := om.NewReplicaSetWithProcesses(om.NewReplicaSet(set.Name, mdb.Version), members)
	return rsWithProcesses
}

func createProcessesAppDb(set *appsv1.StatefulSet, mongoType om.MongoType,
	mdb *mongodb.AppDB) []om.Process {

	hostnames, names := GetDnsForStatefulSet(set, mdb.ClusterName)
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := calculateWiredTigerCache(set, mdb.Version)

	for idx, hostname := range hostnames {
		switch mongoType {
		case om.ProcessTypeMongod:
			processes[idx] = om.NewMongodProcessAppDB(names[idx], hostname, mdb)
			if wiredTigerCache != nil {
				processes[idx].SetWiredTigerCache(*wiredTigerCache)
			}
		default:
			panic("Dev error: Wrong process type passed!")
		}
	}

	return processes
}
