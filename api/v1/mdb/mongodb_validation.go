package mdb

import (
	"errors"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

var _ webhook.Validator = &MongoDB{}

// ValidateCreate and ValidateUpdate should be the same if we intend to do this
// on every reconciliation as well
func (m *MongoDB) ValidateCreate() (admission.Warnings, error) {
	return nil, m.ProcessValidationsOnReconcile(nil)
}

func (m *MongoDB) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	return nil, m.ProcessValidationsOnReconcile(old.(*MongoDB))
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (m *MongoDB) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func replicaSetHorizonsRequireTLS(d DbCommonSpec) v1.ValidationResult {
	if len(d.Connectivity.ReplicaSetHorizons) > 0 && !d.IsSecurityTLSConfigEnabled() {
		return v1.ValidationError("TLS must be enabled in order to use replica set horizons")
	}
	return v1.ValidationSuccess()
}

func horizonsMustEqualMembers(ms MongoDbSpec) v1.ValidationResult {
	numHorizonMembers := len(ms.Connectivity.ReplicaSetHorizons)
	if numHorizonMembers > 0 && numHorizonMembers != ms.Members {
		return v1.ValidationError("Number of horizons must be equal to number of members in replica set")
	}
	return v1.ValidationSuccess()
}

func deploymentsMustHaveTLSInX509Env(d DbCommonSpec) v1.ValidationResult {
	authSpec := d.Security.Authentication
	if authSpec == nil {
		return v1.ValidationSuccess()
	}
	if authSpec.Enabled && authSpec.IsX509Enabled() && !d.GetSecurity().IsTLSEnabled() {
		return v1.ValidationError("Cannot have a non-tls deployment when x509 authentication is enabled")
	}
	return v1.ValidationSuccess()
}

func deploymentsMustHaveAtLeastOneAuthModeIfAuthIsEnabled(d DbCommonSpec) v1.ValidationResult {
	authSpec := d.Security.Authentication
	if authSpec == nil {
		return v1.ValidationSuccess()
	}
	if authSpec.Enabled && len(authSpec.Modes) == 0 {
		return v1.ValidationError("Cannot enable authentication without modes specified")
	}
	return v1.ValidationSuccess()
}

func deploymentsMustHaveAgentModeInAuthModes(d DbCommonSpec) v1.ValidationResult {
	authSpec := d.Security.Authentication
	if authSpec == nil {
		return v1.ValidationSuccess()
	}
	if !authSpec.Enabled {
		return v1.ValidationSuccess()
	}

	if authSpec.Agents.Mode != "" && !IsAuthPresent(authSpec.Modes, authSpec.Agents.Mode) {
		return v1.ValidationError("Cannot configure an Agent authentication mechanism that is not specified in authentication modes")
	}
	return v1.ValidationSuccess()
}

// scramSha1AuthValidation performs the same validation as the Ops Manager does in
// https://github.com/10gen/mms/blob/107304ce6988f6280e8af069d19b7c6226c4f3ce/server/src/main/com/xgen/cloud/atm/publish/_public/svc/AutomationValidationSvc.java
func scramSha1AuthValidation(d DbCommonSpec) v1.ValidationResult {
	authSpec := d.Security.Authentication
	if authSpec == nil {
		return v1.ValidationSuccess()
	}
	if !authSpec.Enabled {
		return v1.ValidationSuccess()
	}

	if IsAuthPresent(authSpec.Modes, util.SCRAMSHA1) {
		if authSpec.Agents.Mode != util.MONGODBCR {
			return v1.ValidationError("Cannot configure SCRAM-SHA-1 without using MONGODB-CR in te Agent Mode")
		}
	}
	return v1.ValidationSuccess()
}

func ldapAuthRequiresEnterprise(d DbCommonSpec) v1.ValidationResult {
	authSpec := d.Security.Authentication
	if authSpec != nil && authSpec.isLDAPEnabled() && !strings.HasSuffix(d.Version, "-ent") {
		return v1.ValidationError("Cannot enable LDAP authentication with MongoDB Community Builds")
	}
	return v1.ValidationSuccess()
}

func additionalMongodConfig(ms MongoDbSpec) v1.ValidationResult {
	if ms.ResourceType == ShardedCluster {
		if ms.AdditionalMongodConfig != nil && ms.AdditionalMongodConfig.object != nil && len(ms.AdditionalMongodConfig.object) > 0 {
			return v1.ValidationError("'spec.additionalMongodConfig' cannot be specified if type of MongoDB is %s", ShardedCluster)
		}
		return v1.ValidationSuccess()
	}
	// Standalone or ReplicaSet
	if ms.ShardSpec != nil || ms.ConfigSrvSpec != nil || ms.MongosSpec != nil {
		return v1.ValidationError("'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is %s", ms.ResourceType)
	}
	return v1.ValidationSuccess()
}

func replicasetMemberIsSpecified(ms MongoDbSpec) v1.ValidationResult {
	if ms.ResourceType == ReplicaSet && ms.Members == 0 {
		return v1.ValidationError("'spec.members' must be specified if type of MongoDB is %s", ms.ResourceType)
	}
	return v1.ValidationSuccess()
}

func agentModeIsSetIfMoreThanADeploymentAuthModeIsSet(d DbCommonSpec) v1.ValidationResult {
	if d.Security == nil || d.Security.Authentication == nil {
		return v1.ValidationSuccess()
	}
	if len(d.Security.Authentication.Modes) > 1 && d.Security.Authentication.Agents.Mode == "" {
		return v1.ValidationError("spec.security.authentication.agents.mode must be specified if more than one entry is present in spec.security.authentication.modes")
	}
	return v1.ValidationSuccess()
}

func ldapGroupDnIsSetIfLdapAuthzIsEnabledAndAgentsAreExternal(d DbCommonSpec) v1.ValidationResult {
	if d.Security == nil || d.Security.Authentication == nil || d.Security.Authentication.Ldap == nil {
		return v1.ValidationSuccess()
	}
	auth := d.Security.Authentication
	if auth.Ldap.AuthzQueryTemplate != "" && auth.Agents.AutomationLdapGroupDN == "" && stringutil.Contains([]string{"X509", "LDAP"}, auth.Agents.Mode) {
		return v1.ValidationError("automationLdapGroupDN must be specified if LDAP authorization is used and agent auth mode is $external (x509 or LDAP)")
	}
	return v1.ValidationSuccess()
}

func resourceTypeImmutable(newObj, oldObj MongoDbSpec) v1.ValidationResult {
	if newObj.ResourceType != oldObj.ResourceType {
		return v1.ValidationError("'resourceType' cannot be changed once created")
	}
	return v1.ValidationSuccess()
}

// This validation blocks topology migrations for any MongoDB resource (Standalone, ReplicaSet, ShardedCluster)
func noTopologyMigration(newObj, oldObj MongoDbSpec) v1.ValidationResult {
	if oldObj.GetTopology() != newObj.GetTopology() {
		return v1.ValidationError("Automatic Topology Migration (Single/Multi Cluster) is not supported for MongoDB resource")
	}
	return v1.ValidationSuccess()
}

// specWithExactlyOneSchema checks that exactly one among "Project/OpsManagerConfig/CloudManagerConfig"
// is configured, doing the "oneOf" validation in the webhook.
func specWithExactlyOneSchema(d DbCommonSpec) v1.ValidationResult {
	count := 0
	if *d.OpsManagerConfig != (PrivateCloudConfig{}) {
		count += 1
	}
	if *d.CloudManagerConfig != (PrivateCloudConfig{}) {
		count += 1
	}

	if count != 1 {
		return v1.ValidationError("must validate one and only one schema")
	}
	return v1.ValidationSuccess()
}

func CommonValidators() []func(d DbCommonSpec) v1.ValidationResult {
	return []func(d DbCommonSpec) v1.ValidationResult{
		replicaSetHorizonsRequireTLS,
		deploymentsMustHaveTLSInX509Env,
		deploymentsMustHaveAtLeastOneAuthModeIfAuthIsEnabled,
		deploymentsMustHaveAgentModeInAuthModes,
		scramSha1AuthValidation,
		ldapAuthRequiresEnterprise,
		rolesAttributeisCorrectlyConfigured,
		agentModeIsSetIfMoreThanADeploymentAuthModeIsSet,
		ldapGroupDnIsSetIfLdapAuthzIsEnabledAndAgentsAreExternal,
		specWithExactlyOneSchema,
		featureCompatibilityVersionValidation,
	}
}

func featureCompatibilityVersionValidation(d DbCommonSpec) v1.ValidationResult {
	fcv := d.FeatureCompatibilityVersion
	return ValidateFCV(fcv)
}

func ValidateFCV(fcv *string) v1.ValidationResult {
	if fcv != nil {
		f := *fcv
		if f == util.AlwaysMatchVersionFCV {
			return v1.ValidationSuccess()
		}
		splitted := strings.Split(f, ".")
		if len(splitted) != 2 {
			return v1.ValidationError("invalid feature compatibility version: %s, possible values are: '%s' or 'major.minor'", f, util.AlwaysMatchVersionFCV)
		}
	}
	return v1.ValidationResult{}
}

func (m *MongoDB) RunValidations(old *MongoDB) []v1.ValidationResult {
	// The below validators apply to all MongoDB resource (but not MongoDBMulti), regardless of the value of the
	// Topology field
	mongoDBValidators := []func(m MongoDbSpec) v1.ValidationResult{
		horizonsMustEqualMembers,
		additionalMongodConfig,
		replicasetMemberIsSpecified,
	}

	updateValidators := []func(newObj MongoDbSpec, oldObj MongoDbSpec) v1.ValidationResult{
		resourceTypeImmutable,
		noTopologyMigration,
	}

	var validationResults []v1.ValidationResult

	for _, validator := range mongoDBValidators {
		res := validator(m.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	for _, validator := range CommonValidators() {
		res := validator(m.Spec.DbCommonSpec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	if m.GetResourceType() == ShardedCluster {
		for _, validator := range ShardedClusterCommonValidators() {
			res := validator(*m)
			if res.Level > 0 {
				validationResults = append(validationResults, res)
			}
		}

		if m.Spec.IsMultiCluster() {
			for _, validator := range ShardedClusterMultiValidators() {
				results := validator(*m)
				for _, res := range results {
					if res.Level > 0 {
						validationResults = append(validationResults, res)
					}
				}
			}
		} else {
			for _, validator := range ShardedClusterSingleValidators() {
				res := validator(*m)
				if res.Level > 0 {
					validationResults = append(validationResults, res)
				}
			}
		}
	}

	if old == nil {
		return validationResults
	}
	for _, validator := range updateValidators {
		res := validator(m.Spec, old.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	return validationResults
}

func (m *MongoDB) ProcessValidationsOnReconcile(old *MongoDB) error {
	for _, res := range m.RunValidations(old) {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}

		if res.Level == v1.WarningLevel {
			m.AddWarningIfNotExists(status.Warning(res.Msg))
		}
	}

	return nil
}

func ValidateUniqueClusterNames(ms ClusterSpecList) v1.ValidationResult {
	present := make(map[string]struct{})

	for _, e := range ms {
		if _, ok := present[e.ClusterName]; ok {
			return v1.ValidationError("Multiple clusters with the same name (%s) are not allowed", e.ClusterName)
		}
		present[e.ClusterName] = struct{}{}
	}
	return v1.ValidationSuccess()
}

func ValidateNonEmptyClusterSpecList(ms ClusterSpecList) v1.ValidationResult {
	if len(ms) == 0 {
		return v1.ValidationError("ClusterSpecList empty is not allowed, please define at least one cluster")
	}
	return v1.ValidationSuccess()
}

func ValidateMemberClusterIsSubsetOfKubeConfig(ms ClusterSpecList) v1.ValidationResult {
	// read the mounted kubeconfig file and
	kubeConfigFile, err := multicluster.NewKubeConfigFile()
	if err != nil {
		// log the error here?
		return v1.ValidationSuccess()
	}

	kubeConfig, err := kubeConfigFile.LoadKubeConfigFile()
	if err != nil {
		// log the error here?
		return v1.ValidationSuccess()
	}

	clusterNames := kubeConfig.GetMemberClusterNames()
	notPresentClusters := make([]string, 0)

	for _, e := range ms {
		if !slices.Contains(clusterNames, e.ClusterName) {
			notPresentClusters = append(notPresentClusters, e.ClusterName)
		}
	}
	if len(notPresentClusters) > 0 {
		return v1.ValidationError("The following clusters specified in ClusterSpecList is not present in Kubeconfig: %s, instead - the following are: %+v", notPresentClusters, clusterNames)
	}
	return v1.ValidationSuccess()
}
