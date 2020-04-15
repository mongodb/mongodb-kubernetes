package api

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

// This is a separate om functionality needed for OM controller
// The 'TryCreateUser' method doesn't use authentication so doesn't use the digest auth.
// That's why it's not added into the 'omclient.go' file

// KubernetesNetMask seems to be the default k8s cluster mask we need to whitelist
// (see https://github.com/kubernetes/kops/issues/2564)
// TODO we can try to guess it (CLOUDP-51402)
const KubernetesNetMask = "100.96.0.0%2F16"

// Initializer knows how to make calls to Ops Manager to create a first user
type Initializer interface {
	// TryCreateUser makes the call to Ops Manager to create the first admin user. Returns the public API key or an
	// error if the user already exists for example
	TryCreateUser(omUrl string, user *User) (string, error)
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

// UserKeys is a json struct corresponding to mms 'ApiAppUserAndApiKeyView' object
type UserKeys struct {
	ApiKey string `json:"apiKey"`
}

// TryCreateUser makes the call to the special OM endpoint '/unauth/users' which creates the GLOBAL_ADMIN user and
// generates public API token. This endpoint returns 409 (Conflict) if the user already exists. Note, that unfortunately
// we cannot ensure the user created is a GLOBAL_ADMIN as if second call to the API is made with another username - the
// user will be created but without GLOBAL_ADMIN permissions. This potentially may result in non-admin API secret if
// the admin removes the API secret and renames the username in the user secret. Though this scenario is almost impossible
func (o *DefaultInitializer) TryCreateUser(omUrl string, user *User) (string, error) {
	buffer, err := serializeToBuffer(user)
	if err != nil {
		return "", err
	}

	// As of now, there is no HTTPS context that we pass to the operator, so we'll skip
	// the HTTPS verification, because this OM instance was just created by the operator itself
	// and we should trust it.
	client, err := NewHTTPClient(OptionSkipVerify)
	if err != nil {
		return "", err
	}
	// dev note: we are doing many similar things that 'http.go' does - though we cannot reuse that now as current
	// request is not a digest one
	resp, err := client.Post(omUrl+"/api/public/v1.0/unauth/users?pretty=true&whitelist=0.0.0.0%2F1&whitelist=128.0.0.0%2F1", "application/json; charset=UTF-8", buffer)

	if err != nil {
		return "", fmt.Errorf("Error sending POST request to %s: %v", omUrl, err)
	}

	var body []byte
	if resp.Body != nil {
		defer resp.Body.Close()
		body, err = ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("Error reading response body from %v status=%v", omUrl, resp.StatusCode)
		}
	}

	if resp.StatusCode == http.StatusOK {
		apiError := parseAPIError(resp.StatusCode, "post", omUrl, body)
		return "", apiError
	}

	u := &UserKeys{}
	if err := json.Unmarshal(body, u); err != nil {
		return "", err
	}
	return u.ApiKey, nil
}
