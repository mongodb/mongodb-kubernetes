package util

import (
	"testing"

	"os"

	"github.com/stretchr/testify/assert"
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
	s, e := MajorMinorVersion("3.6.12")
	assert.NoError(t, e)
	assert.Equal(t, "3.6", s)

	s, e = MajorMinorVersion("4.0.0")
	assert.NoError(t, e)
	assert.Equal(t, "4.0", s)

	s, e = MajorMinorVersion("4.2.12-ent")
	assert.NoError(t, e)
	assert.Equal(t, "4.2", s)
}

func TestReadBoolEnv(t *testing.T) {
	os.Setenv("ENV_1", "true")
	os.Setenv("ENV_2", "false")
	os.Setenv("ENV_3", "TRUE")
	os.Setenv("NOT_BOOL", "not-true")

	result, present := ReadBoolEnv("ENV_1")
	assert.True(t, present)
	assert.True(t, result)

	result, present = ReadBoolEnv("ENV_2")
	assert.True(t, present)
	assert.False(t, result)

	result, present = ReadBoolEnv("ENV_3")
	assert.True(t, present)
	assert.True(t, result)

	result, present = ReadBoolEnv("NOT_BOOL")
	assert.False(t, present)
	assert.False(t, result)

	result, present = ReadBoolEnv("NOT_HERE")
	assert.False(t, present)
	assert.False(t, result)
}

type someId struct {
	name string
}

func (s someId) Identifier() interface{} {
	return s.name
}

func TestSetDifference(t *testing.T) {
	left := []Identifiable{someId{"1"}, someId{"2"}}
	right := []Identifiable{someId{"2"}, someId{"3"}}

	assert.Equal(t, []Identifiable{someId{"1"}}, SetDifference(left, right))
	assert.Equal(t, []Identifiable{someId{"3"}}, SetDifference(right, left))

	left = []Identifiable{someId{"1"}, someId{"2"}}
	right = []Identifiable{someId{"3"}, someId{"4"}}
	assert.Equal(t, left, SetDifference(left, right))

	left = []Identifiable{}
	right = []Identifiable{someId{"3"}, someId{"4"}}
	assert.Empty(t, SetDifference(left, right))
	assert.Equal(t, right, SetDifference(right, left))

	left = nil
	right = []Identifiable{someId{"3"}, someId{"4"}}
	assert.Empty(t, SetDifference(left, right))
	assert.Equal(t, right, SetDifference(right, left))

	// check reflection magic to solve lack of covariance in go. The arrays are declared as '[]someId' instead of
	// '[]Identifiable'
	leftNotIdentifiable := []someId{{"1"}, {"2"}}
	rightNotIdentifiable := []someId{{"2"}, {"3"}}

	assert.Equal(t, []Identifiable{someId{"1"}}, SetDifferenceGeneric(leftNotIdentifiable, rightNotIdentifiable))
	assert.Equal(t, []Identifiable{someId{"3"}}, SetDifferenceGeneric(rightNotIdentifiable, leftNotIdentifiable))

}
