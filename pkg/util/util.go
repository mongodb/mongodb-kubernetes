package util

import (
	"encoding/base64"
	"sort"
	"strings"
	"time"

	"bytes"
	"encoding/gob"

	"strconv"

	"fmt"
	"os"

	crypto "crypto/rand"

	"github.com/blang/semver"
	"github.com/spf13/cast"
	"go.uber.org/zap"
)

// ************** This is a file containing any general "algorithmic" or "util" functions used by other packages

// FindLeftDifference finds the difference between arrays of string - the elements that are present in "left" but absent
// in "right" array
func FindLeftDifference(left, right []string) []string {
	ans := make([]string, 0)
	for _, v := range left {
		if !ContainsString(right, v) {
			ans = append(ans, v)
		}
	}
	return ans
}

// Int32Ref is required to return a *int32, which can't be declared as a literal.
func Int32Ref(i int32) *int32 {
	return &i
}

// Int64Ref is required to return a *int64, which can't be declared as a literal.
func Int64Ref(i int64) *int64 {
	return &i
}

// Float64Ref is required to return a *float64, which can't be declared as a literal.
func Float64Ref(i float64) *float64 {
	return &i
}

// BooleanRef is required to return a *bool, which can't be declared as a literal.
func BooleanRef(b bool) *bool {
	return &b
}

func StringRef(s string) *string {
	return &s
}

// DoAndRetry performs the task 'f' until it returns true or 'count' retrials are executed. Sleeps for 'interval' seconds
// between retries. String return parameter contains the fail message that is printed in case of failure.
func DoAndRetry(f func() (string, bool), log *zap.SugaredLogger, count, interval int) bool {
	for i := 0; i < count; i++ {
		msg, ok := f()
		if ok {
			return true
		}
		if msg != "" {
			msg += "."
		}
		log.Debugf("%s Retrying %d/%d (waiting for %d more seconds)", msg, i+1, count, interval)
		time.Sleep(time.Duration(interval) * time.Second)
	}
	return false
}

// MapDeepCopy is a quick implementation of deep copy mechanism for any Go structures, it uses Go serialization and
// deserialization mechanisms so will always be slower than any manual copy
// https://rosettacode.org/wiki/Deepcopy#Go
func MapDeepCopy(m map[string]interface{}) (map[string]interface{}, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	dec := gob.NewDecoder(&buf)
	err := enc.Encode(m)
	if err != nil {
		return nil, err
	}
	var copy map[string]interface{}
	err = dec.Decode(&copy)
	if err != nil {
		return nil, err
	}
	return copy, nil
}

func ReadOrCreateMap(m map[string]interface{}, key string) map[string]interface{} {
	if _, ok := m[key]; !ok {
		m[key] = make(map[string]interface{}, 0)
	}
	return m[key].(map[string]interface{})
}

func ReadOrCreateStringArray(m map[string]interface{}, key string) []string {
	if _, ok := m[key]; !ok {
		m[key] = make([]string, 0)
	}
	return m[key].([]string)
}

func CompareVersions(version1, version2 string) (int, error) {
	v1, err := semver.Make(version1)
	if err != nil {
		return 0, err
	}
	v2, err := semver.Make(version2)
	if err != nil {
		return 0, err
	}
	return v1.Compare(v2), nil
}

func VersionMatchesRange(version, vRange string) (bool, error) {
	v, err := semver.Parse(version)
	if err != nil {
		return false, err
	}
	expectedRange, err := semver.ParseRange(vRange)
	if err != nil {
		return false, err
	}
	return expectedRange(v), nil
}

func MajorMinorVersion(version string) (string, error) {
	v1, err := semver.Make(version)
	if err != nil {
		return "", nil
	}
	return fmt.Sprintf("%d.%d", v1.Major, v1.Minor), nil
}

// ************ Different functions to work with environment variables **************

func ReadEnvVarOrPanic(key string) string {
	value := os.Getenv(key)
	if value == "" {
		panic(fmt.Sprintf("%s environment variable is not set!", key))
	}
	return value
}

func ReadEnvVarOrPanicInt(key string) int {
	value := os.Getenv(key)
	i, e := cast.ToIntE(value)
	if e != nil {
		panic(fmt.Sprintf("%s env variable is supposed to be of type int but the value is %s", key, value))
	}
	return i
}

func ReadEnvVarIntOrDefault(key string, dflt int) int {
	value, exists := os.LookupEnv(key)
	if !exists {
		return dflt
	}
	i, e := cast.ToIntE(value)
	if e != nil {
		return dflt
	}
	return i
}

func ReadEnv(env string) (string, bool) {
	return os.LookupEnv(env)
}

func ReadBoolEnv(env string) (valueAsBool bool, isPresent bool) {
	value, isPresent := ReadEnv(env)
	if !isPresent {
		return false, false
	}
	boolValue, err := strconv.ParseBool(value)
	return boolValue, err == nil
}

// EnsureEnvVar tests the env variable and sets it if it doesn't exist. We tolerate any errors setting env variable and
// just log the warning
func EnsureEnvVar(key, value string) {
	if _, exist := ReadEnv(key); !exist {
		if err := os.Setenv(key, value); err != nil {
			zap.S().Warnf("Failed to set environment variable \"%s\" to \"%s\": %s", key, value, err)
		}
	}
}

// PrintEnvVars prints environment variables to the global SugaredLogger. It will only print the environment variables
// with a given prefix set inside the function.
func PrintEnvVars() {
	// Only env variables with one of these prefixes will be printed
	printableEnvPrefixes := [...]string{
		"BACKUP_WAIT_",
		"POD_WAIT_",
		"OPERATOR_ENV",
		"WATCH_NAMESPACE",
		"MANAGED_SECURITY_CONTEXT",
		"IMAGE_PULL_SECRETS",
		"IMAGE_PULL_SECRETS",
		"MONGODB_ENTERPRISE_",
		"OPS_MANAGER_",
		"KUBERNETES_",
	}

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

func Now() string {
	return time.Now().Format(time.RFC3339)
}

func MaxInt(x, y int) int {
	if x > y {
		return x
	}
	return y
}

// ************ Different string/array functions **************
//
// Helper functions to check and remove string from a slice of strings.
//

// ContainsString returns true if there is at least one string in `slice`
// that is equal to `s`.
func ContainsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func RemoveString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

// UpperCaseFirstChar ensures the message first char is uppercased
func UpperCaseFirstChar(msg string) string {
	return string(strings.ToUpper(msg[:1])) + msg[1:]
}

// final key must be between 6 and at most 1024 characters
func GenerateKeyFileContents() (string, error) {
	return generateRandomString(500)
}

func generateRandomBytes(size int) ([]byte, error) {
	b := make([]byte, size)
	_, err := crypto.Read(b)
	if err != nil {
		return nil, err
	}

	return b, nil
}

func generateRandomString(numBytes int) (string, error) {
	b, err := generateRandomBytes(numBytes)
	return base64.StdEncoding.EncodeToString(b), err
}
