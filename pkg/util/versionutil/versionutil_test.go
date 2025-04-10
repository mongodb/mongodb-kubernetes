package versionutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetVersionString(t *testing.T) {
	assert.Equal(t, "4.2.4.56729.20191105T2247Z",
		GetVersionFromOpsManagerApiHeader("gitHash=f7bdac406b7beceb1415fd32c81fc64501b6e031; versionString=4.2.4.56729.20191105T2247Z"))
	assert.Equal(t, "4.4.41.12345.20191105T2247Z",
		GetVersionFromOpsManagerApiHeader("gitHash=f7bdac406b7beceb1415fd32c81fc64501b6e031; versionString=4.4.41.12345.20191105T2247Z"))
	assert.Equal(t, "4.3.0.56729.DEFXYZ",
		GetVersionFromOpsManagerApiHeader("gitHash=f7bdac406b7beceb1415fd32c81fc64501b6e031; versionString=4.3.0.56729.DEFXYZ"))
	assert.Equal(t, "31.24.55.202056729.ABCXYZ",
		GetVersionFromOpsManagerApiHeader("gitHash=f7bdac406b7beceb1415fd32c81fc64501b6e031; versionString=31.24.55.202056729.ABCXYZ"))
}

func TestIsInDowngrade(t *testing.T) {
	tests := []struct {
		name            string
		specVersion     string
		lastSpecVersion string
		expected        bool
	}{
		{
			name:            "No downgrade - current version greater",
			specVersion:     "4.4.0",
			lastSpecVersion: "4.2.0",
			expected:        false,
		},
		{
			name:            "Downgrade detected - current version smaller",
			specVersion:     "4.0.0",
			lastSpecVersion: "4.2.0",
			expected:        true,
		},
		{
			name:            "Same version - no downgrade",
			specVersion:     "4.2.0",
			lastSpecVersion: "4.2.0",
			expected:        false,
		},
		{
			name:            "Invalid current version",
			specVersion:     "invalid",
			lastSpecVersion: "4.2.0",
			expected:        false,
		},
		{
			name:            "Invalid last spec version",
			specVersion:     "4.2.0",
			lastSpecVersion: "invalid",
			expected:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, IsDowngrade(tt.lastSpecVersion, tt.specVersion), tt.expected)
		})
	}
}
