package generatememberresources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		{name: "wildcard rejected", raw: "*", memberNs: "mongodb", wantError: "--cluster-scoped"},
		{name: "wildcard in a list rejected", raw: "ns1,*", memberNs: "mongodb", wantError: "--cluster-scoped"},
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
