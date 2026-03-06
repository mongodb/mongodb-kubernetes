package migrate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
)

func TestBuildAuthModes_FromDeploymentAuthMechanisms(t *testing.T) {
	tests := []struct {
		name     string
		mechs    []string
		expected []mdbv1.AuthMode
	}{
		{
			name:     "SCRAM-SHA-256",
			mechs:    []string{"SCRAM-SHA-256"},
			expected: []mdbv1.AuthMode{"SCRAM-SHA-256"},
		},
		{
			name:     "SCRAM-SHA-1",
			mechs:    []string{"SCRAM-SHA-1"},
			expected: []mdbv1.AuthMode{"SCRAM-SHA-1"},
		},
		{
			name:     "MONGODB-CR",
			mechs:    []string{"MONGODB-CR"},
			expected: []mdbv1.AuthMode{"MONGODB-CR"},
		},
		{
			name:     "X509",
			mechs:    []string{"MONGODB-X509"},
			expected: []mdbv1.AuthMode{"X509"},
		},
		{
			name:     "LDAP from PLAIN",
			mechs:    []string{"PLAIN"},
			expected: []mdbv1.AuthMode{"LDAP"},
		},
		{
			name:     "OIDC from MONGODB-OIDC",
			mechs:    []string{"MONGODB-OIDC"},
			expected: []mdbv1.AuthMode{"OIDC"},
		},
		{
			name:     "multiple mechanisms",
			mechs:    []string{"SCRAM-SHA-256", "MONGODB-X509"},
			expected: []mdbv1.AuthMode{"SCRAM-SHA-256", "X509"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &om.Auth{DeploymentAuthMechanisms: tt.mechs}
			modes, err := buildAuthModes(auth)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, modes)
		})
	}
}

func TestBuildAuthModes_UnknownMechanism(t *testing.T) {
	auth := &om.Auth{DeploymentAuthMechanisms: []string{"UNKNOWN-MECH"}}
	_, err := buildAuthModes(auth)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported auth mechanism")
	assert.Contains(t, err.Error(), "UNKNOWN-MECH")
}

func TestBuildAuthModes_MergesAutoAndDeploymentMechanisms(t *testing.T) {
	auth := &om.Auth{
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"},
		AutoAuthMechanisms:       []string{"MONGODB-X509"},
	}
	modes, err := buildAuthModes(auth)
	require.NoError(t, err)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM-SHA-256", "X509"}, modes)
}

func TestBuildAuthModes_DeduplicatesMechanisms(t *testing.T) {
	auth := &om.Auth{
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256", "MONGODB-X509"},
		AutoAuthMechanisms:       []string{"SCRAM-SHA-256"},
	}
	modes, err := buildAuthModes(auth)
	require.NoError(t, err)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM-SHA-256", "X509"}, modes)
}

func TestBuildAuthModes_AutoOnlyMechanisms(t *testing.T) {
	auth := &om.Auth{
		AutoAuthMechanisms: []string{"SCRAM-SHA-256"},
	}
	modes, err := buildAuthModes(auth)
	require.NoError(t, err)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM-SHA-256"}, modes)
}

func TestBuildAuthModes_UnknownAutoMechanism(t *testing.T) {
	auth := &om.Auth{
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"},
		AutoAuthMechanisms:       []string{"UNKNOWN-AUTO"},
	}
	_, err := buildAuthModes(auth)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "autoAuthMechanisms")
	assert.Contains(t, err.Error(), "UNKNOWN-AUTO")
}

func TestMapMechanismToAuthMode(t *testing.T) {
	tests := []struct {
		mech     string
		expected mdbv1.AuthMode
		ok       bool
	}{
		{"MONGODB-CR", "MONGODB-CR", true},
		{"SCRAM-SHA-256", "SCRAM-SHA-256", true},
		{"SCRAM-SHA-1", "SCRAM-SHA-1", true},
		{"MONGODB-X509", "X509", true},
		{"PLAIN", "LDAP", true},
		{"MONGODB-OIDC", "OIDC", true},
		{"UNKNOWN", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.mech, func(t *testing.T) {
			mode, ok := mapMechanismToAuthMode(tt.mech)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.expected, mode)
			}
		})
	}
}

func TestIsTLSEnabled_TLSMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		enabled bool
	}{
		{"requireSSL", "requireSSL", true},
		{"requireTLS", "requireTLS", true},
		{"preferSSL", "preferSSL", true},
		{"preferTLS", "preferTLS", true},
		{"allowSSL", "allowSSL", true},
		{"allowTLS", "allowTLS", true},
		{"disabled", "disabled", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			process := map[string]interface{}{
				"args2_6": map[string]interface{}{
					"net": map[string]interface{}{
						"tls": map[string]interface{}{
							"mode": tt.mode,
						},
					},
				},
			}
			assert.Equal(t, tt.enabled, isTLSEnabled(process))
		})
	}
}

func TestIsTLSEnabled_SSLMode(t *testing.T) {
	process := map[string]interface{}{
		"args2_6": map[string]interface{}{
			"net": map[string]interface{}{
				"ssl": map[string]interface{}{
					"mode": "requireSSL",
				},
			},
		},
	}
	assert.True(t, isTLSEnabled(process))
}

func TestIsTLSEnabled_NoArgs(t *testing.T) {
	assert.False(t, isTLSEnabled(map[string]interface{}{}))
}

func TestIsTLSEnabled_NoNet(t *testing.T) {
	process := map[string]interface{}{
		"args2_6": map[string]interface{}{},
	}
	assert.False(t, isTLSEnabled(process))
}

func TestBuildSecurity_NilAuth(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	result, err := buildSecurity(nil, processMap, members, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestBuildSecurity_AuthDisabled(t *testing.T) {
	auth := &om.Auth{Disabled: true}
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	result, err := buildSecurity(auth, processMap, members, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestBuildSecurity_AuthEnabled(t *testing.T) {
	auth := &om.Auth{
		Disabled:                 false,
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"},
	}
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	result, err := buildSecurity(auth, processMap, members, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Authentication)
	assert.True(t, result.Authentication.Enabled)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM-SHA-256"}, result.Authentication.Modes)
}

func TestBuildSecurity_TLSAndAuth(t *testing.T) {
	auth := &om.Auth{
		Disabled:                 false,
		DeploymentAuthMechanisms: []string{"MONGODB-X509"},
	}
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"tls": map[string]interface{}{"mode": "requireTLS"},
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	result, err := buildSecurity(auth, processMap, members, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.TLSConfig)
	assert.True(t, result.TLSConfig.Enabled)
	require.NotNil(t, result.Authentication)
	assert.Equal(t, []mdbv1.AuthMode{"X509"}, result.Authentication.Modes)
}

func TestBuildSecurity_InternalClusterAuth(t *testing.T) {
	auth := &om.Auth{
		Disabled:                 false,
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"},
	}
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"security": map[string]interface{}{
					"clusterAuthMode": "x509",
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	result, err := buildSecurity(auth, processMap, members, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Authentication)
	assert.Equal(t, "X509", result.Authentication.InternalCluster)
}

func TestBuildSecurity_InvalidMember(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{"not-a-map"}

	_, err := buildSecurity(nil, processMap, members, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid map")
}

func TestBuildSecurity_MissingProcess(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{
		map[string]interface{}{"host": "missing-host"},
	}

	_, err := buildSecurity(nil, processMap, members, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractAdditionalMongodConfig_NonDefaultPort(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27018,
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	netMap, ok := m["net"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 27018, netMap["port"])
}

func TestExtractAdditionalMongodConfig_DefaultPort(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_WiredTigerCache(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
				},
				"storage": map[string]interface{}{
					"wiredTiger": map[string]interface{}{
						"engineConfig": map[string]interface{}{
							"cacheSizeGB": 2.0,
						},
					},
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	storage, ok := m["storage"].(map[string]interface{})
	require.True(t, ok)
	wt, ok := storage["wiredTiger"].(map[string]interface{})
	require.True(t, ok)
	ec, ok := wt["engineConfig"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 2.0, ec["cacheSizeGB"])
}

func TestExtractAdditionalMongodConfig_NoArgs(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	_, err := extractAdditionalMongodConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no args2_6 configuration")
}

func TestExtractAdditionalMongodConfig_NoMembers(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_InvalidMember(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{"not-a-map"}

	_, err := extractAdditionalMongodConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid map")
}

func TestExtractAdditionalMongodConfig_MissingProcess(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{
		map[string]interface{}{"host": "missing-host"},
	}

	_, err := extractAdditionalMongodConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractAdditionalMongodConfig_SetParameter(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
				},
				"setParameter": map[string]interface{}{
					"authenticationMechanisms": "SCRAM-SHA-256",
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	sp, ok := m["setParameter"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "SCRAM-SHA-256", sp["authenticationMechanisms"])
}

func TestExtractAdditionalMongodConfig_OplogSizeMB(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
				},
				"replication": map[string]interface{}{
					"replSetName": "my-rs",
					"oplogSizeMB": 2048,
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	repl, ok := m["replication"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 2048, repl["oplogSizeMB"])
}

func TestExtractAdditionalMongodConfig_AuditLog(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
				},
				"auditLog": map[string]interface{}{
					"destination": "file",
					"format":      "JSON",
					"path":        "/var/log/mongodb/audit.json",
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	al, ok := m["auditLog"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "file", al["destination"])
	assert.Equal(t, "JSON", al["format"])
}

func TestExtractInternalClusterAuthMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		expected string
	}{
		{"x509", "x509", "X509"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processMap := map[string]map[string]interface{}{
				"host-0": {
					"args2_6": map[string]interface{}{
						"security": map[string]interface{}{
							"clusterAuthMode": tt.mode,
						},
					},
				},
			}
			members := []interface{}{
				map[string]interface{}{"host": "host-0"},
			}
			result, err := extractInternalClusterAuthMode(processMap, members)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractInternalClusterAuthMode_KeyFileNotSupported(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"security": map[string]interface{}{
					"clusterAuthMode": "keyFile",
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}
	_, err := extractInternalClusterAuthMode(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported by the operator")
	assert.Contains(t, err.Error(), "keyFile")
}

func TestExtractInternalClusterAuthMode_UnsupportedMode(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"security": map[string]interface{}{
					"clusterAuthMode": "unsupported-mode",
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}
	_, err := extractInternalClusterAuthMode(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported clusterAuthMode")
}

func TestExtractInternalClusterAuthMode_InvalidMember(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{"not-a-map"}
	_, err := extractInternalClusterAuthMode(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid map")
}

func TestExtractInternalClusterAuthMode_MissingProcess(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{
		map[string]interface{}{"host": "missing-host"},
	}
	_, err := extractInternalClusterAuthMode(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestConvertLdapConfig(t *testing.T) {
	l := &ldap.Ldap{
		Servers:              "ldap.example.com:636",
		TransportSecurity:    "tls",
		BindQueryUser:        "cn=admin,dc=example,dc=com",
		AuthzQueryTemplate:   "{USER}?memberOf?base",
		UserToDnMapping:      `[{"match":"(.+)","substitution":"cn={0},dc=example,dc=com"}]`,
		TimeoutMS:            10000,
	}

	cr := convertLdapConfig(l)
	require.NotNil(t, cr)
	assert.Equal(t, []string{"ldap.example.com:636"}, cr.Servers)
	assert.Equal(t, mdbv1.TransportSecurity("tls"), *cr.TransportSecurity)
	assert.Equal(t, "cn=admin,dc=example,dc=com", cr.BindQueryUser)
	assert.Equal(t, "ldap-bind-query-password", cr.BindQuerySecretRef.Name)
	assert.Equal(t, "{USER}?memberOf?base", cr.AuthzQueryTemplate)
	assert.Equal(t, 10000, cr.TimeoutMS)
}

func TestConvertLdapConfig_MultipleServers(t *testing.T) {
	l := &ldap.Ldap{
		Servers: "ldap1.example.com:636, ldap2.example.com:636,ldap3.example.com:636",
	}

	cr := convertLdapConfig(l)
	require.NotNil(t, cr)
	assert.Equal(t, []string{"ldap1.example.com:636", "ldap2.example.com:636", "ldap3.example.com:636"}, cr.Servers)
}

func TestConvertLdapConfig_NoBindUser(t *testing.T) {
	l := &ldap.Ldap{
		Servers: "ldap.example.com:636",
	}

	cr := convertLdapConfig(l)
	require.NotNil(t, cr)
	assert.Empty(t, cr.BindQuerySecretRef.Name)
}

func TestConvertLdapConfig_CaFileContents(t *testing.T) {
	l := &ldap.Ldap{
		Servers:        "ldap.example.com:636",
		CaFileContents: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
	}

	cr := convertLdapConfig(l)
	require.NotNil(t, cr)
	require.NotNil(t, cr.CAConfigMapRef)
	assert.Equal(t, "ldap-ca", cr.CAConfigMapRef.Name)
	assert.Equal(t, "ca.pem", cr.CAConfigMapRef.Key)
}

func TestConvertLdapConfig_NoCaFileContents(t *testing.T) {
	l := &ldap.Ldap{
		Servers: "ldap.example.com:636",
	}

	cr := convertLdapConfig(l)
	require.NotNil(t, cr)
	assert.Nil(t, cr.CAConfigMapRef)
}

func TestConvertOIDCConfigs(t *testing.T) {
	configs := []oidc.ProviderConfig{
		{
			AuthNamePrefix:     "WORKFORCE",
			Audience:           "my-audience",
			IssuerUri:          "https://issuer.example.com",
			UserClaim:          "sub",
			SupportsHumanFlows: true,
			UseAuthorizationClaim: false,
			ClientId:           strPtr("client-123"),
			RequestedScopes:    []string{"openid", "profile"},
		},
		{
			AuthNamePrefix:     "WORKLOAD",
			Audience:           "api-audience",
			IssuerUri:          "https://issuer.example.com",
			UserClaim:          "sub",
			SupportsHumanFlows: false,
			UseAuthorizationClaim: true,
			GroupsClaim:        strPtr("groups"),
		},
	}

	result := convertOIDCConfigs(configs)
	require.Len(t, result, 2)

	assert.Equal(t, "WORKFORCE", result[0].ConfigurationName)
	assert.Equal(t, mdbv1.OIDCAuthorizationMethod("WorkforceIdentityFederation"), result[0].AuthorizationMethod)
	assert.Equal(t, mdbv1.OIDCAuthorizationType("UserID"), result[0].AuthorizationType)
	assert.Equal(t, "client-123", *result[0].ClientId)
	assert.Equal(t, []string{"openid", "profile"}, result[0].RequestedScopes)

	assert.Equal(t, "WORKLOAD", result[1].ConfigurationName)
	assert.Equal(t, mdbv1.OIDCAuthorizationMethod("WorkloadIdentityFederation"), result[1].AuthorizationMethod)
	assert.Equal(t, mdbv1.OIDCAuthorizationType("GroupMembership"), result[1].AuthorizationType)
	assert.Equal(t, "groups", *result[1].GroupsClaim)
}

func TestExtractAdditionalMongodConfig_DbPathNotExtracted(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net":     map[string]interface{}{"port": 27017},
				"storage": map[string]interface{}{"dbPath": "/data/custom"},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "dbPath should not be extracted into additionalMongodConfig because the operator always overwrites it")
}

func TestExtractAdditionalMongodConfig_DefaultDbPath(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net":     map[string]interface{}{"port": 27017},
				"storage": map[string]interface{}{"dbPath": "/data"},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_SystemLogNotExtracted(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{"port": 27017},
				"systemLog": map[string]interface{}{
					"destination": "file",
					"path":        "/var/log/mongodb/mongod.log",
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "systemLog should not be extracted into additionalMongodConfig because the operator always overwrites it; use spec.agent.mongod.systemLog instead")
}

func TestExtractAdditionalMongodConfig_TLSModePrefer(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
					"tls":  map[string]interface{}{"mode": "preferSSL"},
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	net, ok := m["net"].(map[string]interface{})
	require.True(t, ok)
	tls, ok := net["tls"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "preferSSL", tls["mode"])
}

func TestExtractAdditionalMongodConfig_TLSModeRequireNotIncluded(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
					"tls":  map[string]interface{}{"mode": "requireSSL"},
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAgentConfig_LogRotate(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"logRotate": map[string]interface{}{
				"sizeThresholdMB":  1000.0,
				"timeThresholdHrs": 24,
				"numUncompressed":  5,
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.LogRotate)
	assert.Equal(t, "1000", agentConfig.Mongod.LogRotate.SizeThresholdMB)
	assert.Equal(t, 24, agentConfig.Mongod.LogRotate.TimeThresholdHrs)
	assert.Equal(t, 5, agentConfig.Mongod.LogRotate.NumUncompressed)
}

func TestExtractAgentConfig_AuditLogRotate(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"auditLogRotate": map[string]interface{}{
				"sizeThresholdMB":  500.0,
				"timeThresholdHrs": 48,
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.AuditLogRotate)
	assert.Equal(t, "500", agentConfig.Mongod.AuditLogRotate.SizeThresholdMB)
	assert.Equal(t, 48, agentConfig.Mongod.AuditLogRotate.TimeThresholdHrs)
}

func TestExtractAgentConfig_NoLogRotate(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, agentConfig.Mongod.LogRotate)
	assert.Nil(t, agentConfig.Mongod.AuditLogRotate)
}

func TestExtractAgentConfig_InvalidMember(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{"not-a-map"}

	_, err := extractAgentConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid map")
}

func TestExtractAgentConfig_MissingProcess(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{
		map[string]interface{}{"host": "missing-host"},
	}

	_, err := extractAgentConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractAgentConfig_MalformedLogRotate(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"logRotate": "not-a-map",
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	_, err := extractAgentConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid map")
}

func TestExtractAgentConfig_SystemLog(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"systemLog": map[string]interface{}{
					"destination": "file",
					"path":        "/var/log/mongodb/mongod.log",
					"logAppend":   true,
				},
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.SystemLog)
	assert.Equal(t, "file", string(agentConfig.Mongod.SystemLog.Destination))
	assert.Equal(t, "/var/log/mongodb/mongod.log", agentConfig.Mongod.SystemLog.Path)
	assert.True(t, agentConfig.Mongod.SystemLog.LogAppend)
}

func TestExtractAgentConfig_SystemLogNoArgs(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, agentConfig.Mongod.SystemLog)
}

func TestExtractAgentConfig_SystemLogAndLogRotate(t *testing.T) {
	processMap := map[string]map[string]interface{}{
		"host-0": {
			"args2_6": map[string]interface{}{
				"systemLog": map[string]interface{}{
					"destination": "syslog",
					"path":        "/dev/log",
				},
			},
			"logRotate": map[string]interface{}{
				"sizeThresholdMB":  500.0,
				"timeThresholdHrs": 24,
			},
		},
	}
	members := []interface{}{
		map[string]interface{}{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.SystemLog)
	assert.Equal(t, "syslog", string(agentConfig.Mongod.SystemLog.Destination))
	require.NotNil(t, agentConfig.Mongod.LogRotate)
	assert.Equal(t, "500", agentConfig.Mongod.LogRotate.SizeThresholdMB)
}

func TestExtractTLSMode(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		expected string
	}{
		{"tls mode", map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{"mode": "preferSSL"}}}, "preferSSL"},
		{"ssl mode", map[string]interface{}{"net": map[string]interface{}{"ssl": map[string]interface{}{"mode": "requireSSL"}}}, "requireSSL"},
		{"no net", map[string]interface{}{}, ""},
		{"empty tls", map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractTLSMode(tt.args))
		})
	}
}

func TestExtractPrometheusConfig_Enabled(t *testing.T) {
	deployment := om.Deployment{
		"prometheus": map[string]interface{}{
			"enabled":       true,
			"username":      "prom-user",
			"passwordHash":  "hash123",
			"passwordSalt":  "salt456",
			"scheme":        "https",
			"listenAddress": "0.0.0.0:9216",
			"metricsPath":   "/metrics",
			"tlsPemPath":    "/etc/ssl/prom.pem",
		},
	}

	prom, err := extractPrometheusConfig(deployment)
	require.NoError(t, err)
	require.NotNil(t, prom)
	assert.Equal(t, "prom-user", prom.Username)
	assert.Equal(t, 9216, prom.Port)
	assert.Equal(t, "prometheus-password", prom.PasswordSecretRef.Name)
	assert.Equal(t, "prometheus-tls", prom.TLSSecretRef.Name)
}

func TestExtractPrometheusConfig_CustomPort(t *testing.T) {
	deployment := om.Deployment{
		"prometheus": map[string]interface{}{
			"enabled":       true,
			"username":      "prom-user",
			"scheme":        "http",
			"listenAddress": "0.0.0.0:9999",
			"metricsPath":   "/custom-metrics",
		},
	}

	prom, err := extractPrometheusConfig(deployment)
	require.NoError(t, err)
	require.NotNil(t, prom)
	assert.Equal(t, 9999, prom.Port)
	assert.Equal(t, "/custom-metrics", prom.MetricsPath)
	assert.Empty(t, prom.TLSSecretRef.Name)
}

func TestExtractPrometheusConfig_Disabled(t *testing.T) {
	deployment := om.Deployment{
		"prometheus": map[string]interface{}{
			"enabled": false,
		},
	}
	prom, err := extractPrometheusConfig(deployment)
	require.NoError(t, err)
	assert.Nil(t, prom)
}

func TestExtractPrometheusConfig_Missing(t *testing.T) {
	deployment := om.Deployment{}
	prom, err := extractPrometheusConfig(deployment)
	require.NoError(t, err)
	assert.Nil(t, prom)
}

func TestExtractPrometheusConfig_MalformedNotMap(t *testing.T) {
	deployment := om.Deployment{
		"prometheus": "not-a-map",
	}
	_, err := extractPrometheusConfig(deployment)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid map")
}

func TestExtractPrometheusConfig_EnabledNoUsername(t *testing.T) {
	deployment := om.Deployment{
		"prometheus": map[string]interface{}{
			"enabled":       true,
			"listenAddress": "0.0.0.0:9216",
		},
	}
	_, err := extractPrometheusConfig(deployment)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no username configured")
}

func TestExtractPrometheusConfig_InvalidListenAddress(t *testing.T) {
	deployment := om.Deployment{
		"prometheus": map[string]interface{}{
			"enabled":       true,
			"username":      "prom-user",
			"listenAddress": "invalid-no-port",
		},
	}
	_, err := extractPrometheusConfig(deployment)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "valid port")
}

func TestParsePortFromListenAddress(t *testing.T) {
	tests := []struct {
		addr     string
		expected int
	}{
		{"0.0.0.0:9216", 9216},
		{"0.0.0.0:9999", 9999},
		{":8080", 8080},
		{"9216", 9216},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			assert.Equal(t, tt.expected, parsePortFromListenAddress(tt.addr))
		})
	}
}

func TestExtractCustomRoles(t *testing.T) {
	deployment := om.Deployment{
		"roles": []interface{}{
			map[string]interface{}{
				"role": "appReadOnly",
				"db":   "myapp",
				"privileges": []interface{}{
					map[string]interface{}{
						"actions":  []interface{}{"find", "listCollections"},
						"resource": map[string]interface{}{"db": "myapp", "collection": ""},
					},
				},
				"roles": []interface{}{
					map[string]interface{}{"role": "read", "db": "myapp"},
				},
			},
		},
	}

	roles := extractCustomRoles(deployment)
	require.Len(t, roles, 1)
	assert.Equal(t, "appReadOnly", roles[0].Role)
	assert.Equal(t, "myapp", roles[0].Db)
	require.Len(t, roles[0].Privileges, 1)
	assert.Contains(t, roles[0].Privileges[0].Actions, "find")
	require.Len(t, roles[0].Roles, 1)
	assert.Equal(t, "read", roles[0].Roles[0].Role)
}

func TestExtractCustomRoles_Empty(t *testing.T) {
	deployment := om.Deployment{
		"roles": []interface{}{},
	}
	roles := extractCustomRoles(deployment)
	assert.Nil(t, roles)
}

func strPtr(s string) *string {
	return &s
}
