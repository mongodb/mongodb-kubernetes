package util

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/identifiable"
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

func TestClassifyAppDBStatefulSetOwnership(t *testing.T) {
	tests := []struct {
		name      string
		labels    map[string]string
		ownKey    string
		ownValue  string
		expectedX AppDBStatefulSetOwnership
	}{
		{
			name:      "unowned when no participant labels exist",
			labels:    map[string]string{"app": "demo"},
			ownKey:    MongoDBMultiClusterResourceOwnerLabel,
			ownValue:  "ns-temple",
			expectedX: AppDBStatefulSetOwnershipUnowned,
		},
		{
			name:      "owned when own key matches even with controller label missing",
			labels:    map[string]string{MongoDBMultiClusterResourceOwnerLabel: "ns-temple"},
			ownKey:    MongoDBMultiClusterResourceOwnerLabel,
			ownValue:  "ns-temple",
			expectedX: AppDBStatefulSetOwnershipOwned,
		},
		{
			name:      "foreign owner label is ignored when own label matches",
			labels:    map[string]string{MongoDBMultiClusterResourceOwnerLabel: "ns-temple", MongoDBResourceOwnerLabel: "someone-else"},
			ownKey:    MongoDBMultiClusterResourceOwnerLabel,
			ownValue:  "ns-temple",
			expectedX: AppDBStatefulSetOwnershipOwned,
		},
		{
			name:      "own key wrong value is conflict",
			labels:    map[string]string{MongoDBMultiClusterResourceOwnerLabel: "wrong"},
			ownKey:    MongoDBMultiClusterResourceOwnerLabel,
			ownValue:  "ns-temple",
			expectedX: AppDBStatefulSetOwnershipConflict,
		},
		{
			name:      "two participant labels are conflict",
			labels:    map[string]string{MongoDBResourceOwnerLabel: "my-mdb", MongoDBOpsManagerResourceOwnerLabel: "my-om"},
			ownKey:    MongoDBMultiClusterResourceOwnerLabel,
			ownValue:  "ns-temple",
			expectedX: AppDBStatefulSetOwnershipConflict,
		},
		{
			name:      "controller label is ignored",
			labels:    map[string]string{"controller": "mongodb-enterprise-operator", MongoDBMultiClusterResourceOwnerLabel: "ns-temple"},
			ownKey:    MongoDBMultiClusterResourceOwnerLabel,
			ownValue:  "ns-temple",
			expectedX: AppDBStatefulSetOwnershipOwned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedX, ClassifyAppDBStatefulSetOwnership(tt.labels, tt.ownKey, tt.ownValue))
		})
	}
}

func TestHasForeignAppDBOwnerLabel(t *testing.T) {
	tests := []struct {
		name      string
		labels    map[string]string
		ownKey    string
		expectedX bool
	}{
		{
			name:      "nil labels do not report a foreign owner",
			ownKey:    MongoDBOpsManagerResourceOwnerLabel,
			expectedX: false,
		},
		{
			name:      "own label alone does not report a foreign owner",
			labels:    map[string]string{MongoDBOpsManagerResourceOwnerLabel: "ns-temple"},
			ownKey:    MongoDBOpsManagerResourceOwnerLabel,
			expectedX: false,
		},
		{
			name:      "foreign label is reported",
			labels:    map[string]string{MongoDBResourceOwnerLabel: "my-mdb"},
			ownKey:    MongoDBOpsManagerResourceOwnerLabel,
			expectedX: true,
		},
		{
			name:      "multiple foreign labels are reported",
			labels:    map[string]string{MongoDBResourceOwnerLabel: "my-mdb", MongoDBMultiClusterResourceOwnerLabel: "my-mdbm"},
			ownKey:    MongoDBOpsManagerResourceOwnerLabel,
			expectedX: true,
		},
		{
			name:      "foreign own key is ignored when checking other participants",
			labels:    map[string]string{MongoDBOpsManagerResourceOwnerLabel: "ns-temple", MongoDBResourceOwnerLabel: "my-mdb"},
			ownKey:    MongoDBOpsManagerResourceOwnerLabel,
			expectedX: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedX, HasForeignAppDBOwnerLabel(tt.labels, tt.ownKey))
		})
	}
}

func TestStripAppDBParticipantLabels(t *testing.T) {
	tests := []struct {
		name      string
		labels    map[string]string
		expectedX map[string]string
	}{
		{
			name:      "nil labels stay nil",
			expectedX: nil,
		},
		{
			name:      "unrelated labels survive",
			labels:    map[string]string{"app": "demo"},
			expectedX: map[string]string{"app": "demo"},
		},
		{
			name: "every participant key is removed",
			labels: map[string]string{
				"app":                                 "demo",
				MongoDBResourceOwnerLabel:             "my-mdb",
				MongoDBOpsManagerResourceOwnerLabel:   "my-om",
				MongoDBMultiClusterResourceOwnerLabel: "my-mdbm",
			},
			expectedX: map[string]string{"app": "demo"},
		},
		{
			name:      "a map of only participant keys becomes empty",
			labels:    map[string]string{MongoDBResourceOwnerLabel: "my-mdb"},
			expectedX: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedX, StripAppDBParticipantLabels(tt.labels))
		})
	}
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

// TestIsURL tests the ParseURL function with various inputs.
//
//goland:noinspection HttpUrlsUsage
func TestIsURL(t *testing.T) {
	tests := []struct {
		name                string
		input               string
		expectedErrorString string
	}{
		{
			name:  "valid http URL",
			input: "http://example.com",
		},
		{
			name:  "valid https URL with path",
			input: "https://example.com/path",
		},
		{
			name:  "valid URL with port",
			input: "http://example.com:8080",
		},
		{
			name:                "missing scheme",
			input:               "example.com",
			expectedErrorString: "missing URL scheme: example.com",
		},
		{
			name:                "missing host",
			input:               "http://",
			expectedErrorString: "missing URL host: http://",
		},
		{
			name:                "empty string",
			input:               "",
			expectedErrorString: "empty URL",
		},
		{
			name:                "invalid URL",
			input:               ":invalid-url",
			expectedErrorString: "invalid URL: parse \":invalid-url\": missing protocol scheme",
		},
		{
			name:                "file scheme",
			input:               "file://path/to/file",
			expectedErrorString: "invalid URL scheme (http or https): file://path/to/file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := ParseURL(tt.input)
			if tt.expectedErrorString != "" {
				require.Error(t, err)
				assert.Equal(t, tt.expectedErrorString, err.Error())
				assert.Nil(t, u)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, u)
			}
		})
	}
}

func TestIsPOSIXAbsolutePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "root", path: "/", want: true},
		{name: "nested", path: "/etc/ssl/cert.pem", want: true},
		{name: "double_slash", path: "//foo", want: true},
		{name: "empty", path: "", want: false},
		{name: "relative", path: "relative", want: false},
		{name: "dot_slash", path: "./abs", want: false},
		{name: "dot_dot_segment", path: "/safe/../etc/passwd", want: false},
		{name: "dot_dot_in_name", path: "/opt/foo..bar/x", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPOSIXAbsolutePath(tt.path))
		})
	}
}

func pair(left, right identifiable.Identifiable) []identifiable.Identifiable {
	return []identifiable.Identifiable{left, right}
}
