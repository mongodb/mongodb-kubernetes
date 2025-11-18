package operator

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/blang/semver"
	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	rolev1 "github.com/mongodb/mongodb-kubernetes/api/v1/role"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/agentVersionManagement"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/passwordhash"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func automationConfigFirstMsg(resourceType string, valueToSet string) string {
	return fmt.Sprintf("About to set `%s` to %s. automationConfig needs to be updated first", resourceType, valueToSet)
}

// ReconcileCommonController is the "parent" controller that is included into each specific controller and allows
// to reuse the common functionality
type ReconcileCommonController struct {
	client kubernetesClient.Client
	secrets.SecretClient

	resourceWatcher *watch.ResourceWatcher
}

func NewReconcileCommonController(ctx context.Context, client client.Client) *ReconcileCommonController {
	newClient := kubernetesClient.NewClient(client)
	var vaultClient *vault.VaultClient

	if vault.IsVaultSecretBackend() {
		var err error
		// creates the in-cluster config, we cannot use the controller-runtime manager client
		// since the manager hasn't been started yet. Using it will cause error "the cache is not started, can not read objects"
		config, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			panic(err.Error())
		}
		vaultClient, err = vault.InitVaultClient(ctx, clientset)
		if err != nil {
			panic(fmt.Sprintf("Can not initialize vault client: %s", err))
		}
		if err := vaultClient.Login(); err != nil {
			panic(xerrors.Errorf("unable to log in with vault client: %w", err))
		}
	}
	return &ReconcileCommonController{
		client: newClient,
		SecretClient: secrets.SecretClient{
			VaultClient: vaultClient,
			KubeClient:  newClient,
		},
		resourceWatcher: watch.NewResourceWatcher(),
	}
}

// ensureRoles will first check if both roles and roleRefs are populated. If both are, it will return an error, which is inline with the webhook validation rules.
// Otherwise, if roles is populated, then it will extract the list of roles and check if they are already set in Ops Manager. If they are not, it will update the roles in Ops Manager.
// If roleRefs is populated, it will extract the list of roles from the referenced resources and check if they are already set in Ops Manager. If they are not, it will update the roles in Ops Manager.
func (r *ReconcileCommonController) ensureRoles(ctx context.Context, db mdbv1.DbCommonSpec, enableClusterMongoDBRoles bool, conn om.Connection, mongodbResourceNsName types.NamespacedName, log *zap.SugaredLogger) workflow.Status {
	localRoles := db.GetSecurity().Roles
	roleRefs := db.GetSecurity().RoleRefs

	if len(localRoles) > 0 && len(roleRefs) > 0 {
		return workflow.Failed(xerrors.Errorf("At most one one of roles or roleRefs can be non-empty."))
	}

	var roles []mdbv1.MongoDBRole
	if len(roleRefs) > 0 {
		if !enableClusterMongoDBRoles {
			return workflow.Failed(xerrors.Errorf("RoleRefs are not supported when ClusterMongoDBRoles are disabled. Please enable ClusterMongoDBRoles in the operator configuration. This can be done by setting the operator.enableClusterMongoDBRoles to true in the helm values file, which will automatically installed the necessary RBAC. Alternatively, it can be enabled by adding -watch-resource=clustermongodbroles flag to the operator deployment, and manually creating the necessary RBAC."))
		}
		var err error
		roles, err = r.getRoleRefs(ctx, roleRefs, mongodbResourceNsName, db.Version)
		if err != nil {
			return workflow.Failed(err)
		}
	} else {
		roles = localRoles
	}

	d, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err)
	}
	dRoles := d.GetRoles()
	if reflect.DeepEqual(dRoles, roles) {
		return workflow.OK()
	}

	// clone roles list to avoid mutating the spec in normalizePrivilegeResource
	newRoles := make([]mdbv1.MongoDBRole, len(roles))
	for i := range roles {
		newRoles[i] = *roles[i].DeepCopy()
	}
	roles = newRoles

	for roleIdx := range roles {
		// HELP-20798: the agent deals correctly with a null value for
		// privileges only when creating a role, not when updating
		// we work around it by explicitly passing empty array
		if roles[roleIdx].Privileges == nil {
			roles[roleIdx].Privileges = []mdbv1.Privilege{}
		}

		for privilegeIdx := range roles[roleIdx].Privileges {
			roles[roleIdx].Privileges[privilegeIdx].Resource = normalizePrivilegeResource(roles[roleIdx].Privileges[privilegeIdx].Resource)
		}
	}

	log.Infof("Roles have been changed. Updating deployment in Ops Manager.")
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.SetRoles(roles)
			return nil
		},
		log,
	)
	if err != nil {
		return workflow.Failed(err)
	}
	return workflow.OK()
}

// normalizePrivilegeResource ensures that mutually exclusive fields are not passed at the same time and ensures backwards compatibility by
// preserving empty strings for db and collection.
// This function was introduced after we've changed db and collection fields to *string allowing to omit the field from serialization (CLOUDP-349078).
func normalizePrivilegeResource(resource mdbv1.Resource) mdbv1.Resource {
	if resource.Cluster != nil && *resource.Cluster {
		// for cluster-wide privilege mongod is not accepting even empty strings in db and collection
		resource.Db = nil
		resource.Collection = nil
	} else {
		// for backwards compatibility we must convert "not specified" fields as empty strings
		if resource.Db == nil {
			resource.Db = ptr.To("")
		}
		if resource.Collection == nil {
			resource.Collection = ptr.To("")
		}
	}

	return resource
}

// getRoleRefs retrieves the roles from the referenced resources. It will return an error if any of the referenced resources are not found.
// It will also add the referenced resources to the resource watcher, so that they are watched for changes.
// The referenced resources are expected to be of kind ClusterMongoDBRole.
// This implementation is prepared for a future namespaced variant of ClusterMongoDBRole.
func (r *ReconcileCommonController) getRoleRefs(ctx context.Context, roleRefs []mdbv1.MongoDBRoleRef, mongodbResourceNsName types.NamespacedName, mdbVersion string) ([]mdbv1.MongoDBRole, error) {
	roles := make([]mdbv1.MongoDBRole, len(roleRefs))

	for idx, ref := range roleRefs {
		var role mdbv1.MongoDBRole
		switch ref.Kind {

		case util.ClusterMongoDBRoleKind:
			customRole := &rolev1.ClusterMongoDBRole{}

			err := r.client.Get(ctx, types.NamespacedName{Name: ref.Name}, customRole)
			if err != nil {
				if apiErrors.IsNotFound(err) {
					return nil, xerrors.Errorf("ClusterMongoDBRole '%s' not found. If the resource was deleted, the role is still present in MongoDB. To correctly remove a role from MongoDB, please remove the reference from spec.security.roleRefs.", ref.Name)
				}
				return nil, xerrors.Errorf("Failed to retrieve ClusterMongoDBRole '%s': %w", ref.Name, err)
			}

			if res := mdbv1.RoleIsCorrectlyConfigured(customRole.Spec.MongoDBRole, mdbVersion); res.Level == v1.ErrorLevel {
				return nil, xerrors.Errorf("Error validating role '%s' - %s", ref.Name, res.Msg)
			}

			r.resourceWatcher.AddWatchedResourceIfNotAdded(ref.Name, "", watch.ClusterMongoDBRole, mongodbResourceNsName)
			role = customRole.Spec.MongoDBRole

		default:
			return nil, xerrors.Errorf("Invalid value %s for roleRef.kind. It must be %s.", ref.Kind, util.ClusterMongoDBRoleKind)
		}

		roles[idx] = role
	}

	return roles, nil
}

// updateStatus updates the status for the CR using patch operation. Note, that the resource status is mutated and
// it's important to pass resource by pointer to all methods which invoke current 'updateStatus'.
func (r *ReconcileCommonController) updateStatus(ctx context.Context, reconciledResource v1.CustomResourceReadWriter, st workflow.Status, log *zap.SugaredLogger, statusOptions ...status.Option) (reconcile.Result, error) {
	return commoncontroller.UpdateStatus(ctx, r.client, reconciledResource, st, log, statusOptions...)
}

type WatcherResource interface {
	ObjectKey() client.ObjectKey
	GetSecurity() *mdbv1.Security
	GetConnectionSpec() *mdbv1.ConnectionSpec
}

// SetupCommonWatchers is the common shared method for all controller to watch the following resources:
//   - OM related cm and secret
//   - TLS related secrets, if enabled, this includes x509 internal authentication secrets
//
// Note: everything is watched under the same namespace as the objectKey
// in case getSecretNames func is nil, we will default to common mechanism to get the secret names
// TODO: unify the watcher setup with the secret creation/mounting code in database creation
func (r *ReconcileCommonController) SetupCommonWatchers(watcherResource WatcherResource, getTLSSecretNames func() []string, getInternalAuthSecretNames func() []string, resourceNameForSecret string) {
	// We remove all watched resources
	objectToReconcile := watcherResource.ObjectKey()
	r.resourceWatcher.RemoveDependentWatchedResources(objectToReconcile)

	// And then add the ones we care about
	connectionSpec := watcherResource.GetConnectionSpec()
	if connectionSpec != nil {
		r.resourceWatcher.RegisterWatchedMongodbResources(objectToReconcile, connectionSpec.GetProject(), connectionSpec.Credentials)
	}

	security := watcherResource.GetSecurity()
	// And TLS if needed
	if security.IsTLSEnabled() {
		var secretNames []string
		if getTLSSecretNames != nil {
			secretNames = getTLSSecretNames()
		} else {
			secretNames = []string{security.MemberCertificateSecretName(resourceNameForSecret)}
			if security.ShouldUseX509("") {
				secretNames = append(secretNames, security.AgentClientCertificateSecretName(resourceNameForSecret))
			}
		}
		r.resourceWatcher.RegisterWatchedTLSResources(objectToReconcile, security.TLSConfig.CA, secretNames)
	}

	if security.GetInternalClusterAuthenticationMode() == util.X509 {
		var secretNames []string
		if getInternalAuthSecretNames != nil {
			secretNames = getInternalAuthSecretNames()
		} else {
			secretNames = []string{security.InternalClusterAuthSecretName(resourceNameForSecret)}
		}
		for _, secretName := range secretNames {
			r.resourceWatcher.AddWatchedResourceIfNotAdded(secretName, objectToReconcile.Namespace, watch.Secret, objectToReconcile)
		}
	}
}

// GetResource populates the provided runtime.Object with some additional error handling
func (r *ReconcileCommonController) GetResource(ctx context.Context, request reconcile.Request, resource v1.CustomResourceReadWriter, log *zap.SugaredLogger) (reconcile.Result, error) {
	return commoncontroller.GetResource(ctx, r.client, request, resource, log)
}

// prepareResourceForReconciliation finds the object being reconciled. Returns the reconcile result and any error that
// occurred.
func (r *ReconcileCommonController) prepareResourceForReconciliation(ctx context.Context, request reconcile.Request, resource v1.CustomResourceReadWriter, log *zap.SugaredLogger) (reconcile.Result, error) {
	if result, err := r.GetResource(ctx, request, resource, log); err != nil {
		return result, err
	}

	// Reset warnings so that they are not stale, will populate accurate warnings in reconciliation
	resource.SetWarnings([]status.Warning{})

	return reconcile.Result{}, nil
}

// checkIfHasExcessProcesses will check if the project has excess processes.
// Also, it removes the tag ExternallyManaged from the project in this case as
// the user may need to clean the resources from OM UI if they move the
// resource to another project (as recommended by the migration instructions).
func checkIfHasExcessProcesses(conn om.Connection, resourceName string, log *zap.SugaredLogger) workflow.Status {
	deployment, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err)
	}
	excessProcesses := deployment.GetNumberOfExcessProcesses(resourceName)
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

// validateInternalClusterCertsAndCheckTLSType verifies that all the x509 internal cluster certs exist and return whether they are built following the kubernetes.io/tls secret type (tls.crt/tls.key entries).
// TODO: this is almost the same as certs.EnsureSSLCertsForStatefulSet, we should centralize the functionality
func (r *ReconcileCommonController) validateInternalClusterCertsAndCheckTLSType(ctx context.Context, configurator certs.X509CertConfigurator, opts certs.Options, log *zap.SugaredLogger) error {
	secretName := opts.InternalClusterSecretName

	err := certs.VerifyAndEnsureCertificatesForStatefulSet(ctx, configurator.GetSecretReadClient(), configurator.GetSecretWriteClient(), secretName, opts, log)
	if err != nil {
		return xerrors.Errorf("the secret object '%s' does not contain all the certificates needed: %w", secretName, err)
	}

	secretName = fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)

	// Validates that the secret is valid
	if err := certs.ValidateCertificates(ctx, r.client, secretName, opts.Namespace, log); err != nil {
		return err
	}
	return nil
}

// ensureBackupConfigurationAndUpdateStatus configures backup in Ops Manager based on the MongoDB resources spec
func (r *ReconcileCommonController) ensureBackupConfigurationAndUpdateStatus(ctx context.Context, conn om.Connection, mdb backup.ConfigReaderUpdater, secretsReader secrets.SecretClient, log *zap.SugaredLogger) workflow.Status {
	statusOpt, opts := backup.EnsureBackupConfigurationInOpsManager(ctx, mdb, secretsReader, conn.GroupID(), conn, conn, conn, log)
	if len(opts) > 0 {
		if _, err := r.updateStatus(ctx, mdb, statusOpt, log, opts...); err != nil {
			return workflow.Failed(err)
		}
	}
	return statusOpt
}

// validateMongoDBResource performs validation on the MongoDBResource
func validateMongoDBResource(mdb *mdbv1.MongoDB, conn om.Connection) workflow.Status {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return workflow.Failed(err)
	}

	if status := validateScram(mdb, ac); !status.IsOK() {
		return status
	}

	return workflow.OK()
}

func ensureSupportedOpsManagerVersion(conn om.Connection) workflow.Status {
	omVersionString := conn.OpsManagerVersion()
	if !omVersionString.IsCloudManager() {
		omVersion, err := omVersionString.Semver()
		if err != nil {
			return workflow.Failed(xerrors.Errorf("Failed when trying to parse Ops Manager version"))
		}
		if omVersion.LT(semver.MustParse(oldestSupportedOpsManagerVersion)) {
			return workflow.Unsupported("This MongoDB ReplicaSet is managed by Ops Manager version %s, which is not supported by this version of the operator. Please upgrade it to a version >=%s", omVersion, oldestSupportedOpsManagerVersion)
		}
	}
	return workflow.OK()
}

// scaleStatefulSet sets the number of replicas for a StatefulSet and returns a reference of the updated resource.
func (r *ReconcileCommonController) scaleStatefulSet(ctx context.Context, namespace, name string, replicas int32, client kubernetesClient.Client) (appsv1.StatefulSet, error) {
	if set, err := client.GetStatefulSet(ctx, kube.ObjectKey(namespace, name)); err != nil {
		return set, err
	} else {
		set.Spec.Replicas = &replicas
		return client.UpdateStatefulSet(ctx, set)
	}
}

// validateScram ensures that the SCRAM configuration is valid for the MongoDBResource
func validateScram(mdb *mdbv1.MongoDB, ac *om.AutomationConfig) workflow.Status {
	specVersion, err := semver.Make(util.StripEnt(mdb.Spec.GetMongoDBVersion()))
	if err != nil {
		return workflow.Failed(err)
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
		subjects, _, err := authentication.GetCertificateSubject(cert)
		if err != nil {
			return "", err
		}
		return subjects, nil
	}
	if len(rest) > 0 {
		subjects, _, err := authentication.GetCertificateSubject(string(rest))
		if err != nil {
			return "", err
		}
		return subjects, nil
	}
	return "", xerrors.Errorf("unable to extract the subject line from the provided certificate")
}

// updateOmAuthentication examines the state of Ops Manager and the desired state of the MongoDB resource and
// enables/disables authentication. If the authentication can't be fully configured, a boolean value indicating that
// an additional reconciliation needs to be queued up to fully make the authentication changes is returned.
// Note: updateOmAuthentication needs to be called before reconciling other auth related settings.
func (r *ReconcileCommonController) updateOmAuthentication(ctx context.Context, conn om.Connection, processNames []string, ar authentication.AuthResource, agentCertPath, caFilepath, clusterFilePath string, isRecovering bool, log *zap.SugaredLogger) (status workflow.Status, multiStageReconciliation bool) {
	// don't touch authentication settings if resource has not been configured with them
	if ar.GetSecurity() == nil || ar.GetSecurity().Authentication == nil {
		return workflow.OK(), false
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return workflow.Failed(err), false
	}

	// if we have changed the internal cluster auth, we need to update the ac first
	authenticationMode := ar.GetSecurity().GetInternalClusterAuthenticationMode()
	err = r.setupInternalClusterAuthIfItHasChanged(conn, processNames, authenticationMode, clusterFilePath, isRecovering)
	if err != nil {
		return workflow.Failed(err), false
	}

	// we need to wait for all agents to be ready before configuring any authentication settings
	if err := om.WaitForReadyState(conn, processNames, isRecovering, log); err != nil {
		return workflow.Failed(err), false
	}

	clientCerts := util.OptionalClientCertficates
	if ar.GetSecurity().RequiresClientTLSAuthentication() {
		clientCerts = util.RequireClientCertificates
	}

	scramAgentUserName := util.AutomationAgentUserName
	// only use the default name if there is not already a configured username
	if ac.Auth.AutoUser != "" && ac.Auth.AutoUser != scramAgentUserName {
		scramAgentUserName = ac.Auth.AutoUser
	}

	authOpts := authentication.Options{
		Mechanisms:         mdbv1.ConvertAuthModesToStrings(ar.GetSecurity().Authentication.Modes),
		ProcessNames:       processNames,
		AuthoritativeSet:   !ar.GetSecurity().Authentication.IgnoreUnknownUsers,
		AgentMechanism:     ar.GetSecurity().GetAgentMechanism(ac.Auth.AutoAuthMechanism),
		ClientCertificates: clientCerts,
		AutoUser:           scramAgentUserName,
		AutoLdapGroupDN:    ar.GetSecurity().Authentication.Agents.AutomationLdapGroupDN,
		CAFilePath:         caFilepath,
		MongoDBResource:    types.NamespacedName{Namespace: ar.GetNamespace(), Name: ar.GetName()},
	}
	var databaseSecretPath string
	if r.VaultClient != nil {
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}
	if ar.IsLDAPEnabled() {
		bindUserPassword, err := r.ReadSecretKey(ctx, kube.ObjectKey(ar.GetNamespace(), ar.GetSecurity().Authentication.Ldap.BindQuerySecretRef.Name), databaseSecretPath, "password")
		if err != nil {
			return workflow.Failed(xerrors.Errorf("error reading bind user password: %w", err)), false
		}

		caContents := ""
		ca := ar.GetSecurity().Authentication.Ldap.CAConfigMapRef
		if ca != nil {
			log.Debugf("Sending CA file to Pods via AutomationConfig: %s/%s/%s", ar.GetNamespace(), ca.Name, ca.Key)
			caContents, err = configmap.ReadKey(ctx, r.client, ca.Key, types.NamespacedName{Name: ca.Name, Namespace: ar.GetNamespace()})
			if err != nil {
				return workflow.Failed(xerrors.Errorf("error reading CA configmap: %w", err)), false
			}
		}

		authOpts.Ldap = ar.GetLDAP(bindUserPassword, caContents)
	}

	if ar.IsOIDCEnabled() {
		authOpts.OIDCProviderConfigs = authentication.MapOIDCProviderConfigs(ar.GetSecurity().Authentication.OIDCProviderConfigs)
	}

	log.Debugf("Using authentication options %+v", authentication.Redact(authOpts))

	agentCertSecretName := ar.GetSecurity().AgentClientCertificateSecretName(ar.GetName())
	agentCertSecretSelector := corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: agentCertSecretName},
		Key:                  corev1.TLSCertKey,
	}

	wantToEnableAuthentication := ar.GetSecurity().Authentication.Enabled
	if wantToEnableAuthentication && canConfigureAuthentication(ac, ar.GetSecurity().Authentication.GetModes(), log) {
		log.Info("Configuring authentication for MongoDB resource")

		if ar.GetSecurity().ShouldUseX509(ac.Auth.AutoAuthMechanism) || ar.GetSecurity().ShouldUseClientCertificates() {
			authOpts, err = r.configureAgentSubjects(ctx, ar.GetNamespace(), agentCertSecretSelector, authOpts, log)
			if err != nil {
				return workflow.Failed(xerrors.Errorf("error configuring agent subjects: %w", err)), false
			}
			authOpts.AgentsShouldUseClientAuthentication = ar.GetSecurity().ShouldUseClientCertificates()
			authOpts.AutoPEMKeyFilePath = agentCertPath
		}
		if ar.GetSecurity().ShouldUseLDAP(ac.Auth.AutoAuthMechanism) {
			secretRef := ar.GetSecurity().Authentication.Agents.AutomationPasswordSecretRef
			autoConfigPassword, err := r.ReadSecretKey(ctx, kube.ObjectKey(ar.GetNamespace(), secretRef.Name), databaseSecretPath, secretRef.Key)
			if err != nil {
				return workflow.Failed(xerrors.Errorf("error reading automation agent password: %w", err)), false
			}

			authOpts.AutoPwd = autoConfigPassword
			userOpts := authentication.UserOptions{}
			agentName := ar.GetSecurity().Authentication.Agents.AutomationUserName
			userOpts.AutomationSubject = agentName
			authOpts.UserOptions = userOpts
		}

		if err := authentication.Configure(ctx, r.client, conn, authOpts, isRecovering, log); err != nil {
			return workflow.Failed(err), false
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
		userOpts, err := r.readAgentSubjectsFromSecret(ctx, ar.GetNamespace(), agentCertSecretSelector, log)
		err = client.IgnoreNotFound(err)
		if err != nil {
			return workflow.Failed(err), true
		}

		authOpts.UserOptions = userOpts

		if err := authentication.Disable(ctx, r.client, conn, authOpts, false, log); err != nil {
			return workflow.Failed(err), false
		}
	}
	return workflow.OK(), false
}

// configureAgentSubjects returns a new authentication.Options which has configured the Subject lines for the automation agent.
// The Ops Manager user names for these agents will be configured based on the contents of the secret.
func (r *ReconcileCommonController) configureAgentSubjects(ctx context.Context, namespace string, secretKeySelector corev1.SecretKeySelector, authOpts authentication.Options, log *zap.SugaredLogger) (authentication.Options, error) {
	userOpts, err := r.readAgentSubjectsFromSecret(ctx, namespace, secretKeySelector, log)
	if err != nil {
		return authentication.Options{}, xerrors.Errorf("error reading agent subjects from secret: %w", err)
	}
	authOpts.UserOptions = userOpts
	return authOpts, nil
}

func (r *ReconcileCommonController) readAgentSubjectsFromSecret(ctx context.Context, namespace string, secretKeySelector corev1.SecretKeySelector, log *zap.SugaredLogger) (authentication.UserOptions, error) {
	userOpts := authentication.UserOptions{}

	var databaseSecretPath string
	if r.VaultClient != nil {
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}
	agentCerts, err := r.ReadSecret(ctx, kube.ObjectKey(namespace, secretKeySelector.Name), databaseSecretPath)
	if err != nil {
		return userOpts, err
	}

	automationAgentCert, ok := agentCerts[secretKeySelector.Key]
	if !ok {
		return userOpts, xerrors.Errorf("could not find certificate with name %s", secretKeySelector.Key)
	}

	automationAgentSubject, err := getSubjectFromCertificate(automationAgentCert)
	if err != nil {
		return userOpts, xerrors.Errorf("error extracting automation agent subject is not present %w", err)
	}

	log.Debugf("Automation certificate subject is %s", automationAgentSubject)

	return authentication.UserOptions{
		AutomationSubject: automationAgentSubject,
	}, nil
}

func (r *ReconcileCommonController) clearProjectAuthenticationSettings(ctx context.Context, conn om.Connection, mdb *mdbv1.MongoDB, processNames []string, log *zap.SugaredLogger) error {
	agentCertSecretName := mdb.Spec.GetSecurity().AgentClientCertificateSecretName(mdb.Name)
	agentCertSecretSelector := corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: agentCertSecretName},
		Key:                  corev1.TLSCertKey,
	}
	userOpts, err := r.readAgentSubjectsFromSecret(ctx, mdb.Namespace, agentCertSecretSelector, log)
	err = client.IgnoreNotFound(err)
	if err != nil {
		return err
	}
	log.Infof("Disabling authentication for project: %s", conn.GroupName())
	disableOpts := authentication.Options{
		ProcessNames:    processNames,
		UserOptions:     userOpts,
		MongoDBResource: types.NamespacedName{Namespace: mdb.Namespace, Name: mdb.Name},
	}

	return authentication.Disable(ctx, r.client, conn, disableOpts, true, log)
}

// ensureX509SecretAndCheckTLSType checks if the secrets containing the certificates are present and whether the certificate are of kubernetes.io/tls type.
func (r *ReconcileCommonController) ensureX509SecretAndCheckTLSType(ctx context.Context, configurator certs.X509CertConfigurator, currentAuthMechanism string, log *zap.SugaredLogger) workflow.Status {
	security := configurator.GetDbCommonSpec().GetSecurity()
	authSpec := security.Authentication
	if authSpec == nil || !security.Authentication.Enabled {
		return workflow.OK()
	}

	if security.ShouldUseX509(currentAuthMechanism) || security.ShouldUseClientCertificates() {
		if !security.IsTLSEnabled() {
			return workflow.Failed(xerrors.Errorf("Authentication mode for project is x509 but this MDB resource is not TLS enabled"))
		}
		agentSecretName := security.AgentClientCertificateSecretName(configurator.GetName())
		err := certs.VerifyAndEnsureClientCertificatesForAgentsAndTLSType(ctx, configurator.GetSecretReadClient(), configurator.GetSecretWriteClient(), kube.ObjectKey(configurator.GetNamespace(), agentSecretName), log)
		if err != nil {
			return workflow.Failed(err)
		}
	}

	if security.GetInternalClusterAuthenticationMode() == util.X509 {
		errors := make([]error, 0)
		for _, certOption := range configurator.GetCertOptions() {
			err := r.validateInternalClusterCertsAndCheckTLSType(ctx, configurator, certOption, log)
			if err != nil {
				errors = append(errors, err)
			}
		}
		if len(errors) > 0 {
			return workflow.Failed(xerrors.Errorf("failed ensuring internal cluster authentication certs %w", errors[0]))
		}
	}

	return workflow.OK()
}

// setupInternalClusterAuthIfItHasChanged enables internal cluster auth if possible in case the path has changed and did exist before.
func (r *ReconcileCommonController) setupInternalClusterAuthIfItHasChanged(conn om.Connection, names []string, clusterAuth string, filePath string, isRecovering bool) error {
	if filePath == "" {
		return nil
	}
	err := conn.ReadUpdateDeployment(func(deployment om.Deployment) error {
		deployment.SetInternalClusterFilePathOnlyIfItThePathHasChanged(names, filePath, clusterAuth, isRecovering)
		return nil
	}, zap.S())
	return err
}

// getAgentVersion handles the common logic for error handling and instance initialisation
// when retrieving the agent version from a controller
func (r *ReconcileCommonController) getAgentVersion(conn om.Connection, omVersion string, isAppDB bool, log *zap.SugaredLogger) (string, error) {
	m, err := agentVersionManagement.GetAgentVersionManager()
	if err != nil || m == nil {
		return "", xerrors.Errorf("not able to init agentVersionManager: %w", err)
	}

	if agentVersion, err := m.GetAgentVersion(conn, omVersion, isAppDB); err != nil {
		log.Errorf("Failed to get the agent version from the Agent Version manager: %s", err)
		return "", err
	} else {
		log.Debugf("Using agent version %s", agentVersion)
		return agentVersion, nil
	}
}

// deleteClusterResources removes all resources that are associated with the given resource owner in a given cluster.
func (r *ReconcileCommonController) deleteClusterResources(ctx context.Context, client kubernetesClient.Client, clusterName string, resourceOwner v1.ObjectOwner, log *zap.SugaredLogger) error {
	objectKey := resourceOwner.ObjectKey()

	// cleanup resources in the namespace as the MongoDB with the corresponding label.
	cleanupOptions := mdbv1.MongodbCleanUpOptions{
		Namespace: resourceOwner.GetNamespace(),
		Labels:    resourceOwner.GetOwnerLabels(),
	}

	var errs error
	if err := client.DeleteAllOf(ctx, &corev1.Service{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed Services associated with %s in cluster %s", objectKey, clusterName)
	}

	if err := client.DeleteAllOf(ctx, &appsv1.StatefulSet{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed StatefulSets associated with %s in cluster %s", objectKey, clusterName)
	}

	if err := client.DeleteAllOf(ctx, &corev1.ConfigMap{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed ConfigMaps associated with %s in cluster %s", objectKey, clusterName)
	}

	if err := client.DeleteAllOf(ctx, &corev1.Secret{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed Secrets associated with %s in cluster %s", objectKey, clusterName)
	}

	r.resourceWatcher.RemoveDependentWatchedResources(objectKey)

	return errs
}

// agentCertHashAndPath returns a hash of an agent certificate along with file path
// to the said certificate. File path also contains the hash.
func (r *ReconcileCommonController) agentCertHashAndPath(ctx context.Context, log *zap.SugaredLogger, namespace, agentCertSecretName string, appdbSecretPath string) (string, string) {
	agentCertHash := enterprisepem.ReadHashFromSecret(ctx, r.SecretClient, namespace, agentCertSecretName, appdbSecretPath, log)
	agentCertPath := ""
	if agentCertHash != "" {
		agentCertPath = filepath.Join(util.AgentCertMountPath, agentCertHash)
	}

	return agentCertHash, agentCertPath
}

// isPrometheusSupported checks if Prometheus integration can be enabled.
//
// Prometheus is only enabled in Cloud Manager and Ops Manager 5.9 (6.0) and above.
func isPrometheusSupported(conn om.Connection) bool {
	if conn.OpsManagerVersion().IsCloudManager() {
		return true
	}

	omVersion, err := conn.OpsManagerVersion().Semver()
	return err == nil && omVersion.GTE(semver.MustParse("5.9.0"))
}

// UpdatePrometheus configures Prometheus on the Deployment for this resource.
func UpdatePrometheus(ctx context.Context, d *om.Deployment, conn om.Connection, prometheus *mdbcv1.Prometheus, sClient secrets.SecretClient, namespace string, certName string, log *zap.SugaredLogger) error {
	if prometheus == nil {
		return nil
	}

	if !isPrometheusSupported(conn) {
		log.Info("Prometheus can't be enabled, Prometheus is not supported in this version of Ops Manager")
		return nil
	}

	var err error
	var password string

	secretName := prometheus.PasswordSecretRef.Name
	if vault.IsVaultSecretBackend() {
		operatorSecretPath := sClient.VaultClient.OperatorSecretPath()
		passwordString := fmt.Sprintf("%s/%s/%s", operatorSecretPath, namespace, secretName)
		keyedPassword, err := sClient.VaultClient.ReadSecretString(passwordString)
		if err != nil {
			log.Infof("Prometheus can't be enabled, %s", err)
			return err
		}

		var ok bool
		password, ok = keyedPassword[prometheus.GetPasswordKey()]
		if !ok {
			errMsg := fmt.Sprintf("Prometheus password %s not in Secret %s", prometheus.GetPasswordKey(), passwordString)
			log.Info(errMsg)
			return xerrors.Errorf(errMsg)
		}
	} else {
		secretNamespacedName := types.NamespacedName{Name: secretName, Namespace: namespace}
		password, err = secret.ReadKey(ctx, sClient, prometheus.GetPasswordKey(), secretNamespacedName)
		if err != nil {
			log.Infof("Prometheus can't be enabled, %s", err)
			return err
		}
	}

	hash, salt := passwordhash.GenerateHashAndSaltForPassword(password)
	d.ConfigurePrometheus(prometheus, hash, salt, certName)

	return nil
}

// canConfigureAuthentication determines if based on the existing state of Ops Manager
// it is possible to configure the authentication mechanisms specified by the given MongoDB resource
// during this reconciliation. This function may return a different value on the next reconciliation
// if the state of Ops Manager has been changed.
func canConfigureAuthentication(ac *om.AutomationConfig, authenticationModes []string, log *zap.SugaredLogger) bool {
	attemptingToEnableX509 := !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) && stringutil.Contains(authenticationModes, util.X509)
	canEnableX509InOpsManager := ac.Deployment.AllProcessesAreTLSEnabled() || ac.Deployment.NumberOfProcesses() == 0

	log.Debugw("canConfigureAuthentication",
		"attemptingToEnableX509", attemptingToEnableX509,
		"deploymentAuthMechanisms", ac.Auth.DeploymentAuthMechanisms,
		"modes", authenticationModes,
		"canEnableX509InOpsManager", canEnableX509InOpsManager,
		"allProcessesAreTLSEnabled", ac.Deployment.AllProcessesAreTLSEnabled(),
		"numberOfProcesses", ac.Deployment.NumberOfProcesses())

	if attemptingToEnableX509 {
		return canEnableX509InOpsManager
	}

	// x509 is the only mechanism with restrictions determined based on Ops Manager state
	return true
}

// newPodVars initializes a PodEnvVars instance based on the values of the provided Ops Manager connection, project config
// and connection spec
func newPodVars(conn om.Connection, projectConfig mdbv1.ProjectConfig, logLevel mdbv1.LogLevel) *env.PodEnvVars {
	podVars := &env.PodEnvVars{}
	podVars.BaseURL = conn.BaseURL()
	podVars.ProjectID = conn.GroupID()
	podVars.User = conn.PublicKey()
	podVars.LogLevel = string(logLevel)
	podVars.SSLProjectConfig = projectConfig.SSLProjectConfig
	return podVars
}

func getVolumeFromStatefulSet(sts appsv1.StatefulSet, name string) (corev1.Volume, error) {
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return corev1.Volume{}, xerrors.Errorf("can't find volume %s in list of volumes: %v", name, sts.Spec.Template.Spec.Volumes)
}

// wasTLSSecretMounted checks whether TLS was previously enabled by looking at the state of the volumeMounts of the pod.
func wasTLSSecretMounted(ctx context.Context, secretGetter secret.Getter, currentSts appsv1.StatefulSet, mdb mdbv1.MongoDB, log *zap.SugaredLogger) bool {
	tlsVolume, err := getVolumeFromStatefulSet(currentSts, util.SecretVolumeName)
	if err != nil {
		return false
	}

	// With the new design, the volume is always mounted
	// But it is marked with optional.
	//
	// TLS was enabled if the secret it refers to is present

	secretName := tlsVolume.Secret.SecretName
	exists, err := secret.Exists(ctx, secretGetter, types.NamespacedName{
		Namespace: mdb.Namespace,
		Name:      secretName,
	},
	)
	if err != nil {
		log.Warnf("can't determine whether the TLS certificate secret exists or not: %s. Will assume it doesn't", err)
		return false
	}
	log.Debugf("checking if secret %s exists: %v", secretName, exists)

	return exists
}

// wasCAConfigMapMounted checks whether the CA ConfigMap  by looking at the state of the volumeMounts of the pod.
func wasCAConfigMapMounted(ctx context.Context, configMapGetter configmap.Getter, currentSts appsv1.StatefulSet, mdb mdbv1.MongoDB, log *zap.SugaredLogger) bool {
	caVolume, err := getVolumeFromStatefulSet(currentSts, util.ConfigMapVolumeCAMountPath)
	if err != nil {
		return false
	}

	// With the new design, the volume is always mounted
	// But it is marked with optional.
	//
	// The configMap was mounted if the configMap it refers to is present

	cmName := caVolume.ConfigMap.Name
	exists, err := configmap.Exists(ctx, configMapGetter, types.NamespacedName{
		Namespace: mdb.Namespace,
		Name:      cmName,
	},
	)
	if err != nil {
		log.Warnf("can't determine whether the TLS ConfigMap exists or not: %s. Will assume it doesn't", err)
		return false
	}
	log.Debugf("checking if ConfigMap %s exists: %v", cmName, exists)

	return exists
}

// publishAutomationConfigFirst will check if the Published State of the StatefulSet backed MongoDB Deployments
// needs to be updated first. In the case of unmounting certs, for instance, the certs should be not
// required anymore before we unmount them, or the automation-agent and readiness probe will never
// reach goal state.
func publishAutomationConfigFirst(ctx context.Context, getter kubernetesClient.Client, mdb mdbv1.MongoDB, lastSpec *mdbv1.MongoDbSpec, configFunc func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) bool {
	opts := configFunc(mdb)

	namespacedName := kube.ObjectKey(mdb.Namespace, opts.GetStatefulSetName())
	currentSts, err := getter.GetStatefulSet(ctx, namespacedName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// No need to publish state as this is a new StatefulSet
			log.Debugf("New StatefulSet %s", namespacedName)
			return false
		}

		log.Debugw(fmt.Sprintf("Error getting StatefulSet %s", namespacedName), "error", err)
		return false
	}

	databaseContainer := container.GetByName(util.DatabaseContainerName, currentSts.Spec.Template.Spec.Containers)
	volumeMounts := databaseContainer.VolumeMounts

	if !mdb.Spec.Security.IsTLSEnabled() && wasTLSSecretMounted(ctx, getter, currentSts, mdb, log) {
		log.Debug(automationConfigFirstMsg("security.tls.enabled", "false"))
		return true
	}

	if mdb.Spec.Security.TLSConfig.CA == "" && wasCAConfigMapMounted(ctx, getter, currentSts, mdb, log) {
		log.Debug(automationConfigFirstMsg("security.tls.CA", "empty"))
		return true

	}

	if opts.PodVars.SSLMMSCAConfigMap == "" && statefulset.VolumeMountWithNameExists(volumeMounts, construct.CaCertName) {
		log.Debug(automationConfigFirstMsg("SSLMMSCAConfigMap", "empty"))
		return true
	}

	if mdb.Spec.Security.GetAgentMechanism(opts.CurrentAgentAuthMode) != util.X509 && statefulset.VolumeMountWithNameExists(volumeMounts, util.AgentSecretName) {
		log.Debug(automationConfigFirstMsg("project.AuthMode", "empty"))
		return true
	}

	if opts.Replicas < int(*currentSts.Spec.Replicas) {
		log.Debug("Scaling down operation. automationConfig needs to be updated first")
		return true
	}

	if architectures.IsRunningStaticArchitecture(mdb.GetAnnotations()) {
		if mdb.Spec.IsInChangeVersion(lastSpec) {
			return true
		}
	}

	return false
}

// completionMessage is just a general message printed in the logs after mongodb resource is created/updated
func completionMessage(url, projectID string) string {
	return fmt.Sprintf("Please check the link %s/v2/%s to see the status of the deployment", url, projectID)
}

// getAnnotationsForResource returns all of the annotations that should be applied to the resource
// at the end of the reconciliation. The additional mongod options must be manually
// set as the wrapper type we use prevents a regular `json.Marshal` from working in this case due to
// the `json "-"` tag.
func getAnnotationsForResource(mdb *mdbv1.MongoDB) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(mdb.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)
	return finalAnnotations, nil
}

type PrometheusConfiguration struct {
	prometheus         *mdbcv1.Prometheus
	conn               om.Connection
	secretsClient      secrets.SecretClient
	namespace          string
	prometheusCertHash string
}

func ReconcileReplicaSetAC(ctx context.Context, d om.Deployment, spec mdbv1.DbCommonSpec, lastMongodConfig map[string]interface{}, resourceName string, rs om.ReplicaSetWithProcesses, caFilePath string, internalClusterPath string, pc *PrometheusConfiguration, log *zap.SugaredLogger) error {
	// it is not possible to disable internal cluster authentication once enabled
	if d.ExistingProcessesHaveInternalClusterAuthentication(rs.Processes) && spec.Security.GetInternalClusterAuthenticationMode() == "" {
		return xerrors.Errorf("cannot disable x509 internal cluster authentication")
	}

	excessProcesses := d.GetNumberOfExcessProcesses(resourceName)
	if excessProcesses > 0 {
		return xerrors.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
	}

	d.MergeReplicaSet(rs, spec.GetAdditionalMongodConfig().ToMap(), lastMongodConfig, log)
	d.AddMonitoringAndBackup(log, spec.GetSecurity().IsTLSEnabled(), caFilePath)
	d.ConfigureTLS(spec.GetSecurity(), caFilePath)
	d.ConfigureInternalClusterAuthentication(rs.GetProcessNames(), spec.GetSecurity().GetInternalClusterAuthenticationMode(), internalClusterPath)

	// if we don't set up a prometheus connection, then we don't want to set up prometheus for instance because we do not support it yet.
	if pc != nil {
		// At this point, we won't bubble-up the error we got from this
		// function, we don't want to fail the MongoDB resource because
		// Prometheus can't be enabled.
		_ = UpdatePrometheus(ctx, &d, pc.conn, pc.prometheus, pc.secretsClient, pc.namespace, pc.prometheusCertHash, log)
	}

	return nil
}

func ReconcileLogRotateSetting(conn om.Connection, agentConfig mdbv1.AgentConfig, log *zap.SugaredLogger) (workflow.Status, error) {
	if err := conn.ReadUpdateAgentsLogRotation(agentConfig, log); err != nil {
		return workflow.Failed(err), err
	}
	return workflow.OK(), nil
}
