package mdb

import (
	"errors"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
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
	if authSpec.IsX509Enabled() && !d.GetSecurity().IsTLSEnabled() {
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

func oidcAuthValidators(db DbCommonSpec) []func(DbCommonSpec) v1.ValidationResult {
	validators := make([]func(DbCommonSpec) v1.ValidationResult, 0)
	if db.Security == nil || db.Security.Authentication == nil {
		return validators
	}

	authentication := db.Security.Authentication
	if !authentication.IsOIDCEnabled() {
		return validators
	}

	validators = append(validators, oidcAuthModeValidator(authentication))
	validators = append(validators, oidcAuthRequiresEnterprise)

	providerConfigs := authentication.OIDCProviderConfigs
	if len(providerConfigs) == 0 {
		return validators
	}

	validators = append(validators,
		oidcProviderConfigsUniqueNameValidation(providerConfigs),
		oidcProviderConfigsSingleWorkforceIdentityFederationValidation(providerConfigs),
		oidcProviderConfigUniqueIssuerURIValidation(providerConfigs),
	)

	for _, config := range providerConfigs {
		validators = append(validators,
			oidcProviderConfigIssuerURIValidator(config),
			oidcProviderConfigClientIdValidator(config),
			oidcProviderConfigRequestedScopesValidator(config),
			oidcProviderConfigAuthorizationTypeValidator(config),
		)
	}

	return validators
}

// oidcProviderConfigUniqueIssuerURIValidation is based on the documentation here:
// https://www.mongodb.com/docs/manual/reference/parameters/#oidcidentityproviders-fields
func oidcProviderConfigUniqueIssuerURIValidation(configs []OIDCProviderConfig) func(DbCommonSpec) v1.ValidationResult {
	return func(d DbCommonSpec) v1.ValidationResult {
		if len(configs) == 0 {
			return v1.ValidationSuccess()
		}

		// Check if version supports duplicate issuers (7.0, 7.3, or 8.0+)
		versionParts := strings.Split(strings.TrimSuffix(d.Version, "-ent"), ".")
		supportsMultipleIssuers := false
		if len(versionParts) >= 2 {
			major := versionParts[0]
			minor := versionParts[1]
			if major == "8" || (major == "7" && (minor == "0" || minor == "3")) {
				supportsMultipleIssuers = true
			}
		}

		if supportsMultipleIssuers {
			// Track issuer+audience combinations
			issuerAudienceCombos := make(map[string]string)
			for _, config := range configs {
				comboKey := config.IssuerURI + ":" + config.Audience
				if previousConfig, exists := issuerAudienceCombos[comboKey]; exists {
					return v1.ValidationWarning("OIDC provider configs %q and %q have duplicate IssuerURI and Audience combination",
						previousConfig, config.ConfigurationName)
				}
				issuerAudienceCombos[comboKey] = config.ConfigurationName
			}
		} else {
			// For older versions, require unique issuers
			uris := make(map[string]string)
			for _, config := range configs {
				if previousConfig, exists := uris[config.IssuerURI]; exists {
					return v1.ValidationError("OIDC provider configs %q and %q have duplicate IssuerURI: %s",
						previousConfig, config.ConfigurationName, config.IssuerURI)
				}
				uris[config.IssuerURI] = config.ConfigurationName
			}
		}

		return v1.ValidationSuccess()
	}
}

func oidcAuthModeValidator(authentication *Authentication) func(DbCommonSpec) v1.ValidationResult {
	return func(spec DbCommonSpec) v1.ValidationResult {
		// OIDC cannot be used for agent authentication so other auth mode has to enabled as well
		if len(authentication.Modes) == 1 {
			return v1.ValidationError("OIDC authentication cannot be used as the only authentication mechanism")
		}

		oidcProviderConfigs := authentication.OIDCProviderConfigs
		if len(oidcProviderConfigs) == 0 {
			return v1.ValidationError("At least one OIDC provider config needs to be specified when OIDC authentication is enabled")
		}

		return v1.ValidationSuccess()
	}
}

func oidcProviderConfigsUniqueNameValidation(configs []OIDCProviderConfig) func(DbCommonSpec) v1.ValidationResult {
	return func(spec DbCommonSpec) v1.ValidationResult {
		configNames := make(map[string]bool)
		for _, config := range configs {
			if _, ok := configNames[config.ConfigurationName]; ok {
				return v1.ValidationError("OIDC provider config name %s is not unique", config.ConfigurationName)
			}

			configNames[config.ConfigurationName] = true
		}

		return v1.ValidationSuccess()
	}
}

func oidcProviderConfigsSingleWorkforceIdentityFederationValidation(configs []OIDCProviderConfig) func(DbCommonSpec) v1.ValidationResult {
	return func(spec DbCommonSpec) v1.ValidationResult {
		workforceIdentityFederationConfigs := make([]string, 0)
		for _, config := range configs {
			if config.AuthorizationMethod == OIDCAuthorizationMethodWorkforceIdentityFederation {
				workforceIdentityFederationConfigs = append(workforceIdentityFederationConfigs, config.ConfigurationName)
			}
		}

		if len(workforceIdentityFederationConfigs) > 1 {
			configsSeparatedString := strings.Join(workforceIdentityFederationConfigs, ", ")
			return v1.ValidationError("Only one OIDC provider config can be configured with Workforce Identity Federation. "+
				"The following configs are configured with Workforce Identity Federation: %s", configsSeparatedString)
		}

		return v1.ValidationSuccess()
	}
}

func oidcProviderConfigIssuerURIValidator(config OIDCProviderConfig) func(DbCommonSpec) v1.ValidationResult {
	return func(_ DbCommonSpec) v1.ValidationResult {
		url, err := util.ParseURL(config.IssuerURI)
		if err != nil {
			return v1.ValidationError("Invalid IssuerURI in OIDC provider config %q: %s", config.ConfigurationName, err.Error())
		}

		if url.Scheme != "https" {
			return v1.ValidationWarning("IssuerURI %s in OIDC provider config %q in not secure endpoint", url.String(), config.ConfigurationName)
		}

		return v1.ValidationSuccess()
	}
}

func oidcProviderConfigClientIdValidator(config OIDCProviderConfig) func(DbCommonSpec) v1.ValidationResult {
	return func(_ DbCommonSpec) v1.ValidationResult {
		if config.AuthorizationMethod == OIDCAuthorizationMethodWorkforceIdentityFederation {
			if config.ClientId == nil || *config.ClientId == "" {
				return v1.ValidationError("ClientId has to be specified in OIDC provider config %q with Workforce Identity Federation", config.ConfigurationName)
			}
		} else if config.AuthorizationMethod == OIDCAuthorizationMethodWorkloadIdentityFederation {
			if config.ClientId != nil {
				return v1.ValidationWarning("ClientId will be ignored in OIDC provider config %q with Workload Identity Federation", config.ConfigurationName)
			}
		}

		return v1.ValidationSuccess()
	}
}

func oidcProviderConfigRequestedScopesValidator(config OIDCProviderConfig) func(DbCommonSpec) v1.ValidationResult {
	return func(_ DbCommonSpec) v1.ValidationResult {
		if config.AuthorizationMethod == OIDCAuthorizationMethodWorkloadIdentityFederation {
			if len(config.RequestedScopes) > 0 {
				return v1.ValidationWarning("RequestedScopes will be ignored in OIDC provider config %q with Workload Identity Federation", config.ConfigurationName)
			}
		}

		return v1.ValidationSuccess()
	}
}

func oidcProviderConfigAuthorizationTypeValidator(config OIDCProviderConfig) func(DbCommonSpec) v1.ValidationResult {
	return func(_ DbCommonSpec) v1.ValidationResult {
		if config.AuthorizationType == OIDCAuthorizationTypeGroupMembership {
			if config.GroupsClaim == nil || *config.GroupsClaim == "" {
				return v1.ValidationError("GroupsClaim has to be specified in OIDC provider config %q when using Group Membership authorization", config.ConfigurationName)
			}
		} else if config.AuthorizationType == OIDCAuthorizationTypeUserID {
			if config.GroupsClaim != nil {
				return v1.ValidationWarning("GroupsClaim will be ignored in OIDC provider config %q when using User ID authorization", config.ConfigurationName)
			}
		}

		return v1.ValidationSuccess()
	}
}

func oidcAuthRequiresEnterprise(d DbCommonSpec) v1.ValidationResult {
	authSpec := d.Security.Authentication
	if authSpec != nil && authSpec.IsOIDCEnabled() && !strings.HasSuffix(d.Version, "-ent") {
		return v1.ValidationError("Cannot enable OIDC authentication with MongoDB Community Builds")
	}
	return v1.ValidationSuccess()
}

func ldapAuthRequiresEnterprise(d DbCommonSpec) v1.ValidationResult {
	authSpec := d.Security.Authentication
	if authSpec != nil && authSpec.IsLDAPEnabled() && !strings.HasSuffix(d.Version, "-ent") {
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

func CommonValidators(db DbCommonSpec) []func(d DbCommonSpec) v1.ValidationResult {
	validators := []func(d DbCommonSpec) v1.ValidationResult{
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

	validators = append(validators, oidcAuthValidators(db)...)

	return validators
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

	for _, validator := range CommonValidators(m.Spec.DbCommonSpec) {
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
	kubeConfigFile, err := multicluster.NewKubeConfigFile(multicluster.GetKubeConfigPath())
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
		return v1.ValidationWarning("The following clusters specified in ClusterSpecList is not present in Kubeconfig: %s, instead - the following are: %+v", notPresentClusters, clusterNames)
	}
	return v1.ValidationSuccess()
}
