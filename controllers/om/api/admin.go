package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
)

type Key struct {
	Description string              `json:"desc"`
	ID          string              `json:"id"`
	Links       []map[string]string `json:"links"`
	PrivateKey  string              `json:"privateKey"`
	PublicKey   string              `json:"publicKey"`
	Roles       []map[string]string `json:"roles"`
}

type GlobalApiKeyRequest struct {
	Description string   `json:"desc"`
	Roles       []string `json:"roles"`
}

type KeyResponse struct {
	ApiKeys []Key `json:"results"`
}

type Whitelist struct {
	CidrBlock   string `json:"cidrBlock"`
	Description string `json:"description"`
}

type S3OplogStoreAdmin interface {
	ReadS3OplogStoreConfigs() ([]backup.S3Config, error)
	UpdateS3OplogConfig(s3Config backup.S3Config) error
	CreateS3OplogStoreConfig(s3Config backup.S3Config) error
	DeleteS3OplogStoreConfig(id string) error
}

type S3StoreAdmin interface {
	// CreateS3Config creates the given S3Config
	CreateS3Config(s3Config backup.S3Config) error

	// UpdateS3Config updates the given S3Config
	UpdateS3Config(s3Config backup.S3Config) error

	// ReadS3Configs returns a list of all S3Configs
	ReadS3Configs() ([]backup.S3Config, error)

	// DeleteS3Config removes an s3config by id
	DeleteS3Config(id string) error
}

type BlockStoreAdmin interface {
	// ReadBlockStoreConfigs returns all Block stores registered in Ops Manager
	ReadBlockStoreConfigs() ([]backup.DataStoreConfig, error)

	// CreateBlockStoreConfig creates an Block store in Ops Manager
	CreateBlockStoreConfig(config backup.DataStoreConfig) error

	// UpdateBlockStoreConfig updates the Block store in Ops Manager
	UpdateBlockStoreConfig(config backup.DataStoreConfig) error

	// DeleteBlockStoreConfig removes the Block store by its ID
	DeleteBlockStoreConfig(id string) error
}

type OplogStoreAdmin interface {
	// ReadOplogStoreConfigs returns all oplog stores registered in Ops Manager
	ReadOplogStoreConfigs() ([]backup.DataStoreConfig, error)

	// CreateOplogStoreConfig creates an oplog store in Ops Manager
	CreateOplogStoreConfig(config backup.DataStoreConfig) error

	// UpdateOplogStoreConfig updates the oplog store in Ops Manager
	UpdateOplogStoreConfig(config backup.DataStoreConfig) error

	// DeleteOplogStoreConfig removes the oplog store by its ID
	DeleteOplogStoreConfig(id string) error
}

// Admin (imported as 'api.Admin') is the client to all "administrator" related operations with Ops Manager
// which do not relate to specific groups (that's why it's different from 'om.Connection'). The only state expected
// to be encapsulated is baseUrl, user and key
type Admin interface {
	S3OplogStoreAdmin
	S3StoreAdmin
	BlockStoreAdmin
	OplogStoreAdmin
	// ReadDaemonConfig returns the daemon config by hostname and head db path
	ReadDaemonConfig(hostName, headDbDir string) (backup.DaemonConfig, error)

	// CreateDaemonConfig creates the daemon config with specified hostname and head db path
	CreateDaemonConfig(hostName, headDbDir string) error

	// ReadFileSystemStoreConfig reads the FileSystemSnapshot store by its ID
	ReadFileSystemStoreConfigs() ([]backup.DataStoreConfig, error)

	// ReadGlobalAPIKeys reads the global API Keys in Ops Manager
	ReadGlobalAPIKeys() ([]Key, error)

	// CreateGlobalAPIKey creates a new Global API Key in Ops Manager
	CreateGlobalAPIKey(description string) (Key, error)

	// ReadOpsManagerVersion read the version returned in the Header
	ReadOpsManagerVersion() (versionutil.OpsManagerVersion, error)
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

func (a *DefaultOmAdmin) ReadDaemonConfig(hostName, headDbDir string) (backup.DaemonConfig, error) {
	ans, _, apiErr := a.get("admin/backup/daemon/configs/%s/%s", hostName, url.QueryEscape(headDbDir))
	if apiErr != nil {
		return backup.DaemonConfig{}, apiErr
	}
	daemonConfig := &backup.DaemonConfig{}
	if err := json.Unmarshal(ans, daemonConfig); err != nil {
		return backup.DaemonConfig{}, apierror.New(err)
	}

	return *daemonConfig, nil
}

func (a *DefaultOmAdmin) CreateDaemonConfig(hostName, headDbDir string) error {
	config := backup.NewDaemonConfig(hostName, headDbDir)
	// dev note, for creation we don't specify the second path parameter (head db) - it's used only during update
	_, _, err := a.put("admin/backup/daemon/configs/%s", config, hostName)
	if err != nil {
		return err
	}
	return nil
}

// ReadOplogStoreConfigs returns all oplog stores registered in Ops Manager
// Some assumption: while the API returns the paginated source we don't handle it to make api simpler (quite unprobable
// to have 500+ configs)
func (a *DefaultOmAdmin) ReadOplogStoreConfigs() ([]backup.DataStoreConfig, error) {
	res, _, err := a.get("admin/backup/oplog/mongoConfigs/")
	if err != nil {
		return nil, err
	}

	dataStoreConfigResponse := &backup.DataStoreConfigResponse{}
	if err = json.Unmarshal(res, dataStoreConfigResponse); err != nil {
		return nil, apierror.New(err)
	}

	return dataStoreConfigResponse.DataStoreConfigs, nil
}

// CreateOplogStoreConfig creates an oplog store in Ops Manager
func (a *DefaultOmAdmin) CreateOplogStoreConfig(config backup.DataStoreConfig) error {
	_, _, err := a.post("admin/backup/oplog/mongoConfigs/", config)
	return err
}

// UpdateOplogStoreConfig updates an oplog store in Ops Manager
func (a *DefaultOmAdmin) UpdateOplogStoreConfig(config backup.DataStoreConfig) error {
	_, _, err := a.put("admin/backup/oplog/mongoConfigs/%s", config, config.Id)
	return err
}

// DeleteOplogStoreConfig removes the oplog store by its ID
func (a *DefaultOmAdmin) DeleteOplogStoreConfig(id string) error {
	return a.delete("admin/backup/oplog/mongoConfigs/%s", id)
}

func (a *DefaultOmAdmin) ReadS3OplogStoreConfigs() ([]backup.S3Config, error) {
	res, _, err := a.get("admin/backup/oplog/s3Configs/")
	if err != nil {
		return nil, err
	}

	s3Configs := &backup.S3ConfigResponse{}
	if err = json.Unmarshal(res, s3Configs); err != nil {
		return nil, apierror.New(err)
	}

	return s3Configs.S3Configs, nil
}

func (a *DefaultOmAdmin) UpdateS3OplogConfig(s3Config backup.S3Config) error {
	_, _, err := a.put("admin/backup/oplog/s3Configs/%s", s3Config, s3Config.Id)
	return err
}

func (a *DefaultOmAdmin) CreateS3OplogStoreConfig(s3Config backup.S3Config) error {
	_, _, err := a.post("admin/backup/oplog/s3Configs", s3Config)
	return err
}

func (a *DefaultOmAdmin) DeleteS3OplogStoreConfig(id string) error {
	return a.delete("admin/backup/oplog/s3Configs/%s", id)
}

// ReadBlockStoreConfigs returns all Block stores registered in Ops Manager
// Some assumption: while the API returns the paginated source we don't handle it to make api simpler (quite unprobable
// to have 500+ configs)
func (a *DefaultOmAdmin) ReadBlockStoreConfigs() ([]backup.DataStoreConfig, error) {
	res, _, err := a.get("admin/backup/snapshot/mongoConfigs/")
	if err != nil {
		return nil, err
	}

	dataStoreConfigResponse := &backup.DataStoreConfigResponse{}
	if err = json.Unmarshal(res, dataStoreConfigResponse); err != nil {
		return nil, apierror.New(err)
	}

	return dataStoreConfigResponse.DataStoreConfigs, nil
}

// CreateBlockStoreConfig creates an Block store in Ops Manager
func (a *DefaultOmAdmin) CreateBlockStoreConfig(config backup.DataStoreConfig) error {
	_, _, err := a.post("admin/backup/snapshot/mongoConfigs/", config)
	return err
}

// UpdateBlockStoreConfig updates an Block store in Ops Manager
func (a *DefaultOmAdmin) UpdateBlockStoreConfig(config backup.DataStoreConfig) error {
	_, _, err := a.put("admin/backup/snapshot/mongoConfigs/%s", config, config.Id)
	return err
}

// DeleteBlockStoreConfig removes the Block store by its ID
func (a *DefaultOmAdmin) DeleteBlockStoreConfig(id string) error {
	return a.delete("admin/backup/snapshot/mongoConfigs/%s", id)
}

// S3 related methods
func (a *DefaultOmAdmin) CreateS3Config(s3Config backup.S3Config) error {
	_, _, err := a.post("admin/backup/snapshot/s3Configs", s3Config)
	return err
}

func (a *DefaultOmAdmin) UpdateS3Config(s3Config backup.S3Config) error {
	_, _, err := a.put("admin/backup/snapshot/s3Configs/%s", s3Config, s3Config.Id)
	return err
}

func (a *DefaultOmAdmin) ReadS3Configs() ([]backup.S3Config, error) {
	res, _, err := a.get("admin/backup/snapshot/s3Configs")
	if err != nil {
		return nil, apierror.New(err)
	}
	s3ConfigResponse := &backup.S3ConfigResponse{}
	if err = json.Unmarshal(res, s3ConfigResponse); err != nil {
		return nil, apierror.New(err)
	}

	return s3ConfigResponse.S3Configs, nil
}

func (a *DefaultOmAdmin) DeleteS3Config(id string) error {
	return a.delete("admin/backup/snapshot/s3Configs/%s", id)
}

func (a *DefaultOmAdmin) ReadFileSystemStoreConfigs() ([]backup.DataStoreConfig, error) {
	res, _, err := a.get("admin/backup/snapshot/fileSystemConfigs/")
	if err != nil {
		return nil, err
	}

	dataStoreConfigResponse := &backup.DataStoreConfigResponse{}
	if err = json.Unmarshal(res, dataStoreConfigResponse); err != nil {
		return nil, apierror.New(err)
	}

	return dataStoreConfigResponse.DataStoreConfigs, nil
}

// ReadGlobalAPIKeys reads the global API Keys in Ops Manager
func (a *DefaultOmAdmin) ReadGlobalAPIKeys() ([]Key, error) {
	res, _, err := a.get("admin/apiKeys")
	if err != nil {
		return nil, err
	}

	apiKeyResponse := &KeyResponse{}
	if err = json.Unmarshal(res, apiKeyResponse); err != nil {
		return nil, apierror.New(err)
	}

	return apiKeyResponse.ApiKeys, nil
}

//addWhitelistEntryIfItDoesntExist adds a whitelist through OM API. If it already exists, in just retun
func (a *DefaultOmAdmin) addWhitelistEntryIfItDoesntExist(cidrBlock string, description string) error {
	_, _, err := a.post("admin/whitelist", Whitelist{
		CidrBlock:   cidrBlock,
		Description: description,
	})
	if apierror.NewNonNil(err).ErrorCode == apierror.DuplicateWhitelistEntry {
		return err
	}
	return nil
}

// CreateGlobalAPIKey creates a new Global API Key in Ops Manager.
func (a *DefaultOmAdmin) CreateGlobalAPIKey(description string) (Key, error) {
	if err := a.addWhitelistEntryIfItDoesntExist("0.0.0.0/1", description); err != nil {
		return Key{}, err
	}
	if err := a.addWhitelistEntryIfItDoesntExist("128.0.0.0/1", description); err != nil {
		return Key{}, err
	}

	newKeyBytes, _, err := a.post("admin/apiKeys", GlobalApiKeyRequest{
		Description: description,
		Roles:       []string{"GLOBAL_OWNER"},
	})
	if err != nil {
		return Key{}, err
	}

	apiKey := &Key{}
	if err := json.Unmarshal(newKeyBytes, apiKey); err != nil {
		return Key{}, err
	}
	return *apiKey, nil

}

// ReadOpsManagerVersion read the version returned in the Header.
func (a *DefaultOmAdmin) ReadOpsManagerVersion() (versionutil.OpsManagerVersion, error) {
	_, header, err := a.get("")
	if err != nil {
		return versionutil.OpsManagerVersion{}, err
	}
	return versionutil.OpsManagerVersion{
		VersionString: versionutil.GetVersionFromOpsManagerApiHeader(header.Get("X-MongoDB-Service-Version")),
	}, nil
}

//********************************** Private methods *******************************************************************

func (a *DefaultOmAdmin) get(path string, params ...interface{}) ([]byte, http.Header, error) {
	return a.httpVerb("GET", path, nil, params...)
}

func (a *DefaultOmAdmin) put(path string, v interface{}, params ...interface{}) ([]byte, http.Header, error) {
	return a.httpVerb("PUT", path, v, params...)
}

func (a *DefaultOmAdmin) post(path string, v interface{}, params ...interface{}) ([]byte, http.Header, error) {
	return a.httpVerb("POST", path, v, params...)
}

//func (a *DefaultOmAdmin) patch(path string, v interface{}, params ...interface{}) ([]byte, error) {
//	return a.httpVerb("PATCH", path, v, params...)
//}

func (a *DefaultOmAdmin) delete(path string, params ...interface{}) error {
	_, _, err := a.httpVerb("DELETE", path, nil, params...)
	return err
}

func (a *DefaultOmAdmin) httpVerb(method, path string, v interface{}, params ...interface{}) ([]byte, http.Header, error) {
	client, err := NewHTTPClient(OptionDigestAuth(a.User, a.PublicAPIKey), OptionSkipVerify)
	if err != nil {
		return nil, nil, apierror.New(err)
	}

	path = fmt.Sprintf("/api/public/v1.0/%s", path)
	path = fmt.Sprintf(path, params...)

	return client.Request(method, a.BaseURL, path, v)
}
