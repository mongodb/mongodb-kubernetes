package certs

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
)

func TestManagedIssuanceScopeOwnership(t *testing.T) {
	ctx := context.Background()
	om := managedTestOM()
	c := newManagedCertTestClient(t)

	_, _, _ = EnsureServerCertificateAndCA(ctx, c, om, &omv1.ManagedCertificate{}, serverCertificateOptions(), managedTestLogger())
	_, _, _ = EnsureServerCertificateAndCA(ctx, c, om, &omv1.ManagedCertificate{}, backupServerCertificateOptions(), managedTestLogger())
	_, _, _ = EnsureAppDBCertificatesAndCA(ctx, c, om, []Options{AppDBReplicaSetConfig(om)}, managedTestLogger())

	certs := listManagedCertificates(t, ctx, c)
	require.NotEmpty(t, certs)
	for _, cert := range certs {
		assertOwnedByManagedTestOM(t, cert.OwnerReferences)
		assert.Equal(t, managedTestNamespace, cert.Namespace)
	}

	caChains := []string{
		"example-managed-om-server-ca",
		"example-managed-backup-server-ca",
		"example-db-server-ca",
	}
	seen := map[string]bool{}
	for _, name := range caChains {
		caCert := getManagedCertificate(t, ctx, c, name)
		assert.True(t, caCert.Spec.IsCA, "%s must be a CA", name)
		assert.False(t, seen[name], "CA chain %s is shared between scopes", name)
		seen[name] = true
	}
	assert.NotEqual(t, caChains[0], caChains[1])
	assert.NotEqual(t, caChains[0], caChains[2])
	assert.NotEqual(t, caChains[1], caChains[2])

	for _, name := range []string{"example-managed-om-cert", "example-managed-backup-cert"} {
		cert := getManagedCertificate(t, ctx, c, name)
		assert.Equal(t, memberServerCertUsages, cert.Spec.Usages)
		assert.NotContains(t, cert.Spec.Usages, certmanagerv1.UsageClientAuth)
	}

	appDBLeaf := getManagedCertificate(t, ctx, c, "example-db-managed-cert")
	assert.Equal(t, "example-db-managed-cert", appDBLeaf.Spec.SecretName)
	assert.Equal(t, "example-db-server-ca-issuer", appDBLeaf.Spec.IssuerRef.Name)
	assert.Equal(t, serverAndClientUsage, appDBLeaf.Spec.Usages)
	assert.Contains(t, appDBLeaf.Spec.Usages, certmanagerv1.UsageClientAuth)
	assert.Nil(t, appDBLeaf.Spec.Subject, "SCRAM AppDB server certificates carry no membership subject")
	assert.Empty(t, appDBLeaf.Spec.CommonName)

	for _, name := range []string{
		"example-db-managed-clusterfile",
		"example-db-managed-agent-certs",
		"example-db-client-ca",
	} {
		err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: managedTestNamespace}, &certmanagerv1.Certificate{})
		assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not create Certificate %s", name)
	}
	for _, name := range []string{"example-db-managed-clusterfile", "example-db-managed-agent-certs"} {
		_, err := c.GetSecret(ctx, types.NamespacedName{Name: name, Namespace: managedTestNamespace})
		assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not create client Secret %s", name)
	}
	for _, name := range []string{"example-db-client-selfsigned", "example-db-client-ca-issuer"} {
		err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: managedTestNamespace}, &certmanagerv1.Issuer{})
		assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not create Issuer %s", name)
	}
	err := c.Get(ctx, types.NamespacedName{Name: "example-db-managed-client-ca-bundle", Namespace: managedTestNamespace}, &corev1.ConfigMap{})
	assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not create a client CA bundle")

	issuers := listManagedIssuers(t, ctx, c)
	require.NotEmpty(t, issuers)
	for _, issuer := range issuers {
		assertOwnedByManagedTestOM(t, issuer.OwnerReferences)
	}
}

func TestManagedAppDBCertificateIdentity(t *testing.T) {
	ctx := context.Background()
	om := managedTestOM()

	owner := appDBManagedCertificateOwner{om}
	assert.Equal(t, "example-db", owner.GetName(), "issued objects are named after the AppDB")
	assert.Same(t, om, owner.ownerResource(), "the Ops Manager owns the issued objects")
	assert.Same(t, om.Spec.AppDB.GetSecurity(), owner.GetSecurity())

	c := newManagedCertTestClient(t)
	status, _, _ := EnsureAppDBCertificatesAndCA(ctx, c, om, []Options{AppDBReplicaSetConfig(om)}, managedTestLogger())
	assert.False(t, status.IsOK(), "the certificates have not been reported ready")

	leaf := getManagedCertificate(t, ctx, c, "example-db-managed-cert")
	assert.Equal(t, "example-db-managed-cert", leaf.Spec.SecretName)
	assert.Equal(t, "example-db-server-ca-issuer", leaf.Spec.IssuerRef.Name)
	assert.Equal(t, serverAndClientUsage, leaf.Spec.Usages)
	assert.Contains(t, leaf.Spec.Usages, certmanagerv1.UsageClientAuth)
	assert.False(t, leaf.Spec.IsCA)
	assertOwnedByManagedTestOM(t, leaf.OwnerReferences)

	serverCACert := getManagedCertificate(t, ctx, c, "example-db-server-ca")
	assert.True(t, serverCACert.Spec.IsCA)
	assert.Equal(t, "example-db-server-ca", serverCACert.Spec.SecretName)
	assert.Equal(t, "example-db-server-selfsigned", serverCACert.Spec.IssuerRef.Name)
	assertOwnedByManagedTestOM(t, serverCACert.OwnerReferences)

	certNames := make([]string, 0)
	for _, cert := range listManagedCertificates(t, ctx, c) {
		certNames = append(certNames, cert.Name)
		assert.False(t, strings.HasPrefix(cert.Name, "example-managed-"),
			"AppDB objects must derive their names from the AppDB, got %s", cert.Name)
	}
	assert.ElementsMatch(t, []string{"example-db-managed-cert", "example-db-server-ca"}, certNames,
		"a SCRAM AppDB has exactly one server leaf and its server CA chain")

	issuerNames := make([]string, 0)
	for _, issuer := range listManagedIssuers(t, ctx, c) {
		issuerNames = append(issuerNames, issuer.Name)
		assertOwnedByManagedTestOM(t, issuer.OwnerReferences)
	}
	assert.ElementsMatch(t, []string{"example-db-server-selfsigned", "example-db-server-ca-issuer"}, issuerNames)

	for _, name := range []string{"example-db-managed-clusterfile", "example-db-managed-agent-certs"} {
		_, err := c.GetSecret(ctx, types.NamespacedName{Name: name, Namespace: managedTestNamespace})
		assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not issue client Secret %s", name)
	}
	markManagedCertificatesReady(t, ctx, c, nil)
	serverCA := newTestCA(t, "app-db-server-ca")
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{Name: "example-db-server-ca", Namespace: managedTestNamespace},
		Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte(serverCA.pem)},
	}))
	issued := issuedCertificateSecret(t, managedTestNamespace, "example-db-managed-cert", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	require.NoError(t, c.Create(ctx, &issued))

	status, condition, _ := EnsureAppDBCertificatesAndCA(ctx, c, om, []Options{AppDBReplicaSetConfig(om)}, managedTestLogger())
	require.True(t, status.IsOK(), status.Message())
	assert.Equal(t, v1.ConditionTrue, condition.Status)
	assert.Equal(t, reasonAllCertificatesReady, condition.Reason)

	bundle := managedCAConfigMap(t, ctx, c, "example-db-managed-server-ca-bundle")
	assert.Equal(t, managedTestNamespace, bundle.Namespace)
	assert.Equal(t, expectedBundle(serverCA), bundle.Data["ca-pem"])
	latestHash := bundle.Data[util.LatestHashSecretKey]
	require.NotEmpty(t, latestHash)
	assert.Equal(t, hashOfBundle(expectedBundle(serverCA)), latestHash)
	assert.Equal(t, expectedBundle(serverCA), bundle.Data[latestHash])
	assertOwnedByManagedTestOM(t, bundle.OwnerReferences)

	err := c.Get(ctx, types.NamespacedName{Name: "example-db-managed-client-ca-bundle", Namespace: managedTestNamespace}, &corev1.ConfigMap{})
	assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not publish a client CA bundle")
}

func TestAppDBManagedCertificateOptions(t *testing.T) {
	externalDomain := "external.example.com"
	om := managedTestOM()
	om.Spec.AppDB.Members = 5
	om.Status.AppDbStatus.Members = 3
	om.Spec.AppDB.Service = "appdb-svc"
	om.Spec.AppDB.ClusterDomain = "custom.domain"
	om.Spec.AppDB.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: &externalDomain}
	om.Spec.AppDB.Security.TLSConfig = &mdbv1.TLSConfig{AdditionalCertificateDomains: []string{"alt.example.com"}}

	opts := AppDBManagedCertConfig(om)

	assert.Equal(t, "example-db", opts.ResourceName)
	assert.Equal(t, "example-db-managed-cert", opts.CertSecretName)
	assert.Equal(t, "example-db-managed-clusterfile", opts.InternalClusterSecretName)
	assert.Equal(t, managedTestNamespace, opts.Namespace)
	assert.Equal(t, "appdb-svc", opts.ServiceName)
	assert.Equal(t, "custom.domain", opts.ClusterDomain)
	assert.Equal(t, 5, opts.MembersToCover, "scale-up coverage is the desired replica count")
	require.NotNil(t, opts.ExternalDomain)
	assert.Equal(t, externalDomain, *opts.ExternalDomain)
	assert.Contains(t, opts.additionalCertificateDomains, "alt.example.com")
	assert.Equal(t, om.GetOwnerReferences(), opts.OwnerReference)

	om.Spec.AppDB.Members = 1
	om.Status.AppDbStatus.Members = 3
	assert.Equal(t, 3, AppDBManagedCertConfig(om).MembersToCover, "scale-down coverage stays at the current replica count")
}

func TestAppDBManagedCertificateScaling(t *testing.T) {
	ctx := context.Background()
	om := managedTestOM()
	om.Spec.AppDB.Members = 3
	om.Status.AppDbStatus.Members = 3
	c := newManagedCertTestClient(t)

	issue := func() *certmanagerv1.Certificate {
		t.Helper()
		_, _, _ = EnsureAppDBCertificatesAndCA(ctx, c, om, []Options{AppDBManagedCertConfig(om)}, managedTestLogger())
		return getManagedCertificate(t, ctx, c, "example-db-managed-cert")
	}
	expectedNames := func(members int) []string {
		t.Helper()
		opts := AppDBManagedCertConfig(om)
		opts.Replicas = members
		hostnames, _ := GetDNSNames(opts)
		return hostnames
	}

	initial := issue()
	assert.ElementsMatch(t, expectedNames(3), initial.Spec.DNSNames)
	assert.Equal(t, "3", initial.Annotations[coveredMembersAnnotation])

	om.Spec.AppDB.Members = 5
	scaleUp := issue()
	assert.ElementsMatch(t, expectedNames(5), scaleUp.Spec.DNSNames, "a scale-up must cover every target member in a single update")
	assert.Equal(t, "5", scaleUp.Annotations[coveredMembersAnnotation])

	om.Spec.AppDB.Members = 3
	om.Status.AppDbStatus.Members = 5
	scaleDown := issue()
	assert.Equal(t, scaleUp.Spec.DNSNames, scaleDown.Spec.DNSNames, "a scale-down must not shrink the issued SANs")
	assert.Equal(t, scaleUp.Spec, scaleDown.Spec, "repeated identical coverage must not change the certificate spec")
	assert.Equal(t, "5", scaleDown.Annotations[coveredMembersAnnotation])

	certNames := make([]string, 0)
	for _, cert := range listManagedCertificates(t, ctx, c) {
		certNames = append(certNames, cert.Name)
	}
	assert.ElementsMatch(t, []string{"example-db-managed-cert", "example-db-server-ca"}, certNames,
		"SCRAM AppDB issues one server leaf and one server CA chain")

	for _, name := range []string{"example-db-managed-clusterfile", "example-db-managed-agent-certs"} {
		_, err := c.GetSecret(ctx, types.NamespacedName{Name: name, Namespace: managedTestNamespace})
		assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not issue client Secret %s", name)
	}
	err := c.Get(ctx, types.NamespacedName{Name: "example-db-managed-client-ca-bundle", Namespace: managedTestNamespace}, &corev1.ConfigMap{})
	assert.True(t, apiErrors.IsNotFound(err), "SCRAM AppDB must not publish a client CA bundle")
}

func TestVerifyTLSSecretForStatefulSet(t *testing.T) {
	//nolint
	crt := `-----BEGIN CERTIFICATE-----
MIIFWzCCA0OgAwIBAgIRAJ/vHVZs6TGyhP/AIR+TAvMwDQYJKoZIhvcNAQELBQAw
cTELMAkGA1UEBhMCVVMxETAPBgNVBAgMCE5ldyBZb3JrMREwDwYDVQQHDAhOZXcg
WW9yazEQMA4GA1UECgwHTW9uZ29EQjEQMA4GA1UECwwHbW9uZ29kYjEYMBYGA1UE
AwwPd3d3Lm1vbmdvZGIuY29tMB4XDTIyMDQxMTA5MzkwM1oXDTIyMDQyMTA5Mzkw
M1owADCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBANV64qjt1rmfuYAZ
Ly9rOogtQTiFNJqkfVCzaQ8ImztqxeXAwoNd97Jwm82xgpUR7Mrku0raexIhKD8R
zgOr870L7si4MXmZdkuxvQf+SASOsUuaBtc7epzOzfhV2F/2Rgg/RzZEPNLtF9KL
GWrS73q7cz2ZGbTmc6q6DLikQKVq1d/ufCo1ly0i/kc+Koxu4GdamHxyCysFRqIb
RoFdiRSHEYha65U2I+CERBie+LsxCJOK/uYWzP32XRGsFnbmPFscy5A5WMaeIpX/
Bj8FShEBciwjiFrDktG3FeGd0JkVbUaa10i8xhAw0V66E/bPipKJiSNNxdMVeyhB
g5SQiCUCAwEAAaOCAV0wggFZMB0GA1UdJQQWMBQGCCsGAQUFBwMBBggrBgEFBQcD
AjAMBgNVHRMBAf8EAjAAMB8GA1UdIwQYMBaAFM515VEbUh5N/Tl3GYUuanyfd4BO
MIIBBwYDVR0RAQH/BIH8MIH5ghFyZXBsaWNhLXNldC10bHMtMIIRcmVwbGljYS1z
ZXQtdGxzLTGCEXJlcGxpY2Etc2V0LXRscy0ygj5yZXBsaWNhLXNldC10bHMtMC5y
ZXBsaWNhLXNldC10bHMtc3ZjLnJhajI0Mi5zdmMuY2x1c3Rlci5sb2NhbII+cmVw
bGljYS1zZXQtdGxzLTEucmVwbGljYS1zZXQtdGxzLXN2Yy5yYWoyNDIuc3ZjLmNs
dXN0ZXIubG9jYWyCPnJlcGxpY2Etc2V0LXRscy0yLnJlcGxpY2Etc2V0LXRscy1z
dmMucmFqMjQyLnN2Yy5jbHVzdGVyLmxvY2FsMA0GCSqGSIb3DQEBCwUAA4ICAQCS
/0L4gwWJkfKoA/11EMY2PD+OdUtp0z3DXQH1M34iX8ZGWkej9W0FLDHeipVVKpUY
zoUJSlN2NGd+evSGiVG62z76fUu3ztsdVtL0/P4IILNowmj7KZ6AVfOWX/zYVKsy
EWoQp4dSged5HjvHGNgb7r78W5Ye7Bi6C6aLGBt1OSEA6aDr5HLoChxMFfVFw/gB
UFRZm6bnGRTIsb3NFHjw+CxzLGvf+I+RWzXoc9g5bNKCtlOBKSNao15fFp3YSYLK
9hsGe47sJH7WssPuxAELjJA8UOle3+pSl2mg2OhoYvF3YLTt2vMqOWh0emgEXiXk
eDzw0h6ZuucAXG8YkK0PAvNDHSnemyVtFmo5UUn8rPCRm3Ztxy5lAQlGSk5q1eKG
XxBrBf/eLEE6t6gkV17znQOyRgYgCsD90InLtoBK39dBgZcuDE+KJuY4/6a+GL0R
GgcqDSgjU2OgHXGDJM9GUQlMWOcIaxxEeuC3BYErZPlLg/URSWpfzxYWSxFUGk2p
uD7tgoEguEmih1e1Fc8nIlByx3zj+ifNkuwHytIvip3uvaiQZTEda9i9TMZ6sDeN
1pAwMVMKOGqYBR6ibZJ20vGiKMwBLCQOEqckZg5j2ULHx5LYbbF24uKpMTX8OAtQ
WyU44tyEDe5JUgwo3G/3/Hp6WJARpV1MVy3y5hHNaA==
-----END CERTIFICATE-----`
	//nolint
	key := `-----BEGIN RSA PRIVATE KEY-----
MIIEpgIBAAKCAQEA1XriqO3WuZ+5gBkvL2s6iC1BOIU0mqR9ULNpDwibO2rF5cDC
g133snCbzbGClRHsyuS7Stp7EiEoPxHOA6vzvQvuyLgxeZl2S7G9B/5IBI6xS5oG
1zt6nM7N+FXYX/ZGCD9HNkQ80u0X0osZatLvertzPZkZtOZzqroMuKRApWrV3+58
KjWXLSL+Rz4qjG7gZ1qYfHILKwVGohtGgV2JFIcRiFrrlTYj4IREGJ74uzEIk4r+
5hbM/fZdEawWduY8WxzLkDlYxp4ilf8GPwVKEQFyLCOIWsOS0bcV4Z3QmRVtRprX
SLzGEDDRXroT9s+KkomJI03F0xV7KEGDlJCIJQIDAQABAoIBAQCGltDru/cSVFb5
IeeTt8DRNebWoXSGwomXJWVo6v4jOa/Gp/56H/YX89LmnbE8Fm75g7do+9F3npvn
F2yQ+AnU9/71YNsgVNY15rrMnU3+QZAZn+QMMh2dWuyUUlr2NSf17x8QYXkPahcI
0FWX+aCt+hwvi6SfXmMyEdYPWs6++jND8MpjrKJHoNObD22NloVw2JaT9msmYuI9
j/JnTXbpxWSWbwmyqQOJub4kaTPANCRbTgmTersLk1Iu+Q+jCTtN8teRTmx+3kzy
mq8CdPBrME65yRorPZPGTdGwe2pWiLeug288DGKDvqusSv79piVY0wbK3mZYjX70
MbEwzIfBAoGBAO56pfG9HwwP3ZS6OU22YK6oHY/moLsWYIkcA1qi9v+wQie/Rf+6
tLEFNtQ94hw8kncUDPNv+FtnvpywL/5ZBF0JAAJqxr1tBQKtqvPFzA4qY53kE0Lz
fLLEA1rX4JeRQlDCRei8zGhgAYkWfSDKd6/DHqc0iTKMVMCh5D1BSqU5AoGBAOUq
DPv0uKCGi0s3eQTBgnJi0ivPt8RCmqZMi5Xss4y60sI7dZm5J7tIEZ9/4sqRSC6w
qQBWLwIVnVC3qUhubEXnfk+07mBVLohNTFeWW1Sb7WJp5NnUOeg+w2f5gvbfItzt
cY7l+rWkxuklKlq6AqfZfVk6aWoa8v4MkyUsqYZNAoGBAOAexcu9F+uHEZAPv4Do
UE50UmwFq7KHoivY9tH8a7L6XAHswYVHWz8uDkxC6DfvORrN7inuZfLJOhsZfdFE
qVQh/C9JWAN37IiK3CmDD3WUotAlI3D9UYjTq+95CGqJKlCpc3f5zwScjXTffLMP
dJHrBujO981YkuICg3SJ4vQJAoGBAONXrDnotaDK2TVteul07+x6jPZZw304diO0
nGXHxPg//wYh5rDyNrBc9t69CEjdiDaJm59x4IC44LBLA+2PXmqbFXwNis6WsusV
hD8AMurlJcMUOqy/FhOI8GId7gbrprJ1/Mo+7VF2fr6c2D/ZePj7kpcKk7lnstjF
sNSYUjWhAoGBAKjzEsMga2Lonk5sHkVPkYm8Kl4PrfK4lj1U78to0tgcguLjGyRW
c4IFWO5/ibu3HV3eLMiJBmSR8iR5Iue9cCm782/tncuWLsCoDGUmCG1D2Q4iBx2J
EHE1zXjECRf87xR2aKbPUR+44bG/ILCbUypquP48GC+S6OqVF7utkXgF
-----END RSA PRIVATE KEY-----`
	secretData := map[string][]byte{"tls.crt": []byte(crt), "tls.key": []byte(key)}

	_, err := VerifyTLSSecretForStatefulSet(secretData, Options{})
	assert.NoError(t, err)
}

func TestVerifyTLSSecretForStatefulSetHorizonMemberDifference(t *testing.T) {
	_, err := VerifyTLSSecretForStatefulSet(map[string][]byte{}, Options{
		Replicas: 4,
		horizons: []mdbv1.MongoDBHorizonConfig{
			{"1": "a"},
			{"2": "b"},
			{"3": "c"},
		},
	})

	assert.ErrorContains(t, err, "horizon configs")
}

// TestBuildDNSNamesWithMoreMembersThanHorizons covers what a scale-down goes through: the
// certificate is built for every member the deployment still has, while the horizons are already
// configured for fewer members. The members that still have a horizon keep their names, and
// reading past the end of the horizon list must not panic.
func TestBuildDNSNamesWithMoreMembersThanHorizons(t *testing.T) {
	opts := Options{
		ResourceName: "mdb",
		Namespace:    "ns",
		ServiceName:  "mdb-svc",
		Replicas:     4,
		horizons: []mdbv1.MongoDBHorizonConfig{
			{"1": "mdb-0.example.com"},
			{"2": "mdb-1.example.com"},
			{"3": "mdb-2.example.com"},
		},
	}

	dnsNames := buildDNSNames(opts)

	assert.Contains(t, dnsNames, "mdb-0.example.com")
	assert.Contains(t, dnsNames, "mdb-1.example.com")
	assert.Contains(t, dnsNames, "mdb-2.example.com")
}

// TestGetAdditionalCertDomainsForMemberPastHorizons asks for the additional domains of a member
// whose horizon entry is already gone. That is the state a scale-down leaves behind: the
// deployment still has more members than there are horizon entries configured. The member gets no
// horizon names, and reading its entry must not panic.
func TestGetAdditionalCertDomainsForMemberPastHorizons(t *testing.T) {
	opts := Options{
		ResourceName: "mdb",
		Namespace:    "ns",
		ServiceName:  "mdb-svc",
		Replicas:     4,
		horizons: []mdbv1.MongoDBHorizonConfig{
			{"1": "mdb-0.example.com"},
		},
	}

	assert.NotPanics(t, func() {
		assert.Empty(t, GetAdditionalCertDomainsForMember(opts, 3))
	})
}

// This test uses mock hashes and certificate because CreatePemSecretClient does not verify the certificates.
// However, they are verified before and after calling this method.
func TestRotateCertificate(t *testing.T) {
	ctx := context.Background()
	rs := mdbv1.NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()

	fakeClient, _ := mock.NewDefaultFakeClient(rs)
	fakeSecretClient := secrets.SecretClient{
		VaultClient: nil,
		KubeClient:  fakeClient,
	}

	secretNamespacedName := types.NamespacedName{
		Namespace: "test",
		Name:      "test-replica-set-cert",
	}

	certificateKey1 := "mock_hash_1"
	certificateValue1 := "mock_certificate_1"
	certificate1 := map[string]string{certificateKey1: certificateValue1}

	certificateKey2 := "mock_hash_2"
	certificateValue2 := "mock_certificate_2"
	certificate2 := map[string]string{certificateKey2: certificateValue2}

	certificateKey3 := "mock_hash_3"
	certificateValue3 := "mock_certificate_3"

	pemSecretNamespacedName := secretNamespacedName
	pemSecretNamespacedName.Name = fmt.Sprintf("%s%s", secretNamespacedName.Name, OperatorGeneratedCertSuffix)

	t.Run("Case 1: Enabling TLS", func(t *testing.T) {
		err := CreateOrUpdatePEMSecretWithPreviousCert(ctx, fakeSecretClient, secretNamespacedName, certificateKey1, certificateValue1, []v1.OwnerReference{}, Unused)
		assert.NoError(t, err)

		pemSecret, _ := fakeSecretClient.ReadSecret(ctx, pemSecretNamespacedName, Unused)
		expectedPem := map[string]string{
			util.LatestHashSecretKey: "mock_hash_1",
			"mock_hash_1":            "mock_certificate_1",
		}

		assert.Equal(t, expectedPem, pemSecret)
	})

	t.Run("Case 2: Upgrading the operator, with no rotation", func(t *testing.T) {
		existingPem := secret.Builder().
			SetStringMapToData(certificate1).
			SetNamespace(pemSecretNamespacedName.Namespace).
			SetName(pemSecretNamespacedName.Name).
			Build()

		_ = fakeSecretClient.PutSecret(ctx, existingPem, Unused)

		err := CreateOrUpdatePEMSecretWithPreviousCert(ctx, fakeSecretClient, secretNamespacedName, certificateKey1, certificateValue1, []v1.OwnerReference{}, Unused)
		assert.NoError(t, err)

		pemSecret, _ := fakeSecretClient.ReadSecret(ctx, pemSecretNamespacedName, Unused)
		expectedPem := map[string]string{
			util.LatestHashSecretKey: "mock_hash_1",
			"mock_hash_1":            "mock_certificate_1",
		}

		assert.Equal(t, expectedPem, pemSecret)
	})

	t.Run("Case 3: Upgrading the operator, with a rotation", func(t *testing.T) {
		existingPem := secret.Builder().
			SetStringMapToData(certificate1).
			SetNamespace(pemSecretNamespacedName.Namespace).
			SetName(pemSecretNamespacedName.Name).
			Build()

		_ = fakeSecretClient.PutSecret(ctx, existingPem, Unused)

		// The rotation happens here because CreateOrUpdatePEMSecretWithPreviousCert is called with certificate 2, but the existing pem secret references certificate 1.
		err := CreateOrUpdatePEMSecretWithPreviousCert(ctx, fakeSecretClient, secretNamespacedName, certificateKey2, certificateValue2, []v1.OwnerReference{}, Unused)
		assert.NoError(t, err)

		pemSecret, _ := fakeSecretClient.ReadSecret(ctx, pemSecretNamespacedName, Unused)
		expectedPem := map[string]string{
			util.LatestHashSecretKey:   "mock_hash_2",
			util.PreviousHashSecretKey: "mock_hash_1",
			"mock_hash_1":              "mock_certificate_1",
			"mock_hash_2":              "mock_certificate_2",
		}

		assert.Equal(t, expectedPem, pemSecret)
	})

	t.Run("Case 4: First TLS rotation", func(t *testing.T) {
		existingPemData := certificate1
		existingPemData[util.LatestHashSecretKey] = "mock_hash_1"
		existingPem := secret.Builder().
			SetStringMapToData(existingPemData).
			SetNamespace(pemSecretNamespacedName.Namespace).
			SetName(pemSecretNamespacedName.Name).
			Build()

		_ = fakeSecretClient.PutSecret(ctx, existingPem, Unused)

		err := CreateOrUpdatePEMSecretWithPreviousCert(ctx, fakeSecretClient, secretNamespacedName, certificateKey2, certificateValue2, []v1.OwnerReference{}, Unused)
		assert.NoError(t, err)

		pemSecret, _ := fakeSecretClient.ReadSecret(ctx, pemSecretNamespacedName, Unused)
		expectedPem := map[string]string{
			util.LatestHashSecretKey:   "mock_hash_2",
			util.PreviousHashSecretKey: "mock_hash_1",
			"mock_hash_1":              "mock_certificate_1",
			"mock_hash_2":              "mock_certificate_2",
		}

		assert.Equal(t, expectedPem, pemSecret)
	})

	t.Run("Case 5: Subsequent TLS rotations", func(t *testing.T) {
		existingPemData := merge.StringToStringMap(certificate1, certificate2)
		existingPemData[util.LatestHashSecretKey] = "mock_hash_2"
		existingPemData[util.PreviousHashSecretKey] = "mock_hash_1"
		existingPem := secret.Builder().
			SetStringMapToData(existingPemData).
			SetNamespace(pemSecretNamespacedName.Namespace).
			SetName(pemSecretNamespacedName.Name).
			Build()

		_ = fakeSecretClient.PutSecret(ctx, existingPem, Unused)

		err := CreateOrUpdatePEMSecretWithPreviousCert(ctx, fakeSecretClient, secretNamespacedName, certificateKey3, certificateValue3, []v1.OwnerReference{}, Unused)
		assert.NoError(t, err)

		pemSecret, _ := fakeSecretClient.ReadSecret(ctx, pemSecretNamespacedName, Unused)
		expectedPem := map[string]string{
			util.LatestHashSecretKey:   "mock_hash_3",
			util.PreviousHashSecretKey: "mock_hash_2",
			"mock_hash_3":              "mock_certificate_3",
			"mock_hash_2":              "mock_certificate_2",
		}

		assert.Equal(t, expectedPem, pemSecret)
	})
}

// putPEMSecret writes a -pem secret into the fake cluster, to set up what already exists before the
// code under test runs. The name is the -pem name, for example my-rs-cert-pem.
func putPEMSecret(ctx context.Context, t *testing.T, client secrets.SecretClient, name string, data map[string]string) {
	pem := secret.Builder().SetName(name).SetNamespace(mock.TestNamespace).SetStringMapToData(data).Build()
	assert.NoError(t, client.PutSecret(ctx, pem, Unused))
}

// newFakeSecretClient returns the secret client the tests use. It talks to a fake Kubernetes client
// instead of a cluster, so the real read and write code runs without one.
func newFakeSecretClient() secrets.SecretClient {
	fakeClient, _ := mock.NewDefaultFakeClient(mdbv1.NewReplicaSetBuilder().Build())
	return secrets.SecretClient{KubeClient: fakeClient}
}

// This test walks the whole migration: the seeding, and then the write the operator does in every
// reconcile. Together they have to leave each managed -pem secret holding the legacy certificate next
// to the managed one, like a rotation does, so a restarted pod still finds the file the automation
// config names until the config is updated.
func TestSeedManagedPEMSecretsFromLegacy_KeepsTheLegacyCertificateLikeARotation(t *testing.T) {
	ctx := context.Background()
	client := newFakeSecretClient()
	// The security of the last applied spec and of the spec now: legacy TLS first, managed after.
	legacy := &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true, CA: "issuer-ca"}}
	managed := &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}

	// create the legacy -pem secrets the pods mount before the migration
	putPEMSecret(ctx, t, client, "my-rs-cert-pem", map[string]string{util.LatestHashSecretKey: "legacy_member", "legacy_member": "legacy member cert"})
	putPEMSecret(ctx, t, client, "my-rs-clusterfile-pem", map[string]string{util.LatestHashSecretKey: "legacy_cf", "legacy_cf": "legacy clusterfile cert"})
	putPEMSecret(ctx, t, client, "agent-certs-pem", map[string]string{util.LatestHashSecretKey: "legacy_agent", "legacy_agent": "legacy agent cert"})

	// seed the managed -pem secrets with the legacy certificates
	assert.NoError(t, SeedManagedPEMSecretsFromLegacy(ctx, client, mock.TestNamespace, "my-rs", "my-rs", legacy, managed, zap.S()))

	// CreateOrUpdatePEMSecretWithPreviousCert creates or updates the -pem secret with the managed
	// certificate, and keeps the certificate that was the latest one until now as the previous one.
	// The seeding above made the legacy certificate the latest one, so the secret ends up holding both.
	for _, c := range []struct{ secretName, hash, value, legacyHash, legacyValue string }{
		{"my-rs-managed-cert", "managed_member", "managed member cert", "legacy_member", "legacy member cert"},
		{"my-rs-managed-clusterfile", "managed_cf", "managed clusterfile cert", "legacy_cf", "legacy clusterfile cert"},
		{"my-rs-managed-agent-certs", "managed_agent", "managed agent cert", "legacy_agent", "legacy agent cert"},
	} {
		key := types.NamespacedName{Namespace: mock.TestNamespace, Name: c.secretName}
		assert.NoError(t, CreateOrUpdatePEMSecretWithPreviousCert(ctx, client, key, c.hash, c.value, nil, Unused))

		pem, err := client.ReadSecret(ctx, types.NamespacedName{Namespace: mock.TestNamespace, Name: c.secretName + "-pem"}, Unused)
		assert.NoError(t, err)
		assert.Equal(t, map[string]string{
			util.LatestHashSecretKey:   c.hash,
			util.PreviousHashSecretKey: c.legacyHash,
			c.hash:                     c.value,
			c.legacyHash:               c.legacyValue,
		}, pem, c.secretName)
	}
}

// A legacy deployment that used certsSecretPrefix names its secrets after the prefix, and a deployment
// without x509 internal cluster auth has no clusterfile secret at all. The seeding works out both names
// from the last applied spec, so it has to handle both.
func TestSeedManagedPEMSecretsFromLegacy_PrefixInstall(t *testing.T) {
	ctx := context.Background()
	client := newFakeSecretClient()
	legacy := &mdbv1.Security{CertificatesSecretsPrefix: "prefix"}
	managed := &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}

	// the legacy -pem secrets of a prefixed install, with no clusterfile secret
	putPEMSecret(ctx, t, client, "prefix-my-rs-cert-pem", map[string]string{util.LatestHashSecretKey: "legacy_member", "legacy_member": "legacy member cert"})
	putPEMSecret(ctx, t, client, "prefix-my-rs-agent-certs-pem", map[string]string{util.LatestHashSecretKey: "legacy_agent", "legacy_agent": "legacy agent cert"})

	// seed the managed -pem secrets, copying from the prefixed legacy names
	assert.NoError(t, SeedManagedPEMSecretsFromLegacy(ctx, client, mock.TestNamespace, "my-rs", "my-rs", legacy, managed, zap.S()))

	// each managed secret holds the legacy certificate for now, with latestHash naming it
	member, err := client.ReadSecret(ctx, types.NamespacedName{Namespace: mock.TestNamespace, Name: "my-rs-managed-cert-pem"}, Unused)
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{util.LatestHashSecretKey: "legacy_member", "legacy_member": "legacy member cert"}, member)

	agent, err := client.ReadSecret(ctx, types.NamespacedName{Namespace: mock.TestNamespace, Name: "my-rs-managed-agent-certs-pem"}, Unused)
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{util.LatestHashSecretKey: "legacy_agent", "legacy_agent": "legacy agent cert"}, agent)

	// There was no legacy clusterfile, so there is nothing to keep and the managed clusterfile secret
	// is not created here. The write that follows creates it the normal way.
	_, err = client.ReadSecret(ctx, types.NamespacedName{Namespace: mock.TestNamespace, Name: "my-rs-managed-clusterfile-pem"}, Unused)
	assert.True(t, secret.SecretNotExist(err))
}

// The operator reconciles many times during a migration, so the seeding runs again on every pass. Once
// a managed -pem secret exists it holds the managed certificate, so seeding again must not put the
// legacy certificate back as the latest: that would undo the migration and leave the automation config
// naming a file that is no longer the latest one.
func TestSeedManagedPEMSecretsFromLegacy_LeavesAnExistingManagedSecretAlone(t *testing.T) {
	ctx := context.Background()
	client := newFakeSecretClient()
	legacy := &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}
	managed := &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}

	// the legacy -pem secret the pods mount before the migration
	putPEMSecret(ctx, t, client, "my-rs-cert-pem", map[string]string{util.LatestHashSecretKey: "legacy_member", "legacy_member": "legacy member cert"})
	// the managed -pem secret as it looks after the first pass: both certificates, the managed one as
	// latest and the legacy one as previous
	existing := map[string]string{
		util.LatestHashSecretKey:   "managed_member",
		util.PreviousHashSecretKey: "legacy_member",
		"managed_member":           "managed member cert",
		"legacy_member":            "legacy member cert",
	}
	putPEMSecret(ctx, t, client, "my-rs-managed-cert-pem", existing)

	// the seeding runs again on the next reconcile and must leave the secret as it is
	assert.NoError(t, SeedManagedPEMSecretsFromLegacy(ctx, client, mock.TestNamespace, "my-rs", "my-rs", legacy, managed, zap.S()))

	pem, err := client.ReadSecret(ctx, types.NamespacedName{Namespace: mock.TestNamespace, Name: "my-rs-managed-cert-pem"}, Unused)
	assert.NoError(t, err)
	assert.Equal(t, existing, pem)
}
