package operator

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"reflect"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scram"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connectionstring"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/generate"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/identifiable"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault/vaultwatcher"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/api"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/blang/semver"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	oldestSupportedOpsManagerVersion       = "5.0.0"
	opsManagerToVersionMappingJsonFilePath = "/usr/local/om_version_mapping.json" // TODO: make that an envar to support local development
	programmaticKeyVersion                 = "5.0.0"
)

type S3ConfigGetter interface {
	GetAuthenticationModes() []string
	GetResourceName() string
	BuildConnectionString(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string) string
}

type OpsManagerReconciler struct {
	*ReconcileCommonController
	omInitializer          api.Initializer
	omAdminProvider        api.AdminProvider
	omConnectionFactory    om.ConnectionFactory
	versionMappingProvider func(string) ([]byte, error)
	oldestSupportedVersion semver.Version
	programmaticKeyVersion semver.Version
}

var _ reconcile.Reconciler = &OpsManagerReconciler{}

func newOpsManagerReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, initializer api.Initializer, adminProvider api.AdminProvider, versionMappingProvider func(string) ([]byte, error)) *OpsManagerReconciler {
	return &OpsManagerReconciler{
		ReconcileCommonController: newReconcileCommonController(mgr),
		omConnectionFactory:       omFunc,
		omInitializer:             initializer,
		omAdminProvider:           adminProvider,
		versionMappingProvider:    versionMappingProvider,
		oldestSupportedVersion:    semver.MustParse(oldestSupportedOpsManagerVersion),
		programmaticKeyVersion:    semver.MustParse(programmaticKeyVersion),
	}
}

// +kubebuilder:rbac:groups=mongodb.com,resources={opsmanagers,opsmanagers/status,opsmanagers/finalizers},verbs=*,namespace=placeholder

// Reconcile performs the reconciliation logic for AppDB, Ops Manager and Backup
// AppDB is reconciled first (independent of Ops Manager as the agent is run in headless mode) and
// Ops Manager statefulset is created then.
// Backup daemon statefulset is created/updated and configured optionally if backup is enabled.
// Note, that the pointer to ops manager resource is used in 'Reconcile' method as resource status is mutated
// many times during reconciliation, and It's important to keep updates to avoid status override
func (r *OpsManagerReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("OpsManager", request.NamespacedName)

	opsManager := &omv1.MongoDBOpsManager{}

	opsManagerExtraStatusParams := mdbstatus.NewOMPartOption(mdbstatus.OpsManager)

	if reconcileResult, err := r.readOpsManagerResource(request, opsManager, log); err != nil {
		if secrets.SecretNotExist(err) {
			return reconcile.Result{}, nil
		}
		return reconcileResult, err
	}

	log.Info("-> OpsManager.Reconcile")
	log.Infow("OpsManager.Spec", "spec", opsManager.Spec)
	log.Infow("OpsManager.Status", "status", opsManager.Status)

	// TODO: make SetupCommonWatchers support opsmanager watcher setup
	// The order matters here, since appDB and opsManager share the same reconcile ObjectKey being opsmanager crd
	// That means we need to remove first, which SetupCommonWatchers does, then register additional watches
	appDBReplicaSet := opsManager.Spec.AppDB
	r.SetupCommonWatchers(&appDBReplicaSet, nil, nil, appDBReplicaSet.Name())
	// We need to remove the watches on the top of the reconcile since we might add resources with the same key below.
	if opsManager.IsTLSEnabled() {
		r.RegisterWatchedTLSResources(opsManager.ObjectKey(), opsManager.GetSecurity().TLS.CA, []string{opsManager.TLSCertificateSecretName()})
	}

	// We perform this check here and not inside the validation because we don't want to put OM in failed state
	// just log the error and put in the "Unsupported" state
	semverVersion, err := versionutil.StringToSemverVersion(opsManager.Spec.Version)
	if err != nil {
		return r.updateStatus(opsManager, workflow.Invalid("%s is not a valid version", opsManager.Spec.Version), log, opsManagerExtraStatusParams)
	}
	if semverVersion.LT(r.oldestSupportedVersion) {
		return r.updateStatus(opsManager, workflow.Unsupported("Ops Manager Version %s is not supported by this version of the operator. Please upgrade to a version >=%s", opsManager.Spec.Version, oldestSupportedOpsManagerVersion), log, opsManagerExtraStatusParams)
	}

	// register backup
	r.watchMongoDBResourcesReferencedByBackup(*opsManager, log)

	if err, part := opsManager.ProcessValidationsOnReconcile(); err != nil {
		return r.updateStatus(opsManager, workflow.Invalid(err.Error()), log, mdbstatus.NewOMPartOption(part))
	}

	if err := ensureResourcesForArchitectureChange(r.SecretClient, *opsManager); err != nil {
		return r.updateStatus(opsManager, workflow.Failed(xerrors.Errorf("Error ensuring resources for upgrade from 1 to 3 container AppDB: %w", err)), log, opsManagerExtraStatusParams)
	}

	if err := ensureSharedGlobalResources(r.client, *opsManager); err != nil {
		return r.updateStatus(opsManager, workflow.Failed(xerrors.Errorf("Error ensuring shared global resources %w", err)), log, opsManagerExtraStatusParams)
	}

	opsManagerUserPassword, err := r.ensureAppDbPassword(*opsManager, log)

	if err != nil {
		return r.updateStatus(opsManager, workflow.Failed(xerrors.Errorf("Error ensuring Ops Manager user password: %w", err)), log, opsManagerExtraStatusParams)
	}

	// 1. Reconcile AppDB
	emptyResult := reconcile.Result{}
	retryResult := reconcile.Result{Requeue: true}
	appDbReconciler := newAppDBReplicaSetReconciler(r.ReconcileCommonController, r.omConnectionFactory, r.versionMappingProvider)
	result, err := appDbReconciler.ReconcileAppDB(opsManager, opsManagerUserPassword)
	if err != nil || (result != emptyResult && result != retryResult) {
		return result, err
	}

	// 2. Reconcile Ops Manager
	status, omAdmin := r.reconcileOpsManager(opsManager, opsManagerUserPassword, log)
	if !status.IsOK() {
		return r.updateStatus(opsManager, status, log, opsManagerExtraStatusParams, mdbstatus.NewBaseUrlOption(opsManager.CentralURL()))
	}

	// the AppDB still needs to configure monitoring, now that Ops Manager has been created
	// we can finish this configuration.
	if result.Requeue {
		log.Infof("Requeuing reconciliation to configure AppDB monitoring in Ops Manager.")
		return result, nil
	}

	// 3. Reconcile Backup Daemon
	if status := r.reconcileBackupDaemon(opsManager, omAdmin, opsManagerUserPassword, log); !status.IsOK() {
		return r.updateStatus(opsManager, status, log, mdbstatus.NewOMPartOption(mdbstatus.Backup))
	}

	annotationsToAdd, err := getAnnotationsForOpsManagerResource(opsManager)
	if err != nil {
		return r.updateStatus(opsManager, workflow.Failed(err), log)
	}

	if vault.IsVaultSecretBackend() {
		vaultMap := make(map[string]string)
		for _, s := range opsManager.GetSecretsMountedIntoPod() {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.OpsManagerSecretMetadataPath(), appDBReplicaSet.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}
		for _, s := range opsManager.Spec.AppDB.GetSecretsMountedIntoPod() {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.AppDBSecretMetadataPath(), appDBReplicaSet.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}

		for k, val := range vaultMap {
			annotationsToAdd[k] = val
		}
	}

	if err := annotations.SetAnnotations(opsManager, annotationsToAdd, r.client); err != nil {
		return r.updateStatus(opsManager, workflow.Failed(err), log)
	}
	// All statuses are updated by now - we don't need to update any others - just return
	log.Info("Finished reconciliation for MongoDbOpsManager!")
	// success
	return reconcile.Result{}, nil
}

// getMonitoringAgentVersion returns the minimum supported agent version for the given version of Ops Manager.
func getMonitoringAgentVersion(opsManager omv1.MongoDBOpsManager, readFile func(filename string) ([]byte, error)) (string, error) {
	version, err := versionutil.StringToSemverVersion(opsManager.Spec.Version)
	if err != nil {
		return "", xerrors.Errorf("failed extracting semver version from Ops Manager version %s: %w", opsManager.Spec.Version, err)
	}

	majorMinor := fmt.Sprintf("%d.%d", version.Major, version.Minor)
	fileContainingMappingsBytes, err := readFile(opsManagerToVersionMappingJsonFilePath)
	if err != nil {
		return "", xerrors.Errorf("failed reading file %s: %w", opsManagerToVersionMappingJsonFilePath, err)
	}

	// no bytes but no error, with an empty string we will use the same version as automation agent.
	if fileContainingMappingsBytes == nil {
		return "", nil
	}

	m := omv1.OpsManagerAgentVersionMapping{}
	if err := json.Unmarshal(fileContainingMappingsBytes, &m); err != nil {
		return "", xerrors.Errorf("failed unmarshalling bytes: %w", err)
	}

	agentVersion := m.FindAgentVersionForOpsManager(majorMinor)
	if agentVersion == "" {
		return "", xerrors.Errorf("agent version not present in the mapping file %s", opsManagerToVersionMappingJsonFilePath)
	} else {
		return agentVersion, nil
	}
}

// ensureSharedGlobalResources ensures that resources that are shared across watched namespaces (e.g. secrets) are in sync
func ensureSharedGlobalResources(secretGetUpdaterCreator secret.GetUpdateCreator, opsManager omv1.MongoDBOpsManager) error {
	operatorNamespace := env.ReadOrPanic(util.CurrentNamespace)
	if operatorNamespace == opsManager.Namespace {
		// nothing to sync, OM runs in the same namespace as the operator
		return nil
	}

	if imagePullSecretsName, found := env.Read(util.ImagePullSecrets); found {
		imagePullSecrets, err := secretGetUpdaterCreator.GetSecret(kube.ObjectKey(operatorNamespace, imagePullSecretsName))
		if err != nil {
			return err
		}

		omNsSecret := secret.Builder().
			SetName(imagePullSecretsName).
			SetNamespace(opsManager.Namespace).
			SetByteData(imagePullSecrets.Data).
			Build()
		omNsSecret.Type = imagePullSecrets.Type
		if err := createOrUpdateSecretIfNotFound(secretGetUpdaterCreator, omNsSecret); err != nil {
			return err
		}
	}

	return nil
}

// ensureResourcesForArchitectureChange ensures that the new resources expected to be present.
func ensureResourcesForArchitectureChange(secretGetUpdaterCreator secret.GetUpdateCreator, opsManager omv1.MongoDBOpsManager) error {
	acSecret, err := secretGetUpdaterCreator.GetSecret(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName()))

	// if the automation config does not exist, we are not upgrading from an existing deployment. We can create everything from scratch.
	if err != nil {
		if !secrets.SecretNotExist(err) {
			return xerrors.Errorf("error getting existing automation config secret: %w", err)
		}
		return nil
	}

	ac, err := automationconfig.FromBytes(acSecret.Data[automationconfig.ConfigKey])
	if err != nil {
		return xerrors.Errorf("error unmarshalling existing automation: %w", err)
	}

	// the Ops Manager user should always exist within the automation config.
	var omUser automationconfig.MongoDBUser
	for _, user := range ac.Auth.Users {
		if user.Username == util.OpsManagerMongoDBUserName {
			omUser = user
			break
		}
	}

	if omUser.Username == "" {
		return xerrors.Errorf("ops manager user not present in the automation config")
	}

	err = createOrUpdateSecretIfNotFound(secretGetUpdaterCreator, secret.Builder().
		SetName(opsManager.Spec.AppDB.OpsManagerUserScramCredentialsName()).
		SetNamespace(opsManager.Namespace).
		SetField("sha1-salt", omUser.ScramSha1Creds.Salt).
		SetField("sha-1-server-key", omUser.ScramSha1Creds.ServerKey).
		SetField("sha-1-stored-key", omUser.ScramSha1Creds.StoredKey).
		SetField("sha256-salt", omUser.ScramSha256Creds.Salt).
		SetField("sha-256-server-key", omUser.ScramSha256Creds.ServerKey).
		SetField("sha-256-stored-key", omUser.ScramSha256Creds.StoredKey).
		Build(),
	)
	if err != nil {
		return xerrors.Errorf("failed to create/update scram crdentials secret for Ops Manager user: %w", err)
	}

	// ensure that the agent password stays consistent with what it was previously
	err = createOrUpdateSecretIfNotFound(secretGetUpdaterCreator, secret.Builder().
		SetName(opsManager.Spec.AppDB.GetAgentPasswordSecretNamespacedName().Name).
		SetNamespace(opsManager.Spec.AppDB.GetAgentPasswordSecretNamespacedName().Namespace).
		SetField(scram.AgentPasswordKey, ac.Auth.AutoPwd).
		Build(),
	)
	if err != nil {
		return xerrors.Errorf("failed to create/update password secret for agent user: %w", err)
	}

	// ensure that the keyfile stays consistent with what it was previously
	err = createOrUpdateSecretIfNotFound(secretGetUpdaterCreator, secret.Builder().
		SetName(opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name).
		SetNamespace(opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Namespace).
		SetField(scram.AgentKeyfileKey, ac.Auth.Key).
		Build(),
	)

	if err != nil {
		return xerrors.Errorf("failed to create/update keyfile secret for agent user: %w", err)
	}

	// there was a rename for a specific secret, `om-resource-db-password -> om-resource-db-om-password`
	// this was done as now there are multiple secrets associated with the AppDB, and the contents of this old one correspond to the Ops Manager user.
	oldOpsManagerUserPasswordSecret, err := secretGetUpdaterCreator.GetSecret(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()+"-password"))
	if err != nil {
		// if it's not there, we don't want to create it. We only want to create the new secret if it is present.
		if secrets.SecretNotExist(err) {
			return nil
		}
		return err
	}

	return secret.CreateOrUpdate(secretGetUpdaterCreator, secret.Builder().
		SetNamespace(opsManager.Namespace).
		SetName(opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetByteData(oldOpsManagerUserPasswordSecret.Data).
		Build(),
	)
}

// createOrUpdateSecretIfNotFound creates the given secret if it does not exist.
func createOrUpdateSecretIfNotFound(secretGetUpdaterCreator secret.GetUpdateCreator, desiredSecret corev1.Secret) error {
	_, err := secretGetUpdaterCreator.GetSecret(kube.ObjectKey(desiredSecret.Namespace, desiredSecret.Name))
	if err != nil {
		if secrets.SecretNotExist(err) {
			return secret.CreateOrUpdate(secretGetUpdaterCreator, desiredSecret)
		}
		return xerrors.Errorf("error getting secret %s/%s: %w", desiredSecret.Namespace, desiredSecret.Name, err)
	}
	return nil

}

func (r *OpsManagerReconciler) reconcileOpsManager(opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) (workflow.Status, api.OpsManagerAdmin) {
	statusOptions := []mdbstatus.Option{mdbstatus.NewOMPartOption(mdbstatus.OpsManager), mdbstatus.NewBaseUrlOption(opsManager.CentralURL())}

	_, err := r.updateStatus(opsManager, workflow.Reconciling(), log, statusOptions...)
	if err != nil {
		return workflow.Failed(err), nil
	}

	// Prepare Ops Manager StatefulSet (create and wait)
	status := r.createOpsManagerStatefulset(*opsManager, opsManagerUserPassword, log)
	if !status.IsOK() {
		return status, nil
	}

	// 3. Prepare Ops Manager (ensure the first user is created and public API key saved to secret)
	var omAdmin api.OpsManagerAdmin
	if status, omAdmin = r.prepareOpsManager(*opsManager, log); !status.IsOK() {
		return status, nil
	}

	// 4. Trigger agents upgrade if necessary
	if err = triggerOmChangedEventIfNeeded(*opsManager, log); err != nil {
		log.Warn("Not triggering an Ops Manager version changed event: %s", err)
	}

	// 5. Stop backup daemon if necessary
	if err = r.stopBackupDaemonIfNeeded(*opsManager); err != nil {
		return workflow.Failed(err), nil
	}

	if _, err = r.updateStatus(opsManager, workflow.OK(), log, statusOptions...); err != nil {
		return workflow.Failed(err), nil
	}

	return status, omAdmin
}

// triggerOmChangedEventIfNeeded triggers upgrade process for all the MongoDB agents in the system if the major/minor version upgrade
// happened for Ops Manager
func triggerOmChangedEventIfNeeded(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	if opsManager.Spec.Version == opsManager.Status.OpsManagerStatus.Version || opsManager.Status.OpsManagerStatus.Version == "" {
		return nil
	}
	newVersion, err := versionutil.StringToSemverVersion(opsManager.Spec.Version)
	if err != nil {
		return xerrors.Errorf("failed to parse Ops Manager version %s: %w", opsManager.Spec.Version, err)
	}
	oldVersion, err := versionutil.StringToSemverVersion(opsManager.Status.OpsManagerStatus.Version)
	if err != nil {
		return xerrors.Errorf("failed to parse Ops Manager status version %s: %w", opsManager.Status.OpsManagerStatus.Version, err)
	}
	if newVersion.Major != oldVersion.Major || newVersion.Minor != oldVersion.Minor {
		log.Infof("Ops Manager version has upgraded from %s to %s - scheduling the upgrade for all the Agents in the system", oldVersion, newVersion)
		agents.ScheduleUpgrade()
	}

	return nil
}

// stopBackupDaemonIfNeeded stops the backup daemon when OM is upgraded.
// Otherwise, the backup daemon will remain in a broken state (because of version missmatch between OM and backup daemon)
// due to this STS limitation: https://github.com/kubernetes/kubernetes/issues/67250.
// Later, the normal reconcile process will update the STS and start the backup daemon.
func (r *OpsManagerReconciler) stopBackupDaemonIfNeeded(opsManager omv1.MongoDBOpsManager) error {
	if opsManager.Spec.Version == opsManager.Status.OpsManagerStatus.Version || opsManager.Status.OpsManagerStatus.Version == "" {
		return nil
	}

	if _, err := r.scaleStatefulSet(opsManager.Namespace, opsManager.BackupStatefulSetName(), 0); err != nil {
		return client.IgnoreNotFound(err)
	}

	// delete all backup daemon pods, scaling down the statefulSet to 0 does not terminate the pods,
	// if the number of pods is greater than 1 and all of them are in a unhealthy state
	cleanupOptions := mongodbCleanUpOptions{
		namespace: opsManager.Namespace,
		labels: map[string]string{
			"app": opsManager.BackupServiceName(),
		},
	}
	err := r.client.DeleteAllOf(context.TODO(), &corev1.Pod{}, &cleanupOptions)

	return client.IgnoreNotFound(err)
}

func (r *OpsManagerReconciler) reconcileBackupDaemon(opsManager *omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, opsManagerUserPassword string, log *zap.SugaredLogger) workflow.Status {
	backupStatusPartOption := mdbstatus.NewOMPartOption(mdbstatus.Backup)

	// If backup is not enabled, we check whether it is still configured in OM to update the status.
	if !opsManager.Spec.Backup.Enabled {
		var backupStatus workflow.Status
		backupStatus = workflow.OK()

		for _, hostName := range opsManager.BackupDaemonFQDNs() {
			_, err := omAdmin.ReadDaemonConfig(hostName, util.PvcMountPathHeadDb)
			if apierror.NewNonNil(err).ErrorCode == apierror.BackupDaemonConfigNotFound {
				backupStatus = workflow.Disabled()
				break
			}
		}

		_, err := r.updateStatus(opsManager, backupStatus, log, backupStatusPartOption)
		if err != nil {
			return workflow.Failed(err)
		}
		return backupStatus
	}
	_, err := r.updateStatus(opsManager, workflow.Reconciling(), log, backupStatusPartOption)
	if err != nil {
		return workflow.Failed(err)
	}

	// Prepare Backup Daemon StatefulSet (create and wait)
	if status := r.createBackupDaemonStatefulset(*opsManager, opsManagerUserPassword, log); !status.IsOK() {
		return status
	}

	// Configure Backup using API
	if status := r.prepareBackupInOpsManager(*opsManager, omAdmin, log); !status.IsOK() {
		return status
	}

	// StatefulSet will reach ready state eventually once backup has been configured in Ops Manager.
	if status := getStatefulSetStatus(opsManager.Namespace, opsManager.BackupStatefulSetName(), r.client); !status.IsOK() {
		return status
	}

	if _, err := r.updateStatus(opsManager, workflow.OK(), log, backupStatusPartOption); err != nil {
		return workflow.Failed(err)
	}

	return workflow.OK()
}

// readOpsManagerResource reads Ops Manager Custom resource into pointer provided
func (r *OpsManagerReconciler) readOpsManagerResource(request reconcile.Request, ref *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (reconcile.Result, error) {
	if result, err := r.getResource(request, ref, log); err != nil {
		return result, err
	}
	// Reset warnings so that they are not stale, will populate accurate warnings in reconciliation
	ref.SetWarnings([]mdbstatus.Warning{}, mdbstatus.NewOMPartOption(mdbstatus.OpsManager), mdbstatus.NewOMPartOption(mdbstatus.AppDb), mdbstatus.NewOMPartOption(mdbstatus.Backup))
	return reconcile.Result{}, nil
}

// ensureAppDBConnectionString ensures that the AppDB Connection String exists in a secret.
func (r *OpsManagerReconciler) ensureAppDBConnectionString(opsManager omv1.MongoDBOpsManager, computedConnectionString string, log *zap.SugaredLogger) error {
	var opsManagerSecretPath string
	if r.VaultClient != nil {
		opsManagerSecretPath = r.VaultClient.OpsManagerSecretPath()
	}
	_, err := r.ReadSecret(kube.ObjectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()), opsManagerSecretPath)

	if err != nil {
		if secrets.SecretNotExist(err) {
			log.Debugf("AppDB connection string secret was not found, creating %s now", kube.ObjectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()))
			// assume the secret was not found, need to create it

			connectionStringSecret := secret.Builder().
				SetName(opsManager.AppDBMongoConnectionStringSecretName()).
				SetNamespace(opsManager.Namespace).
				SetField(util.AppDbConnectionStringKey, computedConnectionString).
				Build()

			return r.PutSecret(connectionStringSecret, opsManagerSecretPath)
		}
		log.Warnf("Error getting connection string secret: %s", err)
		return err
	}

	connectionStringSecretData := map[string]string{
		util.AppDbConnectionStringKey: computedConnectionString,
	}
	connectionStringSecret := secret.Builder().
		SetName(opsManager.AppDBMongoConnectionStringSecretName()).
		SetNamespace(opsManager.Namespace).
		SetStringMapToData(connectionStringSecretData).Build()
	log.Debugf("Connection string secret already exists, updating %s", kube.ObjectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()))
	return r.PutSecret(connectionStringSecret, opsManagerSecretPath)
}

func hashConnectionString(connectionString string) string {
	bytes := []byte(connectionString)
	hashBytes := sha256.Sum256(bytes)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:])
}

// createOpsManagerStatefulset ensures the gen key secret exists and creates the Ops Manager StatefulSet.
func (r *OpsManagerReconciler) createOpsManagerStatefulset(opsManager omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) workflow.Status {
	if err := r.ensureGenKey(opsManager, log); err != nil {
		return workflow.Failed(err)
	}

	connectionString := buildMongoConnectionUrl(opsManager, opsManagerUserPassword)
	if err := r.ensureAppDBConnectionString(opsManager, connectionString, log); err != nil {
		return workflow.Failed(err)
	}

	r.ensureConfiguration(&opsManager, log)

	var vaultConfig vault.VaultConfiguration
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
	}
	sts, err := construct.OpsManagerStatefulSet(r.SecretClient, opsManager, log,
		construct.WithConnectionStringHash(hashConnectionString(connectionString)),
		construct.WithVaultConfig(vaultConfig),
		construct.WithKmipConfig(opsManager, r.client, log),
	)

	if err != nil {
		return workflow.Failed(xerrors.Errorf("error building OpsManager stateful set: %w", err))
	}

	if err := create.OpsManagerInKubernetes(r.client, opsManager, sts, log); err != nil {
		return workflow.Failed(err)
	}

	if status := getStatefulSetStatus(opsManager.Namespace, opsManager.Name, r.client); !status.IsOK() {
		return status
	}

	return workflow.OK()
}

func AddOpsManagerController(mgr manager.Manager) error {
	reconciler := newOpsManagerReconciler(mgr, om.NewOpsManagerConnection, &api.DefaultInitializer{}, api.NewOmAdmin, ioutil.ReadFile)
	c, err := controller.New(util.MongoDbOpsManagerController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	// watch for changes to the Ops Manager resources
	eventHandler := MongoDBOpsManagerEventHandler{reconciler: reconciler}

	if err = c.Watch(&source.Kind{Type: &omv1.MongoDBOpsManager{}}, &eventHandler, watch.PredicatesForOpsManager()); err != nil {
		return err
	}

	// watch the secret with the Ops Manager user password
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}},
		&watch.ResourcesHandler{ResourceType: watch.MongoDB, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	// if vault secret backend is enabled watch for Vault secret change and trigger reconcile
	if vault.IsVaultSecretBackend() {
		eventChannel := make(chan event.GenericEvent)
		go vaultwatcher.WatchSecretChangeForOM(zap.S(), eventChannel, reconciler.client, reconciler.VaultClient)

		err = c.Watch(
			&source.Channel{Source: eventChannel},
			&handler.EnqueueRequestForObject{},
		)
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %w", err)
		}
	}
	zap.S().Infof("Registered controller %s", util.MongoDbOpsManagerController)
	return nil
}

// ensureConfiguration makes sure the mandatory configuration is specified.
func (r OpsManagerReconciler) ensureConfiguration(opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) {
	// update the central URL
	setConfigProperty(opsManager, util.MmsCentralUrlPropKey, opsManager.CentralURL(), log)

	if opsManager.Spec.AppDB.Security.IsTLSEnabled() {
		setConfigProperty(opsManager, util.MmsMongoSSL, "true", log)
	}
	if opsManager.Spec.AppDB.GetCAConfigMapName() != "" {
		setConfigProperty(opsManager, util.MmsMongoCA, util.AppDBMmsCaFileDirInContainer+"ca-pem", log)
	}

	// override the versions directory (defaults to "/opt/mongodb/mms/mongodb-releases/")
	setConfigProperty(opsManager, util.MmsVersionsDirectory, "/mongodb-ops-manager/mongodb-releases/", log)

	// feature controls will always be enabled
	setConfigProperty(opsManager, util.MmsFeatureControls, "true", log)

	if opsManager.Spec.Backup.QueryableBackupSecretRef.Name != "" {
		setConfigProperty(opsManager, util.BrsQueryablePem, "/certs/queryable.pem", log)
	}
}

// createBackupDaemonStatefulset creates a StatefulSet for backup daemon and waits shortly until it's started
// Note, that the idea of creating two statefulsets for Ops Manager and Backup Daemon in parallel hasn't worked out
// as the daemon in this case just hangs silently (in practice it's ok to start it in ~1 min after start of OM though
// we will just start them sequentially)
func (r *OpsManagerReconciler) createBackupDaemonStatefulset(opsManager omv1.MongoDBOpsManager,
	opsManagerUserPassword string, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}
	connectionString := buildMongoConnectionUrl(opsManager, opsManagerUserPassword)
	if err := r.ensureAppDBConnectionString(opsManager, connectionString, log); err != nil {
		return workflow.Failed(err)
	}

	r.ensureConfiguration(&opsManager, log)

	var vaultConfig vault.VaultConfiguration
	if r.VaultClient != nil {
		vaultConfig = r.VaultClient.VaultConfig
	}
	sts, err := construct.BackupDaemonStatefulSet(r.SecretClient, opsManager, log,
		construct.WithConnectionStringHash(hashConnectionString(connectionString)),
		construct.WithVaultConfig(vaultConfig),
		construct.WithKmipConfig(opsManager, r.client, log))
	if err != nil {
		return workflow.Failed(xerrors.Errorf("error building stateful set: %w", err))
	}

	needToRequeue, err := create.BackupDaemonInKubernetes(r.client, opsManager, sts, log)
	if err != nil {
		return workflow.Failed(err)
	}
	if needToRequeue {
		return workflow.OK().Requeue()
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) watchMongoDBResourcesReferencedByKmip(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) {
	if !opsManager.Spec.IsKmipEnabled() {
		return
	}

	mdbList := &mdbv1.MongoDBList{}
	err := r.client.List(context.TODO(), mdbList)
	if err != nil {
		log.Warnf("failed to fetch MongoDBList from Kubernetes: %v", err)
	}

	for _, m := range mdbList.Items {
		if m.Spec.Backup != nil && m.Spec.Backup.IsKmipEnabled() {
			r.AddWatchedResourceIfNotAdded(
				m.Name,
				m.Namespace,
				watch.MongoDB,
				kube.ObjectKeyFromApiObject(&opsManager))

			r.AddWatchedResourceIfNotAdded(
				m.Spec.Backup.Encryption.Kmip.Client.ClientCertificateSecretName(m.GetName()),
				opsManager.Namespace,
				watch.Secret,
				kube.ObjectKeyFromApiObject(&opsManager))

			r.AddWatchedResourceIfNotAdded(
				m.Spec.Backup.Encryption.Kmip.Client.ClientCertificatePasswordSecretName(m.GetName()),
				opsManager.Namespace,
				watch.Secret,
				kube.ObjectKeyFromApiObject(&opsManager))
		}
	}
}

func (r *OpsManagerReconciler) watchCaReferencedByKmip(opsManager omv1.MongoDBOpsManager) {
	if !opsManager.Spec.IsKmipEnabled() {
		return
	}

	r.AddWatchedResourceIfNotAdded(
		opsManager.Spec.Backup.Encryption.Kmip.Server.CA,
		opsManager.Namespace,
		watch.ConfigMap,
		kube.ObjectKeyFromApiObject(&opsManager))
}

func (r *OpsManagerReconciler) watchMongoDBResourcesReferencedByBackup(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) {
	if !opsManager.Spec.Backup.Enabled {
		return
	}

	// watch mongodb resources for oplog
	oplogs := opsManager.Spec.Backup.OplogStoreConfigs
	for _, oplogConfig := range oplogs {
		r.AddWatchedResourceIfNotAdded(
			oplogConfig.MongoDBResourceRef.Name,
			opsManager.Namespace,
			watch.MongoDB,
			kube.ObjectKeyFromApiObject(&opsManager),
		)
	}

	// watch mongodb resources for block stores
	blockstores := opsManager.Spec.Backup.BlockStoreConfigs
	for _, blockStoreConfig := range blockstores {
		r.AddWatchedResourceIfNotAdded(
			blockStoreConfig.MongoDBResourceRef.Name,
			opsManager.Namespace,
			watch.MongoDB,
			kube.ObjectKeyFromApiObject(&opsManager),
		)
	}

	// watch mongodb resources for s3 stores
	s3Stores := opsManager.Spec.Backup.S3Configs
	for _, s3StoreConfig := range s3Stores {
		// If S3StoreConfig doesn't have mongodb resource reference, skip it (appdb will be used)
		if s3StoreConfig.MongoDBResourceRef != nil {
			r.AddWatchedResourceIfNotAdded(
				s3StoreConfig.MongoDBResourceRef.Name,
				opsManager.Namespace,
				watch.MongoDB,
				kube.ObjectKeyFromApiObject(&opsManager),
			)
		}
	}

	r.watchMongoDBResourcesReferencedByKmip(opsManager, log)
	r.watchCaReferencedByKmip(opsManager)
}

// buildMongoConnectionUrl returns a connection URL to the appdb.
//
// Note, that it overrides the default authMechanism (which internally depends
// on the mongodb version).
func buildMongoConnectionUrl(opsManager omv1.MongoDBOpsManager, password string) string {
	connectionString := opsManager.Spec.AppDB.BuildConnectionURL(
		util.OpsManagerMongoDBUserName,
		password,
		connectionstring.SchemeMongoDB,
		map[string]string{"authMechanism": "SCRAM-SHA-256"})

	return connectionString
}

func setConfigProperty(opsManager *omv1.MongoDBOpsManager, key, value string, log *zap.SugaredLogger) {
	if opsManager.AddConfigIfDoesntExist(key, value) {
		if key == util.MmsMongoUri {
			log.Debugw("Configured property", key, util.RedactMongoURI(value))
		} else {
			log.Debugw("Configured property", key, value)
		}
	}
}

// ensureGenKey
func (r OpsManagerReconciler) ensureGenKey(om omv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	objectKey := kube.ObjectKey(om.Namespace, om.Name+"-gen-key")
	var opsManagerSecretPath string
	if r.VaultClient != nil {
		opsManagerSecretPath = r.VaultClient.OpsManagerSecretPath()
	}
	_, err := r.ReadSecret(objectKey, opsManagerSecretPath)

	if secrets.SecretNotExist(err) {
		// todo if the key is not found but the AppDB is initialized - OM will fail to start as preflight
		// check will complain that keys are different - we need to validate against this here

		// the length must be equal to 'EncryptionUtils.DES3_KEY_LENGTH' (24) from mms
		token := make([]byte, 24)
		rand.Read(token)
		keyMap := map[string][]byte{"gen.key": token}

		log.Infof("Creating secret %s", objectKey)

		genKeySecret := secret.Builder().
			SetName(objectKey.Name).
			SetNamespace(objectKey.Namespace).
			SetLabels(map[string]string{}).
			SetByteData(keyMap).
			Build()

		return r.PutBinarySecret(genKeySecret, opsManagerSecretPath)
	}
	return err
}

// getAppDBPassword will return the password that was specified by the user, or the auto generated password stored in
// the secret (generate it and store in secret otherwise)
func (r OpsManagerReconciler) getAppDBPassword(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	passwordRef := opsManager.Spec.AppDB.PasswordSecretKeyRef
	if passwordRef != nil && passwordRef.Name != "" { // there is a secret specified for the Ops Manager user

		password, err := secret.ReadKey(r.client, passwordRef.Key, kube.ObjectKey(opsManager.Namespace, passwordRef.Name))
		if err != nil {
			if secrets.SecretNotExist(err) {
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
		log.Debugf("Deleting Operator managed password secret/%s from namespace", opsManager.Spec.AppDB.GetSecretName(), opsManager.Namespace)
		if err := r.client.DeleteSecret(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())); err != nil && !secrets.SecretNotExist(err) {
			return "", err
		}

		return password, nil
	}

	// otherwise we'll ensure the auto generated password exists
	secretObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())
	appDbPasswordSecretStringData, err := secret.ReadStringData(r.client, secretObjectKey)

	if secrets.SecretNotExist(err) {
		// create the password
		password, err := generate.RandomFixedLengthStringOfSize(12)
		if err != nil {
			return "", err
		}

		passwordData := map[string]string{
			util.OpsManagerPasswordKey: password,
		}

		log.Infof("Creating mongodb-ops-manager password in secret/%s in namespace %s", secretObjectKey.Name, secretObjectKey.Namespace)

		appDbPasswordSecret := secret.Builder().
			SetName(secretObjectKey.Name).
			SetNamespace(secretObjectKey.Namespace).
			SetStringMapToData(passwordData).
			SetOwnerReferences(kube.BaseOwnerReference(&opsManager)).
			Build()

		if err := r.client.CreateSecret(appDbPasswordSecret); err != nil {
			return "", err
		}

		log.Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetSecretName())
		return password, nil
	} else if err != nil {
		// any other error
		return "", err
	}

	log.Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetSecretName())
	return appDbPasswordSecretStringData[util.OpsManagerPasswordKey], nil
}

func (r OpsManagerReconciler) getOpsManagerAPIKeySecretName(opsManager omv1.MongoDBOpsManager) (string, workflow.Status) {
	var operatorVaultSecretPath string
	if r.VaultClient != nil {
		operatorVaultSecretPath = r.VaultClient.OperatorSecretPath()
	}
	APISecretName, err := opsManager.APIKeySecretName(r.SecretClient, operatorVaultSecretPath)
	if err != nil {
		return "", workflow.Failed(xerrors.Errorf("failed to get ops-manager API key secret name: %w", err)).WithRetry(10)
	}
	return APISecretName, workflow.OK()
}

func detailedAPIErrorMsg(adminKeySecretName types.NamespacedName) string {
	return fmt.Sprintf("This is a fatal error, as the"+
		" Operator requires public API key for the admin user to exist. Please create the GLOBAL_ADMIN user in "+
		"Ops Manager manually and create a secret '%s' with fields '%s' and '%s'", adminKeySecretName, util.OmPublicApiKey,
		util.OmPrivateKey)
}

// prepareOpsManager ensures the admin user is created and the admin public key exists. It returns the instance of
// api.OpsManagerAdmin to perform future Ops Manager configuration
// Note the exception handling logic - if the controller fails to save the public API key secret - it cannot fix this
// manually (the first OM user can be created only once) - so the resource goes to Failed state and shows the message
// asking the user to fix this manually.
// Theoretically the Operator could remove the appdb StatefulSet (as the OM must be empty without any user data) and
// allow the db to get recreated but seems this is a quite radical operation.
func (r OpsManagerReconciler) prepareOpsManager(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) (workflow.Status, api.OpsManagerAdmin) {
	// We won't support cross-namespace secrets until CLOUDP-46636 is resolved
	adminObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AdminSecret)

	var operatorVaultPath string
	if r.VaultClient != nil {
		operatorVaultPath = r.VaultClient.OperatorSecretPath()
	}

	// 1. Read the admin secret
	userData, err := r.ReadSecret(adminObjectKey, operatorVaultPath)

	if secrets.SecretNotExist(err) {
		// This requires user actions - let's wait a bit longer than 10 seconds
		return workflow.Failed(xerrors.Errorf("the secret %s doesn't exist - you need to create it to finish Ops Manager initialization", adminObjectKey)).WithRetry(60), nil
	} else if err != nil {
		return workflow.Failed(err), nil
	}

	user, err := newUserFromSecret(userData)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("failed to read user data from the secret %s: %w", adminObjectKey, err)), nil
	}
	APISecretName, status := r.getOpsManagerAPIKeySecretName(opsManager)
	if !status.IsOK() {
		return status, nil
	}

	adminKeySecretName := kube.ObjectKey(operatorNamespace(), APISecretName)

	// 2. Create a user in Ops Manager if necessary. Note, that we don't send the request if the API key secret exists.
	// This is because of the weird Ops Manager /unauth endpoint logic: it allows to create any number of users though only
	// the first one will have GLOBAL_ADMIN permission. So we should avoid the situation when the admin changes the
	// user secret and reconciles OM resource and the new user (non admin one) is created overriding the previous API secret
	_, err = r.ReadSecret(adminKeySecretName, operatorVaultPath)

	if secrets.SecretNotExist(err) {
		apiKey, err := r.omInitializer.TryCreateUser(opsManager.CentralURL(), opsManager.Spec.Version, user)
		if err != nil {
			// Will wait more than usual (10 seconds) as most of all the problem needs to get fixed by the user
			// by modifying the credentials secret
			return workflow.Failed(xerrors.Errorf("failed to create an admin user in Ops Manager: %w", err)).WithRetry(30), nil
		}

		// Recreate an admin key secret in the Operator namespace if the user was created
		if apiKey.PublicKey != "" {
			log.Infof("Created an admin user %s with GLOBAL_ADMIN role", user.Username)

			// The structure matches the structure of a credentials secret used by normal mongodb resources
			secretData := map[string]string{util.OmPublicApiKey: apiKey.PublicKey, util.OmPrivateKey: apiKey.PrivateKey}

			if err = r.client.DeleteSecret(adminKeySecretName); err != nil && !secrets.SecretNotExist(err) {
				// TODO our desired behavior is not to fail but just append the warning to the status (CLOUDP-51340)
				return workflow.Failed(xerrors.Errorf("failed to replace a secret for admin public api key. %s. The error : %w",
					detailedAPIErrorMsg(adminKeySecretName), err)).WithRetry(300), nil
			}

			adminSecretBuilder := secret.Builder().
				SetNamespace(adminKeySecretName.Namespace).
				SetName(adminKeySecretName.Name).
				SetStringMapToData(secretData).
				SetLabels(map[string]string{})

			if opsManager.Namespace == operatorNamespace() {
				// The Secret where the admin-key is saved is created in the Namespace where the
				// Operator resides.
				// The Secret's OwnerReference is only added if both the Secret and Ops Manager
				// reside in the same Namespace because cross-namespace OwnerReferences are not
				// allowed.
				// More information in: CLOUDP-90848
				adminSecretBuilder.SetOwnerReferences(kube.BaseOwnerReference(&opsManager))
			}
			adminSecret := adminSecretBuilder.Build()

			if err := r.PutSecret(adminSecret, operatorVaultPath); err != nil {
				// TODO see above
				return workflow.Failed(xerrors.Errorf("failed to create a secret for admin public api key. %s. The error : %w",
					detailedAPIErrorMsg(adminKeySecretName), err)).WithRetry(30), nil
			}
			log.Infof("Created a secret for admin public api key %s", adminKeySecretName)

			// Each "read-after-write" operation needs some timeout after write unfortunately :(
			// https://github.com/kubernetes-sigs/controller-runtime/issues/343#issuecomment-468402446
			time.Sleep(time.Duration(env.ReadIntOrDefault(util.K8sCacheRefreshEnv, util.DefaultK8sCacheRefreshTimeSeconds)) * time.Second)
		} else {
			log.Debug("Ops Manager did not return a valid User object.")
		}
	}

	// 3. Final validation of current state - this could be the retry after failing to create the secret during
	// previous reconciliation (and the apiKey is empty as "the first user already exists") - the only fix is
	// to create the secret manually
	_, err = r.ReadSecret(adminKeySecretName, operatorVaultPath)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("admin API key secret for Ops Manager doesn't exit - was it removed accidentally? %s. The error : %w",
			detailedAPIErrorMsg(adminKeySecretName), err)).WithRetry(30), nil
	}
	// Ops Manager api key Secret has the same structure as the MongoDB credentials secret
	APIKeySecretName, err := opsManager.APIKeySecretName(r.SecretClient, operatorVaultPath)
	if err != nil {
		return workflow.Failed(err), nil
	}

	cred, err := project.ReadCredentials(r.SecretClient, kube.ObjectKey(operatorNamespace(), APIKeySecretName), log)
	if err != nil {
		return workflow.Failed(err), nil
	}

	admin := r.omAdminProvider(opsManager.CentralURL(), cred.PublicAPIKey, cred.PrivateAPIKey)
	return workflow.OK(), admin
}

// prepareBackupInOpsManager makes the changes to backup admin configuration based on the Ops Manager spec
func (r *OpsManagerReconciler) prepareBackupInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin,
	log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	// 1. Enabling Daemon Config if necessary
	backupHostNames := opsManager.BackupDaemonFQDNs()
	for _, hostName := range backupHostNames {
		dc, err := omAdmin.ReadDaemonConfig(hostName, util.PvcMountPathHeadDb)
		if apierror.NewNonNil(err).ErrorCode == apierror.BackupDaemonConfigNotFound {
			log.Infow("Backup Daemons is not configured, enabling it", "hostname", hostName, "headDB", util.PvcMountPathHeadDb)

			err = omAdmin.CreateDaemonConfig(hostName, util.PvcMountPathHeadDb, opsManager.Spec.Backup.AssignmentLabels)
			if apierror.NewNonNil(err).ErrorCode == apierror.BackupDaemonConfigNotFound {
				// Unfortunately by this time backup daemon may not have been started yet and we don't have proper
				// mechanism to ensure this using readiness probe so we just retry
				return workflow.Pending("BackupDaemon hasn't started yet")
			} else if err != nil {
				return workflow.Failed(err)
			}
		} else if err != nil {
			return workflow.Failed(err)
		} else {
			// The Assignment Labels are the only thing that can change at the moment.
			// If we add new features for controlling the Backup Daemons, we may want
			// to compare the whole backup.DaemonConfig objects.
			if !reflect.DeepEqual(opsManager.Spec.Backup.AssignmentLabels, dc.Labels) {
				dc.Labels = opsManager.Spec.Backup.AssignmentLabels
				err = omAdmin.UpdateDaemonConfig(dc)
				if err != nil {
					return workflow.Failed(err)
				}
			}
		}
	}

	// 2. Oplog store configs
	status := r.ensureOplogStoresInOpsManager(opsManager, omAdmin, log)

	// 3. S3 Oplog Configs
	status = status.Merge(r.ensureS3OplogStoresInOpsManager(opsManager, omAdmin, log))

	// 4. S3 Configs
	status = status.Merge(r.ensureS3ConfigurationInOpsManager(opsManager, omAdmin, log))

	// 5. Block store configs
	status = status.Merge(r.ensureBlockStoresInOpsManager(opsManager, omAdmin, log))

	// 6. FileSystem store configs
	status = status.Merge(r.ensureFileSystemStoreConfigurationInOpsManager(opsManager, omAdmin, log))
	if len(opsManager.Spec.Backup.S3Configs) == 0 && len(opsManager.Spec.Backup.BlockStoreConfigs) == 0 && len(opsManager.Spec.Backup.FileSystemStoreConfigs) == 0 {
		return status.Merge(workflow.Invalid("Either S3 or Blockstore or FileSystem Snapshot configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending))
	}

	return status
}

// ensureOplogStoresInOpsManager aligns the oplog stores in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureOplogStoresInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.OplogStoreAdmin, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerOplogConfigs, err := omAdmin.ReadOplogStoreConfigs()
	if err != nil {
		return workflow.Failed(err)
	}

	// Creating new configs
	operatorOplogConfigs := opsManager.Spec.Backup.OplogStoreConfigs
	configsToCreate := identifiable.SetDifferenceGeneric(operatorOplogConfigs, opsManagerOplogConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMDatastoreConfig(opsManager, v.(omv1.DataStoreConfig))
		if !status.IsOK() {
			return status
		}
		log.Debugw("Creating Oplog Store in Ops Manager", "config", omConfig)
		if err = omAdmin.CreateOplogStoreConfig(omConfig); err != nil {
			return workflow.Failed(err)
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerOplogConfigs, operatorOplogConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.DataStoreConfig)
		operatorConfig := v[1].(omv1.DataStoreConfig)
		operatorView, status := r.buildOMDatastoreConfig(opsManager, operatorConfig)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Debugw("Updating Oplog Store in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateOplogStoreConfig(configToUpdate); err != nil {
			return workflow.Failed(err)
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerOplogConfigs, opsManager.Spec.Backup.OplogStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Oplog Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteOplogStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(err)
		}
	}

	operatorS3OplogConfigs := opsManager.Spec.Backup.S3OplogStoreConfigs
	if len(operatorOplogConfigs) == 0 && len(operatorS3OplogConfigs) == 0 {
		return workflow.Invalid("Oplog Store configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending)
	}
	return workflow.OK()
}

func (r OpsManagerReconciler) ensureS3OplogStoresInOpsManager(opsManager omv1.MongoDBOpsManager, s3OplogAdmin api.S3OplogStoreAdmin, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerS3OpLogConfigs, err := s3OplogAdmin.ReadS3OplogStoreConfigs()
	if err != nil {
		return workflow.Failed(err)
	}

	// Creating new configs
	s3OperatorOplogConfigs := opsManager.Spec.Backup.S3OplogStoreConfigs
	configsToCreate := identifiable.SetDifferenceGeneric(s3OperatorOplogConfigs, opsManagerS3OpLogConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMS3Config(opsManager, v.(omv1.S3Config), log)
		if !status.IsOK() {
			return status
		}
		log.Infow("Creating S3 Oplog Store in Ops Manager", "config", omConfig)
		if err = s3OplogAdmin.CreateS3OplogStoreConfig(omConfig); err != nil {
			return workflow.Failed(err)
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerS3OpLogConfigs, s3OperatorOplogConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.S3Config)
		operatorConfig := v[1].(omv1.S3Config)
		operatorView, status := r.buildOMS3Config(opsManager, operatorConfig, log)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Infow("Updating S3 Oplog Store in Ops Manager", "config", configToUpdate)
		if err = s3OplogAdmin.UpdateS3OplogConfig(configToUpdate); err != nil {
			return workflow.Failed(err)
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerS3OpLogConfigs, opsManager.Spec.Backup.S3OplogStoreConfigs)
	for _, v := range configsToRemove {
		log.Infof("Removing Oplog Store %s from Ops Manager", v.Identifier())
		if err = s3OplogAdmin.DeleteS3OplogStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(err)
		}
	}

	operatorOplogConfigs := opsManager.Spec.Backup.OplogStoreConfigs
	if len(operatorOplogConfigs) == 0 && len(s3OperatorOplogConfigs) == 0 {
		return workflow.Invalid("Oplog Store configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending)
	}
	return workflow.OK()
}

// ensureBlockStoresInOpsManager aligns the blockStore configs in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureBlockStoresInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.BlockStoreAdmin, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerBlockStoreConfigs, err := omAdmin.ReadBlockStoreConfigs()
	if err != nil {
		return workflow.Failed(err)
	}

	// Creating new configs
	operatorBlockStoreConfigs := opsManager.Spec.Backup.BlockStoreConfigs
	configsToCreate := identifiable.SetDifferenceGeneric(operatorBlockStoreConfigs, opsManagerBlockStoreConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMDatastoreConfig(opsManager, v.(omv1.DataStoreConfig))
		if !status.IsOK() {
			return status
		}
		log.Debugw("Creating Block Store in Ops Manager", "config", omConfig)
		if err = omAdmin.CreateBlockStoreConfig(omConfig); err != nil {
			return workflow.Failed(err)
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerBlockStoreConfigs, operatorBlockStoreConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.DataStoreConfig)
		operatorConfig := v[1].(omv1.DataStoreConfig)
		operatorView, status := r.buildOMDatastoreConfig(opsManager, operatorConfig)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Debugw("Updating Block Store in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateBlockStoreConfig(configToUpdate); err != nil {
			return workflow.Failed(err)
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerBlockStoreConfigs, opsManager.Spec.Backup.BlockStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Block Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteBlockStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(err)
		}
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) ensureS3ConfigurationInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.S3StoreBlockStoreAdmin,
	log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerS3Configs, err := omAdmin.ReadS3Configs()
	if err != nil {
		return workflow.Failed(err)
	}

	operatorS3Configs := opsManager.Spec.Backup.S3Configs
	configsToCreate := identifiable.SetDifferenceGeneric(operatorS3Configs, opsManagerS3Configs)
	for _, config := range configsToCreate {
		omConfig, status := r.buildOMS3Config(opsManager, config.(omv1.S3Config), log)
		if !status.IsOK() {
			return status
		}

		log.Infow("Creating S3Config in Ops Manager", "config", omConfig)
		if err := omAdmin.CreateS3Config(omConfig); err != nil {
			return workflow.Failed(err)
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := identifiable.SetIntersectionGeneric(opsManagerS3Configs, operatorS3Configs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.S3Config)
		operatorConfig := v[1].(omv1.S3Config)
		operatorView, status := r.buildOMS3Config(opsManager, operatorConfig, log)
		if !status.IsOK() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Infow("Updating S3Config in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateS3Config(configToUpdate); err != nil {
			return workflow.Failed(err)
		}
	}

	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerS3Configs, operatorS3Configs)
	for _, config := range configsToRemove {
		log.Infof("Removing S3Config %s from Ops Manager", config.Identifier())
		if err := omAdmin.DeleteS3Config(config.Identifier().(string)); err != nil {
			return workflow.Failed(err)
		}
	}

	return workflow.OK()
}

// readS3Credentials reads the access and secret keys from the awsCredentials secret specified
// in the resource
func (r *OpsManagerReconciler) readS3Credentials(s3SecretName, namespace string) (*backup.S3Credentials, error) {
	var operatorSecretPath string
	if r.VaultClient != nil {
		operatorSecretPath = r.VaultClient.OperatorSecretPath()
	}

	s3SecretData, err := r.ReadSecret(kube.ObjectKey(namespace, s3SecretName), operatorSecretPath)
	if err != nil {
		return nil, err
	}

	s3Creds := &backup.S3Credentials{}
	if accessKey, ok := s3SecretData[util.S3AccessKey]; !ok {
		return nil, xerrors.Errorf("key %s was not present in the secret %s", util.S3AccessKey, s3SecretName)
	} else {
		s3Creds.AccessKey = accessKey
	}

	if secretKey, ok := s3SecretData[util.S3SecretKey]; !ok {
		return nil, xerrors.Errorf("key %s was not present in the secret %s", util.S3SecretKey, s3SecretName)
	} else {
		s3Creds.SecretKey = secretKey
	}

	return s3Creds, nil
}

// ensureFileSystemStoreConfigurationInOpsManage makes sure that the FileSystem snapshot stores specified in the
// MongoDB CR are configured correctly in OpsManager.
func (r *OpsManagerReconciler) ensureFileSystemStoreConfigurationInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.OpsManagerAdmin, log *zap.SugaredLogger) workflow.Status {
	opsManagefsStoreConfigs, err := omAdmin.ReadFileSystemStoreConfigs()
	if err != nil {
		return workflow.Failed(err)
	}

	fsStoreNames := make(map[string]struct{})
	for _, e := range opsManager.Spec.Backup.FileSystemStoreConfigs {
		fsStoreNames[e.Name] = struct{}{}
	}
	// count the number of FS snapshots configured in OM and match them with the one in CR.
	countFS := 0

	for _, e := range opsManagefsStoreConfigs {
		if _, ok := fsStoreNames[e.Id]; ok {
			countFS++
		}
	}

	if countFS != len(opsManager.Spec.Backup.FileSystemStoreConfigs) {
		return workflow.Failed(xerrors.Errorf("Not all fileSystem snapshots have been configured in OM."))
	}
	return workflow.OK()
}

// shouldUseAppDb accepts an S3Config and returns true if the AppDB should be used
// for this S3 configuration. Otherwise, a MongoDB resource is configured for use.
func shouldUseAppDb(config omv1.S3Config) bool {
	return config.MongoDBResourceRef == nil
}

// buildAppDbOMS3Config creates a backup.S3Config which is configured to use The AppDb.
func (r *OpsManagerReconciler) buildAppDbOMS3Config(om omv1.MongoDBOpsManager, config omv1.S3Config,
	log *zap.SugaredLogger) (backup.S3Config, workflow.Status) {

	password, err := r.getAppDBPassword(om, log)
	if err != nil {
		return backup.S3Config{}, workflow.Failed(err)
	}
	var s3Creds *backup.S3Credentials

	if !config.IRSAEnabled {
		s3Creds, err = r.readS3Credentials(config.S3SecretRef.Name, om.Namespace)
		if err != nil {
			return backup.S3Config{}, workflow.Failed(err)
		}
	}

	uri := buildMongoConnectionUrl(om, password)

	bucket := backup.S3Bucket{
		Endpoint: config.S3BucketEndpoint,
		Name:     config.S3BucketName,
	}

	customCAOpts, err := r.readCustomCAFilePathsAndContents(om)
	if err != nil {
		return backup.S3Config{}, workflow.Failed(err)
	}

	return backup.NewS3Config(om, config, uri, customCAOpts, bucket, s3Creds), workflow.OK()
}

// buildMongoDbOMS3Config creates a backup.S3Config which is configured to use a referenced
// MongoDB resource.
func (r *OpsManagerReconciler) buildMongoDbOMS3Config(opsManager omv1.MongoDBOpsManager, config omv1.S3Config) (backup.S3Config, workflow.Status) {
	mongodb, status := r.getMongoDbForS3Config(opsManager, config)
	if !status.IsOK() {
		return backup.S3Config{}, status
	}

	if status := validateS3Config(mongodb.GetAuthenticationModes(), mongodb.GetResourceName(), config); !status.IsOK() {
		return backup.S3Config{}, status
	}

	userName, password, status := r.getS3MongoDbUserNameAndPassword(mongodb.GetAuthenticationModes(), opsManager.Namespace, config)
	if !status.IsOK() {
		return backup.S3Config{}, status
	}

	var s3Creds *backup.S3Credentials
	var err error

	if !config.IRSAEnabled {
		s3Creds, err = r.readS3Credentials(config.S3SecretRef.Name, opsManager.Namespace)
		if err != nil {
			return backup.S3Config{}, workflow.Failed(err)
		}
	}

	uri := mongodb.BuildConnectionString(userName, password, connectionstring.SchemeMongoDB, map[string]string{})

	bucket := backup.S3Bucket{
		Endpoint: config.S3BucketEndpoint,
		Name:     config.S3BucketName,
	}

	customCAOpts, err := r.readCustomCAFilePathsAndContents(opsManager)
	if err != nil {
		return backup.S3Config{}, workflow.Failed(err)
	}

	return backup.NewS3Config(opsManager, config, uri, customCAOpts, bucket, s3Creds), workflow.OK()
}

// readCustomCAFilePathsAndContents returns the filepath and contents of the custom CA which is used to configure
// the S3Store.
func (r *OpsManagerReconciler) readCustomCAFilePathsAndContents(opsManager omv1.MongoDBOpsManager) (backup.S3CustomCertificate, error) {
	if opsManager.Spec.GetAppDbCA() != "" {
		filePath := util.AppDBMmsCaFileDirInContainer + "ca-pem"
		cmContents, err := configmap.ReadKey(r.client, "ca-pem", kube.ObjectKey(opsManager.Namespace, opsManager.Spec.GetAppDbCA()))
		if err != nil {
			return backup.S3CustomCertificate{}, err

		}
		return backup.S3CustomCertificate{
			Filename:   filePath,
			CertString: cmContents,
		}, nil
	}
	return backup.S3CustomCertificate{}, nil
}

// buildOMS3Config builds the OM API S3 config from the Operator OM CR configuration. This involves some logic to
// get the mongo URI which points to either the external resource or to the AppDB
func (r *OpsManagerReconciler) buildOMS3Config(opsManager omv1.MongoDBOpsManager, config omv1.S3Config,
	log *zap.SugaredLogger) (backup.S3Config, workflow.Status) {
	if shouldUseAppDb(config) {
		return r.buildAppDbOMS3Config(opsManager, config, log)
	}
	return r.buildMongoDbOMS3Config(opsManager, config)
}

// getMongoDbForS3Config returns the referenced MongoDB resource which should be used when configuring the backup config.
func (r *OpsManagerReconciler) getMongoDbForS3Config(opsManager omv1.MongoDBOpsManager, config omv1.S3Config) (S3ConfigGetter, workflow.Status) {
	mongodb, mongodbMulti := &mdbv1.MongoDB{}, &mdbmulti.MongoDBMultiCluster{}
	mongodbObjectKey := config.MongodbResourceObjectKey(opsManager)

	err := r.client.Get(context.TODO(), mongodbObjectKey, mongodb)
	if err != nil {
		if secrets.SecretNotExist(err) {

			// try to fetch mongodbMulti if it exists
			err = r.client.Get(context.TODO(), mongodbObjectKey, mongodbMulti)
			if err != nil {
				if secrets.SecretNotExist(err) {
					// Returning pending as the user may create the mongodb resource soon
					return nil, workflow.Pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
				}
				return nil, workflow.Failed(err)
			}
			return mongodbMulti, workflow.OK()
		}

		return nil, workflow.Failed(err)
	}

	return mongodb, workflow.OK()
}

// getS3MongoDbUserNameAndPassword returns userName and password if MongoDB resource has scram-sha enabled.
// Note, that we don't worry if the 'mongodbUserRef' is specified but SCRAM-SHA is not enabled - we just ignore the
// user.
func (r *OpsManagerReconciler) getS3MongoDbUserNameAndPassword(modes []string, namespace string, config omv1.S3Config) (string, string, workflow.Status) {
	if !stringutil.Contains(modes, util.SCRAM) {
		return "", "", workflow.OK()
	}
	mongodbUser := &user.MongoDBUser{}
	mongodbUserObjectKey := config.MongodbUserObjectKey(namespace)
	err := r.client.Get(context.TODO(), mongodbUserObjectKey, mongodbUser)
	if secrets.SecretNotExist(err) {
		return "", "", workflow.Pending("The MongoDBUser object %s doesn't exist", mongodbUserObjectKey)
	}
	if err != nil {
		return "", "", workflow.Failed(xerrors.Errorf("Failed to fetch the user %s: %w", mongodbUserObjectKey, err))
	}
	userName := mongodbUser.Spec.Username
	password, err := mongodbUser.GetPassword(r.SecretClient)
	if err != nil {
		return "", "", workflow.Failed(xerrors.Errorf("Failed to read password for the user %s: %w", mongodbUserObjectKey, err))
	}
	return userName, password, workflow.OK()
}

// buildOMDatastoreConfig builds the OM API datastore config based on the Kubernetes OM resource one.
// To do this it may need to read the Mongodb User and its password to build mongodb url correctly
func (r *OpsManagerReconciler) buildOMDatastoreConfig(opsManager omv1.MongoDBOpsManager, operatorConfig omv1.DataStoreConfig) (backup.DataStoreConfig, workflow.Status) {
	mongodb := &mdbv1.MongoDB{}
	mongodbObjectKey := operatorConfig.MongodbResourceObjectKey(opsManager.Namespace)

	err := r.client.Get(context.TODO(), mongodbObjectKey, mongodb)
	if err != nil {
		if secrets.SecretNotExist(err) {
			// Returning pending as the user may create the mongodb resource soon
			return backup.DataStoreConfig{}, workflow.Pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
		}
		return backup.DataStoreConfig{}, workflow.Failed(err)
	}

	status := validateDataStoreConfig(mongodb.Spec.Security.Authentication.GetModes(), mongodb.Name, operatorConfig)
	if !status.IsOK() {
		return backup.DataStoreConfig{}, status
	}

	// If MongoDB resource has scram-sha enabled then we need to read the username and the password.
	// Note, that we don't worry if the 'mongodbUserRef' is specified but SCRAM-SHA is not enabled - we just ignore the
	// user
	var userName, password string
	if stringutil.Contains(mongodb.Spec.Security.Authentication.GetModes(), util.SCRAM) {
		mongodbUser := &user.MongoDBUser{}
		mongodbUserObjectKey := operatorConfig.MongodbUserObjectKey(opsManager.Namespace)
		err := r.client.Get(context.TODO(), mongodbUserObjectKey, mongodbUser)
		if secrets.SecretNotExist(err) {
			return backup.DataStoreConfig{}, workflow.Pending("The MongoDBUser object %s doesn't exist", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace))
		}
		if err != nil {
			return backup.DataStoreConfig{}, workflow.Failed(xerrors.Errorf("Failed to fetch the user %s: %w", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace), err))
		}
		userName = mongodbUser.Spec.Username
		password, err = mongodbUser.GetPassword(r.SecretClient)
		if err != nil {
			return backup.DataStoreConfig{}, workflow.Failed(xerrors.Errorf("Failed to read password for the user %s: %w", mongodbUserObjectKey, err))
		}
	}

	tls := mongodb.Spec.Security.TLSConfig.Enabled
	mongoUri := mongodb.BuildConnectionString(userName, password, connectionstring.SchemeMongoDB, map[string]string{})
	return backup.NewDataStoreConfig(operatorConfig.Name, mongoUri, tls, operatorConfig.AssignmentLabels), workflow.OK()
}

func validateS3Config(modes []string, mdbName string, s3Config omv1.S3Config) workflow.Status {
	return validateConfig(modes, mdbName, s3Config.MongoDBUserRef, "S3 metadata database")
}

func validateDataStoreConfig(modes []string, mdbName string, dataStoreConfig omv1.DataStoreConfig) workflow.Status {
	return validateConfig(modes, mdbName, dataStoreConfig.MongoDBUserRef, "Oplog/Blockstore databases")
}

func validateConfig(modes []string, mdbName string, userRef *omv1.MongoDBUserRef, description string) workflow.Status {
	// validate
	if !stringutil.Contains(modes, util.SCRAM) &&
		len(modes) > 0 {
		return workflow.Failed(xerrors.Errorf("The only authentication mode supported for the %s is SCRAM-SHA", description))
	}
	if stringutil.Contains(modes, util.SCRAM) &&
		(userRef == nil || userRef.Name == "") {
		return workflow.Failed(xerrors.Errorf("MongoDB resource %s is configured to use SCRAM-SHA authentication mode, the user must be"+
			" specified using 'mongodbUserRef'", mdbName))
	}

	return workflow.OK()
}

func newUserFromSecret(data map[string]string) (api.User, error) {
	// validate
	for _, v := range []string{"Username", "Password", "FirstName", "LastName"} {
		if _, ok := data[v]; !ok {
			return api.User{}, xerrors.Errorf("%s property is missing in the admin secret", v)
		}
	}
	user := api.User{Username: data["Username"],
		Password:  data["Password"],
		FirstName: data["FirstName"],
		LastName:  data["LastName"],
	}
	return user, nil
}

// delete cleans up Ops Manager related resources on CR removal.
func (r *OpsManagerReconciler) delete(obj interface{}, log *zap.SugaredLogger) {
	opsManager := obj.(*omv1.MongoDBOpsManager)

	r.RemoveAllDependentWatchedResources(opsManager.Namespace, kube.ObjectKeyFromApiObject(opsManager))
	r.RemoveDependentWatchedResources(opsManager.AppDBStatefulSetObjectKey())

	log.Info("Cleaned up Ops Manager related resources.")
}

// getAnnotationsForOpsManagerResource returns all of the annotations that should be applied to the resource
// at the end of the reconciliation.
func getAnnotationsForOpsManagerResource(opsManager *omv1.MongoDBOpsManager) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(opsManager.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)
	return finalAnnotations, nil
}
