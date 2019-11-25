package api

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"
)

// Admin (imported as 'api.Admin') is the client to all "administrator" related operations with Ops Manager
// which do not relate to specific groups (that's why it's different from 'om.Connection'). The only state expected
// to be encapsulated is baseUrl, user and key
type Admin interface {
	// ReadDaemonConfig returns the daemon config by hostname and head db path
	ReadDaemonConfig(hostName, headDbDir string) (*backup.DaemonConfig, *Error)

	// CreateDaemonConfig creates the daemon config with specified hostname and head db path
	CreateDaemonConfig(hostName, headDbDir string) *Error
}

// AdminProvider is a function which returns an instance of Admin interface initialized with connection parameters.
// The parameters can be moved to a separate struct when they grow (e.g. tls is added)
type AdminProvider func(baseUrl, user, publicApiKey string) Admin

// DefaultOmAdmin is the default (production) implementation of Admin interface
type DefaultOmAdmin struct {
	BaseURL      string
	User         string
	PublicAPIKey string
}

var _ Admin = &DefaultOmAdmin{}
var _ Admin = &MockedOmAdmin{}

func NewOmAdmin(baseUrl, user, publicApiKey string) Admin {
	return &DefaultOmAdmin{BaseURL: baseUrl, User: user, PublicAPIKey: publicApiKey}
}

func (a *DefaultOmAdmin) ReadDaemonConfig(hostName, headDbDir string) (*backup.DaemonConfig, *Error) {
	ans, apiErr := a.get("admin/backup/daemon/configs/%s/%s", hostName, url.QueryEscape(headDbDir))
	if apiErr != nil {
		return nil, apiErr
	}
	daemonConfig := &backup.DaemonConfig{}
	if err := json.Unmarshal(ans, daemonConfig); err != nil {
		return nil, NewError(err)
	}

	return daemonConfig, nil
}

func (a *DefaultOmAdmin) CreateDaemonConfig(hostName, headDbDir string) *Error {
	config := backup.DaemonConfig{Machine: backup.MachineConfig{
		HeadRootDirectory: headDbDir,
		MachineHostName:   hostName,
	}}
	// dev note, for creation we don't specify the second path parameter (head db) - it's used only during update
	_, err := a.put("admin/backup/daemon/configs/%s", config, hostName)
	if err != nil {
		return err
	}
	return nil
}

//********************************** Private methods *******************************************************************

func (a *DefaultOmAdmin) get(path string, params ...interface{}) ([]byte, *Error) {
	return a.httpVerb("GET", path, nil, params...)
}

func (a *DefaultOmAdmin) put(path string, v interface{}, params ...interface{}) ([]byte, *Error) {
	return a.httpVerb("PUT", path, v, params...)
}

/*func (a *DefaultOmAdmin) post(path string, v interface{}, params ...interface{}) ([]byte, error) {
	return a.httpVerb("POST", path, v, params...)
}

func (a *DefaultOmAdmin) patch(path string, v interface{}, params ...interface{}) ([]byte, error) {
	return a.httpVerb("PATCH", path, v, params...)
}

func (a *DefaultOmAdmin) delete(path string, params ...interface{}) error {
	_, err := a.httpVerb("DELETE", path, nil, params...)
	return err
}
*/
func (a *DefaultOmAdmin) httpVerb(method, path string, v interface{}, params ...interface{}) ([]byte, *Error) {
	client, err := NewHTTPClient()
	if err != nil {
		return nil, NewError(err)
	}

	path = fmt.Sprintf("/api/public/v1.0/%s", path)
	path = fmt.Sprintf(path, params...)

	response, apiErr := DigestRequest(method, a.BaseURL, path, v, a.User, a.PublicAPIKey, client)
	return response, apiErr
}
