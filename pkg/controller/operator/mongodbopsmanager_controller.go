package operator

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
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
	omInitializer   api.Initializer
	omAdminProvider api.AdminProvider
}

var _ reconcile.Reconciler = &OpsManagerReconciler{}

func newOpsManagerReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, initializer api.Initializer, adminProvider api.AdminProvider) *OpsManagerReconciler {
	return &OpsManagerReconciler{
		ReconcileCommonController: newReconcileCommonController(mgr, omFunc),
		omInitializer:             initializer,
		omAdminProvider:           adminProvider,
	}
}

// Reconcile performs the reconciliation logic for AppDB and Ops Manager
// AppDB is reconciled first (independent from Ops Manager as the agent is run in headless mode) and
// Ops Manager statefulset is created then.
// Backup daemon statefulset is created/updated and configured optionally if backup is enabled.
func (r *OpsManagerReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("OpsManager", request.NamespacedName)

	opsManagerRef := &mdbv1.MongoDBOpsManager{}

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(opsManagerRef, fmt.Sprintf("Failed to reconcile Ops Manager: %s", err), log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	if reconcileResult, err := r.prepareResourceForReconciliation(request, opsManagerRef, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	opsManager := *opsManagerRef

	log.Info("-> OpsManager.Reconcile")
	log.Infow("OpsManager.Spec", "spec", opsManager.Spec)
	log.Infow("OpsManager.Status", "status", opsManager.Status)

	if err := opsManager.ProcessValidationsOnReconcile(); err != nil {
		return r.updateStatusValidationFailure(&opsManager, err.Error(), log)
	}

	if err := performValidation(opsManager); err != nil {
		return r.updateStatusValidationFailure(&opsManager, err.Error(), log)
	}

	opsManagerUserPassword, err := r.getAppDBPassword(opsManager, log)

	if err != nil {
		return r.updateStatusFailed(&opsManager, fmt.Sprintf("Error ensuring Ops Manager user password: %s", err), log)
	}

	// 1. AppDB
	emptyResult := reconcile.Result{}
	appDbReconciler := newAppDBReplicaSetReconciler(r.ReconcileCommonController)
	result, err := appDbReconciler.Reconcile(opsManager, opsManager.Spec.AppDB, opsManagerUserPassword)
	if err != nil || result != emptyResult {
		return result, err
	}

	// 2. Ops Manager (create and wait)
	status := r.createOpsManagerStatefulset(opsManager, opsManagerUserPassword, log)
	if !status.isOk() {
		return status.updateStatus(&opsManager, r.ReconcileCommonController, log, opsManager.CentralURL())
	}

	// 3. Backup Daemon (create and wait)
	status = r.createBackupDaemonStatefulset(opsManager, opsManagerUserPassword, log)
	if !status.isOk() {
		return status.updateStatus(&opsManager, r.ReconcileCommonController, log, opsManager.CentralURL())
	}

	// 4. Prepare Ops Manager (ensure the first user is created and public API key saved to secret)
	var omAdmin api.Admin
	if status, omAdmin = r.prepareOpsManager(opsManager, log); !status.isOk() {
		return status.updateStatus(&opsManager, r.ReconcileCommonController, log, opsManager.CentralURL())
	}

	// 5. Prepare Backup Daemon
	if status = r.prepareBackupInOpsManager(opsManager, omAdmin, log); !status.isOk() {
		return status.updateStatus(&opsManager, r.ReconcileCommonController, log, opsManager.CentralURL())
	}

	if status.isOk() {
		successStatus := status.(*successStatus)
		successStatus.warnings = append(successStatus.warnings, opsManager.Status.Warnings...)
	}
	return status.updateStatus(&opsManager, r.ReconcileCommonController, log)
}

// createOpsManagerStatefulset ensures the gen key secret exists and creates the Ops Manager StatefulSet.
func (r *OpsManagerReconciler) createOpsManagerStatefulset(opsManager mdbv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) reconcileStatus {
	if err := r.ensureGenKey(opsManager, log); err != nil {
		return failedErr(err)
	}
	r.ensureConfiguration(&opsManager, opsManagerUserPassword, log)

	helper := r.kubeHelper.NewOpsManagerStatefulSetHelper(opsManager).SetLogger(log)
	if opsManager.Annotations != nil {
		helper.SetAnnotations(opsManager.Annotations)
	}
	if opsManager.Spec.Security.TLS.SecretRef.Name != "" {
		helper.SetHTTPSCertSecretName(opsManager.Spec.Security.TLS.SecretRef.Name)
	}
	if err := helper.CreateOrUpdateInKubernetes(); err != nil {
		return failedErr(err)
	}

	if !r.kubeHelper.isStatefulSetUpdated(opsManager.Namespace, opsManager.Name, log) {
		return pending("Ops Manager is still starting")
	}

	return ok()
}

func AddOpsManagerController(mgr manager.Manager) error {
	reconciler := newOpsManagerReconciler(mgr, om.NewOpsManagerConnection, &api.DefaultInitializer{}, api.NewOmAdmin)
	c, err := controller.New(util.MongoDbOpsManagerController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	// watch for changes to the Ops Manager resources
	if err = c.Watch(&source.Kind{Type: &mdbv1.MongoDBOpsManager{}}, &handler.EnqueueRequestForObject{}, predicatesForOpsManager()); err != nil {
		return err
	}

	// watch the secret with the Ops Manager user password
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&ConfigMapAndSecretHandler{resourceType: Secret, trackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbOpsManagerController)
	return nil
}

// ensureConfiguration makes sure the mandatory configuration is specified.
func (r OpsManagerReconciler) ensureConfiguration(opsManager *mdbv1.MongoDBOpsManager, password string, log *zap.SugaredLogger) {
	// update the central URL
	setConfigProperty(opsManager, util.MmsCentralUrlPropKey, opsManager.CentralURL(), log)

	setConfigProperty(opsManager, util.MmsMongoUri, buildMongoConnectionUrl(*opsManager, password), log)

	// override the versions directory (defaults to "/opt/mongodb/mms/mongodb-releases/")
	setConfigProperty(opsManager, util.MmsVersionsDirectory, "/mongodb-ops-manager/mongodb-releases/", log)

	// feature controls will always be enabled
	setConfigProperty(opsManager, util.MmsFeatureControls, "true", log)
}

// createBackupDaemonStatefulset creates a StatefulSet for backup daemon and waits shortly until it's started
// Note, that the idea of creating two statefulsets for Ops Manager and Backup Daemon in parallel hasn't worked out
// as the daemon in this case just hangs silently (in practice it's ok to start it in ~1 min after start of OM though
// we will just start them sequentially)
func (r *OpsManagerReconciler) createBackupDaemonStatefulset(opsManager mdbv1.MongoDBOpsManager,
	opsManagerUserPassword string, log *zap.SugaredLogger) reconcileStatus {
	if !opsManager.Spec.Backup.Enabled {
		return ok()
	}
	r.ensureConfiguration(&opsManager, opsManagerUserPassword, log)

	backupHelper := r.kubeHelper.NewBackupStatefulSetHelper(opsManager)
	backupHelper.SetLogger(log)

	if err := backupHelper.CreateOrUpdateInKubernetes(); err != nil {
		return failedErr(err)
	}
	// Note, that this will return true quite soon as we don't have daemon readiness so far
	if !r.kubeHelper.isStatefulSetUpdated(opsManager.Namespace, opsManager.BackupStatefulSetName(), log) {
		return pending("Backup Daemon is still starting")
	}
	return ok()
}

// buildMongoConnectionUrl builds the connection url to the appdb. Note, that it overrides the default authMechanism
// (which internally depends on the mongodb version)
func buildMongoConnectionUrl(opsManager mdbv1.MongoDBOpsManager, password string) string {
	return opsManager.Spec.AppDB.ConnectionURL(util.OpsManagerMongoDBUserName, password,
		map[string]string{"authMechanism": "SCRAM-SHA-1"})
}

func setConfigProperty(opsManager *mdbv1.MongoDBOpsManager, key, value string, log *zap.SugaredLogger) {
	if opsManager.AddConfigIfDoesntExist(key, value) {
		if key == util.MmsMongoUri {
			log.Debugw("Configured property", key, util.RedactMongoURI(value))
		} else {
			log.Debugw("Configured property", key, value)
		}
	}
}

// ensureGenKey
func (r OpsManagerReconciler) ensureGenKey(om mdbv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	objectKey := objectKey(om.Namespace, om.Name+"-gen-key")
	_, err := r.kubeHelper.readSecret(objectKey)
	if apiErrors.IsNotFound(err) {
		// todo if the key is not found but the AppDB is initialized - OM will fail to start as preflight
		// check will complain that keys are different - we need to validate against this here

		// the length must be equal to 'EncryptionUtils.DES3_KEY_LENGTH' (24) from mms
		token := make([]byte, 24)
		rand.Read(token)
		keyMap := map[string][]byte{"gen.key": token}

		log.Infof("Creating secret %s", objectKey)
		return r.kubeHelper.createSecret(objectKey, keyMap, map[string]string{}, nil)
	}
	return err
}

// getAppDBPassword will return the password that was specified by the user, or the auto generated password stored in
// the secret (generate it and store in secret otherwise)
func (r OpsManagerReconciler) getAppDBPassword(opsManager mdbv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	passwordRef := opsManager.Spec.AppDB.PasswordSecretKeyRef
	if passwordRef != nil && passwordRef.Name != "" { // there is a secret specified for the Ops Manager user

		password, err := r.kubeHelper.readSecretKey(objectKey(opsManager.Namespace, passwordRef.Name), passwordRef.Key)
		if err != nil {
			return "", err
		}
		log.Debugf("Reading password from secret/%s", passwordRef.Name)

		// watch for any changes on the user provided password
		r.addWatchedResourceIfNotAdded(
			passwordRef.Name,
			opsManager.Namespace,
			Secret,
			objectKeyFromApiObject(&opsManager),
		)

		// delete the auto generated password, we don't need it anymore. We can just generate a new one if
		// the user password is deleted
		log.Debugf("Deleting Operator managed password secret/%s from namespace", opsManager.Spec.AppDB.GetSecretName(), opsManager.Namespace)
		if err := r.kubeHelper.deleteSecret(objectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())); err != nil && !apiErrors.IsNotFound(err) {
			return "", err
		}

		return password, nil
	}

	// otherwise we'll ensure the auto generated password exists
	secretObjectKey := objectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetSecretName())
	secret, err := r.kubeHelper.readSecret(secretObjectKey)

	if apiErrors.IsNotFound(err) {
		// create the password
		password, err := util.GenerateRandomFixedLengthStringOfSize(12)
		if err != nil {
			return "", err
		}

		passwordData := map[string]string{
			util.OpsManagerPasswordKey: password,
		}

		log.Infof("Creating mongodb-ops-manager password in secret/%s in namespace %s", secretObjectKey.Name, secretObjectKey.Namespace)
		if err = r.kubeHelper.createSecret(secretObjectKey, passwordData, nil, &opsManager); err != nil {
			return "", err
		}

		log.Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetSecretName())
		return password, nil
	} else if err != nil {
		// any other error
		return "", err
	}

	log.Debugf("Using auto generated AppDB password stored in secret/%s", opsManager.Spec.AppDB.GetSecretName())
	return secret[util.OpsManagerPasswordKey], nil
}

// prepareOpsManager ensures the admin user is created and the admin public key exists. It returns the instance of
// api.Admin to perform future Ops Manager configuration
// Note the exception handling logic - if the controller fails to save the public API key secret - it cannot fix this
// manually (the first OM user can be created only once) - so the resource goes to Failed state and shows the message
// asking the user to fix this manually.
// Theoretically the Operator could remove the appdb StatefulSet (as the OM must be empty without any user data) and
// allow the db to get recreated but seems this is a quite radical operation.
func (r OpsManagerReconciler) prepareOpsManager(opsManager mdbv1.MongoDBOpsManager, log *zap.SugaredLogger) (reconcileStatus, api.Admin) {
	// We won't support cross-namespace secrets until CLOUDP-46636 is resolved
	secret := objectKey(opsManager.Namespace, opsManager.Spec.AdminSecret)

	// 1. Read the admin secret
	userData, err := r.kubeHelper.readSecret(secret)

	if apiErrors.IsNotFound(err) {
		// This requires user actions - let's wait a bit longer than 10 seconds
		return failedRetry("the secret %s doesn't exist - you need to create it to finish Ops Manager initialization", 60, secret), nil
	} else if err != nil {
		return failedErr(err), nil
	}

	user, err := newUserFromSecret(userData)
	if err != nil {
		return failed("failed to read user data from the secret %s: %s", secret, err), nil
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
			return failed("failed to create an admin user in Ops Manager: %s", err), nil
		}

		// Recreate an admin key secret in the Operator namespace if the user was created
		if apiKey != "" {
			log.Infof("Created an admin user %s with GLOBAL_ADMIN role", user.Username)

			// The structure matches the structure of a credentials secret used by normal mongodb resources
			secretData := map[string]string{util.OmPublicApiKey: apiKey, util.OmUser: user.Username}

			if err = r.kubeHelper.deleteSecret(adminKeySecretName); err != nil && !apiErrors.IsNotFound(err) {
				// TODO our desired behavior is not to fail but just append the warning to the status (CLOUDP-51340)
				return failedRetry("failed to replace a secret for admin public api key. %s. The error : %s", 300,
					detailedMsg, err), nil
			}
			if err = r.kubeHelper.createSecret(adminKeySecretName, secretData, map[string]string{}, &opsManager); err != nil {
				// TODO see above
				return failedRetry("failed to create a secret for admin public api key. %s. The error : %s", 300,
					detailedMsg, err), nil
			}
			log.Infof("Created a secret for admin public api key %s", adminKeySecretName)

			// Each "read-after-write" operation needs some timeout after write unfortunately :(
			// https://github.com/kubernetes-sigs/controller-runtime/issues/343#issuecomment-468402446
			time.Sleep(time.Duration(util.ReadEnvVarIntOrDefault(util.K8sCacheRefreshEnv, util.DefaultK8sCacheRefreshTimeSeconds)) * time.Second)
		}
	}

	// 3. Final validation of current state - this could be the retry after failing to create the secret during
	// previous reconciliation (and the apiKey is empty as "the first user already exists") - the only fix is
	// to create the secret manually
	_, err = r.kubeHelper.readSecret(adminKeySecretName)
	if err != nil {
		return failedRetry("admin API key secret for Ops Manager doesn't exit - was it removed accidentally? %s. The error : %s", 300,
			detailedMsg, err), nil
	}
	cred, err := r.kubeHelper.readCredentials(operatorNamespace(), opsManager.APIKeySecretName())
	if err != nil {
		return failedErr(err), nil
	}

	return ok(), r.omAdminProvider(opsManager.CentralURL(), cred.User, cred.PublicAPIKey)
}

// prepareBackupInOpsManager makes the changes to backup admin configuration based on the Ops Manager spec
func (r *OpsManagerReconciler) prepareBackupInOpsManager(opsManager mdbv1.MongoDBOpsManager, omAdmin api.Admin,
	log *zap.SugaredLogger) reconcileStatus {
	if !opsManager.Spec.Backup.Enabled {
		return ok()
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
			return pending("BackupDaemon hasn't started yet")
		} else if err != nil {
			return failedErr(err)
		}
	} else if err != nil {
		return failedErr(err)
	}

	// 2. Oplog store configs
	status := r.ensureOplogStoresInOpsManager(opsManager, omAdmin, log)

	// 3. S3 Configs
	status = status.merge(r.ensureS3ConfigurationInOpsManager(opsManager, omAdmin, log))

	// 4. Block store configs
	status = status.merge(r.ensureBlockStoresInOpsManager(opsManager, omAdmin, log))

	return status
}

// ensureOplogStoresInOpsManager aligns the oplog stores in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureOplogStoresInOpsManager(opsManager mdbv1.MongoDBOpsManager, omAdmin api.Admin, log *zap.SugaredLogger) reconcileStatus {
	if !opsManager.Spec.Backup.Enabled {
		return ok()
	}

	opsManagerOplogConfigs, err := omAdmin.ReadOplogStoreConfigs()
	if err != nil {
		return failedErr(err)
	}

	// Creating new configs
	operatorOplogConfigs := opsManager.Spec.Backup.OplogStoreConfigs
	configsToCreate := util.SetDifferenceGeneric(operatorOplogConfigs, opsManagerOplogConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMDatastoreConfig(opsManager, v.(mdbv1.DataStoreConfig))
		if !status.isOk() {
			return status
		}
		log.Debugw("Creating Oplog Store in Ops Manager", "config", omConfig)
		if err = omAdmin.CreateOplogStoreConfig(omConfig); err != nil {
			return failedErr(err)
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := util.SetIntersectionGeneric(opsManagerOplogConfigs, operatorOplogConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.DataStoreConfig)
		operatorConfig := v[1].(mdbv1.DataStoreConfig)
		operatorView, status := r.buildOMDatastoreConfig(opsManager, operatorConfig)
		if !status.isOk() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Debugw("Updating Oplog Store in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateOplogStoreConfig(configToUpdate); err != nil {
			return failedErr(err)
		}
	}

	// Removing non-existing configs
	configsToRemove := util.SetDifferenceGeneric(opsManagerOplogConfigs, opsManager.Spec.Backup.OplogStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Oplog Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteOplogStoreConfig(v.Identifier().(string)); err != nil {
			return failedErr(err)
		}
	}
	return ok()
}

// ensureBlockStoresInOpsManager aligns the blockStore configs in Ops Manager with the Operator state. So it adds the new configs
// and removes the non-existing ones. Note that there's no update operation as so far the Operator manages only one field
// 'path'. This will allow users to make any additional changes to the file system stores using Ops Manager UI and the
// Operator won't override them
func (r *OpsManagerReconciler) ensureBlockStoresInOpsManager(opsManager mdbv1.MongoDBOpsManager, omAdmin api.Admin, log *zap.SugaredLogger) reconcileStatus {
	if !opsManager.Spec.Backup.Enabled {
		return ok()
	}

	opsManagerBlockStoreConfigs, err := omAdmin.ReadBlockStoreConfigs()
	if err != nil {
		return failedErr(err)
	}

	// Creating new configs
	operatorBlockStoreConfigs := opsManager.Spec.Backup.BlockStoreConfigs
	configsToCreate := util.SetDifferenceGeneric(operatorBlockStoreConfigs, opsManagerBlockStoreConfigs)
	for _, v := range configsToCreate {
		omConfig, status := r.buildOMDatastoreConfig(opsManager, v.(mdbv1.DataStoreConfig))
		if !status.isOk() {
			return status
		}
		log.Debugw("Creating Block Store in Ops Manager", "config", omConfig)
		if err = omAdmin.CreateBlockStoreConfig(omConfig); err != nil {
			return failedErr(err)
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := util.SetIntersectionGeneric(opsManagerBlockStoreConfigs, operatorBlockStoreConfigs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.DataStoreConfig)
		operatorConfig := v[1].(mdbv1.DataStoreConfig)
		operatorView, status := r.buildOMDatastoreConfig(opsManager, operatorConfig)
		if !status.isOk() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Debugw("Updating Block Store in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateBlockStoreConfig(configToUpdate); err != nil {
			return failedErr(err)
		}
	}

	// Removing non-existing configs
	configsToRemove := util.SetDifferenceGeneric(opsManagerBlockStoreConfigs, opsManager.Spec.Backup.BlockStoreConfigs)
	for _, v := range configsToRemove {
		log.Debugf("Removing Block Store %s from Ops Manager", v.Identifier())
		if err = omAdmin.DeleteBlockStoreConfig(v.Identifier().(string)); err != nil {
			return failedErr(err)
		}
	}
	return ok()
}

func (r *OpsManagerReconciler) ensureS3ConfigurationInOpsManager(opsManager mdbv1.MongoDBOpsManager, omAdmin api.Admin,
	log *zap.SugaredLogger) reconcileStatus {
	if !opsManager.Spec.Backup.Enabled {
		return ok()
	}

	opsManagerS3Configs, err := omAdmin.ReadS3Configs()
	if err != nil {
		return failedErr(err)
	}

	operatorS3Configs := opsManager.Spec.Backup.S3Configs
	configsToCreate := util.SetDifferenceGeneric(operatorS3Configs, opsManagerS3Configs)
	for _, config := range configsToCreate {
		omConfig, status := r.buildOMS3Config(opsManager, config.(mdbv1.S3Config), log)
		if !status.isOk() {
			return status
		}

		log.Debugw("Creating S3Config in Ops Manager", "config", omConfig)
		if err := omAdmin.CreateS3Config(omConfig); err != nil {
			return failedErr(err)
		}
	}

	// Updating existing configs. It intersects the OM API configs with Operator spec configs and returns pairs
	//["omConfig", "operatorConfig"].
	configsToUpdate := util.SetIntersectionGeneric(opsManagerS3Configs, operatorS3Configs)
	for _, v := range configsToUpdate {
		omConfig := v[0].(backup.S3Config)
		operatorConfig := v[1].(mdbv1.S3Config)
		operatorView, status := r.buildOMS3Config(opsManager, operatorConfig, log)
		if !status.isOk() {
			return status
		}

		// Now we need to merge the Operator version into the OM one overriding only the fields that the Operator
		// "owns"
		configToUpdate := operatorView.MergeIntoOpsManagerConfig(omConfig)
		log.Debugw("Updating S3Config in Ops Manager", "config", configToUpdate)
		if err = omAdmin.UpdateS3Config(configToUpdate); err != nil {
			return failedErr(err)
		}
	}

	configsToRemove := util.SetDifferenceGeneric(opsManagerS3Configs, operatorS3Configs)
	for _, config := range configsToRemove {
		log.Debugf("Removing S3Config %s from Ops Manager", config.Identifier())
		if err := omAdmin.DeleteS3Config(config.Identifier().(string)); err != nil {
			return failedErr(err)
		}
	}

	if len(opsManager.Spec.Backup.S3Configs) == 0 || len(opsManager.Spec.Backup.OplogStoreConfigs) == 0 {
		return ok(mdbv1.S3BackupsNotFullyConfigured)
	}

	return ok()
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
func (r *OpsManagerReconciler) buildOMS3Config(opsManager mdbv1.MongoDBOpsManager, config mdbv1.S3Config,
	log *zap.SugaredLogger) (backup.S3Config, reconcileStatus) {
	mongodb, status := r.getMongoDbForS3Config(opsManager, config)
	if !status.isOk() {
		return backup.S3Config{}, status
	}

	isAppDB := config.MongoDBResourceRef == nil

	if !isAppDB {
		if status = validateS3Config(mongodb, config); !status.isOk() {
			return backup.S3Config{}, status
		}
	}

	userName, password, status := r.getS3MongoDbUserNameAndPassword(mongodb, opsManager, config, log)
	if !status.isOk() {
		return backup.S3Config{}, status
	}

	s3Creds, err := r.readS3Credentials(config.S3SecretRef.Name, opsManager.Namespace)
	if err != nil {
		return backup.S3Config{}, failedErr(err)
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

	return backup.NewS3Config(config.Name, uri, bucket, *s3Creds), ok()
}

func (r *OpsManagerReconciler) getMongoDbForS3Config(opsManager mdbv1.MongoDBOpsManager, config mdbv1.S3Config) (mdbv1.MongoDB, reconcileStatus) {
	if config.MongoDBResourceRef == nil {
		// having no mongodb reference means the AppDB should be used as a metadata storage
		// We need to build a fake MongoDB resource
		return mdbv1.MongoDB{Spec: opsManager.Spec.AppDB.MongoDbSpec}, ok()
	}
	mongodb := &mdbv1.MongoDB{}
	mongodbObjectKey := config.MongodbResourceObjectKey(opsManager)
	err := r.client.Get(context.TODO(), mongodbObjectKey, mongodb)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Returning pending as the user may create the mongodb resource soon
			return mdbv1.MongoDB{}, pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
		}
		return mdbv1.MongoDB{}, failedErr(err)
	}
	return *mongodb, ok()
}

// getS3MongoDbUserNameAndPassword returns userName and password if MongoDB resource has scram-sha enabled.
// Note, that we don't worry if the 'mongodbUserRef' is specified but SCRAM-SHA is not enabled - we just ignore the
// user
func (r *OpsManagerReconciler) getS3MongoDbUserNameAndPassword(mongodb mdbv1.MongoDB, opsManager mdbv1.MongoDBOpsManager, config mdbv1.S3Config, log *zap.SugaredLogger) (string, string, reconcileStatus) {
	if !util.ContainsString(mongodb.Spec.Security.Authentication.Modes, util.SCRAM) {
		return "", "", ok()
	}
	// If the resource is empty then we need to consider AppDB credentials
	if config.MongoDBResourceRef == nil {
		password, err := r.getAppDBPassword(opsManager, log)
		if err != nil {
			return "", "", failedErr(err)
		}
		return util.OpsManagerMongoDBUserName, password, ok()
	}
	// Otherwise we are fetching the MongoDBUser entity and a related password
	mongodbUser := &mdbv1.MongoDBUser{}
	mongodbUserObjectKey := config.MongodbUserObjectKey(opsManager.Namespace)
	err := r.client.Get(context.TODO(), mongodbUserObjectKey, mongodbUser)
	if apiErrors.IsNotFound(err) {
		return "", "", pending("The MongoDBUser object %s doesn't exist", mongodbUserObjectKey)
	}
	if err != nil {
		return "", "", failed("Failed to fetch the user %s: %s", mongodbUserObjectKey, err)
	}
	userName := mongodbUser.Spec.Username
	password, err := mongodbUser.GetPassword(r.client)
	if err != nil {
		return "", "", failed("Failed to read password for the user %s: %s", mongodbUserObjectKey, err)
	}
	return userName, password, ok()
}

// buildOMDatastoreConfig builds the OM API datastore config based on the Kubernetes OM resource one.
// To do this it may need to read the Mongodb User and its password to build mongodb url correctly
func (r *OpsManagerReconciler) buildOMDatastoreConfig(opsManager mdbv1.MongoDBOpsManager, operatorConfig mdbv1.DataStoreConfig) (backup.DataStoreConfig, reconcileStatus) {
	mongodb := &mdbv1.MongoDB{}
	mongodbObjectKey := operatorConfig.MongodbResourceObjectKey(opsManager.Namespace)
	err := r.client.Get(context.TODO(), mongodbObjectKey, mongodb)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Returning pending as the user may create the mongodb resource soon
			return backup.DataStoreConfig{}, pending("The MongoDB object %s doesn't exist", mongodbObjectKey)
		}
		return backup.DataStoreConfig{}, failedErr(err)
	}

	status := validateDataStoreConfig(*mongodb, operatorConfig)
	if !status.isOk() {
		return backup.DataStoreConfig{}, status
	}

	// If MongoDB resource has scram-sha enabled then we need to read the username and the password.
	// Note, that we don't worry if the 'mongodbUserRef' is specified but SCRAM-SHA is not enabled - we just ignore the
	// user
	var userName, password string
	if util.ContainsString(mongodb.Spec.Security.Authentication.Modes, util.SCRAM) {
		mongodbUser := &mdbv1.MongoDBUser{}
		mongodbUserObjectKey := operatorConfig.MongodbUserObjectKey(opsManager.Namespace)
		err := r.client.Get(context.TODO(), mongodbUserObjectKey, mongodbUser)
		if apiErrors.IsNotFound(err) {
			return backup.DataStoreConfig{}, pending("The MongoDBUser object %s doesn't exist", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace))
		}
		if err != nil {
			return backup.DataStoreConfig{}, failed("Failed to fetch the user %s: %s", operatorConfig.MongodbResourceObjectKey(opsManager.Namespace), err)
		}
		userName = mongodbUser.Spec.Username
		password, err = mongodbUser.GetPassword(r.client)
		if err != nil {
			return backup.DataStoreConfig{}, failed("Failed to read password for the user %s: %s", mongodbUserObjectKey, err)
		}
	}

	tls := mongodb.Spec.Security.TLSConfig.Enabled
	mongoUri := mongodb.ConnectionURL(userName, password, map[string]string{})
	return backup.NewDataStoreConfig(operatorConfig.Name, mongoUri, tls), ok()
}

func validateS3Config(mongodb mdbv1.MongoDB, s3Config mdbv1.S3Config) reconcileStatus {
	return validateConfig(mongodb, s3Config.MongoDBUserRef, "S3 metadata database")
}

func validateDataStoreConfig(mongodb mdbv1.MongoDB, dataStoreConfig mdbv1.DataStoreConfig) reconcileStatus {
	return validateConfig(mongodb, dataStoreConfig.MongoDBUserRef, "Oplog/Blockstore databases")
}

func validateConfig(mongodb mdbv1.MongoDB, userRef *mdbv1.MongoDBUserRef, description string) reconcileStatus {
	// validate
	if !util.ContainsString(mongodb.Spec.Security.Authentication.Modes, util.SCRAM) &&
		len(mongodb.Spec.Security.Authentication.Modes) > 0 {
		return failed("The only authentication mode supported for the %s is SCRAM-SHA", description)
	}
	if util.ContainsString(mongodb.Spec.Security.Authentication.Modes, util.SCRAM) &&
		(userRef == nil || userRef.Name == "") {
		return failed("MongoDB resource %s is configured to use SCRAM-SHA authentication mode, the user must be"+
			" specified using 'mongodbUserRef'", mongodb.Name)
	}
	comparison, err := util.CompareVersions(mongodb.Spec.GetVersion(), util.MinimumScramSha256MdbVersion)
	if err != nil {
		return failedErr(err)
	}
	if util.ContainsString(mongodb.Spec.Security.Authentication.Modes, util.SCRAM) && comparison >= 0 {
		return failed("The %s with SCRAM-SHA enabled must have version less than 4.0.0", description)
	}
	return ok()
}

// performValidation makes some validation of Ops Manager spec. So far this validation mostly follows the restrictions
// for the app db in ops manager, see MongoConnectionConfigurationCheck
// Ideally it must be done in an admission web hook
func performValidation(opsManager mdbv1.MongoDBOpsManager) error {
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
