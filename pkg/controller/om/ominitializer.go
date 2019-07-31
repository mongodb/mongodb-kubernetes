package om

import (
	"encoding/json"
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// This is a separate om functionality needed for alpha version of OM
// The 'TryCreateUser' method doesn't use authentication so doesn't use the digest auth.
// That's why it's not added into the 'omclient.go' file
// It may be removed completely if we decide to go away from OM managed AppDB approach

// User is a json struct corresponding to mms 'ApiAppUserView' object
type User struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// UserKeys is a json struct corresponding to mms 'ApiAppDbUserWithKeys' object
type UserKeys struct {
	ApiKey   string `json:"apiKey"`
	AgentKey string `json:"agentKey"`
}

func TryCreateUser(omUrl string, opsManager *v1.MongoDBOpsManager, user *User) (apiKey, agentKey string, err error) {
	buffer, err := util.SerializeToBuffer(user)
	if err != nil {
		return "", "", err
	}

	client, err := util.NewHTTPClient()
	if err != nil {
		return "", "", err
	}
	resp, err := client.Post(omUrl+"/api/public/v1.0/appdb/unauth/users?pretty=true&whitelist=0.0.0.1%2F0", "application/json; charset=UTF-8", buffer)

	if err != nil {
		return "", "", fmt.Errorf("Error sending POST request to %s: %v", omUrl, err)
	}

	var body []byte
	if resp.Body != nil {
		defer resp.Body.Close()
		// limit size of response body read to 16MB
		body, err = util.ReadAtMost(resp.Body, 16*1024*1024)
		if err != nil {
			return "", "", fmt.Errorf("Error reading response body from %v status=%v", omUrl, resp.StatusCode)
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiError := ParseAPIError(resp.StatusCode, "post", omUrl, body)

		// Any errors except for "first user already created" are bad
		if apiError.ErrorCode != "CANNOT_CREATE_FIRST_USER_USERS_ALREADY_EXIST" {
			return "", "", apiError
		}
		return "", "", nil
	}

	u := &UserKeys{}
	if err := json.Unmarshal(body, u); err != nil {
		return "", "", err
	}
	return u.ApiKey, u.AgentKey, nil
}
