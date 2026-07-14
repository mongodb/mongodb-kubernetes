package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestBuildJvmEnvVar(t *testing.T) {
	type args struct {
		customParams       []string
		containerMemParams string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "custom params with ssl trust store and a container params",
			args: args{
				customParams:       []string{"-Djavax.net.ssl.trustStore=/etc/ssl"},
				containerMemParams: "-Xmx4291m -Xms4291m",
			},
			want: "-Djavax.net.ssl.trustStore=/etc/ssl -Xmx4291m -Xms4291m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, buildJvmEnvVar(tt.args.customParams, tt.args.containerMemParams), "buildJvmEnvVar(%v, %v)", tt.args.customParams, tt.args.containerMemParams)
		})
	}
}

func TestGetOpsManagerPodMemPercentage(t *testing.T) {
	t.Run("default when env var not set", func(t *testing.T) {
		assert.Equal(t, util.DefaultOmJvmHeapPercentage, getOpsManagerPodMemPercentage())
	})

	t.Run("returns value from env var", func(t *testing.T) {
		t.Setenv(util.OmJvmHeapPercentageEnv, "80")
		assert.Equal(t, 80, getOpsManagerPodMemPercentage())
	})

	t.Run("returns default for invalid value", func(t *testing.T) {
		t.Setenv(util.OmJvmHeapPercentageEnv, "invalid")
		assert.Equal(t, util.DefaultOmJvmHeapPercentage, getOpsManagerPodMemPercentage())
	})

	t.Run("returns default for zero", func(t *testing.T) {
		t.Setenv(util.OmJvmHeapPercentageEnv, "0")
		assert.Equal(t, util.DefaultOmJvmHeapPercentage, getOpsManagerPodMemPercentage())
	})

	t.Run("returns default for over 100", func(t *testing.T) {
		t.Setenv(util.OmJvmHeapPercentageEnv, "101")
		assert.Equal(t, util.DefaultOmJvmHeapPercentage, getOpsManagerPodMemPercentage())
	})
}

func TestIsOmAutoHeapEnabled(t *testing.T) {
	t.Run("disabled when env var not set", func(t *testing.T) {
		assert.False(t, isOmAutoHeapEnabled())
	})

	t.Run("disabled when env var is false", func(t *testing.T) {
		t.Setenv(util.OmAutoHeapEnv, "false")
		assert.False(t, isOmAutoHeapEnabled())
	})

	t.Run("enabled when env var is true", func(t *testing.T) {
		t.Setenv(util.OmAutoHeapEnv, "true")
		assert.True(t, isOmAutoHeapEnabled())
	})
}
