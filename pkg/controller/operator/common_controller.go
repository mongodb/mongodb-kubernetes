package operator

import (
	"context"
	"encoding/json"

	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/inspect"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/blang/semver"
	appsv1 "k8s.io/api/apps/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ClusterDomain                   = "cluster.local"
	TLSGenerationDeprecationWarning = "This feature has been DEPRECATED and should only be used in testing environments."
)

// Updatable is an interface for all "operator owned" Custom Resources
// TODO move to `apis` package (and rename to smth like `MongoDbObject`?)
type Updatable interface {
	runtime.Object
	metav1.Object

	UpdateStatus(phase mdbv1.Phase, statusOptions ...mdbv1.StatusOption)

	// SetWarnings sets the warnings for the Updatable object
	SetWarnings([]mdbv1.StatusWarning)

	// GetWarnings get the warnings from the Updatable object
	GetWarnings() []mdbv1.StatusWarning

	// GetKind returns the kind of the object. This
	// is convenient when setting the owner for K8s objects created by controllers
	GetKind() string

	// GetPlural returns the plural of the type
	GetPlural() string

	// GetStatus returns the status of the object
	GetStatus() interface{}

	// GetSpec returns the spec of the object
	GetSpec() interface{}
}

type patchValue struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

// ensure our types are all Updatable
var _ Updatable = &mdbv1.MongoDB{}
var _ Updatable = &mdbv1.MongoDBUser{}
var _ Updatable = &mdbv1.MongoDBOpsManager{}

// ReconcileCommonController is the "parent" controller that is included into each specific controller and allows
// to reuse the common functionality
// TODO the 'omConnectionFactory' needs to be moved out as Ops Manager controller for example doesn't need it
type ReconcileCommonController struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client              client.Client
	scheme              *runtime.Scheme
	kubeHelper          KubeHelper
	omConnectionFactory om.ConnectionFactory
	// internal multimap mapping watched resources to mongodb resources they are used in
	// (example: config map 'c1' is used in 2 mongodb replica sets 'm1', 'm2', so the map will be [c1]->[m1, m2])
	watchedResources map[watch.Object][]types.NamespacedName
	// this map keeps the locks for the resources the current controller is responsible for
	// This allows to serialize processing logic (edit and removal) and necessary because
	// we don't use reconciliation queue for removal operations
	reconcileLocks sync.Map
}

func newReconcileCommonController(mgr manager.Manager, omFunc om.ConnectionFactory) *ReconcileCommonController {
	return &ReconcileCommonController{
		client:              mgr.GetClient(),
		scheme:              mgr.GetScheme(),
		kubeHelper:          NewKubeHelper(mgr.GetClient()),
		omConnectionFactory: omFunc,
		watchedResources:    map[watch.Object][]types.NamespacedName{},
		reconcileLocks:      sync.Map{},
	}
}

// GetMutex creates or reuses the relevant mutex for resource
func (c *ReconcileCommonController) GetMutex(resourceName types.NamespacedName) *sync.Mutex {
	mutex, _ := c.reconcileLocks.LoadOrStore(resourceName, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

// prepareConnection reads project config map and credential secrets and uses these values to communicate with Ops Manager:
// create or read the project and optionally request an agent key (it could have been returned by group api call)
func (c *ReconcileCommonController) prepareConnection(nsName types.NamespacedName, spec mdbv1.ConnectionSpec, podVars *PodVars, log *zap.SugaredLogger) (om.Connection, error) {
	projectConfig, err := c.kubeHelper.readProjectConfig(nsName.Namespace, spec.GetProject())
	if err != nil {
		return nil, fmt.Errorf("Error reading Project Config: %s", err)
	}
	if projectConfig.ProjectName != "" {
		spec.ProjectName = projectConfig.ProjectName
	}

	credsConfig, err := c.kubeHelper.readCredentials(nsName.Namespace, spec.Credentials)
	if err != nil {
		return nil, fmt.Errorf("Error reading Credentials secret: %s", err)
	}

	c.registerWatchedResources(nsName, spec.GetProject(), spec.Credentials)

	project, conn, err := c.readOrCreateProject(spec.ProjectName, projectConfig, credsConfig, log)
	if err != nil {
		return nil, fmt.Errorf("Error reading or creating project in Ops Manager: %s", err)
	}

	omVersion := conn.OMVersion()
	if omVersion != nil { // older versions of Ops Manager will not include the version in the header
		log.Infof("Using Ops Manager version %s", omVersion)
	}

	if err := c.updateControlledFeatureAndTag(conn, project, nsName.Namespace, log); err != nil {
		return nil, err
	}

	// adds the namespace as a tag to the Ops Manager project
	if err := ensureTagAdded(conn, project, nsName.Namespace, log); err != nil {
		return nil, err
	}

	if err := c.ensureAgentKeySecretExists(conn, nsName.Namespace, project.AgentAPIKey, log); err != nil {
		return nil, err
	}

	if podVars != nil {
		// Register podVars if user passed a valid reference to a PodVars object
		podVars.BaseURL = conn.BaseURL()
		podVars.ProjectID = conn.GroupID()
		podVars.User = conn.User()
		podVars.LogLevel = spec.LogLevel
		podVars.SSLProjectConfig = projectConfig.SSLProjectConfig
	}
	return conn, nil
}

// updateControlledFeatureAndTag will configure the project to use feature controls, and set the
// EXTERNALLY_MANAGED_BY_KUBERNETES tag. The tag will be ignored if feature controls are enabled
func (c *ReconcileCommonController) updateControlledFeatureAndTag(
	conn om.Connection,
	project *om.Project,
	resourceNamespace string,
	log *zap.SugaredLogger,
) error {

	// TODO: for now, always ensure the tag, once feature controls are enabled by default we can stop apply the tag
	// the tag will have no impact if feature controls are enabled. It's either/or
	if err := ensureTagAdded(conn, project, util.OmGroupExternallyManagedTag, log); err != nil {
		return err
	}

	if shouldUseFeatureControls(conn.OMVersion()) {
		log.Debug("Configuring feature controls")
		if err := conn.UpdateControlledFeature(controlledfeature.FullyRestrictive()); err != nil {
			return err
		}
	}

	return nil
}

func ensureTagAdded(conn om.Connection, project *om.Project, tag string, log *zap.SugaredLogger) error {
	// must truncate the tag to at most 32 characters and capitalise as
	// these are Ops Manager requirements
	sanitisedTag := strings.ToUpper(fmt.Sprintf("%.32s", tag))
	alreadyHasTag := stringutil.Contains(project.Tags, sanitisedTag)
	if alreadyHasTag {
		return nil
	}

	project.Tags = append(project.Tags, sanitisedTag)

	log.Infow("Updating group tags", "newTags", project.Tags)
	_, err := conn.UpdateProject(project)
	if err != nil {
		log.Warnf("Failed to update tags for project: %s", err)
	} else {
		log.Info("Project tags are fixed")
	}
	return err
}

// ensureAgentKeySecretExists checks if the Secret with specified name (<groupId>-group-secret) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key. Generation of a key is expected to be
// a rare operation as the group creation api generates agent key already (so the only possible situation is when the group
// was created externally and agent key wasn't generated before)
// Returns the api key existing/generated
func (c *ReconcileCommonController) ensureAgentKeySecretExists(conn om.Connection, nameSpace, agentKey string, log *zap.SugaredLogger) error {
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
				return fmt.Errorf("Failed to generate agent key in OM: %s", err)
			}
			log.Info("Agent key was successfully generated")
		}

		// todo pass a real owner in a next PR
		if err = c.createAgentKeySecret(objectKey(nameSpace, secretName), agentKey, nil); err != nil {
			if apiErrors.IsAlreadyExists(err) {
				return nil
			}
			return fmt.Errorf("Failed to create Secret: %s", err)
		}
		log.Infof("Project agent key is saved in Kubernetes Secret for later usage")
		return nil
	}

	return nil
}

func (c *ReconcileCommonController) createAgentKeySecret(objectKey client.ObjectKey, agentKey string, owner Updatable) error {
	data := map[string]string{util.OmAgentApiKey: agentKey}
	return c.kubeHelper.createSecret(objectKey, data, map[string]string{}, owner)
}

// updateStatus updates the status for the CR using patch operation. Note, that the resource status is mutated and
// it's important to pass resource by pointer to all methods which invoke current 'updateStatus'.
func (c *ReconcileCommonController) updateStatus(reconciledResource Updatable, status workflow.Status, log *zap.SugaredLogger, statusOptions ...mdbv1.StatusOption) (reconcile.Result, error) {
	status.Log(log)

	mergedOptions := append(statusOptions, status.StatusOptions()...)

	reconciledResource.UpdateStatus(status.Phase(), mergedOptions...)
	if err := c.patchUpdateStatus(reconciledResource); err != nil {
		log.Errorf("Error updating status to %s: %s", status.Phase(), err)
		return fail(err)
	}
	return status.ReconcileResult()
}

// We fetch a fresh version in case any modifications have been made.
// Note, that this method enforces update ONLY to the status, so the reconciliation events happening because of this
// can be filtered out by 'controller.shouldReconcile'
// The "jsonPatch" merge allows to update only status field
func (c *ReconcileCommonController) patchUpdateStatus(resource Updatable) error {

	payload := []patchValue{{
		Op:    "replace",
		Path:  "/status",
		Value: resource.GetStatus(),
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	patch := client.ConstantPatch(types.JSONPatchType, data)
	err = c.client.Status().Patch(context.TODO(), resource, patch)
	if err != nil {
		if apiErrors.IsNotFound(err) || apiErrors.IsForbidden(err) {
			zap.S().Debugf("Patching the status subresource is not supported - will patch the whole object (error: %s)", err)
			return c.patchStatusLegacy(resource, patch)
		}
		return err
	}

	return nil
}

// patchStatusLegacy performs status update if the subresources endpoint is not supported
// TODO Remove when we stop supporting Openshift 3.11 and K8s 1.11
func (c *ReconcileCommonController) patchStatusLegacy(resource Updatable, patch client.Patch) error {
	err := c.client.Patch(context.TODO(), resource, patch)
	if err != nil {
		zap.S().Debugf("Failed to apply patch to the status - the field may not exist, we'll add it (error: %s)", err)
		// The replace for status fails if 'status' field doesn't exist may result
		// in "the server rejected our request due to an error in our request" or
		// "jsonpatch replace operation does not apply: doc is missing key: /status"
		// the fix is to first create the 'status' field and the second to patch it
		// Note, that this is quite safe to do as will happen only once for the very first reconciliation of the custom resource
		// see https://github.com/mongodb/mongodb-enterprise-kubernetes/issues/99
		// see https://stackoverflow.com/questions/57480205/error-while-applying-json-patch-to-kubernetes-custom-resource
		emptyPatchPayload := []patchValue{{
			Op:    "add",
			Path:  "/status",
			Value: mdbv1.MongoDBOpsManagerStatus{},
		}}
		data, err := json.Marshal(emptyPatchPayload)
		if err != nil {
			return err
		}
		emptyPatch := client.ConstantPatch(types.JSONPatchType, data)
		err = c.client.Patch(context.TODO(), resource, emptyPatch)
		if err != nil {
			return err
		}
		zap.S().Debugf("Added status field, patching it now")
		// Second patch will perform the normal operation
		return c.client.Patch(context.TODO(), resource, patch)
	}
	return nil
}

// getResource populates the provided runtime.Object with some additional error handling
// Note the logic: any reconcile result different from nil should be considered as "terminal" and will stop reconciliation
// right away (the pointer will be empty). Otherwise the pointer 'resource' will always reference the existing resource
func (c *ReconcileCommonController) getResource(request reconcile.Request, resource runtime.Object, log *zap.SugaredLogger) (*reconcile.Result, error) {
	err := c.client.Get(context.TODO(), request.NamespacedName, resource)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Debugf("Object %s doesn't exist, was it deleted after reconcile request?", request.NamespacedName)
			return &reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Errorf("Failed to query object %s: %s", request.NamespacedName, err)
		return &reconcile.Result{RequeueAfter: 10 * time.Second}, err
	}
	return nil, nil
}

// prepareResourceForReconciliation finds the object being reconciled. Returns pointer to 'reconcile.Status' and error
// If the 'reconcile.Status' pointer is not nil - the client is expected to finish processing
func (c *ReconcileCommonController) prepareResourceForReconciliation(
	request reconcile.Request, resource Updatable, log *zap.SugaredLogger) (*reconcile.Result, error) {
	if result, err := c.getResource(request, resource, log); result != nil {
		return result, err
	}
	// this is a temporary measure to prevent changing type and getting the resource into a bad state
	// this should be removed once we have the functionality in place to convert between resource types
	// todo needs to be moved to a webhook or we should use the K8s OpenAPI immutability for the fields once its ready
	switch res := resource.(type) {
	case *mdbv1.MongoDB:
		spec := res.Spec
		status := res.Status
		if spec.ResourceType != status.ResourceType && status.ResourceType != "" {
			c.updateStatus(res, workflow.Failed("Changing type is not currently supported, please change the resource back to a %s", status.ResourceType), log)
			return &reconcile.Result{}, nil
		}
	}

	result, err := c.updateStatus(resource, workflow.Reconciling(), log)
	if err != nil {
		return &result, err
	}

	// Reset warnings so that they are not stale, will populate accurate warnings in reconciliation
	resource.SetWarnings([]mdbv1.StatusWarning{})

	return nil, nil
}

// registerWatchedResources adds the secret/configMap -> mongodb resource pair to internal reconciler map. This allows
// to start watching for the events for this secret/configMap and trigger reconciliation for all depending mongodb resources
func (c *ReconcileCommonController) registerWatchedResources(mongodbResourceNsName types.NamespacedName, configMap string, secret string) {
	defaultNamespace := mongodbResourceNsName.Namespace

	c.addWatchedResourceIfNotAdded(configMap, defaultNamespace, watch.ConfigMap, mongodbResourceNsName)
	c.addWatchedResourceIfNotAdded(secret, defaultNamespace, watch.Secret, mongodbResourceNsName)
}

// addWatchedResourceIfNotAdded adds the given resource to the list of watched
// resources. A watched resource is a resource that, when changed, will trigger
// a reconciliation for its dependent resource.
func (c *ReconcileCommonController) addWatchedResourceIfNotAdded(name, namespace string,
	wType watch.Type, dependentResourceNsName types.NamespacedName) {
	key := watch.Object{
		ResourceType: wType,
		Resource: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}
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

// stop watching resources with input namespace and watched type, if any
func (c *ReconcileCommonController) removeWatchedResources(namespace string, wType watch.Type, dependentResourceNsName types.NamespacedName) {
	for key := range c.watchedResources {
		if key.ResourceType == wType && key.Resource.Namespace == namespace {
			index := -1
			for i, v := range c.watchedResources[key] {
				if v == dependentResourceNsName {
					index = i
				}
			}

			if index == -1 {
				continue
			}

			zap.S().Infof("Removing %s from resources dependent on %s", dependentResourceNsName, key)

			if index == 0 {
				if len(c.watchedResources[key]) == 1 {
					delete(c.watchedResources, key)
					continue
				}
				c.watchedResources[key] = c.watchedResources[key][index+1:]
				continue
			}

			if index == len(c.watchedResources[key]) {
				c.watchedResources[key] = c.watchedResources[key][:index]
				continue
			}

			c.watchedResources[key] = append(c.watchedResources[key][:index], c.watchedResources[key][index+1:]...)
		}
	}
}

// checkIfHasExcessProcesses will check if the project has excess processes.
// Also it removes the tag ExternallyManaged from the project in this case as
// the user may need to clean the resources from OM UI if they move the
// resource to another project (as recommended by the migration instructions).
func checkIfHasExcessProcesses(conn om.Connection, resource *mdbv1.MongoDB, log *zap.SugaredLogger) workflow.Status {
	deployment, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err.Error())
	}
	excessProcesses := deployment.GetNumberOfExcessProcesses(resource.Name)
	if excessProcesses == 0 {
		// cluster is empty or this resource is the only one living on it
		return workflow.OK()
	}
	// remove tags if multiple clusters in project
	groupWithTags := &om.Project{
		Name:  conn.GroupName(),
		OrgID: conn.OrgID(),
		ID:    conn.GroupID(),
		Tags:  []string{},
	}
	_, err = conn.UpdateProject(groupWithTags)
	if err != nil {
		log.Warnw("could not remove externally managed tag from Ops Manager group", "error", err)
	}

	return workflow.Pending("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
}

// doAgentX509CertsExist looks for the secret "agent-certs" to determine if we can continue with mounting the x509 volumes
func (r *ReconcileCommonController) doAgentX509CertsExist(namespace string) bool {
	secret := &corev1.Secret{}
	err := r.kubeHelper.client.Get(context.TODO(), objectKey(namespace, util.AgentSecretName), secret)
	if err != nil {
		return false
	}
	return true
}

// ensureInternalClusterCerts ensures that all the x509 internal cluster certs exist.
// TODO: this is almost the same as kubeHelper::ensureSSLCertsForStatefulSet, we should centralize the functionality
func (r *ReconcileCommonController) ensureInternalClusterCerts(ss *StatefulSetHelper, log *zap.SugaredLogger) (bool, error) {
	k := r.kubeHelper
	// Flag that's set to false if any of the certificates have not been approved yet.
	certsNeedApproval := false
	secretName := toInternalClusterAuthName(ss.Name) // my-replica-set-clusterfile

	if ss.Security.TLSConfig.CA != "" {
		// A "Certs" attribute has been provided
		// This means that the customer has provided with a secret name they have
		// already populated with the certs and keys for this deployment.
		// Because of the async nature of Kubernetes, this object might not be ready yet,
		// in which case, we'll keep reconciling until the object is created and is correct.
		if notReadyCerts := k.verifyCertificatesForStatefulSet(ss, secretName); notReadyCerts > 0 {
			return false, fmt.Errorf("The secret object '%s' does not contain all the certificates needed."+
				"Required: %d, contains: %d", secretName,
				ss.Replicas,
				ss.Replicas-notReadyCerts,
			)
		}

		// Validates that the secret is valid
		if err := k.validateCertificates(secretName, ss.Namespace); err != nil {
			return false, err
		}
	} else {

		// Validates that the secret is valid
		if err := k.validateCertificates(secretName, ss.Namespace); err != nil {
			return false, err
		}

		if notReadyCerts := k.verifyCertificatesForStatefulSet(ss, secretName); notReadyCerts > 0 {
			// If the Kube CA and the operator are responsible for the certificates to be
			// ready and correctly stored in the secret object, and this secret is not "complete"
			// we'll go through the process of creating the CSR, wait for certs approval and then
			// creating a correct secret with the certificates and keys.

			// For replica set we need to create rs.Spec.Replicas certificates, one per each Pod
			fqdns, podnames := ss.getDNSNames()

			// pemFiles will store every key (during the CSR creation phase) and certificate
			// both can happen on different reconciliation stages (CSR and keys are created, then
			// reconciliation, then certs are obtained from the CA). If this happens we need to
			// store the keys in the final secret, that will be updated with the certs, once they
			// are issued by the CA.
			pemFiles := newPemCollection()

			for idx, host := range fqdns {
				csrName := toInternalClusterAuthName(podnames[idx])
				csr, err := k.readCSR(csrName, ss.Namespace)
				if err != nil {
					certsNeedApproval = true
					key, err := k.createInternalClusterAuthCSR(csrName, ss.Namespace, clusterDomainOrDefault(ss.ClusterDomain), []string{host, podnames[idx]}, podnames[idx])
					if err != nil {
						return false, fmt.Errorf("Failed to create CSR, %s", err)
					}

					// This note was added on Release 1.5.1 of the Operator.
					log.Warn("The Operator is generating TLS x509 certificates for internal cluster authentication. " + TLSGenerationDeprecationWarning)

					pemFiles.addPrivateKey(podnames[idx], string(key))
				} else {
					if checkCSRWasApproved(csr.Status.Conditions) {
						log.Infof("Certificate for Pod %s -> Approved", host)
						pemFiles.addCertificate(podnames[idx], string(csr.Status.Certificate))
					} else {
						log.Infof("Certificate for Pod %s -> Waiting for Approval", host)
						certsNeedApproval = true
					}
				}
			}

			// once we are here we know we have built everything we needed
			// This "secret" object corresponds to the certificates for this statefulset
			labels := make(map[string]string)
			labels["mongodb/secure"] = "certs"
			labels["mongodb/operator"] = "certs." + secretName

			err := k.createOrUpdateSecret(secretName, ss.Namespace, pemFiles, labels)
			if err != nil {
				// If we have an error creating or updating the secret, we might lose
				// the keys, in which case we return an error, to make it clear what
				// the error was to customers -- this should end up in the status
				// message.
				return false, fmt.Errorf("Failed to create or update the secret: %s", err)
			}
		}
	}

	successful := !certsNeedApproval
	return successful, nil
}

//ensureX509AgentCertsForMongoDBResource will generate all the CSRs for the agents
func (r *ReconcileCommonController) ensureX509AgentCertsForMongoDBResource(mdb *mdbv1.MongoDB, useCustomCA bool, namespace string, log *zap.SugaredLogger) (bool, error) {
	k := r.kubeHelper

	certsNeedApproval := false
	if missing := k.verifyClientCertificatesForAgents(util.AgentSecretName, namespace); missing > 0 {
		if useCustomCA {
			return false, fmt.Errorf("The %s Secret file does not contain the necessary Agent certificates. Missing %d certificates", util.AgentSecretName, missing)
		}

		pemFiles := newPemCollection()
		agents := []string{"automation", "monitoring", "backup"}

		for _, agent := range agents {
			agentName := fmt.Sprintf("mms-%s-agent", agent)
			csr, err := k.readCSR(agentName, namespace)
			if err != nil {
				certsNeedApproval = true

				// the agentName name will be the same on each host, but we want to ensure there's
				// a unique name for the CSR created.
				key, err := k.createAgentCSR(agentName, namespace, mdb.Spec.GetClusterDomain())
				if err != nil {
					return false, fmt.Errorf("failed to create CSR, %s", err)
				}

				// This note was added on Release 1.5.1 of the Operator.
				log.Warn("The Operator is generating TLS x509 certificates for agent authentication. " + TLSGenerationDeprecationWarning)

				pemFiles.addPrivateKey(agentName, string(key))
			} else {
				if checkCSRWasApproved(csr.Status.Conditions) {
					pemFiles.addCertificate(agentName, string(csr.Status.Certificate))
				} else {
					certsNeedApproval = true
				}
			}
		}

		// once we are here we know we have built everything we needed
		// This "secret" object corresponds to the certificates for this statefulset
		labels := make(map[string]string)
		labels["mongodb/secure"] = "certs"
		labels["mongodb/operator"] = "certs." + util.AgentSecretName

		err := k.createOrUpdateSecret(util.AgentSecretName, namespace, pemFiles, labels)
		if err != nil {
			// If we have an error creating or updating the secret, we might lose
			// the keys, in which case we return an error, to make it clear what
			// the error was to customers -- this should end up in the status
			// message.
			return false, fmt.Errorf("failed to create or update the secret: %s", err)
		}

	}

	successful := !certsNeedApproval
	return successful, nil
}

// validateMongoDBResource performs validation on the MongoDBResource
func validateMongoDBResource(mdb *mdbv1.MongoDB, conn om.Connection) workflow.Status {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return workflow.Failed(err.Error())
	}

	if status := validateScram(mdb, ac); !status.IsOK() {
		return status
	}

	return workflow.OK()
}

// getStatefulSetStatus returns the workflow.Status based on the status of the StatefulSet.
// If the StatefulSet is not ready the request will be retried in 3 seconds (instead of the default 10 seconds)
// allowing to reach "ready" status sooner
func (r *ReconcileCommonController) getStatefulSetStatus(namespace, name string) workflow.Status {
	// TODO can we do this without sleeping?
	time.Sleep(time.Duration(envutil.ReadIntOrDefault(util.K8sCacheRefreshEnv, util.DefaultK8sCacheRefreshTimeSeconds)) * time.Second)
	set := appsv1.StatefulSet{}
	err := r.client.Get(context.TODO(), objectKey(namespace, name), &set)
	if err != nil {
		return workflow.Failed(err.Error())
	}

	if statefulSetState := inspect.StatefulSet(set); !statefulSetState.IsReady() {
		return workflow.
			Pending(statefulSetState.GetMessage()).
			WithResourcesNotReady(statefulSetState.GetResourcesNotReadyStatus()).
			WithRetry(3)
	}
	return workflow.OK()
}

// validateScram ensures that the SCRAM configuration is valid for the MongoDBResource
func validateScram(mdb *mdbv1.MongoDB, ac *om.AutomationConfig) workflow.Status {
	specVersion, err := semver.Make(util.StripEnt(mdb.Spec.GetVersion()))
	if err != nil {
		return workflow.Failed(err.Error())
	}

	scram256IsAlreadyEnabled := stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(authentication.ScramSha256))
	attemptingToDowngradeMongoDBVersion := ac.Deployment.MinimumMajorVersion() >= 4 && specVersion.Major < 4
	isDowngradingFromScramSha256ToScramSha1 := attemptingToDowngradeMongoDBVersion && stringutil.Contains(mdb.Spec.Security.Authentication.GetModes(), "SCRAM") && scram256IsAlreadyEnabled

	if isDowngradingFromScramSha256ToScramSha1 {
		return workflow.Invalid("Unable to downgrade to SCRAM-SHA-1 when SCRAM-SHA-256 has been enabled")
	}

	return workflow.OK()
}

// Use the first "CERTIFICATE" block found in the PEM file.
func getSubjectFromCertificate(cert string) (string, error) {
	block, rest := pem.Decode([]byte(cert))
	if block != nil && block.Type == "CERTIFICATE" {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		return cert.Subject.ToRDNSequence().String(), nil
	}
	if len(rest) > 0 {
		block, _ = pem.Decode(rest)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		return cert.Subject.ToRDNSequence().String(), nil
	}
	return "", fmt.Errorf("unable to extract the subject line from the provided certificate")
}

// updateOmAuthentication examines the state of Ops Manager and the desired state of the MongoDB resource and
// enables/disables authentication. If the authentication can't be fully configured, a boolean value indicating that
// an additional reconciliation needs to be queued up to fully make the authentication changes is returned.
func (r *ReconcileCommonController) updateOmAuthentication(conn om.Connection, processNames []string, mdb *mdbv1.MongoDB, log *zap.SugaredLogger) (status workflow.Status, multiStageReconciliation bool) {
	// don't touch authentication settings if resource has not been configured with them
	if mdb.Spec.Security == nil || mdb.Spec.Security.Authentication == nil {
		return workflow.OK(), false
	}

	// we need to wait for all agents to be ready before configuring any authentication settings
	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return workflow.Failed(err.Error()), false
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return workflow.Failed(err.Error()), false
	}

	authOpts := authentication.Options{
		MinimumMajorVersion: mdb.Spec.MinimumMajorVersion(),
		Mechanisms:          mdb.Spec.Security.Authentication.Modes,
		ProcessNames:        processNames,
		AuthoritativeSet:    !mdb.Spec.Security.Authentication.IgnoreUnknownUsers,
	}

	log.Debugf("Using authentication options %+v", authOpts)

	wantToEnableAuthentication := mdb.Spec.Security.Authentication.Enabled
	if wantToEnableAuthentication && canConfigureAuthentication(ac, mdb, log) {
		log.Info("Configuring authentication for MongoDB resource")

		if mdb.Spec.Security.GetAgentMechanism() == util.X509 {
			authOpts, err = r.configureAgentSubjects(mdb.Namespace, authOpts, log)
			if err != nil {
				return workflow.Failed("error configuring agent subjects: %v", err), false
			}
		}

		if err := authentication.Configure(conn, authOpts, log); err != nil {
			return workflow.Failed(err.Error()), false
		}
	} else if wantToEnableAuthentication {
		// The MongoDB resource has been configured with a type of authentication
		// but the current state in Ops Manager does not allow a direct transition. This will require
		// an additional reconciliation after a partial update to Ops Manager.
		log.Debug("Attempting to enable authentication, but Ops Manager state will not allow this")
		return workflow.OK(), true
	} else {
		// Should not fail if the Secret object with agent certs is not found.
		// It will only exist on x509 client auth enabled deployments.
		userOpts, err := r.readAgentSubjectsFromSecret(mdb.Namespace, log)
		err = client.IgnoreNotFound(err)
		if err != nil {
			return workflow.Failed(err.Error()), true
		}

		authOpts.UserOptions = userOpts
		if err := authentication.Disable(conn, authOpts, log); err != nil {
			return workflow.Failed(err.Error()), false
		}
	}
	return workflow.OK(), false
}

// configureAgentSubjects returns a new authentication.Options which has configured the Subject lines for the automation, monitoring
// and backup agents. The subjects are read from the "agent-certs" secret. This secret is generated by the operator when
// x509 is configured, but if this secret is provided by the user, custom x509 certificates can be provided and used by the agents.
// The Ops Manager user names for these agents will be configured based on the contents of the secret.
func (r *ReconcileCommonController) configureAgentSubjects(namespace string, authOpts authentication.Options, log *zap.SugaredLogger) (authentication.Options, error) {
	userOpts, err := r.readAgentSubjectsFromSecret(namespace, log)
	if err != nil {
		return authentication.Options{}, fmt.Errorf("error reading agent subjects from secret: %v", err)
	}
	authOpts.UserOptions = userOpts
	return authOpts, nil
}

func (r *ReconcileCommonController) readAgentSubjectsFromSecret(namespace string, log *zap.SugaredLogger) (authentication.UserOptions, error) {
	userOpts := authentication.UserOptions{}
	agentCerts, err := r.kubeHelper.readSecret(objectKey(namespace, util.AgentSecretName))
	if err != nil {
		return userOpts, err
	}

	numAgentCerts := len(agentCerts)
	if numAgentCerts != NumAgents && numAgentCerts != 1 {
		return userOpts, fmt.Errorf("must provided either 1 or 3 agent certificatesm found %d", numAgentCerts)
	}

	var automationAgentCert string
	var ok bool
	if automationAgentCert, ok = agentCerts[util.AutomationAgentPemSecretKey]; !ok {
		return userOpts, fmt.Errorf("when there is only one certificate present, it must have a key of %s", util.AutomationAgentPemSecretKey)
	}

	log.Debugf("Got %d certificate(s) in the Secret", numAgentCerts)
	var automationAgentSubject, backupAgentSubject, monitoringAgentSubject string

	automationAgentSubject, err = getSubjectFromCertificate(automationAgentCert)
	if err != nil {
		return userOpts, fmt.Errorf("error extracting automation agent subject is not present %e", err)
	}

	monitoringAgentSubject = automationAgentSubject
	backupAgentSubject = automationAgentSubject

	if numAgentCerts == NumAgents {
		monitoringAgentSubject, err = getSubjectFromCertificate(agentCerts[util.MonitoringAgentPemSecretKey])
		if err != nil {
			return userOpts, fmt.Errorf("error extracting monitoring agent subject from agent-certs %e", err)
		}
		backupAgentSubject, err = getSubjectFromCertificate(agentCerts[util.BackupAgentPemSecretKey])
		if err != nil {
			return userOpts, fmt.Errorf("error extracting backup agent subject from agent-certs %e", err)
		}
	}

	if automationAgentSubject == "" || monitoringAgentSubject == "" || backupAgentSubject == "" {
		return userOpts, fmt.Errorf("some of the subjects lines are not present")
	}

	return authentication.UserOptions{
		AutomationSubject: automationAgentSubject,
		MonitoringSubject: monitoringAgentSubject,
		BackupSubject:     backupAgentSubject,
	}, nil
}

// canConfigureAuthentication determines if based on the existing state of Ops Manager
// it is possible to configure the authentication mechanisms specified by the given MongoDB resource
// during this reconciliation. This function may return a different value on the next reconciliation
// if the state of Ops Manager has been changed.
func canConfigureAuthentication(ac *om.AutomationConfig, mdb *mdbv1.MongoDB, log *zap.SugaredLogger) bool {
	attemptingToEnableX509 := !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) && stringutil.Contains(mdb.Spec.Security.Authentication.GetModes(), util.X509)
	canEnableX509InOpsManager := ac.Deployment.AllProcessesAreTLSEnabled() || ac.Deployment.NumberOfProcesses() == 0

	log.Debugw("canConfigureAuthentication",
		"attemptingToEnableX509", attemptingToEnableX509,
		"deploymentAuthMechanisms", ac.Auth.DeploymentAuthMechanisms,
		"modes", mdb.Spec.Security.Authentication.GetModes(),
		"canEnableX509InOpsManager", canEnableX509InOpsManager,
		"allProcessesAreTLSEnabled", ac.Deployment.AllProcessesAreTLSEnabled(),
		"numberOfProcesses", ac.Deployment.NumberOfProcesses())

	if attemptingToEnableX509 {
		return canEnableX509InOpsManager
	}

	// x509 is the only mechanism with restrictions determined based on Ops Manager state
	return true
}

func shouldUseFeatureControls(version *om.Version) bool {

	// if we were not successfully able to determine a version
	// from Ops Manager, we can assume it is a legacy version
	if version.IsUnknown() {
		return false
	}

	// feature controls are enabled on Cloud Manager, e.g. v20191112
	if version.IsCloudManager() {
		return true
	}

	sv, err := version.Semver()
	if err != nil {
		return false
	}

	// feature was closed Oct 01 2019  https://jira.mongodb.org/browse/CLOUDP-46339
	// 4.2.2 was cut Oct 02 2019
	// 4.3.0 was cut Sept 12 2019
	// 4.3.1 was cut Oct 03 2019

	// You need 4.2.2 or later
	// 4.3.1 or later
	// or any 4.4 onwards to make use of Feature Controls

	minFourTwoVersion := semver.Version{
		Major: 4,
		Minor: 2,
		Patch: 2,
	}

	minFourThreeVersion := semver.Version{
		Major: 4,
		Minor: 3,
		Patch: 1,
	}

	minFourFourVersion := semver.Version{
		Major: 4,
		Minor: 4,
		Patch: 0,
	}

	if isFourTwo(sv) {
		return sv.GTE(minFourTwoVersion)
	} else if isFourThree(sv) {
		return sv.GTE(minFourThreeVersion)
	} else if isFourFour(sv) {
		return sv.GTE(minFourFourVersion)
	} else { // otherwise it's an older version, so we will use the tag
		return false
	}
}

func isFourTwo(version semver.Version) bool {
	return isMajorMinor(version, 4, 2)
}

func isFourThree(version semver.Version) bool {
	return isMajorMinor(version, 4, 3)
}

func isFourFour(version semver.Version) bool {
	return isMajorMinor(version, 4, 4)
}

func isMajorMinor(v semver.Version, major, minor uint64) bool {
	return v.Major == major && v.Minor == minor
}

func clusterDomainOrDefault(clusterDomain string) string {
	if clusterDomain == "" {
		return ClusterDomain
	}

	return clusterDomain
}
