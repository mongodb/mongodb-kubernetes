package operator

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"reflect"
	"strings"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const DefaultWaitForReadinessSeconds = 5

type VersionManifest struct {
	Updated  int                       `json:"updated"`
	Versions []om.MongoDbVersionConfig `json:"versions"`
}

// ReconcileAppDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileAppDbReplicaSet struct {
	*ReconcileCommonController
}

func newAppDBReplicaSetReconciler(commonController *ReconcileCommonController) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{commonController}
}

// Reconcile deploys the "headless" agent, and wait until it reaches the goal state
func (r *ReconcileAppDbReplicaSet) Reconcile(opsManager *mdbv1.MongoDBOpsManager, rs *mdbv1.AppDB) (res reconcile.Result, e error) {
	log := zap.S().With("ReplicaSet (AppDB)", objectKey(opsManager.Namespace, rs.Name()))

	err := r.updateStatus(opsManager, func(fresh Updatable) {
		(fresh.(*mdbv1.MongoDBOpsManager)).UpdateReconcilingAppDb()
	})
	if err != nil {
		log.Errorf("Error setting state to reconciling: %s", err)
		return retry()
	}

	log.Info("AppDB ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs)
	log.Infow("ReplicaSet.Status", "status", opsManager.Status.AppDbStatus)

	// It's ok to pass 'opsManager' instance to statefulset constructor as it will be the owner for the appdb statefulset
	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(opsManager).
		SetName(rs.Name()).
		SetService(rs.ServiceName()).
		SetPodVars(&PodVars{}). // TODO remove
		SetLogger(log).
		SetClusterName(opsManager.ClusterName).
		SetVersion(opsManager.Spec.Version) // the version of the appdb image must match the OM image one

	config, err := buildAppDbAutomationConfig(rs, opsManager, replicaBuilder.BuildAppDBStatefulSet(), log)
	if err != nil {
		return r.updateStatusFailedAppDb(opsManager, err.Error(), log)
	}

	if err = r.publishAutomationConfig(rs, opsManager, config, log); err != nil {
		return r.updateStatusFailedAppDb(opsManager, err.Error(), log)
	}

	/* TODO CLOUDP-51015
		if rs.Members < opsManager.Status.AppDbStatus.Members {
		if err := prepareScaleDownReplicaSetAppDb(conn, statefulSetObject, opsManager.Status.AppDbStatus.Members, rs, log); err != nil {
			return r.updateStatusFailedAppDb(opsManager, fmt.Sprintf("Failed to prepare Replica Set for scaling down: %s", err), log)
		}
	}*/

	err = replicaBuilder.CreateOrUpdateAppDBInKubernetes()
	if err != nil {
		return r.updateStatusFailedAppDb(opsManager, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	// For the headless agent we cannot check the readiness state of the StatefulSet right away as we rely on
	// readiness status and for the already running pods they are supposed to go from "ready" to "not ready" in maximum
	// 5 seconds (this is the time between readiness.go launches), so 7 seconds (2 inside the method) should be enough
	log.Debugf("Waiting for %d seconds to make sure readiness status is up-to-date", DefaultWaitForReadinessSeconds+util.DefaultK8sCacheRefreshTimeSeconds)
	time.Sleep(time.Duration(util.ReadEnvVarIntOrDefault(util.AppDBReadinessWaitEnv, DefaultWaitForReadinessSeconds)) * time.Second)

	if !r.kubeHelper.isStatefulSetUpdated(opsManager.Namespace, opsManager.Name+"-db", log) {
		return r.updateStatusPendingAppDb(opsManager, fmt.Sprintf("AppDB Statefulset is not ready yet"), log)
	}

	log.Infof("Finished reconciliation for AppDB ReplicaSet!")

	err = r.updateStatus(opsManager, func(fresh Updatable) {
		fresh.(*mdbv1.MongoDBOpsManager).UpdateSuccessfulAppDb(rs)
	})
	if err != nil {
		log.Errorf("Failed to update status for resource to successful: %s", err)
	} else {
		log.Infow("Successful update", "spec", rs)
	}
	return reconcile.Result{}, nil
}

func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(rs *mdbv1.AppDB, opsManager *mdbv1.MongoDBOpsManager, automationConfig *om.AutomationConfig, log *zap.SugaredLogger) error {
	// Create/update the automation config configMap if it changed.
	// Note, that the 'version' field is incremented if there are changes (emulating the db versioning mechanism)
	// No optimistic concurrency control is done - there cannot be a concurrent reconciliation for the same Ops Manager
	// object and the probability that the user will edit the config map manually in the same time is extremely low
	if err := r.kubeHelper.computeConfigMap(objectKey(opsManager.Namespace, rs.AutomationConfigSecretName()),
		func(existingMap *corev1.ConfigMap) bool {
			if len(existingMap.Data) == 0 {
				log.Debugf("ConfigMap for the Automation Config doesn't exist, it will be created")
			} else if existingDeployment, err := om.BuildDeploymentFromBytes([]byte(existingMap.Data["cluster-config.json"])); err != nil {
				// in case of any problems deserializing the existing Deployment - just ignore the error and update
				log.Warnf("There were problems deserializing existing automation config - it will be overwritten (%s)", err.Error())
			} else {
				// Otherwise there is an existing automation config and we need to compare it with the Operator version

				// Aligning the versions to make deep comparison correct
				automationConfig.SetVersion(existingDeployment.Version())

				// If the deployments are the same - we shouldn't perform the update
				// We cannot compare the deployments directly as the "operator" version contains some struct members
				// So we need to turn them into maps
				if reflect.DeepEqual(existingDeployment, automationConfig.Deployment.ToCanonicalForm()) {
					log.Debugf("Automation Config hasn't changed - not updating ConfigMap")
					return false
				}

				// Otherwise we increase the version
				automationConfig.SetVersion(existingDeployment.Version() + 1)
				log.Debugf("The Automation Config change detected, increasing the version %d -> %d", existingDeployment.Version(), existingDeployment.Version()+1)
			}

			// By this time we have the AutomationConfig we want to push
			bytes, err := automationConfig.Serialize()
			if err != nil {
				// this definitely cannot happen and means the dev error - simply panicing to make sure the resource gets
				// to error state
				panic(err)
			}
			existingMap.Data = map[string]string{"cluster-config.json": string(bytes)}
			return true
		}, opsManager); err != nil {
		return err
	}
	return nil
}

func buildAppDbAutomationConfig(rs *mdbv1.AppDB, opsManager *mdbv1.MongoDBOpsManager, set *appsv1.StatefulSet, log *zap.SugaredLogger) (*om.AutomationConfig, error) {
	d := om.NewDeployment()

	replicaSet := buildReplicaSetFromStatefulSetAppDb(set, rs, log)

	d.MergeReplicaSet(replicaSet, nil)
	d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)

	automationConfig := om.NewAutomationConfig(d)
	automationConfig.SetOptions("/tmp/mms-automation/test/versions")
	automationConfig.SetBaseUrlForAgents(centralURL(opsManager))

	if err := addLatestMongodbVersions(automationConfig, log); err != nil {
		return nil, err
	}
	// Setting the default version - will be used if no automation config has been published before
	automationConfig.SetVersion(1)
	return automationConfig, nil
}

func addLatestMongodbVersions(config *om.AutomationConfig, log *zap.SugaredLogger) error {
	start := time.Now()
	client, err := util.NewHTTPClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(fmt.Sprintf("https://opsmanager.mongodb.com/static/version_manifest/%s.json", util.LatestOmVersion))
	if err != nil {
		return err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	versionManifest := &VersionManifest{}
	err = json.Unmarshal(body, &versionManifest)
	if err != nil {
		return err
	}
	fixLinks(versionManifest.Versions)
	config.SetMongodbVersions(versionManifest.Versions)

	log.Debugf("Mongodb version manifest %s downloaded, took %s", util.LatestOmVersion, time.Since(start))
	return nil
}

// fixLinks iterates over build links and prefixes them with a correct domain
// (see mms AutomationMongoDbVersionSvc#buildRemoteUrl)
func fixLinks(configs []om.MongoDbVersionConfig) {
	for _, version := range configs {
		for _, build := range version.Builds {
			if strings.HasSuffix(version.Name, "-ent") {
				build.Url = "https://downloads.mongodb.com" + build.Url
			} else {
				build.Url = "https://fastdl.mongodb.org" + build.Url
			}
			// AA expects not nil element
			if build.Modules == nil {
				build.Modules = []string{}
			}
		}
	}
}

func (c *ReconcileAppDbReplicaSet) updateStatusFailedAppDb(resource *mdbv1.MongoDBOpsManager, msg string, log *zap.SugaredLogger) (reconcile.Result, error) {
	msg = util.UpperCaseFirstChar(msg)

	log.Error(msg)
	// Resource may be nil if the reconciliation failed very early (on fetching the resource) and panic handling function
	// took over
	if resource != nil {
		err := c.updateStatus(resource, func(fresh Updatable) {
			fresh.(*mdbv1.MongoDBOpsManager).UpdateErrorAppDb(msg)
		})
		if err != nil {
			log.Errorf("Failed to update resource status: %s", err)
		}
	}
	return retry()
}

func (c *ReconcileAppDbReplicaSet) updateStatusPendingAppDb(resource *mdbv1.MongoDBOpsManager, msg string, log *zap.SugaredLogger) (reconcile.Result, error) {
	msg = util.UpperCaseFirstChar(msg)

	log.Info(msg)

	err := c.updateStatus(resource, func(fresh Updatable) {
		fresh.(*mdbv1.MongoDBOpsManager).UpdatePendingAppDb(msg)
	})
	if err != nil {
		return fail(err)
	}
	return retry()
}

// FIXME: this should be used and implemented before GA of the OM managed by
// the Operator project
//func prepareScaleDownReplicaSetAppDb(omClient om.Connection, statefulSet *appsv1.StatefulSet, oldMembersCount int, new *mongodb.AppDB, log *zap.SugaredLogger) error {
//_, podNames := GetDnsForStatefulSetReplicasSpecified(statefulSet, new.ClusterName, oldMembersCount)
//podNames = podNames[new.Members:oldMembersCount]

//return prepareScaleDown(omClient, map[string][]string{new.Name(): podNames}, log)
//}

func buildReplicaSetFromStatefulSetAppDb(set *appsv1.StatefulSet, mdb *mdbv1.AppDB, log *zap.SugaredLogger) om.ReplicaSetWithProcesses {
	members := createProcessesAppDb(set, om.ProcessTypeMongod, mdb)
	replicaSet := om.NewReplicaSet(set.Name, mdb.Version)
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members)
	return rsWithProcesses
}

func createProcessesAppDb(set *appsv1.StatefulSet, mongoType om.MongoType,
	mdb *mdbv1.AppDB) []om.Process {

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
