package migratetomck

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	authn "github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// buildSecurity assembles spec.security. certsSecretPrefix non-empty means TLS is enabled
// (set by ensureTLS, mirrors the operator's IsSecurityTLSConfigEnabled).
func buildSecurity(ac *om.AutomationConfig, certsSecretPrefix, resourceName string) (*mdbv1.Security, error) {
	security := &mdbv1.Security{}
	hasSettings := false

	if certsSecretPrefix != "" {
		// certsSecretPrefix enables TLS (tls.enabled is deprecated, see RELEASE_NOTES_MEKO.md 1.15).
		security.CertificatesSecretsPrefix = certsSecretPrefix
		// Explicitly set the CA ConfigMap to the operator default "<resourceName>-ca" (see database_volumes.go).
		security.TLSConfig = &mdbv1.TLSConfig{CA: fmt.Sprintf("%s-ca", resourceName)}
		hasSettings = true
	}

	if ac.Auth != nil && ac.Auth.IsEnabled() {
		authConfig, err := buildAuthenticationConfig(ac)
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

func buildAuthenticationConfig(ac *om.AutomationConfig) (*mdbv1.Authentication, error) {
	auth := ac.Auth
	processMap := ac.Deployment.ProcessMap()
	members := ac.Deployment.GetReplicaSets()[0].Members()
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

	// Empty result means keyFile (the implicit default); only set when explicitly x509.
	internalCluster, err := extractInternalClusterAuthMode(processMap, members)
	if err != nil {
		return nil, err
	}
	if internalCluster != "" {
		authConfig.InternalCluster = internalCluster
	}

	if ac.Ldap != nil && (ac.Ldap.Servers != "" || ac.Ldap.BindQueryUser != "") {
		cr := mdbv1.ConvertACLdapToCR(ac.Ldap)
		if ac.Ldap.BindQueryUser != "" {
			cr.BindQuerySecretRef = mdbv1.SecretRef{Name: LdapBindQuerySecretName}
		}
		if ac.Ldap.CaFileContents != "" {
			cr.CAConfigMapRef = &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: LdapCAConfigMapName},
				Key:                  LdapCAKey,
			}
		}
		authConfig.Ldap = cr
	}

	if crOIDC := authn.MapACOIDCToProviderConfigs(ac.OIDCProviderConfigs); len(crOIDC) > 0 {
		authConfig.OIDCProviderConfigs = crOIDC
	}

	if agentMode, ok := authn.MapMechanismToAuthMode(auth.AutoAuthMechanism); ok {
		authConfig.Agents.Mode = agentMode
	}

	if auth.AutoUser != "" && auth.AutoUser != util.AutomationAgentUserName {
		authConfig.Agents.AutomationUserName = auth.AutoUser
	}

	if ac.AgentSSL != nil && ac.AgentSSL.AutoPEMKeyFilePath != "" {
		authConfig.Agents.AutoPEMKeyFilePath = ac.AgentSSL.AutoPEMKeyFilePath
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
		// keyFile is the default internal cluster auth when auth is enabled;
		// the operator uses it implicitly, so no explicit CR field is needed.
		return "", nil
	default:
		return "", fmt.Errorf("unsupported clusterAuthMode %q in automation config", mode)
	}
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
