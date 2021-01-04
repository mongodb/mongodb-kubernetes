package operator

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
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

// CertificateHash will assign the given CertificateHash during StatefulSet construction.
func CertificateHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.CertificateHash = hash
	}
}
