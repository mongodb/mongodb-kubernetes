package om

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	mdbc "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
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
	return NewOpsManagerBuilder().SetName("default-om").SetVersion("4.4.1").SetAppDbMembers(3).SetAppDbPodSpec(*DefaultAppDbBuilder().Build().PodSpec).SetAppDbVersion("4.2.20")
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

func (b *OpsManagerBuilder) SetAppDbPodSpec(podSpec mdbv1.MongoDbPodSpec) *OpsManagerBuilder {
	b.om.Spec.AppDB.PodSpec = &podSpec
	return b
}

func (b *OpsManagerBuilder) SetOpsManagerConfig(config mdbv1.PrivateCloudConfig) *OpsManagerBuilder {
	b.om.Spec.AppDB.OpsManagerConfig = &config
	return b
}

func (b *OpsManagerBuilder) SetCloudManagerConfig(config mdbv1.PrivateCloudConfig) *OpsManagerBuilder {
	b.om.Spec.AppDB.CloudManagerConfig = &config
	return b
}

func (b *OpsManagerBuilder) SetAppDbConnectivity(connectivitySpec mdbv1.MongoDBConnectivity) *OpsManagerBuilder {
	b.om.Spec.AppDB.Connectivity = &connectivitySpec
	return b
}

func (b *OpsManagerBuilder) SetAppDBTLSConfig(config mdbv1.TLSConfig) *OpsManagerBuilder {
	if b.om.Spec.AppDB.Security == nil {
		b.om.Spec.AppDB.Security = &mdbv1.Security{}
	}

	b.om.Spec.AppDB.Security.TLSConfig = &config
	return b
}

func (b *OpsManagerBuilder) SetTLSConfig(config MongoDBOpsManagerTLS) *OpsManagerBuilder {
	if b.om.Spec.Security == nil {
		b.om.Spec.Security = &MongoDBOpsManagerSecurity{}
	}

	b.om.Spec.Security.TLS = config
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

func (b *OpsManagerBuilder) AddBlockStoreConfig(blockStoreName, userName string, mdbNsName types.NamespacedName) *OpsManagerBuilder {
	if b.om.Spec.Backup == nil {
		b.om.Spec.Backup = &MongoDBOpsManagerBackup{Enabled: true}
	}
	if b.om.Spec.Backup.BlockStoreConfigs == nil {
		b.om.Spec.Backup.BlockStoreConfigs = []DataStoreConfig{}
	}
	b.om.Spec.Backup.BlockStoreConfigs = append(b.om.Spec.Backup.BlockStoreConfigs, DataStoreConfig{
		Name: blockStoreName,
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

func (b *OpsManagerBuilder) SetName(name string) *OpsManagerBuilder {
	b.om.Name = name
	return b
}

func (b *OpsManagerBuilder) SetNamespace(namespace string) *OpsManagerBuilder {
	b.om.Namespace = namespace
	return b
}

func (b *OpsManagerBuilder) SetAppDbMembers(members int) *OpsManagerBuilder {
	b.om.Spec.AppDB.Members = members
	return b
}

func (b *OpsManagerBuilder) SetAppDbCredentials(credentials string) *OpsManagerBuilder {
	b.om.Spec.AppDB.Credentials = credentials
	return b
}

func (b *OpsManagerBuilder) SetBackupMembers(members int) *OpsManagerBuilder {
	if b.om.Spec.Backup == nil {
		b.om.Spec.Backup = &MongoDBOpsManagerBackup{Enabled: true}
	}
	b.om.Spec.Backup.Members = members
	return b
}

func (b *OpsManagerBuilder) SetAdditionalMongodbConfig(config *mdbv1.AdditionalMongodConfig) *OpsManagerBuilder {
	b.om.Spec.AppDB.AdditionalMongodConfig = config
	return b
}

func (b *OpsManagerBuilder) SetAppDbFeatureCompatibility(version string) *OpsManagerBuilder {
	b.om.Spec.AppDB.FeatureCompatibilityVersion = &version
	return b
}

func (b *OpsManagerBuilder) SetStatefulSetSpec(customSpec appsv1.StatefulSetSpec) *OpsManagerBuilder {
	b.om.Spec.StatefulSetConfiguration = &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{Spec: customSpec}}
	return b
}

func (b *OpsManagerBuilder) SetLogRotate(logRotate *automationconfig.CrdLogRotate) *OpsManagerBuilder {
	b.om.Spec.AppDB.AutomationAgent.LogRotate = logRotate
	return b
}

func (b *OpsManagerBuilder) SetSystemLog(systemLog *automationconfig.SystemLog) *OpsManagerBuilder {
	b.om.Spec.AppDB.AutomationAgent.SystemLog = systemLog
	return b
}

func (b *OpsManagerBuilder) SetAppDBPassword(secretName, key string) *OpsManagerBuilder {
	b.om.Spec.AppDB.PasswordSecretKeyRef = &userv1.SecretKeyRef{Name: secretName, Key: key}
	return b
}

func (b *OpsManagerBuilder) SetAppDBAutomationConfigOverride(acOverride mdbc.AutomationConfigOverride) *OpsManagerBuilder {
	b.om.Spec.AppDB.AutomationConfigOverride = &acOverride
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

func (b *OpsManagerBuilder) SetInternalConnectivity(internalConnectivity MongoDBOpsManagerServiceDefinition) *OpsManagerBuilder {
	b.om.Spec.InternalConnectivity = &internalConnectivity
	return b
}

func (b *OpsManagerBuilder) SetExternalConnectivity(externalConnectivity MongoDBOpsManagerServiceDefinition) *OpsManagerBuilder {
	b.om.Spec.MongoDBOpsManagerExternalConnectivity = &externalConnectivity
	return b
}

func (b *OpsManagerBuilder) SetAppDBTopology(topology string) *OpsManagerBuilder {
	b.om.Spec.AppDB.Topology = topology
	return b
}

func (b *OpsManagerBuilder) SetOpsManagerTopology(topology string) *OpsManagerBuilder {
	b.om.Spec.Topology = topology
	return b
}

func (b *OpsManagerBuilder) SetAppDBClusterSpecList(clusterSpecItems mdbv1.ClusterSpecList) *OpsManagerBuilder {
	b.om.Spec.AppDB.ClusterSpecList = append(b.om.Spec.AppDB.ClusterSpecList, clusterSpecItems...)
	return b
}

func (b *OpsManagerBuilder) SetOpsManagerClusterSpecList(clusterSpecItems []ClusterSpecOMItem) *OpsManagerBuilder {
	b.om.Spec.ClusterSpecList = append(b.om.Spec.ClusterSpecList, clusterSpecItems...)
	return b
}

func (b *OpsManagerBuilder) Build() *MongoDBOpsManager {
	b.om.InitDefaultFields()
	return b.om.DeepCopy()
}

// ************************* Private methods ************************************
