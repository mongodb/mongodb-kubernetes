package construct

import (
	"os"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// MDB_PROPAGATE_PROXY_ENV needs to be configurable in the operator environment to support configuring whether the proxy environment
	// variables should be propagated to the database containers. A valid case for this is a multi-cluster environment where the operator
	// might have to use a proxy to connect to OM/CM, but the mongodb agents in different clusters don't have to.
	PropagateProxyEnv = "MDB_PROPAGATE_PROXY_ENV"
)

// below proxy handling has been inspired by the proxy handling in operator-lib
// here: https://github.com/operator-framework/operator-lib/blob/e40c80627593fa6eaad3e2cb1380e3e838afe56c/proxy/proxy.go#L30

// ProxyEnvNames are standard environment variables for proxies
var ProxyEnvNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}

// ReadDatabaseProxyVarsFromEnv retrieves the standard proxy-related environment
// variables from the running environment and returns a slice of corev1 EnvVar
// containing upper and lower case versions of those variables.
func ReadDatabaseProxyVarsFromEnv() []corev1.EnvVar {
	propagateProxyVar, _ := os.LookupEnv(PropagateProxyEnv) // nolint:forbidigo
	propagateProxy, _ := strconv.ParseBool(propagateProxyVar)
	if !propagateProxy {
		return nil
	}
	var envVars []corev1.EnvVar
	for _, s := range ProxyEnvNames {
		value, isSet := os.LookupEnv(s) // nolint:forbidigo
		if isSet {
			envVars = append(envVars, corev1.EnvVar{
				Name:  s,
				Value: value,
			}, corev1.EnvVar{
				Name:  strings.ToLower(s),
				Value: value,
			})
		}
	}
	return envVars
}
