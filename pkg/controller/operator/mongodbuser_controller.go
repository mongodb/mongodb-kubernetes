package operator

import (
	"context"
	"errors"
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type MongoDBUserReconciler struct {
	*ReconcileCommonController
}

func newMongoDBUserReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *MongoDBUserReconciler {
	return &MongoDBUserReconciler{newReconcileCommonController(mgr, omFunc)}
}

func (r *MongoDBUserReconciler) getUser(request reconcile.Request, log *zap.SugaredLogger) (*v1.MongoDBUser, error) {
	user := &v1.MongoDBUser{}
	if _, err := r.getResource(request, user, log); err != nil {
		return nil, err
	}

	return user, nil
}

func (r *MongoDBUserReconciler) getMongoDBSpec(user v1.MongoDBUser) (v1.MongoDbSpec, error) {
	mdb := mongodb.MongoDB{}
	name := objectKey(user.Namespace, user.Spec.MongoDBResourceRef.Name)
	if err := r.client.Get(context.TODO(), name, &mdb); err != nil {
		return mdb.Spec, err
	}

	return mdb.Spec, nil
}

func (r *MongoDBUserReconciler) getConnectionSpec(user v1.MongoDBUser, mdbSpec v1.MongoDbSpec) (v1.ConnectionSpec, error) {
	if user.Spec.MongoDBResourceRef.Name != "" {
		return mdbSpec.ConnectionSpec, nil
	}

	// TODO: once we no longer need to support transition to from operator
	// versions <1.3 then we should be able to remove the rest of this function

	//lint:ignore SA1019 need to use deprecated Project to ensure backwards compatibility
	if user.Spec.Project == "" {
		return v1.ConnectionSpec{}, errors.New("either mongodb reference or project must be defined in user")
	}

	//lint:ignore SA1019 need to use deprecated Project to ensure backwards compatibility
	projectConfig, err := r.kubeHelper.readConfigMap(user.Namespace, user.Spec.Project)
	if err != nil {
		return v1.ConnectionSpec{}, err
	}

	// these parameters both existed in the old config map but are no longer
	// required for one project
	if _, hasProjectName := projectConfig["projectName"]; !hasProjectName {
		return v1.ConnectionSpec{}, errors.New("if using project config map, a project name must be defined")
	}

	if _, hasCredentials := projectConfig["credentials"]; !hasCredentials {
		return v1.ConnectionSpec{}, errors.New("if using project config map, credentials must be defined")
	}

	return v1.ConnectionSpec{
		OpsManagerConfig: v1.OpsManagerConfig{
			ConfigMapRef: v1.ConfigMapRef{
				//lint:ignore SA1019 need to use deprecated Project to ensure backwards compatibility
				Name: user.Spec.Project,
			},
		},
		Credentials: projectConfig["credentials"],
		ProjectName: projectConfig["projectName"],
	}, nil
}

func (r *MongoDBUserReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBUser", request.NamespacedName)
	log.Info("-> MongoDBUser.Reconcile")

	user, err := r.getUser(request, log)
	if err != nil {
		log.Warnf("error getting user %s", err)
		return retry()
	}

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(user, "Failed to reconcile MongoDB User", log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	log.Infow("MongoDBUser.Spec", "spec", user.Spec)
	mdbSpec := v1.MongoDbSpec{}
	if user.Spec.MongoDBResourceRef.Name != "" {
		if mdbSpec, err = r.getMongoDBSpec(*user); err != nil {
			return fail(err)
		}
	} else {
		log.Warn("MongoDB reference not specified. Using deprecated project field.")
	}
	log.Infow("MongoDBUser MongoDBSpec", "spec", mdbSpec)

	// this can happen when a user has registered a configmap as watched resource
	// but the user gets deleted. Reconciliation happens to this user even though it is deleted.
	// TODO: unregister config map upon MongoDBUser deletion
	if user.Namespace == "" && user.Name == "" {
		return stop()
	}

	connSpec, err := r.getConnectionSpec(*user, mdbSpec)
	if err != nil {
		return fail(err)
	}

	conn, err := r.prepareConnection(request.NamespacedName, connSpec, nil, log)
	if err != nil {
		return r.updateStatusFailed(user, fmt.Sprintf("failed to prepare Ops Manager connection. %s", err), log)
	}

	if x509IsEnabled, err := r.isX509Enabled(*user, mdbSpec); err != nil {
		return fail(err)
	} else if !x509IsEnabled {
		log.Info("X509 authentication is not enabled for this project, stopping")
		return stop()
	}

	return r.handleX509User(user, conn, log)
}

func (r *MongoDBUserReconciler) isX509Enabled(user v1.MongoDBUser, mdbSpec v1.MongoDbSpec) (bool, error) {
	if user.Spec.MongoDBResourceRef.Name != "" {
		authEnabled := mdbSpec.Security.Authentication.Enabled
		usingX509 := util.ContainsString(mdbSpec.Security.Authentication.Modes, util.X509)
		return authEnabled && usingX509, nil
	}

	// TODO: remove the rest of this function when backwards compatibility with
	// versions of the operator <1.3 is no longer required

	//lint:ignore SA1019 need to use deprecated Project to ensure backwards compatibility
	projectConfig, err := r.kubeHelper.readConfigMap(user.Namespace, user.Spec.Project)
	if err != nil {
		return false, err
	}

	return projectConfig[util.OmAuthMode] == util.LegacyX509InConfigMapValue, nil
}

func (r *MongoDBUserReconciler) delete(obj interface{}, log *zap.SugaredLogger) error {
	user := obj.(*v1.MongoDBUser)

	mdbSpec, err := r.getMongoDBSpec(*user)
	if err != nil {
		return err
	}

	connSpec, err := r.getConnectionSpec(*user, mdbSpec)
	if err != nil {
		return err
	}

	conn, err := r.prepareConnection(objectKey(user.Namespace, user.Name), connSpec, nil, log)
	if err != nil {
		log.Errorf("error preparing connection to Ops Manager: %s", err)
		return err
	}

	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.EnsureUserRemoved(user.Spec.Username, user.Spec.Database)
		return nil
	}, log)
}

func AddMongoDBUserController(mgr manager.Manager) error {
	// Create a new controller
	reconciler := newMongoDBUserReconciler(mgr, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbUserController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}},
		&ConfigMapAndSecretHandler{resourceType: ConfigMap, trackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&ConfigMapAndSecretHandler{resourceType: Secret, trackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	// watch for changes to MongoDBUser resources
	eventHandler := MongoDBUserEventHandler{reconciler: reconciler}
	err = c.Watch(&source.Kind{Type: &v1.MongoDBUser{}}, &eventHandler, predicatesForUser())
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbUserController)
	return nil
}

// toOmUser converts a MongoDBUser specification and optional password into an
// automation config MongoDB user. If the user has no password then a blank
// password should be provided.
func toOmUser(spec v1.MongoDBUserSpec, password string) om.MongoDBUser {

	user := om.MongoDBUser{
		Database:                   spec.Database,
		Username:                   spec.Username,
		Roles:                      []*om.Role{},
		AuthenticationRestrictions: []string{},
		Mechanisms:                 []string{},
	}

	for _, r := range spec.Roles {
		user.AddRole(&om.Role{Role: r.RoleName, Database: r.Database})
	}
	return user
}

func (r *MongoDBUserReconciler) handleX509User(user *v1.MongoDBUser, conn om.Connection, log *zap.SugaredLogger) (res reconcile.Result, e error) {
	if !r.doAgentX509CertsExist(user.Namespace) {
		log.Info("Agent certs have not yet been created, cannot add MongoDBUser yet")
		return retry()
	}

	shouldRetry := false
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !util.ContainsString(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) {
			shouldRetry = true
			return fmt.Errorf("x509 has not yet been configured")
		}
		auth := ac.Auth

		if user.ChangedIdentifier() { // we've changed username or database, we need to remove the old user before adding new
			auth.RemoveUser(user.Status.Username, user.Status.Database)
		}
		desiredUser := toOmUser(user.Spec, "")
		auth.EnsureUser(desiredUser)
		return nil
	}, log)

	if shouldRetry {
		log.Info("x509 has not yet been configured")
		return retry()
	}
	if err != nil {
		return r.updateStatusFailed(user, fmt.Sprintf("error updating user %s", err), log)
	}

	log.Infof("Finished reconciliation for MongoDBUser!")
	return r.updateStatusSuccessful(user, log)
}
