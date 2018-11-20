package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
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
	omConnectionFunc func(baseUrl, groupId, user, publicApiKey string) om.OmConnection
}

func (c *ReconcileCommonController) prepareOmConnection(namespace, project, credentials string, log *zap.SugaredLogger) (om.OmConnection, *PodVars, error) {
	projectConfig, e := c.kubeHelper.readProjectConfig(namespace, project)
	if e != nil {
		return nil, nil, e
	}
	credsConfig, e := c.kubeHelper.readCredentials(namespace, credentials)
	if e != nil {
		return nil, nil, e
	}

	group, e := c.readOrCreateGroup(projectConfig, credsConfig, log)
	if e != nil {
		return nil, nil, e
	}

	omConnection := c.omConnectionFunc(projectConfig.BaseUrl, group.Id, credsConfig.User, credsConfig.PublicApiKey)

	agentKey, e := c.ensureAgentKeySecretExists(omConnection, namespace, group.AgentApiKey, log)

	if e != nil {
		return nil, nil, e
	}
	vars := &PodVars{
		BaseUrl:     projectConfig.BaseUrl,
		ProjectId:   group.Id,
		AgentApiKey: agentKey,
		User:        credsConfig.User,
	}
	return omConnection, vars, nil
}

// ensureAgentKeySecretExists checks if the Secret with specified name (<groupId>-group-secret) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key. Generation of a key is expected to be
// a rare operation as the group creation api generates agent key already (so the only possible situation is when the group
// was created externally and agent key wasn't generated before)
// Returns the api key existing/generated
func (c *ReconcileCommonController) ensureAgentKeySecretExists(omConnection om.OmConnection, nameSpace, agentKey string, log *zap.SugaredLogger) (string, error) {
	secretName := agentApiKeySecretName(omConnection.GroupId())
	log = log.With("secret", secretName)
	secret, err := c.kubeHelper.kubeApi.getSecret(nameSpace, secretName)
	if err != nil {
		if agentKey == "" {
			log.Info("Generating agent key as current group doesn't have it")

			agentKey, err = omConnection.GenerateAgentKey()
			if err != nil {
				return "", fmt.Errorf("Failed to generate agent key in OM: %s", err)
			}
			log.Info("Agent key was successfully generated")
		}

		if secret, err = c.kubeHelper.kubeApi.createSecret(nameSpace, buildSecret(secretName, nameSpace, agentKey)); err != nil {
			return "", fmt.Errorf("Failed to create Secret: %s", err)
		}
		log.Info("Group agent key is saved in Kubernetes Secret for later usage")
	}

	return strings.TrimSuffix(string(secret.Data[util.OmAgentApiKey]), "\n"), nil
}

func (c *ReconcileCommonController) updateStatusSuccessful(resource v1.StatusUpdater, log *zap.SugaredLogger) {
	resource.UpdateSuccessful()
	// Dev note: "c.client.Status().Update()" doesn't work for some reasons and the examples from Operator SDK use the
	// normal resource update - so we use the same
	err := c.client.Update(context.TODO(), resource)
	if err != nil {
		log.Errorf("Failed to update status for resource to successful: %s", err)
	}
}

func (c *ReconcileCommonController) updateStatusFailed(resource v1.StatusUpdater, errorMessage string, log *zap.SugaredLogger) (reconcile.Result, error) {
	log.Error(errorMessage)
	resource.UpdateError(errorMessage)
	err := c.client.Update(context.TODO(), resource)
	if err != nil {
		log.Errorf("Failed to update resource status: %s", err)
	}
	return reconcile.Result{RequeueAfter: 10 * time.Second}, errors.New(errorMessage)
}

// reconcileDeletion checks the headers and if 'util.MongodbResourceFinalizer' header is present - tries to cleanup Ops Manager
// through calling 'cleanupFunc'. Cleanup is requeued in case of any troubles. Otherwise the header is removed from custom
// resource
func (c *ReconcileCommonController) reconcileDeletion(cleanupFunc func(obj interface{}, log *zap.SugaredLogger) error,
	res interface{}, objectMeta *metav1.ObjectMeta, log *zap.SugaredLogger) (reconcile.Result, error) {
	// Object is being removed - let's check for finalizers left
	if util.ContainsString(objectMeta.Finalizers, util.MongodbResourceFinalizer) {
		if err := cleanupFunc(res, log); err != nil {
			// Retrying cleanup
			return reconcile.Result{}, err
		}

		// remove our finalizer from the list and update it.
		objectMeta.Finalizers = util.RemoveString(objectMeta.Finalizers, util.MongodbResourceFinalizer)
		if err := c.client.Update(context.Background(), res.(runtime.Object)); err != nil {
			return reconcile.Result{}, err
		}
		log.Debug("Removed finalizer header")
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

// fetchResource finds the object being reconciled. Returns 'true' indicating if the main logic should proceed
func (c *ReconcileCommonController) fetchResource(request reconcile.Request, object runtime.Object, log *zap.SugaredLogger) (bool, error) {
	err := c.client.Get(context.TODO(), request.NamespacedName, object)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			// TODO for some reasons the reconciliation is triggered twice after the object has been deleted
			log.Debugf("Object %s doesn't exist, was it deleted after reconcile request?", request.NamespacedName)
			return false, nil
		}
		// Error reading the object - requeue the request.
		log.Errorf("Failed to query object %s: %s", request.NamespacedName, err)
		return false, err
	}
	return true, nil
}
