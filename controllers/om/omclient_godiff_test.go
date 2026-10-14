package om

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/viant/godiff"
	"go.uber.org/zap"
)

func TestLogDeploymentDiffCapturesChanges(t *testing.T) {
	before := Deployment{
		"processes": map[string]any{
			"rs0": map[string]any{"version": "7.0.0", "disabled": false},
		},
	}
	after := Deployment{
		"processes": map[string]any{
			"rs0": map[string]any{"version": "8.0.0", "disabled": false},
		},
	}

	differ, err := godiff.New(reflect.TypeFor[Deployment](), reflect.TypeFor[Deployment]())
	assert.NoError(t, err)

	changelog := differ.Diff(map[string]any(before), map[string]any(after))
	assert.Equal(t, 1, changelog.Size())
	assert.Contains(t, changelog.String(), "8.0.0")

	assert.NoError(t, logDeploymentDiff(before, after, zap.NewNop().Sugar()))
}
