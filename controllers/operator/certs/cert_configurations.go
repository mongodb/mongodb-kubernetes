package certs

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// X509CertConfigurator provides the methods required for ensuring the existence of X.509 certificates
// for encrypted communications in MongoDB resource
type X509CertConfigurator interface {
	GetName() string
	GetNamespace() string
	GetDbCommonSpec() *mdbv1.DbCommonSpec
	GetCertOptions() []Options
	GetSecretReadClient() secrets.SecretClient
	GetSecretWriteClient() secrets.SecretClient
}

type ReplicaSetX509CertConfigurator struct {
	*mdbv1.MongoDB
	SecretClient secrets.SecretClient
}

var _ X509CertConfigurator = &ReplicaSetX509CertConfigurator{}

func (rs ReplicaSetX509CertConfigurator) GetCertOptions() []Options {
	return []Options{ReplicaSetConfig(*rs.MongoDB)}
}

func (rs ReplicaSetX509CertConfigurator) GetSecretReadClient() secrets.SecretClient {
	return rs.SecretClient
}

func (rs ReplicaSetX509CertConfigurator) GetSecretWriteClient() secrets.SecretClient {
	return rs.SecretClient
}

func (rs ReplicaSetX509CertConfigurator) GetDbCommonSpec() *mdbv1.DbCommonSpec {
	return &rs.Spec.DbCommonSpec
}

type ShardedSetX509CertConfigurator struct {
	*mdbv1.MongoDB
	MemberCluster    multicluster.MemberCluster
	SecretReadClient secrets.SecretClient
	CertOptions      []Options
}

var _ X509CertConfigurator = ShardedSetX509CertConfigurator{}

func (sc ShardedSetX509CertConfigurator) GetCertOptions() []Options {
	return sc.CertOptions
}

func (sc ShardedSetX509CertConfigurator) GetSecretReadClient() secrets.SecretClient {
	return sc.SecretReadClient
}

func (sc ShardedSetX509CertConfigurator) GetSecretWriteClient() secrets.SecretClient {
	return sc.MemberCluster.SecretClient
}

func (sc ShardedSetX509CertConfigurator) GetDbCommonSpec() *mdbv1.DbCommonSpec {
	return &sc.Spec.DbCommonSpec
}

type StandaloneX509CertConfigurator struct {
	*mdbv1.MongoDB
	SecretClient secrets.SecretClient
}

var _ X509CertConfigurator = StandaloneX509CertConfigurator{}

func (s StandaloneX509CertConfigurator) GetCertOptions() []Options {
	return []Options{StandaloneConfig(*s.MongoDB)}
}

func (s StandaloneX509CertConfigurator) GetSecretReadClient() secrets.SecretClient {
	return s.SecretClient
}

func (s StandaloneX509CertConfigurator) GetSecretWriteClient() secrets.SecretClient {
	return s.SecretClient
}

func (s StandaloneX509CertConfigurator) GetDbCommonSpec() *mdbv1.DbCommonSpec {
	return &s.Spec.DbCommonSpec
}

type MongoDBMultiX509CertConfigurator struct {
	*mdbmulti.MongoDBMultiCluster
	ClusterNum        int
	ClusterName       string
	Replicas          int
	SecretReadClient  secrets.SecretClient
	SecretWriteClient secrets.SecretClient
}

var _ X509CertConfigurator = MongoDBMultiX509CertConfigurator{}

func (mdbm MongoDBMultiX509CertConfigurator) GetCertOptions() []Options {
	return []Options{MultiReplicaSetConfig(*mdbm.MongoDBMultiCluster, mdbm.ClusterNum, mdbm.ClusterName, mdbm.Replicas)}
}

func (mdbm MongoDBMultiX509CertConfigurator) GetSecretReadClient() secrets.SecretClient {
	return mdbm.SecretReadClient
}

func (mdbm MongoDBMultiX509CertConfigurator) GetSecretWriteClient() secrets.SecretClient {
	return mdbm.SecretWriteClient
}

func (mdbm MongoDBMultiX509CertConfigurator) GetDbCommonSpec() *mdbv1.DbCommonSpec {
	return &mdbm.Spec.DbCommonSpec
}

type Options struct {
	// CertSecretName is the name of the secret which contains the certs.
	CertSecretName string

	// InternalClusterSecretName is the name of the secret which contains the certs of internal cluster auth.
	InternalClusterSecretName string
	// ResourceName is the name of the resource.
	ResourceName string
	// Replicas is the number of replicas.
	Replicas int
	// Namespace is the namespace the resource is in.
	Namespace string
	// ServiceName is the name of the service which is created for the resource.
	ServiceName string
	// ClusterDomain is the cluster domain for the resource
	ClusterDomain                string
	additionalCertificateDomains []string
	// External domain for external access (if enabled)
	ExternalDomain *string

	// horizons is an array of MongoDBHorizonConfig which is used to determine any
	// additional cert domains required.
	horizons []mdbv1.MongoDBHorizonConfig

	Topology string

	OwnerReference []metav1.OwnerReference
}

// StandaloneConfig returns a function which provides all of the configuration options required for the given Standalone.
func StandaloneConfig(mdb mdbv1.MongoDB) Options {
	return Options{
		ResourceName:                 mdb.Name,
		CertSecretName:               GetCertNameWithPrefixOrDefault(*mdb.GetSecurity(), mdb.Name),
		Namespace:                    mdb.Namespace,
		ServiceName:                  mdb.ServiceName(),
		Replicas:                     1,
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		OwnerReference:               mdb.GetOwnerReferences(),
		ExternalDomain:               mdb.Spec.DbCommonSpec.GetExternalDomain(),
	}
}

// ReplicaSetConfig returns a struct which provides all of the configuration options required for the given Replica Set.
func ReplicaSetConfig(mdb mdbv1.MongoDB) Options {
	return Options{
		ResourceName:                 mdb.Name,
		CertSecretName:               mdb.GetSecurity().MemberCertificateSecretName(mdb.Name),
		InternalClusterSecretName:    mdb.GetSecurity().InternalClusterAuthSecretName(mdb.Name),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(&mdb),
		ServiceName:                  mdb.ServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		horizons:                     mdb.Spec.Connectivity.ReplicaSetHorizons,
		OwnerReference:               mdb.GetOwnerReferences(),
		ExternalDomain:               mdb.Spec.DbCommonSpec.GetExternalDomain(),
	}
}

func AppDBReplicaSetConfig(om *omv1.MongoDBOpsManager) Options {
	mdb := om.Spec.AppDB
	opts := Options{
		ResourceName:              mdb.Name(),
		CertSecretName:            mdb.GetSecurity().MemberCertificateSecretName(mdb.Name()),
		InternalClusterSecretName: mdb.GetSecurity().InternalClusterAuthSecretName(mdb.Name()),
		Namespace:                 mdb.Namespace,
		Replicas:                  scale.ReplicasThisReconciliation(scalers.NewAppDBSingleClusterScaler(om)),
		ServiceName:               mdb.ServiceName(),
		ClusterDomain:             mdb.ClusterDomain,
		OwnerReference:            om.GetOwnerReferences(),
	}

	if mdb.GetSecurity().TLSConfig != nil {
		opts.additionalCertificateDomains = append(opts.additionalCertificateDomains, mdb.GetSecurity().TLSConfig.AdditionalCertificateDomains...)
	}

	return opts
}

func AppDBMultiClusterReplicaSetConfig(om *omv1.MongoDBOpsManager, scaler interfaces.MultiClusterReplicaSetScaler) Options {
	mdb := om.Spec.AppDB
	opts := Options{
		ResourceName:              mdb.NameForCluster(scaler.MemberClusterNum()),
		CertSecretName:            mdb.GetSecurity().MemberCertificateSecretName(mdb.Name()),
		InternalClusterSecretName: mdb.GetSecurity().InternalClusterAuthSecretName(mdb.Name()),
		Namespace:                 mdb.Namespace,
		Replicas:                  scale.ReplicasThisReconciliation(scaler),
		ClusterDomain:             mdb.ClusterDomain,
		OwnerReference:            om.GetOwnerReferences(),
		Topology:                  mdbv1.ClusterTopologyMultiCluster,
	}

	if mdb.GetSecurity().TLSConfig != nil {
		opts.additionalCertificateDomains = append(opts.additionalCertificateDomains, mdb.GetSecurity().TLSConfig.AdditionalCertificateDomains...)
	}

	return opts
}

// ShardConfig returns a struct which provides all the configuration options required for the given shard.
func ShardConfig(mdb mdbv1.MongoDB, shardNum int, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler) Options {
	resourceName := mdb.ShardRsName(shardNum)
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiShardRsName(scaler.MemberClusterNum(), shardNum)
	}

	return Options{
		ResourceName:                 resourceName,
		CertSecretName:               mdb.GetSecurity().MemberCertificateSecretName(mdb.ShardRsName(shardNum)),
		InternalClusterSecretName:    mdb.GetSecurity().InternalClusterAuthSecretName(mdb.ShardRsName(shardNum)),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		ServiceName:                  mdb.ShardServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		OwnerReference:               mdb.GetOwnerReferences(),
		ExternalDomain:               externalDomain,
		Topology:                     mdb.Spec.GetTopology(),
	}
}

// MultiReplicaSetConfig returns a struct which provides all of the configuration required for a given MongoDB Multi Replicaset.
func MultiReplicaSetConfig(mdbm mdbmulti.MongoDBMultiCluster, clusterNum int, clusterName string, replicas int) Options {
	return Options{
		ResourceName:              mdbm.MultiStatefulsetName(clusterNum),
		CertSecretName:            mdbm.Spec.GetSecurity().MemberCertificateSecretName(mdbm.Name),
		InternalClusterSecretName: mdbm.Spec.GetSecurity().InternalClusterAuthSecretName(mdbm.Name),
		Namespace:                 mdbm.Namespace,
		Replicas:                  replicas,
		ClusterDomain:             mdbm.Spec.GetClusterDomain(),
		Topology:                  mdbv1.ClusterTopologyMultiCluster,
		OwnerReference:            mdbm.GetOwnerReferences(),
		ExternalDomain:            mdbm.Spec.GetExternalDomainForMemberCluster(clusterName),
	}
}

// MongosConfig returns a struct which provides all of the configuration options required for the given Mongos.
func MongosConfig(mdb mdbv1.MongoDB, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler) Options {
	resourceName := mdb.MongosRsName()
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiMongosRsName(scaler.MemberClusterNum())
	}

	return Options{
		ResourceName:                 resourceName,
		CertSecretName:               mdb.GetSecurity().MemberCertificateSecretName(mdb.MongosRsName()),
		InternalClusterSecretName:    mdb.GetSecurity().InternalClusterAuthSecretName(mdb.MongosRsName()),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		ServiceName:                  mdb.ServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		OwnerReference:               mdb.GetOwnerReferences(),
		ExternalDomain:               externalDomain,
		Topology:                     mdb.Spec.GetTopology(),
	}
}

// ConfigSrvConfig returns a struct which provides all of the configuration options required for the given ConfigServer.
func ConfigSrvConfig(mdb mdbv1.MongoDB, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler) Options {
	resourceName := mdb.ConfigRsName()
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiConfigRsName(scaler.MemberClusterNum())
	}

	return Options{
		ResourceName:                 resourceName,
		CertSecretName:               mdb.GetSecurity().MemberCertificateSecretName(mdb.ConfigRsName()),
		InternalClusterSecretName:    mdb.GetSecurity().InternalClusterAuthSecretName(mdb.ConfigRsName()),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		ServiceName:                  mdb.ConfigSrvServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		OwnerReference:               mdb.GetOwnerReferences(),
		ExternalDomain:               externalDomain,
		Topology:                     mdb.Spec.GetTopology(),
	}
}

// GetCertNameWithPrefixOrDefault returns the name of the cert that will store certificates for the given resource.
// this takes into account the tlsConfig.prefix option.
func GetCertNameWithPrefixOrDefault(ms mdbv1.Security, defaultName string) string {
	if ms.CertificatesSecretsPrefix != "" {
		return fmt.Sprintf("%s-%s-cert", ms.CertificatesSecretsPrefix, defaultName)
	}

	return defaultName + "-cert"
}
