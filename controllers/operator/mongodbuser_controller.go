package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type ClusterType string

const (
	Single = "single"
	Multi  = "multi"
)

type MongoDBUserReconciler struct {
	*ReconcileCommonController
	omConnectionFactory om.ConnectionFactory
}

func newMongoDBUserReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *MongoDBUserReconciler {
	return &MongoDBUserReconciler{
		ReconcileCommonController: newReconcileCommonController(mgr),
		omConnectionFactory:       omFunc,
	}
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

// getMongoDB return a MongoDB deployment of type Single or Multi cluster based on the clusterType passed
func (r *MongoDBUserReconciler) getMongoDB(user userv1.MongoDBUser, clusterType ClusterType) (project.Reader, error) {
	name := kube.ObjectKey(user.Namespace, user.Spec.MongoDBResourceRef.Name)

	if clusterType == Single {
		mdb := &mdbv1.MongoDB{}
		if err := r.client.Get(context.TODO(), name, mdb); err != nil {
			return mdb, err
		}
		return mdb, nil
	}

	mdbm := &mdbmulti.MongoDBMulti{}
	if err := r.client.Get(context.TODO(), name, mdbm); err != nil {
		return mdbm, err
	}
	return mdbm, nil
}

// getProjectReader returns a project.Reader object corresponsing to MongoDB single or multi-cluster deployment
func (r *MongoDBUserReconciler) getProjectReader(user userv1.MongoDBUser) (project.Reader, error) {
	// first try to fetch if MongoDB Single Cluster deployment exists
	mdb, err := r.getMongoDB(user, Single)
	if err != nil && apiErrors.IsNotFound(err) {
		// try to fetch MongoDB Multi Cluster deployment if couldn't find single cluster resource
		return r.getMongoDB(user, Multi)
	}
	return mdb, err
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbusers,mongodbusers/status,mongodbusers/finalizers},verbs=*,namespace=placeholder

// Reconciles a mongodbusers.mongodb.com Custom resource.
func (r *MongoDBUserReconciler) Reconcile(_ context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBUser", request.NamespacedName)
	log.Info("-> MongoDBUser.Reconcile")

	user, err := r.getUser(request, log)
	if err != nil {
		log.Warnf("error getting user %s", err)
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, nil
	}

	log.Infow("MongoDBUser.Spec", "spec", user.Spec)
	var mdb project.Reader

	if user.Spec.MongoDBResourceRef.Name != "" {
		if mdb, err = r.getProjectReader(*user); err != nil {
			log.Warnf("Couldn't fetch MongoDB Single/Multi Cluster Resource with name: %s, err: %s", user.Spec.MongoDBResourceRef.Name, err)
			return r.updateStatus(user, workflow.Pending(err.Error()), log)
		}
	} else {
		log.Warn("MongoDB reference not specified. Using deprecated project field.")
	}

	// this can happen when a user has registered a configmap as watched resource
	// but the user gets deleted. Reconciliation happens to this user even though it is deleted.
	// TODO: unregister config map upon MongoDBUser deletion
	if user.Namespace == "" && user.Name == "" {
		// stop reconciliation
		return reconcile.Result{}, nil
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, mdb, log)
	if err != nil {
		return r.updateStatus(user, workflow.Failed(err.Error()), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, user.Namespace, log)
	if err != nil {
		return r.updateStatus(user, workflow.Failed("Failed to prepare Ops Manager connection: %s", err), log)
	}

	if user.Spec.Database == authentication.ExternalDB {
		return r.handleExternalAuthUser(user, conn, log)
	} else {
		return r.handleScramShaUser(user, conn, log)
	}
}

func (r *MongoDBUserReconciler) delete(obj interface{}, log *zap.SugaredLogger) error {
	user := obj.(*userv1.MongoDBUser)

	mdb, err := r.getProjectReader(*user)
	if err != nil {
		return err
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, mdb, log)
	if err != nil {
		return err
	}

	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, user.Namespace, log)
	if err != nil {
		log.Errorf("Failed to prepare Ops Manager connection: %s", err)
		return err
	}

	r.RemoveAllDependentWatchedResources(user.Namespace, kube.ObjectKeyFromApiObject(user))

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
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.WatchedResources})
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
	if spec.Database != authentication.ExternalDB {
		if err := authentication.ConfigureScramCredentials(&user, password); err != nil {
			return om.MongoDBUser{}, fmt.Errorf("error generating SCRAM credentials: %s", err)
		}
	}

	for _, r := range spec.Roles {
		user.AddRole(&om.Role{Role: r.RoleName, Database: r.Database})
	}
	return user, nil
}

func (r *MongoDBUserReconciler) handleScramShaUser(user *userv1.MongoDBUser, conn om.Connection, log *zap.SugaredLogger) (res reconcile.Result, e error) {
	// watch the password secret in order to trigger reconciliation if the
	// password is updated
	if user.Spec.PasswordSecretKeyRef.Name != "" {
		r.AddWatchedResourceIfNotAdded(
			user.Spec.PasswordSecretKeyRef.Name,
			user.Namespace,
			watch.Secret,
			kube.ObjectKeyFromApiObject(user),
		)
	}

	shouldRetry := false
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if ac.Auth.Disabled ||
			(!stringutil.ContainsAny(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigScramSha256Option, util.AutomationConfigScramSha1Option)) {
			shouldRetry = true
			return fmt.Errorf("scram Sha has not yet been configured")
		}

		password, err := user.GetPassword(r.client)
		if err != nil {
			return err
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

	annotationsToAdd, err := getAnnotationsForUserResource(user)
	if err != nil {
		return r.updateStatus(user, workflow.Failed(err.Error()), log)
	}

	if err := annotations.SetAnnotations(user.DeepCopy(), annotationsToAdd, r.client); err != nil {
		return r.updateStatus(user, workflow.Failed(err.Error()), log)
	}

	log.Infof("Finished reconciliation for MongoDBUser!")
	return r.updateStatus(user, workflow.OK(), log)
}

func (r *MongoDBUserReconciler) handleExternalAuthUser(user *userv1.MongoDBUser, conn om.Connection, log *zap.SugaredLogger) (reconcile.Result, error) {
	desiredUser, err := toOmUser(user.Spec, "")
	if err != nil {
		return r.updateStatus(user, workflow.Failed("error updating user %s", err), log)
	}

	shouldRetry := false
	updateFunction := func(ac *om.AutomationConfig) error {
		if !externalAuthMechanismsAvailable(ac.Auth.DeploymentAuthMechanisms) {
			shouldRetry = true
			return fmt.Errorf("no external authentication mechanisms (LDAP or x509) have been configured")
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

	annotationsToAdd, err := getAnnotationsForUserResource(user)
	if err != nil {
		return r.updateStatus(user, workflow.Failed(err.Error()), log)
	}

	if err := annotations.SetAnnotations(user.DeepCopy(), annotationsToAdd, r.client); err != nil {
		return r.updateStatus(user, workflow.Failed(err.Error()), log)
	}

	log.Infow("Finished reconciliation for MongoDBUser!")
	return r.updateStatus(user, workflow.OK(), log)
}

func externalAuthMechanismsAvailable(mechanisms []string) bool {
	return stringutil.ContainsAny(mechanisms, util.AutomationConfigLDAPOption, util.AutomationConfigX509Option)
}

func getAnnotationsForUserResource(user *userv1.MongoDBUser) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(user.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)
	return finalAnnotations, nil
}
