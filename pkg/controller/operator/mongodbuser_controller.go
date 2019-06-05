package operator

import (
	"fmt"

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

func (r *MongoDBUserReconciler) getConnectionSpec(user *v1.MongoDBUser) (v1.ConnectionSpec, error) {
	projectConfig, err := r.kubeHelper.readConfigMap(user.Namespace, user.Spec.Project)
	if err != nil {
		return v1.ConnectionSpec{}, err
	}
	return v1.ConnectionSpec{
		Project:     user.Spec.Project,
		Credentials: projectConfig["credentials"],
	}, nil
}

func (r *MongoDBUserReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBUser", request.NamespacedName)

	user, err := r.getUser(request, log)

	defer exceptionHandling(
		func(err interface{}) (reconcile.Result, error) {
			return r.updateStatusFailed(user, "Failed to reconcile MongoDB User", log)
		},
		func(result reconcile.Result, err error) { res = result; e = err },
	)

	if err != nil {
		log.Warnf("error getting user %s", err)
		return retry()
	}

	projectConfig, err := r.kubeHelper.readConfigMap(request.Namespace, user.Spec.Project)
	if err != nil {
		return r.updateStatusFailed(user, fmt.Sprintf("Error reading project config. %s", err), log)
	}

	connSpec := v1.ConnectionSpec{
		Project:     user.Spec.Project,
		Credentials: projectConfig["credentials"],
	}

	log.Info("-> MongoDBUser.Reconcile")
	log.Infow("MongoDBUser.Spec", "spec", user.Spec)
	log.Infow("MongoDBUser.Status", "status", user.Status)
	log.Infow("Connection Spec", "connSpec", connSpec)

	conn, err := r.prepareConnection(request.NamespacedName, connSpec, nil, log)
	if err != nil {
		return r.updateStatusFailed(user, fmt.Sprintf("failed to prepare Ops Manager connection. %s", err), log)
	}

	err = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		auth := ac.Auth
		desiredUser := toOmUser(user.Spec)
		if auth.HasUser(desiredUser.Username, desiredUser.Database) {
			auth.UpdateUser(desiredUser.Username, desiredUser.Database, desiredUser)
		} else {
			auth.AddUser(desiredUser)
		}
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if err != nil {
		return r.updateStatusFailed(user, fmt.Sprintf("error updating user %s", err), log)
	}

	log.Infof("Finished reconciliation for MongoDBUser!")
	r.updateStatusSuccessful(user, log)
	return success()
}

func (r *MongoDBUserReconciler) delete(obj interface{}, log *zap.SugaredLogger) error {
	user := obj.(*v1.MongoDBUser)

	connectionSpec, err := r.getConnectionSpec(user)
	if err != nil {
		log.Info("error getting connection spec from user %s. %s", user.Name, err)
		return err
	}

	conn, err := r.prepareConnection(objectKey(user.Namespace, user.Name), connectionSpec, nil, log)

	if err != nil {
		log.Errorf("error preparing connection to Ops Manager: %s", err)
		return err
	}

	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.RemoveUser(user.Spec.Username, user.Spec.Database)
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)
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
	// watch for changes to MongoDBUser resources
	eventHandler := MongoDBUserEventHandler{reconciler: reconciler}
	err = c.Watch(&source.Kind{Type: &v1.MongoDBUser{}}, &eventHandler)
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbUserController)
	return nil
}

func toOmUser(spec v1.MongoDBUserSpec) om.MongoDBUser {
	user := om.MongoDBUser{
		Database:                   spec.Database,
		Username:                   spec.Username,
		Roles:                      []om.Role{},
		AuthenticationRestrictions: []string{},
		Mechanisms:                 []string{},
	}
	for _, r := range spec.Roles {
		user.AddRole(om.Role{Role: r.RoleName, Database: r.Database})
	}
	return user
}
