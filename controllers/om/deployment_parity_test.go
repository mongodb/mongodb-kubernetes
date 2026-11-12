package om

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildDeploymentFromBytesParity checks that the production decoder and
// encoding/json return the same value for the real Automation Config fixture.
func TestBuildDeploymentFromBytesParity(t *testing.T) {
	fixture, err := os.ReadFile("testdata/automation_config.json")
	require.NoError(t, err)

	var want Deployment
	require.NoError(t, json.Unmarshal(fixture, &want))

	got, err := BuildDeploymentFromBytes(fixture)
	require.NoError(t, err)

	if !reflect.DeepEqual(want, got) {
		t.Fatal("encoding/json and goccy/go-json decoded different values")
	}
}
