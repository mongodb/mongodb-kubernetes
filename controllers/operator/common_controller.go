package operator

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

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
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	opMigration "github.com/mongodb/mongodb-kubernetes/controllers/operator/migration"
	pkgMigration "github.com/mongodb/mongodb-kubernetes/pkg/migration"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/agentVersionManagement"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
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

func (r *ReconcileCommonController) getRoleAnnotation(ctx context.Context, db mdbv1.DbCommonSpec, enableClusterMongoDBRoles bool, mongodbResourceNsName types.NamespacedName) (map[string]string, []string, error) {
	previousRoles, err := r.getRoleStrings(ctx, db, enableClusterMongoDBRoles, mongodbResourceNsName)
	if err != nil {
		return nil, nil, xerrors.Errorf("Error retrieving configured roles: %w", err)
	}

	annotationToAdd := make(map[string]string)
	rolesBytes, err := json.Marshal(previousRoles)
	if err != nil {
		return nil, nil, err
	}
	annotationToAdd[util.LastConfiguredRoles] = string(rolesBytes)

	return annotationToAdd, previousRoles, nil
}

func (r *ReconcileCommonController) getRoleStrings(ctx context.Context, db mdbv1.DbCommonSpec, enableClusterMongoDBRoles bool, mongodbResourceNsName types.NamespacedName) ([]string, error) {
	roles, err := r.getRoles(ctx, db, enableClusterMongoDBRoles, mongodbResourceNsName)
	if err != nil {
		return []string{}, err
	}

	roleStrings := make([]string, len(roles))
	for i, r := range roles {
		roleStrings[i] = fmt.Sprintf("%s@%s", r.Role, r.Db)
	}

	return roleStrings, nil
}

func (r *ReconcileCommonController) getRoles(ctx context.Context, db mdbv1.DbCommonSpec, enableClusterMongoDBRoles bool, mongodbResourceNsName types.NamespacedName) ([]mdbv1.MongoDBRole, error) {
	localRoles := db.GetSecurity().Roles
	roleRefs := db.GetSecurity().RoleRefs

	if len(localRoles) > 0 && len(roleRefs) > 0 {
		return nil, xerrors.Errorf("At most one of roles or roleRefs can be non-empty.")
	}

	var roles []mdbv1.MongoDBRole
	if len(roleRefs) > 0 {
		if !enableClusterMongoDBRoles {
			return nil, xerrors.Errorf("RoleRefs are not supported when ClusterMongoDBRoles are disabled. Please enable ClusterMongoDBRoles in the operator configuration. This can be done by setting the operator.enableClusterMongoDBRoles to true in the helm values file, which will automatically installed the necessary RBAC. Alternatively, it can be enabled by adding -watch-resource=clustermongodbroles flag to the operator deployment, and manually creating the necessary RBAC.")
		}
		var err error
		roles, err = r.getRoleRefs(ctx, roleRefs, mongodbResourceNsName, db.Version)
		if err != nil {
			return nil, err
		}
	} else {
		roles = localRoles
	}

	return roles, nil
}

// ensureRoles will first check if both roles and roleRefs are populated. If both are, it will return an error, which is inline with the webhook validation rules.
// Otherwise, if roles is populated, then it will extract the list of roles and check if they are already set in Ops Manager. If they are not, it will update the roles in Ops Manager.
// If roleRefs is populated, it will extract the list of roles from the referenced resources and check if they are already set in Ops Manager. If they are not, it will update the roles in Ops Manager.
func (r *ReconcileCommonController) ensureRoles(ctx context.Context, db mdbv1.DbCommonSpec, enableClusterMongoDBRoles bool, conn om.Connection, mongodbResourceNsName types.NamespacedName, previousRoles []string, log *zap.SugaredLogger) workflow.Status {
	roles, err := r.getRoles(ctx, db, enableClusterMongoDBRoles, mongodbResourceNsName)
	if err != nil {
		return workflow.Failed(err)
	}

	d, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err)
	}
	dRoles := d.GetRoles()
	mergedRoles := mergeRoles(dRoles, roles, previousRoles)

	if reflect.DeepEqual(dRoles, mergedRoles) {
		return workflow.OK()
	}

	// clone roles list to avoid mutating the spec in normalizePrivilegeResource
	newRoles := make([]mdbv1.MongoDBRole, len(mergedRoles))
	for i := range mergedRoles {
		newRoles[i] = *mergedRoles[i].DeepCopy()
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

// mergeRoles merges the deployed roles with the current roles and previously configured roles.
// It ensures that roles configured outside the operator are not removed.
// This is achieved by removing currently configured roles from the deployed roles.
// To ensure that roles removed from the spec are also removed from OM, we also remove the previously configured roles.
// Finally, we add back the currently configured roles.
func mergeRoles(deployed []mdbv1.MongoDBRole, current []mdbv1.MongoDBRole, previous []string) []mdbv1.MongoDBRole {
	roleMap := make(map[string]struct{})
	for _, r := range current {
		roleMap[r.Role+"@"+r.Db] = struct{}{}
	}

	for _, r := range previous {
		roleMap[r] = struct{}{}
	}

	mergedRoles := make([]mdbv1.MongoDBRole, 0)
	for _, r := range deployed {
		key := r.Role + "@" + r.Db
		if _, ok := roleMap[key]; !ok {
			mergedRoles = append(mergedRoles, r)
		}
	}

	mergedRoles = append(mergedRoles, current...)
	return mergedRoles
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
		// TLSConfig may be nil if TLS is enabled via CertificatesSecretsPrefix only
		var ca string
		if security.TLSConfig != nil {
			ca = security.TLSConfig.CA
		}
		r.resourceWatcher.RegisterWatchedTLSResources(objectToReconcile, ca, secretNames)
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
// If there are any externalMembers set, we will ignore the excess processes which appear in this list, as those are expected to be there and should not block the reconciliation.
func checkIfHasExcessProcesses(conn om.Connection, resourceName string, externalMembers []string, log *zap.SugaredLogger) workflow.Status {
	deployment, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err)
	}
	excessProcesses := deployment.GetNumberOfExcessProcesses(resourceName, externalMembers)
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

// checkExternalMembersDrift checks if the external members specified in the CR are present in Ops Manager and if their fields match the ones specified in the CR.
// If there is a drift, it returns an error with the details of the drift.
func checkExternalMembersDrift(conn om.Connection, externalMembers []mdbv1.ExternalMember) workflow.Status {
	if len(externalMembers) == 0 {
		return workflow.OK()
	}

	deployment, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err)
	}

	processNames := deployment.GetAllProcessNames()
	processNamesSet := merge.StringsToSet(processNames)

	for _, member := range externalMembers {
		// Check missing external members in AC
		if _, ok := processNamesSet[member.ProcessName]; !ok {
			return workflow.Failed(xerrors.Errorf("External member with process name %s is not present in Ops Manager", member.ProcessName))
		}
		// Check the other fields from the external member match the AC process
		if !deployment.CheckProcessFields(member.ProcessName, member.Hostname, member.Type, member.ReplicaSetName) {
			return workflow.Failed(xerrors.Errorf("External member with process name %s has different AC fields than the ones specified in the CR", member.ProcessName))
		}
	}
	return workflow.OK()
}

// MaxVotingMembers is MongoDB's hard limit on voting members per replica set.
const MaxVotingMembers = 7

// validateACForMigration checks that the pre-existing Automation Config is in a valid state for
// migration. It runs only when external members are declared on the spec. Today it verifies:
//   - net.tls.mode is set on all processes (operator-managed TLS requires this).
//   - The combined number of voting members (K8s side from spec + external side from AC) does not
//     exceed MongoDB's 7-voting-members limit. When external members are involved the operator no
//     longer fully owns the AC, so we surface this misconfiguration as a reconcile failure with a
//     detailed listing of every voting member and which ones should be made non-voting.
//
// The checks run on a snapshot read here while the merge reads the AC again later. An out of band
// edit between the two reads is accepted risk, the merge layer deliberately has no duplicate guard.
func validateACForMigration(conn om.Connection, mdb *mdbv1.MongoDB) workflow.Status {
	if len(mdb.Spec.GetExternalMembers()) == 0 {
		return workflow.OK()
	}
	deployment, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err)
	}

	// Check net.tls.mode is not null. Applies to all resource types.
	if processes := deployment.GetProcesses(); len(processes) > 0 {
		// Checking first process is enough since OM does not accept different values for net.tls.mode between processes
		tls := processes[0].TLSConfig()
		if _, ok := tls["mode"]; !ok {
			return workflow.Failed(xerrors.Errorf("The deployment has processes with net.tls.mode unset. Please ensure all processes have TLS mode configured before migration. If TLS is not enabled, set net.tls.mode to disabled. If TLS is enabled, set net.tls.mode to one of the supported TLS modes."))
		}
	}

	// Check voting-members limit per resource type. Only enforced when external members are
	// declared (already gated above). For pure-K8s deployments, Deployment.limitVotingMembers
	// handles the limit by auto-zeroing votes on excess members during merge.
	switch mdb.Spec.GetResourceType() {
	case mdbv1.ReplicaSet:
		rs, status := validateRSACIdentity(mdb, deployment)
		if !status.IsOK() {
			return status
		}
		return validateVotingLimitRS(mdb, rs)
	case mdbv1.ShardedCluster:
		if status := validateShardedACIdentity(mdb, deployment); !status.IsOK() {
			return status
		}
		return validateVotingLimitSharded(mdb, deployment)
	}
	return workflow.OK()
}

// validateRSACIdentity verifies the AC contains a replica set under the resolved name and returns it,
// so that a mistyped replicaSetNameOverride fails instead of the merge creating a parallel replica set.
func validateRSACIdentity(mdb *mdbv1.MongoDB, deployment om.Deployment) (om.ReplicaSet, workflow.Status) {
	rsName := mdb.GetReplicaSetName()
	rs := deployment.GetReplicaSetByName(rsName)
	if rs == nil {
		return nil, workflow.Failed(xerrors.Errorf("The Automation Config does not contain a replica set named %s. Recreate the resource with spec.replicaSetNameOverride set to the name of the existing replica set", rsName))
	}
	return rs, workflow.OK()
}

// validateShardedACIdentity verifies the AC names the resource resolves to match the existing sharded
// cluster, so that a missing or mistyped override fails before the merge can corrupt the AC.
func validateShardedACIdentity(mdb *mdbv1.MongoDB, deployment om.Deployment) workflow.Status {
	clusterName := mdb.GetShardedClusterName()
	acCluster, found := deployment.GetShardedClusterByName(clusterName)
	if !found {
		return workflow.Failed(xerrors.Errorf("The Automation Config does not contain a sharded cluster named %s. Recreate the resource with spec.shardedClusterNameOverride set to the name of the existing sharded cluster", clusterName))
	}
	if acConfigRsName := acCluster.ConfigServerRsName(); acConfigRsName != mdb.ConfigACRsName() {
		return workflow.Failed(xerrors.Errorf("The sharded cluster %s in the Automation Config has config server replica set %s but the resource resolves to %s. Recreate the resource with spec.configServerNameOverride set to the name of the existing config server replica set", clusterName, acConfigRsName, mdb.ConfigACRsName()))
	}

	acShardIdByRs := acCluster.ShardRsToIdMap()

	// Every override entry with a replicaSetName must reference an existing shard with the matching _id.
	for _, o := range mdb.Spec.ShardNameOverrides {
		if o.ReplicaSetName == "" {
			continue
		}
		acShardId, ok := acShardIdByRs[o.ReplicaSetName]
		if !ok {
			return workflow.Failed(xerrors.Errorf("The sharded cluster %s in the Automation Config has no shard with replica set name %s referenced by spec.shardNameOverrides", clusterName, o.ReplicaSetName))
		}
		if acShardId != o.ShardId {
			return workflow.Failed(xerrors.Errorf("The shard with replica set name %s has _id %s in the Automation Config but spec.shardNameOverrides specifies shardId %s", o.ReplicaSetName, acShardId, o.ShardId))
		}
	}

	// Every shard of the AC cluster must be covered by the resource, with a matching _id.
	resolvedShardIdByRs := make(map[string]string, mdb.Spec.ShardCount)
	for i := 0; i < mdb.Spec.ShardCount; i++ {
		resolvedShardIdByRs[mdb.ShardACRsName(i)] = mdb.ShardACShardId(i)
	}
	for rsName, acShardId := range acShardIdByRs {
		resolvedId, ok := resolvedShardIdByRs[rsName]
		if !ok {
			return workflow.Failed(xerrors.Errorf("The sharded cluster %s in the Automation Config has a shard with replica set name %s that the resource does not cover. Recreate the resource with a matching spec.shardCount and a spec.shardNameOverrides entry for every shard whose name differs from the Kubernetes default", clusterName, rsName))
		}
		if resolvedId != acShardId {
			return workflow.Failed(xerrors.Errorf("The shard with replica set name %s has _id %s in the Automation Config but the resource resolves to _id %s. Recreate the resource with a spec.shardNameOverrides entry specifying the matching shardId", rsName, acShardId, resolvedId))
		}
	}
	return workflow.OK()
}

// validateVotingLimit checks the MaxVotingMembers limit for a single replica set. externalSet
// identifies the external (non-K8s) members in rs. votingPositions are the desired K8s voting spec
// positions for this RS, and newlyVotingPositions are the subset this reconcile would turn voting
// (callers that cannot tell pass all voting positions).
func validateVotingLimit(rsName string, rs om.ReplicaSet, externalSet map[string]struct{}, votingPositions, newlyVotingPositions []int) workflow.Status {
	externalVoting := 0
	for _, m := range rs.Members() {
		if _, isExternal := externalSet[m.Name()]; isExternal && m.Votes() > 0 {
			externalVoting++
		}
	}
	total := externalVoting + len(votingPositions)
	if total <= MaxVotingMembers {
		return workflow.OK()
	}
	acVoting := collectACVotingMembers(rs, externalSet)
	excess := total - MaxVotingMembers
	return workflow.Failed(xerrors.Errorf("%s", formatTooManyVotingMembersError(
		rsName, total, acVoting, newlyVotingPositions, excess,
	)))
}

// votingPositionsFromConfig returns the spec positions [0..members) that are voting per memberConfig.
func votingPositionsFromConfig(members int, memberConfig []automationconfig.MemberOptions) []int {
	positions := make([]int, 0, members)
	for i := range members {
		opts := automationconfig.MemberOptions{}
		if i < len(memberConfig) {
			opts = memberConfig[i]
		}
		if opts.GetVotes() > 0 {
			positions = append(positions, i)
		}
	}
	return positions
}

// validateVotingLimitRS checks the 7 voting member limit for the replica set returned by validateRSACIdentity.
func validateVotingLimitRS(mdb *mdbv1.MongoDB, rs om.ReplicaSet) workflow.Status {
	externalSet := merge.StringsToSet(mdb.Spec.GetExternalMemberProcessNames())
	_, votingPositions, newlyVotingPositions := computePostReconcileVoting(mdb, rs, externalSet)
	return validateVotingLimit(mdb.GetReplicaSetName(), rs, externalSet, votingPositions, newlyVotingPositions)
}

// validateVotingLimitSharded checks the 7-voting-member limit for each RS component of the
// sharded cluster (config server + each shard RS) independently. Mongos processes are skipped
// since they are not replica set members.
func validateVotingLimitSharded(sc *mdbv1.MongoDB, deployment om.Deployment) workflow.Status {
	// Group external mongod process names by their AC replica set name.
	externalByRS := map[string][]string{}
	for _, m := range sc.Spec.GetExternalMembers() {
		if m.Type == "mongos" || m.ReplicaSetName == "" {
			continue
		}
		externalByRS[m.ReplicaSetName] = append(externalByRS[m.ReplicaSetName], m.ProcessName)
	}

	for rsName, processNames := range externalByRS {
		rs := deployment.GetReplicaSetByName(rsName)
		if rs == nil {
			continue
		}
		k8sMembers, memberConfig := shardedRSK8sConfig(sc, rsName)
		if k8sMembers == 0 {
			continue
		}
		externalSet := merge.StringsToSet(processNames)
		votingPositions := votingPositionsFromConfig(k8sMembers, memberConfig)
		// Treat all K8s voting positions as "newly voting" since K8s members may not yet exist in the AC.
		if status := validateVotingLimit(rsName, rs, externalSet, votingPositions, votingPositions); !status.IsOK() {
			return status
		}
	}
	return workflow.OK()
}

// shardedRSK8sConfig returns the K8s member count and per-member voting options for the RS
// component identified by rsName (the AC replicaSetName). Returns (0, nil) for unknown RS names.
// It mirrors the reconcile side resolution, pinned by TestShardedRSK8sConfigMatchesDesiredConfiguration.
func shardedRSK8sConfig(sc *mdbv1.MongoDB, rsName string) (members int, memberConfig []automationconfig.MemberOptions) {
	if rsName == sc.ConfigACRsName() {
		return sc.Spec.ConfigServerCount, sc.Spec.MemberConfig
	}
	for i := 0; i < sc.Spec.ShardCount; i++ {
		if sc.ShardACRsName(i) != rsName {
			continue
		}
		members = sc.Spec.MongodsPerShardCount
		memberConfig = sc.Spec.MemberConfig
		k8sName := sc.ShardName(i)
		for _, o := range sc.Spec.ShardOverrides {
			if !stringutil.Contains(o.ShardNames, k8sName) {
				continue
			}
			if o.Members != nil {
				members = *o.Members
			}
			if o.MemberConfig != nil {
				memberConfig = o.MemberConfig
			}
		}
		return members, memberConfig
	}
	return 0, nil
}

// votingMemberInfo names one voting member of a replica set for display purposes.
type votingMemberInfo struct {
	identifier string // AC host name
	kind       string // "Kubernetes" or "external"
}

// collectACVotingMembers returns the voting members CURRENTLY in the Automation Config, in AC
// order. By MongoDB's enforcement, len(returned) ≤ MaxVotingMembers.
func collectACVotingMembers(rs om.ReplicaSet, externalSet map[string]struct{}) []votingMemberInfo {
	out := make([]votingMemberInfo, 0)
	for _, m := range rs.Members() {
		if m.Votes() <= 0 {
			continue
		}
		kind := "Kubernetes"
		if _, isExternal := externalSet[m.Name()]; isExternal {
			kind = "external"
		}
		out = append(out, votingMemberInfo{identifier: m.Name(), kind: kind})
	}
	return out
}

// computePostReconcileVoting returns:
//   - externalVotingCount: external members currently voting in the AC (preserved during reconcile).
//   - k8sVotingPositions: all spec positions [0..Members) that would be voting after this reconcile.
//   - newlyVotingPositions: subset of k8sVotingPositions where the AC's corresponding K8s member
//     is non-voting or absent (scale-up). These are the positions THIS reconcile would make voting,
//     and therefore the actionable subset for the user to revert.
//
// "Corresponding K8s member" is found by position among AC members that are NOT in the external
// set, in AC order. Position N in spec maps to the N-th non-external member of the AC.
func computePostReconcileVoting(mdb *mdbv1.MongoDB, rs om.ReplicaSet, externalSet map[string]struct{}) (externalVotingCount int, k8sVotingPositions, newlyVotingPositions []int) {
	rsMemberVotingMap := map[string]bool{}
	for _, m := range rs.Members() {
		if _, isExternal := externalSet[m.Name()]; isExternal {
			if m.Votes() > 0 {
				externalVotingCount++
			}
			continue
		}
		rsMemberVotingMap[m.Name()] = m.Votes() > 0
	}

	for i := 0; i < mdb.Spec.Members; i++ {
		opts := automationconfig.MemberOptions{}
		if i < len(mdb.Spec.GetMemberOptions()) {
			opts = mdb.Spec.MemberConfig[i]
		}
		if opts.GetVotes() <= 0 {
			continue
		}
		// We can safely assume that k8s process names are using the new naming scheme since external members are set
		processName := process.PodNameToProcessName(dns.GetPodName(mdb.Name, i), mdb.Namespace)
		k8sVotingPositions = append(k8sVotingPositions, i)
		wasACVoting := rsMemberVotingMap[processName]
		if !wasACVoting {
			newlyVotingPositions = append(newlyVotingPositions, i)
		}
	}
	return externalVotingCount, k8sVotingPositions, newlyVotingPositions
}

// formatTooManyVotingMembersError builds the user-facing error in five lines:
//  1. Header: post-reconcile total + limit.
//  2. AC voters (≤ 7 lines): live state, what the user can see in OM right now.
//  3. Newly voting K8s positions: what this reconcile would make voting.
//  4. Fix instruction: revert `excess` of the memberConfig entries to votes=0.
//  5. Forward-looking suggestion: to make more K8s voting, drain externals first.
//
// By construction len(newlyVotingPositions) ≥ excess, because the AC is always within the limit
// and the only way to exceed it is via newly voting K8s positions.
func formatTooManyVotingMembersError(rsName string, total int, acVoting []votingMemberInfo, newlyVotingPositions []int, excess int) string {
	var acLines []string
	for i, v := range acVoting {
		acLines = append(acLines, fmt.Sprintf("  %d. %s (%s)", i+1, v.identifier, v.kind))
	}
	if len(acLines) == 0 {
		acLines = []string{"  (none)"}
	}

	var newlyLines []string
	for _, i := range newlyVotingPositions {
		newlyLines = append(newlyLines, fmt.Sprintf("  - spec.memberConfig[%d]", i))
	}
	if len(newlyLines) == 0 {
		// We should never get here
		newlyLines = []string{"  (none. The AC already exceeds the limit. Check Ops Manager for voting members the operator does not manage)"}
	}

	return fmt.Sprintf(
		"%q: this reconcile would result in %d voting members (max: %d).\n"+
			"Currently voting in the Automation Config (%d):\n%s\n"+
			"This reconcile would make the following Kubernetes member(s) voting:\n%s\n"+
			"To fix: revert %d of the above memberConfig entries to votes=0 and priority=\"0\".\n"+
			"If you wish to make more of the kubernetes members voting, make sure to remove one of the voting external members in the list above.",
		rsName, total, MaxVotingMembers,
		len(acVoting), strings.Join(acLines, "\n"),
		strings.Join(newlyLines, "\n"),
		excess,
	)
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
func (r *ReconcileCommonController) ensureBackupConfigurationAndUpdateStatus(ctx context.Context, conn om.Connection, mdb backup.ConfigReaderUpdater, secretsReader secrets.SecretClient, log *zap.SugaredLogger, backupEnableDelay time.Duration) workflow.Status {
	statusOpt, opts := backup.EnsureBackupConfigurationInOpsManager(ctx, mdb, secretsReader, conn.GroupID(), conn, conn, conn, log, backupEnableDelay)
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

// EffectiveAgentCertPEMPath returns the path used for the automation agent PEM in pods and in Ops Manager
// (autoPEMKeyFilePath). When security.authentication.agents.autoPEMKeyFilePath is set, that value is used;
// otherwise defaultPath (typically AgentCertMountPath/<cert hash>) is used.
func EffectiveAgentCertPEMPath(defaultPath string, sec *mdbv1.Security) string {
	if p := sec.GetAgentAutoPEMKeyFilePath(); p != "" {
		return p
	}
	return defaultPath
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

func getReplicaSetProcessIdsFromReplicaSets(replicaSetName string, deployment om.Deployment) map[string]int {
	processIds := map[string]int{}

	replicaSet := deployment.GetReplicaSetByName(replicaSetName)
	if replicaSet == nil {
		return map[string]int{}
	}

	for _, m := range replicaSet.Members() {
		processIds[m.Name()] = m.Id()
	}

	return processIds
}

func ReconcileReplicaSetAC(ctx context.Context, d om.Deployment, spec mdbv1.DbCommonSpec, lastMongodConfig map[string]interface{}, resourceName string, rs om.ReplicaSetWithProcesses, externalProcessNames []string, caFilePath string, internalClusterPath string, pc *PrometheusConfiguration, log *zap.SugaredLogger) error {
	// it is not possible to disable internal cluster authentication once enabled
	if d.ExistingProcessesHaveInternalClusterAuthentication(rs.Processes) && spec.Security.GetInternalClusterAuthenticationMode() == "" {
		return xerrors.Errorf("cannot disable x509 internal cluster authentication")
	}

	excessProcesses := d.GetNumberOfExcessProcesses(resourceName, externalProcessNames)
	if excessProcesses > 0 {
		return xerrors.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes/current/tutorial/migrate-to-single-resource )")
	}

	d.MergeReplicaSet(rs, spec.GetAdditionalMongodConfig().ToMap(), lastMongodConfig, externalProcessNames, log)
	d.ConfigureMonitoringAndBackup(log, spec.GetSecurity().IsTLSEnabled(), caFilePath)
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

// runConnectivityJob builds, launches (or polls) a connectivity-validator Kubernetes Job from a
// pre-built StatefulSet spec and returns the workflow.Status for the result.
// No StatefulSets or Ops Manager config are modified.
func (r *ReconcileCommonController) runConnectivityJob(
	ctx context.Context,
	mdb *mdbv1.MongoDB,
	sts *appsv1.StatefulSet,
	connectionString string,
	allHostnames []string,
	agentAuthMode string,
	agentCertHash string,
	operatorImage string,
	log *zap.SugaredLogger,
) workflow.Status {
	subjectDN := ""
	if sec := mdb.GetSecurity(); sec != nil && sec.GetAgentMechanism(agentAuthMode) == util.X509 {
		agentCertSecretName := sec.AgentClientCertificateSecretName(mdb.Name)
		sel := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: agentCertSecretName},
			Key:                  corev1.TLSCertKey,
		}
		userOpts, err := r.readAgentSubjectsFromSecret(ctx, mdb.Namespace, sel, log)
		if err != nil {
			return workflow.Failed(xerrors.Errorf("connectivity dry-run: automation agent certificate subject: %w", err)).
				WithAdditionalOptions(status.NewMigrationConditionOption(status.MigrationCondition(
					status.MigrationPhaseConnectivityCheckFailed, "AgentCertSubject", err.Error(),
				)))
		}
		subjectDN = userOpts.AutomationSubject
	}

	job := pkgMigration.BuildJobFromStatefulSet(mdb, sts, operatorImage, connectionString, allHostnames, agentAuthMode, agentCertHash, subjectDN)

	result := opMigration.RunConnectivityJob(ctx, r.client, job)
	if result.Err != nil {
		return workflow.Failed(fmt.Errorf("connectivity dry run: %w", result.Err)).
			WithAdditionalOptions(status.NewMigrationConditionOption(status.MigrationCondition(
				result.Phase, result.Reason, result.Message,
			)))
	}

	log.Infow("[DRY-RUN CONNECTIVITY] Job status", "phase", result.Phase, "reason", result.Reason, "message", result.Message)

	switch result.Phase {
	case status.MigrationPhaseConnectivityCheckRunning:
		return workflow.ConnectivityValidation("Connectivity validation in progress. Remove annotation %s to run full reconciliation", opMigration.AnnotationDryRun).
			WithRetry(30).
			WithAdditionalOptions(status.NewMigrationConditionOption(status.MigrationCondition(
				status.MigrationPhaseConnectivityCheckRunning, "Running", "Connectivity validation Job is in progress",
			)))
	case status.MigrationPhaseConnectivityCheckPassed:
		return workflow.ConnectivityValidation("Connectivity validation passed. Remove annotation %s to continue with migration", opMigration.AnnotationDryRun).
			WithAdditionalOptions(status.NewMigrationConditionOption(status.MigrationCondition(
				status.MigrationPhaseConnectivityCheckPassed, result.Reason, result.Message,
			)))
	default:
		return workflow.Failed(fmt.Errorf("%s: %s", result.Reason, result.Message)).
			WithRetry(300).
			WithAdditionalOptions(status.NewMigrationConditionOption(status.MigrationCondition(
				status.MigrationPhaseConnectivityCheckFailed, result.Reason, result.Message,
			)))
	}
}
