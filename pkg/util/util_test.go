package util

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/identifiable"
)

func TestCompareVersions(t *testing.T) {
	i, e := CompareVersions("4.0.5", "4.0.4")
	assert.NoError(t, e)
	assert.Equal(t, 1, i)

	i, e = CompareVersions("4.0.0", "4.0.0")
	assert.NoError(t, e)
	assert.Equal(t, 0, i)

	i, e = CompareVersions("3.6.15", "4.1.0")
	assert.NoError(t, e)
	assert.Equal(t, -1, i)

	i, e = CompareVersions("3.6.2", "3.6.12")
	assert.NoError(t, e)
	assert.Equal(t, -1, i)

	i, e = CompareVersions("4.0.2-ent", "4.0.1")
	assert.NoError(t, e)
	assert.Equal(t, 1, i)
}

func TestMajorMinorVersion(t *testing.T) {
	s, _, e := MajorMinorVersion("3.6.12")
	assert.NoError(t, e)
	assert.Equal(t, "3.6", s)

	s, _, e = MajorMinorVersion("4.0.0")
	assert.NoError(t, e)
	assert.Equal(t, "4.0", s)

	s, _, e = MajorMinorVersion("4.2.12-ent")
	assert.NoError(t, e)
	assert.Equal(t, "4.2", s)
}

func TestReadBoolEnv(t *testing.T) {
	os.Setenv("ENV_1", "true")
	os.Setenv("ENV_2", "false")
	os.Setenv("ENV_3", "TRUE")
	os.Setenv("NOT_BOOL", "not-true")

	result, present := env.ReadBool("ENV_1")
	assert.True(t, present)
	assert.True(t, result)

	result, present = env.ReadBool("ENV_2")
	assert.True(t, present)
	assert.False(t, result)

	result, present = env.ReadBool("ENV_3")
	assert.True(t, present)
	assert.True(t, result)

	result, present = env.ReadBool("NOT_BOOL")
	assert.False(t, present)
	assert.False(t, result)

	result, present = env.ReadBool("NOT_HERE")
	assert.False(t, present)
	assert.False(t, result)
}

func TestRedactURI(t *testing.T) {
	uri := "mongo.mongoUri=mongodb://mongodb-ops-manager:my-scram-password@om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017/?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000&authSource=admin&authMechanism=SCRAM-SHA-1"
	expected := "mongo.mongoUri=mongodb://mongodb-ops-manager:<redacted>@om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017/?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000&authSource=admin&authMechanism=SCRAM-SHA-1"
	assert.Equal(t, expected, RedactMongoURI(uri))

	uri = "mongo.mongoUri=mongodb://mongodb-ops-manager:mongodb-ops-manager@om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017/?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000"
	expected = "mongo.mongoUri=mongodb://mongodb-ops-manager:<redacted>@om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017/?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000"
	assert.Equal(t, expected, RedactMongoURI(uri))

	// the password with '@' in it
	uri = "mongo.mongoUri=mongodb://some-user:12345AllTheCharactersWith@SymbolToo@om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017"
	expected = "mongo.mongoUri=mongodb://some-user:<redacted>@om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017"
	assert.Equal(t, expected, RedactMongoURI(uri))

	// no authentication data
	uri = "mongo.mongoUri=mongodb://om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017"
	expected = "mongo.mongoUri=mongodb://om-scram-db-0.om-scram-db-svc.mongodb.svc.cluster.local:27017"
	assert.Equal(t, expected, RedactMongoURI(uri))
}

type someId struct {
	// name is a "key" field used for merging
	name string
	// some other property. Indicates which exactly object was returned by an aggregation operation
	property string
}

func newSome(name, property string) someId {
	return someId{
		name:     name,
		property: property,
	}
}

func (s someId) Identifier() interface{} {
	return s.name
}

func TestSetDifference(t *testing.T) {
	oneLeft := newSome("1", "left")
	twoLeft := newSome("2", "left")
	twoRight := newSome("2", "right")
	threeRight := newSome("3", "right")
	fourRight := newSome("4", "right")

	left := []identifiable.Identifiable{oneLeft, twoLeft}
	right := []identifiable.Identifiable{twoRight, threeRight}

	assert.Equal(t, []identifiable.Identifiable{oneLeft}, identifiable.SetDifference(left, right))
	assert.Equal(t, []identifiable.Identifiable{threeRight}, identifiable.SetDifference(right, left))

	left = []identifiable.Identifiable{oneLeft, twoLeft}
	right = []identifiable.Identifiable{threeRight, fourRight}
	assert.Equal(t, left, identifiable.SetDifference(left, right))

	left = []identifiable.Identifiable{}
	right = []identifiable.Identifiable{threeRight, fourRight}
	assert.Empty(t, identifiable.SetDifference(left, right))
	assert.Equal(t, right, identifiable.SetDifference(right, left))

	left = nil
	right = []identifiable.Identifiable{threeRight, fourRight}
	assert.Empty(t, identifiable.SetDifference(left, right))
	assert.Equal(t, right, identifiable.SetDifference(right, left))

	// check reflection magic to solve lack of covariance in go. The arrays are declared as '[]someId' instead of
	// '[]Identifiable'
	leftNotIdentifiable := []someId{oneLeft, twoLeft}
	rightNotIdentifiable := []someId{twoRight, threeRight}

	assert.Equal(t, []identifiable.Identifiable{oneLeft}, identifiable.SetDifferenceGeneric(leftNotIdentifiable, rightNotIdentifiable))
	assert.Equal(t, []identifiable.Identifiable{threeRight}, identifiable.SetDifferenceGeneric(rightNotIdentifiable, leftNotIdentifiable))
}

func TestSetIntersection(t *testing.T) {
	oneLeft := newSome("1", "left")
	oneRight := newSome("1", "right")
	twoLeft := newSome("2", "left")
	twoRight := newSome("2", "right")
	threeRight := newSome("3", "right")
	fourRight := newSome("4", "right")

	left := []identifiable.Identifiable{oneLeft, twoLeft}
	right := []identifiable.Identifiable{twoRight, threeRight}

	assert.Equal(t, [][]identifiable.Identifiable{pair(twoLeft, twoRight)}, identifiable.SetIntersection(left, right))
	assert.Equal(t, [][]identifiable.Identifiable{pair(twoRight, twoLeft)}, identifiable.SetIntersection(right, left))

	left = []identifiable.Identifiable{oneLeft, twoLeft}
	right = []identifiable.Identifiable{threeRight, fourRight}
	assert.Empty(t, identifiable.SetIntersection(left, right))
	assert.Empty(t, identifiable.SetIntersection(right, left))

	left = []identifiable.Identifiable{}
	right = []identifiable.Identifiable{threeRight, fourRight}
	assert.Empty(t, identifiable.SetIntersection(left, right))
	assert.Empty(t, identifiable.SetIntersection(right, left))

	left = nil
	right = []identifiable.Identifiable{threeRight, fourRight}
	assert.Empty(t, identifiable.SetIntersection(left, right))
	assert.Empty(t, identifiable.SetIntersection(right, left))

	// check reflection magic to solve lack of covariance in go. The arrays are declared as '[]someId' instead of
	// '[]Identifiable'
	leftNotIdentifiable := []someId{oneLeft, twoLeft}
	rightNotIdentifiable := []someId{oneRight, twoRight, threeRight}

	assert.Equal(t, [][]identifiable.Identifiable{pair(oneLeft, oneRight), pair(twoLeft, twoRight)}, identifiable.SetIntersectionGeneric(leftNotIdentifiable, rightNotIdentifiable))
	assert.Equal(t, [][]identifiable.Identifiable{pair(oneRight, oneLeft), pair(twoRight, twoLeft)}, identifiable.SetIntersectionGeneric(rightNotIdentifiable, leftNotIdentifiable))

	leftNotIdentifiable = []someId{oneLeft, twoLeft}
	rightNotIdentifiable = []someId{oneLeft, twoLeft}

	assert.Len(t, identifiable.SetIntersectionGeneric(leftNotIdentifiable, rightNotIdentifiable), 0)
}

func TestTransform(t *testing.T) {
	assert.Equal(t, []string{"1", "2", "3"}, Transform([]int{1, 2, 3}, func(v int) string {
		return fmt.Sprintf("%d", v)
	}))

	assert.Equal(t, []string{}, Transform([]int{}, func(v int) string {
		return fmt.Sprintf("%d", v)
	}))

	type tmpStruct struct {
		str string
	}
	assert.Equal(t, []string{"a", "b", "c"}, Transform([]tmpStruct{{"a"}, {"b"}, {"c"}}, func(v tmpStruct) string {
		return v.str
	}))
}

func TestTransformToMap(t *testing.T) {
	assert.Equal(t, map[string]string{"0": "1", "1": "2", "2": "3"}, TransformToMap([]int{1, 2, 3}, func(v int, idx int) (string, string) {
		return fmt.Sprintf("%d", idx), fmt.Sprintf("%d", v)
	}))

	assert.Equal(t, map[string]int{}, TransformToMap([]string{}, func(v string, idx int) (string, int) {
		return "", 0
	}))

	type tmpStruct struct {
		str string
		int int
	}
	assert.Equal(t, map[string]int{"a": 0, "b": 1, "c": 2}, TransformToMap([]tmpStruct{{"a", 0}, {"b", 1}, {"c", 2}}, func(v tmpStruct, idx int) (string, int) {
		return v.str, v.int
	}))
}

func pair(left, right identifiable.Identifiable) []identifiable.Identifiable {
	return []identifiable.Identifiable{left, right}
}
