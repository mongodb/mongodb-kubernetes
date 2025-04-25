package mdb

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

func TestEnsureSecurity_WithAllNilValues(t *testing.T) {
	spec := &MongoDbSpec{
		DbCommonSpec: DbCommonSpec{
			Security: nil,
		},
	}

	spec.Security = EnsureSecurity(spec.Security)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
}

func TestEnsureSecurity_WithNilTlsConfig(t *testing.T) {
	spec := &MongoDbSpec{DbCommonSpec: DbCommonSpec{Security: &Security{TLSConfig: nil, Authentication: &Authentication{}}}}
	spec.Security = EnsureSecurity(spec.Security)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
}

func TestEnsureSecurity_EmptySpec(t *testing.T) {
	spec := &MongoDbSpec{}
	spec.Security = EnsureSecurity(spec.Security)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
}

func TestGetAgentAuthentication(t *testing.T) {
	sec := newSecurity()
	sec.Authentication = newAuthentication()
	sec.Authentication.Agents.Mode = "SCRAM"
	assert.Len(t, sec.Authentication.Modes, 0)
	assert.Empty(t, sec.GetAgentMechanism(""))
	assert.Equal(t, "", sec.GetAgentMechanism("MONGODB-X509"))

	sec.Authentication.Enabled = true
	sec.Authentication.Modes = append(sec.Authentication.Modes, util.X509)
	assert.Equal(t, util.X509, sec.GetAgentMechanism("MONGODB-X509"), "if x509 was enabled before, it needs to stay as is")

	sec.Authentication.Modes = append(sec.Authentication.Modes, util.SCRAM)
	assert.Equal(t, util.SCRAM, sec.GetAgentMechanism("SCRAM-SHA-256"), "if scram was enabled, scram will be chosen")

	sec.Authentication.Modes = append(sec.Authentication.Modes, util.SCRAMSHA1)
	assert.Equal(t, util.SCRAM, sec.GetAgentMechanism("SCRAM-SHA-256"), "Adding SCRAM-SHA-1 doesn't change the default")

	sec.Authentication.Agents.Mode = "X509"
	assert.Equal(t, util.X509, sec.GetAgentMechanism("SCRAM-SHA-256"), "transitioning from SCRAM -> X509 is allowed")
}

func TestMinimumMajorVersion(t *testing.T) {
	mdbSpec := MongoDbSpec{
		DbCommonSpec: DbCommonSpec{
			Version:                     "3.6.0-ent",
			FeatureCompatibilityVersion: nil,
		},
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))

	mdbSpec = MongoDbSpec{
		DbCommonSpec: DbCommonSpec{
			Version:                     "4.0.0-ent",
			FeatureCompatibilityVersion: stringutil.Ref("3.6"),
		},
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))

	mdbSpec = MongoDbSpec{
		DbCommonSpec: DbCommonSpec{
			Version:                     "4.0.0",
			FeatureCompatibilityVersion: stringutil.Ref("3.6"),
		},
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))
}

func TestMongoDB_ConnectionURL_NotSecure(t *testing.T) {
	rs := NewReplicaSetBuilder().SetMembers(3).Build()

	var cnx string
	cnx = rs.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017,test-mdb-2.test-mdb-svc.testNS.svc.cluster.local:27017/"+
		"?connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)

	// Connection parameters. The default one is overridden
	cnx = rs.BuildConnectionString("", "", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"})
	assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017,test-mdb-2.test-mdb-svc.testNS.svc.cluster.local:27017/"+
		"?connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)

	// 2 members, custom cluster name
	rs = NewReplicaSetBuilder().SetName("paymentsDb").SetMembers(2).SetClusterDomain("company.domain.net").Build()
	cnx = rs.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://paymentsDb-0.paymentsDb-svc.testNS.svc.company.domain.net:27017,"+
		"paymentsDb-1.paymentsDb-svc.testNS.svc.company.domain.net:27017/?connectTimeoutMS=20000&replicaSet=paymentsDb"+
		"&serverSelectionTimeoutMS=20000",
		cnx)

	// Sharded cluster
	sc := NewClusterBuilder().SetName("contractsDb").SetNamespace("ns").Build()
	cnx = sc.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://contractsDb-mongos-0.contractsDb-svc.ns.svc.cluster.local:27017,"+
		"contractsDb-mongos-1.contractsDb-svc.ns.svc.cluster.local:27017/"+
		"?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000",
		cnx)

	// Standalone
	st := NewStandaloneBuilder().SetName("foo").Build()
	cnx = st.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://foo-0.foo-svc.testNS.svc.cluster.local:27017/?"+
		"connectTimeoutMS=20000&serverSelectionTimeoutMS=20000",
		cnx)
}

func TestMongoDB_ConnectionURL_Secure(t *testing.T) {
	var cnx string

	// Only tls enabled, no auth
	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()
	cnx = rs.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017,test-mdb-2.test-mdb-svc.testNS.svc.cluster.local:27017/?"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000&ssl=true",
		cnx)

	// New version of Mongodb -> SCRAM-SHA-256
	rs = NewReplicaSetBuilder().SetMembers(2).SetSecurityTLSEnabled().EnableAuth([]AuthMode{util.SCRAM}).Build()
	cnx = rs.BuildConnectionString("the_user", "the_passwd", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://the_user:the_passwd@test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000&ssl=true",
		cnx)

	// Old version of Mongodb -> SCRAM-SHA-1. X509 is a second authentication method - user & password are still appended
	rs = NewReplicaSetBuilder().SetMembers(2).SetVersion("3.6.1").EnableAuth([]AuthMode{util.SCRAM, util.X509}).Build()
	cnx = rs.BuildConnectionString("the_user", "the_passwd", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://the_user:the_passwd@test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-1&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)

	// Special symbols in user/password must be encoded
	rs = NewReplicaSetBuilder().SetMembers(2).EnableAuth([]AuthMode{util.SCRAM}).Build()
	cnx = rs.BuildConnectionString("user/@", "pwd#!@", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://user%2F%40:pwd%23%21%40@test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)

	// Caller can override any connection parameters, e.g."authMechanism"
	rs = NewReplicaSetBuilder().SetMembers(2).EnableAuth([]AuthMode{util.SCRAM}).Build()
	cnx = rs.BuildConnectionString("the_user", "the_passwd", connectionstring.SchemeMongoDB, map[string]string{"authMechanism": "SCRAM-SHA-1"})
	assert.Equal(t, "mongodb://the_user:the_passwd@test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-1&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)

	// X509 -> no user/password in the url. It's possible to pass user/password in the params though
	rs = NewReplicaSetBuilder().SetMembers(2).EnableAuth([]AuthMode{util.X509}).Build()
	cnx = rs.BuildConnectionString("the_user", "the_passwd", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?connectTimeoutMS=20000&replicaSet=test-mdb&"+
		"serverSelectionTimeoutMS=20000", cnx)

	// username + password must be provided if scram is enabled
	rs = NewReplicaSetBuilder().SetMembers(2).EnableAuth([]AuthMode{util.SCRAM}).Build()
	cnx = rs.BuildConnectionString("the_user", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)

	cnx = rs.BuildConnectionString("", "the_password", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)

	cnx = rs.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
		"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)
}

func TestMongoDBConnectionURLExternalDomainWithAuth(t *testing.T) {
	externalDomain := "example.com"

	rs := NewReplicaSetBuilder().SetMembers(2).EnableAuth([]AuthMode{util.SCRAM}).ExposedExternally(nil, nil, &externalDomain).Build()
	cnx := rs.BuildConnectionString("the_user", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://test-mdb-0.example.com:27017,"+
		"test-mdb-1.example.com:27017/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000",
		cnx)
}

func TestMongoDBConnectionURLMultiClusterSharded(t *testing.T) {
	sc := NewDefaultMultiShardedClusterBuilder().SetName("sharDB").Build()
	cb := &MongoDBConnectionStringBuilder{
		MongoDB: *sc,
		hostnames: []string{
			"sharDB-mongos-0-0-svc.testNS.svc.cluster.local",
			"sharDB-mongos-0-1-svc.testNS.svc.cluster.local",
			"sharDB-mongos-1-0-svc.testNS.svc.cluster.local",
			"sharDB-mongos-1-1-svc.testNS.svc.cluster.local",
			"sharDB-mongos-1-2-svc.testNS.svc.cluster.local",
		},
	}

	cs := cb.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://sharDB-mongos-0-0-svc.testNS.svc.cluster.local,"+
		"sharDB-mongos-0-1-svc.testNS.svc.cluster.local,"+
		"sharDB-mongos-1-0-svc.testNS.svc.cluster.local,sharDB-mongos-1-1-svc.testNS.svc.cluster.local,"+
		"sharDB-mongos-1-2-svc.testNS.svc.cluster.local/?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000", cs)
}

func TestMongoDB_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDB{}
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.Warnings)
}

func TestMongoDB_IsSecurityTLSConfigEnabled(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	tests := []struct {
		name     string
		security *Security
		expected bool
	}{
		{
			name:     "TLS is not enabled when Security is nil",
			security: nil,
			expected: false,
		},
		{
			name:     "TLS is not enabled when TLSConfig is nil",
			security: &Security{},
			expected: false,
		},
		{
			name: "TLS is enabled when CertificatesSecretsPrefix is specified",
			security: &Security{
				CertificatesSecretsPrefix: "prefix",
			},
			expected: true,
		},
		{
			name: "TLS is enabled when TLSConfig fields are specified and CertificatesSecretsPrefix is specified",
			security: &Security{
				CertificatesSecretsPrefix: "prefix",
				TLSConfig: &TLSConfig{
					CA: "issuer-ca",
				},
			},
			expected: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs.Spec.Security = tc.security
			assert.Equal(t, tc.expected, rs.GetSecurity().IsTLSEnabled())
		})
	}
	rs.GetSpec().IsSecurityTLSConfigEnabled()
}

func TestMemberCertificateSecretName(t *testing.T) {
	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()

	// If nothing is specified, we return <name>-cert
	assert.Equal(t, fmt.Sprintf("%s-cert", rs.Name), rs.GetSecurity().MemberCertificateSecretName(rs.Name))

	// If the top-level prefix is specified, we use it
	rs.Spec.Security.CertificatesSecretsPrefix = "top-level-prefix"
	assert.Equal(t, fmt.Sprintf("top-level-prefix-%s-cert", rs.Name), rs.GetSecurity().MemberCertificateSecretName(rs.Name))
}

func TestAgentClientCertificateSecretName(t *testing.T) {
	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().EnableAuth([]AuthMode{util.X509}).Build()

	// Default is the hardcoded "agent-certs"
	assert.Equal(t, util.AgentSecretName, rs.GetSecurity().AgentClientCertificateSecretName(rs.Name).Name)

	// If the top-level prefix is there, we use it
	rs.Spec.Security.CertificatesSecretsPrefix = "prefix"
	assert.Equal(t, fmt.Sprintf("prefix-%s-%s", rs.Name, util.AgentSecretName), rs.GetSecurity().AgentClientCertificateSecretName(rs.Name).Name)

	// If the name is provided (deprecated) we return it
	rs.GetSecurity().Authentication.Agents.ClientCertificateSecretRefWrap.ClientCertificateSecretRef.Name = "foo"
	assert.Equal(t, "foo", rs.GetSecurity().AgentClientCertificateSecretName(rs.Name).Name)
}

func TestInternalClusterAuthSecretName(t *testing.T) {
	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()

	// Default is  <resource-name>-clusterfile
	assert.Equal(t, fmt.Sprintf("%s-clusterfile", rs.Name), rs.GetSecurity().InternalClusterAuthSecretName(rs.Name))

	// IF there is a prefix, use it
	rs.Spec.Security.CertificatesSecretsPrefix = "prefix"
	assert.Equal(t, fmt.Sprintf("prefix-%s-clusterfile", rs.Name), rs.GetSecurity().InternalClusterAuthSecretName(rs.Name))
}

func TestGetTransportSecurity(t *testing.T) {
	someTLS := TransportSecurity("SomeTLS")
	none := TransportSecurity("NONE")
	noneUpper := TransportSecurity("none")

	tests := []struct {
		name    string
		mdbLdap *Ldap
		want    TransportSecurity
	}{
		{
			name: "enabling transport security",
			mdbLdap: &Ldap{
				TransportSecurity: &someTLS,
			},
			want: TransportSecurityTLS,
		},
		{
			name:    "no transport set",
			mdbLdap: &Ldap{},
			want:    TransportSecurityNone,
		},
		{
			name: "none set",
			mdbLdap: &Ldap{
				TransportSecurity: &none,
			},
			want: TransportSecurityNone,
		},
		{
			name: "NONE set",
			mdbLdap: &Ldap{
				TransportSecurity: &noneUpper,
			},
			want: TransportSecurityNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, GetTransportSecurity(tt.mdbLdap), "GetTransportSecurity(%v)", tt.mdbLdap)
		})
	}
}

func TestAdditionalMongodConfigMarshalJSON(t *testing.T) {
	mdb := MongoDB{Spec: MongoDbSpec{DbCommonSpec: DbCommonSpec{Version: "4.2.1"}}}
	mdb.InitDefaults()
	mdb.Spec.AdditionalMongodConfig = &AdditionalMongodConfig{object: map[string]interface{}{"net": map[string]interface{}{"port": "30000"}}}

	marshal, err := json.Marshal(mdb.Spec)
	assert.NoError(t, err)

	unmarshalledSpec := MongoDbSpec{}

	err = json.Unmarshal(marshal, &unmarshalledSpec)
	assert.NoError(t, err)

	expected := mdb.Spec.GetAdditionalMongodConfig().ToMap()
	actual := unmarshalledSpec.AdditionalMongodConfig.ToMap()
	assert.Equal(t, expected, actual)
}
