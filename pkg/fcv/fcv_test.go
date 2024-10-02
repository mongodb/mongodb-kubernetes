package fcv

import (
	"reflect"
	"testing"

	"k8s.io/utils/ptr"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

func TestCalculateFeatureCompatibilityVersion(t *testing.T) {
	tests := []struct {
		name                  string
		newVersion            string
		lastAppliedFCVVersion string
		currentFCV            *string
		expectedResult        string
	}{
		{
			name:                  "FCV is set",
			newVersion:            "4.4.6",
			lastAppliedFCVVersion: "4.2",
			currentFCV:            ptr.To("4.4"),
			expectedResult:        "4.4",
		},
		{
			name:                  "FCV is set and equal",
			newVersion:            "4.4.6",
			lastAppliedFCVVersion: "4.4",
			currentFCV:            ptr.To("4.4"),
			expectedResult:        "4.4",
		},
		{
			name:                  "FCV is AlwaysMatchVersion, new version is smaller",
			newVersion:            "4.4.6",
			lastAppliedFCVVersion: "5.0",
			currentFCV:            ptr.To(util.AlwaysMatchVersionFCV),
			expectedResult:        "4.4",
		},
		{
			name:                  "FCV is AlwaysMatchVersion, new version is higher",
			newVersion:            "5.0.8",
			lastAppliedFCVVersion: "4.4",
			currentFCV:            ptr.To(util.AlwaysMatchVersionFCV),
			expectedResult:        "5.0",
		},
		{
			name:                  "FCV is nil, new version is higher",
			newVersion:            "5.4.6",
			lastAppliedFCVVersion: "4.2",
			expectedResult:        "4.2",
		},
		{
			name:                  "FCV is nil, old version is higher",
			newVersion:            "4.2.8",
			lastAppliedFCVVersion: "5.4",
			expectedResult:        "4.2",
		},
		{
			name:                  "FCV is nil, jumping 2 versions",
			newVersion:            "6.2.8",
			lastAppliedFCVVersion: "4.4",
			expectedResult:        "6.2",
		},
		{
			name:                  "lastAppliedFCV is empty, first deployment",
			newVersion:            "6.2.8",
			lastAppliedFCVVersion: "",
			expectedResult:        "6.2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateFeatureCompatibilityVersion(tt.newVersion, tt.lastAppliedFCVVersion, tt.currentFCV)
			if !reflect.DeepEqual(result, tt.expectedResult) {
				t.Errorf("expected %v, got %v", tt.expectedResult, result)
			}
		})
	}
}
