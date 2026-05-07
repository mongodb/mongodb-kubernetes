package telemetry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventTypeMethods(t *testing.T) {
	tests := []struct {
		eventType       EventType
		expectedPayload string
		expectedTimeKey string
	}{
		{
			eventType:       Deployments,
			expectedTimeKey: "lastSendTimestampDeployments",
		},
		{
			eventType:       Operators,
			expectedTimeKey: "lastSendTimestampOperators",
		},
		{
			eventType:       Clusters,
			expectedTimeKey: "lastSendTimestampClusters",
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			assert.Equal(t, tt.expectedTimeKey, tt.eventType.GetTimeStampKey(), "GetTimeStampKey() mismatch")
		})
	}
}

func TestSearchDeploymentUsageSnapshotProperties_ConvertToFlatMap(t *testing.T) {
	databaseClusters := 5
	appDBClusters := 3
	omClusters := 2

	props := SearchDeploymentUsageSnapshotProperties{
		DeploymentUsageSnapshotProperties: DeploymentUsageSnapshotProperties{
			DatabaseClusters:         &databaseClusters,
			AppDBClusters:            &appDBClusters,
			OmClusters:               &omClusters,
			DeploymentUID:            "test-deployment-uid",
			OperatorID:               "test-operator-id",
			Architecture:             "amd64",
			IsMultiCluster:           true,
			Type:                     "RS",
			IsRunningEnterpriseImage: true,
			ExternalDomains:          "Uniform",
			CustomRoles:              "ClusterSpecific",
			AuthenticationAgentMode:  "SCRAM",
			AuthenticationModes:      []string{"SCRAM", "X509"},
		},
		IsAutoEmbeddingEnabled: true,
	}

	result, err := props.ConvertToFlatMap()
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Extract expected keys from struct using reflection
	expectedKeys := getExpectedKeysFromStruct(reflect.TypeOf(props))

	// Add special keys from AuthenticationModes transformation
	for _, mode := range props.AuthenticationModes {
		expectedKeys = append(expectedKeys, "authenticationMode"+mode)
	}

	// Get actual keys from result map
	var actualKeys []string
	for key := range result {
		actualKeys = append(actualKeys, key)
	}

	// Assert all expected keys are present in the result map
	for _, key := range expectedKeys {
		assert.Contains(t, result, key, "Expected key %s to be present in the result map. Expected keys: %v, Actual keys: %v", key, expectedKeys, actualKeys)
	}

	// Ensure AuthenticationModes field is not in the map (it's marked with json:"-")
	assert.NotContains(t, result, "authenticationModes")
}

// getExpectedKeysFromStruct recursively extracts JSON tag names from a struct type
func getExpectedKeysFromStruct(t reflect.Type) []string {
	var keys []string

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return keys
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		jsonTag := field.Tag.Get("json")

		// Skip fields without json tags or with "-" tag
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Parse json tag (format: "name,omitempty" or "name" or ",inline")
		tagParts := strings.Split(jsonTag, ",")
		tagName := tagParts[0]

		// Handle embedded/inline structs
		if tagName == "" && len(tagParts) > 1 && tagParts[1] == "inline" {
			// Recursively get keys from embedded struct
			embeddedKeys := getExpectedKeysFromStruct(field.Type)
			keys = append(keys, embeddedKeys...)
		} else if tagName != "" {
			keys = append(keys, tagName)
		}
	}

	return keys
}
