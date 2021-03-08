package operator

import (
	"fmt"
	"path"
	"strings"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/wiredtiger"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/agent"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scram"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/stretchr/objx"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/blang/semver"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/scale"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/manifest"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

const (
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
	log := zap.S().With("ReplicaSet (AppDB)", kube.ObjectKey(opsManager.Namespace, rs.Name()))

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
	appDbSts := construct.AppDbStatefulSet(*opsManager,
		PodEnvVars(&podVars),
	)

	if workflowStatus := r.reconcileAppDB(*opsManager, appDbSts, appDbOpts, log); !workflowStatus.IsOK() {
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
func (r *ReconcileAppDbReplicaSet) reconcileAppDB(opsManager omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, config construct.AppDBConfiguration, log *zap.SugaredLogger) workflow.Status {
	rs := opsManager.Spec.AppDB
	automationConfigFirst := true
	// The only case when we push the StatefulSet first is when we are ensuring TLS for the already existing AppDB
	_, err := r.client.GetStatefulSet(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()))
	if err == nil && opsManager.Spec.AppDB.GetSecurity().TLSConfig.IsEnabled() {
		automationConfigFirst = false
	}
	return workflow.RunInGivenOrder(automationConfigFirst,
		func() workflow.Status {
			return r.deployAutomationConfig(opsManager, rs, appDbSts, log)
		},
		func() workflow.Status {
			return r.deployStatefulSet(opsManager, appDbSts, config, log)
		})
}

// publishAutomationConfig publishes the automation config to the Secret if necessary. Note that it's done only
// if the automation config has changed - the version is incremented in this case.
// Method returns the version of the automation config.
// No optimistic concurrency control is done - there cannot be a concurrent reconciliation for the same Ops Manager
// object and the probability that the user will edit the config map manually in the same time is extremely low
// returns the version of AutomationConfig just published
func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(rs omv1.AppDBSpec,
	opsManager omv1.MongoDBOpsManager, automationConfig automationconfig.AutomationConfig) (int, error) {
	ac, err := automationconfig.EnsureSecret(r.client, kube.ObjectKey(opsManager.Namespace, rs.AutomationConfigSecretName()), kube.BaseOwnerReference(&opsManager), automationConfig)
	if err != nil {
		return -1, err
	}
	return ac.Version, err
}

func getDomain(service, namespace, clusterName string) string {
	if clusterName == "" {
		clusterName = "cluster.local"
	}
	return fmt.Sprintf("%s.%s.svc.%s", service, namespace, clusterName)
}

func addMonitoring(ac *automationconfig.AutomationConfig, log *zap.SugaredLogger, tls bool) {
	if len(ac.Processes) == 0 {
		return
	}

	monitoringVersions := ac.MonitoringVersions
	for _, p := range ac.Processes {
		found := false
		for _, m := range monitoringVersions {
			if m.Hostname == p.HostName {
				found = true
				break
			}
		}

		if !found {
			monitoringVersion := automationconfig.MonitoringVersion{
				Hostname: p.HostName,
				Name:     om.MonitoringAgentDefaultVersion,
			}
			if tls {
				additionalParams := map[string]string{
					"useSslForAllConnections":      "true",
					"sslTrustedServerCertificates": util.CAFilePathInContainer,
				}

				pemKeyFile := p.Args26.Get("net.ssl.PEMKeyFile")
				if pemKeyFile != nil {
					additionalParams["sslClientCertificate"] = pemKeyFile.String()
				}
				monitoringVersion.AdditionalParams = additionalParams
			}
			log.Debugw("Added monitoring agent configuration", "host", p.HostName, "tls", tls)
			monitoringVersions = append(monitoringVersions, monitoringVersion)
		}
	}

	ac.MonitoringVersions = monitoringVersions
}

// setBaseUrlForAgents will update the baseUrl for all backup and monitoring versions to the provided url.
func setBaseUrlForAgents(ac *automationconfig.AutomationConfig, url string) {
	for i := range ac.MonitoringVersions {
		ac.MonitoringVersions[i].BaseUrl = url
	}

	for i := range ac.BackupVersions {
		ac.BackupVersions[i].BaseUrl = url
	}
}

func (r ReconcileAppDbReplicaSet) buildAppDbAutomationConfig(rs omv1.AppDBSpec, opsManager omv1.MongoDBOpsManager, set appsv1.StatefulSet, log *zap.SugaredLogger) (automationconfig.AutomationConfig, error) {

	domain := getDomain(rs.ServiceName(), opsManager.Namespace, opsManager.GetClusterName())

	auth := automationconfig.Auth{}
	if err := scram.Enable(&auth, r.client, rs); err != nil {
		return automationconfig.AutomationConfig{}, err
	}

	// the existing automation config is required as we compare it against what we build to determine
	// if we need to increment the version.
	existingAutomationConfig, err := automationconfig.ReadFromSecret(r.client, types.NamespacedName{Name: rs.AutomationConfigSecretName(), Namespace: opsManager.Namespace})
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}

	ac, err := automationconfig.NewBuilder().
		SetTopology(automationconfig.ReplicaSetTopology).
		SetMembers(scale.ReplicasThisReconciliation(&opsManager)).
		SetName(rs.Name()).
		SetDomain(domain).
		SetAuth(auth).
		SetFCV(rs.FeatureCompatibilityVersion).
		SetMongoDBVersion(rs.GetVersion()).
		SetOptions(automationconfig.Options{DownloadBase: util.AgentDownloadsDir}).
		SetPreviousAutomationConfig(existingAutomationConfig).
		// TODO: this should be TLS config with the new agent version
		SetSSLConfig(
			automationconfig.TLS{
				CAFilePath:            util.CAFilePathInContainer,
				ClientCertificateMode: automationconfig.ClientCertificateModeOptional,
			}).
		AddProcessModification(func(i int, p *automationconfig.Process) {
			p.AuthSchemaVersion = om.CalculateAuthSchemaVersion(rs.GetVersion())
			p.Args26 = objx.New(rs.AdditionalMongodConfig.ToMap())
			p.SetPort(int(rs.AdditionalMongodConfig.GetPortOrDefault()))
			p.SetReplicaSetName(rs.Name())
			p.SetWiredTigerCache(wiredtiger.CalculateCache(set, util.AppDbContainerName, rs.GetVersion()))
			p.SetSystemLog(automationconfig.SystemLog{
				Destination: "file",
				Path:        path.Join(util.PvcMountPathLogs, "mongodb.log"),
			})
			p.SetStoragePath(automationconfig.DefaultMongoDBDataDir)
			if rs.GetTlsCertificatesSecretName() != "" {

				certFile := fmt.Sprintf("%s/certs/%s-pem", util.SecretVolumeMountPath, p.Name)

				// TODO: this will move to net.tls.x with the new agent
				p.Args26.Set("net.ssl.mode", string(mdbv1.RequireSSLMode))
				if p.Args26.Has("net.ssl.certificateKeyFile") {
					p.Args26.Set("net.ssl.certificateKeyFile", certFile)
				} else {
					p.Args26.Set("net.ssl.PEMKeyFile", certFile)
				}
			}
		}).Build()

	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}

	// TODO: configure these fields as part of the building process
	// for now, this is a replication of the operations performed on om.AutomationConfig
	// acting on the type automationconfig.AutomationConfig
	addMonitoring(&ac, log, rs.GetTLSConfig().IsEnabled())
	setBaseUrlForAgents(&ac, opsManager.CentralURL())
	if err := r.configureMongoDBVersions(&ac, rs, log); err != nil {
		return automationconfig.AutomationConfig{}, err
	}

	return ac, nil
}

func (r ReconcileAppDbReplicaSet) configureMongoDBVersions(config *automationconfig.AutomationConfig, rs omv1.AppDBSpec, log *zap.SugaredLogger) error {
	if rs.GetVersion() == util.BundledAppDbMongoDBVersion {
		versionManifest, err := manifest.FileProvider{FilePath: r.VersionManifestFilePath}.GetVersion()
		if err != nil {
			return err
		}
		config.Versions = versionManifest.Versions
		log.Infof("Using bundled MongoDB version: %s", util.BundledAppDbMongoDBVersion)
		return nil
	} else {
		return r.addLatestMongoDBVersions(config, log)
	}
}

func (r *ReconcileAppDbReplicaSet) addLatestMongoDBVersions(config *automationconfig.AutomationConfig, log *zap.SugaredLogger) error {
	start := time.Now()
	versionManifest, err := r.InternetManifestProvider.GetVersion()
	if err != nil {
		return err
	}
	config.Versions = versionManifest.Versions
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
	agentKeyFromSecret, err := secret.ReadKey(r.client, util.OmAgentApiKey, kube.ObjectKey(opsManager.Namespace, agents.ApiKeySecretName(conn.GroupID())))
	err = client.IgnoreNotFound(err)
	if err != nil {
		return fmt.Errorf("error reading secret %s: %s", kube.ObjectKey(opsManager.Namespace, agents.ApiKeySecretName(conn.GroupID())), err)
	}

	if err := agents.EnsureAgentKeySecretExists(r.client, conn, opsManager.Namespace, agentKeyFromSecret, conn.GroupID(), log); err != nil {
		return fmt.Errorf("error ensuring agent key secret exists: %s", err)
	}

	return nil
}

// tryConfigureMonitoringInOpsManager attempts to configure monitoring in Ops Manager. This might not be possible if Ops Manager
// has not been created yet, if that is the case, an empty PodVars will be returned.
func (r *ReconcileAppDbReplicaSet) tryConfigureMonitoringInOpsManager(opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) (env.PodEnvVars, error) {
	APIKeySecretName, err := opsManager.APIKeySecretName(r.client)
	if err != nil {
		return env.PodEnvVars{}, fmt.Errorf("error getting opsManager secret name: %s", err)
	}

	cred, err := project.ReadCredentials(r.client, kube.ObjectKey(operatorNamespace(), APIKeySecretName))
	if err != nil {
		log.Debugf("Ops Manager has not yet been created, not configuring monitoring: %s", err)
		return env.PodEnvVars{}, nil
	}
	log.Debugf("Ensuring monitoring of AppDB is configured in Ops Manager")

	existingPodVars, err := r.readExistingPodVars(*opsManager)
	if client.IgnoreNotFound(err) != nil {
		return env.PodEnvVars{}, fmt.Errorf("error reading existing podVars: %s", err)
	}

	projectConfig, err := opsManager.GetAppDBProjectConfig(r.client)
	if err != nil {
		return existingPodVars, fmt.Errorf("error getting existing project config: %s", err)
	}

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
	cm, err := r.client.GetConfigMap(kube.ObjectKey(om.Namespace, om.Spec.AppDB.ProjectIDConfigMapName()))
	if err != nil {
		return env.PodEnvVars{}, err
	}
	var projectId string
	if projectId = cm.Data[util.AppDbProjectIdKey]; projectId == "" {
		return env.PodEnvVars{}, fmt.Errorf("ConfigMap %s did not have the key %s", om.Spec.AppDB.ProjectIDConfigMapName(), util.AppDbProjectIdKey)
	}

	APISecretName, err := om.APIKeySecretName(r.client)
	if err != nil {
		return env.PodEnvVars{}, fmt.Errorf("error getting ops-manager API secret name: %s", err)
	}

	cred, err := project.ReadCredentials(r.client, kube.ObjectKey(operatorNamespace(), APISecretName))
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
func (r *ReconcileAppDbReplicaSet) deployAutomationConfig(opsManager omv1.MongoDBOpsManager, rs omv1.AppDBSpec, appDbSts appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {
	config, err := r.buildAppDbAutomationConfig(rs, opsManager, appDbSts, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	var configVersion int
	if configVersion, err = r.publishAutomationConfig(rs, opsManager, config); err != nil {
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
func (r *ReconcileAppDbReplicaSet) allAgentsReachedGoalState(manager omv1.MongoDBOpsManager, targetConfigVersion int, log *zap.SugaredLogger) workflow.Status {
	appdbSize := manager.Spec.AppDB.Members
	upgradeFromOldInitImage := false
	// We need to read the current StatefulSet to find the real number of pods - we cannot rely on OpsManager resource
	set, err := r.client.GetStatefulSet(manager.AppDBStatefulSetObjectKey())
	if err == nil {
		appdbSize = int(set.Status.Replicas)
		if len(set.Spec.Template.Spec.InitContainers) > 0 {
			appDbInitContainer := container.GetByName(construct.InitAppDbContainerName, set.Spec.Template.Spec.InitContainers)
			upgradeFromOldInitImage = isOldInitAppDBImageForAgentsCheck(appDbInitContainer.Image, log)
			if upgradeFromOldInitImage {
				return workflow.OK()
			}
		}
	} else if !apiErrors.IsNotFound(err) {
		return workflow.Failed(err.Error())
	}

	goalState, err := agent.AllReachedGoalState(set, r.client, appdbSize, targetConfigVersion, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}
	if goalState {
		return workflow.OK()
	}
	return workflow.Pending("Application Database Agents haven't reached Running state yet")
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
