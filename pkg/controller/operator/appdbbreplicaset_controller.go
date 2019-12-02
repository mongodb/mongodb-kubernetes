package operator

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io/ioutil"
	"reflect"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
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
func (r *ReconcileAppDbReplicaSet) Reconcile(opsManager *mdbv1.MongoDBOpsManager, rs *mdbv1.AppDB, opsManagerUserPassword string) (res reconcile.Result, e error) {
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

	config, err := r.buildAppDbAutomationConfig(rs, opsManager, opsManagerUserPassword, replicaBuilder.BuildAppDBStatefulSet(), log)
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

// generateScramShaCredentials generates both ScramSha1Creds and ScramSha256Creds. The ScramSha256Creds
// will not be used, but it makes comparisons with the deployment simpler to generate them both, and would make
// changing to use ScramSha256 trivial once supported by the Java driver.
func generateScramShaCredentials(password string, opsManager *mdbv1.MongoDBOpsManager) (*om.ScramShaCreds, *om.ScramShaCreds, error) {
	sha256Creds, err := authentication.ComputeScramShaCreds(util.OpsManagerMongoDBUserName, password, getOpsManagerUserSalt(opsManager, sha256.New), authentication.ScramSha256)
	if err != nil {
		return nil, nil, err
	}

	sha1Creds, err := authentication.ComputeScramShaCreds(util.OpsManagerMongoDBUserName, password, getOpsManagerUserSalt(opsManager, sha1.New), authentication.MongoDBCR)
	if err != nil {
		return nil, nil, err
	}
	return sha1Creds, sha256Creds, nil
}

// getOpsManagerUserSalt returns a deterministic salt based on the name of the resource.
// the required number of characters will be taken based on the requirements for the SCRAM-SHA-1/MONGODB-CR algorithm
func getOpsManagerUserSalt(om *mdbv1.MongoDBOpsManager, hashConstructor func() hash.Hash) []byte {
	sha256bytes32 := sha256.Sum256([]byte(fmt.Sprintf("%s-mongodbopsmanager", om.Name)))
	return sha256bytes32[:hashConstructor().Size()-authentication.RFC5802MandatedSaltSize]
}

// ensureConsistentAgentAuthenticationCredentials makes sure that if there are existing authentication credentials
// specified, we use those instead of always generating new ones which would cause constant remounting of the config map
func ensureConsistentAgentAuthenticationCredentials(newAutomationConfig *om.AutomationConfig, existingAutomationConfig *om.AutomationConfig, log *zap.SugaredLogger) error {
	// we will keep existing automation agent password
	if existingAutomationConfig.Auth.AutoPwd != "" {
		log.Debug("Agent password has already been generated, using existing password")
		newAutomationConfig.Auth.AutoPwd = existingAutomationConfig.Auth.AutoPwd
	} else {
		log.Debug("Generating new automation agent password")
		if _, err := newAutomationConfig.EnsurePassword(); err != nil {
			return err
		}
	}

	// keep existing keyfile contents
	if existingAutomationConfig.Auth.Key != "" {
		log.Debug("Keyfile contents have already been generated, using existing keyfile contents")
		newAutomationConfig.Auth.Key = existingAutomationConfig.Auth.Key
	} else {
		log.Debug("Generating new keyfile contents")
		if err := newAutomationConfig.EnsureKeyFileContents(); err != nil {
			return err
		}
	}

	return newAutomationConfig.Apply()
}

func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(rs *mdbv1.AppDB,
	opsManager *mdbv1.MongoDBOpsManager, automationConfig *om.AutomationConfig, log *zap.SugaredLogger) error {
	// Create/update the automation config configMap if it changed.
	// Note, that the 'version' field is incremented if there are changes (emulating the db versioning mechanism)
	// No optimistic concurrency control is done - there cannot be a concurrent reconciliation for the same Ops Manager
	// object and the probability that the user will edit the config map manually in the same time is extremely low
	if err := r.kubeHelper.computeConfigMap(objectKey(opsManager.Namespace, rs.AutomationConfigSecretName()),
		func(existingMap *corev1.ConfigMap) bool {
			if len(existingMap.Data) == 0 {
				log.Debugf("ConfigMap for the Automation Config doesn't exist, it will be created")
			} else if existingAutomationConfig, err := om.BuildAutomationConfigFromBytes([]byte(existingMap.Data[util.AppDBAutomationConfigKey])); err != nil {
				// in case of any problems deserializing the existing AutomationConfig - just ignore the error and update
				log.Warnf("There were problems deserializing existing automation config - it will be overwritten (%s)", err.Error())
			} else {
				// Otherwise there is an existing automation config and we need to compare it with the Operator version

				// Aligning the versions to make deep comparison correct
				automationConfig.SetVersion(existingAutomationConfig.Deployment.Version())

				log.Debug("ensuring authentication credentials")
				if err := ensureConsistentAgentAuthenticationCredentials(automationConfig, existingAutomationConfig, log); err != nil {
					log.Warnf("error ensuring consistent authentication credentials: %s", err)
					return false
				}

				// If the deployments are the same - we shouldn't perform the update
				// We cannot compare the deployments directly as the "operator" version contains some struct members
				// So we need to turn them into maps
				if reflect.DeepEqual(existingAutomationConfig.Deployment, automationConfig.Deployment.ToCanonicalForm()) {
					log.Debugf("Automation Config hasn't changed - not updating ConfigMap")
					return false
				}

				// Otherwise we increase the version
				automationConfig.SetVersion(existingAutomationConfig.Deployment.Version() + 1)
				log.Debugf("The Automation Config change detected, increasing the version %d -> %d", existingAutomationConfig.Deployment.Version(), existingAutomationConfig.Deployment.Version()+1)
			}

			// By this time we have the AutomationConfig we want to push
			bytes, err := automationConfig.Serialize()
			if err != nil {
				// this definitely cannot happen and means the dev error - simply panicking to make sure the resource gets
				// to error state
				panic(err)
			}
			existingMap.Data = map[string]string{util.AppDBAutomationConfigKey: string(bytes)}
			return true
		}, opsManager); err != nil {
		return err
	}
	return nil
}

func (r *ReconcileAppDbReplicaSet) buildAppDbAutomationConfig(rs *mdbv1.AppDB, opsManager *mdbv1.MongoDBOpsManager, opsManagerUserPassword string, set *appsv1.StatefulSet, log *zap.SugaredLogger) (*om.AutomationConfig, error) {
	d := om.NewDeployment()

	replicaSet := buildReplicaSetFromStatefulSetAppDb(set, rs, log)

	d.MergeReplicaSet(replicaSet, nil)
	d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)

	automationConfig := om.NewAutomationConfig(d)
	automationConfig.SetOptions("/tmp/mms-automation/test/versions")
	automationConfig.SetBaseUrlForAgents(centralURL(opsManager))

	sha1Creds, sha256Creds, err := generateScramShaCredentials(opsManagerUserPassword, opsManager)

	if err != nil {
		return nil, err
	}

	if err := configureScramShaAuthentication(automationConfig, sha1Creds, sha256Creds, log); err != nil {
		return nil, err
	}

	if err := addLatestMongodbVersions(automationConfig, log); err != nil {
		return nil, err
	}
	// Setting the default version - will be used if no automation config has been published before
	automationConfig.SetVersion(1)
	return automationConfig, nil
}

// configureScramShaAuthentication configures agent and deployment authentication mechanisms using SCRAM-SHA-1
// and also adds the Ops Manager MongoDB user to the automation config.
func configureScramShaAuthentication(automationConfig *om.AutomationConfig, sha1Creds, sha256Creds *om.ScramShaCreds, log *zap.SugaredLogger) error {
	// we currently only support SCRAM-SHA-1/MONGODB-CR with the AppDB due to older Java driver
	scramSha1 := authentication.NewAutomationConfigScramSha1(automationConfig)

	// scram deployment mechanisms need to be configured before agent auth can be configured
	if err := scramSha1.EnableDeploymentAuthentication(); err != nil {
		return err
	}

	// we set AuthoritativeSet to false which ensures that it is possible to add additional AppDB
	// MongoDB users if required which will not be deleted by the operator. We will never be dealing with
	// a multi agent environment here.
	authOpts := authentication.Options{AuthoritativeSet: false, OneAgent: true}
	if err := scramSha1.EnableAgentAuthentication(authOpts, log); err != nil {
		return err
	}

	opsManagerUser := buildOpsManagerUser(sha1Creds, sha256Creds)
	automationConfig.Auth.AddUser(opsManagerUser)

	// update the underlying deployment with the changes made
	return automationConfig.Apply()
}

func buildOpsManagerUser(scramSha1Creds, scramSha256Creds *om.ScramShaCreds) om.MongoDBUser {
	return om.MongoDBUser{
		Username:                   util.OpsManagerMongoDBUserName,
		Database:                   util.DefaultUserDatabase,
		AuthenticationRestrictions: []string{},
		Mechanisms:                 []string{},
		ScramSha1Creds:             scramSha1Creds,
		ScramSha256Creds:           scramSha256Creds,

		// required roles for the AppDB user are outlined in the documentation
		// https://docs.opsmanager.mongodb.com/current/tutorial/prepare-backing-mongodb-instances/#replica-set-security
		Roles: []*om.Role{
			{
				Role:     "readWriteAnyDatabase",
				Database: "admin",
			},
			{
				Role:     "dbAdminAnyDatabase",
				Database: "admin",
			},
			{
				Role:     "clusterMonitor",
				Database: "admin",
			},
		},
	}
}

func addLatestMongodbVersions(config *om.AutomationConfig, log *zap.SugaredLogger) error {
	start := time.Now()
	client, err := api.NewHTTPClient()
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

func (r *ReconcileAppDbReplicaSet) updateStatusFailedAppDb(resource *mdbv1.MongoDBOpsManager, msg string, log *zap.SugaredLogger) (reconcile.Result, error) {
	msg = util.UpperCaseFirstChar(msg)

	log.Error(msg)
	// Resource may be nil if the reconciliation failed very early (on fetching the resource) and panic handling function
	// took over
	if resource != nil {
		err := r.updateStatus(resource, func(fresh Updatable) {
			fresh.(*mdbv1.MongoDBOpsManager).UpdateErrorAppDb(msg)
		})
		if err != nil {
			log.Errorf("Failed to update resource status: %s", err)
		}
	}
	return retry()
}

func (r *ReconcileAppDbReplicaSet) updateStatusPendingAppDb(resource *mdbv1.MongoDBOpsManager, msg string, log *zap.SugaredLogger) (reconcile.Result, error) {
	msg = util.UpperCaseFirstChar(msg)

	log.Info(msg)

	err := r.updateStatus(resource, func(fresh Updatable) {
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

	hostnames, names := util.GetDnsForStatefulSet(set, mdb.ClusterName)
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
