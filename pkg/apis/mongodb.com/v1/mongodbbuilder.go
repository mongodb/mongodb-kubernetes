package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TODO must replace all [Standalone|Replicaset|Cluster]Builder classes in 'operator' package
// TODO 2 it makes sense now to group different resources in different packages (types.go, mongobuilder.go, types_test.go)
// Convenience builder for Mongodb object
type MongoDBBuilder struct {
	*MongoDB
}

func NewReplicaSetBuilder() *MongoDBBuilder {
	return defaultMongoDB().setType(ReplicaSet).SetMembers(3)
}

func NewStandaloneBuilder() *MongoDBBuilder {
	return defaultMongoDB().setType(Standalone)
}

func NewClusterBuilder() *MongoDBBuilder {
	sizeConfig := MongodbShardedClusterSizeConfig{
		ShardCount:           2,
		MongodsPerShardCount: 3,
		ConfigServerCount:    4,
		MongosCount:          2,
	}
	mongodb := defaultMongoDB().setType(ShardedCluster)
	mongodb.Spec.MongodbShardedClusterSizeConfig = sizeConfig
	return mongodb
}

func (b *MongoDBBuilder) SetVersion(version string) *MongoDBBuilder {
	b.Spec.Version = version
	return b
}

func (b *MongoDBBuilder) SetName(name string) *MongoDBBuilder {
	b.Name = name
	return b
}

func (b *MongoDBBuilder) SetNamespace(namespace string) *MongoDBBuilder {
	b.Namespace = namespace
	return b
}

func (b *MongoDBBuilder) SetFCVersion(version string) *MongoDBBuilder {
	b.Spec.FeatureCompatibilityVersion = &version
	return b
}

func (b *MongoDBBuilder) SetMembers(m int) *MongoDBBuilder {
	if b.Spec.ResourceType != ReplicaSet {
		panic("Only replicaset can have members configuration")
	}
	b.Spec.Members = m
	return b
}
func (b *MongoDBBuilder) SetClusterDomain(m string) *MongoDBBuilder {
	b.Spec.ClusterDomain = m
	return b
}

func (b *MongoDBBuilder) SetAdditionalConfig(c *AdditionalMongodConfig) *MongoDBBuilder {
	b.Spec.AdditionalMongodConfig = c
	return b
}

func (b *MongoDBBuilder) SetSecurityTLSEnabled() *MongoDBBuilder {
	b.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *MongoDBBuilder) EnableAuth(modes []string) *MongoDBBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = modes
	return b
}

func (b *MongoDBBuilder) SetShardCountSpec(count int) *MongoDBBuilder {
	if b.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have shards configuration")
	}
	b.Spec.ShardCount = count
	return b
}
func (b *MongoDBBuilder) SetMongodsPerShardCountSpec(count int) *MongoDBBuilder {
	if b.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have shards configuration")
	}
	b.Spec.MongodsPerShardCount = count
	return b
}
func (b *MongoDBBuilder) SetConfigServerCountSpec(count int) *MongoDBBuilder {
	if b.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have config server configuration")
	}
	b.Spec.ConfigServerCount = count
	return b
}
func (b *MongoDBBuilder) SetMongosCountSpec(count int) *MongoDBBuilder {
	if b.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have mongos configuration")
	}
	b.Spec.MongosCount = count
	return b
}

func (b *MongoDBBuilder) Build() *MongoDB {
	b.InitDefaults()
	return b.MongoDB
}

// ************************* Package private methods *********************************************************

func defaultMongoDB() *MongoDBBuilder {
	spec := MongoDbSpec{
		Version: "4.0.0",
	}
	mdb := &MongoDB{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "testMDB", Namespace: "testNS"}}
	mdb.InitDefaults()
	return &MongoDBBuilder{mdb}
}

func (b *MongoDBBuilder) setType(resourceType ResourceType) *MongoDBBuilder {
	b.Spec.ResourceType = resourceType
	return b
}
