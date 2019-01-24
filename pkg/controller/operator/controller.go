package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TODO rename the file to "common_controller.go" later

// ReconcileCommonController is the "parent" controller that is included into each specific controller and allows
// to reuse the common functionality
type ReconcileCommonController struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client           client.Client
	scheme           *runtime.Scheme
	kubeHelper       KubeHelper
	omConnectionFunc om.ConnectionFunc
	// internal multimap mapping watched resources to mongodb resources they are used in
	// (example: config map 'c1' is used in 2 mongodb replica sets 'm1', 'm2', so the map will be [c1]->[m1, m2])
	watchedResources map[watchedObject][]types.NamespacedName
}

func newReconcileCommonController(mgr manager.Manager, omFunc om.ConnectionFunc) *ReconcileCommonController {
	return &ReconcileCommonController{
		client:           mgr.GetClient(),
		scheme:           mgr.GetScheme(),
		kubeHelper:       KubeHelper{mgr.GetClient()},
		omConnectionFunc: omFunc,
		watchedResources: map[watchedObject][]types.NamespacedName{},
	}
}

// prepareConnection reads project config map and credential secrets and uses these values to communicate with Ops Manager:
// create or read the group and optionally request an agent key (it could have been returned by group api call)
func (c *ReconcileCommonController) prepareConnection(nsName types.NamespacedName, spec v1.CommonSpec, podVars *PodVars, log *zap.SugaredLogger) (om.Connection, error) {
	projectConfig, err := c.kubeHelper.readProjectConfig(nsName.Namespace, spec.Project)
	if err != nil {
		return nil, fmt.Errorf("Error reading Project Config: %s", err)
	}
	credsConfig, err := c.kubeHelper.readCredentials(nsName.Namespace, spec.Credentials)
	if err != nil {
		return nil, fmt.Errorf("Error reading Credentials secret: %s", err)
	}

	c.registerWatchedResources(nsName, spec.Project, spec.Credentials)

	group, err := c.readOrCreateGroup(projectConfig, credsConfig, log)
	if err != nil {
		return nil, fmt.Errorf("Error reading or creating project in Ops Manager: %s", err)
	}

	conn := c.omConnectionFunc(projectConfig.BaseURL, group.ID, credsConfig.User, credsConfig.PublicAPIKey)
	agentAPIKey, err := c.ensureAgentKeySecretExists(conn, nsName.Namespace, group.AgentAPIKey, log)
	if err != nil {
		return nil, err
	}

	if podVars != nil {
		// Register podVars if user passed a valid reference to a PodVars object
		podVars.BaseURL = conn.BaseURL()
		podVars.ProjectID = conn.GroupID()
		podVars.User = conn.User()
		podVars.AgentAPIKey = agentAPIKey
		podVars.LogLevel = spec.LogLevel
	}
	return conn, nil
}

// ensureAgentKeySecretExists checks if the Secret with specified name (<groupId>-group-secret) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key. Generation of a key is expected to be
// a rare operation as the group creation api generates agent key already (so the only possible situation is when the group
// was created externally and agent key wasn't generated before)
// Returns the api key existing/generated
func (c *ReconcileCommonController) ensureAgentKeySecretExists(conn om.Connection, nameSpace, agentKey string, log *zap.SugaredLogger) (string, error) {
	secretName := agentApiKeySecretName(conn.GroupID())
	log = log.With("secret", secretName)
	secret := &corev1.Secret{}
	err := c.client.Get(context.TODO(),
		objectKey(nameSpace, secretName),
		secret)
	if err != nil {
		if agentKey == "" {
			log.Info("Generating agent key as current group doesn't have it")

			agentKey, err = conn.GenerateAgentKey()
			if err != nil {
				return "", fmt.Errorf("Failed to generate agent key in OM: %s", err)
			}
			log.Info("Agent key was successfully generated")
		}

		secret = buildSecret(secretName, nameSpace, agentKey)
		if err = c.client.Create(context.TODO(), secret); err != nil {
			return "", fmt.Errorf("Failed to create Secret: %s", err)
		}
		log.Infof("Group agent key is saved in Kubernetes Secret for later usage")
	}

	return strings.TrimSuffix(string(secret.Data[util.OmAgentApiKey]), "\n"), nil
}

func (c *ReconcileCommonController) updateStatusSuccessful(resource v1.StatusUpdater, log *zap.SugaredLogger) {
	// Sometimes we get the "Operation cannot be fulfilled on mongodbstandalones.mongodb.com : the object has
	// been modified; please apply your changes to the latest version and try again" error - so let's fetch the latest
	// object before updating it
	_ = c.client.Get(context.TODO(), objectKeyFromApiObject(resource), resource)

	resource.UpdateSuccessful()

	// Dev note: "c.client.Status().Update()" doesn't work for some reasons and the examples from Operator SDK use the
	// normal resource update - so we use the same
	// None of the following seems to work!
	// err := c.client.Status().Update(context.TODO(), resource)
	err := c.client.Update(context.TODO(), resource)
	if err != nil {
		log.Errorf("Failed to update status for resource to successful: %s", err)
	}
}

func (c *ReconcileCommonController) updateStatusFailed(resource v1.StatusUpdater, msg string, log *zap.SugaredLogger) (reconcile.Result, error) {
	log.Error(msg)
	// Resource may be nil if the reconciliation failed very early (on fetching the resource) and panic handling function
	// took over
	if resource != nil {
		resource.UpdateError(msg)
		err := c.client.Update(context.TODO(), resource)
		if err != nil {
			log.Errorf("Failed to update resource status: %s", err)
			return reconcile.Result{RequeueAfter: 10 * time.Second}, err
		}
	}
	return reconcile.Result{RequeueAfter: 10 * time.Second}, errors.New(msg)
}

func needsDeletion(meta v1.Meta) bool {
	return !meta.DeletionTimestamp.IsZero()
}

// reconcileDeletion checks the headers and if 'util.MongodbResourceFinalizer' header is present - tries to cleanup Ops Manager
// through calling 'cleanupFunc'. Cleanup is requeued in case of any troubles. Otherwise the header is removed from custom
// resource
func (c *ReconcileCommonController) reconcileDeletion(cleanupFunc func(obj interface{}, log *zap.SugaredLogger) error,
	res v1.StatusUpdater, objectMeta *metav1.ObjectMeta, log *zap.SugaredLogger) (reconcile.Result, error) {
	// Object is being removed - let's check for finalizers left
	if util.ContainsString(objectMeta.Finalizers, util.MongodbResourceFinalizer) {
		if err := cleanupFunc(res, log); err != nil {
			// Important: we are not retrying cleanup as there can be situations when this will block deletion forever (examples:
			// config map changed/removed, Ops Manager deleted etc)
			// TODO Ideally we should retry for N times though
			log.Errorf("Failed to cleanup Ops Manager state, proceeding anyway: %s", err)
		}

		// remove our finalizer from the list and update it.
		// (Sometimes we get the "the object has been modified" error - so let's fetch the latest object before updating it)
		_ = c.client.Get(context.TODO(), objectKeyFromApiObject(res), res)
		objectMeta.Finalizers = util.RemoveString(objectMeta.Finalizers, util.MongodbResourceFinalizer)
		if err := c.client.Update(context.Background(), res.(runtime.Object)); err != nil {
			return c.updateStatusFailed(res, fmt.Sprintf("Failed to update object finalizer headers: %s", err), log)
		}
		log.Debug("Removed finalizer header")
	} else {
		log.Warnf("Why was reconcileDeletion() function called but there is no %s header?", util.MongodbResourceFinalizer)
	}

	// Our finalizer has finished, so the reconciler can do nothing.
	return reconcile.Result{}, nil
}

// ensureFinalizerHeaders adds the finalizer header to custom resource if it doesn't exist
// see https://book.kubebuilder.io/beyond_basics/using_finalizers.html
func (c *ReconcileCommonController) ensureFinalizerHeaders(res runtime.Object, objectMeta *metav1.ObjectMeta, log *zap.SugaredLogger) error {
	if !util.ContainsString(objectMeta.Finalizers, util.MongodbResourceFinalizer) {
		objectMeta.Finalizers = append(objectMeta.Finalizers, util.MongodbResourceFinalizer)
		if err := c.client.Update(context.Background(), res); err != nil {
			return err
		}
		log.Debug("Added finalizer header")
	}
	return nil
}

// fetchResource finds the object being reconciled. Returns pointer to 'reconcile.Result' and error.
// If the 'reconcile.Result' pointer is not nil - the client is expected to finish processing
func (c *ReconcileCommonController) fetchResource(request reconcile.Request, object runtime.Object, log *zap.SugaredLogger) (*reconcile.Result, error) {
	err := c.client.Get(context.TODO(), request.NamespacedName, object)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			// Note: for some reasons the reconciliation is triggered twice after the object has been deleted
			log.Debugf("Object %s doesn't exist, was it deleted after reconcile request?", request.NamespacedName)
			return &reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Errorf("Failed to query object %s: %s", request.NamespacedName, err)
		return &reconcile.Result{RequeueAfter: 10 * time.Second}, err
	}
	return nil, nil
}

// registerWatchedResources adds the secret/configMap -> mongodb resource pair to internal reconciler map. This allows
// to start watching for the events for this secret/configMap and trigger reconciliation for all depending mongodb resources
func (c *ReconcileCommonController) registerWatchedResources(mongodbResourceNsName types.NamespacedName, configMap string, secret string) {
	defaultNamespace := mongodbResourceNsName.Namespace

	c.addWatchedResourceIfNotAdded(configMap, defaultNamespace, ConfigMap, mongodbResourceNsName)
	c.addWatchedResourceIfNotAdded(secret, defaultNamespace, Secret, mongodbResourceNsName)
}

func (c *ReconcileCommonController) addWatchedResourceIfNotAdded(watchedResourceFullName, watchedResourceDefaultNamespace string,
	wType watchedType, dependentResourceNsName types.NamespacedName) {
	watchedNamespacedName, err := getNamespaceAndNameForResource(watchedResourceFullName, dependentResourceNsName.Namespace)
	if err != nil {
		// note, that we don't propagate an error in case the full name has formatting errors
		return
	}
	key := watchedObject{resourceType: wType, resource: watchedNamespacedName}
	if _, ok := c.watchedResources[key]; !ok {
		c.watchedResources[key] = make([]types.NamespacedName, 0)
	}
	found := false
	for _, v := range c.watchedResources[key] {
		if v == dependentResourceNsName {
			found = true
		}
	}
	if !found {
		c.watchedResources[key] = append(c.watchedResources[key], dependentResourceNsName)
		zap.S().Debugf("Watching %s to trigger reconciliation for %s", key, dependentResourceNsName)
	}
}
