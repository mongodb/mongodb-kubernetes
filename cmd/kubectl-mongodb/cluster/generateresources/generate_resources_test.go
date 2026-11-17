package generateresources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name         string
		cluster      string
		operatorNs   string
		workloadNs   string
		wantCluster  string
		wantWorkload []string
		wantError    string
	}{
		{name: "missing member cluster rejected", operatorNs: "mongodb", wantError: "[member-cluster, operator-namespace]"},
		{name: "missing operator namespace rejected", cluster: "cluster-east", wantError: "[member-cluster, operator-namespace]"},
		{name: "whitespace-only member cluster rejected", cluster: "  ", operatorNs: "mongodb", wantError: "[member-cluster, operator-namespace]"},
		{name: "valid member cluster accepted", cluster: "cluster-east", operatorNs: "mongodb", wantCluster: "cluster-east", wantWorkload: []string{"mongodb"}},
		{name: "non RFC 1123 member cluster accepted verbatim", cluster: "gke_east_us", operatorNs: "mongodb", wantCluster: "gke_east_us", wantWorkload: []string{"mongodb"}},
		{name: "workload namespaces honoured", cluster: "cluster-east", operatorNs: "mongodb", workloadNs: "om,mdb", wantCluster: "cluster-east", wantWorkload: []string{"om", "mdb"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			saved := flags
			defer func() { flags = saved }()
			flags.memberCluster = tc.cluster
			flags.operatorNamespace = tc.operatorNs
			flags.workloadNamespaces = tc.workloadNs

			cluster, workload, err := parseFlags()
			if tc.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantCluster, cluster)
			assert.Equal(t, tc.wantWorkload, workload)
		})
	}
}

func TestNormalizeWorkloadNamespaces(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		memberNs  string
		want      []string
		wantError string
	}{
		{name: "blank defaults to member namespace", raw: "", memberNs: "mongodb", want: []string{"mongodb"}},
		{name: "whitespace defaults to member namespace", raw: "  ", memberNs: "mongodb", want: []string{"mongodb"}},
		{name: "single entry", raw: "ns1", memberNs: "mongodb", want: []string{"ns1"}},
		{name: "multiple entries", raw: "ns1,ns2", memberNs: "mongodb", want: []string{"ns1", "ns2"}},
		{name: "entries are trimmed", raw: " ns1 , ns2 ", memberNs: "mongodb", want: []string{"ns1", "ns2"}},
		{name: "duplicates are deduped", raw: "ns1,ns2,ns1", memberNs: "mongodb", want: []string{"ns1", "ns2"}},
		{name: "wildcard rejected", raw: "*", memberNs: "mongodb", wantError: "--operator-cluster-scoped"},
		{name: "wildcard in a list rejected", raw: "ns1,*", memberNs: "mongodb", wantError: "--operator-cluster-scoped"},
		{name: "empty entry rejected", raw: "ns1,,ns2", memberNs: "mongodb", wantError: "non-empty"},
		{name: "trailing comma rejected", raw: "ns1,", memberNs: "mongodb", wantError: "non-empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeWorkloadNamespaces(tc.raw, tc.memberNs)
			if tc.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
