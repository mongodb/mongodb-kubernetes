package v1

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TODO move this to a separate package ("mongodb") if we decide this is a good idea
type PodSpecWrapperBuilder struct {
	spec PodSpecWrapper
}

type PersistenceConfigBuilder struct {
	config *PersistenceConfig
}

// NewPodSpecWrapperBuilder returns the builder with some default values, used in tests mostly
func NewPodSpecWrapperBuilder() *PodSpecWrapperBuilder {
	spec := MongoDbPodSpec{
		Cpu:            "1.0",
		CpuRequests:    "0.5",
		Memory:         "500M",
		MemoryRequests: "400M",
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
			Persistence: &Persistence{},
		},
		Default: MongoDbPodSpec{
			Persistence: &Persistence{SingleConfig: &PersistenceConfig{}},
		},
	}}
}

func (p *PodSpecWrapperBuilder) SetCpu(cpu string) *PodSpecWrapperBuilder {
	p.spec.Cpu = cpu
	return p
}
func (p *PodSpecWrapperBuilder) SetCpuRequests(cpu string) *PodSpecWrapperBuilder {
	p.spec.CpuRequests = cpu
	return p
}
func (p *PodSpecWrapperBuilder) SetMemory(memory string) *PodSpecWrapperBuilder {
	p.spec.Memory = memory
	return p
}
func (p *PodSpecWrapperBuilder) SetMemoryRequest(memory string) *PodSpecWrapperBuilder {
	p.spec.MemoryRequests = memory
	return p
}

func (p *PodSpecWrapperBuilder) SetPodAffinity(affinity corev1.PodAffinity) *PodSpecWrapperBuilder {
	p.spec.PodAffinity = &affinity
	return p
}

func (p *PodSpecWrapperBuilder) SetNodeAffinity(affinity corev1.NodeAffinity) *PodSpecWrapperBuilder {
	p.spec.NodeAffinity = &affinity
	return p
}

func (p *PodSpecWrapperBuilder) SetPodAntiAffinityTopologyKey(topologyKey string) *PodSpecWrapperBuilder {
	p.spec.PodAntiAffinityTopologyKey = topologyKey
	return p
}

func (p *PodSpecWrapperBuilder) SetPodTemplate(template *corev1.PodTemplateSpec) *PodSpecWrapperBuilder {
	p.spec.PodTemplate = template
	return p
}

func (p *PodSpecWrapperBuilder) SetSinglePersistence(builder *PersistenceConfigBuilder) *PodSpecWrapperBuilder {
	if p.spec.Persistence == nil {
		p.spec.Persistence = &Persistence{}
	}
	p.spec.Persistence.SingleConfig = builder.config
	return p
}

func (p *PodSpecWrapperBuilder) SetMultiplePersistence(dataBuilder, journalBuilder, logsBuilder *PersistenceConfigBuilder) *PodSpecWrapperBuilder {
	if p.spec.Persistence == nil {
		p.spec.Persistence = &Persistence{}
	}
	p.spec.Persistence.MultipleConfig = &MultiplePersistenceConfig{}
	if dataBuilder != nil {
		p.spec.Persistence.MultipleConfig.Data = *dataBuilder.config
	}
	if journalBuilder != nil {
		p.spec.Persistence.MultipleConfig.Journal = *journalBuilder.config
	}
	if logsBuilder != nil {
		p.spec.Persistence.MultipleConfig.Logs = *logsBuilder.config
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
	return &PersistenceConfigBuilder{config: &PersistenceConfig{Storage: size}}
}

func (p *PersistenceConfigBuilder) SetStorageClass(class string) *PersistenceConfigBuilder {
	p.config.StorageClass = &class
	return p
}
func (p *PersistenceConfigBuilder) SetLabelSelector(labels map[string]string) *PersistenceConfigBuilder {
	p.config.LabelSelector = &metav1.LabelSelector{MatchLabels: labels}
	return p
}

func NewPodSpecWithDefaultValues() MongoDbPodSpec {
	defaultPodSpec := MongoDbPodSpec{PodAntiAffinityTopologyKey: "kubernetes.io/hostname"}
	defaultPodSpec.Persistence = &Persistence{
		SingleConfig: &PersistenceConfig{Storage: "30G"},
		MultipleConfig: &MultiplePersistenceConfig{
			Data:    PersistenceConfig{Storage: util.DefaultMongodStorageSize},
			Journal: PersistenceConfig{Storage: util.DefaultJournalStorageSize},
			Logs:    PersistenceConfig{Storage: util.DefaultLogsStorageSize},
		},
	}
	return defaultPodSpec
}
