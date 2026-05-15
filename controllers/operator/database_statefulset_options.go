package operator

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

// CurrentAgentAuthMechanism will assign the given value as the current authentication mechanism.
func CurrentAgentAuthMechanism(mode string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.CurrentAgentAuthMode = mode
	}
}

// PodEnvVars will assign the given env vars which will used during StatefulSet construction.
func PodEnvVars(vars *env.PodEnvVars) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.PodVars = vars
	}
}

// Replicas will set the given number of replicas when building a StatefulSet.
func Replicas(replicas int) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.Replicas = replicas
	}
}

func Name(name string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.Name = name
	}
}

func StatefulSetNameOverride(statefulSetNameOverride string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.StatefulSetNameOverride = statefulSetNameOverride
	}
}

func ServiceName(serviceName string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.ServiceName = serviceName
	}
}

// CertificateHash will assign the given CertificateHash during StatefulSet construction.
func CertificateHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.CertificateHash = hash
	}
}

// AgentCertHash will assign the given AgentCertHash during StatefulSet construction.
func AgentCertHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.AgentCertHash = hash
	}
}

// InternalClusterHash will assign the given InternalClusterHash during StatefulSet construction.
func InternalClusterHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.InternalClusterHash = hash
	}
}

func PrometheusTLSCertHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.PrometheusTLSCertHash = hash
	}
}

// WithLabels will assign the provided labels during the statefulset construction
func WithLabels(labels map[string]string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.Labels = labels
	}
}

// WithStsLabels will assign the provided labels during the statefulset construction
func WithStsLabels(labels map[string]string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.StsLabels = labels
	}
}

// WithVaultConfig sets the vault configuration to extract annotations for the statefulset.
func WithVaultConfig(config vault.VaultConfiguration) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.VaultConfig = config
	}
}

func WithAdditionalMongodConfig(additionalMongodConfig *mdbv1.AdditionalMongodConfig) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.AdditionalMongodConfig = additionalMongodConfig
	}
}

func WithDefaultConfigSrvStorageSize() func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.PodSpec.Default.Persistence.SingleConfig.Storage = util.DefaultConfigSrvStorageSize
	}
}

// WithInitDatabaseNonStaticImage sets the InitDatabaseImage field.
func WithInitDatabaseNonStaticImage(image string) func(*construct.DatabaseStatefulSetOptions) {
	return func(opts *construct.DatabaseStatefulSetOptions) {
		opts.InitDatabaseImage = image
	}
}

// WithDatabaseNonStaticImage sets the DatabaseNonStaticImage field.
func WithDatabaseNonStaticImage(image string) func(*construct.DatabaseStatefulSetOptions) {
	return func(opts *construct.DatabaseStatefulSetOptions) {
		opts.DatabaseNonStaticImage = image
	}
}

// WithMongodbImage sets the MongodbImage field.
func WithMongodbImage(image string) func(*construct.DatabaseStatefulSetOptions) {
	return func(opts *construct.DatabaseStatefulSetOptions) {
		opts.MongodbImage = image
	}
}

// WithAgentImage sets the AgentImage field.
func WithAgentImage(image string) func(*construct.DatabaseStatefulSetOptions) {
	return func(opts *construct.DatabaseStatefulSetOptions) {
		opts.AgentImage = image
	}
}

func WithAgentDebug(debug bool) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.AgentDebug = debug
	}
}

func WithAgentDebugImage(debugImage string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.AgentDebugImage = debugImage
	}
}

// WithoutOwnerReference clears the OwnerReference field on the STS options.
//
// G iter-17d: in distributed multi-cluster mode the central CR's UID is not
// resolvable on member clusters (each member has its own per-cluster CR with
// a distinct server-assigned UID, created by do_distributed_pre_replicate
// at takeover). K8s GC sweeps any STS whose ownerRef points at an
// unresolvable UID — that was the iter-17c-identified root cause of the
// Phase D STS-recreation disruption.
//
// Distributed-mode STS lifecycle is owned by the local operator directly;
// the existing label-driven cleanup in ShardedClusterReconcileHelper.OnDelete
// → deleteClusterResources (label-matched DeleteAllOf) handles CR-delete
// cleanup without relying on the K8s GC ownerRef sweep. Hub-spoke retains
// ownerReferences (the central CR owns member STSes via cross-cluster GC
// in that mode, even though that's fragile — addressed only when the
// coordinator is attached).
func WithoutOwnerReference() func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.OwnerReference = []metav1.OwnerReference{}
	}
}
