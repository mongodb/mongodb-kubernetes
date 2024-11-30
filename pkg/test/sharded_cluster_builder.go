package test

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1"

	v12 "k8s.io/api/core/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type ClusterBuilder struct {
	*mdb.MongoDB
}

func DefaultClusterBuilder() *ClusterBuilder {
	sizeConfig := status.MongodbShardedClusterSizeConfig{
		ShardCount:           2,
		MongodsPerShardCount: 3,
		ConfigServerCount:    3,
		MongosCount:          4,
	}

	status := mdb.MongoDbStatus{
		MongodbShardedClusterSizeConfig: sizeConfig,
	}

	spec := mdb.MongoDbSpec{
		DbCommonSpec: mdb.DbCommonSpec{
			Persistent: util.BooleanRef(false),
			ConnectionSpec: mdb.ConnectionSpec{
				SharedConnectionSpec: mdb.SharedConnectionSpec{
					OpsManagerConfig: &mdb.PrivateCloudConfig{
						ConfigMapRef: mdb.ConfigMapRef{
							Name: mock.TestProjectConfigMapName,
						},
					},
				},
				Credentials: mock.TestCredentialsSecretName,
			},
			Version:      "3.6.4",
			ResourceType: mdb.ShardedCluster,

			Security: &mdb.Security{
				TLSConfig: &mdb.TLSConfig{},
				Authentication: &mdb.Authentication{
					Modes: []mdb.AuthMode{},
				},
			},
		},
		MongodbShardedClusterSizeConfig: sizeConfig,
		ShardedClusterSpec: mdb.ShardedClusterSpec{
			ConfigSrvSpec:    &mdb.ShardedClusterComponentSpec{},
			MongosSpec:       &mdb.ShardedClusterComponentSpec{},
			ShardSpec:        &mdb.ShardedClusterComponentSpec{},
			ConfigSrvPodSpec: mdb.NewMongoDbPodSpec(),
			ShardPodSpec:     mdb.NewMongoDbPodSpec(),
		},
	}

	resource := &mdb.MongoDB{
		ObjectMeta: v1.ObjectMeta{Name: "slaney", Namespace: mock.TestNamespace},
		Status:     status,
		Spec:       spec,
	}

	return &ClusterBuilder{resource}
}

func (b *ClusterBuilder) SetName(name string) *ClusterBuilder {
	b.Name = name
	return b
}

func (b *ClusterBuilder) SetShardCountSpec(count int) *ClusterBuilder {
	b.Spec.ShardCount = count
	return b
}

func (b *ClusterBuilder) SetMongodsPerShardCountSpec(count int) *ClusterBuilder {
	b.Spec.MongodsPerShardCount = count
	return b
}

func (b *ClusterBuilder) SetConfigServerCountSpec(count int) *ClusterBuilder {
	b.Spec.ConfigServerCount = count
	return b
}

func (b *ClusterBuilder) SetMongosCountSpec(count int) *ClusterBuilder {
	b.Spec.MongosCount = count
	return b
}

func (b *ClusterBuilder) SetShardCountStatus(count int) *ClusterBuilder {
	b.Status.ShardCount = count
	return b
}

func (b *ClusterBuilder) SetMongodsPerShardCountStatus(count int) *ClusterBuilder {
	b.Status.MongodsPerShardCount = count
	return b
}

func (b *ClusterBuilder) SetConfigServerCountStatus(count int) *ClusterBuilder {
	b.Status.ConfigServerCount = count
	return b
}

func (b *ClusterBuilder) SetMongosCountStatus(count int) *ClusterBuilder {
	b.Status.MongosCount = count
	return b
}

func (b *ClusterBuilder) SetSecurity(security mdb.Security) *ClusterBuilder {
	b.Spec.Security = &security
	return b
}

func (b *ClusterBuilder) EnableTLS() *ClusterBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		return b.SetSecurity(mdb.Security{TLSConfig: &mdb.TLSConfig{Enabled: true}})
	}
	b.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *ClusterBuilder) SetTLSCA(ca string) *ClusterBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		b.SetSecurity(mdb.Security{TLSConfig: &mdb.TLSConfig{}})
	}
	b.Spec.Security.TLSConfig.CA = ca
	return b
}

func (b *ClusterBuilder) SetTLSConfig(tlsConfig mdb.TLSConfig) *ClusterBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdb.Security{}
	}
	b.Spec.Security.TLSConfig = &tlsConfig
	return b
}

func (b *ClusterBuilder) EnableX509() *ClusterBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.X509)
	return b
}

func (b *ClusterBuilder) EnableSCRAM() *ClusterBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.SCRAM)
	return b
}

func (b *ClusterBuilder) RemoveAuth() *ClusterBuilder {
	b.Spec.Security.Authentication = nil

	return b
}

func (b *ClusterBuilder) EnableAuth() *ClusterBuilder {
	b.Spec.Security.Authentication.Enabled = true
	return b
}

func (b *ClusterBuilder) SetAuthModes(modes []mdb.AuthMode) *ClusterBuilder {
	b.Spec.Security.Authentication.Modes = modes
	return b
}

func (b *ClusterBuilder) EnableX509InternalClusterAuth() *ClusterBuilder {
	b.Spec.Security.Authentication.InternalCluster = util.X509
	return b
}

func (b *ClusterBuilder) SetShardPodSpec(spec v12.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.ShardPodSpec == nil {
		b.Spec.ShardPodSpec = &mdb.MongoDbPodSpec{}
	}
	b.Spec.ShardPodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetPodConfigSvrSpecTemplate(spec v12.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.ConfigSrvPodSpec == nil {
		b.Spec.ConfigSrvPodSpec = &mdb.MongoDbPodSpec{}
	}
	b.Spec.ConfigSrvPodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetMongosPodSpecTemplate(spec v12.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.MongosPodSpec == nil {
		b.Spec.MongosPodSpec = &mdb.MongoDbPodSpec{}
	}
	b.Spec.MongosPodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetShardSpecificPodSpecTemplate(specs []v12.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.ShardSpecificPodSpec == nil {
		b.Spec.ShardSpecificPodSpec = make([]mdb.MongoDbPodSpec, 0)
	}

	mongoDBPodSpec := make([]mdb.MongoDbPodSpec, len(specs))

	for n, e := range specs {
		mongoDBPodSpec[n] = mdb.MongoDbPodSpec{PodTemplateWrapper: mdb.PodTemplateSpecWrapper{
			PodTemplate: &e,
		}}
	}

	b.Spec.ShardSpecificPodSpec = mongoDBPodSpec
	return b
}

func (b *ClusterBuilder) SetAnnotations(annotations map[string]string) *ClusterBuilder {
	b.Annotations = annotations
	return b
}

func (b *ClusterBuilder) SetTopology(topology string) *ClusterBuilder {
	b.MongoDB.Spec.Topology = topology
	return b
}

func (b *ClusterBuilder) SetConfigSrvClusterSpec(clusterSpecList mdb.ClusterSpecList) *ClusterBuilder {
	b.Spec.ConfigSrvSpec.ClusterSpecList = clusterSpecList
	return b
}

func (b *ClusterBuilder) SetMongosClusterSpec(clusterSpecList mdb.ClusterSpecList) *ClusterBuilder {
	b.Spec.MongosSpec.ClusterSpecList = clusterSpecList
	return b
}

func (b *ClusterBuilder) SetShardClusterSpec(clusterSpecList mdb.ClusterSpecList) *ClusterBuilder {
	b.Spec.ShardSpec.ClusterSpecList = clusterSpecList
	return b
}

func (b *ClusterBuilder) SetShardOverrides(override []mdb.ShardOverride) *ClusterBuilder {
	b.Spec.ShardOverrides = override
	return b
}

func (b *ClusterBuilder) SetOpsManagerConfigMapName(configMapName string) *ClusterBuilder {
	b.Spec.SharedConnectionSpec.OpsManagerConfig.ConfigMapRef.Name = configMapName
	return b
}

func (b *ClusterBuilder) SetExternalAccessDomain(externalDomains ClusterDomains) *ClusterBuilder {
	if b.Spec.IsMultiCluster() {
		for i := range b.Spec.ConfigSrvSpec.ClusterSpecList {
			if b.Spec.ConfigSrvSpec.ClusterSpecList[i].ExternalAccessConfiguration == nil {
				b.Spec.ConfigSrvSpec.ClusterSpecList[i].ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
			}
			if len(externalDomains.ConfigServerExternalDomain) > 0 {
				b.Spec.ConfigSrvSpec.ClusterSpecList[i].ExternalAccessConfiguration.ExternalDomain = &externalDomains.ConfigServerExternalDomain
			}
		}
		for i := range b.Spec.MongosSpec.ClusterSpecList {
			if b.Spec.MongosSpec.ClusterSpecList[i].ExternalAccessConfiguration == nil {
				b.Spec.MongosSpec.ClusterSpecList[i].ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
			}
			if len(externalDomains.MongosExternalDomain) > 0 {
				b.Spec.MongosSpec.ClusterSpecList[i].ExternalAccessConfiguration.ExternalDomain = &externalDomains.MongosExternalDomain
			}
		}
		for i := range b.Spec.ShardSpec.ClusterSpecList {
			if b.Spec.ShardSpec.ClusterSpecList[i].ExternalAccessConfiguration == nil {
				b.Spec.ShardSpec.ClusterSpecList[i].ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
			}
			if len(externalDomains.ShardsExternalDomain) > 0 {
				b.Spec.ShardSpec.ClusterSpecList[i].ExternalAccessConfiguration.ExternalDomain = &externalDomains.ShardsExternalDomain
			}
		}
	} else {
		if b.Spec.ExternalAccessConfiguration == nil {
			b.Spec.ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
		}
		b.Spec.ExternalAccessConfiguration.ExternalDomain = &externalDomains.SingleClusterDomain
	}
	return b
}

func (b *ClusterBuilder) SetExternalAccessDomainAnnotations(annotationWithPlaceholders map[string]string) *ClusterBuilder {
	if b.Spec.IsMultiCluster() {
		for i := range b.Spec.ConfigSrvSpec.ClusterSpecList {
			if b.Spec.ConfigSrvSpec.ClusterSpecList[i].ExternalAccessConfiguration == nil {
				b.Spec.ConfigSrvSpec.ClusterSpecList[i].ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
			}
			b.Spec.ConfigSrvSpec.ClusterSpecList[i].ExternalAccessConfiguration.ExternalService.Annotations = annotationWithPlaceholders
		}
		for i := range b.Spec.MongosSpec.ClusterSpecList {
			if b.Spec.MongosSpec.ClusterSpecList[i].ExternalAccessConfiguration == nil {
				b.Spec.MongosSpec.ClusterSpecList[i].ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
			}
			b.Spec.MongosSpec.ClusterSpecList[i].ExternalAccessConfiguration.ExternalService.Annotations = annotationWithPlaceholders

		}
		for i := range b.Spec.ShardSpec.ClusterSpecList {
			if b.Spec.ShardSpec.ClusterSpecList[i].ExternalAccessConfiguration == nil {
				b.Spec.ShardSpec.ClusterSpecList[i].ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
			}
			b.Spec.ShardSpec.ClusterSpecList[i].ExternalAccessConfiguration.ExternalService.Annotations = annotationWithPlaceholders
		}
	} else {
		if b.Spec.ExternalAccessConfiguration == nil {
			b.Spec.ExternalAccessConfiguration = &mdb.ExternalAccessConfiguration{}
		}
		b.Spec.ExternalAccessConfiguration.ExternalService.Annotations = annotationWithPlaceholders
	}
	return b
}

func (b *ClusterBuilder) WithMultiClusterSetup(memberClusters MemberClusters) *ClusterBuilder {
	b.SetTopology(mdb.ClusterTopologyMultiCluster)
	b.SetShardCountSpec(memberClusters.ShardCount())

	// The below parameters should be ignored when a clusterSpecList is configured/for multiClusterTopology
	b.SetMongodsPerShardCountSpec(0)
	b.SetConfigServerCountSpec(0)
	b.SetMongosCountSpec(0)

	b.SetShardClusterSpec(CreateClusterSpecList(memberClusters.ClusterNames, memberClusters.ShardDistribution[0]))
	b.SetConfigSrvClusterSpec(CreateClusterSpecList(memberClusters.ClusterNames, memberClusters.ConfigServerDistribution))
	b.SetMongosClusterSpec(CreateClusterSpecList(memberClusters.ClusterNames, memberClusters.MongosDistribution))

	return b
}

func (b *ClusterBuilder) Build() *mdb.MongoDB {
	b.Spec.ResourceType = mdb.ShardedCluster
	b.InitDefaults()
	return b.MongoDB
}

// Creates a list of ClusterSpecItems based on names and distribution
// The two input list must have the same size
func CreateClusterSpecList(clusterNames []string, memberCounts map[string]int) mdb.ClusterSpecList {
	specList := make(mdb.ClusterSpecList, 0)
	for _, clusterName := range clusterNames {
		if _, ok := memberCounts[clusterName]; !ok {
			continue
		}

		specList = append(specList, mdb.ClusterSpecItem{
			ClusterName: clusterName,
			Members:     memberCounts[clusterName],
		})
	}
	return specList
}
