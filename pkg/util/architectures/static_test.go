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
		name    string
		version string
		want    string
		envs    func(t *testing.T)
	}{
		{
			name:    "nothing",
			version: "8.0.0",
			want:    "8.0.0",
			envs: func(t *testing.T) {

			},
		},
		{
			name:    "enterprise repo",
			version: "8.0.0",
			want:    "8.0.0-ent",
			envs: func(t *testing.T) {
				t.Setenv("MONGODB_IMAGE", "quay.io/mongodb/mongodb-enterprise-server")
				t.Setenv(DefaultEnvArchitecture, string(Static))
			},
		},
		{
			name:    "community repo",
			version: "8.0.0",
			want:    "8.0.0",
			envs: func(t *testing.T) {
				t.Setenv("MONGODB_IMAGE", "quay.io/mongodb/mongodb-community-server")
			},
		},
		{
			name:    "enterprise repo forced",
			version: "8.0.0",
			want:    "8.0.0-ent",
			envs: func(t *testing.T) {
				t.Setenv("MONGODB_IMAGE", "quay.io/mongodb/mongodb-private-server")
				t.Setenv("MDB_ASSUME_ENTERPRISE_IMAGE", "True")
				t.Setenv(DefaultEnvArchitecture, string(Static))
			},
		},
	}
	for _, tt := range tests {
		testConfig := tt
		t.Run(testConfig.name, func(t *testing.T) {
			testConfig.envs(t)
			if got := GetMongoVersionForAutomationConfig(testConfig.version, nil); got != testConfig.want {
				t.Errorf("GetMongoVersionForAutomationConfig() = %v, want %v", got, testConfig.want)
			}
		})
	}
}
