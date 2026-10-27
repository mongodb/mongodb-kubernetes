package recover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/common"
)

const (
	testCentralCluster = "operator-cluster"
	testNamespace      = "mongodb"
	testServiceAccount = "mongodb-kubernetes-operator-multi-cluster"
)

// only the clusters are read out of the KubeConfig, to resolve the member cluster api server urls.
const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://api.operator-cluster
  name: operator-cluster
- cluster:
    server: https://api.member-cluster-0
  name: member-cluster-0
- cluster:
    server: https://api.member-cluster-1
  name: member-cluster-1
`

var testMemberClusters = []string{"member-cluster-0", "member-cluster-1"}

func TestParseRecoverFlags(t *testing.T) {
	tests := map[string]struct {
		extraArgs     []string
		expectedError string
	}{
		"SourceClusterIsAMemberCluster": {
			extraArgs: []string{"--source-cluster", testMemberClusters[0]},
		},
		"SourceClusterIsNotAMemberCluster": {
			extraArgs:     []string{"--source-cluster", "member-cluster-9"},
			expectedError: "source-cluster has to be one of the healthy member clusters",
		},
		"MemberClusterCAForAClusterThatIsNoLongerAMember": {
			extraArgs:     []string{"--source-cluster", testMemberClusters[0], "--member-cluster-ca", "member-cluster-9=/dev/null"},
			expectedError: "not one of the member clusters",
		},
		"MalformedMemberClusterCA": {
			extraArgs:     []string{"--source-cluster", testMemberClusters[0], "--member-cluster-ca", "no-equals-sign"},
			expectedError: "expected format <member-cluster-name>=<path-to-pem-file>",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := parseFlags(t, tc.extraArgs...)

			if tc.expectedError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expectedError)
		})
	}
}

// parseFlags points the command at a KubeConfig holding the test clusters and feeds it argv the way
// the CLI does. The cobra flag set and the parsed flags are package level state that outlives a
// single test, so they are reset on the way in.
func parseFlags(t *testing.T, extraArgs ...string) error {
	t.Helper()

	kubeConfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeConfigPath, []byte(testKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeConfigPath)

	common.MemberClusterCAFiles = nil
	RecoverFlags.MemberClusterApiServerUrls = nil
	RecoverFlags.SourceCluster = ""

	require.NoError(t, RecoverCmd.Flags().Parse(append([]string{
		"--member-clusters", strings.Join(testMemberClusters, ","),
		"--central-cluster", testCentralCluster,
		"--member-cluster-namespace", testNamespace,
		"--central-cluster-namespace", testNamespace,
		"--service-account", testServiceAccount,
	}, extraArgs...)))

	return parseRecoverFlags(nil)
}
