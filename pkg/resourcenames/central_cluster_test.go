package resourcenames

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestNormalizeMemberClusterName(t *testing.T) {
	t.Run("RFC 1123 names pass through unchanged", func(t *testing.T) {
		for _, name := range []string{"a", "cluster-east", "gke-foo-bar.us-east-1", "cluster.east"} {
			got, err := NormalizeMemberClusterName(name)
			require.NoError(t, err)
			assert.Equal(t, name, got)
		}
	})

	t.Run("RFC 1123 name exactly at the length budget passes through unchanged", func(t *testing.T) {
		name := strings.Repeat("a", maxMemberClusterNameLength)
		got, err := NormalizeMemberClusterName(name)
		require.NoError(t, err)
		assert.Equal(t, name, got)
	})

	t.Run("non RFC 1123 names are normalized with a hash suffix", func(t *testing.T) {
		tests := []struct {
			logical  string
			wantBase string
		}{
			{logical: "gke_foo_bar", wantBase: "gke-foo-bar"},
			{logical: "GKE_Foo_Bar", wantBase: "gke-foo-bar"},
			{logical: "my cluster", wantBase: "my-cluster"},
			{logical: "_leading-and-trailing_", wantBase: "leading-and-trailing"},
			{logical: ".dots.around.", wantBase: "dots.around"},
			{logical: "a..b", wantBase: "a.b"},
			{logical: "foo..bar", wantBase: "foo.bar"},
			{logical: "a.-b", wantBase: "a.b"},
			{logical: "a-.b", wantBase: "a.b"},
			{logical: "a/.b", wantBase: "a.b"},
			{logical: "a./b", wantBase: "a.b"},
			{logical: "a/-b", wantBase: "a--b"},
			{logical: "a_b..c_d", wantBase: "a-b.c-d"},
			{logical: "cluster.local..x", wantBase: "cluster.local.x"},
			{logical: "a.....b", wantBase: "a.b"},
		}
		for _, tc := range tests {
			t.Run(tc.logical, func(t *testing.T) {
				got, err := NormalizeMemberClusterName(tc.logical)
				require.NoError(t, err)
				assert.Regexp(t, regexp.MustCompile("^"+regexp.QuoteMeta(tc.wantBase)+"-[0-9a-f]{8}$"), got)
				assertValidDerivedName(t, got)
			})
		}
	})

	t.Run("derivation is deterministic", func(t *testing.T) {
		first, err := NormalizeMemberClusterName("gke_foo_bar")
		require.NoError(t, err)
		second, err := NormalizeMemberClusterName("gke_foo_bar")
		require.NoError(t, err)
		assert.Equal(t, first, second)
	})

	t.Run("different originals normalizing to the same base get different hashes", func(t *testing.T) {
		underscore, err := NormalizeMemberClusterName("gke_foo")
		require.NoError(t, err)
		space, err := NormalizeMemberClusterName("gke foo")
		require.NoError(t, err)
		assert.Regexp(t, regexp.MustCompile("^gke-foo-[0-9a-f]{8}$"), underscore)
		assert.Regexp(t, regexp.MustCompile("^gke-foo-[0-9a-f]{8}$"), space)
		assert.NotEqual(t, underscore, space)
	})

	t.Run("name with no alphanumeric characters is rejected", func(t *testing.T) {
		for _, name := range []string{"___", "---", "...", "_-."} {
			_, err := NormalizeMemberClusterName(name)
			require.Error(t, err, "expected %q to be rejected", name)
			assert.Contains(t, err.Error(), "no alphanumeric characters")
		}
	})

	t.Run("long name is truncated within the length budget", func(t *testing.T) {
		got, err := NormalizeMemberClusterName(strings.Repeat("a", 300))
		require.NoError(t, err)
		assert.Len(t, got, maxMemberClusterNameLength)
		assert.Regexp(t, regexp.MustCompile("^a+-[0-9a-f]{8}$"), got)
		assertValidDerivedName(t, got)
	})

	t.Run("RFC 1123 name beyond the length budget goes through the hash path", func(t *testing.T) {
		long := strings.Repeat("a", 240)
		got, err := NormalizeMemberClusterName(long)
		require.NoError(t, err)
		assert.NotEqual(t, long, got)
		assert.Regexp(t, regexp.MustCompile("^a+-[0-9a-f]{8}$"), got)
		assertValidDerivedName(t, got)
	})
}

// assertValidDerivedName checks every derived name is a valid RFC 1123 subdomain within the
// length budget, so the credential Secret name built from it stays within 253 chars.
func assertValidDerivedName(t *testing.T, name string) {
	t.Helper()
	assert.Empty(t, validation.IsDNS1123Subdomain(name), "derived name %q must be RFC 1123 compliant", name)
	assert.LessOrEqual(t, len(name), maxMemberClusterNameLength)
	assert.LessOrEqual(t, len(MemberClusterCredentialSecretName(name)), 253)
}
