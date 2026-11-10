package certs

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
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
	GetOwnerReferences() []metav1.OwnerReference
	GetOwnerLabels() map[string]string
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

// GetOwnerReferences returns the owner reference to put on the secrets the operator writes for the
// agent. The replica set runs in the same cluster as the resource, so it can point at it.
func (rs ReplicaSetX509CertConfigurator) GetOwnerReferences() []metav1.OwnerReference {
	return OwnerReferenceFor(rs.GetSecurity(), rs.MongoDB, true)
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

// GetOwnerReferences returns the owner reference to put on the secrets the operator writes for the
// agent. The secrets of a single-cluster sharded cluster are in the cluster that holds the resource, so
// they can point at it; those of a multi-cluster one are in the member clusters, where they cannot.
func (sc ShardedSetX509CertConfigurator) GetOwnerReferences() []metav1.OwnerReference {
	return OwnerReferenceFor(sc.GetSecurity(), sc.MongoDB, !sc.Spec.IsMultiCluster())
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

// GetOwnerReferences returns the owner reference to put on the secrets the operator writes for the
// agent. The standalone runs in the same cluster as the resource, so it can point at it.
func (s StandaloneX509CertConfigurator) GetOwnerReferences() []metav1.OwnerReference {
	return OwnerReferenceFor(s.GetSecurity(), s.MongoDB, true)
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

// GetOwnerReferences returns the owner reference to put on the secrets the operator writes for the
// agent. Every secret of a multi-cluster replica set is in a member cluster, so none can point at the
// resource: the owner labels remove them instead.
func (mdbm MongoDBMultiX509CertConfigurator) GetOwnerReferences() []metav1.OwnerReference {
	return OwnerReferenceFor(mdbm.GetSecurity(), mdbm.MongoDBMultiCluster, false)
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

	OwnerReference []metav1.OwnerReference

	// OwnerLabels are the labels the operator puts on the objects it creates for a resource, so its
	// cleanup on deletion finds them by label. Secrets cannot carry an owner reference across
	// clusters, so the copies of the -pem secrets in member clusters rely on these labels.
	OwnerLabels map[string]string
}

// MemberCluster describes one member cluster's details to figure out the union SAN list of a
// multi-cluster member certificate.
type MemberCluster struct {
	ClusterNum     int
	ClusterName    string
	Members        int
	ExternalDomain *string
}

// ownerLabelsFor returns the labels the operator puts on the secrets it creates for a resource, or
// nothing when the resource does not use operator-managed certificates. The labels decide what the
// cleanup on deletion removes, and only the managed-certificate feature relies on that today: a
// deployment that brings its own certificates keeps the behaviour it had.
func ownerLabelsFor(sec *mdbv1.Security, labels map[string]string) map[string]string {
	if sec == nil || !sec.IsManagedCertificateEnabled() {
		return nil
	}
	return labels
}

// OwnerReferenceFor returns the owner reference the operator puts on the -pem secrets it writes itself
// for a resource, or nothing when it must not set one.
//
// A deployment that brings its own certificates keeps the references it had. With operator-managed
// certificates the reference points at the resource itself, so Kubernetes deletes the secrets together
// with it. Only a secret in the cluster that holds the resource can have one: Kubernetes deletes a
// dependent whose owner it cannot find, so a copy in a member cluster must not carry a reference to a
// resource of another cluster. Those copies are removed by the owner labels instead.
//
// ownCluster says whether the secrets being written are in the cluster that holds the resource.
func OwnerReferenceFor(sec *mdbv1.Security, owner v1.ObjectOwner, ownCluster bool) []metav1.OwnerReference {
	if sec == nil || !sec.IsManagedCertificateEnabled() {
		return owner.GetOwnerReferences()
	}
	if !ownCluster {
		return nil
	}
	return kube.BaseOwnerReference(owner)
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
		OwnerReference:               OwnerReferenceFor(mdb.GetSecurity(), &mdb, true),
		OwnerLabels:                  ownerLabelsFor(mdb.GetSecurity(), mdb.GetOwnerLabels()),
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
		OwnerReference:               OwnerReferenceFor(mdb.GetSecurity(), &mdb, true),
		OwnerLabels:                  ownerLabelsFor(mdb.GetSecurity(), mdb.GetOwnerLabels()),
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
func shardedComponentConfig(mdb mdbv1.MongoDB, resourceName, componentName, serviceName string, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler) Options {
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
		OwnerReference:               OwnerReferenceFor(mdb.GetSecurity(), &mdb, !mdb.Spec.IsMultiCluster()),
		OwnerLabels:                  ownerLabelsFor(mdb.GetSecurity(), mdb.GetOwnerLabels()),
		ExternalDomain:               externalDomain,
		Topology:                     mdb.Spec.GetTopology(),
	}
}

// ShardConfig returns a struct which provides all the configuration options required for the given shard.
func ShardConfig(mdb mdbv1.MongoDB, shardNum int, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler) Options {
	resourceName := mdb.ShardName(shardNum)
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiShardRsName(scaler.MemberClusterNum(), shardNum)
	}

	return shardedComponentConfig(mdb, resourceName, mdb.ShardName(shardNum), mdb.ShardServiceName(), externalDomain, scaler)
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
		OwnerReference:            OwnerReferenceFor(mdbm.GetSecurity(), &mdbm, false),
		OwnerLabels:               ownerLabelsFor(mdbm.GetSecurity(), mdbm.GetOwnerLabels()),
		ExternalDomain:            mdbm.Spec.GetExternalDomainForMemberCluster(clusterName),
	}
}

// MultiReplicaSetManagedCertConfig builds the single Options type for the managed member certificate of a
// multi-cluster replica set. All member clusters share one member cert, so its SANs are the union
// across clusters.
func MultiReplicaSetManagedCertConfig(mdbm mdbmulti.MongoDBMultiCluster, clusters []MemberCluster) Options {
	return Options{
		ResourceName:              mdbm.Name,
		CertSecretName:            mdbm.Spec.GetSecurity().MemberCertificateSecretName(mdbm.Name),
		InternalClusterSecretName: mdbm.Spec.GetSecurity().InternalClusterAuthSecretName(mdbm.Name),
		Namespace:                 mdbm.Namespace,
		ClusterDomain:             mdbm.Spec.GetClusterDomain(),
		Topology:                  mdbv1.ClusterTopologyMultiCluster,
		OwnerReference:            OwnerReferenceFor(mdbm.GetSecurity(), &mdbm, false),
		OwnerLabels:               ownerLabelsFor(mdbm.GetSecurity(), mdbm.GetOwnerLabels()),
		MultiClusterMembers:       clusters,
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
func MultiShardedComponentManagedCertConfig(sc mdbv1.MongoDB, baseName string, clusters []MemberCluster) Options {
	return Options{
		ResourceName:                 baseName,
		CertSecretName:               sc.GetSecurity().MemberCertificateSecretName(baseName),
		InternalClusterSecretName:    sc.GetSecurity().InternalClusterAuthSecretName(baseName),
		Namespace:                    sc.Namespace,
		ClusterDomain:                sc.Spec.GetClusterDomain(),
		Topology:                     mdbv1.ClusterTopologyMultiCluster,
		OwnerReference:               OwnerReferenceFor(sc.GetSecurity(), &sc, false),
		OwnerLabels:                  ownerLabelsFor(sc.GetSecurity(), sc.GetOwnerLabels()),
		MultiClusterMembers:          clusters,
		additionalCertificateDomains: sc.Spec.Security.TLSConfig.AdditionalCertificateDomains,
	}
}

// MongosConfig returns a struct which provides all of the configuration options required for the given Mongos.
func MongosConfig(mdb mdbv1.MongoDB, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler) Options {
	resourceName := mdb.MongosRsName()
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiMongosRsName(scaler.MemberClusterNum())
	}

	return shardedComponentConfig(mdb, resourceName, mdb.MongosRsName(), mdb.ServiceName(), externalDomain, scaler)
}

// ConfigSrvConfig returns a struct which provides all of the configuration options required for the given ConfigServer.
func ConfigSrvConfig(mdb mdbv1.MongoDB, externalDomain *string, scaler interfaces.MultiClusterReplicaSetScaler) Options {
	resourceName := mdb.ConfigRsName()
	if mdb.Spec.IsMultiCluster() {
		resourceName = mdb.MultiConfigRsName(scaler.MemberClusterNum())
	}

	return shardedComponentConfig(mdb, resourceName, mdb.ConfigRsName(), mdb.ConfigSrvServiceName(), externalDomain, scaler)
}
