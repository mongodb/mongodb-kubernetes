package envutil

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

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
