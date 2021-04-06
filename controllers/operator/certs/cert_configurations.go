package certs

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/scale"
)

type Options struct {
	// CertSecretName is the name of the secret which contains the certs.
	CertSecretName string
	// ResourceName is the name of the resource.
	ResourceName string
	// Replicas is the number of replicas.
	Replicas int
	// Namespace is the namepsace the resource is in.
	Namespace string
	// ServiceName is the name of the service which is created for the resource.
	ServiceName string
	// ClusterDomain is the cluster domain for the resource
	ClusterDomain                string
	additionalCertificateDomains []string

	// horizons is an array of MongoDBHorizonConfig which is used to determine any
	// additional cert domains required.
	horizons []mdbv1.MongoDBHorizonConfig
}

// StandaloneConfig returns a function which provides all of the configuration options required for the given Standalone.
func StandaloneConfig(mdb mdbv1.MongoDB) Options {
	return Options{
		ResourceName:                 mdb.Name,
		CertSecretName:               getCertNameWithPrefixOrDefault(mdb, mdb.Name),
		Namespace:                    mdb.Namespace,
		ServiceName:                  mdb.ServiceName(),
		Replicas:                     1,
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
	}
}

// ReplicaSetConfig returns a struct which provides all of the configuration options required for the given Replica Set.
func ReplicaSetConfig(mdb mdbv1.MongoDB) Options {
	return Options{
		ResourceName:                 mdb.Name,
		CertSecretName:               getCertNameWithPrefixOrDefault(mdb, mdb.Name),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(&mdb),
		ServiceName:                  mdb.ServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		horizons:                     mdb.Spec.Connectivity.ReplicaSetHorizons,
	}
}

// ShardConfig returns a struct which provides all of the configuration options required for the given shard.
func ShardConfig(mdb mdbv1.MongoDB, shardNum int, scaler scale.ReplicaSetScaler) Options {
	return Options{
		ResourceName:                 mdb.ShardRsName(shardNum),
		CertSecretName:               getCertNameWithPrefixOrDefault(mdb, mdb.ShardRsName(shardNum)),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		ServiceName:                  mdb.ShardServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
	}
}

// MongosConfig returns a struct which provides all of the configuration options required for the given Mongos.
func MongosConfig(mdb mdbv1.MongoDB, scaler scale.ReplicaSetScaler) Options {
	return Options{
		ResourceName:                 mdb.MongosRsName(),
		CertSecretName:               getCertNameWithPrefixOrDefault(mdb, mdb.MongosRsName()),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		ServiceName:                  mdb.ServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
	}
}

// ConfigSrvConfig returns a struct which provides all of the configuration options required for the given ConfigServer.
func ConfigSrvConfig(mdb mdbv1.MongoDB, scaler scale.ReplicaSetScaler) Options {
	return Options{
		ResourceName:                 mdb.ConfigRsName(),
		CertSecretName:               getCertNameWithPrefixOrDefault(mdb, mdb.ConfigRsName()),
		Namespace:                    mdb.Namespace,
		Replicas:                     scale.ReplicasThisReconciliation(scaler),
		ServiceName:                  mdb.ConfigSrvServiceName(),
		ClusterDomain:                mdb.Spec.GetClusterDomain(),
		additionalCertificateDomains: mdb.Spec.Security.TLSConfig.AdditionalCertificateDomains,
		horizons:                     mdb.Spec.Connectivity.ReplicaSetHorizons,
	}
}

// getCertNameWithPrefixOrDefault returns the name of the cert that will store certificates for the given resource.
// this takes into account the tlsConfig.prefix option.
func getCertNameWithPrefixOrDefault(mdb mdbv1.MongoDB, defaultName string) string {
	tlsConfig := mdb.GetSecurity().TLSConfig
	if tlsConfig != nil {
		// use old behaviour if name is specified
		if tlsConfig.SecretRef.Name != "" {
			return tlsConfig.SecretRef.Name
		}

		if tlsConfig.SecretRef.Prefix != "" {
			return fmt.Sprintf("%s-%s-cert", tlsConfig.SecretRef.Prefix, defaultName)
		}
	}
	return defaultName + "-cert"
}
