package api

import (
	"errors"
	"fmt"
	"sort"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
)

// ********************************************************************************************************************
// This is a mock for om admin. It's created as a normal (not a test) go file to allow different packages use it for
// testing.
// Surely don't use it in production :)
// ********************************************************************************************************************

// Global variable for current OM admin object that was created by MongoDbOpsManager - just for tests
// It's important to clear the state on time - the lifespan of om admin is supposed to be bound to a lifespan of a
// OM controller instance - once a new OM controller is created the mocked admin state must be cleared
var CurrMockedAdmin *MockedOmAdmin

type MockedOmAdmin struct {
	// These variables are not used internally but are used to check the correctness of parameters passed by the controller
	BaseURL    string
	PublicKey  string
	PrivateKey string

	daemonConfigs          []backup.DaemonConfig
	s3Configs              map[string]backup.S3Config
	s3OpLogConfigs         map[string]backup.S3Config
	oplogConfigs           map[string]backup.DataStoreConfig
	blockStoreConfigs      map[string]backup.DataStoreConfig
	fileSystemStoreConfigs map[string]backup.DataStoreConfig
	apiKeys                []Key
}

// NewMockedAdminProvider is the function creating the admin object. The function returns the existing mocked admin instance
// if it exists - this allows to survive through multiple reconciliations and to keep OM state over them
func NewMockedAdminProvider(baseUrl, publicApiKey, privateApiKey string) OpsManagerAdmin {
	CurrMockedAdmin = &MockedOmAdmin{}
	CurrMockedAdmin.BaseURL = baseUrl
	CurrMockedAdmin.PublicKey = publicApiKey
	CurrMockedAdmin.PrivateKey = privateApiKey

	CurrMockedAdmin.daemonConfigs = make([]backup.DaemonConfig, 0)
	CurrMockedAdmin.s3Configs = make(map[string]backup.S3Config)
	CurrMockedAdmin.s3OpLogConfigs = make(map[string]backup.S3Config)
	CurrMockedAdmin.oplogConfigs = make(map[string]backup.DataStoreConfig)
	CurrMockedAdmin.blockStoreConfigs = make(map[string]backup.DataStoreConfig)
	CurrMockedAdmin.apiKeys = []Key{{
		PrivateKey: privateApiKey,
		PublicKey:  publicApiKey,
	}}

	return CurrMockedAdmin
}

// NewMockedAdmin creates an empty mocked om admin. This must be called by tests when the Om controller is created to
// make sure the state is cleaned
func NewMockedAdmin() *MockedOmAdmin {
	CurrMockedAdmin = &MockedOmAdmin{}
	return CurrMockedAdmin
}

func (a *MockedOmAdmin) ReadDaemonConfig(hostName, headDbDir string) (backup.DaemonConfig, error) {
	for _, v := range a.daemonConfigs {
		if v.Machine.HeadRootDirectory == headDbDir && v.Machine.MachineHostName == hostName {
			return v, nil
		}
	}
	return backup.DaemonConfig{}, apierror.NewErrorWithCode(apierror.BackupDaemonConfigNotFound)
}

func (a *MockedOmAdmin) CreateDaemonConfig(hostName, headDbDir string) error {
	config := backup.NewDaemonConfig(hostName, headDbDir)

	for _, dConf := range a.daemonConfigs {
		// Ops Manager should never be performing Update Operations, only Creations
		if dConf.Machine.HeadRootDirectory == headDbDir && dConf.Machine.MachineHostName == hostName {
			panic(fmt.Sprintf("Config %s, %s already exists", hostName, headDbDir))
		}
	}

	a.daemonConfigs = append(a.daemonConfigs, config)
	return nil
}

func (a *MockedOmAdmin) ReadS3Configs() ([]backup.S3Config, error) {
	allConfigs := make([]backup.S3Config, 0)
	for _, v := range a.s3Configs {
		allConfigs = append(allConfigs, v)
	}

	sort.SliceStable(allConfigs, func(i, j int) bool {
		return allConfigs[i].Id < allConfigs[j].Id
	})

	return allConfigs, nil
}

func (a *MockedOmAdmin) DeleteS3Config(id string) error {
	if _, ok := a.s3Configs[id]; !ok {
		return errors.New("Failed to remove as the s3 config doesn't exist!")
	}
	delete(a.s3Configs, id)
	return nil
}

func (a *MockedOmAdmin) CreateS3Config(s3Config backup.S3Config) error {
	a.s3Configs[s3Config.Id] = s3Config
	return nil
}

func (a *MockedOmAdmin) UpdateS3Config(s3Config backup.S3Config) error {
	return a.CreateS3Config(s3Config)
}

func (a *MockedOmAdmin) ReadOplogStoreConfigs() ([]backup.DataStoreConfig, error) {
	allConfigs := make([]backup.DataStoreConfig, 0)
	for _, v := range a.oplogConfigs {
		allConfigs = append(allConfigs, v)
	}

	sort.SliceStable(allConfigs, func(i, j int) bool {
		return allConfigs[i].Id < allConfigs[j].Id
	})

	return allConfigs, nil
}

func (a *MockedOmAdmin) CreateOplogStoreConfig(config backup.DataStoreConfig) error {
	// Note, that backup API doesn't throw an error if the config already exists - it just updates it
	a.oplogConfigs[config.Id] = config
	return nil
}

func (a *MockedOmAdmin) UpdateOplogStoreConfig(config backup.DataStoreConfig) error {
	a.oplogConfigs[config.Id] = config
	// OM backup service doesn't throw any errors if the config is not there
	return nil
}

func (a *MockedOmAdmin) DeleteOplogStoreConfig(id string) error {
	if _, ok := a.oplogConfigs[id]; !ok {
		return errors.New("Failed to remove as the oplog doesn't exist!")
	}
	delete(a.oplogConfigs, id)
	return nil
}

func (a *MockedOmAdmin) ReadS3OplogStoreConfigs() ([]backup.S3Config, error) {
	allConfigs := make([]backup.S3Config, 0)
	for _, v := range a.s3OpLogConfigs {
		allConfigs = append(allConfigs, v)
	}

	return allConfigs, nil
}

func (a *MockedOmAdmin) UpdateS3OplogConfig(s3Config backup.S3Config) error {
	a.s3OpLogConfigs[s3Config.Id] = s3Config
	return nil
}

func (a *MockedOmAdmin) CreateS3OplogStoreConfig(s3Config backup.S3Config) error {
	return a.UpdateS3OplogConfig(s3Config)
}

func (a *MockedOmAdmin) DeleteS3OplogStoreConfig(id string) error {
	if _, ok := a.s3OpLogConfigs[id]; !ok {
		return errors.New("Failed to remove as the s3 oplog doesn't exist!")
	}
	delete(a.s3OpLogConfigs, id)
	return nil
}

func (a *MockedOmAdmin) ReadBlockStoreConfigs() ([]backup.DataStoreConfig, error) {
	allConfigs := make([]backup.DataStoreConfig, 0)
	for _, v := range a.blockStoreConfigs {
		allConfigs = append(allConfigs, v)
	}

	sort.SliceStable(allConfigs, func(i, j int) bool {
		return allConfigs[i].Id < allConfigs[j].Id
	})

	return allConfigs, nil
}

func (a *MockedOmAdmin) CreateBlockStoreConfig(config backup.DataStoreConfig) error {
	a.blockStoreConfigs[config.Id] = config
	return nil
}

func (a *MockedOmAdmin) UpdateBlockStoreConfig(config backup.DataStoreConfig) error {
	a.blockStoreConfigs[config.Id] = config
	// OM backup service doesn't throw any errors if the config is not there
	return nil
}

func (a *MockedOmAdmin) DeleteBlockStoreConfig(id string) error {
	if _, ok := a.blockStoreConfigs[id]; !ok {
		return errors.New("Failed to remove as the block store doesn't exist!")
	}
	delete(a.blockStoreConfigs, id)
	return nil
}

func (a *MockedOmAdmin) ReadFileSystemStoreConfigs() ([]backup.DataStoreConfig, error) {
	allConfigs := make([]backup.DataStoreConfig, len(a.blockStoreConfigs))
	for _, v := range a.fileSystemStoreConfigs {
		allConfigs = append(allConfigs, v)
	}
	return allConfigs, nil
}

func (a *MockedOmAdmin) ReadGlobalAPIKeys() ([]Key, error) {
	return a.apiKeys, nil
}

func (a *MockedOmAdmin) CreateGlobalAPIKey(description string) (Key, error) {
	newKey := Key{
		Description: description,
		Roles:       []map[string]string{{"role_name": "GLOBAL_ONWER"}},
	}
	a.apiKeys = append(a.apiKeys, newKey)
	return newKey, nil
}

func (a *MockedOmAdmin) ReadOpsManagerVersion() (versionutil.OpsManagerVersion, error) {
	return versionutil.OpsManagerVersion{}, nil
}
