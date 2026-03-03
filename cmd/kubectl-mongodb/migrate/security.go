package migrate

import (
	"github.com/spf13/cast"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func inferSecurity(auth *om.Auth, processMap map[string]map[string]interface{}, members []interface{}) *mdbv1.Security {
	security := &mdbv1.Security{}
	hasSettings := false

	tlsEnabled := false
	for _, m := range members {
		member, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		host := cast.ToString(member["host"])
		proc, ok := processMap[host]
		if !ok {
			continue
		}
		if isTLSEnabled(proc) {
			tlsEnabled = true
			break
		}
	}

	if tlsEnabled {
		security.TLSConfig = &mdbv1.TLSConfig{
			Enabled: true,
		}
		hasSettings = true
	}

	if auth != nil && auth.IsEnabled() {
		authModes := inferAuthModes(auth)
		if len(authModes) > 0 {
			security.Authentication = &mdbv1.Authentication{
				Enabled: true,
				Modes:   authModes,
			}
			hasSettings = true
		}
	}

	if !hasSettings {
		return nil
	}
	return security
}

func inferAuthModes(auth *om.Auth) []mdbv1.AuthMode {
	var modes []mdbv1.AuthMode

	for _, mech := range auth.AutoAuthMechanisms {
		if mode, ok := mapMechanismToAuthMode(mech); ok {
			modes = append(modes, mode)
		}
	}

	if len(modes) == 0 && auth.AutoAuthMechanism != "" {
		if mode, ok := mapMechanismToAuthMode(auth.AutoAuthMechanism); ok {
			modes = append(modes, mode)
		}
	}

	return modes
}

func mapMechanismToAuthMode(mech string) (mdbv1.AuthMode, bool) {
	switch mech {
	case "MONGODB-CR", "SCRAM-SHA-256", "SCRAM-SHA-1":
		return mdbv1.AuthMode(mech), true
	case "MONGODB-X509":
		return mdbv1.AuthMode("X509"), true
	case "PLAIN":
		return mdbv1.AuthMode("LDAP"), true
	case "MONGODB-OIDC":
		return mdbv1.AuthMode("OIDC"), true
	default:
		return "", false
	}
}

func isTLSEnabled(process map[string]interface{}) bool {
	args, ok := process["args2_6"].(map[string]interface{})
	if !ok {
		return false
	}
	net, ok := args["net"].(map[string]interface{})
	if !ok {
		return false
	}

	if tls, ok := net["tls"].(map[string]interface{}); ok {
		mode := cast.ToString(tls["mode"])
		if mode == "requireSSL" || mode == "requireTLS" || mode == "preferSSL" || mode == "preferTLS" {
			return true
		}
	}

	if ssl, ok := net["ssl"].(map[string]interface{}); ok {
		mode := cast.ToString(ssl["mode"])
		if mode == "requireSSL" || mode == "requireTLS" || mode == "preferSSL" || mode == "preferTLS" {
			return true
		}
	}

	return false
}

func extractAdditionalMongodConfig(processMap map[string]map[string]interface{}, members []interface{}) *mdbv1.AdditionalMongodConfig {
	for _, m := range members {
		member, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		host := cast.ToString(member["host"])
		proc, ok := processMap[host]
		if !ok {
			continue
		}

		args, ok := proc["args2_6"].(map[string]interface{})
		if !ok {
			continue
		}

		config := mdbv1.NewEmptyAdditionalMongodConfig()
		hasConfig := false

		if net, ok := args["net"].(map[string]interface{}); ok {
			port := cast.ToInt(net["port"])
			if port != 0 && port != 27017 {
				config.AddOption("net.port", port)
				hasConfig = true
			}
		}

		if storage, ok := args["storage"].(map[string]interface{}); ok {
			if wt, ok := storage["wiredTiger"].(map[string]interface{}); ok {
				if ec, ok := wt["engineConfig"].(map[string]interface{}); ok {
					if cacheSizeGB, ok := ec["cacheSizeGB"]; ok {
						config.AddOption("storage.wiredTiger.engineConfig.cacheSizeGB", cacheSizeGB)
						hasConfig = true
					}
				}
			}
		}

		if hasConfig {
			return config
		}
		return nil
	}
	return nil
}
