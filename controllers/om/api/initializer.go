package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/blang/semver"
	"golang.org/x/xerrors"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
)

// This is a separate om functionality needed for OM controller
// The 'TryCreateUser' method doesn't use authentication so doesn't use the digest auth.
// That's why it's not added into the 'omclient.go' file

// Initializer knows how to make calls to Ops Manager to create a first user
type Initializer interface {
	// TryCreateUser makes the call to Ops Manager to create the first admin user. Returns the public API key or an
	// error if the user already exists for example
	TryCreateUser(omUrl string, omVersion string, user User, ca *string) (OpsManagerKeyPair, error)
}

// DefaultInitializer is the "production" implementation of 'Initializer'. Seems we don't need to keep any state
// as the clients won't call the 'TryCreateUser' more than once after struct instantiation
type DefaultInitializer struct{}

// User is a json struct corresponding to mms 'ApiAppUserView' object
type User struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// UserKeys is a json struct corresponding to mms 'ApiAppUserAndApiKeyView'
// object
type UserKeys struct {
	ApiKey string `json:"apiKey"`
}

// ResultProgrammaticAPIKey struct that deserializes to the result of
// calling `unauth/users` Ops Manager endpoint.
type ResultProgrammaticAPIKey struct {
	ProgrammaticAPIKey OpsManagerKeyPair `json:"programmaticApiKey"`
}

// OpsManagerKeyPair convenient type we use to fetch credentials from different
// versions of Ops Manager API.
type OpsManagerKeyPair struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

// TryCreateUser makes the call to the special OM endpoint '/unauth/users' which
// creates a GLOBAL_ADMIN user and generates public API token.
//
// If the endpoint has been called already and a user already exist, then it
// will return HTTP-409.
//
// If this endpoint is called once again with a different `username` the user
// will be created but with no `GLOBAL_ADMIN` role.
// More here: https://www.mongodb.com/docs/ops-manager/current/reference/api/user-create-first/
func (o *DefaultInitializer) TryCreateUser(omUrl string, omVersion string, user User, ca *string) (OpsManagerKeyPair, error) {
	buffer, err := serializeToBuffer(user)
	if err != nil {
		return OpsManagerKeyPair{}, err
	}

	client, err := CreateOMHttpClient(ca, nil, nil)
	if err != nil {
		return OpsManagerKeyPair{}, err
	}
	// dev note: we are doing many similar things that 'http.go' does - though we cannot reuse that now as current
	// request is not a digest one
	endpoint := buildOMUnauthEndpoint(omUrl)
	resp, err := client.Post(endpoint, "application/json; charset=UTF-8", buffer)
	if err != nil {
		return OpsManagerKeyPair{}, xerrors.Errorf("Error sending POST request to %s: %w", endpoint, err)
	}

	var body []byte
	if resp.Body != nil {
		defer func(Body io.ReadCloser) {
			_ = Body.Close()
		}(resp.Body)
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return OpsManagerKeyPair{}, xerrors.Errorf("Error reading response body from %v status=%v", omUrl, resp.StatusCode)
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiError := parseAPIError(resp.StatusCode, "post", omUrl, body)
		return OpsManagerKeyPair{}, apiError
	}

	return fetchOMCredentialsFromResponse(body, omVersion, user)
}

// buildOMUnauthEndpoint returns a string pointing at the unauth/users endpoint
// needed to create the first global owner user.
func buildOMUnauthEndpoint(baseUrl string) string {
	u, err := url.Parse(baseUrl)
	if err != nil {
		panic(fmt.Sprintf("Could not parse %s as a url", baseUrl))
	}

	q := u.Query()
	q.Add("whitelist", "0.0.0.0/1")
	q.Add("whitelist", "128.0.0.0/1")
	q.Add("pretty", "true")

	u.Path = "/api/public/v1.0/unauth/users"
	u.RawQuery = q.Encode()

	return u.String()
}

// fetchOMCredentialsFromResponse returns a `OpsManagerKeyPair` which consist of
// a public and private parts.
//
// This function deserializes the result of calling the `unauth/users` endpoint
// and returns a generic credentials object that can work for both 5.0 and
// pre-5.0 OM versions.
//
// One important detail about the returned value is that in the old-style user
// APIs, the entry called `PrivateAPIKey` corresponds to `PrivateAPIKey` in
// programmatic key, and `Username` corresponds to `PublicAPIKey`.
func fetchOMCredentialsFromResponse(body []byte, omVersion string, user User) (OpsManagerKeyPair, error) {
	version, err := versionutil.StringToSemverVersion(omVersion)
	if err != nil {
		return OpsManagerKeyPair{}, err
	}

	if semver.MustParseRange(">=5.0.0")(version) {
		// Ops Manager 5.0.0+ returns a Programmatic API Key
		apiKey := &ResultProgrammaticAPIKey{}
		if err := json.Unmarshal(body, apiKey); err != nil {
			return OpsManagerKeyPair{}, err
		}

		if apiKey.ProgrammaticAPIKey.PrivateKey != "" && apiKey.ProgrammaticAPIKey.PublicKey != "" {
			return apiKey.ProgrammaticAPIKey, nil
		}

		return OpsManagerKeyPair{}, xerrors.Errorf("Could not fetch credentials from Ops Manager")
	}

	// OpsManager up to 4.4.x return a user API key
	u := &UserKeys{}
	if err := json.Unmarshal(body, u); err != nil {
		return OpsManagerKeyPair{}, err
	}

	if u.ApiKey == "" {
		return OpsManagerKeyPair{}, xerrors.Errorf("Could not find a Global API key from Ops Manager")
	}

	return OpsManagerKeyPair{
		PublicKey:  user.Username,
		PrivateKey: u.ApiKey,
	}, nil
}
