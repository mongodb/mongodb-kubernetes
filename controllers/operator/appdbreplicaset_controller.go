package operator

import (
	"fmt"
	"path"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/wiredtiger"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/agent"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scram"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/generate"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"
	"github.com/stretchr/objx"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"go.uber.org/zap"
)

type agentType string

const (
	// PodAnnotationAgentVersion is the Pod Annotation key which contains the current version of the Automation Config
	// the Agent on the Pod is on now
	PodAnnotationAgentVersion = "agent.mongodb.com/version"
	// This is the version of init appdb image which had a different agents reporting mechanism and didn't modify
	// annotations
	InitAppDBVersionBeforeBreakingChange = "1.0.4"
	appdbCAFilePath                      = "/var/lib/mongodb-automation/secrets/ca/ca-pem"
	lastSuccessfulConfiguration          = "mongodb.com/v1.lastSuccessfulAppDBConfiguration"

	monitoring agentType = "MONITORING"
	automation agentType = "AUTOMATION"
)

// ReconcileAppDbReplicaSet reconciles a MongoDB with a type of ReplicaSet
type ReconcileAppDbReplicaSet struct {
	*ReconcileCommonController
	omConnectionFactory    om.ConnectionFactory
	versionMappingProvider func(string) ([]byte, error)
}

func newAppDBReplicaSetReconciler(commonController *ReconcileCommonController, omConnectionFactory om.ConnectionFactory, versionMappingProvider func(string) ([]byte, error)) *ReconcileAppDbReplicaSet {
	return &ReconcileAppDbReplicaSet{
		ReconcileCommonController: commonController,
		omConnectionFactory:       omConnectionFactory,
		versionMappingProvider:    versionMappingProvider,
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

	monitoringAgentVersion, err := getMonitoringAgentVersion(*opsManager, r.versionMappingProvider)
	if err != nil {
		return r.updateStatus(opsManager, workflow.Failed("Error reading monitoring agent version: %s", err), log, appDbStatusOption)
	}

	appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, monitoringAgentVersion)
	if err != nil {

		return r.updateStatus(opsManager, workflow.Failed("can't construct AppDB Statefulset: %s", err), log, omStatusOption)
	}

	if workflowStatus := r.reconcileAppDB(*opsManager, appDbSts, log); !workflowStatus.IsOK() {
		return r.updateStatus(opsManager, workflowStatus, log, appDbStatusOption)
	}

	if err := annotations.UpdateLastAppliedMongoDBVersion(opsManager, r.client); err != nil {
		return r.updateStatus(opsManager, workflow.Failed("Could not save current state as an annotation: %s", err), log, omStatusOption)
	}
	if err := statefulset.ResetUpdateStrategy(opsManager, r.client); err != nil {

		return r.updateStatus(opsManager, workflow.Failed("can't reset AppDB StatefulSet UpdateStrategyType: %s", err), log, omStatusOption)
	}

	if podVars.ProjectID == "" {
		// this doesn't requeue the reconciliation immediately, the calling OM controller
		// requeues after Ops Manager has been fully configured.
		log.Infof("Requeuing reconciliation to configure Monitoring in Ops Manager.")
		return r.updateStatus(opsManager, workflow.OK().Requeue(), log, appDbStatusOption, status.MembersOption(opsManager))
	}

	if scale.IsStillScaling(opsManager) {
		return r.updateStatus(opsManager, workflow.Pending("Continuing scaling operation on AppDB desiredMembers=%d, currentMembers=%d",
			opsManager.DesiredReplicas(), scale.ReplicasThisReconciliation(opsManager)), log, appDbStatusOption, status.MembersOption(opsManager))
	}

	log.Infof("Finished reconciliation for AppDB ReplicaSet!")

	return r.updateStatus(opsManager, workflow.OK(), log, appDbStatusOption, status.MembersOption(opsManager))
}

// reconcileAppDB performs the reconciliation for the AppDB: update the AutomationConfig Secret if necessary and
// update the StatefulSet. It does it in the necessary order depending on the changes to the spec
func (r *ReconcileAppDbReplicaSet) reconcileAppDB(opsManager omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {
	automationConfigFirst := true

	currentAc, err := automationconfig.ReadFromSecret(r.client, types.NamespacedName{
		Namespace: opsManager.GetNamespace(),
		Name:      opsManager.Spec.AppDB.AutomationConfigSecretName(),
	})

	if err != nil {
		return workflow.Failed("can't read existing automation config from secret")
	}

	// The only case when we push the StatefulSet first is when we are ensuring TLS for the already existing AppDB
	_, err = r.client.GetStatefulSet(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()))
	if err == nil && opsManager.Spec.AppDB.GetSecurity().TLSConfig.IsEnabled() {
		automationConfigFirst = false
	}

	// Set it to true if the currentAC has the old keyfile path
	// This is needed for appdb upgrade from 1 to 3 contaienrs
	// as the AC contains the new path of the keyfile and the agents needs it
	if currentAc.Auth.KeyFile == util.AutomationAgentKeyFilePathInContainer {
		automationConfigFirst = true
	}
	if opsManager.IsChangingVersion() {
		log.Info("Version change in progress, the StatefulSet must be updated first")
		automationConfigFirst = false
	}
	return workflow.RunInGivenOrder(automationConfigFirst,
		func() workflow.Status {
			log.Infof("Deploying Automation Config\n")
			return r.deployAutomationConfig(opsManager, appDbSts, log)
		},
		func() workflow.Status {

			// in the case of an upgrade from the 1 to 3 container architecture, when the stateful set is updated before the agent automation config
			// the monitoring agent automation config needs to exist for the volumes to mount correctly.
			if err := r.deployMonitoringAgentAutomationConfig(opsManager, appDbSts, log); err != nil {
				return workflow.Failed(err.Error())
			}

			log.Infof("Deploying Statefulset\n")
			return r.deployStatefulSet(opsManager, appDbSts, log)
		})
}

func getDomain(service, namespace, clusterName string) string {
	if clusterName == "" {
		clusterName = "cluster.local"
	}
	return fmt.Sprintf("%s.%s.svc.%s", service, namespace, clusterName)
}

// publishAutomationConfig publishes the automation config to the Secret if necessary. Note that it's done only
// if the automation config has changed - the version is incremented in this case.
// Method returns the version of the automation config.
// No optimistic concurrency control is done - there cannot be a concurrent reconciliation for the same Ops Manager
// object and the probability that the user will edit the config map manually in the same time is extremely low
// returns the version of AutomationConfig just published
func (r *ReconcileAppDbReplicaSet) publishAutomationConfig(opsManager omv1.MongoDBOpsManager, automationConfig automationconfig.AutomationConfig, secretName string) (int, error) {
	ac, err := automationconfig.EnsureSecret(r.client, kube.ObjectKey(opsManager.Namespace, secretName), kube.BaseOwnerReference(&opsManager), automationConfig)
	if err != nil {
		return -1, err
	}
	return ac.Version, err
}

func (r ReconcileAppDbReplicaSet) buildAppDbAutomationConfig(opsManager omv1.MongoDBOpsManager, set appsv1.StatefulSet, acType agentType, log *zap.SugaredLogger) (automationconfig.AutomationConfig, error) {
	rs := opsManager.Spec.AppDB
	domain := getDomain(rs.ServiceName(), opsManager.Namespace, opsManager.GetClusterName())
	auth := automationconfig.Auth{}
	appDBConfigurable := omv1.AppDBConfigurable{AppDBSpec: rs, OpsManager: opsManager}
	if err := scram.Enable(&auth, r.client, appDBConfigurable); err != nil {
		return automationconfig.AutomationConfig{}, err
	}
	// the existing automation config is required as we compare it against what we build to determine
	// if we need to increment the version.
	secretName := rs.AutomationConfigSecretName()
	if acType == monitoring {
		secretName = rs.MonitoringAutomationConfigSecretName()
	}
	existingAutomationConfig, err := automationconfig.ReadFromSecret(r.client, types.NamespacedName{Name: secretName, Namespace: opsManager.Namespace})
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}
	fcVersion := ""
	if rs.FeatureCompatibilityVersion != nil {
		fcVersion = *rs.FeatureCompatibilityVersion
	}

	return automationconfig.NewBuilder().
		SetTopology(automationconfig.ReplicaSetTopology).
		SetMembers(scale.ReplicasThisReconciliation(&opsManager)).
		SetName(rs.Name()).
		SetDomain(domain).
		SetAuth(auth).
		SetFCV(fcVersion).
		AddVersions(existingAutomationConfig.Versions).
		SetMongoDBVersion(rs.GetMongoDBVersion()).
		SetOptions(automationconfig.Options{DownloadBase: util.AgentDownloadsDir}).
		SetPreviousAutomationConfig(existingAutomationConfig).
		SetTLSConfig(
			automationconfig.TLS{
				CAFilePath:            appdbCAFilePath,
				ClientCertificateMode: automationconfig.ClientCertificateModeOptional,
			}).
		AddModifications(func(automationConfig *automationconfig.AutomationConfig) {
			if acType == monitoring {
				addMonitoring(automationConfig, log, rs.GetTLSConfig().IsEnabled())
				automationConfig.ReplicaSets = []automationconfig.ReplicaSet{}
				automationConfig.Processes = []automationconfig.Process{}
			}
			setBaseUrlForAgents(automationConfig, opsManager.CentralURL())
		}).
		AddProcessModification(func(i int, p *automationconfig.Process) {
			p.AuthSchemaVersion = om.CalculateAuthSchemaVersion(rs.GetMongoDBVersion())
			p.Args26 = objx.New(rs.AdditionalMongodConfig.ToMap())
			p.SetPort(int(rs.AdditionalMongodConfig.GetPortOrDefault()))
			p.SetReplicaSetName(rs.Name())
			p.SetWiredTigerCache(wiredtiger.CalculateCache(set, util.AppDbContainerName, rs.GetMongoDBVersion()))
			p.SetSystemLog(automationconfig.SystemLog{
				Destination: "file",
				Path:        path.Join(util.PvcMountPathLogs, "mongodb.log"),
			})
			p.SetStoragePath(automationconfig.DefaultMongoDBDataDir)
			if rs.GetTlsCertificatesSecretName() != "" {

				certFile := fmt.Sprintf("%s/certs/%s-pem", util.SecretVolumeMountPath, p.Name)

				p.Args26.Set("net.tls.mode", string(tls.Require))

				p.Args26.Set("net.tls.certificateKeyFile", certFile)

			}
		}).Build()

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
					"sslTrustedServerCertificates": appdbCAFilePath,
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

func (r OpsManagerReconciler) generatePasswordAndCreateSecret(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	// create the password
	password, err := generate.RandomFixedLengthStringOfSize(12)
	if err != nil {
		return "", err
	}

	passwordData := map[string]string{
		util.OpsManagerPasswordKey: password,
	}

	secretObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())

	log.Infof("Creating mongodb-ops-manager password in secret/%s in namespace %s", secretObjectKey.Name, secretObjectKey.Namespace)

	appDbPasswordSecret := secret.Builder().
		SetName(secretObjectKey.Name).
		SetNamespace(secretObjectKey.Namespace).
		SetStringData(passwordData).
		SetOwnerReferences(kube.BaseOwnerReference(&opsManager)).
		Build()

	if err := r.client.CreateSecret(appDbPasswordSecret); err != nil {
		return "", err
	}

	return password, nil
}

// ensureAppDbPassword will return the password that was specified by the user, or the auto generated password stored in
// the secret (generate it and store in secret otherwise)
func (r OpsManagerReconciler) ensureAppDbPassword(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	passwordRef := opsManager.Spec.AppDB.PasswordSecretKeyRef
	if passwordRef != nil && passwordRef.Name != "" { // there is a secret specified for the Ops Manager user
		if passwordRef.Key == "" {
			passwordRef.Key = "password"
		}
		password, err := secret.ReadKey(r.client, passwordRef.Key, kube.ObjectKey(opsManager.Namespace, passwordRef.Name))

		if err != nil {
			if apiErrors.IsNotFound(err) {
				log.Debugf("Generated AppDB password and storing in secret/%s", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
				return r.generatePasswordAndCreateSecret(opsManager, log)
			}
			return "", err
		}

		log.Debugf("Reading password from secret/%s", passwordRef.Name)

		// watch for any changes on the user provided password
		r.AddWatchedResourceIfNotAdded(
			passwordRef.Name,
			opsManager.Namespace,
			watch.Secret,
			kube.ObjectKeyFromApiObject(&opsManager),
		)

		// delete the auto generated password, we don't need it anymore. We can just generate a new one if
		// the user password is deleted
		log.Debugf("Deleting Operator managed password secret/%s from namespace", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName(), opsManager.Namespace)
		if err := r.client.DeleteSecret(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())); err != nil && !apiErrors.IsNotFound(err) {
			return "", err
		}
		return password, nil
	}

	// otherwise we'll ensure the auto generated password exists
	secretObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
	appDbPasswordSecretStringData, err := secret.ReadStringData(r.client, secretObjectKey)

	if apiErrors.IsNotFound(err) {
		// create the password
		if password, err := r.generatePasswordAndCreateSecret(opsManager, log); err != nil {
			return "", err
		} else {
			log.Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
			return password, nil
		}
	} else if err != nil {
		// any other error
		return "", err
	}
	log.
		Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName())
	return appDbPasswordSecretStringData[util.OpsManagerPasswordKey], nil
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
func (r *ReconcileAppDbReplicaSet) deployAutomationConfig(opsManager omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {

	rs := opsManager.Spec.AppDB

	config, err := r.buildAppDbAutomationConfig(opsManager, appDbSts, automation, log)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	var configVersion int
	if configVersion, err = r.publishAutomationConfig(opsManager, config, rs.AutomationConfigSecretName()); err != nil {
		return workflow.Failed(err.Error())
	}

	if _, err = r.buildAppDbAutomationConfig(opsManager, appDbSts, monitoring, log); err != nil {
		return workflow.Failed(err.Error())
	}

	if err := r.deployMonitoringAgentAutomationConfig(opsManager, appDbSts, log); err != nil {
		return workflow.Failed(err.Error())
	}

	return r.allAgentsReachedGoalState(opsManager, configVersion, log)
}

// deployMonitoringAgentAutomationConfig deploys the monitoring agent's automation config.
func (r *ReconcileAppDbReplicaSet) deployMonitoringAgentAutomationConfig(opsManager omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, log *zap.SugaredLogger) error {
	config, err := r.buildAppDbAutomationConfig(opsManager, appDbSts, monitoring, log)
	if err != nil {
		return err
	}
	if _, err = r.publishAutomationConfig(opsManager, config, opsManager.Spec.AppDB.MonitoringAutomationConfigSecretName()); err != nil {
		return err
	}
	return nil
}

// deployStatefulSet updates the StatefulSet spec and returns its status (if it's ready or not)
func (r *ReconcileAppDbReplicaSet) deployStatefulSet(opsManager omv1.MongoDBOpsManager, appDbSts appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {

	if err := create.AppDBInKubernetes(r.client, opsManager, appDbSts, log); err != nil {
		return workflow.Failed(err.Error())

	}

	return r.getStatefulSetStatus(opsManager.Namespace, opsManager.Spec.AppDB.Name())
}

// allAgentsReachedGoalState checks if all the AppDB Agents have reached the goal state.
func (r *ReconcileAppDbReplicaSet) allAgentsReachedGoalState(manager omv1.MongoDBOpsManager, targetConfigVersion int, log *zap.SugaredLogger) workflow.Status {
	appdbSize := manager.Spec.AppDB.Members
	// We need to read the current StatefulSet to find the real number of pods - we cannot rely on OpsManager resource
	set, err := r.client.GetStatefulSet(manager.AppDBStatefulSetObjectKey())
	if err == nil {
		appdbSize = int(set.Status.Replicas)
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
