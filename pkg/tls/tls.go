package tls

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
)

type Mode string

const (
	Disabled              Mode = "disabled"
	Require               Mode = "requireTLS"
	Prefer                Mode = "preferTLS"
	Allow                 Mode = "allowTLS"
	ConfigMapVolumeCAName      = "secret-ca"
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
