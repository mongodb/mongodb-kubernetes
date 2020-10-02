package om

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	userv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/user"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
)

type OpsManagerBuilder struct {
	om MongoDBOpsManager
}

func NewOpsManagerBuilder() *OpsManagerBuilder {
	return &OpsManagerBuilder{}
}

func NewOpsManagerBuilderDefault() *OpsManagerBuilder {
	return NewOpsManagerBuilder().SetVersion("4.4.1").SetAppDbMembers(3)
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

func (b *OpsManagerBuilder) SetAppDBTLSConfig(config mdbv1.TLSConfig) *OpsManagerBuilder {
	if b.om.Spec.AppDB.Security == nil {
		b.om.Spec.AppDB.Security = &mdbv1.Security{}
	}

	b.om.Spec.AppDB.Security.TLSConfig = &config
	return b
}

func (b *OpsManagerBuilder) AddS3Config(s3ConfigName, credentialsName string) *OpsManagerBuilder {
	if b.om.Spec.Backup == nil {
		b.om.Spec.Backup = &MongoDBOpsManagerBackup{Enabled: true}
	}
	if b.om.Spec.Backup.S3Configs == nil {
		b.om.Spec.Backup.S3Configs = []S3Config{}
	}
	b.om.Spec.Backup.S3Configs = append(b.om.Spec.Backup.S3Configs, S3Config{
		S3SecretRef: SecretRef{
			Name: credentialsName,
		},
		Name: s3ConfigName,
	})
	return b
}

func (b *OpsManagerBuilder) AddOplogStoreConfig(oplogStoreName, userName string, mdbNsName types.NamespacedName) *OpsManagerBuilder {
	if b.om.Spec.Backup == nil {
		b.om.Spec.Backup = &MongoDBOpsManagerBackup{Enabled: true}
	}
	if b.om.Spec.Backup.OplogStoreConfigs == nil {
		b.om.Spec.Backup.OplogStoreConfigs = []DataStoreConfig{}
	}
	b.om.Spec.Backup.OplogStoreConfigs = append(b.om.Spec.Backup.OplogStoreConfigs, DataStoreConfig{
		Name: oplogStoreName,
		MongoDBResourceRef: userv1.MongoDBResourceRef{
			Name:      mdbNsName.Name,
			Namespace: mdbNsName.Namespace,
		},
		MongoDBUserRef: &MongoDBUserRef{
			Name: userName,
		},
	})
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

func (b *OpsManagerBuilder) SetAdditionalMongodbConfig(config mdbv1.AdditionalMongodConfig) *OpsManagerBuilder {
	b.om.Spec.AppDB.AdditionalMongodConfig = config
	return b
}

func (b *OpsManagerBuilder) SetAppDbFeatureCompatibility(version string) *OpsManagerBuilder {
	b.om.Spec.AppDB.FeatureCompatibilityVersion = &version
	return b
}

func (b *OpsManagerBuilder) SetStatefulSetSpec(customSpec appsv1.StatefulSetSpec) *OpsManagerBuilder {
	b.om.Spec.StatefulSetConfiguration = &mdbv1.StatefulSetConfiguration{Spec: customSpec}
	return b
}

func (b *OpsManagerBuilder) SetAppDBPassword(secretName, key string) *OpsManagerBuilder {
	b.om.Spec.AppDB.PasswordSecretKeyRef = &userv1.SecretKeyRef{Name: secretName, Key: key}
	return b
}

func (b *OpsManagerBuilder) SetBackup(backup MongoDBOpsManagerBackup) *OpsManagerBuilder {
	b.om.Spec.Backup = &backup
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

func (b *OpsManagerBuilder) SetOMStatusVersion(version string) *OpsManagerBuilder {
	b.om.Status.OpsManagerStatus.Version = version
	return b
}

func (b *OpsManagerBuilder) Build() MongoDBOpsManager {
	b.om.InitDefaultFields()
	return *b.om.DeepCopy()
}

// ************************* Private methods ************************************
