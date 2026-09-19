package construct

import (
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// below proxy handling has been inspired by the proxy handling in operator-lib
// here: https://github.com/operator-framework/operator-lib/blob/e40c80627593fa6eaad3e2cb1380e3e838afe56c/proxy/proxy.go#L30

// ProxyEnvNames are standard environment variables for proxies
var ProxyEnvNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}

// ReadDatabaseProxyVarsFromEnv retrieves the standard proxy-related environment variables from the
// operator's running environment and returns a slice of corev1 EnvVar containing upper and lower
// case versions of those variables.
func ReadDatabaseProxyVarsFromEnv(propagateProxyEnv bool) []corev1.EnvVar {
	if !propagateProxyEnv {
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
