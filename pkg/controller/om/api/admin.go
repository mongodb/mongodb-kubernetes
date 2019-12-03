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
	ReadDaemonConfig(hostName, headDbDir string) (*backup.DaemonConfig, error)

	// CreateDaemonConfig creates the daemon config with specified hostname and head db path
	CreateDaemonConfig(hostName, headDbDir string) error

	// ReadOplogStoreConfigs returns all oplog stores registered in Ops Manager
	ReadOplogStoreConfigs() ([]*backup.DataStoreConfig, error)

	// CreateOplogStoreConfig creates an oplog store in Ops Manager
	CreateOplogStoreConfig(config *backup.DataStoreConfig) error

	// UpdateOplogStoreConfig updates the oplog store in Ops Manager
	UpdateOplogStoreConfig(config *backup.DataStoreConfig) error

	// DeleteOplogStoreConfig removes the oplog store by its ID
	DeleteOplogStoreConfig(id string) error
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

func (a *DefaultOmAdmin) ReadDaemonConfig(hostName, headDbDir string) (*backup.DaemonConfig, error) {
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

func (a *DefaultOmAdmin) CreateDaemonConfig(hostName, headDbDir string) error {
	config := backup.NewDaemonConfig(hostName, headDbDir)
	// dev note, for creation we don't specify the second path parameter (head db) - it's used only during update
	_, err := a.put("admin/backup/daemon/configs/%s", config, hostName)
	if err != nil {
		return err
	}
	return nil
}

// ReadOplogStoreConfigs returns all oplog stores registered in Ops Manager
// Some assumption: while the API returns the paginated source we don't handle it to make api simpler (quite unprobable
// to have 500+ configs)
func (a *DefaultOmAdmin) ReadOplogStoreConfigs() ([]*backup.DataStoreConfig, error) {
	res, err := a.get("admin/backup/oplog/mongoConfigs/")
	if err != nil {
		return nil, err
	}

	dataStoreConfigResponse := &backup.DataStoreConfigResponse{}
	if err = json.Unmarshal(res, dataStoreConfigResponse); err != nil {
		return nil, NewError(err)
	}

	return dataStoreConfigResponse.DataStoreConfigs, nil
}

// CreateOplogStoreConfig creates an oplog store in Ops Manager
func (a *DefaultOmAdmin) CreateOplogStoreConfig(config *backup.DataStoreConfig) error {
	_, err := a.post("admin/backup/oplog/mongoConfigs/", config)

	return err
}

// UpdateOplogStoreConfig updates an oplog store in Ops Manager
func (a *DefaultOmAdmin) UpdateOplogStoreConfig(config *backup.DataStoreConfig) error {
	_, err := a.put("admin/backup/oplog/mongoConfigs/%s", config.Id, config)

	return err
}

// DeleteOplogStoreConfig removes the oplog store by its ID
func (a *DefaultOmAdmin) DeleteOplogStoreConfig(id string) error {
	return a.delete("admin/backup/oplog/mongoConfigs/%s", id)
}

//********************************** Private methods *******************************************************************

func (a *DefaultOmAdmin) get(path string, params ...interface{}) ([]byte, error) {
	return a.httpVerb("GET", path, nil, params...)
}

func (a *DefaultOmAdmin) put(path string, v interface{}, params ...interface{}) ([]byte, error) {
	return a.httpVerb("PUT", path, v, params...)
}

func (a *DefaultOmAdmin) post(path string, v interface{}, params ...interface{}) ([]byte, error) {
	return a.httpVerb("POST", path, v, params...)
}

/*
func (a *DefaultOmAdmin) patch(path string, v interface{}, params ...interface{}) ([]byte, error) {
	return a.httpVerb("PATCH", path, v, params...)
}
*/
func (a *DefaultOmAdmin) delete(path string, params ...interface{}) error {
	_, err := a.httpVerb("DELETE", path, nil, params...)
	return err
}

func (a *DefaultOmAdmin) httpVerb(method, path string, v interface{}, params ...interface{}) ([]byte, error) {
	client, err := NewHTTPClient()
	if err != nil {
		return nil, NewError(err)
	}

	path = fmt.Sprintf("/api/public/v1.0/%s", path)
	path = fmt.Sprintf(path, params...)

	response, apiErr := DigestRequest(method, a.BaseURL, path, v, a.User, a.PublicAPIKey, client)
	return response, apiErr
}
