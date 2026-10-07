package mdb

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/authtypes"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstring"
)

// TestConnectionStringAdditionalConfig pins how spec.additionalConnectionStringConfig reaches
// the generated connection strings.
func TestConnectionStringAdditionalConfig(t *testing.T) {
	t.Run("resource options are appended to both schemes", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetMembers(2).Build()
		rs.Spec.AdditionalConnectionStringConfig = v1.MapWrapper{Object: map[string]interface{}{
			"appName":     "my-app",
			"retryWrites": false,
		}}

		standard := buildConnectionString(rs, "", "", "", connectionstring.SchemeMongoDB, nil)
		assert.Contains(t, standard, "appName=my-app")
		assert.Contains(t, standard, "retryWrites=false")

		srv := buildConnectionString(rs, "", "", "", connectionstring.SchemeMongoDBSRV, nil)
		assert.Contains(t, srv, "appName=my-app")
		assert.Contains(t, srv, "retryWrites=false")
	})

	t.Run("resource options override the derived ones", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetMembers(2).Build()
		rs.Spec.AdditionalConnectionStringConfig = v1.MapWrapper{Object: map[string]interface{}{
			"replicaSet": "custom-rs",
		}}

		standard := buildConnectionString(rs, "", "", "", connectionstring.SchemeMongoDB, nil)
		assert.Contains(t, standard, "replicaSet=custom-rs")
		assert.Equal(t, 1, strings.Count(standard, "replicaSet="))
	})

	t.Run("sharded clusters carry the resource options too", func(t *testing.T) {
		sc := NewClusterBuilder().SetName("contractsDb").SetNamespace("ns").Build()
		sc.Spec.AdditionalConnectionStringConfig = v1.MapWrapper{Object: map[string]interface{}{
			"appName": "my-app",
		}}

		assert.Contains(t, buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDB, nil), "appName=my-app")
		assert.Contains(t, buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDBSRV, nil), "appName=my-app")
	})

	t.Run("user options override the resource options which override the derived ones", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetMembers(2).Build()
		rs.Spec.AdditionalConnectionStringConfig = v1.MapWrapper{Object: map[string]interface{}{
			"replicaSet":               "resource-rs",
			"serverSelectionTimeoutMS": 5000,
		}}

		options := rs.ConnectionOptions().WithUser(authtypes.User{
			Username: "user",
			Database: "admin",
			ConnectionStringOptions: map[string]interface{}{
				"serverSelectionTimeoutMS": 9000,
			},
		}, "password")

		cs := options.Build(connectionstring.SchemeMongoDB)

		// Parameters are merged by key, so a supplied value replaces the generated one
		// instead of being appended next to it.
		assert.Contains(t, cs, "replicaSet=resource-rs")
		assert.Contains(t, cs, "serverSelectionTimeoutMS=9000")
		assert.NotContains(t, cs, "serverSelectionTimeoutMS=5000")
		assert.NotContains(t, cs, "serverSelectionTimeoutMS=20000")
		assert.Equal(t, 1, strings.Count(cs, "replicaSet="))
		assert.Equal(t, 1, strings.Count(cs, "serverSelectionTimeoutMS="))
	})
}

// TestConnectionStringExternalDomain pins the domain each topology resolves for its connection
// strings. Replica sets and standalones read the top level field, sharded clusters resolve the
// mongos tier and deliberately ignore the per member cluster domains.
func TestConnectionStringExternalDomain(t *testing.T) {
	topLevelDomain := "top-level.example.com"
	mongosDomain := "mongos.example.com"

	t.Run("replica set uses the top level domain", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetMembers(2).ExposedExternally(nil, nil, &topLevelDomain).Build()

		assert.Equal(t, "mongodb://test-mdb-0.top-level.example.com:27017,"+
			"test-mdb-1.top-level.example.com:27017/"+
			"?connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(rs, "", "", "", connectionstring.SchemeMongoDB, nil))

		assert.Equal(t, "mongodb+srv://top-level.example.com/"+
			"?connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(rs, "", "", "", connectionstring.SchemeMongoDBSRV, nil))
	})

	t.Run("replica set without a domain uses the in cluster names", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetMembers(2).Build()

		assert.Equal(t, "mongodb://test-mdb-0.test-mdb-svc.testNS.svc.cluster.local:27017,"+
			"test-mdb-1.test-mdb-svc.testNS.svc.cluster.local:27017/"+
			"?connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(rs, "", "", "", connectionstring.SchemeMongoDB, nil))

		assert.Equal(t, "mongodb+srv://test-mdb-svc.testNS.svc.cluster.local/"+
			"?connectTimeoutMS=20000&replicaSet=test-mdb&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(rs, "", "", "", connectionstring.SchemeMongoDBSRV, nil))
	})

	t.Run("sharded cluster falls back to the top level domain for the mongos tier", func(t *testing.T) {
		sc := NewClusterBuilder().SetName("contractsDb").SetNamespace("ns").ExposedExternally(nil, nil, &topLevelDomain).Build()

		assert.Equal(t, "mongodb://contractsDb-mongos-0.top-level.example.com:27017,"+
			"contractsDb-mongos-1.top-level.example.com:27017/"+
			"?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDB, nil))

		assert.Equal(t, "mongodb+srv://top-level.example.com/"+
			"?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDBSRV, nil))
	})

	t.Run("sharded cluster prefers the mongos tier domain over the top level one", func(t *testing.T) {
		sc := NewClusterBuilder().SetName("contractsDb").SetNamespace("ns").ExposedExternally(nil, nil, &topLevelDomain).Build()
		sc.Spec.MongosSpec = &ShardedClusterComponentSpec{
			ExternalAccessConfiguration: &ExternalAccessConfiguration{ExternalDomain: &mongosDomain},
		}

		assert.Equal(t, "mongodb://contractsDb-mongos-0.mongos.example.com:27017,"+
			"contractsDb-mongos-1.mongos.example.com:27017/"+
			"?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDB, nil))

		assert.Equal(t, "mongodb+srv://mongos.example.com/"+
			"?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000&ssl=false",
			buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDBSRV, nil))
	})

	t.Run("sharded cluster ignores per member cluster domains", func(t *testing.T) {
		perClusterDomain := "per-cluster.example.com"
		sc := NewClusterBuilder().SetName("contractsDb").SetNamespace("ns").Build()
		sc.Spec.Topology = ClusterTopologyMultiCluster
		sc.Spec.MongosSpec = &ShardedClusterComponentSpec{
			ClusterSpecList: ClusterSpecList{
				{
					ClusterName:                 "cluster-1",
					Members:                     2,
					ExternalAccessConfiguration: &ExternalAccessConfiguration{ExternalDomain: &perClusterDomain},
				},
			},
		}

		// A connection string is cluster agnostic, so the per member cluster domain does not apply.
		assert.NotContains(t, buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDB, nil), perClusterDomain)
		assert.NotContains(t, buildConnectionString(sc, "", "", "", connectionstring.SchemeMongoDBSRV, nil), perClusterDomain)
	})
}
