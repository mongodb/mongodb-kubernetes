package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
