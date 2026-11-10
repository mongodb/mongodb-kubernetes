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
			name:                "only default and is static-ga",
			defaultArchitecture: StaticGA,
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
			name:                "only annotation and is static-ga",
			annotations:         map[string]string{ArchitectureAnnotation: string(StaticGA)},
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
			name:                "non-static without configuration defaults to ubi8",
			defaultArchitecture: NonStatic,
			want:                ImageTypeUBI8,
		},
		{
			name:                "non-static uses configured default",
			defaultArchitecture: NonStatic,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "non-static annotation takes precedence",
			annotations:         map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
			defaultArchitecture: NonStatic,
			defaultImageType:    ImageTypeUBI9,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "non-static architecture annotation honors image type annotation",
			annotations:         map[string]string{ArchitectureAnnotation: string(NonStatic), ContainerImageTypeAnnotation: string(ImageTypeUBI9)},
			defaultArchitecture: Static,
			want:                ImageTypeUBI9,
		},
		{
			name:                "non-static invalid annotation and default fall back to ubi8",
			annotations:         map[string]string{ContainerImageTypeAnnotation: "invalid"},
			defaultArchitecture: NonStatic,
			defaultImageType:    "invalid",
			want:                ImageTypeUBI8,
		},
		{
			name:                "static without configuration defaults to ubi9",
			defaultArchitecture: Static,
			want:                ImageTypeUBI9,
		},
		{
			name:                "static-ga without configuration defaults to ubi9",
			defaultArchitecture: StaticGA,
			want:                ImageTypeUBI9,
		},
		{
			name:                "static-ga annotation defaults to ubi9",
			annotations:         map[string]string{ArchitectureAnnotation: string(StaticGA)},
			defaultArchitecture: NonStatic,
			want:                ImageTypeUBI9,
		},
		{
			name:                "static-ga honors configured image type",
			defaultArchitecture: StaticGA,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "static-ga image type annotation takes precedence",
			annotations:         map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI8)},
			defaultArchitecture: StaticGA,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI8,
		},
		{
			name:                "static uses configured default",
			defaultArchitecture: Static,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "static annotation takes precedence",
			annotations:         map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI8)},
			defaultArchitecture: Static,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI8,
		},
		{
			name:                "static invalid annotation falls back to configured default",
			annotations:         map[string]string{ContainerImageTypeAnnotation: "invalid"},
			defaultArchitecture: Static,
			defaultImageType:    ImageTypeUBI9Slim,
			want:                ImageTypeUBI9Slim,
		},
		{
			name:                "static invalid default falls back to ubi9",
			defaultArchitecture: Static,
			defaultImageType:    "invalid",
			want:                ImageTypeUBI9,
		},
		{
			name:                "static architecture annotation falls back to ubi9",
			annotations:         map[string]string{ArchitectureAnnotation: string(Static)},
			defaultArchitecture: NonStatic,
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
		name        string
		annotations map[string]string
		wantWarning bool
	}{
		{
			name: "no annotations",
		},
		{
			name:        "supported annotation",
			annotations: map[string]string{ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
		},
		{
			name:        "supported annotation with non-static architecture annotation",
			annotations: map[string]string{ArchitectureAnnotation: string(NonStatic), ContainerImageTypeAnnotation: string(ImageTypeUBI9Slim)},
		},
		{
			name:        "unsupported annotation",
			annotations: map[string]string{ContainerImageTypeAnnotation: "ubi9slim"},
			wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerImageTypeAnnotationWarning(tt.annotations)
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
		{value: "ubi9slim", want: "", wantWarning: true},
		{value: "", want: ""},
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

func TestGetArchitecture(t *testing.T) {
	tests := []struct {
		name                string
		annotations         map[string]string
		defaultArchitecture DefaultArchitecture
		want                DefaultArchitecture
	}{
		{name: "default static-ga", defaultArchitecture: StaticGA, want: StaticGA},
		{name: "annotation static-ga", annotations: map[string]string{ArchitectureAnnotation: string(StaticGA)}, defaultArchitecture: Static, want: StaticGA},
		{name: "legacy static annotation", annotations: map[string]string{ArchitectureAnnotation: string(Static)}, defaultArchitecture: StaticGA, want: Static},
		{name: "unknown annotation uses default", annotations: map[string]string{ArchitectureAnnotation: "brokenValue"}, defaultArchitecture: StaticGA, want: StaticGA},
		{name: "empty default is non-static", want: NonStatic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetArchitecture(tt.annotations, tt.defaultArchitecture); got != tt.want {
				t.Errorf("GetArchitecture() = %v, want %v", got, tt.want)
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
			name:         "static-ga enterprise repo",
			mongoDBImage: "quay.io/mongodb/mongodb-enterprise-server",
			version:      "8.0.0",
			architecture: StaticGA,
			want:         "8.0.0-ent",
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
