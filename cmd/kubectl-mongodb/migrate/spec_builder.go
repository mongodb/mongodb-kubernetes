package migrate

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
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

// buildSecurity assembles the top-level spec.security block by inspecting
// TLS state, authentication mechanisms, LDAP, and OIDC from the automation config.
// certsSecretPrefix is the value for spec.security.certsSecretPrefix when TLS is enabled;
// must be non-empty when TLS is enabled (no default).
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

	tlsEnabled, err := isTLSEnabled(processMap, members)
	if err != nil {
		return nil, err
	}
	if tlsEnabled {
		// Use certsSecretPrefix to enable TLS; tls.enabled is deprecated (see RELEASE_NOTES_MEKO.md 1.15).
		// Caller must provide a non-empty prefix (no default).
		if certsSecretPrefix == "" {
			return nil, fmt.Errorf("certsSecretPrefix is required when TLS is enabled; provide a value when prompted")
		}
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

// buildAuthModes merges mechanisms from both deploymentAuthMechanisms and
// autoAuthMechanisms, deduplicating by the mapped CR mode value.
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

// extractInternalClusterAuthMode reads security.clusterAuthMode from the
// first member's process args2_6 and maps it to
// spec.security.authentication.internalCluster.
func extractInternalClusterAuthMode(processMap map[string]om.Process, members []om.ReplicaSetMember) (string, error) {
	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return "", fmt.Errorf("process %q referenced by member at index %d was not found", host, i)
		}
		if mode := proc.ClusterAuthMode(); mode != "" {
			return mapClusterAuthMode(mode)
		}
	}
	return "", nil
}

func mapClusterAuthMode(mode string) (string, error) {
	switch mode {
	case "x509":
		return util.X509, nil
	case "keyFile":
		return "", fmt.Errorf("clusterAuthMode %q is not supported by the operator (only x509 is supported). Migrate the deployment to x509 internal cluster authentication before using the operator.", mode)
	default:
		return "", fmt.Errorf("unsupported clusterAuthMode %q in automation config. Only x509 is supported by the operator.", mode)
	}
}

// isTLSEnabled returns true if at least one replica set member has TLS enabled.
func isTLSEnabled(processMap map[string]om.Process, members []om.ReplicaSetMember) (bool, error) {
	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return false, fmt.Errorf("process %q referenced by member at index %d was not found", host, i)
		}
		args := proc.Args()
		if len(args) == 0 {
			continue
		}
		// GetTLSModeFromMongodConfig defaults to Require when no mode is set,
		// but that only applies when TLS is already known to be enabled.
		// Here we need to detect presence: if args2_6 has no net.tls/ssl section
		// at all, TLS is not configured.
		if hasTLSSection(args) && pkgtls.GetTLSModeFromMongodConfig(args) != pkgtls.Disabled {
			return true, nil
		}
	}
	return false, nil
}

func hasTLSSection(args map[string]interface{}) bool {
	return maputil.ReadMapValueAsMap(args, "net", "tls") != nil ||
		maputil.ReadMapValueAsMap(args, "net", "ssl") != nil
}

// extractCustomRoles returns the custom MongoDB roles defined in the deployment.
func extractCustomRoles(d om.Deployment) []mdbv1.MongoDBRole {
	roles := d.GetRoles()
	if len(roles) == 0 {
		return nil
	}
	return roles
}

// extractPrometheusConfig reads the Prometheus section from the deployment
// and maps it to the CR's spec.prometheus block.
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


// extractAdditionalMongodConfig reads user-facing mongod options from the source
// process's args2_6 and maps them to spec.additionalMongodConfig.
// Fields the operator fully owns (dbPath, systemLog, replication.replSetName)
// are excluded.
func extractAdditionalMongodConfig(sourceProcess *om.Process) (*mdbv1.AdditionalMongodConfig, error) {
	if sourceProcess == nil {
		return nil, nil
	}

	args := sourceProcess.Args()
	if len(args) == 0 {
		return nil, nil
	}

	cfg := mdbv1.NewEmptyAdditionalMongodConfig()
	extractNetConfig(args, cfg)
	extractStorageConfig(args, cfg)
	extractReplicationConfig(args, cfg)
	extractGenericSections(args, cfg)
	extractNonDefaultTLSMode(args, cfg)

	if len(cfg.ToMap()) == 0 {
		return nil, nil
	}
	return cfg, nil
}


func extractNetConfig(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	netMap := maputil.ReadMapValueAsMap(args, "net")
	if netMap == nil {
		return false
	}
	hasConfig := false
	if port := maputil.ReadMapValueAsInt(netMap, "port"); port != 0 && port != util.MongoDbDefaultPort {
		config.AddOption("net.port", port)
		hasConfig = true
	}
	if compressors := maputil.ReadMapValueAsInterface(netMap, "compression", "compressors"); compressors != nil {
		config.AddOption("net.compression.compressors", compressors)
		hasConfig = true
	}
	if maxConns := maputil.ReadMapValueAsInt(netMap, "maxIncomingConnections"); maxConns != 0 {
		config.AddOption("net.maxIncomingConnections", maxConns)
		hasConfig = true
	}
	return hasConfig
}

func extractStorageConfig(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	if maputil.ReadMapValueAsMap(args, "storage") == nil {
		return false
	}
	hasConfig := false
	if engine := maputil.ReadMapValueAsString(args, "storage", "engine"); engine != "" && engine != "wiredTiger" {
		config.AddOption("storage.engine", engine)
		hasConfig = true
	}
	if v := maputil.ReadMapValueAsInterface(args, "storage", "directoryPerDB"); v != nil {
		config.AddOption("storage.directoryPerDB", v)
		hasConfig = true
	}
	if v := maputil.ReadMapValueAsInterface(args, "storage", "journal", "enabled"); v != nil {
		config.AddOption("storage.journal.enabled", v)
		hasConfig = true
	}
	if v := maputil.ReadMapValueAsInterface(args, "storage", "wiredTiger", "engineConfig", "cacheSizeGB"); v != nil {
		config.AddOption("storage.wiredTiger.engineConfig.cacheSizeGB", v)
		hasConfig = true
	}
	if v := maputil.ReadMapValueAsInterface(args, "storage", "wiredTiger", "engineConfig", "journalCompressor"); v != nil {
		config.AddOption("storage.wiredTiger.engineConfig.journalCompressor", v)
		hasConfig = true
	}
	if v := maputil.ReadMapValueAsInterface(args, "storage", "wiredTiger", "collectionConfig", "blockCompressor"); v != nil {
		config.AddOption("storage.wiredTiger.collectionConfig.blockCompressor", v)
		hasConfig = true
	}
	return hasConfig
}

func extractReplicationConfig(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	replMap := maputil.ReadMapValueAsMap(args, "replication")
	if replMap == nil {
		return false
	}
	if v := replMap["oplogSizeMB"]; v != nil {
		config.AddOption("replication.oplogSizeMB", v)
		return true
	}
	return false
}

func extractGenericSections(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	hasConfig := false

	for _, section := range []string{"setParameter", "auditLog", "operationProfiling"} {
		if sectionMap, ok := args[section].(map[string]interface{}); ok && len(sectionMap) > 0 {
			for k, v := range sectionMap {
				config.AddOption(section+"."+k, v)
				hasConfig = true
			}
		}
	}

	return hasConfig
}

// extractNonDefaultTLSMode adds the TLS mode to additionalMongodConfig when
// it differs from the operator's default (requireTLS). Modes "disabled" and
// empty are excluded: the customer must explicitly add
// net.tls.mode: "disabled" to the CR before applying.
func extractNonDefaultTLSMode(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	mode := pkgtls.GetTLSModeFromMongodConfig(args)
	if mode == pkgtls.Disabled || mode == pkgtls.Require || mode == "requireSSL" {
		return false
	}
	config.AddOption("net.tls.mode", string(mode))
	return true
}

// extractAgentConfig reads log rotation settings from the project-level OM API
// endpoints and systemLog from the first process in the automation config.
// Log rotation is applied uniformly to mongod, monitoring agent, and backup agent.
func extractAgentConfig(sourceProcess *om.Process, projectProcessConfigs *ProjectProcessConfigs) mdbv1.AgentConfig {
	var agentConfig mdbv1.AgentConfig
	if projectProcessConfigs != nil {
		agentConfig.Mongod.LogRotate = crdLogRotateFromAc(projectProcessConfigs.SystemLogRotate)
		agentConfig.Mongod.AuditLogRotate = crdLogRotateFromAc(projectProcessConfigs.AuditLogRotate)
		agentConfig.MonitoringAgent.LogRotate = om.LogRotateForAgentsFromAc(projectProcessConfigs.SystemLogRotate)
		agentConfig.BackupAgent.LogRotate = om.LogRotateForAgentsFromAc(projectProcessConfigs.SystemLogRotate)
	}
	if sourceProcess != nil {
		agentConfig.Mongod.SystemLog = automationconfig.SystemLogFromMap(sourceProcess.SystemLogMap())
	}
	return agentConfig
}


// crdLogRotateFromAc converts an AcLogRotate (agent representation with float64
// thresholds) into a CrdLogRotate (CRD representation with string thresholds).
func crdLogRotateFromAc(ac *automationconfig.AcLogRotate) *automationconfig.CrdLogRotate {
	if ac == nil || (ac.SizeThresholdMB == 0 && ac.TimeThresholdHrs == 0) {
		return nil
	}
	return automationconfig.ConvertACLogRotateToCrd(ac)
}

// sharedSpecFields holds the computed fields that are identical between
// single-cluster and multi-cluster replica set specs.
type sharedSpecFields struct {
	security               *mdbv1.Security
	prometheus             *mdbcv1.Prometheus
	agentConfig            mdbv1.AgentConfig
	additionalMongodConfig *mdbv1.AdditionalMongodConfig
}

// buildReplicaSetCommonSpec constructs the DbCommonSpec for replica set deployments,
// embedded by both MongoDbSpec (single-cluster) and MongoDBMultiSpec (multi-cluster).
// Not suitable for sharded clusters, which use a different ResourceType and spec structure.
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

// buildSharedSpecFields computes the spec fields shared across topologies:
// security (including custom roles), prometheus, additional mongod config,
// and agent config.
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

	additionalConfig, err := extractAdditionalMongodConfig(opts.SourceProcess)
	if err != nil {
		return sharedSpecFields{}, fmt.Errorf("failed to extract additional mongod config: %w", err)
	}

	agentConfig := extractAgentConfig(opts.SourceProcess, opts.ProcessConfigs)
	return sharedSpecFields{
		security:               security,
		prometheus:             prom,
		agentConfig:            agentConfig,
		additionalMongodConfig: additionalConfig,
	}, nil
}

// applySharedFields sets the fields from sharedSpecFields onto a DbCommonSpec.
// Called by each spec builder after constructing the topology-specific parts.
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

// buildReplicaSetMultiClusterSpec assembles a MongoDBMultiSpec, distributing members
// across the provided target clusters.
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

// buildMemberConfig creates MemberOptions for each member with votes=0 and
// priority="0" (draining policy for external members being transitioned).
// Tags are preserved from the automation config.
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

// distributeMembers spreads memberCount as evenly as possible across the
// given cluster names. Extra members go to the earlier clusters.
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

// distributeMemberConfig builds per-cluster MemberOptions slices that mirror
// the member distribution in distributeMembers. Each member gets votes=0 and
// priority="0" (draining policy). Tags are preserved from the automation config.
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
