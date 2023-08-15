package mdb

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
	return defaultMongoDB(ReplicaSet).SetMembers(3)
}

func NewDefaultReplicaSetBuilder() *MongoDBBuilder {
	return defaultMongoDB(ReplicaSet)
}
func NewDefaultShardedClusterBuilder() *MongoDBBuilder {
	return defaultMongoDB(ShardedCluster)
}

func NewStandaloneBuilder() *MongoDBBuilder {
	return defaultMongoDB(Standalone)
}

func NewClusterBuilder() *MongoDBBuilder {
	sizeConfig := MongodbShardedClusterSizeConfig{
		ShardCount:           2,
		MongodsPerShardCount: 3,
		ConfigServerCount:    4,
		MongosCount:          2,
	}
	mongodb := defaultMongoDB(ShardedCluster)
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

func (b *MongoDBBuilder) SetMongosAdditionalConfig(c *AdditionalMongodConfig) *MongoDBBuilder {
	if b.mdb.Spec.MongosSpec == nil {
		b.mdb.Spec.MongosSpec = &ShardedClusterComponentSpec{}
	}
	b.mdb.Spec.MongosSpec.AdditionalMongodConfig = c
	return b
}

func (b *MongoDBBuilder) SetConfigSrvAdditionalConfig(c *AdditionalMongodConfig) *MongoDBBuilder {
	if b.mdb.Spec.ConfigSrvSpec == nil {
		b.mdb.Spec.ConfigSrvSpec = &ShardedClusterComponentSpec{}
	}
	b.mdb.Spec.ConfigSrvSpec.AdditionalMongodConfig = c
	return b
}

func (b *MongoDBBuilder) SetShardAdditionalConfig(c *AdditionalMongodConfig) *MongoDBBuilder {
	if b.mdb.Spec.ShardSpec == nil {
		b.mdb.Spec.ShardSpec = &ShardedClusterComponentSpec{}
	}
	b.mdb.Spec.ShardSpec.AdditionalMongodConfig = c
	return b
}

func (b *MongoDBBuilder) SetSecurityTLSEnabled() *MongoDBBuilder {
	b.mdb.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *MongoDBBuilder) SetLabels(labels map[string]string) *MongoDBBuilder {
	b.mdb.Labels = labels
	return b
}

func (b *MongoDBBuilder) SetAnnotations(annotations map[string]string) *MongoDBBuilder {
	b.mdb.Annotations = annotations
	return b
}

func (b *MongoDBBuilder) EnableAuth(modes []AuthMode) *MongoDBBuilder {
	if b.mdb.Spec.Security.Authentication == nil {
		b.mdb.Spec.Security.Authentication = &Authentication{}
	}
	b.mdb.Spec.Security.Authentication.Enabled = true
	b.mdb.Spec.Security.Authentication.Modes = modes
	return b
}

func (b *MongoDBBuilder) EnableAgentAuth(mode string) *MongoDBBuilder {
	if b.mdb.Spec.Security.Authentication == nil {
		b.mdb.Spec.Security.Authentication = &Authentication{}
	}
	b.mdb.Spec.Security.Authentication.Agents.Mode = mode
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

func (b *MongoDBBuilder) SetAdditionalOptions(config AdditionalMongodConfig) *MongoDBBuilder {
	b.mdb.Spec.AdditionalMongodConfig = &config
	return b
}

func (b *MongoDBBuilder) SetBackup(backupSpec Backup) *MongoDBBuilder {
	if b.mdb.Spec.ResourceType == Standalone {
		panic("Backup is only supported for ReplicaSets and ShardedClusters")
	}
	b.mdb.Spec.Backup = &backupSpec
	return b
}

func (b *MongoDBBuilder) SetConnectionSpec(spec ConnectionSpec) *MongoDBBuilder {
	b.mdb.Spec.ConnectionSpec = spec
	return b
}

func (b *MongoDBBuilder) SetAgentConfig(agentOptions AgentConfig) *MongoDBBuilder {
	b.mdb.Spec.Agent = agentOptions
	return b
}

func (b *MongoDBBuilder) SetPersistent(p *bool) *MongoDBBuilder {
	b.mdb.Spec.Persistent = p
	return b
}

func (b *MongoDBBuilder) SetPodSpec(podSpec *MongoDbPodSpec) *MongoDBBuilder {
	b.mdb.Spec.PodSpec = podSpec
	return b
}

func (b *MongoDBBuilder) Build() *MongoDB {
	b.mdb.InitDefaults()
	return b.mdb.DeepCopy()
}

// ************************* Package private methods *********************************************************

func defaultMongoDB(resourceType ResourceType) *MongoDBBuilder {
	spec := MongoDbSpec{
		DbCommonSpec: DbCommonSpec{
			Version:      "4.0.0",
			ResourceType: resourceType,
		},
	}
	mdb := &MongoDB{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "test-mdb", Namespace: "testNS"}}
	mdb.InitDefaults()
	return &MongoDBBuilder{mdb}
}

func (b *MongoDBBuilder) setType(resourceType ResourceType) *MongoDBBuilder {
	b.mdb.Spec.ResourceType = resourceType
	return b
}
