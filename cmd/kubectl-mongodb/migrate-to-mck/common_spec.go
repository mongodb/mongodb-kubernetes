package migratetomck

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
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
		cr := mdbv1.ConvertACLdapToCR(acLdap)
		if acLdap.BindQueryUser != "" {
			cr.BindQuerySecretRef = mdbv1.SecretRef{Name: LdapBindQuerySecretName}
		}
		if acLdap.CaFileContents != "" {
			cr.CAConfigMapRef = &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: LdapCAConfigMapName},
				Key:                  LdapCAKey,
			}
		}
		authConfig.Ldap = cr
	}

	if crOIDC := authn.MapACOIDCToProviderConfigs(oidcConfigs); len(crOIDC) > 0 {
		authConfig.OIDCProviderConfigs = crOIDC
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
			Key:  passwordSecretDataKey,
		},
	}

	if acProm.ListenAddress != "" {
		s := acProm.ListenAddress
		if i := strings.LastIndex(acProm.ListenAddress, ":"); i >= 0 {
			s = acProm.ListenAddress[i+1:]
		}
		port, err := strconv.Atoi(s)
		if err != nil || port <= 0 {
			return nil, fmt.Errorf("prometheus listenAddress %q does not contain a valid port: %v", acProm.ListenAddress, err)
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
func extractAgentConfig(sourceProcess *om.Process, projectConfigs *ProjectConfigs) mdbv1.AgentConfig {
	var agentConfig mdbv1.AgentConfig
	if projectConfigs != nil {
		if lr := projectConfigs.SystemLogRotate; lr != nil && (lr.SizeThresholdMB != 0 || lr.TimeThresholdHrs != 0) {
			agentConfig.Mongod.LogRotate = automationconfig.ConvertACLogRotateToCrd(lr)
		}
		if alr := projectConfigs.AuditLogRotate; alr != nil && (alr.SizeThresholdMB != 0 || alr.TimeThresholdHrs != 0) {
			agentConfig.Mongod.AuditLogRotate = automationconfig.ConvertACLogRotateToCrd(alr)
		}
		agentConfig.MonitoringAgent.LogRotate = om.LogRotateForAgentsFromAc(projectConfigs.SystemLogRotate)
		agentConfig.BackupAgent.LogRotate = om.LogRotateForAgentsFromAc(projectConfigs.SystemLogRotate)
	}
	if sourceProcess != nil {
		agentConfig.Mongod.SystemLog = automationconfig.SystemLogFromMap(sourceProcess.SystemLogMap())
	}
	return agentConfig
}

// distributeMembers distributes externalMembers (by count) evenly across clusterNames, extra members
// going to earlier clusters. MemberConfig from rsMembers is assigned per slot; when rsMembers is nil
// or empty no MemberConfig is set, which is correct for processes without replica-set membership (mongos).
func distributeMembers(externalMembers []mdbv1.ExternalMember, rsMembers []om.ReplicaSetMember, clusterNames []string) (mdbv1.ClusterSpecList, error) {
	n := len(clusterNames)
	if n == 0 {
		return nil, nil
	}
	total := len(externalMembers)
	if total < n {
		return nil, fmt.Errorf("cannot distribute %d members across %d clusters: need at least one member per cluster", total, n)
	}
	base := total / n
	remainder := total % n
	allConfig := buildMemberConfig(rsMembers)

	list := make(mdbv1.ClusterSpecList, n)
	offset := 0
	for i, name := range clusterNames {
		count := base
		if i < remainder {
			count++
		}
		item := mdbv1.ClusterSpecItem{
			ClusterName: name,
			Members:     count,
		}
		if len(allConfig) > 0 {
			item.MemberConfig = allConfig[offset : offset+count]
		}
		list[i] = item
		offset += count
	}
	return list, nil
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
