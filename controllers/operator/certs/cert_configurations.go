package certs

import (
	"context"

	"go.uber.org/zap"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/scale"
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

// appDBManagedCertificateOwner presents the internal AppDB's MongoDB security to the shared
// issuance helpers: certificates, secrets and subjects are named after the AppDB while the
// Ops Manager resource owns every created object.
type appDBManagedCertificateOwner struct {
	*omv1.MongoDBOpsManager
}

var _ CertificateOwner = appDBManagedCertificateOwner{}

func (o appDBManagedCertificateOwner) GetName() string { return o.AppDBName() }
func (o appDBManagedCertificateOwner) GetSecurity() *mdbv1.Security {
	return o.Spec.AppDB.GetSecurity()
}

func (o appDBManagedCertificateOwner) ownerResource() v1.ObjectOwner {
	return o.MongoDBOpsManager
}

// EnsureAppDBCertificatesAndCA issues the internal AppDB's certificates and CA bundles. The
// Ops Manager resource owns every created object; names, secrets and subjects derive from the
// AppDB name.
func EnsureAppDBCertificatesAndCA(
	ctx context.Context,
	c kubernetesClient.Client,
	om *omv1.MongoDBOpsManager,
	allOpts []Options,
	log *zap.SugaredLogger,
) (workflow.Status, metav1.Condition) {
	if om == nil || om.Spec.AppDB == nil {
		return workflow.OK(), certificatesReadyCondition(metav1.ConditionTrue, reasonAllCertificatesReady, "Operator-managed certificates are not required for this resource")
	}
	return EnsureCertificatesAndCA(ctx, c, appDBManagedCertificateOwner{om}, allOpts, log)
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
	// MembersToCover is how many members the member/server cert's SANs should cover this reconcile and
	// is max(current, desired). On a scale-up this is the target size, so the SANs cover all target
	// members at once and the cert is issued a single time, instead of growing the SANs (and reissuing
	// the cert) at every one-member step. On a scale-down it equals the current member count, which
	// decreases one by one. The SANs are then held steady by the count recorded in the Certificate
	// annotation, not by this field.
	MembersToCover int
	// MultiClusterMembers is set in multi cluster scenarios and is used to figure out the union
	// of the member's SAN across all the clusters while creating the shared member certificate.
	MultiClusterMembers []MemberCluster
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

	// WithholdsClientCertificate is true when the mongod configuration of the component these options
	// are for sets setParameter.tlsWithholdClientCertificate using MongoDB CR. It only matters when x509 is not the
	// internal cluster authentication because in that the member cert is used/issued with dual EKU. To make
	// sure the member cert is not issued with dual EKU, customers can set setParameter.tlsWithholdClientCertificate
	// and member certs would just have serverAuth EKU because in that case member certs are not treated as client certs.
	WithholdsClientCertificate bool

	OwnerReference []metav1.OwnerReference
}

// MemberCluster describes one member cluster's details to figure out the union SAN list of a
// multi-cluster member certificate.
type MemberCluster struct {
	ClusterNum     int
	ClusterName    string
	Members        int
	ExternalDomain *string
}

// WithoutHostnames returns a copy of the options that asks the certificate for none of the hostnames
// the deployment is reached by: the external domain, the additional certificate domains and the
// replica set horizons. The rest of the options, such as where the -pem secret is written, is kept.
func (o Options) WithoutHostnames() Options {
	o.ExternalDomain = nil
	o.additionalCertificateDomains = nil
	o.horizons = nil
	return o
}

// StandaloneConfig returns a function which provides all of the configuration options required for the given Standalone.
func StandaloneConfig(mdb mdbv1.MongoDB) Options {
	return Options{
		ResourceName:                 mdb.Name,
		CertSecretName:               mdb.GetSecurity().MemberCertificateSecretName(mdb.Name),
		Namespace:                    mdb.Namespace,
		ServiceName:                  mdb.ServiceName(),
		Replicas:                     1,
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		OwnerReference:               mdb.GetOwnerReferences(),
		WithholdsClientCertificate:   WithholdsClientCertificate(mdb.Spec.AdditionalMongodConfig),
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
		MembersToCover:               max(mdb.CurrentReplicas(), mdb.DesiredReplicas()),
		ServiceName:                  mdb.ServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		horizons:                     mdb.Spec.Connectivity.ReplicaSetHorizons,
		OwnerReference:               mdb.GetOwnerReferences(),
		WithholdsClientCertificate:   WithholdsClientCertificate(mdb.Spec.AdditionalMongodConfig),
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

// shardedComponentConfig returns the certificate options of one component of a sharded cluster: a
// shard, the config server or the mongos. The three names that differ between the components come
// from the caller: the StatefulSet the component runs under (which in a multi-cluster deployment is
// per member cluster), the component's hostname base, and its service.
func shardedComponentConfig(
	mdb mdbv1.MongoDB,
	resourceName, componentName, serviceName string,
	externalDomain *string,
	scaler interfaces.MultiClusterReplicaSetScaler,
	mongodConfig *mdbv1.AdditionalMongodConfig,
) Options {
	return Options{
		ResourceName:                 resourceName,
		CertSecretName:               mdb.GetSecurity().MemberCertificateSecretName(componentName),
		InternalClusterSecretName:    mdb.GetSecurity().InternalClusterAuthSecretName(componentName),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		MembersToCover:               max(scaler.CurrentReplicas(), scaler.DesiredReplicas()),
		ServiceName:                  serviceName,
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		OwnerReference:               mdb.GetOwnerReferences(),
		WithholdsClientCertificate:   WithholdsClientCertificate(mongodConfig),
		ExternalDomain:               externalDomain,
		Topology:                     mdb.Spec.GetTopology(),
	}
}

// ShardConfig returns a struct which provides all the configuration options required for the given shard.
func ShardConfig(
	mdb mdbv1.MongoDB,
	shardNum int,
	externalDomain *string,
	scaler interfaces.MultiClusterReplicaSetScaler,
	mongodConfig *mdbv1.AdditionalMongodConfig,
) Options {
	resourceName := mdb.ShardName(shardNum)
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiShardRsName(scaler.MemberClusterNum(), shardNum)
	}

	return Options{
		ResourceName:                 resourceName,
		CertSecretName:               mdb.GetSecurity().MemberCertificateSecretName(mdb.ShardName(shardNum)),
		InternalClusterSecretName:    mdb.GetSecurity().InternalClusterAuthSecretName(mdb.ShardName(shardNum)),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		MembersToCover:               max(scaler.CurrentReplicas(), scaler.DesiredReplicas()),
		ServiceName:                  mdb.ShardServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		OwnerReference:               mdb.GetOwnerReferences(),
		WithholdsClientCertificate:   WithholdsClientCertificate(mongodConfig),
		ExternalDomain:               externalDomain,
		Topology:                     mdb.Spec.GetTopology(),
	}
}

// MultiReplicaSetConfig returns a struct which provides all of the configuration required for a given MongoDB Multi Replicaset.
func MultiReplicaSetConfig(mdbm mdbmulti.MongoDBMultiCluster, clusterNum int, clusterName string, replicas int) Options {
	return Options{
		ResourceName:               mdbm.MultiStatefulsetName(clusterNum),
		CertSecretName:             mdbm.Spec.GetSecurity().MemberCertificateSecretName(mdbm.Name),
		InternalClusterSecretName:  mdbm.Spec.GetSecurity().InternalClusterAuthSecretName(mdbm.Name),
		Namespace:                  mdbm.Namespace,
		Replicas:                   replicas,
		ClusterDomain:              mdbm.Spec.GetClusterDomain(),
		Topology:                   mdbv1.ClusterTopologyMultiCluster,
		OwnerReference:             mdbm.GetOwnerReferences(),
		WithholdsClientCertificate: WithholdsClientCertificate(mdbm.Spec.AdditionalMongodConfig),
		ExternalDomain:             mdbm.Spec.GetExternalDomainForMemberCluster(clusterName),
	}
}

// MultiReplicaSetManagedCertConfig builds the single Options type for the managed member certificate of a
// multi-cluster replica set. All member clusters share one member cert, so its SANs are the union
// across clusters.
func MultiReplicaSetManagedCertConfig(mdbm mdbmulti.MongoDBMultiCluster, clusters []MemberCluster) Options {
	return Options{
		ResourceName:               mdbm.Name,
		CertSecretName:             mdbm.Spec.GetSecurity().MemberCertificateSecretName(mdbm.Name),
		InternalClusterSecretName:  mdbm.Spec.GetSecurity().InternalClusterAuthSecretName(mdbm.Name),
		Namespace:                  mdbm.Namespace,
		ClusterDomain:              mdbm.Spec.GetClusterDomain(),
		Topology:                   mdbv1.ClusterTopologyMultiCluster,
		OwnerReference:             mdbm.GetOwnerReferences(),
		WithholdsClientCertificate: WithholdsClientCertificate(mdbm.Spec.AdditionalMongodConfig),
		MultiClusterMembers:        clusters,
	}
}

// MultiShardedComponentManagedCertConfig builds the single Options type for the managed member
// certificate of one sharded component (mongos, the config server, or one shard) in a multi-cluster
// sharded cluster. Every member cluster that runs that component shares the one certificate, so its
// SANs are the union across those clusters.
//
// baseName is the component's hostname base, MongosRsName(), ConfigRsName() or ShardName(i), not
// the per-cluster StatefulSet name: the pods are addressed as
// <baseName>-<cluster index>-<pod>-svc.<namespace>.svc.<clusterDomain>, so passing the StatefulSet
// name would put the cluster index in the hostname twice.
func MultiShardedComponentManagedCertConfig(
	sc mdbv1.MongoDB,
	baseName string,
	clusters []MemberCluster,
	mongodConfig *mdbv1.AdditionalMongodConfig,
) Options {
	return Options{
		ResourceName:                 baseName,
		CertSecretName:               sc.GetSecurity().MemberCertificateSecretName(baseName),
		InternalClusterSecretName:    sc.GetSecurity().InternalClusterAuthSecretName(baseName),
		Namespace:                    sc.Namespace,
		ClusterDomain:                sc.Spec.GetClusterDomain(),
		Topology:                     mdbv1.ClusterTopologyMultiCluster,
		OwnerReference:               sc.GetOwnerReferences(),
		WithholdsClientCertificate:   WithholdsClientCertificate(mongodConfig),
		MultiClusterMembers:          clusters,
		additionalCertificateDomains: sc.Spec.Security.TLSConfig.AdditionalCertificateDomains,
	}
}

// MongosConfig returns a struct which provides all of the configuration options required for the given Mongos.
func MongosConfig(
	mdb mdbv1.MongoDB,
	externalDomain *string,
	scaler interfaces.MultiClusterReplicaSetScaler,
	mongodConfig *mdbv1.AdditionalMongodConfig,
) Options {
	resourceName := mdb.MongosRsName()
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiMongosRsName(scaler.MemberClusterNum())
	}

	return shardedComponentConfig(mdb, resourceName, mdb.MongosRsName(), mdb.ServiceName(), externalDomain, scaler, mongodConfig)
}

// ConfigSrvConfig returns a struct which provides all of the configuration options required for the given ConfigServer.
func ConfigSrvConfig(mdb mdbv1.MongoDB, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler, mongodConfig *mdbv1.AdditionalMongodConfig) Options {
	resourceName := mdb.ConfigRsName()
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiConfigRsName(scaler.MemberClusterNum())
	}

	return shardedComponentConfig(mdb, resourceName, mdb.ConfigRsName(), mdb.ConfigSrvServiceName(), externalDomain, scaler, mongodConfig)
}
