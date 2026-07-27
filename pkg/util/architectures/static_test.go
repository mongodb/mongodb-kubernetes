package architectures

import (
	"testing"
)

func TestIsRunningStaticArchitecture(t *testing.T) {
	tests := []struct {
		name                string
		annotations         map[string]string
		defaultArchitecture DefaultArchitecture
		want                bool
	}{
		{
			name:                "no annotation and no default",
			annotations:         nil,
			defaultArchitecture: "",
			want:                false,
		},
		{
			name:                "only default and is static",
			defaultArchitecture: Static,
			want:                true,
		},
		{
			name:                "only default and is non-static",
			defaultArchitecture: NonStatic,
			want:                false,
		},
		{
			name:                "only annotation and is static",
			annotations:         map[string]string{ArchitectureAnnotation: string(Static)},
			defaultArchitecture: "",
			want:                true,
		},
		{
			name:                "only annotation and is not static",
			annotations:         map[string]string{ArchitectureAnnotation: string(NonStatic)},
			defaultArchitecture: "",
			want:                false,
		},
		{
			name:                "only annotation and is wrong value",
			annotations:         map[string]string{ArchitectureAnnotation: "brokenValue"},
			defaultArchitecture: "",
			want:                false,
		},
		{
			name:                "annotation takes precedence over default",
			annotations:         map[string]string{ArchitectureAnnotation: string(Static)},
			defaultArchitecture: NonStatic,
			want:                true,
		},
	}
	for _, tt := range tests {
		testConfig := tt
		t.Run(testConfig.name, func(t *testing.T) {
			if got := IsRunningStaticArchitecture(testConfig.annotations, testConfig.defaultArchitecture); got != testConfig.want {
				t.Errorf("IsRunningStaticArchitecture() = %v, want %v", got, testConfig.want)
			}
		})
	}
}

func TestHasSupportedImageTypeSuffix(t *testing.T) {
	tests := []struct {
		name         string
		imageVersion string
		wantFound    bool
		wantSuffix   string
	}{
		{
			name:         "ubi8 suffix",
			imageVersion: "8.0.0-ubi8",
			wantFound:    true,
			wantSuffix:   "ubi8",
		},
		{
			name:         "ubi9 suffix",
			imageVersion: "8.0.0-ubi9",
			wantFound:    true,
			wantSuffix:   "ubi9",
		},
		{
			name:         "ubi9-slim suffix",
			imageVersion: "8.0.0-ubi9-slim",
			wantFound:    true,
			wantSuffix:   "ubi9-slim",
		},
		{
			name:         "no suffix",
			imageVersion: "8.0.0",
			wantFound:    false,
			wantSuffix:   "",
		},
		{
			name:         "unsupported suffix",
			imageVersion: "8.0.0-ubuntu",
			wantFound:    false,
			wantSuffix:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, suffix := HasSupportedImageTypeSuffix(tt.imageVersion)
			if found != tt.wantFound {
				t.Errorf("HasSupportedImageTypeSuffix(%q) found = %v, want %v", tt.imageVersion, found, tt.wantFound)
			}
			if suffix != tt.wantSuffix {
				t.Errorf("HasSupportedImageTypeSuffix(%q) suffix = %q, want %q", tt.imageVersion, suffix, tt.wantSuffix)
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
