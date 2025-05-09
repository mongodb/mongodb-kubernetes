package pprof

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestIsPprofEnabled(t *testing.T) {
	tests := map[string]struct {
		pprofEnabledString string
		operatorEnv        util.OperatorEnvironment
		expected           bool
		expectedErrMsg     string
	}{
		"pprof enabled by default in dev": {
			operatorEnv: util.OperatorEnvironmentDev,
			expected:    true,
		},
		"pprof enabled by default in local": {
			operatorEnv: util.OperatorEnvironmentLocal,
			expected:    true,
		},
		"pprof disabled by default in prod": {
			operatorEnv: util.OperatorEnvironmentProd,
			expected:    false,
		},
		"pprof enabled in prod": {
			pprofEnabledString: "true",
			operatorEnv:        util.OperatorEnvironmentProd,
			expected:           true,
		},
		"pprof explicitly enabled in dev": {
			pprofEnabledString: "true",
			operatorEnv:        util.OperatorEnvironmentDev,
			expected:           true,
		},
		"pprof explicitly enabled in local": {
			pprofEnabledString: "true",
			operatorEnv:        util.OperatorEnvironmentLocal,
			expected:           true,
		},
		"pprof disabled in dev": {
			pprofEnabledString: "false",
			operatorEnv:        util.OperatorEnvironmentDev,
			expected:           false,
		},
		"pprof disabled in local": {
			pprofEnabledString: "false",
			operatorEnv:        util.OperatorEnvironmentLocal,
			expected:           false,
		},
		"pprof disabled explicitly in prod": {
			pprofEnabledString: "false",
			operatorEnv:        util.OperatorEnvironmentProd,
			expected:           false,
		},
		"pprof misconfigured": {
			pprofEnabledString: "false11",
			operatorEnv:        util.OperatorEnvironmentProd,
			expected:           false,
			expectedErrMsg:     "unable to parse PPROF_ENABLED environment variable: strconv.ParseBool: parsing \"false11\": invalid syntax",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := IsPprofEnabled(test.pprofEnabledString, test.operatorEnv)
			if test.expectedErrMsg != "" {
				require.Error(t, err)
				assert.Equal(t, test.expectedErrMsg, err.Error())
			}

			assert.Equal(t, test.expected, result)
		})
	}
}
