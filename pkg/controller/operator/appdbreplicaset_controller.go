package operator

import (
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"reflect"
	"time"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/scale"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/manifest"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/project"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/host"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const DefaultWaitForReadinessSeconds = 13

// ReconcileAppDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileAppDbReplicaSet struct {
	*ReconcileCommonController
	VersionManifestFilePath  string
	InternetManifestProvider manifest.Provider
}

func newAppDBReplicaSetReconciler(commonController *ReconcileCommonController, appDbVersionManifestPath string) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{ReconcileCommonController: commonController, VersionManifestFilePath: appDbVersionManifestPath, InternetManifestProvider: manifest.InternetProvider{}}
}

// Reconcile deploys the "headless" agent, and wait until it reaches the goal state
func (r *ReconcileAppDbReplicaSet) Reconcile(opsManager *omv1.MongoDBOpsManager, rs omv1.AppDB, opsManagerUserPassword string) (res reconcile.Result, e error) {
	log := zap.S().With("ReplicaSet (AppDB)", objectKey(opsManager.Namespace, rs.Name()))

	appDbStatusOption := status.NewOMPartOption(status.AppDb)
	result, err := r.updateStatus(opsManager, workflow.Reconciling(), log, appDbStatusOption)
	if err != nil {
		return result, err
	}

	log.Info("AppDB ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs)
	log.Infow("ReplicaSet.Status", "status", opsManager.Status.AppDbStatus)

	podVars, err := r.tryConfigureMonitoringInOpsManager(opsManager, opsManagerUserPassword, log)
	// it's possible that Ops Manager will not be available when we attempt to configure AppDB monitoring
	// in Ops Manager. This is not a blocker to continue with the reset of the reconcilliation.
	if err != nil {
		log.Warnf("Unable to configure monitoring of AppDB: %s, configuration will be attempted next reconcilliation.", err)
	}

	// Providing the default size of pod as otherwise sometimes the agents in pod complain about not enough memory
	// on mongodb download: "write /tmp/mms-automation/test/versions/mongodb-linux-x86_64-4.0.0/bin/mongo: cannot
	// allocate memory"
	appdbPodSpec := NewDefaultPodSpecWrapper(*rs.PodSpec)
	appdbPodSpec.Default.MemoryRequests = util.DefaultMemoryAppDB

	// It's ok to pass 'opsManager' instance to statefulset constructor as it will be the owner for the appdb statefulset
	replicaBuilder := r.kubeHelper.NewStatefulSetHelper(opsManager).
		SetReplicas(scale.ReplicasThisReconciliation(opsManager)).
		SetName(rs.Name()).
		SetService(rs.ServiceName()).
		SetServicePort(rs.MongoDbSpec.AdditionalMongodConfig.GetPortOrDefault()).
		SetPodVars(podVars).
		SetStartupParameters(rs.Agent.StartupParameters).
		SetLogger(log).
		SetPodSpec(appdbPodSpec).
		SetClusterName(opsManager.ClusterName).
		SetSecurity(rs.Security).
		//TODO Remove?
		SetVersion(opsManager.Spec.Version). // the version of the appdb image must match the OM image one
		SetContainerName(util.AppDbContainerName)
	// TODO: configure once StatefulSetConfiguration is supported for appDb
	//SetStatefulSetConfiguration(opsManager.Spec.AppDB.StatefulSetConfiguration)

	appDbSts, err := replicaBuilder.BuildAppDbStatefulSet()
	if err != nil {
		return r.updateStatus(opsManager, workflow.Failed(err.Error()), log, appDbStatusOption)
	}

	status, wasPublished := r.deployAutomationConfig(opsManager, rs, opsManagerUserPassword, appDbSts, log)
	if !status.IsOK() {
		return r.updateStatus(opsManager, status, log, appDbStatusOption)
	}

	// Note that the only case when we need to wait for readiness probe to go from 'true' to 'false' is when we publish
	// the new config map - in this case we need to wait for some time to make sure the readiness probe has updated
	// the state (usually takes up to 5 seconds).
	if wasPublished {
		waitTimeout := envutil.ReadIntOrDefault(util.AppDBReadinessWaitEnv, DefaultWaitForReadinessSeconds)
		log.Debugf("Waiting for %d seconds to make sure readiness status is up-to-date", waitTimeout)
		time.Sleep(time.Duration(waitTimeout) * time.Second)
	}

	if status := r.deployStatefulSet(opsManager, replicaBuilder); !status.IsOK() {
		return r.updateStatus(opsManager, status, log, appDbStatusOption)
	}

	if podVars.ProjectID == "" {
		// this doesn't requeue the reconcilliation immediately, the calling OM controller
		// requeues after Ops Manager has been fully configured.
		log.Infof("Requeuing reconciliation to configure Monitoring in Ops Manager.")
		return r.updateStatus(opsManager, workflow.OK().Requeue(), log, appDbStatusOption, scale.MembersOption(opsManager))
	}

	if scale.IsStillScaling(opsManager) {
		return r.updateStatus(opsManager, workflow.Pending("Continuing scaling operation on AppDB desiredMembers=%d, currentMembers=%d",
			opsManager.DesiredReplicaSetMembers(), scale.ReplicasThisReconciliation(opsManager)), log, appDbStatusOption, scale.MembersOption(opsManager))
	}

	log.Infof("Finished reconciliation for AppDB ReplicaSet!")

	return r.updateStatus(opsManager, workflow.OK(), log, appDbStatusOption, scale.MembersOption(opsManager))
}

// ensureLegacyConfigMapRemoved makes sure that the ConfigMap which stored the automation config
// is removed. It is now stored in a Secret instead.
func ensureLegacyConfigMapRemoved(deleter configmap.Deleter, rs omv1.AppDB) error {
	err := deleter.DeleteConfigMap(kube.ObjectKey(rs.Namespace, rs.AutomationConfigSecretName()))
	return client.IgnoreNotFound(err)
}

// generateScramShaCredentials generates both ScramSha1Creds and ScramSha256Creds. The ScramSha256Creds
// will not be used, but it makes comparisons with the deployment simpler to generate them both, and would make
// changing to use ScramSha256 trivial once supported by the Java driver.
func generateScramShaCredentials(password string, opsManager omv1.MongoDBOpsManager) (*om.ScramShaCreds, *om.ScramShaCreds, error) {
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
func getOpsManagerUserSalt(om omv1.MongoDBOpsManager, hashConstructor func() hash.Hash) []byte {
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

// publishAutomationConfig publishes the automation config to the Secret if necessary. Note that it's done only
// if the automation config has changed - the version is incremented in this case.
// Method returns 'bool' to indicate if the config was published.
// No optimistic concurrency control is done - there cannot be a concurrent reconciliation for the same Ops Manager
// object and the probability that the user will edit the config map manually in the same time is extremely low
func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(rs omv1.AppDB,
	opsManager omv1.MongoDBOpsManager, automationConfig *om.AutomationConfig, log *zap.SugaredLogger) (bool, error) {
	wasPublished := false
	if err := r.kubeHelper.computeSecret(objectKey(opsManager.Namespace, rs.AutomationConfigSecretName()),
		func(existingSecret *corev1.Secret) bool {
			if len(existingSecret.Data) == 0 {
				log.Debugf("ConfigMap for the Automation Config doesn't exist, it will be created")
			} else if existingAutomationConfig, err := om.BuildAutomationConfigFromBytes([]byte(existingSecret.Data[util.AppDBAutomationConfigKey])); err != nil {
				// in case of any problems deserializing the existing AutomationConfig - just ignore the error and update
				log.Warnf("There were problems deserializing existing automation config - it will be overwritten (%s)", err.Error())
			} else {
				// Otherwise there is an existing automation config and we need to compare it with the Operator version

				// Aligning the versions to make deep comparison correct
				automationConfig.SetVersion(existingAutomationConfig.Deployment.Version())

				log.Debug("Ensuring authentication credentials")
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
			existingSecret.Data = map[string][]byte{util.AppDBAutomationConfigKey: bytes}
			wasPublished = true

			return true
		}, &opsManager); err != nil {
		return false, err
	}
	return wasPublished, nil
}

func (r ReconcileAppDbReplicaSet) buildAppDbAutomationConfig(rs omv1.AppDB, opsManager omv1.MongoDBOpsManager, opsManagerUserPassword string, set appsv1.StatefulSet, log *zap.SugaredLogger) (*om.AutomationConfig, error) {
	d := om.NewDeployment()

	replicaSet := buildReplicaSetFromStatefulSetAppDb(set, rs)

	d.MergeReplicaSet(replicaSet, nil)

	automationConfig := om.NewAutomationConfig(d)
	automationConfig.SetOptions(util.AgentDownloadsDir)

	d.AddMonitoring(log, rs.GetTLSConfig().IsEnabled())
	automationConfig.SetBaseUrlForAgents(opsManager.CentralURL())

	sha1Creds, sha256Creds, err := generateScramShaCredentials(opsManagerUserPassword, opsManager)

	if err != nil {
		return nil, err
	}

	if err := configureScramShaAuthentication(automationConfig, sha1Creds, sha256Creds, opsManager, log); err != nil {
		return nil, err
	}

	if err := r.configureMongoDBVersions(automationConfig, rs, log); err != nil {
		return nil, err
	}
	// Setting the default version - will be used if no automation config has been published before
	automationConfig.SetVersion(1)
	return automationConfig, nil
}

// configureScramShaAuthentication configures agent and deployment authentication mechanisms using both SHA-1 and SHA-256
// mechanisms. This is actual for 1.8.0 version of the Operator and needs to be changed after 3-4 versions released -
// the OM 4.4 should get support for SHA-256 only.
// Unfortunately we cannot move to SHA-256 right away due to Agent limitation which requires to do a stepped move
// (SHA-1 -> SHA-1, SHA-256, SHA-256)
func configureScramShaAuthentication(automationConfig *om.AutomationConfig, sha1Creds, sha256Creds *om.ScramShaCreds, opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	scramSha1 := authentication.NewAutomationConfigScramSha1(automationConfig)

	if err := scramSha1.EnableDeploymentAuthentication(authentication.Options{}); err != nil {
		return err
	}
	scramSha256 := authentication.NewAutomationConfigScramSha256(automationConfig)
	if err := scramSha256.EnableDeploymentAuthentication(authentication.Options{}); err != nil {
		return err
	}
	// Adding SCRAM-SHA-256 to the list of mechanisms for the agent
	automationConfig.Auth.AutoAuthMechanisms = append(automationConfig.Auth.AutoAuthMechanisms, string(authentication.ScramSha256))

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

func (r ReconcileAppDbReplicaSet) configureMongoDBVersions(config *om.AutomationConfig, rs omv1.AppDB, log *zap.SugaredLogger) error {
	if rs.GetVersion() == util.BundledAppDbMongoDBVersion {
		versionManifest, err := manifest.FileProvider{FilePath: r.VersionManifestFilePath}.GetVersion()
		if err != nil {
			return err
		}
		config.SetMongodbVersions(versionManifest.Versions)
		log.Infof("Using bundled MongoDB version: %s", util.BundledAppDbMongoDBVersion)
		return nil
	} else {
		return r.addLatestMongoDBVersions(config, log)
	}
}

func (r *ReconcileAppDbReplicaSet) addLatestMongoDBVersions(config *om.AutomationConfig, log *zap.SugaredLogger) error {
	start := time.Now()
	versionManifest, err := r.InternetManifestProvider.GetVersion()
	if err != nil {
		return err
	}
	config.SetMongodbVersions(versionManifest.Versions)
	log.Debugf("Mongodb version manifest %s downloaded, took %s", util.LatestOmVersion, time.Since(start))
	return nil
}

// registerAppDBHostsWithProject uses the Hosts API to add each process in the AppBD to the project
func (r *ReconcileAppDbReplicaSet) registerAppDBHostsWithProject(opsManager *omv1.MongoDBOpsManager, conn om.Connection, opsManagerPassword string, log *zap.SugaredLogger) error {
	appDbStatefulSet, err := r.client.GetStatefulSet(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()))
	if err != nil {
		return err
	}

	hostnames, _ := util.GetDnsForStatefulSet(appDbStatefulSet, opsManager.Spec.AppDB.GetClusterDomain())
	getHostsResult, err := conn.GetHosts()
	if err != nil {
		return fmt.Errorf("error fetching existing hosts: %s", err)
	}

	for _, hostname := range hostnames {
		appDbHost := host.Host{
			Port:              util.MongoDbDefaultPort,
			Username:          util.OpsManagerMongoDBUserName,
			Password:          opsManagerPassword,
			Hostname:          hostname,
			AuthMechanismName: "MONGODB_CR",
		}
		if host.Contains(getHostsResult.Results, appDbHost) {
			continue
		}
		log.Debugf("Registering AppDB host %s with project %s", hostname, conn.GroupID())
		if err := conn.AddHost(appDbHost); err != nil {
			return fmt.Errorf("error adding appdb host %s", err)
		}
	}
	return nil
}

// ensureAppDbAgentApiKey makes sure there is an agent API key for the AppDB automation agent
func (r *ReconcileAppDbReplicaSet) ensureAppDbAgentApiKey(opsManager *omv1.MongoDBOpsManager, conn om.Connection, log *zap.SugaredLogger) error {
	agentKeyFromSecret, err := secret.ReadKey(r.client, util.OmAgentApiKey, kube.ObjectKey(opsManager.Namespace, agentApiKeySecretName(conn.GroupID())))
	err = client.IgnoreNotFound(err)
	if err != nil {
		return fmt.Errorf("error reading secret %s: %s", objectKey(opsManager.Namespace, agentApiKeySecretName(conn.GroupID())), err)
	}

	if err := r.ensureAgentKeySecretExists(conn, opsManager.Namespace, agentKeyFromSecret, log); err != nil {
		return fmt.Errorf("error ensuring agent key secret exists: %s", err)
	}
	return nil
}

// tryConfigureMonitoringInOpsManager attempts to configure monitoring in Ops Manager. This might not be possible if Ops Manager
// has not been created yet, if that is the case, an empty PodVars will be returned.
func (r *ReconcileAppDbReplicaSet) tryConfigureMonitoringInOpsManager(opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) (*PodEnvVars, error) {
	cred, err := project.ReadCredentials(r.kubeHelper.client, kube.ObjectKey(operatorNamespace(), opsManager.APIKeySecretName()))
	if err != nil {
		log.Debugf("Ops Manager has not yet been created, not configuring monitoring: %s", err)
		return &PodEnvVars{}, nil
	}
	log.Debugf("Ensuring monitoring of AppDB is configured in Ops Manager")

	existingPodVars, err := r.readExistingPodVars(*opsManager)
	if client.IgnoreNotFound(err) != nil {
		return &PodEnvVars{}, fmt.Errorf("error reading existing podVars: %s", err)
	}

	projectConfig := opsManager.GetAppDBProjectConfig()
	_, conn, err := project.ReadOrCreateProject(projectConfig, cred, r.omConnectionFactory, log)
	if err != nil {
		return existingPodVars, fmt.Errorf("error reading/creating project: %s", err)
	}

	if err := r.registerAppDBHostsWithProject(opsManager, conn, opsManagerUserPassword, log); err != nil {
		return existingPodVars, fmt.Errorf("error registering hosts with project: %s", err)
	}

	if err := r.ensureAppDbAgentApiKey(opsManager, conn, log); err != nil {
		return existingPodVars, fmt.Errorf("error ensuring AppDB agent api key: %s", err)
	}

	if err := markAppDBAsBackingProject(conn, log); err != nil {
		return existingPodVars, fmt.Errorf("error marking project has backing db: %s", err)
	}

	cm := configmap.Builder().
		SetName(opsManager.Spec.AppDB.ProjectIDConfigMapName()).
		SetNamespace(opsManager.Namespace).
		SetField(util.AppDbProjectIdKey, conn.GroupID()).
		Build()

	if err := configmap.CreateOrUpdate(r.kubeHelper.client, cm); err != nil {
		return existingPodVars, fmt.Errorf("error creating ConfigMap: %s", err)
	}

	return &PodEnvVars{User: conn.User(), ProjectID: conn.GroupID(), SSLProjectConfig: mdbv1.SSLProjectConfig{
		SSLMMSCAConfigMap: opsManager.Spec.GetOpsManagerCA(),
	},
	}, nil
}

// readExistingPodVars is a backup function which provides the required podVars for the AppDB
// in the case of Ops Manager not being reachable. An example of when this is used is:
// 1. The AppDB starts as normal
// 2. Ops Manager starts as normal
// 3. The AppDB password was configured mid-reconciliation
// 4. AppDB reconciles and attempts to configure monitoring, but this is not possible
// as OM cannot currently connect to the AppDB as it has not yet been provided the updated password.
// In such a case, we cannot read the groupId from OM, so we fall back to the ConfigMap we created
// before hand. This is required as with empty PodVars this would trigger an unintentional
// rolling restart of the AppDB.
func (r *ReconcileAppDbReplicaSet) readExistingPodVars(om omv1.MongoDBOpsManager) (*PodEnvVars, error) {
	cm, err := r.kubeHelper.client.GetConfigMap(objectKey(om.Namespace, om.Spec.AppDB.ProjectIDConfigMapName()))
	if err != nil {
		return nil, err
	}
	var projectId string
	if projectId = cm.Data[util.AppDbProjectIdKey]; projectId == "" {
		return nil, fmt.Errorf("ConfigMap %s did not have the key %s", om.Spec.AppDB.ProjectIDConfigMapName(), util.AppDbProjectIdKey)
	}

	cred, err := project.ReadCredentials(r.kubeHelper.client, objectKey(operatorNamespace(), om.APIKeySecretName()))
	if err != nil {
		return nil, fmt.Errorf("error reading credentials: %s", err)
	}

	return &PodEnvVars{
		User:      cred.User,
		ProjectID: projectId,
		SSLProjectConfig: mdbv1.SSLProjectConfig{
			SSLMMSCAConfigMap: om.Spec.GetOpsManagerCA(),
		},
	}, nil
}

// deployAutomationConfig updates the Automation Config secret if necessary and waits for the pods to fall to "not ready"
// In this case the next StatefulSet update will be safe as the rolling upgrade will wait for the pods to get ready
func (r *ReconcileAppDbReplicaSet) deployAutomationConfig(opsManager *omv1.MongoDBOpsManager, rs omv1.AppDB, opsManagerUserPassword string, appDbSts appsv1.StatefulSet, log *zap.SugaredLogger) (workflow.Status, bool) {
	config, err := r.buildAppDbAutomationConfig(rs, *opsManager, opsManagerUserPassword, appDbSts, log)
	if err != nil {
		return workflow.Failed(err.Error()), false
	}

	// TODO remove in some later Operator releases
	_ = ensureLegacyConfigMapRemoved(r.client, rs)

	var wasPublished bool
	if wasPublished, err = r.publishAutomationConfig(rs, *opsManager, config, log); err != nil {
		return workflow.Failed(err.Error()), false
	}

	_, err = r.client.GetStatefulSet(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()))
	if err != nil && apiErrors.IsNotFound(err) {
		// This seems to be the new resource and StatefulSet doesn't exist yet so no further actions needed, also
		// we don't need to wait
		return workflow.OK(), false
	}

	return workflow.OK(), wasPublished
}

// deployStatefulSet updates the StatefulSet spec and returns its status (if it's ready or not)
func (r *ReconcileAppDbReplicaSet) deployStatefulSet(opsManager *omv1.MongoDBOpsManager, replicaBuilder *StatefulSetHelper) workflow.Status {
	if err := replicaBuilder.CreateOrUpdateAppDBInKubernetes(); err != nil {
		return workflow.Failed(err.Error())
	}

	return r.getStatefulSetStatus(opsManager.Namespace, opsManager.Spec.AppDB.Name())
}

// markAppDBAsBackingProject will configure the AppDB project to be read only. Errors are ignored
// if the OpsManager version does not support this feature.
func markAppDBAsBackingProject(conn om.Connection, log *zap.SugaredLogger) error {
	log.Debugf("Configuring the project as a backing database project.")
	err := conn.MarkProjectAsBackingDatabase(om.AppDBDatabaseType)
	if err != nil {
		if apiErr, ok := err.(*api.Error); ok {
			opsManagerDoesNotSupportApi := apiErr.Status != nil && *apiErr.Status == 404 && apiErr.ErrorCode == "RESOURCE_NOT_FOUND"
			if opsManagerDoesNotSupportApi {
				msg := "This version of Ops Manager does not support the markAsBackingDatabase API."
				if !conn.OMVersion().IsUnknown() {
					msg += fmt.Sprintf(" Version=%s", conn.OMVersion())
				}
				log.Debug(msg)
				return nil
			}
		}
		return err
	}
	return nil
}

func buildReplicaSetFromStatefulSetAppDb(set appsv1.StatefulSet, mdb omv1.AppDB) om.ReplicaSetWithProcesses {
	members := createProcessesAppDb(set, om.ProcessTypeMongod, mdb)
	replicaSet := om.NewReplicaSet(set.Name, mdb.GetVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members)
	return rsWithProcesses
}

func createProcessesAppDb(set appsv1.StatefulSet, mongoType om.MongoType,
	mdb omv1.AppDB) []om.Process {

	hostnames, names := util.GetDnsForStatefulSet(set, mdb.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := calculateWiredTigerCache(set, mdb.GetVersion())

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
