package mdbmulti

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// defaultTestClusterSpecList returns a deterministic single-cluster spec list of
// 3 members, mirroring the 3-member replica set used by the MongoDB builder test
// corpus in mdb/mongodb_types_test.go.
func defaultTestClusterSpecList() mdb.ClusterSpecList {
	return mdb.ClusterSpecList{
		{ClusterName: "cluster-1", Members: 3},
	}
}

func TestMongoDBMultiSpecMinimumMajorVersion(t *testing.T) {
	tests := []struct {
		name         string
		DbCommonSpec mdb.DbCommonSpec
		want         uint64
	}{
		{
			name: "non ent",
			DbCommonSpec: mdb.DbCommonSpec{
				Version: "7.1.0",
			},
			want: 7,
		},
		{
			name: "ent",
			DbCommonSpec: mdb.DbCommonSpec{
				Version: "7.0.2-ent",
			},
			want: 7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MongoDBMultiSpec{
				DbCommonSpec: tt.DbCommonSpec,
			}
			assert.Equalf(t, tt.want, m.MinimumMajorVersion(), "MinimumMajorVersion()")
		})
	}
}

// TestMongoDBMultiCluster_ConnectionURL_NotSecure mirrors TestMongoDB_ConnectionURL_NotSecure
// for the multi-cluster builder. Multi-cluster hostnames carry no :port suffix (they are
// prebuilt via dns.GetMultiClusterProcessHostnames) and use the <name>-<clusterIdx>-<podIdx>-svc
// naming scheme.
func TestMongoDBMultiCluster_ConnectionURL_NotSecure(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = defaultTestClusterSpecList()

	var cnx string
	cnx = buildConnectionString(mrs, "", "", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local,temple-0-2-svc.my-namespace.svc.cluster.local/"+
		"?connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// Connection parameters. The default one is overridden
	cnx = buildConnectionString(mrs, "", "", "", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"})
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local,temple-0-2-svc.my-namespace.svc.cluster.local/"+
		"?connectTimeoutMS=30000&readPreference=secondary&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// Custom cluster domain
	mrs = DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{{ClusterName: "cluster-1", Members: 2}}
	mrs.Spec.ClusterDomain = "company.domain.net"
	cnx = buildConnectionString(mrs, "", "", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.company.domain.net,"+
		"temple-0-1-svc.my-namespace.svc.company.domain.net/?connectTimeoutMS=20000&replicaSet=temple"+
		"&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)
}

// TestMongoDBMultiCluster_ConnectionURL_MultiClusterTopology verifies hostnames span all member
// clusters using each cluster's assigned index (ClusterNum assigns indexes in ClusterSpecList order).
func TestMongoDBMultiCluster_ConnectionURL_MultiClusterTopology(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{
		{ClusterName: "member-cluster-1", Members: 2},
		{ClusterName: "member-cluster-2", Members: 1},
	}

	cnx := buildConnectionString(mrs, "", "", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local,temple-1-0-svc.my-namespace.svc.cluster.local/"+
		"?connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)
}

// TestMongoDBMultiCluster_ConnectionURL_Secure mirrors TestMongoDB_ConnectionURL_Secure:
// TLS, auth modes, credential encoding, and SRV scheme handling.
func TestMongoDBMultiCluster_ConnectionURL_Secure(t *testing.T) {
	var cnx string

	// Only tls enabled, no auth
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = defaultTestClusterSpecList()
	mrs.Spec.Security.TLSConfig.Enabled = true
	cnx = buildConnectionString(mrs, "", "", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local,temple-0-2-svc.my-namespace.svc.cluster.local/?"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=true",
		cnx)

	// New version of Mongodb -> SCRAM-SHA-256
	mrs = DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{{ClusterName: "cluster-1", Members: 2}}
	mrs.Spec.Security.TLSConfig.Enabled = true
	mrs.Spec.Security.Authentication = &mdb.Authentication{Modes: []mdb.AuthMode{util.SCRAM}}
	cnx = buildConnectionString(mrs, "the_user", "the_passwd", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://the_user:the_passwd@temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=true",
		cnx)

	// Old version of Mongodb -> SCRAM-SHA-1. X509 is a second authentication method - user & password are still appended
	mrs = DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{{ClusterName: "cluster-1", Members: 2}}
	mrs.Spec.Version = "3.6.1"
	mrs.Spec.Security.Authentication = &mdb.Authentication{Modes: []mdb.AuthMode{util.SCRAM, util.X509}}
	cnx = buildConnectionString(mrs, "the_user", "the_passwd", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://the_user:the_passwd@temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-1&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// Explicit SCRAM-SHA-1 mode -> credentials embedded, authMechanism set by builder, authSource is caller's responsibility
	mrs = DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{{ClusterName: "cluster-1", Members: 2}}
	mrs.Spec.Security.Authentication = &mdb.Authentication{Modes: []mdb.AuthMode{util.SCRAMSHA1, util.MONGODBCR}}
	cnx = buildConnectionString(mrs, "the_user", "the_passwd", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://the_user:the_passwd@temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-1&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// Explicit SCRAM-SHA-1 mode with SRV scheme
	cnx = buildConnectionString(mrs, "the_user", "the_passwd", "", connectionstring.SchemeMongoDBSRV, nil)
	assert.Equal(t, "mongodb+srv://the_user:the_passwd@temple-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-1&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// Caller-supplied authSource (as updateConnectionStringSecret always does) is added alongside authMechanism
	cnx = buildConnectionString(mrs, "the_user", "the_passwd", "", connectionstring.SchemeMongoDB, map[string]string{"authSource": "testdb"})
	assert.Equal(t, "mongodb://the_user:the_passwd@temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-1&authSource=testdb&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// Special symbols in user/password must be encoded
	mrs = DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{{ClusterName: "cluster-1", Members: 2}}
	mrs.Spec.Security.Authentication = &mdb.Authentication{Modes: []mdb.AuthMode{util.SCRAM}}
	cnx = buildConnectionString(mrs, "user/@", "pwd#!@", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://user%2F%40:pwd%23%21%40@temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// Caller can override any connection parameters, e.g. "authMechanism"
	cnx = buildConnectionString(mrs, "the_user", "the_passwd", "", connectionstring.SchemeMongoDB, map[string]string{"authMechanism": "SCRAM-SHA-1"})
	assert.Equal(t, "mongodb://the_user:the_passwd@temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-1&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	// X509 -> no user/password in the url. It's possible to pass user/password in the params though
	mrs = DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{{ClusterName: "cluster-1", Members: 2}}
	mrs.Spec.Security.Authentication = &mdb.Authentication{Modes: []mdb.AuthMode{util.X509}}
	cnx = buildConnectionString(mrs, "the_user", "the_passwd", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?connectTimeoutMS=20000&replicaSet=temple&"+
		"serverSelectionTimeoutMS=20000&ssl=false", cnx)

	// username + password must both be provided if scram is enabled
	mrs = DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{{ClusterName: "cluster-1", Members: 2}}
	mrs.Spec.Security.Authentication = &mdb.Authentication{Modes: []mdb.AuthMode{util.SCRAM}}
	cnx = buildConnectionString(mrs, "the_user", "", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	cnx = buildConnectionString(mrs, "", "the_password", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)

	cnx = buildConnectionString(mrs, "", "", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0-svc.my-namespace.svc.cluster.local,"+
		"temple-0-1-svc.my-namespace.svc.cluster.local/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)
}

// TestMongoDBMultiCluster_ConnectionURL_ExternalDomain mirrors TestMongoDBConnectionURLExternalDomainWithAuth:
// per-cluster external domains flow through GetExternalDomainForMemberCluster into the hostnames.
func TestMongoDBMultiCluster_ConnectionURL_ExternalDomain(t *testing.T) {
	az1Domain := "az1.example.com"
	az2Domain := "az2.example.com"

	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{
		{ClusterName: "cluster-1", Members: 2, ExternalAccessConfiguration: &mdb.ExternalAccessConfiguration{ExternalDomain: &az1Domain}},
		{ClusterName: "cluster-2", Members: 2, ExternalAccessConfiguration: &mdb.ExternalAccessConfiguration{ExternalDomain: &az2Domain}},
	}
	mrs.Spec.Security.Authentication = &mdb.Authentication{Modes: []mdb.AuthMode{util.SCRAM}}

	cnx := buildConnectionString(mrs, "the_user", "", "", connectionstring.SchemeMongoDB, nil)
	assert.Equal(t, "mongodb://temple-0-0.az1.example.com,"+
		"temple-0-1.az1.example.com,temple-1-0.az2.example.com,temple-1-1.az2.example.com"+
		"/?authMechanism=SCRAM-SHA-256&authSource=admin&"+
		"connectTimeoutMS=20000&replicaSet=temple&serverSelectionTimeoutMS=20000&ssl=false",
		cnx)
}

// buildConnectionString mirrors the positional API removed from the resource:
// it fills the resource options with caller supplied user data and parameters.
func buildConnectionString(mrs *MongoDBMultiCluster, username, password, connectionStringDatabase string, scheme connectionstring.Scheme, connectionParams map[string]string) string {
	options := mrs.ConnectionOptions()
	options.Username = username
	options.Password = password
	options.Database = connectionStringDatabase
	maps.Copy(options.Params, connectionParams)
	return options.Build(scheme)
}
