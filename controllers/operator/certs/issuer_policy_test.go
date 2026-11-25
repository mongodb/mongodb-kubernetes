package certs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
)

func TestParseAllowedIssuers(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		expect  []AllowedIssuer
		invalid string
	}{
		{
			name: "an empty value allows any issuer",
			raw:  "",
		},
		{
			name:   "an entry without a kind means Issuer",
			raw:    "mdb-certs",
			expect: []AllowedIssuer{{Kind: "Issuer", Name: "mdb-certs"}},
		},
		{
			name:   "both kinds are read",
			raw:    "Issuer/mdb-certs,ClusterIssuer/prod-ca",
			expect: []AllowedIssuer{{Kind: "Issuer", Name: "mdb-certs"}, {Kind: "ClusterIssuer", Name: "prod-ca"}},
		},
		{
			name:   "whitespace around entries is ignored",
			raw:    " Issuer/mdb-certs , ClusterIssuer/prod-ca ",
			expect: []AllowedIssuer{{Kind: "Issuer", Name: "mdb-certs"}, {Kind: "ClusterIssuer", Name: "prod-ca"}},
		},
		{
			name:    "an unknown kind is invalid",
			raw:     "Certificate/mdb-certs",
			invalid: `issuer kind "Certificate" is not Issuer or ClusterIssuer`,
		},
		{
			name:    "an entry without a name is invalid",
			raw:     "ClusterIssuer/",
			invalid: `issuer entry "ClusterIssuer/" has no name`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			allowed := ParseAllowedIssuers(tc.raw)
			if tc.invalid != "" {
				assert.Equal(t, tc.invalid, allowed.err)
				assert.False(t, allowed.IsUnrestricted(), "an unreadable value must not allow any issuer")
				return
			}
			assert.Empty(t, allowed.err)
			assert.Equal(t, tc.expect, allowed.issuers)
		})
	}
}

func TestAllowedIssuers_Allows(t *testing.T) {
	allowed := ParseAllowedIssuers("Issuer/mdb-certs,ClusterIssuer/prod-ca")

	assert.True(t, allowed.Allows("Issuer", "mdb-certs"))
	assert.True(t, allowed.Allows("", "mdb-certs"), "an empty kind means Issuer")
	assert.True(t, allowed.Allows("ClusterIssuer", "prod-ca"))
	assert.False(t, allowed.Allows("Issuer", "prod-ca"), "the kind is part of the match")
	assert.False(t, allowed.Allows("ClusterIssuer", "mdb-certs"))
	assert.False(t, allowed.Allows("Issuer", "other"))

	assert.True(t, ParseAllowedIssuers("").IsUnrestricted())
	assert.True(t, ParseAllowedIssuers("").Allows("ClusterIssuer", "anything"))
	assert.False(t, ParseAllowedIssuers("Issuer/mdb-certs").IsUnrestricted())
}

func TestAllowedIssuers_ValidateIssuerRef(t *testing.T) {
	allowed := ParseAllowedIssuers("Issuer/mdb-certs")

	t.Run("an unset issuerRef is allowed", func(t *testing.T) {
		assert.NoError(t, allowed.ValidateIssuerRef(nil, "security.managedCertificate.server.issuerRef"))
		assert.NoError(t, allowed.ValidateIssuerRef(&mdbv1.IssuerRef{}, "security.managedCertificate.server.issuerRef"))
	})

	t.Run("an allowed issuer passes", func(t *testing.T) {
		assert.NoError(t, allowed.ValidateIssuerRef(&mdbv1.IssuerRef{Name: "mdb-certs"}, "security.managedCertificate.server.issuerRef"))
	})

	t.Run("a disallowed issuer fails and names the allowed ones", func(t *testing.T) {
		err := allowed.ValidateIssuerRef(&mdbv1.IssuerRef{Name: "prod-ca", Kind: "ClusterIssuer"}, "spec.security.managedCertificate.issuerRef")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the issuerRef spec.security.managedCertificate.issuerRef points at ClusterIssuer/prod-ca")
		assert.Contains(t, err.Error(), "configured to allow only Issuer/mdb-certs")
	})

	t.Run("a disallowed issuer without a kind is reported as Issuer", func(t *testing.T) {
		err := allowed.ValidateIssuerRef(&mdbv1.IssuerRef{Name: "other"}, "security.managedCertificate.server.issuerRef")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "points at Issuer/other")
	})

	t.Run("an unrestricted operator allows any issuer", func(t *testing.T) {
		assert.NoError(t, ParseAllowedIssuers("").ValidateIssuerRef(&mdbv1.IssuerRef{Name: "any", Kind: "ClusterIssuer"}, "security.managedCertificate.server.issuerRef"))
	})

	t.Run("a value that could not be read fails closed", func(t *testing.T) {
		invalid := ParseAllowedIssuers("Certificate/mdb-certs")
		err := invalid.ValidateIssuerRef(&mdbv1.IssuerRef{Name: "mdb-certs"}, "security.managedCertificate.server.issuerRef")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the operator's allowed-issuer list could not be read")
		assert.Contains(t, err.Error(), `issuer kind "Certificate" is not Issuer or ClusterIssuer`)
		assert.Contains(t, err.Error(), "the issuerRef security.managedCertificate.server.issuerRef cannot be used")
	})
}

func TestCertManagerReadiness_ValidateManagedCertificateIssuers(t *testing.T) {
	readiness := CertManagerReadiness{Ready: true, AllowedIssuers: ParseAllowedIssuers("Issuer/mdb-certs")}

	serverOnly := &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{
		Server: &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "mdb-certs"}},
	}}
	assert.NoError(t, readiness.ValidateManagedCertificateIssuers(serverOnly, ""))

	clientDisallowed := &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{
		Server: &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "mdb-certs"}},
		Client: &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "other"}},
	}}
	err := readiness.ValidateManagedCertificateIssuers(clientDisallowed, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the issuerRef security.managedCertificate.client.issuerRef points at Issuer/other")

	err = readiness.ValidateManagedCertificateIssuers(clientDisallowed, "spec.applicationDatabase.")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the issuerRef spec.applicationDatabase.security.managedCertificate.client.issuerRef points at Issuer/other")

	assert.NoError(t, readiness.ValidateManagedCertificateIssuers(nil, ""))
	assert.NoError(t, readiness.ValidateManagedCertificateIssuers(&mdbv1.Security{}, ""))
}
