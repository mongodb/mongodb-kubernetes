package env

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/spf13/cast"
	"go.uber.org/zap"
)

func Read(env string) (string, bool) {
	return os.LookupEnv(env)
}

func ReadBool(env string) (valueAsBool bool, isPresent bool) {
	value, isPresent := Read(env)
	if !isPresent {
		return false, false
	}
	boolValue, err := strconv.ParseBool(value)
	return boolValue, err == nil
}

func ReadBoolOrDefault(key string, defaultValue bool) bool {
	value, isPresent := ReadBool(key)
	if isPresent {
		return value
	}
	return defaultValue
}

// EnsureVar tests the env variable and sets it if it doesn't exist. We tolerate any errors setting env variable and
// just log the warning
func EnsureVar(key, value string) {
	if _, exist := Read(key); !exist {
		if err := os.Setenv(key, value); err != nil {
			zap.S().Warnf("Failed to set environment variable \"%s\" to \"%s\": %s", key, value, err)
		}
	}
}

// PrintWithPrefix prints environment variables to the global SugaredLogger. It will only print the environment variables
// with a given prefix set inside the function.
func PrintWithPrefix(printableEnvPrefixes []string) {
	zap.S().Info("Environment variables:")
	envVariables := os.Environ()
	sort.Strings(envVariables)
	for _, e := range envVariables {
		for _, prefix := range printableEnvPrefixes {
			if strings.HasPrefix(e, prefix) {
				zap.S().Infof("%s", e)
			}
		}
	}
}

func ReadOrPanic(key string) string {
	value := os.Getenv(key)
	if value == "" {
		panic(fmt.Sprintf("%s environment variable is not set!", key))
	}
	return value
}

func ReadIntOrPanic(key string) int {
	value := os.Getenv(key)
	i, e := cast.ToIntE(value)
	if e != nil {
		panic(fmt.Sprintf("%s env variable is supposed to be of type int but the value is %s", key, value))
	}
	return i
}

func ReadOrDefault(key string, dflt string) string {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return dflt
	}
	return value
}

func ReadIntOrDefault(key string, dflt int) int {
	value := ReadOrDefault(key, strconv.Itoa(dflt))
	i, e := cast.ToIntE(value)
	if e != nil {
		return dflt
	}
	return i
}

// PodEnvVars is a convenience struct to pass environment variables to Pods as needed.
// They are used by the automation agent to connect to Ops/Cloud Manager.
type PodEnvVars struct {
	BaseURL     string
	ProjectID   string
	User        string
	AgentAPIKey string
	LogLevel    string

	// Related to MMS SSL configuration
	SSLProjectConfig
}

// SSLProjectConfig contains the configuration options that are relevant for MMS SSL configuraiton
type SSLProjectConfig struct {
	// This is set to true if baseUrl is HTTPS
	SSLRequireValidMMSServerCertificates bool

	// Name of a configmap containing a `mms-ca.crt` entry that will be mounted
	// on every Pod.
	SSLMMSCAConfigMap string

	// SSLMMSCAConfigMap will contain the CA cert, used to push multiple
	SSLMMSCAConfigMapContents string
}

// FromSecret returns a corev1.EnvVar that is a reference to a secret with the field
// "secretKey" being used
func FromSecret(envVarName, secretName, secretKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envVarName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
				Key: secretKey,
			},
		},
	}
}

// ToMap accepts a variable number of EnvVars and returns them as a map
// with the name as the key.
func ToMap(vars ...corev1.EnvVar) map[string]string {
	variablesMap := map[string]string{}
	for _, envVar := range vars {
		variablesMap[envVar.Name] = envVar.Value
	}
	return variablesMap
}

// RevertEnvVariables saves current values of environment variables and restores them when the returned function is called.
// Intended to be used in tests as a defer statement.
// Make sure returned function is called in defer statement:
//
//	defer RevertEnvVariables(envVars)()
func RevertEnvVariables(envVars ...string) func() {
	originalEnvVars := make(map[string]string)
	for _, envVar := range envVars {
		originalEnvVars[envVar] = os.Getenv(envVar)
	}
	return func() {
		for envVar, value := range originalEnvVars {
			_ = os.Setenv(envVar, value)
		}
	}
}
