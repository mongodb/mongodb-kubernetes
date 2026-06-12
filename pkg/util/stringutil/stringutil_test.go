package stringutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEncodeUserinfoComponent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pass word", "pass%20word"},
		{"pass+word", "pass%2Bword"},
		{"p@ss", "p%40ss"},
		{"p%25x", "p%2525x"},
		{"my:p@ss/w?rd# %[+]!$&'()*,;=~-._", "my%3Ap%40ss%2Fw%3Frd%23%20%25%5B%2B%5D%21%24%26%27%28%29%2A%2C%3B%3D~-._"},
		{"plain", "plain"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, EncodeUserinfoComponent(c.in), c.in)
	}
}

func TestContainsAny(t *testing.T) {
	assert.True(t, ContainsAny([]string{"one", "two"}, "one"))
	assert.True(t, ContainsAny([]string{"one", "two"}, "two"))
	assert.True(t, ContainsAny([]string{"one", "two"}, "one", "two"))
	assert.True(t, ContainsAny([]string{"one", "two"}, "one", "two", "three"))

	assert.False(t, ContainsAny([]string{"one", "two"}, "three"))
	assert.False(t, ContainsAny([]string{"one", "two"}))
}

func TestCheckCertificateDomains(t *testing.T) {
	assert.True(t, CheckCertificateAddresses([]string{"abd.efg.com", "*.cluster.local", "*.dev.local", "abc.mongodb.com"}, "abc.cluster.local"))
	assert.True(t, CheckCertificateAddresses([]string{"abd.efg.com", "*.cluster.local", "*.dev.local", "abc.mongodb.com"}, "*.cluster.local"))
	assert.True(t, CheckCertificateAddresses([]string{"abd.efg.com", "*.cluster.local", "*.dev.local", "abc.mongodb.com"}, "abd.efg.com"))
	assert.True(t, CheckCertificateAddresses([]string{"abd.efg.com", "*.cluster.local", "*.dev.local", "abc.mongodb.com", "abcdefg"}, "abcdefg"))

	assert.False(t, CheckCertificateAddresses([]string{"abd.efg.com", "*.cluster.local", "*.dev.local", "abc.mongodb.com"}, "abc.efg.com"))
	assert.False(t, CheckCertificateAddresses([]string{"abd.efg.com", "*.cluster.local", "*.dev.local", "abc.mongodb.com", "abcdef"}, "abdcdefg"))
	assert.False(t, CheckCertificateAddresses([]string{"abd.efg.com", "*.cluster.local", "*.dev.local", "abc.mongodb.com", "abcdef"}, "*.somethingthatdoesntfit"))
}
