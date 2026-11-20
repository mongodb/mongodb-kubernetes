package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1 "k8s.io/api/core/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestBuildJvmEnvVar(t *testing.T) {
	tests := []struct {
		name         string
		customParams []string
		maxPodMem    int
		minPodMem    int
		want         string
	}{
		{
			name:         "custom params with ssl trust store and a container params",
			customParams: []string{"-Djavax.net.ssl.trustStore=/etc/ssl"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-Djavax.net.ssl.trustStore=/etc/ssl -Xmx4291m -Xms4291m",
		},
		{
			name:         "no container memory keeps OM defaults",
			customParams: []string{"-DFoo"},
			want:         "-DFoo",
		},
		{
			name:         "user xmx takes precedence",
			customParams: []string{"-Xmx10g", "-Xms10g"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-Xmx10g -Xms10g",
		},
		{
			name:         "user MaxHeapSize takes precedence",
			customParams: []string{"-XX:MaxHeapSize=10g"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-XX:MaxHeapSize=10g",
		},
		{
			name:         "user MaxRAMPercentage takes precedence",
			customParams: []string{"-XX:MaxRAMPercentage=65"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-XX:MaxRAMPercentage=65",
		},
		{
			name:         "user InitialRAMPercentage alone keeps operator xmx",
			customParams: []string{"-XX:InitialRAMPercentage=50"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-XX:InitialRAMPercentage=50 -Xmx4291m",
		},
		{
			name:         "user MinRAMPercentage is not a heap setting",
			customParams: []string{"-XX:MinRAMPercentage=50"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-XX:MinRAMPercentage=50 -Xmx4291m -Xms4291m",
		},
		{
			name:         "user MaxRAMPercentage with InitialRAMPercentage takes precedence",
			customParams: []string{"-XX:MaxRAMPercentage=65", "-XX:InitialRAMPercentage=65"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-XX:MaxRAMPercentage=65 -XX:InitialRAMPercentage=65",
		},
		{
			name:         "user xms alone keeps operator xmx",
			customParams: []string{"-Xms2g"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-Xms2g -Xmx4291m",
		},
		{
			name:         "user InitialHeapSize alone keeps operator xmx",
			customParams: []string{"-XX:InitialHeapSize=1073741824"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-XX:InitialHeapSize=1073741824 -Xmx4291m",
		},
		{
			name:         "user xms above operator xmx is overridden so the JVM can start",
			customParams: []string{"-Xms6g"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-Xms6g -Xmx4291m -Xms4291m",
		},
		{
			name:         "unparseable user xms is overridden",
			customParams: []string{"-Xmsfoo"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-Xmsfoo -Xmx4291m -Xms4291m",
		},
		{
			name:         "Xmx inside a property value is not a heap flag",
			customParams: []string{"-Dfoo=Xmx"},
			maxPodMem:    4291,
			minPodMem:    4291,
			want:         "-Dfoo=Xmx -Xmx4291m -Xms4291m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildJvmEnvVar(tt.customParams, tt.maxPodMem, tt.minPodMem))
		})
	}
}

func TestBuildJvmParamsEnvVars_AutoHeap(t *testing.T) {
	sixteenGi := resource.MustParse("16Gi")
	templateWithEnv := func(env ...corev1.EnvVar) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: util.OpsManagerContainerName,
					Env:  env,
					Resources: corev1.ResourceRequirements{
						Limits:   corev1.ResourceList{corev1.ResourceMemory: sixteenGi},
						Requests: corev1.ResourceList{corev1.ResourceMemory: sixteenGi},
					},
				}},
			},
		}
	}

	tests := []struct {
		name       string
		spec       omv1.MongoDBOpsManagerSpec
		env        []corev1.EnvVar
		wantMms    string
		wantBackup string
	}{
		{
			name:       "no auto heap uses 90 percent",
			spec:       omv1.MongoDBOpsManagerSpec{Backup: &omv1.MongoDBOpsManagerBackup{}},
			wantMms:    "-Xmx14745m -Xms14745m",
			wantBackup: "-Xmx14745m -Xms14745m -DDAEMON.DEBUG.PORT=8090",
		},
		{
			name:       "auto heap skips operator flags for ops manager only",
			spec:       omv1.MongoDBOpsManagerSpec{Backup: &omv1.MongoDBOpsManagerBackup{}},
			env:        []corev1.EnvVar{{Name: "OM_AUTO_HEAP", Value: "true"}},
			wantMms:    "",
			wantBackup: "-Xmx14745m -Xms14745m -DDAEMON.DEBUG.PORT=8090",
		},
		{
			name:       "auto heap false keeps operator flags",
			spec:       omv1.MongoDBOpsManagerSpec{Backup: &omv1.MongoDBOpsManagerBackup{}},
			env:        []corev1.EnvVar{{Name: "OM_AUTO_HEAP", Value: "false"}},
			wantMms:    "-Xmx14745m -Xms14745m",
			wantBackup: "-Xmx14745m -Xms14745m -DDAEMON.DEBUG.PORT=8090",
		},
		{
			name:       "last OM_AUTO_HEAP entry wins",
			spec:       omv1.MongoDBOpsManagerSpec{Backup: &omv1.MongoDBOpsManagerBackup{}},
			env:        []corev1.EnvVar{{Name: "OM_AUTO_HEAP", Value: "true"}, {Name: "OM_AUTO_HEAP", Value: "false"}},
			wantMms:    "-Xmx14745m -Xms14745m",
			wantBackup: "-Xmx14745m -Xms14745m -DDAEMON.DEBUG.PORT=8090",
		},
		{
			name: "auto heap keeps user params",
			spec: omv1.MongoDBOpsManagerSpec{
				JVMParams: []string{"-XX:MaxRAMPercentage=65"},
				Backup:    &omv1.MongoDBOpsManagerBackup{JVMParams: []string{"-XX:MaxRAMPercentage=50"}},
			},
			env:        []corev1.EnvVar{{Name: "OM_AUTO_HEAP", Value: "true"}},
			wantMms:    "-XX:MaxRAMPercentage=65",
			wantBackup: "-XX:MaxRAMPercentage=50 -DDAEMON.DEBUG.PORT=8090",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envVars, err := buildJvmParamsEnvVars(tt.spec, util.OpsManagerContainerName, templateWithEnv(tt.env...))
			assert.NoError(t, err)
			assert.Equal(t, tt.wantMms, envVars[0].Value)
			assert.Equal(t, tt.wantBackup, envVars[1].Value)
		})
	}
}

func TestParseJvmSizeMB(t *testing.T) {
	tests := map[string]int64{
		"2g":      2048,
		"2G":      2048,
		"512m":    512,
		"1048576": 1,
		"4096k":   4,
		"1t":      1048576,
		"":        -1,
		"abc":     -1,
		"-1g":     -1,
	}
	for in, want := range tests {
		assert.Equal(t, want, parseJvmSizeMB(in), in)
	}
}
