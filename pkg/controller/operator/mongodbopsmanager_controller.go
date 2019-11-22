package operator

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/blang/semver"
	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type OpsManagerReconciler struct {
	*ReconcileCommonController
	omInitializer api.Initializer
}

var _ reconcile.Reconciler = &OpsManagerReconciler{}

func newOpsManagerReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, initializer api.Initializer) *OpsManagerReconciler {
	return &OpsManagerReconciler{
		ReconcileCommonController: newReconcileCommonController(mgr, omFunc),
		omInitializer:             initializer,
	}
}

// Reconcile performs the reconciliation logic for AppDB and Ops Manager
// AppDB is reconciled first (independent from Ops Manager as the agent is run in headless mode) and
// Ops Manager statefulset is created then.
// Backup daemon statefulset is created/updated and configured optionally if backup is enabled.
func (r *OpsManagerReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("OpsManager", request.NamespacedName)

	opsManager := &mdbv1.MongoDBOpsManager{}

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(opsManager, fmt.Sprintf("Failed to reconcile Ops Manager: %s", err), log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	if reconcileResult, err := r.prepareResourceForReconciliation(request, opsManager, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	log.Info("-> OpsManager.Reconcile")
	log.Infow("OpsManager.Spec", "spec", opsManager.Spec)
	log.Infow("OpsManager.Status", "status", opsManager.Status)

	if err := performValidation(opsManager); err != nil {
		return r.updateStatusValidationFailure(opsManager, err.Error(), log)
	}

	opsManagerUserPassword, err := r.ensureOpsManagerUserPassword(opsManager)
	if err != nil {
		return r.updateStatusFailed(opsManager, fmt.Sprintf("Error ensuring Ops Manager user password: %s", err), log)
	}

	// 1. AppDB
	emptyResult := reconcile.Result{}
	appDbReconciler := newAppDBReplicaSetReconciler(r.ReconcileCommonController)
	result, err := appDbReconciler.Reconcile(opsManager, &opsManager.Spec.AppDB, opsManagerUserPassword)
	if err != nil || result != emptyResult {
		return result, err
	}

	// 2. Ops Manager (create and wait)
	status := r.createOpsManagerStatefulset(opsManager, opsManagerUserPassword, log)
	if !status.isOk() {
		return status.updateStatus(opsManager, r.ReconcileCommonController, log)
	}

	// 3. Backup Daemon (create and wait)
	status = r.createBackupDaemonStatefulset(opsManager, log)
	if !status.isOk() {
		return status.updateStatus(opsManager, r.ReconcileCommonController, log)
	}

	// 4. Prepare Ops Manager (ensure the first user is created and public API key saved to secret)
	credentials := &Credentials{}
	if result := r.prepareOpsManager(opsManager, credentials, log); !result.isOk() {
		return result.updateStatus(opsManager, r.ReconcileCommonController, log)
	}

	// 5. Prepare Backup Daemon

	return r.updateStatusSuccessful(opsManager, log, centralURL(opsManager))
}

// createOpsManagerStatefulset ensures the gen key secret exists and creates the Ops Manager StatefulSet.
func (r *OpsManagerReconciler) createOpsManagerStatefulset(opsManager *mdbv1.MongoDBOpsManager, opsManagerUserPassword string, log *zap.SugaredLogger) reconcileStatus {
	if err := r.ensureGenKey(opsManager, log); err != nil {
		return failedErr(err)
	}
	r.ensureConfiguration(opsManager, opsManagerUserPassword, log)

	helper := r.kubeHelper.NewOpsManagerStatefulSetHelper(opsManager).SetLogger(log)
	if err := helper.CreateOrUpdateInKubernetes(); err != nil {
		return failedErr(err)
	}

	if !r.kubeHelper.isStatefulSetUpdated(opsManager.Namespace, opsManager.Name, log) {
		return pending("Ops Manager is still starting")
	}

	return ok()
}

func AddOpsManagerController(mgr manager.Manager) error {
	reconciler := newOpsManagerReconciler(mgr, om.NewOpsManagerConnection, &api.DefaultInitializer{})
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

// ensureConfiguration makes sure the mandatory configuration is specified
func (r OpsManagerReconciler) ensureConfiguration(opsManager *mdbv1.MongoDBOpsManager, password string, log *zap.SugaredLogger) {
	// update the central URL
	setConfigProperty(opsManager, util.MmsCentralUrlPropKey, centralURL(opsManager), log)

	setConfigProperty(opsManager, util.MmsMongoUri, buildMongoConnectionUrl(opsManager, password), log)

	// override the versions directory (defaults to "/opt/mongodb/mms/mongodb-releases/")
	setConfigProperty(opsManager, util.MmsVersionsDirectory, "/mongodb-ops-manager/mongodb-releases/", log)
}

// createBackupDaemonStatefulset creates a StatefulSet for backup daemon and waits shortly until it's started
// Note, that the idea of creating two statefulsets for Ops Manager and Backup Daemon in parallel hasn't worked out
// as the daemon in this case just hangs silently (in practice it's ok to start it in ~1 min after start of OM though
// we will just start them sequentially)
func (r *OpsManagerReconciler) createBackupDaemonStatefulset(opsManager *mdbv1.MongoDBOpsManager, log *zap.SugaredLogger) reconcileStatus {
	if opsManager.Spec.Backup.Enabled {
		log.Debug("Enabling backup for Ops Manager")

		backupHelper := r.kubeHelper.NewBackupStatefulSetHelper(opsManager)
		backupHelper.SetLogger(log)

		if err := backupHelper.CreateOrUpdateInKubernetes(); err != nil {
			return failedErr(err)
		}
		// Note, that this will return true quite soon as we don't have daemon readiness so far
		if !r.kubeHelper.isStatefulSetUpdated(opsManager.Namespace, opsManager.BackupStatefulSetName(), log) {
			return pending("Backup Daemon is still starting")
		}
	}
	return ok()
}

// Ideally this must be a method in mdbv1.MongoDB - todo move it there when the AppDB is gone and mdbv1.MongoDB is used instead
func buildMongoConnectionUrl(opsManager *mdbv1.MongoDBOpsManager, password string) string {
	db := opsManager.Spec.AppDB
	statefulsetName := db.Name()
	serviceName := db.ServiceName()
	replicas := db.Members

	hostnames, _ := GetDNSNames(statefulsetName, serviceName, opsManager.Namespace, db.ClusterName, replicas)
	uri := fmt.Sprintf("mongodb://%s:%s@", util.OpsManagerMongoDBUserName, password)
	for i, h := range hostnames {
		hostnames[i] = fmt.Sprintf("%s:%d", h, util.MongoDbDefaultPort)
	}
	uri += strings.Join(hostnames, ",")
	uri += "/?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000"
	uri += "&authSource=admin&authMechanism=SCRAM-SHA-1"
	return uri
}

func setConfigProperty(opsManager *mdbv1.MongoDBOpsManager, key, value string, log *zap.SugaredLogger) {
	if opsManager.AddConfigIfDoesntExist(key, value) {
		log.Debugw("Configured property", key, value)
	}
}

// ensureGenKey
func (r OpsManagerReconciler) ensureGenKey(om *mdbv1.MongoDBOpsManager, log *zap.SugaredLogger) error {
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

// ensureOpsManagerUserPassword will attempt to read a password from a secret, or generate a password
// and create the secret if it does not exist.
func (r OpsManagerReconciler) ensureOpsManagerUserPassword(opsManager *mdbv1.MongoDBOpsManager) (string, error) {

	// TODO: check for custom password that a user has specified

	// otherwise we'll ensure the auto generated password exists
	secretName := opsManager.Name + "-password" // TODO: CLOUDP-53194 take this from the spec of the resource
	objectKey := objectKey(opsManager.Namespace, secretName)
	secret, err := r.kubeHelper.readSecret(objectKey)
	if apiErrors.IsNotFound(err) {
		// create the password
		password, err := util.GenerateRandomFixedLengthStringOfSize(100)
		if err != nil {
			return "", err
		}

		passwordData := map[string]string{
			util.OpsManagerPasswordKey: password, // TODO: CLOUDP-53194 key will be specified in spec, this will be fallback value if not specified
		}

		if err := r.kubeHelper.createSecret(types.NamespacedName{Namespace: opsManager.Namespace, Name: secretName}, passwordData, nil, opsManager); err != nil {
			return "", err
		}

		// watch the secret which contains the password for the Ops Manager user
		r.addWatchedResourceIfNotAdded(
			secretName,
			opsManager.Namespace,
			Secret,
			objectKeyFromApiObject(opsManager),
		)

		return password, nil
	}

	// any other error
	if err != nil {
		return "", err
	}

	r.addWatchedResourceIfNotAdded(
		secretName,
		opsManager.Namespace,
		Secret,
		objectKeyFromApiObject(opsManager),
	)

	return secret[util.OpsManagerPasswordKey], nil
}

// prepareOpsManager ensures the admin user is created and the admin public key exists
// Note the exception handling logic - if the controller fails to save the public API key secret - it cannot fix this
// manually (the first OM user can be created only once) - so the resource goes to Failed state and shows the message
// asking the user to fix this manually.
// Theoretically the Operator could remove the appdb StatefulSet (as the OM must be empty without any user data) and
// allow the db to get recreated but seems this is a quite radical operation.
func (r OpsManagerReconciler) prepareOpsManager(opsManager *mdbv1.MongoDBOpsManager, credentials *Credentials, log *zap.SugaredLogger) reconcileStatus {
	// We won't support cross-namespace secrets until CLOUDP-46636 is resolved
	secret := objectKey(opsManager.Namespace, opsManager.Spec.AdminSecret)

	// 1. Read the admin secret
	userData, err := r.kubeHelper.readSecret(secret)

	if apiErrors.IsNotFound(err) {
		// This requires user actions - let's wait a bit longer than 10 seconds
		return failedRetry("the secret %s doesn't exist - you need to create it to finish Ops Manager initialization", 60, secret)
	} else if err != nil {
		return failedErr(err)
	}

	user, err := newUserFromSecret(userData)
	if err != nil {
		return failed("failed to read user data from the secret %s: %s", secret, err)
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
		apiKey, err := r.omInitializer.TryCreateUser(centralURL(opsManager), user)
		if err != nil {
			return failed("failed to create an admin user in Ops Manager: %s", err)
		}

		// Recreate an admin key secret in the Operator namespace if the user was created
		if apiKey != "" {
			log.Infof("Created an admin user %s with GLOBAL_ADMIN role", user.Username)

			// The structure matches the structure of a credentials secret used by normal mongodb resources
			secretData := map[string]string{util.OmPublicApiKey: apiKey, util.OmUser: user.Username}

			if err = r.kubeHelper.deleteSecret(adminKeySecretName); err != nil && !apiErrors.IsNotFound(err) {
				// TODO our desired behavior is not to fail but just append the warning to the status (CLOUDP-51340)
				return failedRetry("failed to replace a secret for admin public api key. %s. The error : %s", 300,
					detailedMsg, err)
			}
			if err = r.kubeHelper.createSecret(adminKeySecretName, secretData, map[string]string{}, opsManager); err != nil {
				// TODO see above
				return failedRetry("failed to create a secret for admin public api key. %s. The error : %s", 300,
					detailedMsg, err)
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
			detailedMsg, err)
	}
	cred, err := r.kubeHelper.readCredentials(operatorNamespace(), opsManager.APIKeySecretName())
	if err != nil {
		return failedErr(err)
	}
	*credentials = *cred
	return ok()
}

// performValidation makes some validation of Ops Manager spec. So far this validation mostly follows the restrictions
// for the app db in ops manager, see MongoConnectionConfigurationCheck
// Ideally it must be done in an admission web hook
func performValidation(opsManager *mdbv1.MongoDBOpsManager) error {
	version := opsManager.Spec.AppDB.Version
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

// centralURL constructs the service name that can be used to access Ops Manager from within
// the cluster
func centralURL(om *mdbv1.MongoDBOpsManager) string {
	fqdn := GetServiceFQDN(om.SvcName(), om.Namespace, om.ClusterName)

	// protocol must be calculated based on tls configuration of the ops manager resource
	protocol := "http"

	return fmt.Sprintf("%s://%s:%d", protocol, fqdn, util.OpsManagerDefaultPort)
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
