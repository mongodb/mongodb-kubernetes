package stringutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
