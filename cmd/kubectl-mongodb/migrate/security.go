package migrate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
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
func buildSecurity(
	auth *om.Auth,
	processMap map[string]om.Process,
	members []om.ReplicaSetMember,
	acLdap *ldap.Ldap,
	oidcConfigs []oidc.ProviderConfig,
) (*mdbv1.Security, error) {
	security := &mdbv1.Security{}
	hasSettings := false

	tlsEnabled, err := isTLSEnabledForAnyMember(processMap, members)
	if err != nil {
		return nil, err
	}
	if tlsEnabled {
		security.TLSConfig = &mdbv1.TLSConfig{Enabled: true}
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

	if acLdap != nil && isLdapConfigured(acLdap) {
		authConfig.Ldap = convertLdapConfig(acLdap)
	}

	if len(oidcConfigs) > 0 {
		if crOIDC := convertOIDCConfigs(oidcConfigs); len(crOIDC) > 0 {
			authConfig.OIDCProviderConfigs = crOIDC
		}
	}

	if agentMode, ok := mapMechanismToAuthMode(auth.AutoAuthMechanism); ok {
		authConfig.Agents.Mode = string(agentMode)
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
			mode, ok := mapMechanismToAuthMode(mech)
			if !ok {
				return fmt.Errorf("unsupported auth mechanism %q in automation config (%s)", mech, source)
			}
			if !seen[mode] {
				modes = append(modes, mode)
				seen[mode] = true
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

// mapMechanismToAuthMode converts an automation config mechanism string to
// the corresponding CR AuthMode. The mapping mirrors the operator's
// convertToMechanismOrPanic in reverse.
func mapMechanismToAuthMode(mech string) (mdbv1.AuthMode, bool) {
	switch mech {
	case util.AutomationConfigScramSha1Option, // "MONGODB-CR"
		util.AutomationConfigScramSha256Option, // "SCRAM-SHA-256"
		util.SCRAMSHA1:                         // "SCRAM-SHA-1"
		return mdbv1.AuthMode(mech), true
	case util.AutomationConfigX509Option: // "MONGODB-X509" → CR "X509"
		return mdbv1.AuthMode(util.X509), true
	case util.AutomationConfigLDAPOption: // "PLAIN" → CR "LDAP"
		return mdbv1.AuthMode(util.LDAP), true
	case util.AutomationConfigOIDCOption: // "MONGODB-OIDC" → CR "OIDC"
		return mdbv1.AuthMode(util.OIDC), true
	default:
		return "", false
	}
}

// extractInternalClusterAuthMode reads security.clusterAuthMode from the
// first member's process args2_6 and maps it to
// spec.security.authentication.internalCluster.
func extractInternalClusterAuthMode(processMap map[string]om.Process, members []om.ReplicaSetMember) (string, error) {
	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return "", fmt.Errorf("process %q referenced by member at index %d not found", host, i)
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
		return "", fmt.Errorf("clusterAuthMode %q is not supported by the operator (only x509 is supported); migrate your deployment to x509 internal cluster authentication before using the operator", mode)
	default:
		return "", fmt.Errorf("unsupported clusterAuthMode %q in automation config; only x509 is supported by the operator", mode)
	}
}

// isTLSEnabledForAnyMember returns true if at least one replica set member
// has a non-disabled TLS mode in its process args2_6.
func isTLSEnabledForAnyMember(processMap map[string]om.Process, members []om.ReplicaSetMember) (bool, error) {
	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return false, fmt.Errorf("process %q referenced by member at index %d not found", host, i)
		}
		if isTLSEnabled(proc) {
			return true, nil
		}
	}
	return false, nil
}

// isTLSEnabled returns true if the process has an explicit non-disabled TLS mode.
func isTLSEnabled(process om.Process) bool {
	args := process.Args()
	if len(args) == 0 {
		return false
	}
	// GetTLSModeFromMongodConfig defaults to Require when no mode is set,
	// but that only applies when TLS is already known to be enabled.
	// Here we need to detect presence: if args2_6 has no net.tls/ssl section
	// at all, TLS is not configured.
	if !hasTLSSection(args) {
		return false
	}
	return pkgtls.GetTLSModeFromMongodConfig(args) != pkgtls.Disabled
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
		port := parsePortFromListenAddress(acProm.ListenAddress)
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

// IsPrometheusEnabled returns true when the AC has Prometheus monitoring
// enabled with a username configured (i.e. the generated CR will reference
// a prometheus-password Secret).
func IsPrometheusEnabled(d om.Deployment) bool {
	acProm := d.GetPrometheus()
	return acProm != nil && acProm.Enabled && acProm.Username != ""
}

func parsePortFromListenAddress(addr string) int {
	s := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		s = addr[i+1:]
	}
	port, _ := strconv.Atoi(s)
	return port
}

func isLdapConfigured(l *ldap.Ldap) bool {
	return l.Servers != "" || l.BindQueryUser != ""
}

// convertLdapConfig maps AC LDAP fields to the CR's spec.security.authentication.ldap.
// Secrets and ConfigMaps referenced here must be created by the user before applying the CR.
func convertLdapConfig(l *ldap.Ldap) *mdbv1.Ldap {
	cr := &mdbv1.Ldap{
		BindQueryUser:                 l.BindQueryUser,
		AuthzQueryTemplate:            l.AuthzQueryTemplate,
		UserToDNMapping:               l.UserToDnMapping,
		TimeoutMS:                     l.TimeoutMS,
		UserCacheInvalidationInterval: l.UserCacheInvalidationInterval,
		ValidateLDAPServerConfig:      &l.ValidateLDAPServerConfig,
	}

	if l.Servers != "" {
		servers := strings.Split(l.Servers, ",")
		for i := range servers {
			servers[i] = strings.TrimSpace(servers[i])
		}
		cr.Servers = servers
	}

	if l.TransportSecurity != "" {
		ts := mdbv1.TransportSecurity(l.TransportSecurity)
		cr.TransportSecurity = &ts
	}

	if l.BindQueryUser != "" {
		cr.BindQuerySecretRef = mdbv1.SecretRef{Name: LdapBindQuerySecretName}
	}

	if l.CaFileContents != "" {
		cr.CAConfigMapRef = &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: LdapCAConfigMapName},
			Key:                  LdapCAKey,
		}
	}

	return cr
}

// convertOIDCConfigs maps AC OIDC provider configs to the CR's
// spec.security.authentication.oidcProviderConfigs.
func convertOIDCConfigs(configs []oidc.ProviderConfig) []mdbv1.OIDCProviderConfig {
	var out []mdbv1.OIDCProviderConfig
	for _, c := range configs {
		authzType := mdbv1.OIDCAuthorizationType("UserID")
		authzMethod := mdbv1.OIDCAuthorizationMethod("WorkloadIdentityFederation")
		if c.SupportsHumanFlows {
			authzMethod = "WorkforceIdentityFederation"
		}
		if c.UseAuthorizationClaim {
			authzType = "GroupMembership"
		}

		out = append(out, mdbv1.OIDCProviderConfig{
			ConfigurationName:   c.AuthNamePrefix,
			IssuerURI:           c.IssuerUri,
			Audience:            c.Audience,
			AuthorizationType:   authzType,
			UserClaim:           c.UserClaim,
			GroupsClaim:         c.GroupsClaim,
			AuthorizationMethod: authzMethod,
			ClientId:            c.ClientId,
			RequestedScopes:     c.RequestedScopes,
		})
	}
	return out
}

// extractAdditionalMongodConfig reads user-facing mongod options from every
// member's args2_6 and maps them to spec.additionalMongodConfig. Only fields
// that have identical values across all members are included, since
// Kubernetes applies additionalMongodConfig uniformly to every member.
// Fields the operator fully owns (dbPath, systemLog, replication.replSetName)
// are excluded.
func extractAdditionalMongodConfig(processMap map[string]om.Process, members []om.ReplicaSetMember) (*mdbv1.AdditionalMongodConfig, error) {
	if len(members) == 0 {
		return nil, nil
	}

	var allConfigs []map[string]interface{}
	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return nil, fmt.Errorf("process %q referenced by member at index %d not found", host, i)
		}
		args := proc.Args()
		if len(args) == 0 {
			return nil, fmt.Errorf("process %q has no args2_6 configuration", host)
		}

		cfg := mdbv1.NewEmptyAdditionalMongodConfig()
		extractNetConfig(args, cfg)
		extractStorageConfig(args, cfg)
		extractReplicationConfig(args, cfg)
		extractGenericSections(args, cfg)
		extractNonDefaultTLSMode(args, cfg)

		allConfigs = append(allConfigs, cfg.ToMap())
	}

	common := intersectConfigMaps(allConfigs)
	if len(common) == 0 {
		return nil, nil
	}

	config := mdbv1.NewEmptyAdditionalMongodConfig()
	populateConfigFromMap(config, common, "")
	return config, nil
}

// populateConfigFromMap recursively walks a nested map and calls AddOption
// for each leaf value, preserving original Go types (no JSON roundtrip).
func populateConfigFromMap(config *mdbv1.AdditionalMongodConfig, m map[string]interface{}, prefix string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]interface{}); ok {
			populateConfigFromMap(config, sub, key)
		} else {
			config.AddOption(key, v)
		}
	}
}

// intersectConfigMaps returns a map containing only the keys and values
// that are identical across all input maps. Nested maps are intersected
// recursively; a nested key is kept only when all inputs agree on its value.
func intersectConfigMaps(maps []map[string]interface{}) map[string]interface{} {
	if len(maps) == 0 {
		return nil
	}
	if len(maps) == 1 {
		return maps[0]
	}

	result := make(map[string]interface{})
	for key, firstVal := range maps[0] {
		allPresent := true
		for _, other := range maps[1:] {
			if _, exists := other[key]; !exists {
				allPresent = false
				break
			}
		}
		if !allPresent {
			continue
		}

		firstSub, firstIsMap := firstVal.(map[string]interface{})
		if firstIsMap {
			subs := make([]map[string]interface{}, len(maps))
			subs[0] = firstSub
			allMaps := true
			for i, other := range maps[1:] {
				otherSub, ok := other[key].(map[string]interface{})
				if !ok {
					allMaps = false
					break
				}
				subs[i+1] = otherSub
			}
			if !allMaps {
				continue
			}
			sub := intersectConfigMaps(subs)
			if len(sub) > 0 {
				result[key] = sub
			}
		} else {
			firstJSON, _ := json.Marshal(firstVal)
			allEqual := true
			for _, other := range maps[1:] {
				otherJSON, _ := json.Marshal(other[key])
				if string(firstJSON) != string(otherJSON) {
					allEqual = false
					break
				}
			}
			if allEqual {
				result[key] = firstVal
			}
		}
	}

	return result
}

func extractNetConfig(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	if maputil.ReadMapValueAsMap(args, "net") == nil {
		return false
	}

	hasConfig := false

	if port := maputil.ReadMapValueAsInt(args, "net", "port"); port != 0 && port != util.MongoDbDefaultPort {
		config.AddOption("net.port", port)
		hasConfig = true
	}
	if compressors := maputil.ReadMapValueAsInterface(args, "net", "compression", "compressors"); compressors != nil {
		config.AddOption("net.compression.compressors", compressors)
		hasConfig = true
	}
	if maxConns := maputil.ReadMapValueAsInt(args, "net", "maxIncomingConnections"); maxConns != 0 {
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
	if v := maputil.ReadMapValueAsInterface(args, "replication", "oplogSizeMB"); v != nil {
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

// extractAgentConfig reads logRotate and auditLogRotate from the project-level
// OM API endpoints, monitoring agent logRotate from the monitoringAgentConfig
// endpoint, and systemLog from processes. The project-level settings are applied
// uniformly by the operator.
func extractAgentConfig(processMap map[string]om.Process, members []om.ReplicaSetMember, projectAgentConfigs *ProjectAgentConfigs, projectProcessConfigs *ProjectProcessConfigs) (mdbv1.AgentConfig, error) {
	if len(members) == 0 {
		return mdbv1.AgentConfig{}, nil
	}

	var systemLogMaps []map[string]interface{}
	allHaveSL := true

	for i, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			return mdbv1.AgentConfig{}, fmt.Errorf("process %q referenced by member at index %d not found", host, i)
		}

		sysLog := proc.SystemLogMap()
		if len(sysLog) > 0 {
			systemLogMaps = append(systemLogMaps, sysLog)
		} else {
			allHaveSL = false
		}
	}

	var agentConfig mdbv1.AgentConfig

	if projectProcessConfigs != nil {
		agentConfig.Mongod.LogRotate = crdLogRotateFromAc(projectProcessConfigs.SystemLogRotate)
		agentConfig.Mongod.AuditLogRotate = crdLogRotateFromAc(projectProcessConfigs.AuditLogRotate)
	}
	if projectAgentConfigs != nil {
		agentConfig.MonitoringAgent.LogRotate = monitoringLogRotateFromConfig(projectAgentConfigs.MonitoringConfig)
	}

	if allHaveSL && len(systemLogMaps) > 0 {
		common := intersectConfigMaps(systemLogMaps)
		agentConfig.Mongod.SystemLog = systemLogFromMap(common)
	}

	return agentConfig, nil
}

func monitoringLogRotateFromConfig(config *om.MonitoringAgentConfig) *mdbv1.LogRotateForBackupAndMonitoring {
	if config == nil {
		return nil
	}
	return config.ReadLogRotate()
}

// crdLogRotateFromAc converts an AcLogRotate (agent representation with float64
// thresholds) into a CrdLogRotate (CRD representation with string thresholds).
func crdLogRotateFromAc(ac *automationconfig.AcLogRotate) *automationconfig.CrdLogRotate {
	if ac == nil || (ac.SizeThresholdMB == 0 && ac.TimeThresholdHrs == 0) {
		return nil
	}
	return automationconfig.ConvertACLogRotateToCrd(ac)
}

func systemLogFromMap(m map[string]interface{}) *automationconfig.SystemLog {
	return automationconfig.SystemLogFromMap(m)
}
