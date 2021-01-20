package operator

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"reflect"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/create"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/blang/semver"

	"github.com/spf13/cast"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/agents"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/apierror"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/scale"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/manifest"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/project"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/host"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// PodAnnotationAgentVersion is the Pod Annotation key which contains the current version of the Automation Config
	// the Agent on the Pod is on now
	PodAnnotationAgentVersion = "agent.mongodb.com/version"
	// This is the version of init appdb image which had a different agents reporting mechanism and didn't modify
	// annotations
	InitAppDBVersionBeforeBreakingChange = "1.0.4"
)

// ReconcileAppDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileAppDbReplicaSet struct {
	*ReconcileCommonController
	VersionManifestFilePath  string
	InternetManifestProvider manifest.Provider
	omConnectionFactory      om.ConnectionFactory
}

func newAppDBReplicaSetReconciler(commonController *ReconcileCommonController, omConnectionFactory om.ConnectionFactory, appDbVersionManifestPath string) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{
		ReconcileCommonController: commonController,
		VersionManifestFilePath:   appDbVersionManifestPath,
		InternetManifestProvider:  manifest.InternetProvider{},
		omConnectionFactory:       omConnectionFactory,
	}
}

// Reconcile deploys the "headless" agent, and wait until it reaches the goal state
func (r *ReconcileAppDbReplicaSet) Reconcile(opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string) (res reconcile.Result, e error) {
	rs := opsManager.Spec.AppDB
	log := zap.S().With("ReplicaSet (AppDB)", objectKey(opsManager.Namespace, rs.Name()))

	appDbStatusOption := status.NewOMPartOption(status.AppDb)
	omStatusOption := status.NewOMPartOption(status.OpsManager)

	result, err := r.updateStatus(opsManager, workflow.Reconciling(), log, appDbStatusOption)
	if err != nil {
		return result, err
	}

	log.Info("AppDB ReplicaSet.Reconcile")
	log.Infow("ReplicaSet.Spec", "spec", rs)
	log.Infow("ReplicaSet.Status", "status", opsManager.Status.AppDbStatus)

	podVars, err := r.tryConfigureMonitoringInOpsManager(opsManager, opsManagerUserPassword, log)
	// it's possible that Ops Manager will not be available when we attempt to configure AppDB monitoring
	// in Ops Manager. This is not a blocker to continue with the reset of the reconciliation.
	if err != nil {
		log.Errorf("Unable to configure monitoring of AppDB: %s, configuration will be attempted next reconciliation.", err)
		// errors returned from "tryConfigureMonitoringInOpsManager" could be either transient or persistent. Transient errors could be when the ops-manager pods
		// are not ready and trying to connect to the ops-manager service timesout, a persistent error is when the "ops-manager-admin-key" is corrputed, in this case
		// any API call to ops-manager will fail(including the confguration of AppDB monitoring), this error should be reflected to the user in the "OPSMANAGER" status.
		if strings.Contains(err.Error(), "401 (Unauthorized)") {
			return r.updateStatus(opsManager, workflow.Failed(fmt.Sprintf("The admin-key secret might be corrupted: %s", err)), log, omStatusOption)
		}
	}

	appDbOpts := construct.AppDbOptions(PodEnvVars(&podVars))
	appDbSts, err := construct.AppDbStatefulSet(*opsManager,
		PodEnvVars(&podVars),
	)

	if err != nil {
		return r.updateStatus(opsManager, workflow.Failed(err.Error()), log, appDbStatusOption)
	}

	if workflowStatus := r.reconcileAppDB(*opsManager, opsManagerUserPassword, appDbSts, appDbOpts, log); !workflowStatus.IsOK() {
		return r.updateStatus(opsManager, workflowStatus, log, appDbStatusOption)
	}

	if podVars.ProjectID == "" {
		// this doesn't requeue the reconciliation immediately, the calling OM controller
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

// reconcileAppDB performs the reconciliation for the AppDB: update the AutomationConfig Secret if necessary and
// update the StatefulSet. It does it in the necessary order depending on the changes to the spec
func (r *ReconcileAppDbReplicaSet) reconcileAppDB(opsManager omv1.MongoDBOpsManager, opsManagerUserPassword string, appDbSts appsv1.StatefulSet, config func(om omv1.MongoDBOpsManager) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) workflow.Status {
	rs := opsManager.Spec.AppDB
	automationConfigFirst := true
	// The only case when we push the StatefulSet first is when we are ensuring TLS for the already existing AppDB
	_, err := r.client.GetStatefulSet(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()))
	if err == nil && opsManager.Spec.AppDB.GetSecurity().TLSConfig.IsEnabled() {
		automationConfigFirst = false
	}
	return runInGivenOrder(automationConfigFirst,
		func() workflow.Status {
			return r.deployAutomationConfig(opsManager, rs, opsManagerUserPassword, appDbSts, log)
		},
		func() workflow.Status {
			return r.deployStatefulSet(opsManager, appDbSts, config, log)
		})
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
// Method returns the version of the automation config.
// No optimistic concurrency control is done - there cannot be a concurrent reconciliation for the same Ops Manager
// object and the probability that the user will edit the config map manually in the same time is extremely low
// returns the version of AutomationConfig just published
func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(rs omv1.AppDB,
	opsManager omv1.MongoDBOpsManager, automationConfig *om.AutomationConfig, log *zap.SugaredLogger) (int64, error) {

	automationConfigUpdateCallback := func(s *corev1.Secret) bool {
		return changeAutomationConfigDataIfNecessary(s, automationConfig, log)
	}
	// Perform computation of the automation config and possibly creation/update of the existing Secret
	computedSecret, err := ensureAutomationConfigSecret(r.client, kube.ObjectKey(opsManager.Namespace, rs.AutomationConfigSecretName()),
		automationConfigUpdateCallback, &opsManager)

	if err != nil {
		return -1, err
	}
	// Return the final automation config version
	var updatedAutomationConfig *om.AutomationConfig
	if updatedAutomationConfig, err = om.BuildAutomationConfigFromBytes(computedSecret.Data[util.AppDBAutomationConfigKey]); err != nil {
		return -1, err
	}
	return updatedAutomationConfig.Deployment.Version(), err
}

// changeAutomationConfigDataIfNecessary is a function that optionally changes the existing Automation Config Secret in
// case if its content is different from the desired Automation Config.
// Returns true if the data was changed.
func changeAutomationConfigDataIfNecessary(existingSecret *corev1.Secret, targetAutomationConfig *om.AutomationConfig, log *zap.SugaredLogger) bool {
	if len(existingSecret.Data) == 0 {
		log.Debugf("ConfigMap for the Automation Config doesn't exist, it will be created")
	} else {
		if existingAutomationConfig, err := om.BuildAutomationConfigFromBytes(existingSecret.Data[util.AppDBAutomationConfigKey]); err != nil {
			// in case of any problems deserializing the existing AutomationConfig - just ignore the error and update
			log.Warnf("There were problems deserializing existing automation config - it will be overwritten (%s)", err.Error())
		} else {
			// Otherwise there is an existing automation config and we need to compare it with the Operator version

			// Aligning the versions to make deep comparison correct
			targetAutomationConfig.SetVersion(existingAutomationConfig.Deployment.Version())

			log.Debug("Ensuring authentication credentials")
			if err := ensureConsistentAgentAuthenticationCredentials(targetAutomationConfig, existingAutomationConfig, log); err != nil {
				log.Warnf("error ensuring consistent authentication credentials: %s", err)
				return false
			}

			// If the deployments are the same - we shouldn't perform the update
			// We cannot compare the deployments directly as the "operator" version contains some struct members
			// So we need to turn them into maps
			if reflect.DeepEqual(existingAutomationConfig.Deployment, targetAutomationConfig.Deployment.ToCanonicalForm()) {
				log.Debugf("Automation Config hasn't changed - not updating ConfigMap")
				return false
			}

			// Otherwise we increase the version
			targetAutomationConfig.SetVersion(existingAutomationConfig.Deployment.Version() + 1)
			log.Debugf("Automation Config change detected, increasing version: %d -> %d", existingAutomationConfig.Deployment.Version(), existingAutomationConfig.Deployment.Version()+1)
		}
	}

	// By this time we have the AutomationConfig we want to push
	bytes, err := targetAutomationConfig.Serialize()
	if err != nil {
		// this definitely cannot happen and means the dev error
		log.Errorf("Failed to serialize automation config! %s", err)
		return false
	}
	existingSecret.Data = map[string][]byte{util.AppDBAutomationConfigKey: bytes}

	return true
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
			// Enables backup and restoration roles
			// https://docs.mongodb.com/manual/reference/built-in-roles/#backup-and-restoration-roles
			{
				Role:     "backup",
				Database: "admin",
			},
			{
				Role:     "restore",
				Database: "admin",
			},
			// Allows user to do db.fsyncLock required by CLOUDP-78890
			// https://docs.mongodb.com/manual/reference/built-in-roles/#hostManager
			{
				Role:     "hostManager",
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

	if err := agents.EnsureAgentKeySecretExists(r.client, conn, opsManager.Namespace, agentKeyFromSecret, conn.GroupID(), log); err != nil {
		return fmt.Errorf("error ensuring agent key secret exists: %s", err)
	}

	return nil
}

// tryConfigureMonitoringInOpsManager attempts to configure monitoring in Ops Manager. This might not be possible if Ops Manager
// has not been created yet, if that is the case, an empty PodVars will be returned.
func (r *ReconcileAppDbReplicaSet) tryConfigureMonitoringInOpsManager(opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) (env.PodEnvVars, error) {
	cred, err := project.ReadCredentials(r.client, kube.ObjectKey(operatorNamespace(), opsManager.APIKeySecretName()))
	if err != nil {
		log.Debugf("Ops Manager has not yet been created, not configuring monitoring: %s", err)
		return env.PodEnvVars{}, nil
	}
	log.Debugf("Ensuring monitoring of AppDB is configured in Ops Manager")

	existingPodVars, err := r.readExistingPodVars(*opsManager)
	if client.IgnoreNotFound(err) != nil {
		return env.PodEnvVars{}, fmt.Errorf("error reading existing podVars: %s", err)
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

	// Saving the "backup" ConfigMap which contains the project id
	if err := configmap.CreateOrUpdate(r.client, cm); err != nil {
		return existingPodVars, fmt.Errorf("error creating ConfigMap: %s", err)
	}

	return env.PodEnvVars{User: conn.User(), ProjectID: conn.GroupID(), SSLProjectConfig: env.SSLProjectConfig{
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
func (r *ReconcileAppDbReplicaSet) readExistingPodVars(om omv1.MongoDBOpsManager) (env.PodEnvVars, error) {
	cm, err := r.client.GetConfigMap(objectKey(om.Namespace, om.Spec.AppDB.ProjectIDConfigMapName()))
	if err != nil {
		return env.PodEnvVars{}, err
	}
	var projectId string
	if projectId = cm.Data[util.AppDbProjectIdKey]; projectId == "" {
		return env.PodEnvVars{}, fmt.Errorf("ConfigMap %s did not have the key %s", om.Spec.AppDB.ProjectIDConfigMapName(), util.AppDbProjectIdKey)
	}

	cred, err := project.ReadCredentials(r.client, objectKey(operatorNamespace(), om.APIKeySecretName()))
	if err != nil {
		return env.PodEnvVars{}, fmt.Errorf("error reading credentials: %s", err)
	}

	return env.PodEnvVars{
		User:      cred.User,
		ProjectID: projectId,
		SSLProjectConfig: env.SSLProjectConfig{
			SSLMMSCAConfigMap: om.Spec.GetOpsManagerCA(),
		},
	}, nil
}

// deployAutomationConfig updates the Automation Config secret if necessary and waits for the pods to fall to "not ready"
// In this case the next StatefulSet update will be safe as the rolling upgrade will wait for the pods to get ready
func (r *ReconcileAppDbReplicaSet) deployAutomationConfig(opsManager omv1.MongoDBOpsManager, rs omv1.AppDB, opsManagerUserPassword string, appDbSts appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {
	config, err := r.buildAppDbAutomationConfig(rs, opsManager, opsManagerUserPassword, appDbSts, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	// TODO remove in some later Operator releases
	_ = ensureLegacyConfigMapRemoved(r.client, rs)

	var configVersion int64
	if configVersion, err = r.publishAutomationConfig(rs, opsManager, config, log); err != nil {
		return workflow.Failed(err.Error())
	}

	return r.allAgentsReachedGoalState(opsManager, configVersion, log)
}

// deployStatefulSet updates the StatefulSet spec and returns its status (if it's ready or not)
func (r *ReconcileAppDbReplicaSet) deployStatefulSet(opsManager omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, config func(om omv1.MongoDBOpsManager) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) workflow.Status {
	if err := create.AppDBInKubernetes(r.client, opsManager, appDbSts, config, log); err != nil {
		return workflow.Failed(err.Error())
	}

	return r.getStatefulSetStatus(opsManager.Namespace, opsManager.Spec.AppDB.Name())
}

// allAgentsReachedGoalState checks if all the AppDB Agents have reached the goal state.
func (r *ReconcileAppDbReplicaSet) allAgentsReachedGoalState(manager omv1.MongoDBOpsManager, targetConfigVersion int64, log *zap.SugaredLogger) workflow.Status {
	appdbSize := manager.Spec.AppDB.Members
	upgradeFromOldInitImage := false
	// We need to read the current StatefulSet to find the real number of pods - we cannot rely on OpsManager resource
	set, err := r.client.GetStatefulSet(manager.AppDBStatefulSetObjectKey())
	if err == nil {
		appdbSize = int(set.Status.Replicas)
		if len(set.Spec.Template.Spec.InitContainers) > 0 {
			upgradeFromOldInitImage = isOldInitAppDBImageForAgentsCheck(set.Spec.Template.Spec.InitContainers[0].Image, log)
			if upgradeFromOldInitImage {
				return workflow.OK()
			}
		}
	} else if !apiErrors.IsNotFound(err) {
		return workflow.Failed(err.Error())
	}
	var podsNotFound []string
	for _, podName := range manager.AppDBMemberNames(appdbSize) {
		pod := &corev1.Pod{}
		if err := r.client.Get(context.TODO(), kube.ObjectKey(manager.Namespace, podName), pod); err != nil {
			if apiErrors.IsNotFound(err) {
				// If the Pod is not found yet - this could mean one of:
				// 1. The StatefulSet just doesn't exist
				// 2. The StatefulSet exists but the Pod doesn't exist (terminated/failed and will be rescheduled?)
				podsNotFound = append(podsNotFound, podName)
				continue
			}
			return workflow.Failed(err.Error())
		}
		if agentReachedGoalState := agentReachedGoalState(pod, targetConfigVersion, log); !agentReachedGoalState {
			return workflow.Pending("Application Database Agents haven't reached Running state yet")
		}
	}

	if len(podsNotFound) == manager.Spec.AppDB.Members {
		// No Pod existing means that the StatefulSet hasn't been created yet - will be done during the next step
		return workflow.OK()
	}
	if len(podsNotFound) > 0 {
		log.Infof("The following Pods don't exist: %v. Assuming they will be rescheduled by Kubernetes soon", podsNotFound)
		return workflow.Pending("Application Database Agents haven't reached Running state yet")
	}
	log.Infof("All %d Application Database Agents have reached Running state", manager.Spec.AppDB.Members)
	return workflow.OK()
}

// isOldInitAppDBImageForAgentsCheck returns true if the currently deployed Init Containers don't support annotations yet.
// In this case we shouldn't perform the logic to wait for annotations at all
// (note, that this is a very temporary state that may happen only once the Operator upgrade to 1.8.1 happened and the
// existing resources are reconciled - the StatefulSet reconciliation will upgrade the appdb init images soon)
func isOldInitAppDBImageForAgentsCheck(initAppDBImage string, log *zap.SugaredLogger) bool {
	initAppDBImageAndVersion := strings.Split(initAppDBImage, ":")
	// This is highly unprobable as default Operator configuration always specifies the version but let's be save
	if len(initAppDBImageAndVersion) == 1 {
		log.Warnf("The Init AppDB image url doesn't contain the tag! %s", initAppDBImage)
		return false
	}
	version, err := semver.Parse(initAppDBImageAndVersion[1])
	if err != nil {
		log.Warnf("Failed to parse the existing version of Init AppDB image: %s", err)
		return false
	}
	breakingVersion := semver.MustParse(InitAppDBVersionBeforeBreakingChange)
	return version.LTE(breakingVersion)
}

// agentReachedGoalState checks if a single AppDB Agent has reached the goal state. To do this it reads the Pod annotation
// to find out the current version the Agent is on.
func agentReachedGoalState(pod *corev1.Pod, targetConfigVersion int64, log *zap.SugaredLogger) bool {
	currentAgentVersion, ok := pod.Annotations[PodAnnotationAgentVersion]
	if !ok {
		log.Debugf("The Pod '%s' doesn't have annotation '%s' yet", pod.Name, PodAnnotationAgentVersion)
		return false
	}
	if cast.ToInt64(currentAgentVersion) != targetConfigVersion {
		log.Debugf("The Agent in the Pod '%s' hasn't reached the goal state yet (goal: %d, agent: %s)", pod.Name, targetConfigVersion, currentAgentVersion)
		return false
	}
	return true
}

// markAppDBAsBackingProject will configure the AppDB project to be read only. Errors are ignored
// if the OpsManager version does not support this feature.
func markAppDBAsBackingProject(conn om.Connection, log *zap.SugaredLogger) error {
	log.Debugf("Configuring the project as a backing database project.")
	err := conn.MarkProjectAsBackingDatabase(om.AppDBDatabaseType)
	if err != nil {
		if apiErr, ok := err.(*apierror.Error); ok {
			opsManagerDoesNotSupportApi := apiErr.Status != nil && *apiErr.Status == 404 && apiErr.ErrorCode == "RESOURCE_NOT_FOUND"
			if opsManagerDoesNotSupportApi {
				msg := "This version of Ops Manager does not support the markAsBackingDatabase API."
				if !conn.OpsManagerVersion().IsUnknown() {
					msg += fmt.Sprintf(" Version=%s", conn.OpsManagerVersion())
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
