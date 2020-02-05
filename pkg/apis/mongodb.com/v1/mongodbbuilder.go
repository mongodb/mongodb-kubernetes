package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TODO must replace all [Standalone|Replicaset|Cluster]Builder classes in 'operator' package
// TODO 2 move this to a separate package 'mongodb' together with 'types.go' and 'podspecbuilder.go'
// Convenience builder for Mongodb object
type MongoDBBuilder struct {
	mdb *MongoDB
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
	mongodb.mdb.Spec.MongodbShardedClusterSizeConfig = sizeConfig
	return mongodb
}

func (b *MongoDBBuilder) SetVersion(version string) *MongoDBBuilder {
	b.mdb.Spec.Version = version
	return b
}

func (b *MongoDBBuilder) SetName(name string) *MongoDBBuilder {
	b.mdb.Name = name
	return b
}

func (b *MongoDBBuilder) SetNamespace(namespace string) *MongoDBBuilder {
	b.mdb.Namespace = namespace
	return b
}

func (b *MongoDBBuilder) SetFCVersion(version string) *MongoDBBuilder {
	b.mdb.Spec.FeatureCompatibilityVersion = &version
	return b
}

func (b *MongoDBBuilder) SetMembers(m int) *MongoDBBuilder {
	if b.mdb.Spec.ResourceType != ReplicaSet {
		panic("Only replicaset can have members configuration")
	}
	b.mdb.Spec.Members = m
	return b
}
func (b *MongoDBBuilder) SetClusterDomain(m string) *MongoDBBuilder {
	b.mdb.Spec.ClusterDomain = m
	return b
}

func (b *MongoDBBuilder) SetAdditionalConfig(c *AdditionalMongodConfig) *MongoDBBuilder {
	b.mdb.Spec.AdditionalMongodConfig = c
	return b
}

func (b *MongoDBBuilder) SetSecurityTLSEnabled() *MongoDBBuilder {
	b.mdb.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *MongoDBBuilder) EnableAuth(modes []string) *MongoDBBuilder {
	b.mdb.Spec.Security.Authentication.Enabled = true
	b.mdb.Spec.Security.Authentication.Modes = modes
	return b
}

func (b *MongoDBBuilder) SetShardCountSpec(count int) *MongoDBBuilder {
	if b.mdb.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have shards configuration")
	}
	b.mdb.Spec.ShardCount = count
	return b
}
func (b *MongoDBBuilder) SetMongodsPerShardCountSpec(count int) *MongoDBBuilder {
	if b.mdb.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have shards configuration")
	}
	b.mdb.Spec.MongodsPerShardCount = count
	return b
}
func (b *MongoDBBuilder) SetConfigServerCountSpec(count int) *MongoDBBuilder {
	if b.mdb.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have config server configuration")
	}
	b.mdb.Spec.ConfigServerCount = count
	return b
}
func (b *MongoDBBuilder) SetMongosCountSpec(count int) *MongoDBBuilder {
	if b.mdb.Spec.ResourceType != ShardedCluster {
		panic("Only sharded cluster can have mongos configuration")
	}
	b.mdb.Spec.MongosCount = count
	return b
}

func (b *MongoDBBuilder) Build() *MongoDB {
	b.mdb.InitDefaults()
	return b.mdb.DeepCopy()
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
	b.mdb.Spec.ResourceType = resourceType
	return b
}
