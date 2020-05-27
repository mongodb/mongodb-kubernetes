package v1

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
)

func TestEnsureSecurity_WithAllNilValues(t *testing.T) {
	spec := &MongoDbSpec{Security: nil}
	ensureSecurity(spec)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
}

func TestEnsureSecurity_WithNilTlsConfig(t *testing.T) {
	spec := &MongoDbSpec{Security: &Security{TLSConfig: nil, Authentication: &Authentication{}}}
	ensureSecurity(spec)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
}

func TestEnsureSecurity_EmptySpec(t *testing.T) {
	spec := &MongoDbSpec{}
	ensureSecurity(spec)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
}

func TestGetAgentAuthentication(t *testing.T) {
	sec := newSecurity()
	sec.Authentication = newAuthentication()
	assert.Len(t, sec.Authentication.Modes, 0)
	assert.Empty(t, sec.GetAgentMechanism())

	sec.Authentication.Modes = append(sec.Authentication.Modes, util.X509)
	assert.Len(t, sec.Authentication.Modes, 1)
	assert.Equal(t, util.X509, sec.GetAgentMechanism())

	sec.Authentication.Modes = append(sec.Authentication.Modes, util.SCRAM)

	assert.Len(t, sec.Authentication.Modes, 2)
	assert.Equal(t, util.SCRAM, sec.GetAgentMechanism())
}

func TestMinimumMajorVersion(t *testing.T) {
	mdbSpec := MongoDbSpec{
		Version:                     "3.6.0-ent",
		FeatureCompatibilityVersion: nil,
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))

	mdbSpec = MongoDbSpec{
		Version:                     "4.0.0-ent",
		FeatureCompatibilityVersion: stringutil.Ref("3.6"),
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))

	mdbSpec = MongoDbSpec{
		Version:                     "4.0.0",
		FeatureCompatibilityVersion: stringutil.Ref("3.6"),
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))
}

func TestMongoDB_ConnectionURL_NotSecure(t *testing.T) {
	rs := NewReplicaSetBuilder().SetMembers(3).Build()
	assert.Equal(t, "mongodb://testMDB-0.testMDB-svc.testNS.svc.cluster.local:27017,"+
		"testMDB-1.testMDB-svc.testNS.svc.cluster.local:27017,testMDB-2.testMDB-svc.testNS.svc.cluster.local:27017/"+
		"?connectTimeoutMS=20000&replicaSet=testMDB&serverSelectionTimeoutMS=20000", rs.ConnectionURL("", "", nil))

	// Connection parameters. The default one is overridden
	assert.Equal(t, "mongodb://testMDB-0.testMDB-svc.testNS.svc.cluster.local:27017,"+
		"testMDB-1.testMDB-svc.testNS.svc.cluster.local:27017,testMDB-2.testMDB-svc.testNS.svc.cluster.local:27017/"+
		"?connectTimeoutMS=30000&readPreference=secondary&replicaSet=testMDB&serverSelectionTimeoutMS=20000",
		rs.ConnectionURL("", "", map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}))

	// 2 members, custom cluster name
	rs = NewReplicaSetBuilder().SetName("paymentsDb").SetMembers(2).SetClusterDomain("company.domain.net").Build()
	assert.Equal(t, "mongodb://paymentsDb-0.paymentsDb-svc.testNS.svc.company.domain.net:27017,"+
		"paymentsDb-1.paymentsDb-svc.testNS.svc.company.domain.net:27017/?connectTimeoutMS=20000&replicaSet=paymentsDb"+
		"&serverSelectionTimeoutMS=20000", rs.ConnectionURL("", "", nil))

	// Sharded cluster
	sc := NewClusterBuilder().SetName("contractsDb").SetNamespace("ns").Build()
	assert.Equal(t, "mongodb://contractsDb-mongos-0.contractsDb-svc.ns.svc.cluster.local:27017,"+
		"contractsDb-mongos-1.contractsDb-svc.ns.svc.cluster.local:27017/"+
		"?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000", sc.ConnectionURL("", "", nil))

	// Standalone
	st := NewStandaloneBuilder().SetName("foo").Build()
	assert.Equal(t, "mongodb://foo-0.foo-svc.testNS.svc.cluster.local:27017/?"+
		"connectTimeoutMS=20000&serverSelectionTimeoutMS=20000", st.ConnectionURL("", "", nil))

}

func TestMongoDB_ConnectionURL_Secure(t *testing.T) {
	// Only tls enabled, no auth
	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()
	assert.Equal(t, "mongodb://testMDB-0.testMDB-svc.testNS.svc.cluster.local:27017,"+
		"testMDB-1.testMDB-svc.testNS.svc.cluster.local:27017,testMDB-2.testMDB-svc.testNS.svc.cluster.local:27017/?"+
		"connectTimeoutMS=20000&replicaSet=testMDB&serverSelectionTimeoutMS=20000&ssl=true",
		rs.ConnectionURL("", "", nil))

	// New version of Mongodb -> SCRAM-SHA-256
	rs = NewReplicaSetBuilder().SetMembers(2).SetSecurityTLSEnabled().EnableAuth([]string{util.SCRAM}).Build()
	assert.Equal(t, "mongodb://the_user:the_passwd@testMDB-0.testMDB-svc.testNS.svc.cluster.local:27017,"+
		"testMDB-1.testMDB-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=testMDB&serverSelectionTimeoutMS=20000&ssl=true",
		rs.ConnectionURL("the_user", "the_passwd", nil))

	// Old version of Mongodb -> SCRAM-SHA-1. X509 is a second authentication method - user & password are still appended
	rs = NewReplicaSetBuilder().SetMembers(2).SetVersion("3.6.1").EnableAuth([]string{util.SCRAM, util.X509}).Build()
	assert.Equal(t, "mongodb://the_user:the_passwd@testMDB-0.testMDB-svc.testNS.svc.cluster.local:27017,"+
		"testMDB-1.testMDB-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-1&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=testMDB&serverSelectionTimeoutMS=20000",
		rs.ConnectionURL("the_user", "the_passwd", nil))

	// Caller can override any connection parameters, e.g."authMechanism"
	rs = NewReplicaSetBuilder().SetMembers(2).EnableAuth([]string{util.SCRAM}).Build()
	assert.Equal(t, "mongodb://the_user:the_passwd@testMDB-0.testMDB-svc.testNS.svc.cluster.local:27017,"+
		"testMDB-1.testMDB-svc.testNS.svc.cluster.local:27017/?authMechanism=SCRAM-SHA-1&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=testMDB&serverSelectionTimeoutMS=20000",
		rs.ConnectionURL("the_user", "the_passwd", map[string]string{"authMechanism": "SCRAM-SHA-1"}))

	// X509 -> no user/password in the url. It's possible to pass user/password in the params though
	rs = NewReplicaSetBuilder().SetMembers(2).EnableAuth([]string{util.X509}).Build()
	assert.Equal(t, "mongodb://testMDB-0.testMDB-svc.testNS.svc.cluster.local:27017,"+
		"testMDB-1.testMDB-svc.testNS.svc.cluster.local:27017/?connectTimeoutMS=20000&replicaSet=testMDB&"+
		"serverSelectionTimeoutMS=20000", rs.ConnectionURL("the_user", "the_passwd", nil))

	// username + password must be provided if scram is enabled
	rs = NewReplicaSetBuilder().EnableAuth([]string{util.SCRAM}).Build()
	assert.Panics(t, func() { rs.ConnectionURL("the_user", "", nil) })
	assert.Panics(t, func() { rs.ConnectionURL("", "the_passwd", nil) })
	assert.Panics(t, func() { rs.ConnectionURL("", "", nil) })

}

func TestMongoDB_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDB{}
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.Warnings)
}
