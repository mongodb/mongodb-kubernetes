package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildOMEndpoint(t *testing.T) {
	var endpoint string

	endpoint = buildOMUnauthEndpoint("https://om")
	assert.Equal(t, "https://om/api/public/v1.0/unauth/users?pretty=true&whitelist=0.0.0.0%2F1&whitelist=128.0.0.0%2F1", endpoint)

	endpoint = buildOMUnauthEndpoint("http://om:8080")
	assert.Equal(t, "http://om:8080/api/public/v1.0/unauth/users?pretty=true&whitelist=0.0.0.0%2F1&whitelist=128.0.0.0%2F1", endpoint)
}

func TestFetchOMCredentialsFromResponseOMPre50(t *testing.T) {
	user := User{
		Username:  "some-user",
		Password:  "whatever",
		FirstName: "Some First Name",
		LastName:  "Some Last Name",
	}

	bodyStr := `{"apiKey": "hola"}`
	body := []byte(bodyStr)

	credentials, err := fetchOMCredentialsFromResponse(body, "4.2.2", user)
	assert.NoError(t, err)

	assert.Equal(t, credentials,
		OpsManagerKeyPair{
			PrivateKey: "hola",
			PublicKey:  "some-user",
		})

	bodyStr = `{}`
	body = []byte(bodyStr)

	credentials, err = fetchOMCredentialsFromResponse(body, "4.2.2", user)
	assert.Equal(t, err.Error(), "Could not find a Global API key from Ops Manager")

	assert.Equal(t, credentials,
		OpsManagerKeyPair{
			PrivateKey: "",
			PublicKey:  "",
		})
}

func TestFetchOMCredentialsFromResponseOM50(t *testing.T) {
	user := User{
		Username:  "some-user",
		Password:  "whatever",
		FirstName: "Some First Name",
		LastName:  "Some Last Name",
	}

	bodyStr := `{"programmaticApiKey": {"privateKey": "returned-private-key", "publicKey": "returned-public-key"}}`
	body := []byte(bodyStr)

	expected := OpsManagerKeyPair{
		PrivateKey: "returned-private-key",
		PublicKey:  "returned-public-key",
	}

	var credentials OpsManagerKeyPair
	var err error

	credentials, err = fetchOMCredentialsFromResponse(body, "5.0.1", user)
	assert.NoError(t, err)
	assert.Equal(t, credentials, expected)

	credentials, err = fetchOMCredentialsFromResponse(body, "5.0.2", user)
	assert.NoError(t, err)
	assert.Equal(t, credentials, expected)

	credentials, err = fetchOMCredentialsFromResponse(body, "6.0.2", user)
	assert.NoError(t, err)
	assert.Equal(t, credentials, expected)

	expected = OpsManagerKeyPair{
		PrivateKey: "",
		PublicKey:  "",
	}
	credentials, err = fetchOMCredentialsFromResponse(body, "4.4.99", user)
	assert.Equal(t, err.Error(), "Could not find a Global API key from Ops Manager")
	assert.Equal(t, credentials, expected)
}
