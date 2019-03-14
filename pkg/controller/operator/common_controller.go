package operator

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TODO rename the file to "common_controller.go" later

// omMutexes is the synchronous map of mutexes that allow to get strict serializability for operations "read-modify-write"
// for Ops Manager. Keys are (group_name + org_id) and values are mutexes.
var omMutexes = sync.Map{}

// ReconcileCommonController is the "parent" controller that is included into each specific controller and allows
// to reuse the common functionality
type ReconcileCommonController struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client              client.Client
	scheme              *runtime.Scheme
	kubeHelper          KubeHelper
	omConnectionFactory om.ConnectionFactory
	// internal multimap mapping watched resources to mongodb resources they are used in
	// (example: config map 'c1' is used in 2 mongodb replica sets 'm1', 'm2', so the map will be [c1]->[m1, m2])
	watchedResources map[watchedObject][]types.NamespacedName
}

func newReconcileCommonController(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileCommonController {
	return &ReconcileCommonController{
		client:              mgr.GetClient(),
		scheme:              mgr.GetScheme(),
		kubeHelper:          KubeHelper{mgr.GetClient()},
		omConnectionFactory: omFunc,
		watchedResources:    map[watchedObject][]types.NamespacedName{},
	}
}

// prepareConnection reads project config map and credential secrets and uses these values to communicate with Ops Manager:
// create or read the project and optionally request an agent key (it could have been returned by group api call)
func (c *ReconcileCommonController) prepareConnection(nsName types.NamespacedName, spec v1.MongoDbSpec, podVars *PodVars, log *zap.SugaredLogger) (om.Connection, error) {
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

	omContext := om.OMContext{
		GroupID:      group.ID,
		GroupName:    projectConfig.ProjectName,
		OrgID:        projectConfig.OrgID,
		BaseURL:      projectConfig.BaseURL,
		PublicAPIKey: credsConfig.PublicAPIKey,
		User:         credsConfig.User,
	}
	conn := c.omConnectionFactory(&omContext)
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
			log.Info("Generating agent key as current project doesn't have it")

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
		log.Infof("Project agent key is saved in Kubernetes Secret for later usage")
	}

	return strings.TrimSuffix(string(secret.Data[util.OmAgentApiKey]), "\n"), nil
}

// getMutex creates or reuses the relevant mutex for the group + org
func getMutex(projectName, orgId string) *sync.Mutex {
	lockName := projectName + orgId
	mutex, _ := omMutexes.LoadOrStore(lockName, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

func (c *ReconcileCommonController) updateStatusSuccessful(reconciledResource *v1.MongoDB, log *zap.SugaredLogger, url, groupId string) {
	old := reconciledResource.DeepCopyObject()
	err := c.updateStatus(reconciledResource, func(fresh *v1.MongoDB) {
		// we need to update the MongoDB based on the Spec of the reconciled resource
		// if there has been a change to the spec since, we don't want to change the state
		// subresource to match an incorrect spec
		fresh.UpdateSuccessful(DeploymentLink(url, groupId), old.(*v1.MongoDB))
	})
	if err != nil {
		log.Errorf("Failed to update status for resource to successful: %s", err)
	} else {
		log.Infow("Successful update", "spec", reconciledResource.Spec)
	}
}

func (c *ReconcileCommonController) updateStatusFailed(resource *v1.MongoDB, msg string, log *zap.SugaredLogger) (reconcile.Result, error) {
	log.Error(msg)
	// Resource may be nil if the reconciliation failed very early (on fetching the resource) and panic handling function
	// took over
	if resource != nil {
		err := c.updateStatus(resource, func(fresh *v1.MongoDB) {
			fresh.UpdateError(msg)
		})
		if err != nil {
			log.Errorf("Failed to update resource status: %s", err)
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}
	return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
}

// if the resource is updated externally during an update, it's possible that we get concurrent modification errors
// when trying to update.
// E.g: "Operation cannot be fulfilled on mongodbstandalones.mongodb.com : the object has
// been modified; please apply your changes to the latest version and try again" error - so let's fetch the latest
// object before updating it.
// We fetch a fresh version in case any modifications have been made.
// Note, that this method enforces update ONLY to the status, so the reconciliation events happening because of this
// can be filtered out by 'controller.shouldReconcile'
func (c *ReconcileCommonController) updateStatus(reconciledResource *v1.MongoDB, updateFunc func(fresh *v1.MongoDB)) error {
	var err error
	for i := 0; i < 3; i++ {
		err = c.client.Get(context.TODO(), objectKeyFromApiObject(reconciledResource), reconciledResource)
		if err == nil {
			updateFunc(reconciledResource)
			err = c.client.Update(context.TODO(), reconciledResource)
			if err == nil {
				return nil
			}
			// we want to try again if there's a conflict, possible concurrent modification
			if apiErrors.IsConflict(err) {
				continue
			}
			// otherwise we've got a different error
			return err
		}
	}
	return err
}

func (c *ReconcileCommonController) updatePending(reconciledResource *v1.MongoDB) error {
	return c.updateStatus(reconciledResource, func(fresh *v1.MongoDB) {
		fresh.Status.Phase = v1.PhasePending
	})
}

// shouldReconcile checks if the resource must be reconciled.
// Edge cases:
// 1) Statuses changes - we never reconcile on them, only on spec/metadata ones
// 2) Controller may add a finalizer or it may be removed by K8s - ignoring this
//
// Important notes about why we can just check statuses/finalizers and be sure that we don't miss the reconciliation:
// - the watchers receive signals only about any changes to *CR*. If the reconciliation failed and a reconciler returned
// "requeue after 10 seconds" - this doesn't get to watcher, so will never be filtered out.
// - the only client making changes to status is the Operator itself and it makes sure that spec stays untouched
func shouldReconcile(oldResource *v1.MongoDB, newResource *v1.MongoDB) bool {
	newStatus := newResource.Status
	if !reflect.DeepEqual(oldResource.Status, newStatus) {
		return false
	}
	return true
}

// prepareResourceForReconciliation finds the object being reconciled. Returns pointer to 'reconcile.Result', error and the hashSpec for the resource.
// If the 'reconcile.Result' pointer is not nil - the client is expected to finish processing
func (c *ReconcileCommonController) prepareResourceForReconciliation(
	request reconcile.Request, mdbResource *v1.MongoDB, log *zap.SugaredLogger) (*reconcile.Result, error) {
	err := c.client.Get(context.TODO(), request.NamespacedName, mdbResource)
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

	// this is a temporary measure to prevent changing type and getting the resource into a bad state
	// this should be removed once we have the functionality in place to convert between resource types
	spec := mdbResource.Spec
	status := mdbResource.Status
	if spec.ResourceType != status.ResourceType && status.ResourceType != "" {
		c.updateStatusFailed(mdbResource, fmt.Sprintf("Changing type is not currently supported, please change the resource back to a %s", status.ResourceType), log)
		return &reconcile.Result{}, nil
	}

	updateErr := c.updatePending(mdbResource)
	if updateErr != nil {
		log.Errorf("Error setting state to pending: %s, the resource: %+v", updateErr, mdbResource)
		return &reconcile.Result{RequeueAfter: 10 * time.Second}, nil
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
