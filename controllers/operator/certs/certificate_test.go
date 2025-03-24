package certs

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

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
