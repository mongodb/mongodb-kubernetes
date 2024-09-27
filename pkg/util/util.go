package util

import (
	"bytes"
	"crypto/md5" //nolint //Part of the algorithm
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/blang/semver"
	"go.uber.org/zap"
)

// ************** This is a file containing any general "algorithmic" or "util" functions used by other packages

// FindLeftDifference finds the difference between arrays of string - the elements that are present in "left" but absent
// in "right" array
func FindLeftDifference(left, right []string) []string {
	ans := make([]string, 0)
	for _, v := range left {
		if !stringutil.Contains(right, v) {
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

func StripEnt(version string) string {
	return strings.Trim(version, "-ent")
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
// TODO move to maputil
func MapDeepCopy(m map[string]interface{}) (map[string]interface{}, error) {
	gob.Register(map[string]interface{}{})

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
		m[key] = make(map[string]interface{})
	}
	return m[key].(map[string]interface{})
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

func MajorMinorVersion(version string) (string, semver.Version, error) {
	v1, err := semver.Make(version)
	if err != nil {
		return "", v1, nil
	}
	return fmt.Sprintf("%d.%d", v1.Major, v1.Minor), v1, nil
}

// ************ Different functions to work with environment variables **************

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

// MD5Hex computes the MDB checksum of the given string as per https://golang.org/pkg/crypto/md5/
func MD5Hex(s string) string {
	h := md5.New() //nolint //This is part of the HTTP Digest Authentication mechanism.
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// RedactMongoURI will strip the password out of the MongoURI and replace it with the text "<redacted>"
func RedactMongoURI(uri string) string {
	if !strings.Contains(uri, "@") {
		return uri
	}
	re := regexp.MustCompile("(mongodb://.*:)(.*)(@.*:.*)")
	return re.ReplaceAllString(uri, "$1<redacted>$3")
}

func Redact(toRedact interface{}) string {
	if toRedact == nil {
		return "nil"
	}
	return "<redacted>"
}

// Transform converts a slice of objects to a new slice containing objects returned from f.
// It is useful for simple slice transformations that otherwise require declaring a new slice var and for loop.
//
// Example:
//
//	 processHostnames := util.Transform(ac.Processes, func(obj automationconfig.Process) string {
//		  return obj.HostName
//	 })
func Transform[T any, U any](objs []T, f func(obj T) U) []U {
	result := make([]U, len(objs))
	for i := 0; i < len(objs); i++ {
		result[i] = f(objs[i])
	}
	return result
}

// TransformToMap converts a slice of objects to a map with key values returned from f.
func TransformToMap[T any, K comparable, V any](objs []T, f func(obj T, idx int) (K, V)) map[K]V {
	result := make(map[K]V, len(objs))
	for i := 0; i < len(objs); i++ {
		k, v := f(objs[i], i)
		result[k] = v
	}
	return result
}
