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

func TestGetImageType(t *testing.T) {
	tests := []struct {
		name                string
		annotations         map[string]string
		defaultArchitecture DefaultArchitecture
		defaultImageType    ImageType
		want                ImageType
	}{
		{
			name:                "non-static defaults to ubi8",
			defaultArchitecture: NonStatic,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI8,
		},
		{
			name:                "non-static ignores annotation",
			annotations:         map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
			defaultArchitecture: NonStatic,
			defaultImageType:    ImageTypeUBI9,
			want:                ImageTypeUBI8,
		},
		{
			name:                "static defaults to ubi9",
			defaultArchitecture: Static,
			want:                ImageTypeUBI9,
		},
		{
			name:                "static uses configured default",
			defaultArchitecture: Static,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "annotation takes precedence",
			annotations:         map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI8)},
			defaultArchitecture: Static,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI8,
		},
		{
			name:                "architecture annotation enables static image type",
			annotations:         map[string]string{ArchitectureAnnotation: string(Static), ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
			defaultArchitecture: NonStatic,
			defaultImageType:    ImageTypeUBI9,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "invalid annotation falls back to configured default",
			annotations:         map[string]string{ContainerImageTypeAnnotation: "invalid"},
			defaultArchitecture: Static,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "invalid default falls back to ubi9",
			defaultArchitecture: Static,
			defaultImageType:    "invalid",
			want:                ImageTypeUBI9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetImageType(tt.annotations, tt.defaultArchitecture, tt.defaultImageType); got != tt.want {
				t.Errorf("GetImageType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainerImageTypeAnnotationWarning(t *testing.T) {
	tests := []struct {
		name                string
		annotations         map[string]string
		defaultArchitecture DefaultArchitecture
		wantWarning         bool
	}{
		{
			name:                "no annotations",
			defaultArchitecture: Static,
		},
		{
			name:                "no image type annotation",
			annotations:         map[string]string{ArchitectureAnnotation: string(Static)},
			defaultArchitecture: NonStatic,
		},
		{
			name:                "supported annotation on static",
			annotations:         map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
			defaultArchitecture: Static,
		},
		{
			name:                "supported annotation with static architecture annotation",
			annotations:         map[string]string{ArchitectureAnnotation: string(Static), ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
			defaultArchitecture: NonStatic,
		},
		{
			name:                "unsupported annotation on static",
			annotations:         map[string]string{ContainerImageTypeAnnotation: "ubi9slim"},
			defaultArchitecture: Static,
			wantWarning:         true,
		},
		{
			name:                "annotation on non-static default",
			annotations:         map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
			defaultArchitecture: NonStatic,
			wantWarning:         true,
		},
		{
			name:                "annotation with non-static architecture annotation",
			annotations:         map[string]string{ArchitectureAnnotation: string(NonStatic), ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
			defaultArchitecture: Static,
			wantWarning:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerImageTypeAnnotationWarning(tt.annotations, tt.defaultArchitecture)
			if tt.wantWarning && got == "" {
				t.Errorf("ContainerImageTypeAnnotationWarning() returned no warning, want one")
			}
			if !tt.wantWarning && got != "" {
				t.Errorf("ContainerImageTypeAnnotationWarning() = %q, want no warning", got)
			}
		})
	}
}

func TestValidateDefaultImageType(t *testing.T) {
	tests := []struct {
		value       string
		want        ImageType
		wantWarning bool
	}{
		{value: "ubi8", want: ImageTypeUBI8},
		{value: "ubi9", want: ImageTypeUBI9},
		{value: "ubi9-slim", want: ImageTypeUBI9Slim},
		{value: "ubi9slim", want: ImageTypeUBI9, wantWarning: true},
		{value: "", want: ImageTypeUBI9, wantWarning: true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, warning := ValidateDefaultImageType(tt.value)
			if got != tt.want {
				t.Errorf("ValidateDefaultImageType(%q) = %v, want %v", tt.value, got, tt.want)
			}
			if tt.wantWarning != (warning != "") {
				t.Errorf("ValidateDefaultImageType(%q) warning = %q, wantWarning %v", tt.value, warning, tt.wantWarning)
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
