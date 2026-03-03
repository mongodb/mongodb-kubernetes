package migrate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func TestInferAuthModes_FromAutoAuthMechanisms(t *testing.T) {
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
		{
			name:     "unknown mechanism skipped",
			mechs:    []string{"UNKNOWN-MECH"},
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &om.Auth{AutoAuthMechanisms: tt.mechs}
			modes := inferAuthModes(auth)
			assert.Equal(t, tt.expected, modes)
		})
	}
}

func TestInferAuthModes_FallbackToAutoAuthMechanism(t *testing.T) {
	auth := &om.Auth{
		AutoAuthMechanisms: nil,
		AutoAuthMechanism:  "SCRAM-SHA-256",
	}
	modes := inferAuthModes(auth)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM-SHA-256"}, modes)
}

func TestInferAuthModes_FallbackX509(t *testing.T) {
	auth := &om.Auth{
		AutoAuthMechanisms: nil,
		AutoAuthMechanism:  "MONGODB-X509",
	}
	modes := inferAuthModes(auth)
	assert.Equal(t, []mdbv1.AuthMode{"X509"}, modes)
}

func TestInferAuthModes_FallbackNotUsedWhenMechanismsPresent(t *testing.T) {
	auth := &om.Auth{
		AutoAuthMechanisms: []string{"SCRAM-SHA-1"},
		AutoAuthMechanism:  "SCRAM-SHA-256",
	}
	modes := inferAuthModes(auth)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM-SHA-1"}, modes)
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
		{"allowSSL", "allowSSL", false},
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

func TestInferSecurity_NilAuth(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	result := inferSecurity(nil, processMap, members)
	assert.Nil(t, result)
}

func TestInferSecurity_AuthDisabled(t *testing.T) {
	auth := &om.Auth{Disabled: true}
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	result := inferSecurity(auth, processMap, members)
	assert.Nil(t, result)
}

func TestInferSecurity_AuthEnabled(t *testing.T) {
	auth := &om.Auth{
		Disabled:           false,
		AutoAuthMechanisms: []string{"SCRAM-SHA-256"},
	}
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	result := inferSecurity(auth, processMap, members)
	require.NotNil(t, result)
	require.NotNil(t, result.Authentication)
	assert.True(t, result.Authentication.Enabled)
	assert.Equal(t, []mdbv1.AuthMode{"SCRAM-SHA-256"}, result.Authentication.Modes)
}

func TestInferSecurity_TLSAndAuth(t *testing.T) {
	auth := &om.Auth{
		Disabled:           false,
		AutoAuthMechanisms: []string{"MONGODB-X509"},
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

	result := inferSecurity(auth, processMap, members)
	require.NotNil(t, result)
	require.NotNil(t, result.TLSConfig)
	assert.True(t, result.TLSConfig.Enabled)
	require.NotNil(t, result.Authentication)
	assert.Equal(t, []mdbv1.AuthMode{"X509"}, result.Authentication.Modes)
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

	config := extractAdditionalMongodConfig(processMap, members)
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

	config := extractAdditionalMongodConfig(processMap, members)
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

	config := extractAdditionalMongodConfig(processMap, members)
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

	config := extractAdditionalMongodConfig(processMap, members)
	assert.Nil(t, config)
}

func TestExtractAdditionalMongodConfig_NoMembers(t *testing.T) {
	processMap := map[string]map[string]interface{}{}
	members := []interface{}{}

	config := extractAdditionalMongodConfig(processMap, members)
	assert.Nil(t, config)
}
