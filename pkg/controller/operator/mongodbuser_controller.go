package operator

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	userv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/user"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
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

func (r *MongoDBUserReconciler) getUser(request reconcile.Request, log *zap.SugaredLogger) (*userv1.MongoDBUser, error) {
	user := &userv1.MongoDBUser{}
	if _, err := r.getResource(request, user, log); err != nil {
		return nil, err
	}

	// if database isn't specified default to the admin database, the recommended
	// place for creating non-$external users
	if user.Spec.Database == "" {
		user.Spec.Database = "admin"
	}

	return user, nil
}

func (r *MongoDBUserReconciler) getMongoDB(user userv1.MongoDBUser) (mdbv1.MongoDB, error) {
	mdb := mdbv1.MongoDB{}
	name := objectKey(user.Namespace, user.Spec.MongoDBResourceRef.Name)
	if err := r.client.Get(context.TODO(), name, &mdb); err != nil {
		return mdb, err
	}

	return mdb, nil
}

func (r *MongoDBUserReconciler) getConnectionSpec(user userv1.MongoDBUser, mdbSpec mdbv1.MongoDbSpec) (mdbv1.ConnectionSpec, error) {
	if user.Spec.MongoDBResourceRef.Name != "" {
		return mdbSpec.ConnectionSpec, nil
	}

	// TODO: once we no longer need to support transition to from operator
	// versions <1.3 then we should be able to remove the rest of this function

	//lint:ignore SA1019 need to use deprecated Project to ensure backwards compatibility
	if user.Spec.Project == "" {
		return mdbv1.ConnectionSpec{}, errors.New("either mongodb reference or project must be defined in user")
	}

	//lint:ignore SA1019 need to use deprecated Project to ensure backwards compatibility
	projectConfig, err := r.kubeHelper.configmapClient.GetData(objectKey(user.Namespace, user.Spec.Project))
	if err != nil {
		return mdbv1.ConnectionSpec{}, err
	}

	// these parameters both existed in the old config map but are no longer
	// required for one project
	if _, hasProjectName := projectConfig["projectName"]; !hasProjectName {
		return mdbv1.ConnectionSpec{}, errors.New("if using project config map, a project name must be defined")
	}

	if _, hasCredentials := projectConfig["credentials"]; !hasCredentials {
		return mdbv1.ConnectionSpec{}, errors.New("if using project config map, credentials must be defined")
	}

	return mdbv1.ConnectionSpec{
		OpsManagerConfig: &mdbv1.PrivateCloudConfig{
			ConfigMapRef: mdbv1.ConfigMapRef{
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
			return r.updateStatus(user, workflow.Failed("Failed to reconcile MongoDB User"), log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	log.Infow("MongoDBUser.Spec", "spec", user.Spec)
	mdb := mdbv1.MongoDB{}
	if user.Spec.MongoDBResourceRef.Name != "" {
		if mdb, err = r.getMongoDB(*user); err != nil {
			return r.updateStatus(user, workflow.Pending(err.Error()), log)
		}
	} else {
		log.Warn("MongoDB reference not specified. Using deprecated project field.")
	}
	log.Infow("MongoDBUser MongoDBSpec", "spec", mdb.Spec)

	// this can happen when a user has registered a configmap as watched resource
	// but the user gets deleted. Reconciliation happens to this user even though it is deleted.
	// TODO: unregister config map upon MongoDBUser deletion
	if user.Namespace == "" && user.Name == "" {
		return stop()
	}

	connSpec, err := r.getConnectionSpec(*user, mdb.Spec)
	if err != nil {
		return fail(err)
	}

	conn, err := r.prepareConnection(request.NamespacedName, connSpec, nil, log)
	if err != nil {
		return r.updateStatus(user, workflow.Failed("failed to prepare Ops Manager connection. %s", err), log)
	}

	currentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return r.updateStatus(user, workflow.Failed(err.Error()), log)
	}

	if user.Spec.Database == util.ExternalAuthenticationDB {
		return r.handleExternalAuthDatabaseUser(user, mdb, conn, currentAuthMode, log)
	} else {
		return r.handleScramShaUser(user, conn, log)
	}

}

func (r *MongoDBUserReconciler) handleExternalAuthDatabaseUser(user *userv1.MongoDBUser, mdb mdbv1.MongoDB, conn om.Connection, currentAuthMode string, log *zap.SugaredLogger) (reconcile.Result, error) {
	x509Enabled := stringutil.Contains(mdb.Spec.Security.Authentication.Modes, util.X509)
	ldapEnabled := stringutil.Contains(mdb.Spec.Security.Authentication.Modes, util.LDAP)
	if x509Enabled && !ldapEnabled {
		return r.handleX509User(user, mdb, conn, currentAuthMode, log)
	}
	if ldapEnabled && !x509Enabled {
		return r.handleLDAPUser(user, mdb, conn, log)
	}
	log.Infof("Authentication.Modes: %+v", mdb.Spec.Security.Authentication.Modes)

	// TODO: Handle case where both LDAP and x509 authentication mechanisms exists
	// maybe decouple the x509 setup from the user creation, this is, making
	// $external users generic. It is up to MongoDB to see which backed to use.

	if x509Enabled && ldapEnabled {
		return fail(errors.New("attempted to create user on $external database, but there are multiple $external databases enabled"))
	}
	return fail(errors.New("attempted to create user on $external database, but there are no backends enabled (LDAP or x509)"))
}

func (r *MongoDBUserReconciler) isX509Enabled(user userv1.MongoDBUser, mdbSpec mdbv1.MongoDbSpec) (bool, error) {
	if user.Spec.MongoDBResourceRef.Name != "" {
		authEnabled := mdbSpec.Security.Authentication.Enabled
		usingX509 := stringutil.Contains(mdbSpec.Security.Authentication.Modes, util.X509)
		return authEnabled && usingX509, nil
	}

	// TODO: remove the rest of this function when backwards compatibility with
	// versions of the operator <1.3 is no longer required

	//lint:ignore SA1019 need to use deprecated Project to ensure backwards compatibility
	omAuthMode, err := r.kubeHelper.configmapClient.ReadKey(util.OmAuthMode, objectKey(user.Namespace, user.Spec.Project))
	if err != nil {
		return false, err
	}
	return omAuthMode == util.LegacyX509InConfigMapValue, nil
}

func (r *MongoDBUserReconciler) delete(obj interface{}, log *zap.SugaredLogger) error {
	user := obj.(*userv1.MongoDBUser)

	mdb, err := r.getMongoDB(*user)
	if err != nil {
		return err
	}

	connSpec, err := r.getConnectionSpec(*user, mdb.Spec)
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
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, TrackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.watchedResources})
	if err != nil {
		return err
	}

	// watch for changes to MongoDBUser resources
	eventHandler := MongoDBUserEventHandler{reconciler: reconciler}
	err = c.Watch(&source.Kind{Type: &userv1.MongoDBUser{}}, &eventHandler, watch.PredicatesForUser())
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbUserController)
	return nil
}

// toOmUser converts a MongoDBUser specification and optional password into an
// automation config MongoDB user. If the user has no password then a blank
// password should be provided.
func toOmUser(spec userv1.MongoDBUserSpec, password string) (om.MongoDBUser, error) {
	user := om.MongoDBUser{
		Database:                   spec.Database,
		Username:                   spec.Username,
		Roles:                      []*om.Role{},
		AuthenticationRestrictions: []string{},
		Mechanisms:                 []string{},
	}

	// only specify password if we're dealing with non-x509 users
	if spec.Database != util.X509Db {
		if err := authentication.ConfigureScramCredentials(&user, password); err != nil {
			return om.MongoDBUser{}, fmt.Errorf("error generating SCRAM credentials: %s", err)
		}
	}

	for _, r := range spec.Roles {
		user.AddRole(&om.Role{Role: r.RoleName, Database: r.Database})
	}
	return user, nil
}

// ensureAgentUsers makes sure that the correct agent users are present in the Automation Config.
// it is not possible to assume the agent users are using the same authentication mechanism as the user being added
func (r *MongoDBUserReconciler) ensureAgentUsers(ac *om.AutomationConfig, user userv1.MongoDBUser, log *zap.SugaredLogger) error {
	mdb, err := r.getMongoDB(user)
	if err != nil {
		return fmt.Errorf("error reading MongoDB resource: %s", err)
	}
	log = log.With("MongoDB", mdb.ObjectKey())

	// if we are dealing with X509 agent users, or ever had X509 agent users, the secret containing the subjects should exist
	userOpts, err := r.readAgentSubjectsFromSecret(mdb.Namespace, log)
	err = client.IgnoreNotFound(err)
	if err != nil {
		return fmt.Errorf("error reading agent subjects from secret: %s", err)
	}

	// determine the agent auth mechanism based on the MongoDB resource
	currentMode := mdb.Spec.Security.GetAgentMechanism(ac.Auth.AutoAuthMechanism)
	if currentMode == "" {
		log.Infof("no authentication currently configured for resource, not ensuring agent users")
		return nil
	}

	var mn authentication.MechanismName
	if currentMode == util.X509 {
		mn = authentication.MongoDBX509
	} else if currentMode == util.SCRAM {
		mn = authentication.ScramSha256
	}

	log.Debugf("ensuring agent users, using %s authentication", string(mn))
	return authentication.EnsureAgentUsers(userOpts, ac, mn)
}

func (r *MongoDBUserReconciler) handleScramShaUser(user *userv1.MongoDBUser, conn om.Connection, log *zap.SugaredLogger) (res reconcile.Result, e error) {
	// watch the password secret in order to trigger reconciliation if the
	// password is updated
	if user.Spec.PasswordSecretKeyRef.Name != "" {
		userNamespacedName := types.NamespacedName{
			Name:      user.Name,
			Namespace: user.Namespace,
		}
		r.addWatchedResourceIfNotAdded(
			user.Spec.PasswordSecretKeyRef.Name,
			user.Namespace,
			watch.Secret,
			userNamespacedName,
		)
	}

	shouldRetry := false
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigScramSha256Option) && !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigScramSha1Option) {
			shouldRetry = true
			return fmt.Errorf("scram Sha has not yet been configured")
		}

		password, err := user.GetPassword(r.client)
		if err != nil {
			return err
		}

		if err := r.ensureAgentUsers(ac, *user, log); err != nil {
			return fmt.Errorf("error ensuring agent users: %s", err)
		}

		auth := ac.Auth
		if user.ChangedIdentifier() { // we've changed username or database, we need to remove the old user before adding new
			auth.RemoveUser(user.Status.Username, user.Status.Database)
		}

		desiredUser, err := toOmUser(user.Spec, password)
		if err != nil {
			return err
		}

		auth.EnsureUser(desiredUser)
		return nil
	}, log)

	if err != nil {
		if shouldRetry {
			return r.updateStatus(user, workflow.Pending(err.Error()).WithRetry(10), log)
		}
		return r.updateStatus(user, workflow.Failed("error updating user %s", err), log)
	}

	log.Infof("Finished reconciliation for MongoDBUser!")
	return r.updateStatus(user, workflow.OK(), log)
}

func (r *MongoDBUserReconciler) handleX509User(user *userv1.MongoDBUser, mdb mdbv1.MongoDB, conn om.Connection, currentAuthMode string, log *zap.SugaredLogger) (reconcile.Result, error) {

	if x509IsEnabled, err := r.isX509Enabled(*user, mdb.Spec); err != nil {
		return fail(err)
	} else if !x509IsEnabled {
		log.Info("X509 authentication is not enabled for this project, stopping")
		return stop()
	}

	security := mdb.Spec.Security

	if security.ShouldUseX509(currentAuthMode) && !r.doAgentX509CertsExist(user.Namespace) {
		log.Info("Agent certs have not yet been created, cannot add MongoDBUser yet")
		return retry()
	}

	shouldRetry := false
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) {
			shouldRetry = true
			return fmt.Errorf("x509 has not yet been configured")
		}

		if err := r.ensureAgentUsers(ac, *user, log); err != nil {
			return fmt.Errorf("error ensuring agent users: %s", err)
		}

		auth := ac.Auth
		if user.ChangedIdentifier() { // we've changed username or database, we need to remove the old user before adding new
			auth.RemoveUser(user.Status.Username, user.Status.Database)
		}

		desiredUser, err := toOmUser(user.Spec, "")
		if err != nil {
			return err
		}
		auth.EnsureUser(desiredUser)
		return nil
	}, log)

	if err != nil {
		if shouldRetry {
			return r.updateStatus(user, workflow.Pending(err.Error()).WithRetry(10), log)
		}
		return r.updateStatus(user, workflow.Failed("error updating user %s", err), log)
	}

	log.Infow("Finished reconciliation for MongoDBUser!")
	return r.updateStatus(user, workflow.OK(), log)
}

func (r *MongoDBUserReconciler) handleLDAPUser(user *userv1.MongoDBUser, mdb mdbv1.MongoDB, conn om.Connection, log *zap.SugaredLogger) (reconcile.Result, error) {
	desiredUser, err := toOmUser(user.Spec, "")
	if err != nil {
		return r.updateStatus(user, workflow.Failed("error updating user %s", err), log)
	}

	shouldRetry := false
	updateFunction := func(ac *om.AutomationConfig) error {
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigLDAPOption) {
			shouldRetry = true
			return fmt.Errorf("LDAP has not yet been configured")
		}

		auth := ac.Auth
		if user.ChangedIdentifier() {
			auth.RemoveUser(user.Status.Username, user.Status.Database)
		}

		auth.EnsureUser(desiredUser)
		return nil
	}

	err = conn.ReadUpdateAutomationConfig(updateFunction, log)
	if err != nil {
		if shouldRetry {
			return r.updateStatus(user, workflow.Pending(err.Error()).WithRetry(10), log)
		}
		return r.updateStatus(user, workflow.Failed("error updating user %s", err), log)
	}

	log.Infow("Finished reconciliation for MongoDBUser!")
	return r.updateStatus(user, workflow.OK(), log)
}
