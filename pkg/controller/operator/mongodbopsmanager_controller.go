package operator

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	BackingGroupId = "000000000000000000000000"
)

type OpsManagerReconciler struct {
	*ReconcileCommonController
}

var _ reconcile.Reconciler = &OpsManagerReconciler{}

func newOpsManagerReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *OpsManagerReconciler {
	return &OpsManagerReconciler{newReconcileCommonController(mgr, omFunc)}
}

func (r OpsManagerReconciler) createOpsManagerStatefulSet(opsManager *v1.MongoDBOpsManager, log *zap.SugaredLogger) error {

	return nil
}

// Reconcile performs the reconciliation logic for Ops Manager and AppDB
// The workflow description: https://docs.google.com/document/d/1M20PMIl3eyHXPpXOC-tAL7zjr-w5vU5ITvB3xJ8fVNU/edit#bookmark=id.gkgnh437vcx3
// Note, that as the design with "appdb managed by OM" may be rejected for OM beta we don't create separate files
func (r *OpsManagerReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("OpsManager", request.NamespacedName)

	opsManager := &v1.MongoDBOpsManager{}

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

	if err := r.ensureGenKey(opsManager, log); err != nil {
		return r.updateStatusFailed(opsManager, err.Error(), log)
	}

	r.ensureConfiguration(opsManager, log)

	helper := r.kubeHelper.NewOpsManagerStatefulSetHelper(opsManager)
	helper.SetService(opsManager.SvcName()).
		SetLogger(log).
		SetVersion(opsManager.Spec.Version)

	if err := helper.CreateOrUpdateInKubernetes(); err != nil {
		return r.updateStatusFailed(opsManager, fmt.Sprintf("Failed to create/update the StatefulSet: %s", err), log)
	}

	if err := r.waitForOpsManagerToBeReady(opsManager, log); err != nil {
		return r.updateStatusPending(opsManager, err.Error(), log)
	}

	if status := r.prepareOpsManager(opsManager, log); status != nil {
		return status.updateStatus(opsManager, r.ReconcileCommonController, log)
	}

	log.Info("Finished reconciliation for MongoDBOpsManager!")
	return r.updateStatusSuccessful(opsManager, log)
}

func AddOpsManagerController(mgr manager.Manager) error {
	reconciler := newOpsManagerReconciler(mgr, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbOpsManagerController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	// watch for changes to the Ops Manager resources
	eventHandler := MongoDBResourceEventHandler{reconciler: reconciler}

	if err = c.Watch(&source.Kind{Type: &v1.MongoDBOpsManager{}}, &eventHandler, predicatesForOpsManager()); err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbOpsManagerController)
	return nil
}

func (r *OpsManagerReconciler) delete(obj interface{}, log *zap.SugaredLogger) error {
	log.Info("TODO: implement OpsManagerReconciler::delete - manual cleanup required")
	return nil
}

// waitForOpsManagerToBeReady waits until Ops Manager is ready.
// Note, that the waiting time is deliberately made short to make the OpsManager resource more interactive
// to changes/removal. Anyway OpsManager takes crazily long to start so we don't want to block waiting for
// successful start. If unsuccessful - the resource gets into "Pending" state and "info" message is logged.
// The downside is that we can sit in "Pending" forever even if something bad has happened - may be we need to add some
// timer (since last successful start) and log errors if Ops Manager is stuck.
func (r *OpsManagerReconciler) waitForOpsManagerToBeReady(om *v1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	if !util.DoAndRetry(func() (string, bool) {
		// Simple tcp check for port 8080 so far
		host := GetServiceFQDN(om.SvcName(), om.Namespace, om.ClusterName)
		conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, util.OpsManagerDefaultPort))
		if err != nil {
			return fmt.Sprintf("Ops Manager (%s:%d) is not accessible", host, util.OpsManagerDefaultPort), false
		}
		defer conn.Close()
		return "", true
	}, log, 6, 5) {
		return errors.New("Ops Manager hasn't started during specified timeout (it may take quite long)")
	}
	return nil
}

// ensureConfiguration makes sure the mandatory configuration is specified
func (r OpsManagerReconciler) ensureConfiguration(opsManager *v1.MongoDBOpsManager, log *zap.SugaredLogger) {
	// update the central URL
	setConfigProperty(opsManager, util.MmsCentralUrlPropKey, centralURL(opsManager), log)

	setConfigProperty(opsManager, util.MmsManagedAppDB, "true", log)

	setConfigProperty(opsManager, util.MmsTempAppDB, opsManager.Spec.AppDB.Version, log)

	setConfigProperty(opsManager, util.MmsMongoUri, buildMongoConnectionUrl(opsManager), log)
}

// Ideally this must be a method in v1.MongoDB - todo move it there when the AppDB is gone and v1.MongoDB is used instead
func buildMongoConnectionUrl(opsManager *v1.MongoDBOpsManager) string {
	db := opsManager.Spec.AppDB
	var replicas int

	statefulsetName := db.Name()
	serviceName := db.ServiceName()
	switch db.ResourceType {
	case v1.Standalone:
		replicas = 1
	case v1.ReplicaSet:
		replicas = db.Members
	case v1.ShardedCluster:
		{
			replicas = db.MongosCount
			statefulsetName = db.MongosRsName()
		}
	}
	hostnames, _ := GetDNSNames(statefulsetName, serviceName, opsManager.Namespace, db.ClusterName, replicas)
	uri := "mongodb://"
	for _, h := range hostnames {
		h = fmt.Sprintf("%s:%d", h, util.MongoDbDefaultPort)
	}
	uri += strings.Join(hostnames, ",")
	uri += "/?connectTimeoutMS=5000&serverSelectionTimeoutMS=5000"
	return uri
}

func setConfigProperty(opsManager *v1.MongoDBOpsManager, key, value string, log *zap.SugaredLogger) {
	if opsManager.AddConfigIfDoesntExist(key, value) {
		log.Debugw("Configured property", key, value)
	}
}

func (r OpsManagerReconciler) ensureGenKey(om *v1.MongoDBOpsManager, log *zap.SugaredLogger) error {
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
		return r.kubeHelper.createSecret(objectKey, keyMap, map[string]string{})
	}
	return err
}

func (r OpsManagerReconciler) prepareOpsManager(opsManager *v1.MongoDBOpsManager, log *zap.SugaredLogger) reconcileStatus {
	fullMode, err := checkOpsManagerInFullMode(opsManager)
	if err != nil {
		return failedErr(err)
	}
	if !fullMode {
		// The OM is in the "initialization" mode so we need to create the first user
		log.Info(`Ops Manager is running in "Application Database initialization mode", ensuring the admin user is created`)

		if err = r.initializeOpsManager(opsManager, log); err != nil {
			return failed("failed to initialize Ops Manager: %s", err)
		}
		log.Info("Ops Manager is initialized (admin user created), ready to start AppDB")
	} else {
		// Validating that the admin key secret exists
		adminKeySecretName := objectKey(operatorNamespace(), opsManager.APIKeySecretName())
		_, err = r.kubeHelper.readSecret(adminKeySecretName)
		if err != nil {
			// doesn't make much sense to retry soon - let's wait for 5 minutes
			return failedRetry("Admin API key secret for Ops Manager doesn't exit - was it removed accidentally? "+
				"Reconciliation cannot proceed, please create the API key using Ops Manager UI and create a secret manually.", 300)

		}
	}
	return nil
}

// checkOpsManagerInFullMode checks if the OM instance in "lightweight" or "full" mode.
// "lightweight" (appdb initialization) mode doesn't have the '/monitor/health' endpoint.
// in case of "full" mode the endpoint will report 200.
func checkOpsManagerInFullMode(om *v1.MongoDBOpsManager) (bool, error) {
	omUrl := centralURL(om)
	client, err := util.NewHTTPClient()
	if err != nil {
		return false, err
	}
	resp, err := client.Get(omUrl + "/monitor/health")
	if err != nil {
		return false, err
	}

	if resp.StatusCode == 404 {
		return false, nil
	} else if resp.StatusCode == 200 {
		return true, nil
	} else {
		return false, fmt.Errorf("unexpected HTTP status for /monitor/health, expected either 404 or 200 but got %d", resp.StatusCode)
	}
}

// initializeOpsManager ensures the admin user is created and the admin public key exists
func (r OpsManagerReconciler) initializeOpsManager(opsManager *v1.MongoDBOpsManager, log *zap.SugaredLogger) error {
	// We won't support cross-namespace secrets until CLOUDP-46636 is resolved
	secret := objectKey(opsManager.Namespace, opsManager.Spec.AdminSecret)

	// 1. Read the admin secret
	userData, err := r.kubeHelper.readSecret(secret)

	if apiErrors.IsNotFound(err) {
		return fmt.Errorf("the secret %s doesn't exist - you need to create it to finish Ops Manager initialization", secret)
	} else if err != nil {
		return err
	}

	user, err := newUserFromSecret(userData)
	if err != nil {
		return fmt.Errorf("failed to read user data from the secret %s: %s", secret, err)
	}

	// 2. Try to create a user in Ops Manager
	apiKey, agentKey, err := om.TryCreateUser(centralURL(opsManager), opsManager, user)
	if err != nil {
		return fmt.Errorf("failed to create an admin user in Ops Manager: %s", err)
	}

	// 3. Recreate an admin key secret in the Operator namespace
	adminKeySecretName := objectKey(operatorNamespace(), opsManager.APIKeySecretName())
	if apiKey != "" {
		log.Infof("Created an admin user %s with GLOBAL_ADMIN role", user.Username)

		// The structure matches the structure of a credentials secret used by normal mongodb resources
		secretData := map[string]string{util.OmPublicApiKey: apiKey, util.OmUser: user.Username}

		if err = r.kubeHelper.deleteSecret(adminKeySecretName); err != nil && !apiErrors.IsNotFound(err) {
			return r.cleanStatefulsetAndError(opsManager, fmt.Errorf("failed to replace a secret for admin public api key: %s", err))
		}
		if err = r.kubeHelper.createSecret(adminKeySecretName, secretData, map[string]string{}); err != nil {
			return r.cleanStatefulsetAndError(opsManager, fmt.Errorf("failed to create a secret for admin public api key: %s", err))
		}
		log.Infof("Created a secret for admin public api key %s", adminKeySecretName)
	}

	// 4. Recreate an agent key secret in the Operator namespace
	if agentKey != "" {
		agentKeySecretName := objectKey(operatorNamespace(), agentApiKeySecretName(BackingGroupId))
		if err = r.kubeHelper.deleteSecret(agentKeySecretName); err == nil || apiErrors.IsNotFound(err) {
			err = r.createAgentKeySecret(agentKeySecretName, agentKey)
		}
		if err != nil {
			log.Warnf("Failed to (re)create agent key secret %s, this is not critical, will try to create it again later: %s",
				agentKeySecretName, err)
		} else {
			log.Infof("Created a secret for agent key %s", agentKeySecretName)
		}
	}

	// 5. Final validation of current state - this could be the retry after failing to create the secret during
	// previous reconciliation (and the apiKey is empty as "the first user already exists") - the only fix is
	// start everything again
	_, err = r.kubeHelper.readSecret(adminKeySecretName)
	if err != nil {
		return r.cleanStatefulsetAndError(opsManager, fmt.Errorf("failed to read the admin key secret, is it the restart after the failed reconciliation? %s", err))
	}

	return nil
}

// cleanStatefulsetAndError is the "garbage collector" method which removes the statefulset for Ops Manager
// This must be used only when the Ops Manager is in "lightweight" mode as it's safe to remove it (no appdb yet)
func (r OpsManagerReconciler) cleanStatefulsetAndError(opsManager *v1.MongoDBOpsManager, err error) error {
	set := r.kubeHelper.NewOpsManagerStatefulSetHelper(opsManager).BuildStatefulSet()

	if err = r.kubeHelper.deleteStatefulSet(objectKey(set.Namespace, set.Name)); err != nil {
		return err
	}
	return err
}

// centralURL constructs the service name that can be used to access Ops Manager from within
// the cluster
func centralURL(om *v1.MongoDBOpsManager) string {
	fqdn := GetServiceFQDN(om.SvcName(), om.Namespace, om.ClusterName)

	// protocol must be calculated based on tls configuration of the ops manager resource
	protocol := "http"

	return fmt.Sprintf("%s://%s:%d", protocol, fqdn, util.OpsManagerDefaultPort)
}

func newUserFromSecret(data map[string]string) (*om.User, error) {
	// validate
	for _, v := range []string{"Username", "Password", "FirstName", "LastName"} {
		if _, ok := data[v]; !ok {
			return nil, fmt.Errorf("%s property is missing in the admin secret", v)
		}
	}
	user := &om.User{Username: data["Username"],
		Password:  data["Password"],
		FirstName: data["FirstName"],
		LastName:  data["LastName"],
	}
	return user, nil
}
