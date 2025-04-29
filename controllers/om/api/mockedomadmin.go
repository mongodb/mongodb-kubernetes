package api

import (
	"errors"
	"fmt"
	"sort"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

// ********************************************************************************************************************
// This is a mock for om admin. It's created as a normal (not a test) go file to allow different packages use it for
// testing.
// Surely don't use it in production :)
// ********************************************************************************************************************

// CurrMockedAdmin is a global variable for current OM admin object that was created by MongoDbOpsManager - just for tests
// It's important to clear the state on time - the lifespan of om admin is supposed to be bound to a lifespan of an
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
	agentVersion           string
}

func (a *MockedOmAdmin) UpdateDaemonConfig(config backup.DaemonConfig) error {
	for _, dc := range a.daemonConfigs {
		if dc.Machine.MachineHostName == config.Machine.HeadRootDirectory {
			dc.AssignmentEnabled = config.AssignmentEnabled
			return nil
		}
	}
	return apierror.NewErrorWithCode(apierror.BackupDaemonConfigNotFound)
}

// NewMockedAdminProvider is the function creating the admin object. The function returns the existing mocked admin instance
// if it exists - this allows to survive through multiple reconciliations and to keep OM state over them
func NewMockedAdminProvider(baseUrl, publicApiKey, privateApiKey string, withSingleton bool) OpsManagerAdmin {
	mockedAdmin := &MockedOmAdmin{}
	mockedAdmin.BaseURL = baseUrl
	mockedAdmin.PublicKey = publicApiKey
	mockedAdmin.PrivateKey = privateApiKey

	mockedAdmin.daemonConfigs = make([]backup.DaemonConfig, 0)
	mockedAdmin.s3Configs = make(map[string]backup.S3Config)
	mockedAdmin.s3OpLogConfigs = make(map[string]backup.S3Config)
	mockedAdmin.oplogConfigs = make(map[string]backup.DataStoreConfig)
	mockedAdmin.blockStoreConfigs = make(map[string]backup.DataStoreConfig)
	mockedAdmin.apiKeys = []Key{{
		PrivateKey: privateApiKey,
		PublicKey:  publicApiKey,
	}}

	if withSingleton {
		CurrMockedAdmin = mockedAdmin
	}

	return mockedAdmin
}

func (a *MockedOmAdmin) Reset() {
	NewMockedAdminProvider(a.BaseURL, a.PublicKey, a.PrivateKey, true)
}

func (a *MockedOmAdmin) ReadDaemonConfigs() ([]backup.DaemonConfig, error) {
	return a.daemonConfigs, nil
}

func (a *MockedOmAdmin) ReadDaemonConfig(hostName, headDbDir string) (backup.DaemonConfig, error) {
	for _, v := range a.daemonConfigs {
		if v.Machine.HeadRootDirectory == headDbDir && v.Machine.MachineHostName == hostName {
			return v, nil
		}
	}
	return backup.DaemonConfig{}, apierror.NewErrorWithCode(apierror.BackupDaemonConfigNotFound)
}

func (a *MockedOmAdmin) CreateDaemonConfig(hostName, headDbDir string, assignmentLabels []string) error {
	config := backup.NewDaemonConfig(hostName, headDbDir, assignmentLabels)

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
		return errors.New("failed to remove as the s3 config doesn't exist")
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
		return errors.New("failed to remove as the oplog doesn't exist")
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
		return errors.New("failed to remove as the s3 oplog doesn't exist")
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
		return errors.New("failed to remove as the block store doesn't exist")
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

func (a *MockedOmAdmin) UpdateAgentVersion(version string) {
	a.agentVersion = version
}

func (a *MockedOmAdmin) ReadAgentVersion() (string, error) {
	return a.agentVersion, nil
}
