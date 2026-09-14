package generatememberresources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
)

func TestParseFlags(t *testing.T) {
	original := flags
	defer func() { flags = original }()

	tests := []struct {
		name                  string
		memberNs              string
		serviceAccount        string
		wantServiceAccount    string
		wantCreateCredentials bool
		wantError             string
	}{
		{name: "service account unset resolves to the default and renders credentials", memberNs: "mongodb", wantServiceAccount: resourcenames.MemberClusterServiceAccountName, wantCreateCredentials: true},
		{name: "service account blank resolves to the default", memberNs: "mongodb", serviceAccount: "  ", wantServiceAccount: resourcenames.MemberClusterServiceAccountName, wantCreateCredentials: true},
		{name: "service account set binds to it without rendering credentials", memberNs: "mongodb", serviceAccount: "my-member-sa", wantServiceAccount: "my-member-sa", wantCreateCredentials: false},
		{name: "service account not RFC 1123", memberNs: "mongodb", serviceAccount: "Invalid_SA", wantError: "invalid --member-cluster-service-account"},
		{name: "member-cluster-namespace missing", wantError: "member-cluster-namespace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flags.memberClusterNamespace = tc.memberNs
			flags.memberClusterServiceAccount = tc.serviceAccount
			flags.workloadNamespaces = ""

			serviceAccount, createCredentials, _, err := parseFlags()
			if tc.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantServiceAccount, serviceAccount)
			assert.Equal(t, tc.wantCreateCredentials, createCredentials)
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
