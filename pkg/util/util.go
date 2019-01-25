package util

import (
	"sort"
	"time"

	"bytes"
	"encoding/gob"

	"strconv"

	"fmt"
	"strings"

	"os"

	"crypto/sha1"
	"encoding/json"
	"errors"
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

// ParseMongodbMinorVersion returns the mongodb version as major + minor parts that can be represented as float.
// So the result can be used for direct comparison
// Note, that this method doesn't perform deep validation of the format (negative, big numbers etc)
// There should be a separate method for that that will be invoked during validation of user-provided version
// May be when it's added - it should be invoked here as well
// TODO use https://github.com/blang/semver to do proper versioning
func ParseMongodbMinorVersion(version string) (float32, error) {
	s := strings.FieldsFunc(version, func(c rune) bool { return c == '.' })

	if len(s) < 2 || len(s) > 3 {
		return -1, fmt.Errorf("Wrong format of version: %s is expected to have either 2 or 3 parts separated by '.'", version)
	}
	// if we have 3 parts - we need to parse only two of them
	if len(s) == 3 {
		version = strings.Join(s[:2], ".")
	}
	v, err := strconv.ParseFloat(version, 32)

	if err != nil {
		return -1, err
	}
	return float32(v), nil
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

func PrintEnvVars() {
	zap.S().Info("Environment variables:")
	envVariables := os.Environ()
	sort.Strings(envVariables)
	for _, e := range envVariables {
		zap.S().Infof("%s", e)
	}
}

func Hash(item interface{}) (uint64, error) {
	// json package always orders keys in the order defined in the struct
	// so we don't need to worry about different orders causing inconsistencies
	// https://stackoverflow.com/questions/18668652/how-to-produce-json-with-sorted-keys-in-go
	jsonBytes, err := json.Marshal(item)
	if err != nil {
		return 0, errors.New(fmt.Sprintf("Failed to hash %s", item))
	}
	hash := sha1.New()
	hash.Write(jsonBytes)
	hashBytes := hash.Sum(nil)
	sum := uint64(0)
	for _, b := range hashBytes {
		sum += uint64(b)
	}
	return sum, nil
}

func Now() string {
	return time.Now().Format(time.RFC3339)
}

// ************ Different string/array functions **************
//
// Helper functions to check and remove string from a slice of strings.
//
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
