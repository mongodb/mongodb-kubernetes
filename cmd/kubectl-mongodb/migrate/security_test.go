package migrate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
	pkgtls "github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
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
		{"empty defaults to require", "", true},
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
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{}

	result, err := buildSecurity(nil, processMap, members, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestBuildSecurity_AuthDisabled(t *testing.T) {
	auth := &om.Auth{Disabled: true}
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{}

	result, err := buildSecurity(auth, processMap, members, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestBuildSecurity_AuthEnabled(t *testing.T) {
	auth := &om.Auth{
		Disabled:                 false,
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"},
	}
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{}

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
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"tls": map[string]interface{}{"mode": "requireTLS"},
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
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
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"security": map[string]interface{}{
					"clusterAuthMode": "x509",
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	result, err := buildSecurity(auth, processMap, members, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Authentication)
	assert.Equal(t, "X509", result.Authentication.InternalCluster)
}


func TestBuildSecurity_MissingProcess(t *testing.T) {
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{
		{"host": "missing-host"},
	}

	_, err := buildSecurity(nil, processMap, members, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractAdditionalMongodConfig_NonDefaultPort(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27018,
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
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
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_WiredTigerCache(t *testing.T) {
	processMap := map[string]om.Process{
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
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
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
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	_, err := extractAdditionalMongodConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no args2_6 configuration")
}

func TestExtractAdditionalMongodConfig_NoMembers(t *testing.T) {
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}


func TestExtractAdditionalMongodConfig_MissingProcess(t *testing.T) {
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{
		{"host": "missing-host"},
	}

	_, err := extractAdditionalMongodConfig(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractAdditionalMongodConfig_SetParameter(t *testing.T) {
	processMap := map[string]om.Process{
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
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
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
	processMap := map[string]om.Process{
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
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
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
	processMap := map[string]om.Process{
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
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
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
			processMap := map[string]om.Process{
				"host-0": {
					"args2_6": map[string]interface{}{
						"security": map[string]interface{}{
							"clusterAuthMode": tt.mode,
						},
					},
				},
			}
			members := []om.ReplicaSetMember{
				{"host": "host-0"},
			}
			result, err := extractInternalClusterAuthMode(processMap, members)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractInternalClusterAuthMode_KeyFileNotSupported(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"security": map[string]interface{}{
					"clusterAuthMode": "keyFile",
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	_, err := extractInternalClusterAuthMode(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported by the operator")
	assert.Contains(t, err.Error(), "keyFile")
}

func TestExtractInternalClusterAuthMode_UnsupportedMode(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"security": map[string]interface{}{
					"clusterAuthMode": "unsupported-mode",
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	_, err := extractInternalClusterAuthMode(processMap, members)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported clusterAuthMode")
}


func TestExtractInternalClusterAuthMode_MissingProcess(t *testing.T) {
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{
		{"host": "missing-host"},
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
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net":     map[string]interface{}{"port": 27017},
				"storage": map[string]interface{}{"dbPath": "/data/custom"},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "dbPath should not be extracted into additionalMongodConfig because the operator always overwrites it")
}

func TestExtractAdditionalMongodConfig_DefaultDbPath(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net":     map[string]interface{}{"port": 27017},
				"storage": map[string]interface{}{"dbPath": "/data"},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_SystemLogNotExtracted(t *testing.T) {
	processMap := map[string]om.Process{
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
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "systemLog should not be extracted into additionalMongodConfig because the operator always overwrites it; use spec.agent.mongod.systemLog instead")
}

func TestExtractAdditionalMongodConfig_TLSModePrefer(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
					"tls":  map[string]interface{}{"mode": "preferSSL"},
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
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
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"port": 27017,
					"tls":  map[string]interface{}{"mode": "requireSSL"},
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAgentConfig_LogRotateFromEndpoint(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	projectProcessConfigs := &ProjectProcessConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 24,
				NumUncompressed:  5,
			},
			SizeThresholdMB: 1000,
		},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, projectProcessConfigs)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.LogRotate)
	assert.Equal(t, "1000", agentConfig.Mongod.LogRotate.SizeThresholdMB)
	assert.Equal(t, 24, agentConfig.Mongod.LogRotate.TimeThresholdHrs)
	assert.Equal(t, 5, agentConfig.Mongod.LogRotate.NumUncompressed)
}

func TestExtractAgentConfig_AuditLogRotateFromEndpoint(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	projectProcessConfigs := &ProjectProcessConfigs{
		AuditLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 48,
			},
			SizeThresholdMB: 500,
		},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, projectProcessConfigs)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.AuditLogRotate)
	assert.Equal(t, "500", agentConfig.Mongod.AuditLogRotate.SizeThresholdMB)
	assert.Equal(t, 48, agentConfig.Mongod.AuditLogRotate.TimeThresholdHrs)
}

func TestExtractAgentConfig_NilAgentConfigs(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, agentConfig.Mongod.LogRotate)
	assert.Nil(t, agentConfig.Mongod.AuditLogRotate)
	assert.Nil(t, agentConfig.MonitoringAgent.LogRotate)
}

func TestExtractAgentConfig_MissingProcess(t *testing.T) {
	processMap := map[string]om.Process{}
	members := []om.ReplicaSetMember{
		{"host": "missing-host"},
	}

	_, err := extractAgentConfig(processMap, members, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractAgentConfig_EmptyEndpointData(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	projectProcessConfigs := &ProjectProcessConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{},
		AuditLogRotate:  &automationconfig.AcLogRotate{},
	}

	cfg, err := extractAgentConfig(processMap, members, nil, projectProcessConfigs)
	assert.NoError(t, err)
	assert.Nil(t, cfg.Mongod.LogRotate)
	assert.Nil(t, cfg.Mongod.AuditLogRotate)
}

func TestExtractAgentConfig_SystemLog(t *testing.T) {
	processMap := map[string]om.Process{
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
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.SystemLog)
	assert.Equal(t, "file", string(agentConfig.Mongod.SystemLog.Destination))
	assert.Equal(t, "/var/log/mongodb/mongod.log", agentConfig.Mongod.SystemLog.Path)
	assert.True(t, agentConfig.Mongod.SystemLog.LogAppend)
}

func TestExtractAgentConfig_SystemLogNoArgs(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, agentConfig.Mongod.SystemLog)
}

func TestExtractAgentConfig_SystemLogAndLogRotate(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"systemLog": map[string]interface{}{
					"destination": "syslog",
					"path":        "/dev/log",
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	projectProcessConfigs := &ProjectProcessConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 24,
			},
			SizeThresholdMB: 500,
		},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, projectProcessConfigs)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.Mongod.SystemLog)
	assert.Equal(t, "syslog", string(agentConfig.Mongod.SystemLog.Destination))
	require.NotNil(t, agentConfig.Mongod.LogRotate)
	assert.Equal(t, "500", agentConfig.Mongod.LogRotate.SizeThresholdMB)
}

func TestExtractAgentConfig_SystemLogIntersectsAcrossMembers(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"systemLog": map[string]interface{}{
					"destination": "file",
					"path":        "/var/log/mongod0.log",
					"logAppend":   true,
				},
			},
		},
		"host-1": {
			"args2_6": map[string]interface{}{
				"systemLog": map[string]interface{}{
					"destination": "file",
					"path":        "/var/log/mongod1.log",
					"logAppend":   true,
				},
			},
		},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
		{"host": "host-1"},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, nil)
	require.NoError(t, err)

	require.NotNil(t, agentConfig.Mongod.SystemLog)
	assert.Equal(t, "file", string(agentConfig.Mongod.SystemLog.Destination), "destination should be kept (same)")
	assert.Empty(t, agentConfig.Mongod.SystemLog.Path, "path should be dropped (differs)")
	assert.True(t, agentConfig.Mongod.SystemLog.LogAppend, "logAppend should be kept (same)")
}

func TestExtractAgentConfig_MonitoringLogRotateFromEndpoint(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	projectAgentConfigs := &ProjectAgentConfigs{
		MonitoringConfig: &om.MonitoringAgentConfig{
			MonitoringAgentTemplate: &om.MonitoringAgentTemplate{},
			BackingMap: map[string]interface{}{
				"logRotate": map[string]interface{}{
					"sizeThresholdMB":  500.0,
					"timeThresholdHrs": 12,
				},
			},
		},
	}

	agentConfig, err := extractAgentConfig(processMap, members, projectAgentConfigs, nil)
	require.NoError(t, err)
	require.NotNil(t, agentConfig.MonitoringAgent.LogRotate)
	assert.Equal(t, 500, agentConfig.MonitoringAgent.LogRotate.SizeThresholdMB)
	assert.Equal(t, 12, agentConfig.MonitoringAgent.LogRotate.TimeThresholdHrs)
}

func TestExtractAgentConfig_MonitoringLogRotateNil(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	agentConfig, err := extractAgentConfig(processMap, members, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, agentConfig.MonitoringAgent.LogRotate)
}

func TestExtractAgentConfig_MonitoringLogRotateEmpty(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}
	projectAgentConfigs := &ProjectAgentConfigs{
		MonitoringConfig: &om.MonitoringAgentConfig{
			MonitoringAgentTemplate: &om.MonitoringAgentTemplate{},
			BackingMap:              map[string]interface{}{},
		},
	}

	agentConfig, err := extractAgentConfig(processMap, members, projectAgentConfigs, nil)
	require.NoError(t, err)
	assert.Nil(t, agentConfig.MonitoringAgent.LogRotate)
}

func TestGetTLSModeFromMongodConfig(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		expected pkgtls.Mode
	}{
		{"tls mode", map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{"mode": "preferSSL"}}}, "preferSSL"},
		{"ssl mode", map[string]interface{}{"net": map[string]interface{}{"ssl": map[string]interface{}{"mode": "requireSSL"}}}, "requireSSL"},
		{"no net defaults to require", map[string]interface{}{}, pkgtls.Require},
		{"empty tls defaults to require", map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{}}}, pkgtls.Require},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, pkgtls.GetTLSModeFromMongodConfig(tt.args))
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
	assert.Panics(t, func() {
		_, _ = extractPrometheusConfig(deployment)
	})
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

// --- Multi-member intersection tests ---
// These tests verify that extractAdditionalMongodConfig only includes fields
// that are identical across all members. Each test covers a specific field
// from the extraction functions (extractNetConfig, extractStorageConfig,
// extractReplicationConfig, extractGenericSections, extractNonDefaultTLSMode).

func TestExtractAdditionalMongodConfig_MultiMember_SamePort_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27018}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27018}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, 27018, maputil.ReadMapValueAsInt(config.ToMap(), "net", "port"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentPort_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27018}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27019}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameCompressors_Included(t *testing.T) {
	compressors := []interface{}{"snappy", "zstd"}
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"compression": map[string]interface{}{"compressors": compressors}}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"compression": map[string]interface{}{"compressors": compressors}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.NotNil(t, maputil.ReadMapValueAsInterface(config.ToMap(), "net", "compression", "compressors"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentCompressors_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"compression": map[string]interface{}{"compressors": []interface{}{"snappy"}}}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"compression": map[string]interface{}{"compressors": []interface{}{"zstd"}}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameMaxConns_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"maxIncomingConnections": 500}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"maxIncomingConnections": 500}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, 500, maputil.ReadMapValueAsInt(config.ToMap(), "net", "maxIncomingConnections"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentMaxConns_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"maxIncomingConnections": 500}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"maxIncomingConnections": 1000}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameStorageEngine_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"engine": "inMemory"}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"engine": "inMemory"}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "inMemory", maputil.ReadMapValueAsString(config.ToMap(), "storage", "engine"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentStorageEngine_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"engine": "inMemory"}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"dbPath": "/data"}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "storage.engine differs (inMemory vs default wiredTiger)")
}

func TestExtractAdditionalMongodConfig_MultiMember_SameDirectoryPerDB_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"directoryPerDB": true}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"directoryPerDB": true}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, true, maputil.ReadMapValueAsInterface(config.ToMap(), "storage", "directoryPerDB"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentDirectoryPerDB_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"directoryPerDB": true}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"directoryPerDB": false}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameJournalEnabled_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"journal": map[string]interface{}{"enabled": true}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"journal": map[string]interface{}{"enabled": true}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, true, maputil.ReadMapValueAsInterface(config.ToMap(), "storage", "journal", "enabled"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentJournalEnabled_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"journal": map[string]interface{}{"enabled": true}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"journal": map[string]interface{}{"enabled": false}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameCacheSizeGB_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"cacheSizeGB": 2.0}}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"cacheSizeGB": 2.0}}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, 2.0, maputil.ReadMapValueAsInterface(config.ToMap(), "storage", "wiredTiger", "engineConfig", "cacheSizeGB"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentCacheSizeGB_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"cacheSizeGB": 2.0}}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"cacheSizeGB": 4.0}}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameJournalCompressor_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"journalCompressor": "zlib"}}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"journalCompressor": "zlib"}}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "zlib", maputil.ReadMapValueAsInterface(config.ToMap(), "storage", "wiredTiger", "engineConfig", "journalCompressor"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentJournalCompressor_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"journalCompressor": "zlib"}}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"journalCompressor": "snappy"}}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameBlockCompressor_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"collectionConfig": map[string]interface{}{"blockCompressor": "zstd"}}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"collectionConfig": map[string]interface{}{"blockCompressor": "zstd"}}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "zstd", maputil.ReadMapValueAsInterface(config.ToMap(), "storage", "wiredTiger", "collectionConfig", "blockCompressor"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentBlockCompressor_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"collectionConfig": map[string]interface{}{"blockCompressor": "zstd"}}}}},
		"host-1": {"args2_6": map[string]interface{}{"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"collectionConfig": map[string]interface{}{"blockCompressor": "snappy"}}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameOplogSizeMB_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"replication": map[string]interface{}{"oplogSizeMB": 2048}}},
		"host-1": {"args2_6": map[string]interface{}{"replication": map[string]interface{}{"oplogSizeMB": 2048}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, 2048, maputil.ReadMapValueAsInterface(config.ToMap(), "replication", "oplogSizeMB"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentOplogSizeMB_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"replication": map[string]interface{}{"oplogSizeMB": 2048}}},
		"host-1": {"args2_6": map[string]interface{}{"replication": map[string]interface{}{"oplogSizeMB": 4096}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameSetParameter_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"setParameter": map[string]interface{}{"authenticationMechanisms": "SCRAM-SHA-256"}}},
		"host-1": {"args2_6": map[string]interface{}{"setParameter": map[string]interface{}{"authenticationMechanisms": "SCRAM-SHA-256"}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "SCRAM-SHA-256", maputil.ReadMapValueAsInterface(config.ToMap(), "setParameter", "authenticationMechanisms"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentSetParameter_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"setParameter": map[string]interface{}{"authenticationMechanisms": "SCRAM-SHA-256"}}},
		"host-1": {"args2_6": map[string]interface{}{"setParameter": map[string]interface{}{"authenticationMechanisms": "SCRAM-SHA-1"}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameAuditLog_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"auditLog": map[string]interface{}{"destination": "file", "format": "JSON"}}},
		"host-1": {"args2_6": map[string]interface{}{"auditLog": map[string]interface{}{"destination": "file", "format": "JSON"}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	assert.Equal(t, "file", maputil.ReadMapValueAsInterface(m, "auditLog", "destination"))
	assert.Equal(t, "JSON", maputil.ReadMapValueAsInterface(m, "auditLog", "format"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentAuditLogFormat_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"auditLog": map[string]interface{}{"destination": "file", "format": "JSON"}}},
		"host-1": {"args2_6": map[string]interface{}{"auditLog": map[string]interface{}{"destination": "file", "format": "BSON"}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config, "auditLog.destination matches so it should be kept")
	m := config.ToMap()
	assert.Equal(t, "file", maputil.ReadMapValueAsInterface(m, "auditLog", "destination"))
	assert.Nil(t, maputil.ReadMapValueAsInterface(m, "auditLog", "format"), "format differs and should be excluded")
}

func TestExtractAdditionalMongodConfig_MultiMember_SameOperationProfiling_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"operationProfiling": map[string]interface{}{"mode": "slowOp", "slowOpThresholdMs": 200}}},
		"host-1": {"args2_6": map[string]interface{}{"operationProfiling": map[string]interface{}{"mode": "slowOp", "slowOpThresholdMs": 200}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	assert.Equal(t, "slowOp", maputil.ReadMapValueAsInterface(m, "operationProfiling", "mode"))
	assert.Equal(t, 200, maputil.ReadMapValueAsInterface(m, "operationProfiling", "slowOpThresholdMs"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentOperationProfiling_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"operationProfiling": map[string]interface{}{"mode": "slowOp"}}},
		"host-1": {"args2_6": map[string]interface{}{"operationProfiling": map[string]interface{}{"mode": "all"}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_SameTLSMode_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{"mode": "preferSSL"}}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{"mode": "preferSSL"}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "preferSSL", maputil.ReadMapValueAsInterface(config.ToMap(), "net", "tls", "mode"))
}

func TestExtractAdditionalMongodConfig_MultiMember_DifferentTLSMode_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{"mode": "preferSSL"}}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"tls": map[string]interface{}{"mode": "allowTLS"}}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_MultiMember_FieldPresentOnlyInOne_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{
			"storage": map[string]interface{}{"engine": "inMemory"},
			"net":     map[string]interface{}{"port": 27017},
		}},
		"host-1": {"args2_6": map[string]interface{}{
			"net": map[string]interface{}{"port": 27017},
		}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "storage.engine is only on host-0, net.port is default 27017, nothing should be included")
}

func TestExtractAdditionalMongodConfig_MultiMember_MixedSameAndDifferent(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{
			"net":     map[string]interface{}{"port": 27018, "maxIncomingConnections": 500},
			"storage": map[string]interface{}{"engine": "inMemory"},
		}},
		"host-1": {"args2_6": map[string]interface{}{
			"net":     map[string]interface{}{"port": 27018, "maxIncomingConnections": 1000},
			"storage": map[string]interface{}{"dbPath": "/data"},
		}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config, "net.port matches so should be included")
	m := config.ToMap()

	assert.Equal(t, 27018, maputil.ReadMapValueAsInt(m, "net", "port"), "same port should be kept")
	assert.Nil(t, maputil.ReadMapValueAsInterface(m, "net", "maxIncomingConnections"), "different maxIncomingConnections should be excluded")
	assert.Nil(t, maputil.ReadMapValueAsInterface(m, "storage", "engine"), "engine differs (inMemory vs absent) should be excluded")
}

func TestExtractAdditionalMongodConfig_ThreeMembers_AllSame_Included(t *testing.T) {
	args := map[string]interface{}{
		"net":     map[string]interface{}{"port": 27018},
		"storage": map[string]interface{}{"wiredTiger": map[string]interface{}{"engineConfig": map[string]interface{}{"cacheSizeGB": 4.0}}},
	}
	processMap := map[string]om.Process{
		"host-0": {"args2_6": args},
		"host-1": {"args2_6": args},
		"host-2": {"args2_6": args},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}, {"host": "host-2"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()
	assert.Equal(t, 27018, maputil.ReadMapValueAsInt(m, "net", "port"))
	assert.Equal(t, 4.0, maputil.ReadMapValueAsInterface(m, "storage", "wiredTiger", "engineConfig", "cacheSizeGB"))
}

func TestExtractAdditionalMongodConfig_ThreeMembers_OneDiffers_Excluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27018}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27018}}},
		"host-2": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27019}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}, {"host": "host-2"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "host-2 has a different port so net.port should be excluded")
}

func TestExtractAdditionalMongodConfig_MultiMember_AllFieldsSame_KitchenSink(t *testing.T) {
	args := map[string]interface{}{
		"net": map[string]interface{}{
			"port":                  27018,
			"maxIncomingConnections": 500,
			"compression":           map[string]interface{}{"compressors": []interface{}{"snappy", "zstd"}},
			"tls":                   map[string]interface{}{"mode": "preferSSL"},
		},
		"storage": map[string]interface{}{
			"engine":         "inMemory",
			"directoryPerDB": true,
			"journal":        map[string]interface{}{"enabled": true},
			"wiredTiger": map[string]interface{}{
				"engineConfig":     map[string]interface{}{"cacheSizeGB": 2.0, "journalCompressor": "zlib"},
				"collectionConfig": map[string]interface{}{"blockCompressor": "zstd"},
			},
		},
		"replication":        map[string]interface{}{"oplogSizeMB": 2048},
		"setParameter":       map[string]interface{}{"authenticationMechanisms": "SCRAM-SHA-256"},
		"auditLog":           map[string]interface{}{"destination": "file", "format": "JSON"},
		"operationProfiling": map[string]interface{}{"mode": "slowOp", "slowOpThresholdMs": 200},
	}
	processMap := map[string]om.Process{
		"host-0": {"args2_6": args},
		"host-1": {"args2_6": args},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	require.NotNil(t, config)
	m := config.ToMap()

	assert.Equal(t, 27018, maputil.ReadMapValueAsInt(m, "net", "port"))
	assert.Equal(t, 500, maputil.ReadMapValueAsInt(m, "net", "maxIncomingConnections"))
	assert.NotNil(t, maputil.ReadMapValueAsInterface(m, "net", "compression", "compressors"))
	assert.Equal(t, "preferSSL", maputil.ReadMapValueAsInterface(m, "net", "tls", "mode"))
	assert.Equal(t, "inMemory", maputil.ReadMapValueAsString(m, "storage", "engine"))
	assert.Equal(t, true, maputil.ReadMapValueAsInterface(m, "storage", "directoryPerDB"))
	assert.Equal(t, true, maputil.ReadMapValueAsInterface(m, "storage", "journal", "enabled"))
	assert.Equal(t, 2.0, maputil.ReadMapValueAsInterface(m, "storage", "wiredTiger", "engineConfig", "cacheSizeGB"))
	assert.Equal(t, "zlib", maputil.ReadMapValueAsInterface(m, "storage", "wiredTiger", "engineConfig", "journalCompressor"))
	assert.Equal(t, "zstd", maputil.ReadMapValueAsInterface(m, "storage", "wiredTiger", "collectionConfig", "blockCompressor"))
	assert.Equal(t, 2048, maputil.ReadMapValueAsInterface(m, "replication", "oplogSizeMB"))
	assert.Equal(t, "SCRAM-SHA-256", maputil.ReadMapValueAsInterface(m, "setParameter", "authenticationMechanisms"))
	assert.Equal(t, "file", maputil.ReadMapValueAsInterface(m, "auditLog", "destination"))
	assert.Equal(t, "JSON", maputil.ReadMapValueAsInterface(m, "auditLog", "format"))
	assert.Equal(t, "slowOp", maputil.ReadMapValueAsInterface(m, "operationProfiling", "mode"))
	assert.Equal(t, 200, maputil.ReadMapValueAsInterface(m, "operationProfiling", "slowOpThresholdMs"))
}

func TestExtractAdditionalMongodConfig_MultiMember_AllFieldsDifferent_AllExcluded(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{
			"net": map[string]interface{}{
				"port":                  27018,
				"maxIncomingConnections": 500,
				"compression":           map[string]interface{}{"compressors": []interface{}{"snappy"}},
				"tls":                   map[string]interface{}{"mode": "preferSSL"},
			},
			"storage": map[string]interface{}{
				"engine":         "inMemory",
				"directoryPerDB": true,
				"journal":        map[string]interface{}{"enabled": true},
				"wiredTiger": map[string]interface{}{
					"engineConfig":     map[string]interface{}{"cacheSizeGB": 2.0, "journalCompressor": "zlib"},
					"collectionConfig": map[string]interface{}{"blockCompressor": "zstd"},
				},
			},
			"replication":        map[string]interface{}{"oplogSizeMB": 2048},
			"setParameter":       map[string]interface{}{"authenticationMechanisms": "SCRAM-SHA-256"},
			"auditLog":           map[string]interface{}{"destination": "file", "format": "JSON"},
			"operationProfiling": map[string]interface{}{"mode": "slowOp", "slowOpThresholdMs": 200},
		}},
		"host-1": {"args2_6": map[string]interface{}{
			"net": map[string]interface{}{
				"port":                  27019,
				"maxIncomingConnections": 1000,
				"compression":           map[string]interface{}{"compressors": []interface{}{"zstd"}},
				"tls":                   map[string]interface{}{"mode": "allowTLS"},
			},
			"storage": map[string]interface{}{
				"directoryPerDB": false,
				"journal":        map[string]interface{}{"enabled": false},
				"wiredTiger": map[string]interface{}{
					"engineConfig":     map[string]interface{}{"cacheSizeGB": 4.0, "journalCompressor": "snappy"},
					"collectionConfig": map[string]interface{}{"blockCompressor": "snappy"},
				},
			},
			"replication":        map[string]interface{}{"oplogSizeMB": 4096},
			"setParameter":       map[string]interface{}{"authenticationMechanisms": "SCRAM-SHA-1"},
			"auditLog":           map[string]interface{}{"destination": "syslog", "format": "BSON"},
			"operationProfiling": map[string]interface{}{"mode": "all", "slowOpThresholdMs": 100},
		}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config, err := extractAdditionalMongodConfig(processMap, members)
	require.NoError(t, err)
	assert.Nil(t, config, "every field differs so nothing should be included")
}

func strPtr(s string) *string {
	return &s
}
