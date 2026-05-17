package searchcontroller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

func TestCreateSearchStatefulSetFunc_JVMFlags(t *testing.T) {
	testCases := []struct {
		name                 string
		userProvidedJVMFlags []string
		userProvidedMemory   string
		expectedJVMFlags     string
	}{
		// Default memory (4G), varying user JVM flags
		{
			name:                 "no jvm flags - default heap from 4Gi memory",
			userProvidedJVMFlags: nil,
			expectedJVMFlags:     `--jvm-flags "-Xmx2048m -Xms2048m"`,
		},
		{
			name:                 "user provides -Xmx only - jvm heap flag is derived from that",
			userProvidedJVMFlags: []string{"-Xmx2g"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2g"`,
		},
		{
			name:                 "user provides -Xms only - jvm heap flag is derived from that",
			userProvidedJVMFlags: []string{"-Xms1g"},
			expectedJVMFlags:     `--jvm-flags "-Xms1g"`,
		},
		{
			name:                 "user provides both -Xmx and -Xms - jvm heap flags are derived from that",
			userProvidedJVMFlags: []string{"-Xmx2g", "-Xms512m"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2g -Xms512m"`,
		},
		{
			name:                 "user provides non-heap flags only - jvm heap flags are derived from default memory req",
			userProvidedJVMFlags: []string{"-XX:+UseG1GC"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2048m -Xms2048m -XX:+UseG1GC"`,
		},
		{
			name:                 "user provides heap and non-heap flags - jvm heap flags are derived from that",
			userProvidedJVMFlags: []string{"-Xmx2g", "-Xms512m", "-XX:+UseG1GC"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2g -Xms512m -XX:+UseG1GC"`,
		},
		// Custom memory, no user JVM flags
		{
			name:               "4Gi memory - half is 2048m",
			userProvidedMemory: "4Gi",
			expectedJVMFlags:   `--jvm-flags "-Xmx2048m -Xms2048m"`,
		},
		{
			name:               "4G memory - half is 1907m",
			userProvidedMemory: "4G",
			expectedJVMFlags:   `--jvm-flags "-Xmx1907m -Xms1907m"`,
		},
		{
			name:               "512Mi memory - half is 256m",
			userProvidedMemory: "512Mi",
			expectedJVMFlags:   `--jvm-flags "-Xmx256m -Xms256m"`,
		},
		{
			name:               "8Gi memory - half is 4096m",
			userProvidedMemory: "8Gi",
			expectedJVMFlags:   `--jvm-flags "-Xmx4096m -Xms4096m"`,
		},
		// Custom memory + user JVM flags combined
		{
			name:                 "8Gi memory with non-heap user flags - auto heap from custom memory",
			userProvidedJVMFlags: []string{"-XX:+UseG1GC"},
			userProvidedMemory:   "8Gi",
			expectedJVMFlags:     `--jvm-flags "-Xmx4096m -Xms4096m -XX:+UseG1GC"`,
		},
		{
			name:                 "8Gi memory with user heap flags - auto heap suppressed",
			userProvidedJVMFlags: []string{"-Xmx2g"},
			userProvidedMemory:   "8Gi",
			expectedJVMFlags:     `--jvm-flags "-Xmx2g"`,
		},
		{
			name:                 "512Mi memory with user heap and non-heap flags - auto heap suppressed",
			userProvidedJVMFlags: []string{"-Xmx256m", "-Xms128m", "-XX:+UseG1GC"},
			userProvidedMemory:   "512Mi",
			expectedJVMFlags:     `--jvm-flags "-Xmx256m -Xms128m -XX:+UseG1GC"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.JVMFlags = tc.userProvidedJVMFlags
				if tc.userProvidedMemory != "" {
					//nolint:staticcheck // SA1019: exercising the legacy single-cluster auto-promotion path.
					s.Spec.ResourceRequirements = &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse(tc.userProvidedMemory),
							corev1.ResourceCPU:    resource.MustParse("2"),
						},
					}
				}
			})

			stsModification, err := CreateSearchStatefulSetFunc(search, "", "", "", "", "", nil, "mongot:latest", false)
			require.NoError(t, err)
			sts := statefulset.New(stsModification)

			// Find the mongot container
			var mongotContainer *containerInfo
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == MongotContainerName {
					mongotContainer = &containerInfo{
						args: sts.Spec.Template.Spec.Containers[i].Args,
					}
					break
				}
			}

			require.NotNil(t, mongotContainer, "mongot container not found in StatefulSet")
			require.Len(t, mongotContainer.args, 2, "the args are of form ['-c', '<script>'], that's why 2 is expected")

			script := mongotContainer.args[1]

			if tc.expectedJVMFlags != "" {
				assert.True(t, strings.Contains(script, tc.expectedJVMFlags),
					"expected args to contain %q, got %q", tc.expectedJVMFlags, script)
			}
		})
	}
}

func TestCreateSearchResourceRequirements(t *testing.T) {
	defaultCPU := construct.ParseQuantityOrZero("2")
	defaultMemory := construct.ParseQuantityOrZero("4Gi")

	testCases := []struct {
		name             string
		userRequirements *corev1.ResourceRequirements
		expectedCPU      resource.Quantity
		expectedMemory   resource.Quantity
	}{
		{
			name:             "nil requirements - full defaults",
			userRequirements: nil,
			expectedCPU:      defaultCPU,
			expectedMemory:   defaultMemory,
		},
		{
			name: "nil Requests - full defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: nil,
			},
			expectedCPU:    defaultCPU,
			expectedMemory: defaultMemory,
		},
		{
			name: "only CPU set - memory defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("4"),
				},
			},
			expectedCPU:    resource.MustParse("4"),
			expectedMemory: defaultMemory,
		},
		{
			name: "only memory set - CPU defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
			expectedCPU:    defaultCPU,
			expectedMemory: resource.MustParse("8Gi"),
		},
		{
			name: "both CPU and memory set - no defaults applied",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
			},
			expectedCPU:    resource.MustParse("8"),
			expectedMemory: resource.MustParse("16Gi"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := createSearchResourceRequirements(tc.userRequirements)

			assert.True(t, result.Requests.Cpu().Equal(tc.expectedCPU),
				"expected CPU %s, got %s", tc.expectedCPU.String(), result.Requests.Cpu().String())
			assert.True(t, result.Requests.Memory().Equal(tc.expectedMemory),
				"expected memory %s, got %s", tc.expectedMemory.String(), result.Requests.Memory().String())
		})
	}
}

type containerInfo struct {
	args []string
}

func TestCreateSearchStatefulSetFunc_LogLevelOverrides(t *testing.T) {
	render := func(t *testing.T) string {
		t.Helper()
		search := newTestMongoDBSearch("test-search", "default")
		stsModification, err := CreateSearchStatefulSetFunc(search, "", "", "", "", "", nil, "mongot:latest", false)
		require.NoError(t, err)
		sts := statefulset.New(stsModification)
		for i := range sts.Spec.Template.Spec.Containers {
			if sts.Spec.Template.Spec.Containers[i].Name == MongotContainerName {
				args := sts.Spec.Template.Spec.Containers[i].Args
				require.Len(t, args, 2, "expected mongot container args of form ['-c', '<script>']")
				return args[1]
			}
		}
		t.Fatal("mongot container not found in StatefulSet")
		return ""
	}

	// When MDB_SEARCH_LOG_LEVEL_OVERRIDES is unset, the mongot container's
	// startup script must NOT contain any logback override surface — no
	// heredoc, no -Dlogback.configurationFile flag.
	t.Run("unset env var leaves the container args unchanged", func(t *testing.T) {
		t.Setenv("MDB_SEARCH_LOG_LEVEL_OVERRIDES", "")
		script := render(t)
		assert.NotContains(t, script, "logback")
		assert.NotContains(t, script, "MONGOT_LOGBACK_OVERRIDE_EOF")
		assert.NotContains(t, script, "-Dlogback.configurationFile")
	})

	// When set, the container args must (a) materialize a logback.xml at
	// /tmp/mongot-logback.xml via a heredoc, (b) include the
	// -Dlogback.configurationFile=... JVM flag pointing at that file, and
	// (c) include a <logger> entry for every requested package.
	t.Run("packages get per-logger entries and the JVM flag is set", func(t *testing.T) {
		t.Setenv("MDB_SEARCH_LOG_LEVEL_OVERRIDES",
			"com.xgen.mongot.server.command.search=TRACE,com.xgen.mongot.server.grpc=DEBUG")
		script := render(t)
		assert.Contains(t, script, "cat > /tmp/mongot-logback.xml <<'MONGOT_LOGBACK_OVERRIDE_EOF'")
		assert.Contains(t, script, `<logger name="com.xgen.mongot.server.command.search" level="TRACE"/>`)
		assert.Contains(t, script, `<logger name="com.xgen.mongot.server.grpc" level="DEBUG"/>`)
		assert.Contains(t, script, "-Dlogback.configurationFile=/tmp/mongot-logback.xml")
		// The heredoc must close before mongot is exec'd.
		eofIdx := strings.Index(script, "MONGOT_LOGBACK_OVERRIDE_EOF\n/mongot-community/mongot")
		assert.NotEqual(t, -1, eofIdx, "expected heredoc terminator immediately before mongot launcher exec")
	})

	t.Run("malformed entries are skipped silently", func(t *testing.T) {
		// Only the well-formed entry survives the parser.
		t.Setenv("MDB_SEARCH_LOG_LEVEL_OVERRIDES", " , =FOO, bar=,com.xgen=TRACE,, ")
		script := render(t)
		assert.Contains(t, script, `<logger name="com.xgen" level="TRACE"/>`)
		// Empty-key / empty-value entries must NOT produce a logger element.
		assert.NotContains(t, script, `name=""`)
		assert.NotContains(t, script, `name="bar"`)
	})
}

func TestParseLogLevelOverrides(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want [][2]string
	}{
		{name: "empty string", raw: "", want: nil},
		{name: "whitespace only", raw: "   ", want: nil},
		{name: "single pair", raw: "com.xgen=TRACE", want: [][2]string{{"com.xgen", "TRACE"}}},
		{
			name: "multi-pair, sorted output",
			raw:  "com.xgen.b=DEBUG,com.xgen.a=TRACE",
			want: [][2]string{{"com.xgen.a", "TRACE"}, {"com.xgen.b", "DEBUG"}},
		},
		{
			name: "trims whitespace and normalizes level case",
			raw:  "  com.xgen = trace ,  io.grpc=Debug  ",
			want: [][2]string{{"com.xgen", "TRACE"}, {"io.grpc", "DEBUG"}},
		},
		{
			name: "duplicate keys: last write wins",
			raw:  "com.xgen=DEBUG,com.xgen=TRACE",
			want: [][2]string{{"com.xgen", "TRACE"}},
		},
		{name: "malformed: no equals", raw: "com.xgen", want: nil},
		{name: "malformed: empty key", raw: "=TRACE", want: nil},
		{name: "malformed: empty value", raw: "com.xgen=", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLogLevelOverrides(tc.raw)
			assert.Equal(t, tc.want, got)
		})
	}
}

