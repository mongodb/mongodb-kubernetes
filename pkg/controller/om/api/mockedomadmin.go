package api

import (
	"errors"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"
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
	BaseURL      string
	User         string
	PublicAPIKey string

	s3Configs     map[string]*backup.S3Config
	daemonConfigs []*backup.DaemonConfig
	oplogConfigs  []*backup.DataStoreConfig
}

// NewMockedAdminProvider is the function creating the admin object. The function returns the existing mocked admin instance
// if it exists - this allows to survive through multiple reconciliations and to keep OM state over them
func NewMockedAdminProvider(baseUrl, user, publicApiKey string) Admin {
	if CurrMockedAdmin == nil {
		CurrMockedAdmin = &MockedOmAdmin{}
	}
	CurrMockedAdmin.BaseURL = baseUrl
	CurrMockedAdmin.User = user
	CurrMockedAdmin.PublicAPIKey = publicApiKey

	return CurrMockedAdmin
}

// NewMockedAdmin creates an empty mocked om admin. This must be called by tests when the Om controller is created to
// make sure the state is cleaned
func NewMockedAdmin() *MockedOmAdmin {
	CurrMockedAdmin = &MockedOmAdmin{}
	return CurrMockedAdmin
}

func (a *MockedOmAdmin) ReadDaemonConfig(hostName, headDbDir string) (*backup.DaemonConfig, error) {
	for _, v := range a.daemonConfigs {
		if v.Machine.HeadRootDirectory == headDbDir && v.Machine.MachineHostName == hostName {
			return v, nil
		}
	}
	return nil, NewErrorWithCode(BackupDaemonConfigNotFound)
}

func (a *MockedOmAdmin) CreateDaemonConfig(hostName, headDbDir string) error {
	config := backup.NewDaemonConfig(hostName, headDbDir)
	// Unfortunately backup API for daemon configs is a bit weird: if headdb dir is not empty - this is an update
	// as (hostname, headdb) is a composite key
	if headDbDir != "" {
		_, err := a.ReadDaemonConfig(hostName, headDbDir)
		if err != nil {
			return NewError(err)
		}
		panic("Updates are not supported!")
	}

	a.daemonConfigs = append(a.daemonConfigs, config)
	return nil
}

func (a *MockedOmAdmin) ReadS3Configs() ([]*backup.S3Config, error) {
	if a.s3Configs == nil {
		a.s3Configs = make(map[string]*backup.S3Config)
	}

	allConfigs := make([]*backup.S3Config, len(a.s3Configs))
	for _, v := range a.s3Configs {
		allConfigs = append(allConfigs, v)
	}

	return allConfigs, nil
}

func (a *MockedOmAdmin) DeleteS3Config(id string) error {
	if a.s3Configs == nil {
		a.s3Configs = make(map[string]*backup.S3Config)
	}
	delete(a.s3Configs, id)
	return nil
}

func (a *MockedOmAdmin) ReadOplogStoreConfigs() ([]*backup.DataStoreConfig, error) {
	return a.oplogConfigs, nil
}

func (a *MockedOmAdmin) CreateOplogStoreConfig(config *backup.DataStoreConfig) error {
	for i, v := range a.oplogConfigs {
		if v.Id == config.Id {
			// Note, that backup API doesn't throw an error if the config already exists - it just updates it
			a.oplogConfigs[i] = config
			return nil
		}
	}
	a.oplogConfigs = append(a.oplogConfigs, config)
	return nil
}

func (a *MockedOmAdmin) UpdateOplogStoreConfig(config *backup.DataStoreConfig) error {
	for i, v := range a.oplogConfigs {
		if v.Id == config.Id {
			a.oplogConfigs[i] = config
			return nil
		}
	}
	// OM backup service doesn't throw any errors if the config is not there
	return nil
}

func (a *MockedOmAdmin) DeleteOplogStoreConfig(id string) error {
	for i, v := range a.oplogConfigs {
		if v.Id == id {
			a.oplogConfigs = append(a.oplogConfigs[:i], a.oplogConfigs[i+1:]...)
			return nil
		}
	}

	return errors.New("Failed to remove as the oplog doesn't exist!")
}

func (a *MockedOmAdmin) CreateS3Config(s3Config *backup.S3Config) error {
	if a.s3Configs == nil {
		a.s3Configs = make(map[string]*backup.S3Config)
	}
	a.s3Configs[s3Config.Id] = s3Config
	return nil
}

func (a *MockedOmAdmin) UpdateS3Config(s3Config *backup.S3Config) error {
	return a.CreateS3Config(s3Config)
}
