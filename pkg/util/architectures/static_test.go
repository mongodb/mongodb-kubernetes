package architectures

import (
	"testing"
)

func TestIsRunningStaticArchitecture(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
		envFunc     func(t *testing.T)
	}{
		{
			name:        "no annotation and no env",
			annotations: nil,
			want:        false,
			envFunc:     nil,
		},
		{
			name: "only env and is static",
			want: true,
			envFunc: func(t *testing.T) {
				t.Setenv(DefaultEnvArchitecture, string(Static))
			},
		},
		{
			name: "only env and is non static",
			want: false,
			envFunc: func(t *testing.T) {
				t.Setenv(DefaultEnvArchitecture, string(NonStatic))
			},
		},
		{
			name:        "only annotation and is static",
			annotations: map[string]string{ArchitectureAnnotation: string(Static)},
			want:        true,
		},
		{
			name:        "only annotation and is not static",
			annotations: map[string]string{ArchitectureAnnotation: string(NonStatic)},

			want: false,
		},
		{
			name:        "only annotation and is wrong value",
			annotations: map[string]string{ArchitectureAnnotation: "brokenValue"},
			want:        false,
		},
		{
			name:        "env and annotation differ. Annotations has precedence",
			annotations: map[string]string{ArchitectureAnnotation: string(Static)},
			envFunc: func(t *testing.T) {
				t.Setenv(DefaultEnvArchitecture, string(NonStatic))
			},
			want: true,
		},
	}
	for _, tt := range tests {
		testConfig := tt
		t.Run(testConfig.name, func(t *testing.T) {
			if testConfig.envFunc != nil {
				testConfig.envFunc(t)
			}
			if got := IsRunningStaticArchitecture(testConfig.annotations); got != testConfig.want {
				t.Errorf("IsRunningStaticArchitecture() = %v, want %v", got, testConfig.want)
			}
		})
	}
}

func TestGetMongoVersion(t *testing.T) {
	tests := []struct {
		name            string
		mongoDBImage    string
		version         string
		forceEnterprise bool
		architecture    DefaultArchitecture
		want            string
	}{
		{
			name:            "nothing",
			mongoDBImage:    "",
			version:         "8.0.0",
			forceEnterprise: false,
			architecture:    NonStatic,
			want:            "8.0.0",
		},
		{
			name:            "enterprise repo",
			mongoDBImage:    "quay.io/mongodb/mongodb-enterprise-server",
			version:         "8.0.0",
			forceEnterprise: false,
			architecture:    Static,
			want:            "8.0.0-ent",
		},
		{
			name:            "community repo",
			mongoDBImage:    "quay.io/mongodb/mongodb-community-server",
			version:         "8.0.0",
			forceEnterprise: false,
			architecture:    NonStatic,
			want:            "8.0.0",
		},
		{
			name:            "enterprise repo forced",
			mongoDBImage:    "quay.io/mongodb/mongodb-private-server",
			version:         "8.0.0",
			forceEnterprise: true,
			architecture:    Static,
			want:            "8.0.0-ent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetMongoVersionForAutomationConfig(tt.mongoDBImage, tt.version, tt.forceEnterprise, tt.architecture); got != tt.want {
				t.Errorf("GetMongoVersionForAutomationConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}
