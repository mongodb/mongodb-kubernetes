package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	authn "github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
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
			expected: []mdbv1.AuthMode{"SCRAM"},
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
			expected: []mdbv1.AuthMode{"SCRAM", "X509"},
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
	assert.Contains(t, err.Error(), "unsupported authentication mechanism")
	assert.Contains(t, err.Error(), "UNKNOWN-MECH")
}

func TestBuildAuthModes_MergesAutoAndDeploymentMechanisms(t *testing.T) {
	auth := &om.Auth{
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"},
		AutoAuthMechanisms:       []string{"MONGODB-X509"},
	}
	modes, err := buildAuthModes(auth)
	require.NoError(t, err)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM", "X509"}, modes)
}

func TestBuildAuthModes_DeduplicatesMechanisms(t *testing.T) {
	auth := &om.Auth{
		DeploymentAuthMechanisms: []string{"SCRAM-SHA-256", "MONGODB-X509"},
		AutoAuthMechanisms:       []string{"SCRAM-SHA-256"},
	}
	modes, err := buildAuthModes(auth)
	require.NoError(t, err)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM", "X509"}, modes)
}

func TestBuildAuthModes_AutoOnlyMechanisms(t *testing.T) {
	auth := &om.Auth{
		AutoAuthMechanisms: []string{"SCRAM-SHA-256"},
	}
	modes, err := buildAuthModes(auth)
	require.NoError(t, err)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM"}, modes)
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
		expected string
		ok       bool
	}{
		{"MONGODB-CR", "MONGODB-CR", true},
		{"SCRAM-SHA-256", "SCRAM", true},
		{"SCRAM-SHA-1", "SCRAM-SHA-1", true},
		{"MONGODB-X509", "X509", true},
		{"PLAIN", "LDAP", true},
		{"MONGODB-OIDC", "OIDC", true},
		{"UNKNOWN", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.mech, func(t *testing.T) {
			mode, ok := authn.MapMechanismToAuthMode(tt.mech)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.expected, mode)
			}
		})
	}
}

// testAC builds a minimal AutomationConfig for buildSecurity tests.
func testAC(auth *om.Auth, processMap map[string]om.Process, members []om.ReplicaSetMember) *om.AutomationConfig {
	processes := make([]interface{}, 0, len(processMap))
	for name, proc := range processMap {
		proc["name"] = name
		processes = append(processes, map[string]interface{}(proc))
	}
	memberSlice := make([]interface{}, len(members))
	for i, m := range members {
		memberSlice[i] = map[string]interface{}(m)
	}
	var replicaSets []interface{}
	if len(members) > 0 {
		replicaSets = []interface{}{map[string]interface{}{"_id": "test-rs", "members": memberSlice}}
	}
	d := om.Deployment(map[string]interface{}{
		"processes":   processes,
		"replicaSets": replicaSets,
	})
	return &om.AutomationConfig{Auth: auth, Deployment: d}
}

func TestBuildSecurity_NilAuth(t *testing.T) {
	ac := testAC(nil, nil, nil)
	result, err := buildSecurity(ac, "", "")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestBuildSecurity_AuthDisabled(t *testing.T) {
	ac := testAC(&om.Auth{Disabled: true}, nil, nil)
	result, err := buildSecurity(ac, "", "")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestBuildSecurity_AuthEnabled(t *testing.T) {
	ac := testAC(
		&om.Auth{Disabled: false, DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"}},
		map[string]om.Process{"host-0": {}},
		[]om.ReplicaSetMember{{"host": "host-0"}},
	)
	result, err := buildSecurity(ac, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Authentication)
	assert.True(t, result.Authentication.Enabled)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM"}, result.Authentication.Modes)
}

func TestBuildSecurity_TLSAndAuth(t *testing.T) {
	ac := testAC(
		&om.Auth{Disabled: false, DeploymentAuthMechanisms: []string{"MONGODB-X509"}},
		map[string]om.Process{
			"host-0": {
				"args2_6": map[string]interface{}{
					"net": map[string]interface{}{
						"tls": map[string]interface{}{"mode": "requireTLS"},
					},
				},
			},
		},
		[]om.ReplicaSetMember{{"host": "host-0"}},
	)
	result, err := buildSecurity(ac, "mdb", "my-rs")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "mdb", result.CertificatesSecretsPrefix, "TLS should be enabled via certsSecretPrefix (deprecated tls.enabled)")
	assert.True(t, result.IsTLSEnabled())
	require.NotNil(t, result.TLSConfig)
	assert.Equal(t, "my-rs-ca", result.TLSConfig.CA, "TLS CA ConfigMap should default to <resourceName>-ca")
	require.NotNil(t, result.Authentication)
	assert.Equal(t, []mdbv1.AuthMode{"X509"}, result.Authentication.Modes)
}

// TestBuildSecurity_TLS_EmptyPrefix verifies that buildSecurity does not set TLS when certsSecretPrefix
// is empty, regardless of the process config. The responsibility for ensuring the prefix is set when TLS
// is detected lies with ensureTLS (which calls isTLSEnabled before buildSecurity is reached).
func TestBuildSecurity_TLS_EmptyPrefix(t *testing.T) {
	ac := testAC(nil, map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"tls": map[string]interface{}{"mode": "requireTLS"},
				},
			},
		},
	}, []om.ReplicaSetMember{{"host": "host-0"}})
	result, err := buildSecurity(ac, "", "")
	require.NoError(t, err)
	assert.Nil(t, result, "expected no security config when certsSecretPrefix is empty")
}

func TestBuildSecurity_InternalClusterAuth(t *testing.T) {
	ac := testAC(
		&om.Auth{Disabled: false, DeploymentAuthMechanisms: []string{"SCRAM-SHA-256"}},
		map[string]om.Process{
			"host-0": {
				"args2_6": map[string]interface{}{
					"security": map[string]interface{}{
						"clusterAuthMode": "x509",
					},
				},
			},
		},
		[]om.ReplicaSetMember{{"host": "host-0"}},
	)
	result, err := buildSecurity(ac, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Authentication)
	assert.Equal(t, "X509", result.Authentication.InternalCluster)
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	require.NotNil(t, config)
	m := config.ToMap()
	netMap, ok := m["net"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 27018, netMap["port"])
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	require.NotNil(t, config)
	m := config.ToMap()
	storage, ok := m["storage"].(map[string]interface{})
	require.True(t, ok)
	wt, ok := storage["wiredTiger"].(map[string]interface{})
	require.True(t, ok)
	ec, ok := wt["engineConfig"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 2.0, ec["cacheSizeGB"])
}

func TestExtractAdditionalMongodConfig_ZstdCompressionLevel(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{"port": 27017},
				"storage": map[string]interface{}{
					"wiredTiger": map[string]interface{}{
						"engineConfig": map[string]interface{}{
							"zstdCompressionLevel": 6,
						},
					},
				},
			},
		},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}}

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	require.NotNil(t, config)
	m := config.ToMap()
	storage, ok := m["storage"].(map[string]interface{})
	require.True(t, ok)
	wt, ok := storage["wiredTiger"].(map[string]interface{})
	require.True(t, ok)
	ec, ok := wt["engineConfig"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 6, ec["zstdCompressionLevel"])
}

func TestExtractAdditionalMongodConfig_WiredTigerConfigString(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]any{
				"net": map[string]any{"port": 27017},
				"storage": map[string]any{
					"wiredTiger": map[string]any{
						"engineConfig": map[string]any{
							"configString": "builtin_extension_config=(zlib=(compression_level=2))",
						},
						"collectionConfig": map[string]any{
							"configString": "block_compressor=zlib",
						},
					},
				},
			},
		},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}}

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	require.NotNil(t, config)
	m := config.ToMap()
	storage, ok := m["storage"].(map[string]any)
	require.True(t, ok)
	wt, ok := storage["wiredTiger"].(map[string]any)
	require.True(t, ok)
	ec, ok := wt["engineConfig"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "builtin_extension_config=(zlib=(compression_level=2))", ec["configString"])
	cc, ok := wt["collectionConfig"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "block_compressor=zlib", cc["configString"])
}

func TestExtractAdditionalMongodConfig_NoArgs(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	members := []om.ReplicaSetMember{
		{"host": "host-0"},
	}

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	assert.Nil(t, config)
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	require.NotNil(t, config)
	m := config.ToMap()
	repl, ok := m["replication"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 2048, repl["oplogSizeMB"])
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
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

func TestExtractInternalClusterAuthMode_KeyFileImplicit(t *testing.T) {
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
	result, err := extractInternalClusterAuthMode(processMap, members)
	require.NoError(t, err)
	assert.Equal(t, "", result, "keyFile is the default, so no explicit CR field is needed")
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

func TestConvertACLdapToCR(t *testing.T) {
	l := &ldap.Ldap{
		Servers:            "ldap.example.com:636",
		TransportSecurity:  "tls",
		BindQueryUser:      "cn=admin,dc=example,dc=com",
		AuthzQueryTemplate: "{USER}?memberOf?base",
		UserToDnMapping:    `[{"match":"(.+)","substitution":"cn={0},dc=example,dc=com"}]`,
		TimeoutMS:          10000,
	}

	cr := mdbv1.ConvertACLdapToCR(l)
	require.NotNil(t, cr)
	assert.Equal(t, []string{"ldap.example.com:636"}, cr.Servers)
	assert.Equal(t, mdbv1.TransportSecurity("tls"), *cr.TransportSecurity)
	assert.Equal(t, "cn=admin,dc=example,dc=com", cr.BindQueryUser)
	assert.Equal(t, "{USER}?memberOf?base", cr.AuthzQueryTemplate)
	assert.Equal(t, 10000, cr.TimeoutMS)
}

func TestConvertACLdapToCR_MultipleServers(t *testing.T) {
	l := &ldap.Ldap{
		Servers: "ldap1.example.com:636, ldap2.example.com:636,ldap3.example.com:636",
	}

	cr := mdbv1.ConvertACLdapToCR(l)
	require.NotNil(t, cr)
	assert.Equal(t, []string{"ldap1.example.com:636", "ldap2.example.com:636", "ldap3.example.com:636"}, cr.Servers)
}

func TestConvertACLdapToCR_NoBindUser(t *testing.T) {
	l := &ldap.Ldap{
		Servers: "ldap.example.com:636",
	}

	cr := mdbv1.ConvertACLdapToCR(l)
	require.NotNil(t, cr)
	assert.Empty(t, cr.BindQuerySecretRef.Name)
}

func TestConvertACLdapToCR_CaFileContents(t *testing.T) {
	l := &ldap.Ldap{
		Servers:        "ldap.example.com:636",
		CaFileContents: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
	}

	cr := mdbv1.ConvertACLdapToCR(l)
	require.NotNil(t, cr)
	assert.Nil(t, cr.CAConfigMapRef, "CAConfigMapRef is set by the caller, not by ConvertACLdapToCR")
}

func TestConvertACLdapToCR_NoCaFileContents(t *testing.T) {
	l := &ldap.Ldap{
		Servers: "ldap.example.com:636",
	}

	cr := mdbv1.ConvertACLdapToCR(l)
	require.NotNil(t, cr)
	assert.Nil(t, cr.CAConfigMapRef)
}

func TestMapACOIDCToProviderConfigs(t *testing.T) {
	configs := []oidc.ProviderConfig{
		{
			AuthNamePrefix:        "WORKFORCE",
			Audience:              "my-audience",
			IssuerUri:             "https://issuer.example.com",
			UserClaim:             "sub",
			SupportsHumanFlows:    true,
			UseAuthorizationClaim: false,
			ClientId:              strPtr("client-123"),
			RequestedScopes:       []string{"openid", "profile"},
		},
		{
			AuthNamePrefix:        "WORKLOAD",
			Audience:              "api-audience",
			IssuerUri:             "https://issuer.example.com",
			UserClaim:             "sub",
			SupportsHumanFlows:    false,
			UseAuthorizationClaim: true,
			GroupsClaim:           strPtr("groups"),
		},
	}

	result := authn.MapACOIDCToProviderConfigs(configs)
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	assert.Nil(t, config, "systemLog should not be extracted into additionalMongodConfig because the operator always overwrites it. Use spec.agent.mongod.systemLog instead")
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
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

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	assert.Nil(t, config)
}

func TestExtractAgentConfig_LogRotateFromEndpoint(t *testing.T) {
	projectProcessConfigs := &ProjectConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 24,
				NumUncompressed:  5,
			},
			SizeThresholdMB: 1000,
		},
	}

	agentConfig := extractAgentConfig(nil, projectProcessConfigs)
	require.NotNil(t, agentConfig.Mongod.LogRotate)
	assert.Equal(t, "1000", agentConfig.Mongod.LogRotate.SizeThresholdMB)
	assert.Equal(t, 24, agentConfig.Mongod.LogRotate.TimeThresholdHrs)
	assert.Equal(t, 5, agentConfig.Mongod.LogRotate.NumUncompressed)
}

func TestExtractAgentConfig_AuditLogRotateFromEndpoint(t *testing.T) {
	projectProcessConfigs := &ProjectConfigs{
		AuditLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 48,
			},
			SizeThresholdMB: 500,
		},
	}

	agentConfig := extractAgentConfig(nil, projectProcessConfigs)
	require.NotNil(t, agentConfig.Mongod.AuditLogRotate)
	assert.Equal(t, "500", agentConfig.Mongod.AuditLogRotate.SizeThresholdMB)
	assert.Equal(t, 48, agentConfig.Mongod.AuditLogRotate.TimeThresholdHrs)
}

func TestExtractAgentConfig_NilProcessConfigs(t *testing.T) {
	agentConfig := extractAgentConfig(nil, nil)
	assert.Nil(t, agentConfig.Mongod.LogRotate)
	assert.Nil(t, agentConfig.Mongod.AuditLogRotate)
	assert.Nil(t, agentConfig.MonitoringAgent.LogRotate)
	assert.Nil(t, agentConfig.BackupAgent.LogRotate)
}

func TestExtractAgentConfig_EmptyEndpointData(t *testing.T) {
	projectProcessConfigs := &ProjectConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{},
		AuditLogRotate:  &automationconfig.AcLogRotate{},
	}

	cfg := extractAgentConfig(nil, projectProcessConfigs)
	assert.Nil(t, cfg.Mongod.LogRotate)
	assert.Nil(t, cfg.Mongod.AuditLogRotate)
	assert.Nil(t, cfg.MonitoringAgent.LogRotate)
	assert.Nil(t, cfg.BackupAgent.LogRotate)
}

func TestExtractAgentConfig_AgentLogRotateMatchesMongod(t *testing.T) {
	projectProcessConfigs := &ProjectConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{
			LogRotate:       automationconfig.LogRotate{TimeThresholdHrs: 24},
			SizeThresholdMB: 1000,
		},
	}

	agentConfig := extractAgentConfig(nil, projectProcessConfigs)
	require.NotNil(t, agentConfig.MonitoringAgent.LogRotate)
	assert.Equal(t, 1000, agentConfig.MonitoringAgent.LogRotate.SizeThresholdMB)
	assert.Equal(t, 24, agentConfig.MonitoringAgent.LogRotate.TimeThresholdHrs)
	require.NotNil(t, agentConfig.BackupAgent.LogRotate)
	assert.Equal(t, 1000, agentConfig.BackupAgent.LogRotate.SizeThresholdMB)
	assert.Equal(t, 24, agentConfig.BackupAgent.LogRotate.TimeThresholdHrs)
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

	roles := deployment.GetRoles()
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
	roles := deployment.GetRoles()
	assert.Empty(t, roles)
}

func TestExtractAdditionalMongodConfig_MultiMember_SamePort_Included(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27018}}},
		"host-1": {"args2_6": map[string]interface{}{"net": map[string]interface{}{"port": 27018}}},
	}
	members := []om.ReplicaSetMember{{"host": "host-0"}, {"host": "host-1"}}

	config := sourceProc(processMap, members).AdditionalMongodConfig()
	require.NotNil(t, config)
	assert.Equal(t, 27018, maputil.ReadMapValueAsInt(config.ToMap(), "net", "port"))
}

func strPtr(s string) *string {
	return &s
}

func sourceProc(processMap map[string]om.Process, members []om.ReplicaSetMember) *om.Process {
	p := processMap[members[0].Name()]
	return &p
}

func TestDistributeMembers(t *testing.T) {
	clusters := []string{"east1", "west1", "central1"}
	result := distributeMembers(clusters)
	require.Len(t, result, 3)
	for i, item := range result {
		assert.Equal(t, clusters[i], item.ClusterName)
		assert.Equal(t, 0, item.Members, "cluster %s should have 0 initial members", clusters[i])
	}
}

func TestDistributeMembers_SingleCluster(t *testing.T) {
	result := distributeMembers([]string{"only"})
	require.Len(t, result, 1)
	assert.Equal(t, "only", result[0].ClusterName)
	assert.Equal(t, 0, result[0].Members)
}

func TestDistributeMembers_EmptyClusterNames(t *testing.T) {
	assert.Nil(t, distributeMembers(nil))
	assert.Nil(t, distributeMembers([]string{}))
}
