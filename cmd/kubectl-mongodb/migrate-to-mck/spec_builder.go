package migratetomck

import (
	"fmt"
	"strconv"
	"strings"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	authn "github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	pkgtls "github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// buildSecurity assembles spec.security. certsSecretPrefix non-empty means TLS is enabled
// (set by ensureTLS, mirrors the operator's IsSecurityTLSConfigEnabled).
func buildSecurity(
	auth *om.Auth,
	processMap map[string]om.Process,
	members []om.ReplicaSetMember,
	acLdap *ldap.Ldap,
	oidcConfigs []oidc.ProviderConfig,
	certsSecretPrefix string,
) (*mdbv1.Security, error) {
	security := &mdbv1.Security{}
	hasSettings := false

	if certsSecretPrefix != "" {
		// certsSecretPrefix enables TLS (tls.enabled is deprecated, see RELEASE_NOTES_MEKO.md 1.15).
		security.CertificatesSecretsPrefix = certsSecretPrefix
		hasSettings = true
	}

	if auth != nil && auth.IsEnabled() {
		authConfig, err := buildAuthenticationConfig(auth, processMap, members, acLdap, oidcConfigs)
		if err != nil {
			return nil, err
		}
		if authConfig != nil {
			security.Authentication = authConfig
			hasSettings = true
		}
	}

	if !hasSettings {
		return nil, nil
	}
	return security, nil
}

func buildAuthenticationConfig(
	auth *om.Auth,
	processMap map[string]om.Process,
	members []om.ReplicaSetMember,
	acLdap *ldap.Ldap,
	oidcConfigs []oidc.ProviderConfig,
) (*mdbv1.Authentication, error) {
	modes, err := buildAuthModes(auth)
	if err != nil {
		return nil, err
	}
	if len(modes) == 0 {
		return nil, nil
	}

	authConfig := &mdbv1.Authentication{
		Enabled:            true,
		Modes:              modes,
		IgnoreUnknownUsers: !auth.AuthoritativeSet,
	}

	internalCluster, err := extractInternalClusterAuthMode(processMap, members)
	if err != nil {
		return nil, err
	}
	if internalCluster != "" {
		authConfig.InternalCluster = internalCluster
	}

	if acLdap != nil && (acLdap.Servers != "" || acLdap.BindQueryUser != "") {
		authConfig.Ldap = mdbv1.ConvertACLdapToCR(acLdap, LdapBindQuerySecretName, LdapCAConfigMapName, LdapCAKey)
	}

	if len(oidcConfigs) > 0 {
		if crOIDC := authn.MapACOIDCToProviderConfigs(oidcConfigs); len(crOIDC) > 0 {
			authConfig.OIDCProviderConfigs = crOIDC
		}
	}

	if agentMode, ok := authn.MapMechanismToAuthMode(auth.AutoAuthMechanism); ok {
		authConfig.Agents.Mode = agentMode
	}

	if auth.AutoUser != "" && auth.AutoUser != util.AutomationAgentUserName {
		authConfig.Agents.AutomationUserName = auth.AutoUser
	}

	return authConfig, nil
}

// buildAuthModes deduplicates deploymentAuthMechanisms and autoAuthMechanisms into CR auth modes.
func buildAuthModes(auth *om.Auth) ([]mdbv1.AuthMode, error) {
	seen := map[mdbv1.AuthMode]bool{}
	var modes []mdbv1.AuthMode

	collect := func(mechs []string, source string) error {
		for _, mech := range mechs {
			mode, ok := authn.MapMechanismToAuthMode(mech)
			if !ok {
				return fmt.Errorf("unsupported authentication mechanism %q in automation config (%s)", mech, source)
			}
			authMode := mdbv1.AuthMode(mode)
			if !seen[authMode] {
				modes = append(modes, authMode)
				seen[authMode] = true
			}
		}
		return nil
	}

	if err := collect(auth.DeploymentAuthMechanisms, "deploymentAuthMechanisms"); err != nil {
		return nil, err
	}
	if err := collect(auth.AutoAuthMechanisms, "autoAuthMechanisms"); err != nil {
		return nil, err
	}

	return modes, nil
}

// extractInternalClusterAuthMode maps the first member's clusterAuthMode to the CR internalCluster field.
func extractInternalClusterAuthMode(processMap map[string]om.Process, members []om.ReplicaSetMember) (string, error) {
	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return "", fmt.Errorf("process %q referenced by member at index %d was not found", host, i)
		}
		if proc.HasInternalClusterAuthentication() {
			return mapClusterAuthMode(proc.ClusterAuthMode())
		}
	}
	return "", nil
}

func mapClusterAuthMode(mode string) (string, error) {
	switch mode {
	case "x509":
		return util.X509, nil
	case "keyFile":
		return "", fmt.Errorf("clusterAuthMode %q is not supported by the operator (only x509 is supported). Migrate the deployment to x509 internal cluster authentication before using the operator", mode)
	default:
		return "", fmt.Errorf("unsupported clusterAuthMode %q in automation config. Only x509 is supported by the operator", mode)
	}
}

// isTLSEnabled returns true if any member has an explicit TLS section with a non-disabled mode.
func isTLSEnabled(processMap map[string]om.Process, members []om.ReplicaSetMember) (bool, error) {
	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return false, fmt.Errorf("process %q referenced by member at index %d was not found", host, i)
		}
		// Require explicit section: GetTLSModeFromMongodConfig defaults to Require when absent.
		if len(proc.NetTLSSections()) > 0 && pkgtls.GetTLSModeFromMongodConfig(proc.Args()) != pkgtls.Disabled {
			return true, nil
		}
	}
	return false, nil
}

// extractCustomRoles returns custom roles from the deployment, or nil if none.
func extractCustomRoles(d om.Deployment) []mdbv1.MongoDBRole {
	roles := d.GetRoles()
	if len(roles) == 0 {
		return nil
	}
	return roles
}

// extractPrometheusConfig builds spec.prometheus from the deployment's Prometheus section.
func extractPrometheusConfig(d om.Deployment) (*mdbcv1.Prometheus, error) {
	acProm := d.GetPrometheus()
	if acProm == nil || !acProm.Enabled {
		return nil, nil
	}

	if acProm.Username == "" {
		return nil, fmt.Errorf("prometheus is enabled but has no username configured")
	}

	prom := &mdbcv1.Prometheus{
		Username: acProm.Username,
		PasswordSecretRef: mdbcv1.SecretKeyReference{
			Name: PrometheusPasswordSecretName,
			Key:  "password",
		},
	}

	if acProm.ListenAddress != "" {
		s := acProm.ListenAddress
		if i := strings.LastIndex(acProm.ListenAddress, ":"); i >= 0 {
			s = acProm.ListenAddress[i+1:]
		}
		port, _ := strconv.Atoi(s)
		if port <= 0 {
			return nil, fmt.Errorf("prometheus listenAddress %q does not contain a valid port", acProm.ListenAddress)
		}
		prom.Port = port
	}

	if acProm.MetricsPath != "" && acProm.MetricsPath != "/metrics" {
		prom.MetricsPath = acProm.MetricsPath
	}

	if acProm.Scheme == "https" {
		prom.TLSSecretRef = mdbcv1.SecretKeyReference{Name: PrometheusTLSSecretName}
	}

	return prom, nil
}

// extractAgentConfig builds spec.agent from project-level log rotation and the source process systemLog.
func extractAgentConfig(sourceProcess *om.Process, projectProcessConfigs *ProjectProcessConfigs) mdbv1.AgentConfig {
	var agentConfig mdbv1.AgentConfig
	if projectProcessConfigs != nil {
		if lr := projectProcessConfigs.SystemLogRotate; lr != nil && (lr.SizeThresholdMB != 0 || lr.TimeThresholdHrs != 0) {
			agentConfig.Mongod.LogRotate = automationconfig.ConvertACLogRotateToCrd(lr)
		}
		if alr := projectProcessConfigs.AuditLogRotate; alr != nil && (alr.SizeThresholdMB != 0 || alr.TimeThresholdHrs != 0) {
			agentConfig.Mongod.AuditLogRotate = automationconfig.ConvertACLogRotateToCrd(alr)
		}
		agentConfig.MonitoringAgent.LogRotate = om.LogRotateForAgentsFromAc(projectProcessConfigs.SystemLogRotate)
		agentConfig.BackupAgent.LogRotate = om.LogRotateForAgentsFromAc(projectProcessConfigs.SystemLogRotate)
	}
	if sourceProcess != nil {
		agentConfig.Mongod.SystemLog = automationconfig.SystemLogFromMap(sourceProcess.SystemLogMap())
	}
	return agentConfig
}

// sharedSpecFields holds fields common to single-cluster and multi-cluster specs.
type sharedSpecFields struct {
	security               *mdbv1.Security
	prometheus             *mdbcv1.Prometheus
	agentConfig            mdbv1.AgentConfig
	additionalMongodConfig *mdbv1.AdditionalMongodConfig
}

// buildReplicaSetCommonSpec constructs the DbCommonSpec shared across topologies.
func buildReplicaSetCommonSpec(version, fcv, rsName, resourceName string, externalMembers []mdbv1.ExternalMember, opts GenerateOptions) mdbv1.DbCommonSpec {
	common := mdbv1.DbCommonSpec{
		Version:                     version,
		ResourceType:                mdbv1.ReplicaSet,
		FeatureCompatibilityVersion: &fcv,
		ConnectionSpec: mdbv1.ConnectionSpec{
			SharedConnectionSpec: mdbv1.SharedConnectionSpec{
				OpsManagerConfig: &mdbv1.PrivateCloudConfig{
					ConfigMapRef: mdbv1.ConfigMapRef{
						Name: opts.ConfigMapName,
					},
				},
			},
			Credentials: opts.CredentialsSecretName,
		},
		ExternalMembers: externalMembers,
	}
	if resourceName != rsName {
		common.ReplicaSetNameOverride = rsName
	}
	return common
}

// buildSharedSpecFields extracts the spec fields shared across all topologies: security, Prometheus, additionalMongodConfig (from source process), and agent config.
func buildSharedSpecFields(ac *om.AutomationConfig, opts GenerateOptions) (sharedSpecFields, error) {
	security, err := buildSecurity(ac.Auth, opts.ProcessMap, opts.Members, ac.Ldap, ac.OIDCProviderConfigs, opts.CertsSecretPrefix)
	if err != nil {
		return sharedSpecFields{}, fmt.Errorf("failed to build security config: %w", err)
	}
	if roles := ac.Deployment.GetRoles(); len(roles) > 0 {
		if security == nil {
			security = &mdbv1.Security{}
		}
		security.Roles = roles
	}

	prom, err := extractPrometheusConfig(ac.Deployment)
	if err != nil {
		return sharedSpecFields{}, fmt.Errorf("failed to extract Prometheus config: %w", err)
	}

	var additionalConfig *mdbv1.AdditionalMongodConfig
	if opts.SourceProcess != nil {
		additionalConfig = opts.SourceProcess.AdditionalMongodConfig()
	}

	agentConfig := extractAgentConfig(opts.SourceProcess, opts.ProjectProcessConfigs)
	return sharedSpecFields{
		security:               security,
		prometheus:             prom,
		agentConfig:            agentConfig,
		additionalMongodConfig: additionalConfig,
	}, nil
}

// applySharedFields copies sharedSpecFields into a DbCommonSpec.
func applySharedFields(common *mdbv1.DbCommonSpec, shared sharedSpecFields) {
	common.Security = shared.security
	common.Prometheus = shared.prometheus
	common.AdditionalMongodConfig = shared.additionalMongodConfig
	common.Agent = shared.agentConfig
}

// buildReplicaSetSpec assembles the MongoDbSpec for a single-cluster replica set.
func buildReplicaSetSpec(
	version, fcv string,
	externalMembers []mdbv1.ExternalMember,
	rsName, resourceName string,
	opts GenerateOptions,
	ac *om.AutomationConfig,
) (mdbv1.MongoDbSpec, error) {
	shared, err := buildSharedSpecFields(ac, opts)
	if err != nil {
		return mdbv1.MongoDbSpec{}, err
	}
	spec := mdbv1.MongoDbSpec{
		DbCommonSpec: buildReplicaSetCommonSpec(version, fcv, rsName, resourceName, externalMembers, opts),
		Members:      len(externalMembers),
		MemberConfig: buildMemberConfig(opts.Members),
	}
	applySharedFields(&spec.DbCommonSpec, shared)
	return spec, nil
}

// buildReplicaSetMultiClusterSpec assembles a MongoDBMultiSpec, distributing members across target clusters.
func buildReplicaSetMultiClusterSpec(
	version, fcv string,
	externalMembers []mdbv1.ExternalMember,
	rsName, resourceName string,
	opts GenerateOptions,
	ac *om.AutomationConfig,
) (mdbmulti.MongoDBMultiSpec, error) {
	shared, err := buildSharedSpecFields(ac, opts)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, err
	}

	clusterSpecList := distributeMembers(len(externalMembers), opts.MultiClusterNames)
	clusterMemberConfig := distributeMemberConfig(opts.Members, opts.MultiClusterNames)
	for i := range clusterSpecList {
		clusterSpecList[i].MemberConfig = clusterMemberConfig[i]
	}

	spec := mdbmulti.MongoDBMultiSpec{
		DbCommonSpec:    buildReplicaSetCommonSpec(version, fcv, rsName, resourceName, externalMembers, opts),
		ClusterSpecList: clusterSpecList,
	}
	applySharedFields(&spec.DbCommonSpec, shared)
	return spec, nil
}

// buildMemberConfig sets votes=0 and priority="0" for all members (migration-safe defaults), preserving tags.
func buildMemberConfig(members []om.ReplicaSetMember) []automationconfig.MemberOptions {
	config := make([]automationconfig.MemberOptions, len(members))
	for i, m := range members {
		v, p := 0, "0"
		config[i] = automationconfig.MemberOptions{
			Votes:    &v,
			Priority: &p,
		}
		if tags := m.Tags(); len(tags) > 0 {
			config[i].Tags = tags
		}
	}
	return config
}

// distributeMembers spreads memberCount evenly across clusterNames, extra members go to earlier clusters.
func distributeMembers(memberCount int, clusterNames []string) mdbv1.ClusterSpecList {
	n := len(clusterNames)
	if n == 0 {
		return nil
	}
	base := memberCount / n
	remainder := memberCount % n

	list := make(mdbv1.ClusterSpecList, n)
	for i, name := range clusterNames {
		count := base
		if i < remainder {
			count++
		}
		list[i] = mdbv1.ClusterSpecItem{
			ClusterName: name,
			Members:     count,
		}
	}
	return list
}

// distributeMemberConfig slices buildMemberConfig output to match the distributeMembers layout.
func distributeMemberConfig(members []om.ReplicaSetMember, clusterNames []string) [][]automationconfig.MemberOptions {
	n := len(clusterNames)
	if n == 0 {
		return nil
	}
	allConfig := buildMemberConfig(members)
	base := len(allConfig) / n
	remainder := len(allConfig) % n

	result := make([][]automationconfig.MemberOptions, n)
	offset := 0
	for i := 0; i < n; i++ {
		count := base
		if i < remainder {
			count++
		}
		result[i] = allConfig[offset : offset+count]
		offset += count
	}
	return result
}
