package v1

type OpsManagerBuilder struct {
	om MongoDBOpsManager
}

func NewOpsManagerBuilder() *OpsManagerBuilder {
	return &OpsManagerBuilder{}
}

func NewOpsManagerBuilderFromResource(resource MongoDBOpsManager) *OpsManagerBuilder {
	return &OpsManagerBuilder{om: resource}
}

func (b *OpsManagerBuilder) SetVersion(version string) *OpsManagerBuilder {
	b.om.Spec.Version = version
	return b
}

func (b *OpsManagerBuilder) SetAppDbVersion(version string) *OpsManagerBuilder {
	b.om.Spec.AppDB.Version = version
	return b
}

func (b *OpsManagerBuilder) SetClusterDomain(clusterDomain string) *OpsManagerBuilder {
	b.om.Spec.ClusterDomain = clusterDomain
	return b
}

func (b *OpsManagerBuilder) SetAppDbMembers(members int) *OpsManagerBuilder {
	b.om.Spec.AppDB.Members = members
	return b
}

func (b *OpsManagerBuilder) SetAppDbFeatureCompatibility(version string) *OpsManagerBuilder {
	b.om.Spec.AppDB.FeatureCompatibilityVersion = &version
	return b
}

func (b *OpsManagerBuilder) SetAppDBPassword(secretName, key string) *OpsManagerBuilder {
	b.om.Spec.AppDB.PasswordSecretKeyRef = &SecretKeyRef{Name: secretName, Key: key}
	return b
}

func (b *OpsManagerBuilder) SetPodSpec(podSpec PodSpecWrapper) *OpsManagerBuilder {
	b.om.Spec.PodSpec = &podSpec.MongoDbPodSpec
	return b
}

func (b *OpsManagerBuilder) AddConfiguration(key, value string) *OpsManagerBuilder {
	b.om.AddConfigIfDoesntExist(key, value)
	return b
}

func (b *OpsManagerBuilder) AddS3SnapshotStore(config S3Config) *OpsManagerBuilder {
	if b.om.Spec.Backup == nil {
		b.om.Spec.Backup = newBackup()
	}
	if b.om.Spec.Backup.S3Configs == nil {
		b.om.Spec.Backup.S3Configs = []S3Config{}
	}
	b.om.Spec.Backup.S3Configs = append(b.om.Spec.Backup.S3Configs, config)
	return b
}

func (b *OpsManagerBuilder) Build() MongoDBOpsManager {
	b.om.InitDefaultFields()
	return *b.om.DeepCopy()
}
