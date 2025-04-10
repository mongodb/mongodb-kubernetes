package mdb

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// TODO remove the wrapper in favor of podSpecBuilder
type PodSpecWrapperBuilder struct {
	spec PodSpecWrapper
}

type PersistenceConfigBuilder struct {
	config *common.PersistenceConfig
}

// NewPodSpecWrapperBuilder returns the builder with some default values, used in tests mostly
func NewPodSpecWrapperBuilder() *PodSpecWrapperBuilder {
	spec := MongoDbPodSpec{
		ContainerResourceRequirements: ContainerResourceRequirements{
			CpuLimit:       "1.0",
			CpuRequests:    "0.5",
			MemoryLimit:    "500M",
			MemoryRequests: "400M",
		},
		PodTemplateWrapper: common.PodTemplateSpecWrapper{PodTemplate: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Affinity: &corev1.Affinity{
					PodAffinity: &corev1.PodAffinity{},
				},
			},
		}},
	}
	return &PodSpecWrapperBuilder{PodSpecWrapper{
		MongoDbPodSpec: spec,
		Default:        NewPodSpecWithDefaultValues(),
	}}
}

func NewPodSpecWrapperBuilderFromSpec(spec *MongoDbPodSpec) *PodSpecWrapperBuilder {
	if spec == nil {
		return &PodSpecWrapperBuilder{PodSpecWrapper{}}
	}
	return &PodSpecWrapperBuilder{PodSpecWrapper{MongoDbPodSpec: *spec}}
}

func NewEmptyPodSpecWrapperBuilder() *PodSpecWrapperBuilder {
	return &PodSpecWrapperBuilder{spec: PodSpecWrapper{
		MongoDbPodSpec: MongoDbPodSpec{
			Persistence: &common.Persistence{},
		},
		Default: MongoDbPodSpec{
			Persistence: &common.Persistence{SingleConfig: &common.PersistenceConfig{}},
		},
	}}
}

func (p *PodSpecWrapperBuilder) SetCpuLimit(cpu string) *PodSpecWrapperBuilder {
	p.spec.CpuLimit = cpu
	return p
}

func (p *PodSpecWrapperBuilder) SetCpuRequests(cpu string) *PodSpecWrapperBuilder {
	p.spec.CpuRequests = cpu
	return p
}

func (p *PodSpecWrapperBuilder) SetMemoryLimit(memory string) *PodSpecWrapperBuilder {
	p.spec.MemoryLimit = memory
	return p
}

func (p *PodSpecWrapperBuilder) SetMemoryRequest(memory string) *PodSpecWrapperBuilder {
	p.spec.MemoryRequests = memory
	return p
}

func (p *PodSpecWrapperBuilder) SetPodAffinity(affinity corev1.PodAffinity) *PodSpecWrapperBuilder {
	p.spec.PodTemplateWrapper.PodTemplate.Spec.Affinity.PodAffinity = &affinity
	return p
}

func (p *PodSpecWrapperBuilder) SetNodeAffinity(affinity corev1.NodeAffinity) *PodSpecWrapperBuilder {
	p.spec.PodTemplateWrapper.PodTemplate.Spec.Affinity.NodeAffinity = &affinity
	return p
}

func (p *PodSpecWrapperBuilder) SetPodAntiAffinityTopologyKey(topologyKey string) *PodSpecWrapperBuilder {
	p.spec.PodAntiAffinityTopologyKey = topologyKey
	return p
}

func (p *PodSpecWrapperBuilder) SetPodTemplate(template *corev1.PodTemplateSpec) *PodSpecWrapperBuilder {
	p.spec.PodTemplateWrapper.PodTemplate = template
	return p
}

func (p *PodSpecWrapperBuilder) SetSinglePersistence(builder *PersistenceConfigBuilder) *PodSpecWrapperBuilder {
	if p.spec.Persistence == nil {
		p.spec.Persistence = &common.Persistence{}
	}
	p.spec.Persistence.SingleConfig = builder.config
	return p
}

func (p *PodSpecWrapperBuilder) SetMultiplePersistence(dataBuilder, journalBuilder, logsBuilder *PersistenceConfigBuilder) *PodSpecWrapperBuilder {
	if p.spec.Persistence == nil {
		p.spec.Persistence = &common.Persistence{}
	}
	p.spec.Persistence.MultipleConfig = &common.MultiplePersistenceConfig{}
	if dataBuilder != nil {
		p.spec.Persistence.MultipleConfig.Data = dataBuilder.config
	}
	if journalBuilder != nil {
		p.spec.Persistence.MultipleConfig.Journal = journalBuilder.config
	}
	if logsBuilder != nil {
		p.spec.Persistence.MultipleConfig.Logs = logsBuilder.config
	}
	return p
}

func (p *PodSpecWrapperBuilder) SetDefault(builder *PodSpecWrapperBuilder) *PodSpecWrapperBuilder {
	p.spec.Default = builder.Build().MongoDbPodSpec
	return p
}

func (p *PodSpecWrapperBuilder) Build() *PodSpecWrapper {
	return p.spec.DeepCopy()
}

func NewPersistenceBuilder(size string) *PersistenceConfigBuilder {
	return &PersistenceConfigBuilder{config: &common.PersistenceConfig{Storage: size}}
}

func (p *PersistenceConfigBuilder) SetStorageClass(class string) *PersistenceConfigBuilder {
	p.config.StorageClass = &class
	return p
}

func (p *PersistenceConfigBuilder) SetLabelSelector(labels map[string]string) *PersistenceConfigBuilder {
	p.config.LabelSelector = &common.LabelSelectorWrapper{LabelSelector: metav1.LabelSelector{MatchLabels: labels}}
	return p
}

func NewPodSpecWithDefaultValues() MongoDbPodSpec {
	defaultPodSpec := MongoDbPodSpec{PodAntiAffinityTopologyKey: "kubernetes.io/hostname"}
	defaultPodSpec.Persistence = &common.Persistence{
		SingleConfig: &common.PersistenceConfig{Storage: "30G"},
		MultipleConfig: &common.MultiplePersistenceConfig{
			Data:    &common.PersistenceConfig{Storage: util.DefaultMongodStorageSize},
			Journal: &common.PersistenceConfig{Storage: util.DefaultJournalStorageSize},
			Logs:    &common.PersistenceConfig{Storage: util.DefaultLogsStorageSize},
		},
	}
	defaultPodSpec.PodTemplateWrapper = NewMongoDbPodSpec().PodTemplateWrapper
	return defaultPodSpec
}
