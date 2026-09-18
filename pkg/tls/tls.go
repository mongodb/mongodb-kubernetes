package tls

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

type Mode string

const (
	Disabled              Mode = "disabled"
	Require               Mode = "requireTLS"
	Prefer                Mode = "preferTLS"
	Allow                 Mode = "allowTLS"
	ConfigMapVolumeCAName      = "secret-ca"
	CAConfigMapKey             = "ca-pem"
	// ClusterCAConfigMapKey is the key holding the client-category (clientAuth) trust bundle
	// in the operator-owned client CA ConfigMap in managed-certificate mode.
	ClusterCAConfigMapKey = "clusterca-pem"
	// ConfigMapVolumeClusterCAName is the volume for the operator-owned client-category CA
	// bundle ConfigMap mounted in managed-certificate mode.
	ConfigMapVolumeClusterCAName = "secret-clusterca"
)

func GetTLSModeFromMongodConfig(config map[string]interface{}) Mode {
	// spec.Security.TLSConfig.IsEnabled() is true -> requireSSLMode
	if config == nil {
		return Require
	}
	mode := maputil.ReadMapValueAsString(config, "net", "tls", "mode")

	if mode == "" {
		mode = maputil.ReadMapValueAsString(config, "net", "ssl", "mode")
	}
	if mode == "" {
		return Require
	}

	return Mode(mode)
}
