package operator

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/user"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/project"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/generate"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/identifiable"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/blang/semver"
	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type OpsManagerReconciler struct {
	*ReconcileCommonController
	omInitializer            api.Initializer
	omAdminProvider          api.AdminProvider
	appDbVersionManifestPath string
}

var _ reconcile.Reconciler = &OpsManagerReconciler{}

func newOpsManagerReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, initializer api.Initializer, adminProvider api.AdminProvider, appDbVersionManifestPath string) *OpsManagerReconciler {
	return &OpsManagerReconciler{
		ReconcileCommonController: newReconcileCommonController(mgr, omFunc),
		omInitializer:             initializer,
		omAdminProvider:           adminProvider,
		appDbVersionManifestPath:  appDbVersionManifestPath,
	}
}

// Reconcile performs the reconciliation logic for AppDB, Ops Manager and Backup
// AppDB is reconciled first (independent from Ops Manager as the agent is run in headless mode) and
// Ops Manager statefulset is created then.
// Backup daemon statefulset is created/updated and configured optionally if backup is enabled.
// Note, that the pointer to ops manager resource is used in 'Reconcile' method as resource status is mutated
// many times during reconciliation and its important to keep updates to avoid status override
func (r *OpsManagerReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("OpsManager", request.NamespacedName)

	opsManager := &omv1.MongoDBOpsManager{}

	opsManagerExtraStatusParams := mdbstatus.NewOMPartOption(mdbstatus.OpsManager)
	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatus(opsManager, workflow.Failed("Failed to reconcile Ops Manager: %s", err), log, opsManagerExtraStatusParams)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	if reconcileResult, err := r.readOpsManagerResource(request, opsManager, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> OpsManager.Reconcile")
	log.Infow("OpsManager.Spec", "spec", opsManager.Spec)
	log.Infow("OpsManager.Status", "status", opsManager.Status)

	// register backup
	r.watchMongoDBResourcesReferencedByBackup(*opsManager)

	if err := opsManager.ProcessValidationsOnReconcile(); err != nil {
		return r.updateStatus(opsManager, workflow.Invalid(err.Error()), log, opsManagerExtraStatusParams)
	}

	// TODO move to validation web hook
	if err := performValidation(*opsManager); err != nil {
		return r.updateStatus(opsManager, workflow.Invalid(err.Error()), log, opsManagerExtraStatusParams)
	}

	opsManagerUserPassword, err := r.getAppDBPassword(*opsManager, log)

	if err != nil {
		return r.updateStatus(opsManager, workflow.Failed("Error ensuring Ops Manager user password: %s", err), log, opsManagerExtraStatusParams)
	}

	// 1. Reconcile AppDB
	emptyResult := reconcile.Result{}
	retryResult := reconcile.Result{Requeue: true}
	appDbReconciler := newAppDBReplicaSetReconciler(r.ReconcileCommonController, r.appDbVersionManifestPath)
	result, err := appDbReconciler.Reconcile(opsManager, opsManager.Spec.AppDB, opsManagerUserPassword)
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
	if opsManager.Spec.Backup.Enabled {
		if status = r.reconcileBackupDaemon(opsManager, omAdmin, opsManagerUserPassword, log); !status.IsOK() {
			return r.updateStatus(opsManager, status, log, mdbstatus.NewOMPartOption(mdbstatus.Backup))
		}
	}

	// All statuses are updated by now - we don't need to update any others - just return
	log.Info("Finished reconciliation for MongoDbOpsManager!")
	// success
	return reconcile.Result{}, nil
}

func (r *OpsManagerReconciler) reconcileOpsManager(opsManager *omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) (workflow.Status, api.Admin) {
	statusOptions := []mdbstatus.Option{mdbstatus.NewOMPartOption(mdbstatus.OpsManager), mdbstatus.NewBaseUrlOption(opsManager.CentralURL())}

	_, err := r.updateStatus(opsManager, workflow.Reconciling(), log, statusOptions...)
	if err != nil {
		return workflow.Failed(err.Error()), nil
	}

	// Prepare Ops Manager StatefulSet (create and wait)
	status := r.createOpsManagerStatefulset(*opsManager, opsManagerUserPassword, log)
	if !status.IsOK() {
		return status, nil
	}

	// 3. Prepare Ops Manager (ensure the first user is created and public API key saved to secret)
	var omAdmin api.Admin
	if status, omAdmin = r.prepareOpsManager(*opsManager, log); !status.IsOK() {
		return status, nil
	}

	// 4. Trigger agents upgrade if necessary
	if err = triggerOmChangedEventIfNeeded(*opsManager, log); err != nil {
		log.Warn("Not triggering an Ops Manager version changed event: %s", err)
	}

	if _, err = r.updateStatus(opsManager, workflow.OK(), log, statusOptions...); err != nil {
		return workflow.Failed(err.Error()), nil
	}

	return status, omAdmin
}

// triggerOmChangedEventIfNeeded triggers upgrade process for all the MongoDB agents in the system if the major/minor version upgrade
// happened for Ops Manager
func triggerOmChangedEventIfNeeded(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	if opsManager.Spec.Version == opsManager.Status.OpsManagerStatus.Version || opsManager.Status.OpsManagerStatus.Version == "" {
		return nil
	}
	newVersion, err := semver.Parse(opsManager.Spec.Version)
	if err != nil {
		return fmt.Errorf("Failed to parse Ops Manager version %s: %s", opsManager.Spec.Version, err)
	}
	oldVersion, err := semver.Parse(opsManager.Status.OpsManagerStatus.Version)
	if err != nil {
		return fmt.Errorf("Failed to parse Ops Manager status version %s: %s", opsManager.Status.OpsManagerStatus.Version, err)
	}
	if newVersion.Major != oldVersion.Major || newVersion.Minor != oldVersion.Minor {
		log.Infof("Ops Manager version has upgraded from %s to %s - scheduling the upgrade for all the Agents in the system", oldVersion, newVersion)
		agents.ScheduleUpgrade()
	}
	return nil
}

func (r *OpsManagerReconciler) reconcileBackupDaemon(opsManager *omv1.MongoDBOpsManager, omAdmin api.Admin, opsManagerUserPassword string, log *zap.SugaredLogger) workflow.Status {
	backupStatusPartOption := mdbstatus.NewOMPartOption(mdbstatus.Backup)

	_, err := r.updateStatus(opsManager, workflow.Reconciling(), log, backupStatusPartOption)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	// Prepare Backup Daemon StatefulSet (create and wait)
	if status := r.createBackupDaemonStatefulset(*opsManager, opsManagerUserPassword, log); !status.IsOK() {
		return status
	}

	// Configure Backup using API
	if status := r.prepareBackupInOpsManager(*opsManager, omAdmin, log); !status.IsOK() {
		return status
	}

	if _, err := r.updateStatus(opsManager, workflow.OK(), log, backupStatusPartOption); err != nil {
		return workflow.Failed(err.Error())
	}

	return workflow.OK()
}

// readOpsManagerResource reads Ops Manager Custom resource into pointer provided
func (r *OpsManagerReconciler) readOpsManagerResource(request reconcile.Request, ref *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (*reconcile.Result, error) {
	if result, err := r.getResource(request, ref, log); result != nil {
		return result, err
	}
	// Reset warnings so that they are not stale, will populate accurate warnings in reconciliation
	ref.SetWarnings([]mdbstatus.Warning{})
	return nil, nil
}

// ensureAppDBConnectionString ensures that the AppDB Connection String exists in a secret.
func (r *OpsManagerReconciler) ensureAppDBConnectionString(opsManager omv1.MongoDBOpsManager, computedConnectionString string, log *zap.SugaredLogger) error {
	connectionStringSecret, err := r.kubeHelper.client.GetSecret(kube.ObjectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()))

	if err != nil {
		if apiErrors.IsNotFound(err) {
			log.Debugf("AppDB connection string secret was not found, creating %s now", objectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()))
			// assume the secret was not found, need to create it

			connectionStringSecret = secret.Builder().
				SetName(opsManager.AppDBMongoConnectionStringSecretName()).
				SetNamespace(opsManager.Namespace).
				SetField(util.AppDbConnectionStringKey, computedConnectionString).
				Build()

			return r.kubeHelper.client.CreateSecret(connectionStringSecret)
		}
		log.Warnf("Error getting connection string secret: %s", err)
		return err
	}
	connectionStringSecret.StringData = map[string]string{
		util.AppDbConnectionStringKey: computedConnectionString,
	}
	log.Debugf("Connection string secret already exists, updating %s", objectKey(opsManager.Namespace, opsManager.AppDBMongoConnectionStringSecretName()))
	return r.kubeHelper.client.UpdateSecret(connectionStringSecret)
}

func hashConnectionString(connectionString string) string {
	bytes := []byte(connectionString)
	hashBytes := sha256.Sum256(bytes)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:])
}

// createOpsManagerStatefulset ensures the gen key secret exists and creates the Ops Manager StatefulSet.
func (r *OpsManagerReconciler) createOpsManagerStatefulset(opsManager omv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) workflow.Status {
	if err := r.ensureGenKey(opsManager, log); err != nil {
		return workflow.Failed(err.Error())
	}

	connectionString := buildMongoConnectionUrl(opsManager, opsManagerUserPassword)
	if err := r.ensureAppDBConnectionString(opsManager, connectionString, log); err != nil {
		return workflow.Failed(err.Error())
	}

	r.ensureConfiguration(&opsManager, log)

	helper := r.kubeHelper.NewOpsManagerStatefulSetHelper(opsManager).SetLogger(log).SetAppDBConnectionStringHash(hashConnectionString(connectionString))
	if opsManager.Annotations != nil {
		helper.SetAnnotations(opsManager.Annotations)
	}
	if err := helper.CreateOrUpdateInKubernetes(); err != nil {
		return workflow.Failed(err.Error())
	}

	if status := r.getStatefulSetStatus(opsManager.Namespace, opsManager.Name); !status.IsOK() {
		return status
	}

	return workflow.OK()
}

func AddOpsManagerController(mgr manager.Manager) error {
	reconciler := newOpsManagerReconciler(mgr, om.NewOpsManagerConnection, &api.DefaultInitializer{}, api.NewOmAdmin, util.VersionManifestFilePath)
	c, err := controller.New(util.MongoDbOpsManagerController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	// watch for changes to the Ops Manager resources
	if err = c.Watch(&source.Kind{Type: &omv1.MongoDBOpsManager{}}, &handler.EnqueueRequestForObject{}, watch.PredicatesForOpsManager()); err != nil {
		return err
	}

	// watch the secret with the Ops Manager user password
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &mdbv1.MongoDB{}},
		&watch.ResourcesHandler{ResourceType: watch.MongoDB, TrackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbOpsManagerController)
	return nil
}

// ensureConfiguration makes sure the mandatory configuration is specified.
func (r OpsManagerReconciler) ensureConfiguration(opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) {
	// update the central URL
	setConfigProperty(opsManager, util.MmsCentralUrlPropKey, opsManager.CentralURL(), log)

	tlsConfig := opsManager.Spec.AppDB.Security.TLSConfig
	if tlsConfig != nil && tlsConfig.SecretRef.Name != "" {
		setConfigProperty(opsManager, util.MmsMongoSSL, "true", log)
	}
	if tlsConfig != nil && tlsConfig.CA != "" {
		setConfigProperty(opsManager, util.MmsMongoCA, util.MmsCaFileDirInContainer+"ca-pem", log)
	}

	// override the versions directory (defaults to "/opt/mongodb/mms/mongodb-releases/")
	setConfigProperty(opsManager, util.MmsVersionsDirectory, "/mongodb-ops-manager/mongodb-releases/", log)

	// feature controls will always be enabled
	setConfigProperty(opsManager, util.MmsFeatureControls, "true", log)
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
		return workflow.Failed(err.Error())
	}

	r.ensureConfiguration(&opsManager, log)

	backupHelper := r.kubeHelper.NewBackupStatefulSetHelper(opsManager)
	backupHelper.OpsManagerStatefulSetHelper.SetAppDBConnectionStringHash(hashConnectionString(connectionString))
	backupHelper.SetLogger(log)

	if err := backupHelper.CreateOrUpdateInKubernetes(); err != nil {
		return workflow.Failed(err.Error())
	}
	// Note, that this will return true quite soon as we don't have daemon readiness so far
	if status := r.getStatefulSetStatus(opsManager.Namespace, opsManager.BackupStatefulSetName()); !status.IsOK() {
		return status
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) watchMongoDBResourcesReferencedByBackup(opsManager omv1.MongoDBOpsManager) {
	if !opsManager.Spec.Backup.Enabled {
		r.removeWatchedResources(opsManager.Namespace, watch.MongoDB, kube.ObjectKeyFromApiObject(&opsManager))
		return
	}

	// watch mongodb resources for oplog
	oplogs := opsManager.Spec.Backup.OplogStoreConfigs
	for _, oplogConfig := range oplogs {
		r.addWatchedResourceIfNotAdded(
			oplogConfig.MongoDBResourceRef.Name,
			opsManager.Namespace,
			watch.MongoDB,
			kube.ObjectKeyFromApiObject(&opsManager),
		)
	}

	// watch mongodb resources for block stores
	blockstores := opsManager.Spec.Backup.BlockStoreConfigs
	for _, blockStoreConfig := range blockstores {
		r.addWatchedResourceIfNotAdded(
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
			r.addWatchedResourceIfNotAdded(
				s3StoreConfig.MongoDBResourceRef.Name,
				opsManager.Namespace,
				watch.MongoDB,
				kube.ObjectKeyFromApiObject(&opsManager),
			)
		}
	}
}

// buildMongoConnectionUrl builds the connection url to the appdb. Note, that it overrides the default authMechanism
// (which internally depends on the mongodb version)
func buildMongoConnectionUrl(opsManager omv1.MongoDBOpsManager, password string) string {
	return opsManager.Spec.AppDB.ConnectionURL(util.OpsManagerMongoDBUserName, password,
		map[string]string{"authMechanism": "SCRAM-SHA-1"})
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
	objectKey := objectKey(om.Namespace, om.Name+"-gen-key")
	_, err := r.kubeHelper.client.GetSecret(objectKey)

	if apiErrors.IsNotFound(err) {
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

		return r.kubeHelper.client.CreateSecret(genKeySecret)
	}
	return err
}

// getAppDBPassword will return the password that was specified by the user, or the auto generated password stored in
// the secret (generate it and store in secret otherwise)
func (r OpsManagerReconciler) getAppDBPassword(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	passwordRef := opsManager.Spec.AppDB.PasswordSecretKeyRef
	if passwordRef != nil && passwordRef.Name != "" { // there is a secret specified for the Ops Manager user

		password, err := secret.ReadKey(r.kubeHelper.client, passwordRef.Key, kube.ObjectKey(opsManager.Namespace, passwordRef.Name))
		if err != nil {
			return "", err
		}
		log.Debugf("Reading password from secret/%s", passwordRef.Name)

		// watch for any changes on the user provided password
		r.addWatchedResourceIfNotAdded(
			passwordRef.Name,
			opsManager.Namespace,
			watch.Secret,
			kube.ObjectKeyFromApiObject(&opsManager),
		)

		// delete the auto generated password, we don't need it anymore. We can just generate a new one if
		// the user password is deleted
		log.Debugf("Deleting Operator managed password secret/%s from namespace", opsManager.Spec.AppDB.GetSecretName(), opsManager.Namespace)
		if err := r.kubeHelper.client.DeleteSecret(kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())); err != nil && !apiErrors.IsNotFound(err) {
			return "", err
		}

		return password, nil
	}

	// otherwise we'll ensure the auto generated password exists
	secretObjectKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())
	appDbPasswordSecretStringData, err := secret.ReadStringData(r.kubeHelper.client, secretObjectKey)

	if apiErrors.IsNotFound(err) {
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
			SetStringData(passwordData).
			SetOwnerReferences(baseOwnerReference(&opsManager)).
			Build()

		if err := r.kubeHelper.client.CreateSecret(appDbPasswordSecret); err != nil {
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

// prepareOpsManager ensures the admin user is created and the admin public key exists. It returns the instance of
// api.Admin to perform future Ops Manager configuration
// Note the exception handling logic - if the controller fails to save the public API key secret - it cannot fix this
// manually (the first OM user can be created only once) - so the resource goes to Failed state and shows the message
// asking the user to fix this manually.
// Theoretically the Operator could remove the appdb StatefulSet (as the OM must be empty without any user data) and
// allow the db to get recreated but seems this is a quite radical operation.
func (r OpsManagerReconciler) prepareOpsManager(opsManager omv1.MongoDBOpsManager, log *zap.SugaredLogger) (workflow.Status, api.Admin) {
	// We won't support cross-namespace secrets until CLOUDP-46636 is resolved
	adminObjectKey := objectKey(opsManager.Namespace, opsManager.Spec.AdminSecret)

	// 1. Read the admin secret
	userData, err := r.kubeHelper.readSecret(adminObjectKey)

	if apiErrors.IsNotFound(err) {
		// This requires user actions - let's wait a bit longer than 10 seconds
		return workflow.Failed("the secret %s doesn't exist - you need to create it to finish Ops Manager initialization", adminObjectKey).WithRetry(60), nil
	} else if err != nil {
		return workflow.Failed(err.Error()), nil
	}

	user, err := newUserFromSecret(userData)
	if err != nil {
		return workflow.Failed("failed to read user data from the secret %s: %s", adminObjectKey, err), nil
	}

	adminKeySecretName := objectKey(operatorNamespace(), opsManager.APIKeySecretName())
	detailedMsg := fmt.Sprintf("This is a fatal error, as the"+
		" Operator requires public API key for the admin user to exist. Please create the GLOBAL_ADMIN user in "+
		"Ops Manager manually and create a secret '%s' with fields '%s' and '%s'", adminKeySecretName, util.OmPublicApiKey,
		util.OmUser)

	// 2. Create a user in Ops Manager if necessary. Note, that we don't send the request if the API key secret exists.
	// This is because of the weird Ops Manager /unauth endpoint logic: it allows to create any number of users though only
	// the first one will have GLOBAL_ADMIN permission. So we should avoid the situation when the admin changes the
	// user secret and reconciles OM resource and the new user (non admin one) is created overriding the previous API secret
	_, err = r.kubeHelper.readSecret(adminKeySecretName)

	if apiErrors.IsNotFound(err) {
		apiKey, err := r.omInitializer.TryCreateUser(opsManager.CentralURL(), user)
		if err != nil {
			// Will wait more than usual (10 seconds) as most of all the problem needs to get fixed by the user
			// by modifying the credentials secret
			return workflow.Failed("failed to create an admin user in Ops Manager: %s", err).WithRetry(30), nil
		}

		// Recreate an admin key secret in the Operator namespace if the user was created
		if apiKey != "" {
			log.Infof("Created an admin user %s with GLOBAL_ADMIN role", user.Username)

			// The structure matches the structure of a credentials secret used by normal mongodb resources
			secretData := map[string]string{util.OmPublicApiKey: apiKey, util.OmUser: user.Username}

			if err = r.kubeHelper.client.DeleteSecret(adminKeySecretName); err != nil && !apiErrors.IsNotFound(err) {
				// TODO our desired behavior is not to fail but just append the warning to the status (CLOUDP-51340)
				return workflow.Failed("failed to replace a secret for admin public api key. %s. The error : %s",
					detailedMsg, err).WithRetry(300), nil
			}

			adminSecret := secret.Builder().
				SetNamespace(adminKeySecretName.Namespace).
				SetName(adminKeySecretName.Name).
				SetStringData(secretData).
				SetOwnerReferences(baseOwnerReference(&opsManager)).
				SetLabels(map[string]string{}).
				Build()

			if err := r.kubeHelper.client.CreateSecret(adminSecret); err != nil {
				// TODO see above
				return workflow.Failed("failed to create a secret for admin public api key. %s. The error : %s",
					detailedMsg, err).WithRetry(30), nil
			}
			log.Infof("Created a secret for admin public api key %s", adminKeySecretName)

			// Each "read-after-write" operation needs some timeout after write unfortunately :(
			// https://github.com/kubernetes-sigs/controller-runtime/issues/343#issuecomment-468402446
			time.Sleep(time.Duration(envutil.ReadIntOrDefault(util.K8sCacheRefreshEnv, util.DefaultK8sCacheRefreshTimeSeconds)) * time.Second)
		}
	}

	// 3. Final validation of current state - this could be the retry after failing to create the secret during
	// previous reconciliation (and the apiKey is empty as "the first user already exists") - the only fix is
	// to create the secret manually
	_, err = r.kubeHelper.readSecret(adminKeySecretName)
	if err != nil {
		return workflow.Failed("admin API key secret for Ops Manager doesn't exit - was it removed accidentally? %s. The error : %s",
			detailedMsg, err).WithRetry(30), nil
	}
	// Ops Manager api key Secret has the same structure as the MongoDB credentials secret
	cred, err := project.ReadCredentials(r.kubeHelper.client, objectKey(operatorNamespace(), opsManager.APIKeySecretName()))
	if err != nil {
		return workflow.Failed(err.Error()), nil
	}

	return workflow.OK(), r.omAdminProvider(opsManager.CentralURL(), cred.User, cred.PublicAPIKey)
}

// prepareBackupInOpsManager makes the changes to backup admin configuration based on the Ops Manager spec
func (r *OpsManagerReconciler) prepareBackupInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.Admin,
	log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}
	// 1. Enabling Daemon Config if necessary
	backupHostName := opsManager.BackupDaemonHostName()
	_, err := omAdmin.ReadDaemonConfig(backupHostName, util.PvcMountPathHeadDb)
	if api.NewErrorNonNil(err).ErrorCode == api.BackupDaemonConfigNotFound {
		log.Infow("Backup Daemon is not configured, enabling it", "hostname", backupHostName, "headDB", util.PvcMountPathHeadDb)

		err = omAdmin.CreateDaemonConfig(backupHostName, util.PvcMountPathHeadDb)
		if api.NewErrorNonNil(err).ErrorCode == api.BackupDaemonConfigNotFound {
			// Unfortunately by this time backup daemon may not have been started yet and we don't have proper
			// mechanism to ensure this using readiness probe so we just retry
			return workflow.Pending("BackupDaemon hasn't started yet")
		} else if err != nil {
			return workflow.Failed(err.Error())
		}
	} else if err != nil {
		return workflow.Failed(err.Error())
	}

	// 2. Oplog store configs
	status := r.ensureOplogStoresInOpsManager(opsManager, omAdmin, log)

	// 3. S3 Configs
	status = status.Merge(r.ensureS3ConfigurationInOpsManager(opsManager, omAdmin, log))

	// 4. Block store configs
	status = status.Merge(r.ensureBlockStoresInOpsManager(opsManager, omAdmin, log))

	if len(opsManager.Spec.Backup.S3Configs) == 0 && len(opsManager.Spec.Backup.BlockStoreConfigs) == 0 {
		return status.Merge(workflow.Invalid("Either S3 or Blockstore Snapshot configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending))
	}

	return status
}

// ensureOplogStoresInOpsManager aligns the oplog stores in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureOplogStoresInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.Admin, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerOplogConfigs, err := omAdmin.ReadOplogStoreConfigs()
	if err != nil {
		return workflow.Failed(err.Error())
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
			return workflow.Failed(err.Error())
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
			return workflow.Failed(err.Error())
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerOplogConfigs, opsManager.Spec.Backup.OplogStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Oplog Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteOplogStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(err.Error())
		}
	}

	if len(operatorOplogConfigs) == 0 {
		return workflow.Invalid("Oplog Store configuration is required for backup").WithTargetPhase(mdbstatus.PhasePending)
	}
	return workflow.OK()
}

// ensureBlockStoresInOpsManager aligns the blockStore configs in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureBlockStoresInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.Admin, log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerBlockStoreConfigs, err := omAdmin.ReadBlockStoreConfigs()
	if err != nil {
		return workflow.Failed(err.Error())
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
			return workflow.Failed(err.Error())
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
			return workflow.Failed(err.Error())
		}
	}

	// Removing non-existing configs
	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerBlockStoreConfigs, opsManager.Spec.Backup.BlockStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Block Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteBlockStoreConfig(v.Identifier().(string)); err != nil {
			return workflow.Failed(err.Error())
		}
	}
	return workflow.OK()
}

func (r *OpsManagerReconciler) ensureS3ConfigurationInOpsManager(opsManager omv1.MongoDBOpsManager, omAdmin api.Admin,
	log *zap.SugaredLogger) workflow.Status {
	if !opsManager.Spec.Backup.Enabled {
		return workflow.OK()
	}

	opsManagerS3Configs, err := omAdmin.ReadS3Configs()
	if err != nil {
		return workflow.Failed(err.Error())
	}

	operatorS3Configs := opsManager.Spec.Backup.S3Configs
	configsToCreate := identifiable.SetDifferenceGeneric(operatorS3Configs, opsManagerS3Configs)
	for _, config := range configsToCreate {
		omConfig, status := r.buildOMS3Config(opsManager, config.(omv1.S3Config), log)
		if !status.IsOK() {
			return status
		}

		log.Debugw("Creating S3Config in Ops Manager", "config", omConfig)
		if err := omAdmin.CreateS3Config(omConfig); err != nil {
			return workflow.Failed(err.Error())
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
		log.Debugw("Updating S3Config in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateS3Config(configToUpdate); err != nil {
			return workflow.Failed(err.Error())
		}
	}

	configsToRemove := identifiable.SetDifferenceGeneric(opsManagerS3Configs, operatorS3Configs)
	for _, config := range configsToRemove {
		log.Debugf("Removing S3Config %s from Ops Manager", config.Identifier())
		if err := omAdmin.DeleteS3Config(config.Identifier().(string)); err != nil {
			return workflow.Failed(err.Error())
		}
	}

	return workflow.OK()
}

// readS3Credentials reads the access and secret keys from the awsCredentials secret specified
// in the resource
func (r *OpsManagerReconciler) readS3Credentials(s3SecretName, namespace string) (*backup.S3Credentials, error) {
	s3SecretData, err := r.kubeHelper.readSecret(objectKey(namespace, s3SecretName))
	if err != nil {
		return nil, err
	}

	s3Creds := &backup.S3Credentials{}
	if accessKey, ok := s3SecretData[util.S3AccessKey]; !ok {
		return nil, fmt.Errorf("key %s was not present in the secret %s", util.S3AccessKey, s3SecretName)
	} else {
		s3Creds.AccessKey = accessKey
	}

	if secretKey, ok := s3SecretData[util.S3SecretKey]; !ok {
		return nil, fmt.Errorf("key %s was not present in the secret %s", util.S3SecretKey, s3SecretName)
	} else {
		s3Creds.SecretKey = secretKey
	}

	return s3Creds, nil
}

// buildOMS3Config builds the OM API S3 config from the Operator OM CR configuration. This involves some logic to
// get the mongo URI which points to either the external resource or to the AppDB
func (r *OpsManagerReconciler) buildOMS3Config(opsManager omv1.MongoDBOpsManager, config omv1.S3Config,
	log *zap.SugaredLogger) (backup.S3Config, workflow.Status) {
	mongodb, status := r.getMongoDbForS3Config(opsManager, config)
	if !status.IsOK() {
		return backup.S3Config{}, status
	}

	isAppDB := config.MongoDBResourceRef == nil

	if !isAppDB {
		if status = validateS3Config(mongodb, config); !status.IsOK() {
			return backup.S3Config{}, status
		}
	}

	userName, password, status := r.getS3MongoDbUserNameAndPassword(mongodb, opsManager, config, log)
	if !status.IsOK() {
		return backup.S3Config{}, status
	}

	s3Creds, err := r.readS3Credentials(config.S3SecretRef.Name, opsManager.Namespace)
	if err != nil {
		return backup.S3Config{}, workflow.Failed(err.Error())
	}

	var uri string
	if isAppDB {
		uri = buildMongoConnectionUrl(opsManager, password)
	} else {
		uri = mongodb.ConnectionURL(userName, password, map[string]string{})
	}

	bucket := backup.S3Bucket{
		Endpoint: config.S3BucketEndpoint,
		Name:     config.S3BucketName,
	}

	return backup.NewS3Config(config.Name, uri, bucket, *s3Creds), workflow.OK()
}

func (r *OpsManagerReconciler) getMongoDbForS3Config(opsManager omv1.MongoDBOpsManager, config omv1.S3Config) (mdbv1.MongoDB, workflow.Status) {
	if config.MongoDBResourceRef == nil {
		// having no mongodb reference means the AppDB should be used as a metadata storage
		// We need to build a fake MongoDB resource
		return mdbv1.MongoDB{Spec: opsManager.Spec.AppDB.MongoDbSpec}, workflow.OK()
	}
	mongodb := &mdbv1.MongoDB{}
	mongodbObjectKey := config.MongodbResourceObjectKey(opsManager)
	err := r.client.Get(context.TODO(), mongodbObjectKey, mongodb)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Returning pending as the user may create the mongodb resource soon
			return mdbv1.MongoDB{}, workflow.Pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
		}
		return mdbv1.MongoDB{}, workflow.Failed(err.Error())
	}
	return *mongodb, workflow.OK()
}

// getS3MongoDbUserNameAndPassword returns userName and password if MongoDB resource has scram-sha enabled.
// Note, that we don't worry if the 'mongodbUserRef' is specified but SCRAM-SHA is not enabled - we just ignore the
// user
func (r *OpsManagerReconciler) getS3MongoDbUserNameAndPassword(mongodb mdbv1.MongoDB, opsManager omv1.MongoDBOpsManager, config omv1.S3Config, log *zap.SugaredLogger) (string, string, workflow.Status) {
	if !stringutil.Contains(mongodb.Spec.Security.Authentication.GetModes(), util.SCRAM) {
		return "", "", workflow.OK()
	}
	// If the resource is empty then we need to consider AppDB credentials
	if config.MongoDBResourceRef == nil {
		password, err := r.getAppDBPassword(opsManager, log)
		if err != nil {
			return "", "", workflow.Failed(err.Error())
		}
		return util.OpsManagerMongoDBUserName, password, workflow.OK()
	}
	// Otherwise we are fetching the MongoDBUser entity and a related password
	mongodbUser := &user.MongoDBUser{}
	mongodbUserObjectKey := config.MongodbUserObjectKey(opsManager.Namespace)
	err := r.client.Get(context.TODO(), mongodbUserObjectKey, mongodbUser)
	if apiErrors.IsNotFound(err) {
		return "", "", workflow.Pending("The MongoDBUser object %s doesn't exist", mongodbUserObjectKey)
	}
	if err != nil {
		return "", "", workflow.Failed("Failed to fetch the user %s: %s", mongodbUserObjectKey, err)
	}
	userName := mongodbUser.Spec.Username
	password, err := mongodbUser.GetPassword(r.client)
	if err != nil {
		return "", "", workflow.Failed("Failed to read password for the user %s: %s", mongodbUserObjectKey, err)
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
		if apiErrors.IsNotFound(err) {
			// Returning pending as the user may create the mongodb resource soon
			return backup.DataStoreConfig{}, workflow.Pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
		}
		return backup.DataStoreConfig{}, workflow.Failed(err.Error())
	}

	status := validateDataStoreConfig(*mongodb, operatorConfig)
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
		if apiErrors.IsNotFound(err) {
			return backup.DataStoreConfig{}, workflow.Pending("The MongoDBUser object %s doesn't exist", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace))
		}
		if err != nil {
			return backup.DataStoreConfig{}, workflow.Failed("Failed to fetch the user %s: %s", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace), err)
		}
		userName = mongodbUser.Spec.Username
		password, err = mongodbUser.GetPassword(r.client)
		if err != nil {
			return backup.DataStoreConfig{}, workflow.Failed("Failed to read password for the user %s: %s", mongodbUserObjectKey, err)
		}
	}

	tls := mongodb.Spec.Security.TLSConfig.Enabled
	mongoUri := mongodb.ConnectionURL(userName, password, map[string]string{})
	return backup.NewDataStoreConfig(operatorConfig.Name, mongoUri, tls), workflow.OK()
}

func validateS3Config(mongodb mdbv1.MongoDB, s3Config omv1.S3Config) workflow.Status {
	return validateConfig(mongodb, s3Config.MongoDBUserRef, "S3 metadata database")
}

func validateDataStoreConfig(mongodb mdbv1.MongoDB, dataStoreConfig omv1.DataStoreConfig) workflow.Status {
	return validateConfig(mongodb, dataStoreConfig.MongoDBUserRef, "Oplog/Blockstore databases")
}

func validateConfig(mongodb mdbv1.MongoDB, userRef *omv1.MongoDBUserRef, description string) workflow.Status {
	// validate
	if !stringutil.Contains(mongodb.Spec.Security.Authentication.GetModes(), util.SCRAM) &&
		len(mongodb.Spec.Security.Authentication.GetModes()) > 0 {
		return workflow.Failed("The only authentication mode supported for the %s is SCRAM-SHA", description)
	}
	if stringutil.Contains(mongodb.Spec.Security.Authentication.GetModes(), util.SCRAM) &&
		(userRef == nil || userRef.Name == "") {
		return workflow.Failed("MongoDB resource %s is configured to use SCRAM-SHA authentication mode, the user must be"+
			" specified using 'mongodbUserRef'", mongodb.Name)
	}
	comparison, err := util.CompareVersions(mongodb.Spec.GetVersion(), util.MinimumScramSha256MdbVersion)
	if err != nil {
		return workflow.Failed(err.Error())
	}
	if stringutil.Contains(mongodb.Spec.Security.Authentication.GetModes(), util.SCRAM) && comparison >= 0 {
		return workflow.Failed("The %s with SCRAM-SHA enabled must have version less than 4.0.0", description)
	}
	return workflow.OK()
}

// performValidation makes some validation of Ops Manager spec. So far this validation mostly follows the restrictions
// for the app db in ops manager, see MongoConnectionConfigurationCheck
// Ideally it must be done in an admission web hook
func performValidation(opsManager omv1.MongoDBOpsManager) error {
	version := opsManager.Spec.AppDB.GetVersion()
	v, err := semver.Make(version)
	if err != nil {
		return fmt.Errorf("version %s has wrong format!", version)
	}
	fourZero, _ := semver.Make("4.0.0")
	if v.LT(fourZero) {
		return errors.New("the version of Application Database must be >= 4.0")
	}

	return nil
}

func newUserFromSecret(data map[string]string) (*api.User, error) {
	// validate
	for _, v := range []string{"Username", "Password", "FirstName", "LastName"} {
		if _, ok := data[v]; !ok {
			return nil, fmt.Errorf("%s property is missing in the admin secret", v)
		}
	}
	user := &api.User{Username: data["Username"],
		Password:  data["Password"],
		FirstName: data["FirstName"],
		LastName:  data["LastName"],
	}
	return user, nil
}
