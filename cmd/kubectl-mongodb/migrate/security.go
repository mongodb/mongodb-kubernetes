package migrate

import (
	"fmt"
	"strings"

	"github.com/spf13/cast"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// buildSecurity assembles the top-level spec.security block by inspecting
// TLS state, authentication mechanisms, LDAP, and OIDC from the automation config.
func buildSecurity(
	auth *om.Auth,
	processMap map[string]map[string]interface{},
	members []interface{},
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
	processMap map[string]map[string]interface{},
	members []interface{},
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
		util.SCRAMSHA1:                          // "SCRAM-SHA-1"
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
func extractInternalClusterAuthMode(processMap map[string]map[string]interface{}, members []interface{}) (string, error) {
	for i, m := range members {
		member, ok := m.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("member at index %d is not a valid map", i)
		}
		host := cast.ToString(member["host"])
		proc, ok := processMap[host]
		if !ok {
			return "", fmt.Errorf("process %q referenced by member at index %d not found", host, i)
		}
		args, ok := proc["args2_6"].(map[string]interface{})
		if !ok {
			continue
		}
		sec, ok := args["security"].(map[string]interface{})
		if !ok {
			continue
		}
		if mode := cast.ToString(sec["clusterAuthMode"]); mode != "" {
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
func isTLSEnabledForAnyMember(processMap map[string]map[string]interface{}, members []interface{}) (bool, error) {
	for i, m := range members {
		member, ok := m.(map[string]interface{})
		if !ok {
			return false, fmt.Errorf("member at index %d is not a valid map", i)
		}
		host := cast.ToString(member["host"])
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

// isTLSEnabled returns true if the process has any non-disabled TLS mode.
// The operator only reads net.tls.mode from additionalMongodConfig when
// spec.security.tls.enabled is true, so any TLS mode (require, prefer,
// allow) must result in tls.enabled=true in the CR.
func isTLSEnabled(process map[string]interface{}) bool {
	args, ok := process["args2_6"].(map[string]interface{})
	if !ok {
		return false
	}
	mode := extractTLSMode(args)
	return mode != "" && mode != "disabled"
}

func extractTLSMode(args map[string]interface{}) string {
	if net, ok := args["net"].(map[string]interface{}); ok {
		if tls, ok := net["tls"].(map[string]interface{}); ok {
			if mode := cast.ToString(tls["mode"]); mode != "" {
				return mode
			}
		}
		if ssl, ok := net["ssl"].(map[string]interface{}); ok {
			if mode := cast.ToString(ssl["mode"]); mode != "" {
				return mode
			}
		}
	}
	return ""
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
	promRaw, ok := d["prometheus"]
	if !ok || promRaw == nil {
		return nil, nil
	}

	promMap, ok := promRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("prometheus config is present but is not a valid map")
	}

	if !cast.ToBool(promMap["enabled"]) {
		return nil, nil
	}

	username := cast.ToString(promMap["username"])
	if username == "" {
		return nil, fmt.Errorf("prometheus is enabled but has no username configured")
	}

	prom := &mdbcv1.Prometheus{
		Username: username,
		PasswordSecretRef: mdbcv1.SecretKeyReference{
			Name: "prometheus-password",
			Key:  "password",
		},
	}

	if listenAddr := cast.ToString(promMap["listenAddress"]); listenAddr != "" {
		port := parsePortFromListenAddress(listenAddr)
		if port <= 0 {
			return nil, fmt.Errorf("prometheus listenAddress %q does not contain a valid port", listenAddr)
		}
		prom.Port = port
	}

	if metricsPath := cast.ToString(promMap["metricsPath"]); metricsPath != "" && metricsPath != "/metrics" {
		prom.MetricsPath = metricsPath
	}

	if cast.ToString(promMap["scheme"]) == "https" {
		prom.TLSSecretRef = mdbcv1.SecretKeyReference{Name: "prometheus-tls"}
	}

	return prom, nil
}

func parsePortFromListenAddress(addr string) int {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return cast.ToInt(addr[i+1:])
	}
	return cast.ToInt(addr)
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
		cr.BindQuerySecretRef = mdbv1.SecretRef{Name: "ldap-bind-query-password"}
	}

	if l.CaFileContents != "" {
		cr.CAConfigMapRef = &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "ldap-ca"},
			Key:                  "ca.pem",
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

// extractAdditionalMongodConfig reads user-facing mongod options from the first
// member's args2_6 and maps them to spec.additionalMongodConfig. Fields the
// operator fully owns (dbPath, systemLog, replication.replSetName) are excluded.
func extractAdditionalMongodConfig(processMap map[string]map[string]interface{}, members []interface{}) (*mdbv1.AdditionalMongodConfig, error) {
	if len(members) == 0 {
		return nil, nil
	}

	member, ok := members[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("member at index 0 is not a valid map")
	}
	host := cast.ToString(member["host"])
	proc, ok := processMap[host]
	if !ok {
		return nil, fmt.Errorf("process %q referenced by member at index 0 not found", host)
	}
	args, ok := proc["args2_6"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("process %q has no args2_6 configuration", host)
	}

	config := mdbv1.NewEmptyAdditionalMongodConfig()
	hasConfig := false

	hasConfig = extractNetConfig(args, config) || hasConfig
	hasConfig = extractStorageConfig(args, config) || hasConfig
	hasConfig = extractReplicationConfig(args, config) || hasConfig
	hasConfig = extractGenericSections(args, config) || hasConfig
	hasConfig = extractNonDefaultTLSMode(args, config) || hasConfig

	if hasConfig {
		return config, nil
	}
	return nil, nil
}

func extractNetConfig(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	net, ok := args["net"].(map[string]interface{})
	if !ok {
		return false
	}

	hasConfig := false

	if port := cast.ToInt(net["port"]); port != 0 && port != util.MongoDbDefaultPort {
		config.AddOption("net.port", port)
		hasConfig = true
	}
	if compression, ok := net["compression"].(map[string]interface{}); ok {
		if compressors, ok := compression["compressors"]; ok {
			config.AddOption("net.compression.compressors", compressors)
			hasConfig = true
		}
	}
	if maxConns := cast.ToInt(net["maxIncomingConnections"]); maxConns != 0 {
		config.AddOption("net.maxIncomingConnections", maxConns)
		hasConfig = true
	}

	return hasConfig
}

func extractStorageConfig(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	storage, ok := args["storage"].(map[string]interface{})
	if !ok {
		return false
	}

	hasConfig := false

	if engine := cast.ToString(storage["engine"]); engine != "" && engine != "wiredTiger" {
		config.AddOption("storage.engine", engine)
		hasConfig = true
	}
	if dirPerDB, ok := storage["directoryPerDB"]; ok {
		config.AddOption("storage.directoryPerDB", dirPerDB)
		hasConfig = true
	}
	if journal, ok := storage["journal"].(map[string]interface{}); ok {
		if enabled, ok := journal["enabled"]; ok {
			config.AddOption("storage.journal.enabled", enabled)
			hasConfig = true
		}
	}

	if wt, ok := storage["wiredTiger"].(map[string]interface{}); ok {
		if ec, ok := wt["engineConfig"].(map[string]interface{}); ok {
			if v, ok := ec["cacheSizeGB"]; ok {
				config.AddOption("storage.wiredTiger.engineConfig.cacheSizeGB", v)
				hasConfig = true
			}
			if v, ok := ec["journalCompressor"]; ok {
				config.AddOption("storage.wiredTiger.engineConfig.journalCompressor", v)
				hasConfig = true
			}
		}
		if cc, ok := wt["collectionConfig"].(map[string]interface{}); ok {
			if v, ok := cc["blockCompressor"]; ok {
				config.AddOption("storage.wiredTiger.collectionConfig.blockCompressor", v)
				hasConfig = true
			}
		}
	}

	return hasConfig
}

func extractReplicationConfig(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	repl, ok := args["replication"].(map[string]interface{})
	if !ok {
		return false
	}
	if oplogSizeMB, ok := repl["oplogSizeMB"]; ok {
		config.AddOption("replication.oplogSizeMB", oplogSizeMB)
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
// it differs from the operator's default (requireTLS). The operator reads
// this value via pkg/tls.GetTLSModeFromMongodConfig when tls.enabled=true.
func extractNonDefaultTLSMode(args map[string]interface{}, config *mdbv1.AdditionalMongodConfig) bool {
	mode := extractTLSMode(args)
	if mode != "" && mode != "requireSSL" && mode != "requireTLS" {
		config.AddOption("net.tls.mode", mode)
		return true
	}
	return false
}

// extractAgentConfig reads logRotate, auditLogRotate, and systemLog from
// process entries and maps them to spec.agent.mongod.
func extractAgentConfig(processMap map[string]map[string]interface{}, members []interface{}) (mdbv1.AgentConfig, error) {
	var agentConfig mdbv1.AgentConfig
	hasConfig := false

	for i, m := range members {
		member, ok := m.(map[string]interface{})
		if !ok {
			return mdbv1.AgentConfig{}, fmt.Errorf("member at index %d is not a valid map", i)
		}
		host := cast.ToString(member["host"])
		proc, ok := processMap[host]
		if !ok {
			return mdbv1.AgentConfig{}, fmt.Errorf("process %q referenced by member at index %d not found", host, i)
		}

		lr, err := extractLogRotate(proc, "logRotate")
		if err != nil {
			return mdbv1.AgentConfig{}, fmt.Errorf("error extracting logRotate for process %q: %w", host, err)
		}
		if lr != nil {
			agentConfig.Mongod.LogRotate = lr
			hasConfig = true
		}

		alr, err := extractLogRotate(proc, "auditLogRotate")
		if err != nil {
			return mdbv1.AgentConfig{}, fmt.Errorf("error extracting auditLogRotate for process %q: %w", host, err)
		}
		if alr != nil {
			agentConfig.Mongod.AuditLogRotate = alr
			hasConfig = true
		}

		if sysLog := extractSystemLog(proc); sysLog != nil {
			agentConfig.Mongod.SystemLog = sysLog
			hasConfig = true
		}

		if hasConfig {
			break
		}
	}

	return agentConfig, nil
}

func extractLogRotate(proc map[string]interface{}, key string) (*automationconfig.CrdLogRotate, error) {
	lrRaw, ok := proc[key]
	if !ok || lrRaw == nil {
		return nil, nil
	}
	lrMap, ok := lrRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s is present but is not a valid map", key)
	}
	if len(lrMap) == 0 {
		return nil, nil
	}

	sizeThresholdMB := cast.ToString(lrMap["sizeThresholdMB"])
	timeThresholdHrs := cast.ToInt(lrMap["timeThresholdHrs"])
	if sizeThresholdMB == "" && timeThresholdHrs == 0 {
		return nil, nil
	}

	lr := &automationconfig.CrdLogRotate{
		SizeThresholdMB:    sizeThresholdMB,
		PercentOfDiskspace: cast.ToString(lrMap["percentOfDiskspace"]),
	}
	lr.TimeThresholdHrs = timeThresholdHrs
	lr.NumUncompressed = cast.ToInt(lrMap["numUncompressed"])
	lr.NumTotal = cast.ToInt(lrMap["numTotal"])
	lr.IncludeAuditLogsWithMongoDBLogs = cast.ToBool(lrMap["includeAuditLogsWithMongoDBLogs"])
	return lr, nil
}

func extractSystemLog(proc map[string]interface{}) *automationconfig.SystemLog {
	args, ok := proc["args2_6"].(map[string]interface{})
	if !ok {
		return nil
	}
	sysLog, ok := args["systemLog"].(map[string]interface{})
	if !ok {
		return nil
	}

	dest := cast.ToString(sysLog["destination"])
	logPath := cast.ToString(sysLog["path"])
	if dest == "" && logPath == "" {
		return nil
	}

	return &automationconfig.SystemLog{
		Destination: automationconfig.Destination(dest),
		Path:        logPath,
		LogAppend:   cast.ToBool(sysLog["logAppend"]),
	}
}
