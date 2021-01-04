package operator

import (
	"context"
	"encoding/json"
	"os"
	"reflect"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/certs"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/pem"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/project"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/inspect"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/blang/semver"

	"sigs.k8s.io/controller-runtime/pkg/manager"

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

const (
	ClusterDomain                   = "cluster.local"
	TLSGenerationDeprecationWarning = "This feature has been DEPRECATED and should only be used in testing environments."
)

type patchValue struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

// ReconcileCommonController is the "parent" controller that is included into each specific controller and allows
// to reuse the common functionality
type ReconcileCommonController struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client     kubernetesClient.Client
	scheme     *runtime.Scheme
	kubeHelper KubeHelper
	// this map keeps the locks for the resources the current controller is responsible for
	// This allows to serialize processing logic (edit and removal) and necessary because
	// we don't use reconciliation queue for removal operations
	reconcileLocks sync.Map
}

func newReconcileCommonController(mgr manager.Manager) *ReconcileCommonController {
	newClient := kubernetesClient.NewClient(mgr.GetClient())
	return &ReconcileCommonController{
		client:         newClient,
		scheme:         mgr.GetScheme(),
		kubeHelper:     NewKubeHelper(mgr.GetClient()),
		reconcileLocks: sync.Map{},
	}
}

// GetMutex creates or reuses the relevant mutex for resource
func (c *ReconcileCommonController) GetMutex(resourceName types.NamespacedName) *sync.Mutex {
	mutex, _ := c.reconcileLocks.LoadOrStore(resourceName, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

func ensureRoles(roles []mdbv1.MongoDbRole, conn om.Connection, log *zap.SugaredLogger) workflow.Status {
	d, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err.Error())
	}
	dRoles := d.GetRoles()
	if reflect.DeepEqual(dRoles, roles) {
		return workflow.OK()
	}
	// HELP-20798: the agent deals correctly with a null value for
	// privileges only when creating a role, not when updating
	// we work around it by explicitly passing empty array
	for i, role := range roles {
		if role.Privileges == nil {
			roles[i].Privileges = []mdbv1.Privilege{}
		}
	}
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.SetRoles(roles)
			return nil
		},
		log,
	)
	if err != nil {
		return workflow.Failed(err.Error())
	}
	return workflow.OK()
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

// updateStatus updates the status for the CR using patch operation. Note, that the resource status is mutated and
// it's important to pass resource by pointer to all methods which invoke current 'updateStatus'.
func (c *ReconcileCommonController) updateStatus(reconciledResource v1.CustomResourceReadWriter, status workflow.Status, log *zap.SugaredLogger, statusOptions ...status.Option) (reconcile.Result, error) {
	status.Log(log)

	mergedOptions := append(statusOptions, status.StatusOptions()...)
	reconciledResource.UpdateStatus(status.Phase(), mergedOptions...)
	if err := c.patchUpdateStatus(reconciledResource, statusOptions...); err != nil {
		log.Errorf("Error updating status to %s: %s", status.Phase(), err)
		return reconcile.Result{}, err
	}
	return status.ReconcileResult()
}

// We fetch a fresh version in case any modifications have been made.
// Note, that this method enforces update ONLY to the status, so the reconciliation events happening because of this
// can be filtered out by 'controller.shouldReconcile'
// The "jsonPatch" merge allows to update only status field
func (c *ReconcileCommonController) patchUpdateStatus(resource v1.CustomResourceReadWriter, options ...status.Option) error {
	payload := []patchValue{{
		Op:   "replace",
		Path: resource.GetStatusPath(options...),
		// in most cases this will be "/status", but for each of the different Ops Manager components
		// this will be different
		Value: resource.GetStatus(options...),
	}}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	patch := client.ConstantPatch(types.JSONPatchType, data)
	err = c.client.Status().Patch(context.TODO(), resource, patch)

	if err != nil && apiErrors.IsInvalid(err) {
		zap.S().Debug("The Status subresource might not exist yet, creating empty subresource")
		if err := c.ensureStatusSubresourceExists(resource, options...); err != nil {
			return err
		}
		err = c.client.Status().Patch(context.TODO(), resource, patch)
	}

	if err != nil {
		if apiErrors.IsNotFound(err) || apiErrors.IsForbidden(err) {
			zap.S().Debugf("Patching the status subresource is not supported - will patch the whole object (error: %s)", err)
			return c.patchStatusLegacy(resource, patch)
		}
		return err
	}

	return nil
}

type emptyPayload struct{}

// ensureStatusSubresourceExists ensures that the status subresource section we are trying to write to exists.
// if we just try and patch the full path directly, the subresource sections are not recursively created, so
// we need to ensure that the actual object we're trying to write to exists, otherwise we will get errors.
func (c *ReconcileCommonController) ensureStatusSubresourceExists(resource v1.CustomResourceReadWriter, options ...status.Option) error {
	fullPath := resource.GetStatusPath(options...)
	parts := strings.Split(fullPath, "/")

	if strings.HasPrefix(fullPath, "/") {
		parts = parts[1:]
	}

	var path []string
	for _, part := range parts {
		pathStr := "/" + strings.Join(path, "/")
		path = append(path, part)
		emptyPatchPayload := []patchValue{{
			Op:    "add",
			Path:  pathStr,
			Value: emptyPayload{},
		}}
		data, err := json.Marshal(emptyPatchPayload)
		if err != nil {
			return err
		}
		patch := client.ConstantPatch(types.JSONPatchType, data)
		if err := c.client.Status().Patch(context.TODO(), resource, patch); err != nil && !apiErrors.IsInvalid(err) {
			return err
		}
	}
	return nil
}

// patchStatusLegacy performs status update if the subresources endpoint is not supported
// TODO Remove when we stop supporting Openshift 3.11 and K8s 1.11
func (c *ReconcileCommonController) patchStatusLegacy(resource v1.CustomResourceReadWriter, patch client.Patch) error {
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
			Value: omv1.MongoDBOpsManagerStatus{},
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
// Note the logic: any reconcileAppDB result different from nil should be considered as "terminal" and will stop reconciliation
// right away (the pointer will be empty). Otherwise the pointer 'resource' will always reference the existing resource
func (c *ReconcileCommonController) getResource(request reconcile.Request, resource runtime.Object, log *zap.SugaredLogger) (*reconcile.Result, error) {
	err := c.client.Get(context.TODO(), request.NamespacedName, resource)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcileAppDB request.
			// Return and don't requeue
			log.Debugf("Object %s doesn't exist, was it deleted after reconcileAppDB request?", request.NamespacedName)
			return &reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Errorf("Failed to query object %s: %s", request.NamespacedName, err)
		return &reconcile.Result{RequeueAfter: 10 * time.Second}, err
	}
	return nil, nil
}

// prepareResourceForReconciliation finds the object being reconciled. Returns pointer to 'reconcileAppDB.Status' and error
// If the 'reconcileAppDB.Status' pointer is not nil - the client is expected to finish processing
func (c *ReconcileCommonController) prepareResourceForReconciliation(
	request reconcile.Request, resource v1.CustomResourceReadWriter, log *zap.SugaredLogger) (*reconcile.Result, error) {
	if result, err := c.getResource(request, resource, log); result != nil {
		return result, err
	}

	result, err := c.updateStatus(resource, workflow.Reconciling(), log)
	if err != nil {
		return &result, err
	}

	// Reset warnings so that they are not stale, will populate accurate warnings in reconciliation
	resource.SetWarnings([]status.Warning{})

	return nil, nil
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
	_, err := r.kubeHelper.client.GetSecret(kube.ObjectKey(namespace, util.AgentSecretName))
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
			pemFiles := enterprisepem.NewCollection()

			for idx, host := range fqdns {
				csrName := toInternalClusterAuthName(podnames[idx])
				csr, err := certs.ReadCSR(k.client, csrName, ss.Namespace)
				if err != nil {
					certsNeedApproval = true
					key, err := certs.CreateInternalClusterAuthCSR(k.client, csrName, ss.Namespace, clusterDomainOrDefault(ss.ClusterDomain), []string{host, podnames[idx]}, podnames[idx])
					if err != nil {
						return false, fmt.Errorf("Failed to create CSR, %s", err)
					}

					// This note was added on Release 1.5.1 of the Operator.
					log.Warn("The Operator is generating TLS x509 certificates for internal cluster authentication. " + TLSGenerationDeprecationWarning)

					pemFiles.AddPrivateKey(podnames[idx], string(key))
				} else {
					if certs.CSRWasApproved(csr) {
						log.Infof("Certificate for Pod %s -> Approved", host)
						pemFiles.AddCertificate(podnames[idx], string(csr.Status.Certificate))
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

			err := enterprisepem.CreateOrUpdateSecret(r.client, secretName, ss.Namespace, pemFiles)
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

		pemFiles := enterprisepem.NewCollection()
		agents := []string{"automation", "monitoring", "backup"}

		for _, agent := range agents {
			agentName := fmt.Sprintf("mms-%s-agent", agent)
			csr, err := certs.ReadCSR(k.client, agentName, namespace)
			if err != nil {
				certsNeedApproval = true

				// the agentName name will be the same on each host, but we want to ensure there's
				// a unique name for the CSR created.
				key, err := certs.CreateAgentCSR(r.client, agentName, namespace, mdb.Spec.GetClusterDomain())
				if err != nil {
					return false, fmt.Errorf("failed to create CSR, %s", err)
				}

				// This note was added on Release 1.5.1 of the Operator.
				log.Warn("The Operator is generating TLS x509 certificates for agent authentication. " + TLSGenerationDeprecationWarning)

				pemFiles.AddPrivateKey(agentName, string(key))
			} else {
				if certs.CSRWasApproved(csr) {
					pemFiles.AddCertificate(agentName, string(csr.Status.Certificate))
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

		err := enterprisepem.CreateOrUpdateSecret(r.client, util.AgentSecretName, namespace, pemFiles)
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

// ensureBackupConfigurationAndUpdateStatus configures backup in Ops Manager based on the MongoDB resources spec
func (r *ReconcileCommonController) ensureBackupConfigurationAndUpdateStatus(conn om.Connection, mdb *mdbv1.MongoDB, log *zap.SugaredLogger) workflow.Status {
	statusOpt, opts := backup.EnsureBackupConfigurationInOpsManager(mdb.Spec.Backup, conn.GroupID(), conn, log)
	if len(opts) > 0 {
		if _, err := r.updateStatus(mdb, statusOpt, log, opts...); err != nil {
			return workflow.Failed(err.Error())
		}
	}
	return statusOpt
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
	set, err := r.client.GetStatefulSet(kube.ObjectKey(namespace, name))
	i := 0

	// Sometimes it is possible that the StatefulSet which has just been created
	// returns a not found error when getting it too soon afterwards.
	for apiErrors.IsNotFound(err) && i < 10 {
		i++
		zap.S().Debugf("StatefulSet was not found: %s, attempt %d", err, i)
		time.Sleep(time.Second * 1)
		set, err = r.client.GetStatefulSet(kube.ObjectKey(namespace, name))
	}

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

func getWatchedNamespace() string {
	// get watch namespace from environment variable
	namespace, nsSpecified := os.LookupEnv(util.WatchNamespace)

	// if the watch namespace is not specified - we assume the Operator is watching the current namespace
	if !nsSpecified {
		// the current namespace is expected to be always specified as main.go performs the hard check of this
		namespace = env.ReadOrDefault(util.CurrentNamespace, "")
	}
	return namespace
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

	clientCerts := util.OptionalClientCertficates
	if mdb.Spec.Security.RequiresClientTLSAuthentication() {
		clientCerts = util.RequireClientCertificates
	}

	scramAgentUserName := util.AutomationAgentUserName
	// only use the default name if there is not already a configure user name
	if ac.Auth.AutoUser != "" && ac.Auth.AutoUser != scramAgentUserName {
		scramAgentUserName = ac.Auth.AutoUser
	}

	authOpts := authentication.Options{
		MinimumMajorVersion: mdb.Spec.MinimumMajorVersion(),
		Mechanisms:          mdb.Spec.Security.Authentication.Modes,
		ProcessNames:        processNames,
		AuthoritativeSet:    !mdb.Spec.Security.Authentication.IgnoreUnknownUsers,
		AgentMechanism:      mdb.Spec.Security.GetAgentMechanism(ac.Auth.AutoAuthMechanism),
		ClientCertificates:  clientCerts,
		AutoUser:            scramAgentUserName,
		AutoLdapGroupDN:     mdb.Spec.Security.Authentication.Agents.AutomationLdapGroupDN,
	}

	if mdb.IsLDAPEnabled() {
		bindUserPassword, err := secret.ReadKey(r.client, "password", kube.ObjectKey(mdb.Namespace, mdb.Spec.Security.Authentication.Ldap.BindQuerySecretRef.Name))
		if err != nil {
			return workflow.Failed(fmt.Sprintf("error reading bind user password: %s", err)), false
		}

		caContents := ""
		ca := mdb.Spec.Security.Authentication.Ldap.CAConfigMapRef
		if ca != nil {
			log.Debugf("Sending CA file to Pods via AutomationConfig: %s/%s/%s", mdb.GetNamespace(), ca.Name, ca.Key)
			caContents, err = configmap.ReadKey(r.client, ca.Key, types.NamespacedName{Name: ca.Name, Namespace: mdb.GetNamespace()})
			if err != nil {
				return workflow.Failed(fmt.Sprintf("error reading CA configmap: %s", err)), false
			}
		}

		authOpts.Ldap = mdb.GetLDAP(bindUserPassword, caContents)
	}

	log.Debugf("Using authentication options %+v", authentication.Redact(authOpts))

	wantToEnableAuthentication := mdb.Spec.Security.Authentication.Enabled
	if wantToEnableAuthentication && canConfigureAuthentication(ac, mdb, log) {
		log.Info("Configuring authentication for MongoDB resource")

		if mdb.Spec.Security.ShouldUseX509(ac.Auth.AutoAuthMechanism) || mdb.Spec.Security.ShouldUseClientCertificates() {
			authOpts, err = r.configureAgentSubjects(mdb.Namespace, mdb.Spec.Security.AgentClientCertificateSecretName(), authOpts, log)
			if err != nil {
				return workflow.Failed("error configuring agent subjects: %v", err), false
			}
			authOpts.AgentsShouldUseClientAuthentication = mdb.Spec.Security.ShouldUseClientCertificates()

		}
		if mdb.Spec.Security.ShouldUseLDAP(ac.Auth.AutoAuthMechanism) {
			secretRef := mdb.Spec.Security.Authentication.Agents.AutomationPasswordSecretRef
			autoConfigPassword, err := secret.ReadKey(r.client, secretRef.Key, kube.ObjectKey(mdb.Namespace, secretRef.Name))
			if err != nil {
				return workflow.Failed(fmt.Sprintf("error reading automation config  password: %s", err)), false
			}
			authOpts.AutoPwd = autoConfigPassword
			userOpts := authentication.UserOptions{}
			agentName := mdb.Spec.Security.Authentication.Agents.AutomationUserName
			userOpts.AutomationSubject = agentName
			userOpts.MonitoringSubject = agentName
			userOpts.BackupSubject = agentName
			authOpts.UserOptions = userOpts
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
		userOpts, err := r.readAgentSubjectsFromSecret(mdb.Namespace, mdb.Spec.Security.AgentClientCertificateSecretName(), log)
		err = client.IgnoreNotFound(err)
		if err != nil {
			return workflow.Failed(err.Error()), true
		}

		authOpts.UserOptions = userOpts
		if err := authentication.Disable(conn, authOpts, false, log); err != nil {
			return workflow.Failed(err.Error()), false
		}
	}
	return workflow.OK(), false
}

// configureAgentSubjects returns a new authentication.Options which has configured the Subject lines for the automation, monitoring
// and backup agents. The subjects are read from the "agent-certs" secret. This secret is generated by the operator when
// x509 is configured, but if this secret is provided by the user, custom x509 certificates can be provided and used by the agents.
// The Ops Manager user names for these agents will be configured based on the contents of the secret.
func (r *ReconcileCommonController) configureAgentSubjects(namespace string, secretKeySelector corev1.SecretKeySelector, authOpts authentication.Options, log *zap.SugaredLogger) (authentication.Options, error) {
	userOpts, err := r.readAgentSubjectsFromSecret(namespace, secretKeySelector, log)
	if err != nil {
		return authentication.Options{}, fmt.Errorf("error reading agent subjects from secret: %v", err)
	}
	authOpts.UserOptions = userOpts
	return authOpts, nil
}

func (r *ReconcileCommonController) readAgentSubjectsFromSecret(namespace string, secretKeySelector corev1.SecretKeySelector, log *zap.SugaredLogger) (authentication.UserOptions, error) {
	userOpts := authentication.UserOptions{}
	agentCerts, err := secret.ReadStringData(r.client, kube.ObjectKey(namespace, secretKeySelector.Name))
	if err != nil {
		return userOpts, err
	}

	numAgentCerts := len(agentCerts)
	if numAgentCerts != NumAgents && numAgentCerts != 1 {
		return userOpts, fmt.Errorf("must provided either 1 or 3 agent certificates found %d", numAgentCerts)
	}

	var automationAgentCert string
	var ok bool
	if automationAgentCert, ok = agentCerts[secretKeySelector.Key]; !ok {
		return userOpts, fmt.Errorf("could not find certificate with name %s", secretKeySelector.Key)
	}

	log.Debugf("Got %d certificate(s) in the Secret", numAgentCerts)
	var automationAgentSubject, backupAgentSubject, monitoringAgentSubject string
	automationAgentSubject, err = getSubjectFromCertificate(automationAgentCert)
	if err != nil {
		return userOpts, fmt.Errorf("error extracting automation agent subject is not present %e", err)
	}

	log.Debugf("Automation certificate subject is %s", automationAgentSubject)

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

		// TODO: These should be removed, but there must be an upgrade path from 3 to 1 agents environments.
		MonitoringSubject: monitoringAgentSubject,
		BackupSubject:     backupAgentSubject,
	}, nil
}

func (r *ReconcileCommonController) clearProjectAuthenticationSettings(conn om.Connection, mdb *mdbv1.MongoDB, processNames []string, log *zap.SugaredLogger) error {
	userOpts, err := r.readAgentSubjectsFromSecret(mdb.Namespace, mdb.Spec.Security.AgentClientCertificateSecretName(), log)
	err = client.IgnoreNotFound(err)
	if err != nil {
		return err
	}
	log.Infof("Disabling authentication for project: %s", conn.GroupName())
	disableOpts := authentication.Options{
		ProcessNames: processNames,
		UserOptions:  userOpts,
	}

	return authentication.Disable(conn, disableOpts, true, log)
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

func clusterDomainOrDefault(clusterDomain string) string {
	if clusterDomain == "" {
		return ClusterDomain
	}

	return clusterDomain
}

func readProjectConfigAndCredentials(client kubernetesClient.Client, mdb mdbv1.MongoDB) (mdbv1.ProjectConfig, mdbv1.Credentials, error) {
	projectConfig, err := project.ReadProjectConfig(client, objectKey(mdb.Namespace, mdb.Spec.GetProject()), mdb.Name)
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, fmt.Errorf("error reading project %s", err)
	}
	credsConfig, err := project.ReadCredentials(client, kube.ObjectKey(mdb.Namespace, mdb.Spec.Credentials))
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, fmt.Errorf("error reading Credentials secret: %s", err)
	}
	return projectConfig, credsConfig, nil
}

// newPodVars initializes a PodEnvVars instance based on the values of the provided Ops Manager connection, project config
// and connection spec
func newPodVars(conn om.Connection, projectConfig mdbv1.ProjectConfig, spec mdbv1.ConnectionSpec) *env.PodEnvVars {
	podVars := &env.PodEnvVars{}
	podVars.BaseURL = conn.BaseURL()
	podVars.ProjectID = conn.GroupID()
	podVars.User = conn.User()
	podVars.LogLevel = string(spec.LogLevel)
	podVars.SSLProjectConfig = projectConfig.SSLProjectConfig
	return podVars
}
