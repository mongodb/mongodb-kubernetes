package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
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
	LicenseState           any
	LicenseReadState       any
	InstalledLicense       json.RawMessage
	ReleasedIDs            []string
	LicenseReceipt         string
	IdentityRotationErr    error
	IdentityRotationCalls  int
	LicenseIdentity        IdentityView
	LicenseIdentityCalls   int
	LicenseReadErr         error
	LicenseInstallErr      error
	LicenseRecomputeErr    error
	LicenseReceiptErr      error
	LicenseIdentityErr     error
	LicenseReceiptCalls    int
	LicenseRecomputeCalls  int
}

func (a *MockedOmAdmin) UpdateDaemonConfig(ctx context.Context, config backup.DaemonConfig) error {
	for _, dc := range a.daemonConfigs {
		if dc.Machine.MachineHostName == config.Machine.HeadRootDirectory {
			dc.AssignmentEnabled = config.AssignmentEnabled
			return nil
		}
	}
	return apierror.NewErrorWithCode(apierror.BackupDaemonConfigNotFound)
}

// NewMockedAdminProvider creates the admin object
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

func (a *MockedOmAdmin) ReadDaemonConfig(ctx context.Context, hostName, headDbDir string) (backup.DaemonConfig, error) {
	for _, v := range a.daemonConfigs {
		if v.Machine.HeadRootDirectory == headDbDir && v.Machine.MachineHostName == hostName {
			return v, nil
		}
	}
	return backup.DaemonConfig{}, apierror.NewErrorWithCode(apierror.BackupDaemonConfigNotFound)
}

func (a *MockedOmAdmin) CreateDaemonConfig(ctx context.Context, hostName, headDbDir string, assignmentLabels []string) error {
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

func (a *MockedOmAdmin) ReadS3Configs(ctx context.Context) ([]backup.S3Config, error) {
	allConfigs := make([]backup.S3Config, 0)
	for _, v := range a.s3Configs {
		allConfigs = append(allConfigs, v)
	}

	sort.SliceStable(allConfigs, func(i, j int) bool {
		return allConfigs[i].Id < allConfigs[j].Id
	})

	return allConfigs, nil
}

func (a *MockedOmAdmin) DeleteS3Config(ctx context.Context, id string) error {
	if _, ok := a.s3Configs[id]; !ok {
		return errors.New("failed to remove as the s3 config doesn't exist")
	}
	delete(a.s3Configs, id)
	return nil
}

func (a *MockedOmAdmin) CreateS3Config(ctx context.Context, s3Config backup.S3Config) error {
	a.s3Configs[s3Config.Id] = s3Config
	return nil
}

func (a *MockedOmAdmin) UpdateS3Config(ctx context.Context, s3Config backup.S3Config) error {
	return a.CreateS3Config(ctx, s3Config)
}

func (a *MockedOmAdmin) ReadOplogStoreConfigs(ctx context.Context) ([]backup.DataStoreConfig, error) {
	allConfigs := make([]backup.DataStoreConfig, 0)
	for _, v := range a.oplogConfigs {
		allConfigs = append(allConfigs, v)
	}

	sort.SliceStable(allConfigs, func(i, j int) bool {
		return allConfigs[i].Id < allConfigs[j].Id
	})

	return allConfigs, nil
}

func (a *MockedOmAdmin) CreateOplogStoreConfig(ctx context.Context, config backup.DataStoreConfig) error {
	// Note, that backup API doesn't throw an error if the config already exists - it just updates it
	a.oplogConfigs[config.Id] = config
	return nil
}

func (a *MockedOmAdmin) UpdateOplogStoreConfig(ctx context.Context, config backup.DataStoreConfig) error {
	a.oplogConfigs[config.Id] = config
	// OM backup service doesn't throw any errors if the config is not there
	return nil
}

func (a *MockedOmAdmin) DeleteOplogStoreConfig(ctx context.Context, id string) error {
	if _, ok := a.oplogConfigs[id]; !ok {
		return errors.New("failed to remove as the oplog doesn't exist")
	}
	delete(a.oplogConfigs, id)
	return nil
}

func (a *MockedOmAdmin) ReadS3OplogStoreConfigs(ctx context.Context) ([]backup.S3Config, error) {
	allConfigs := make([]backup.S3Config, 0)
	for _, v := range a.s3OpLogConfigs {
		allConfigs = append(allConfigs, v)
	}

	return allConfigs, nil
}

func (a *MockedOmAdmin) UpdateS3OplogConfig(ctx context.Context, s3Config backup.S3Config) error {
	a.s3OpLogConfigs[s3Config.Id] = s3Config
	return nil
}

func (a *MockedOmAdmin) CreateS3OplogStoreConfig(ctx context.Context, s3Config backup.S3Config) error {
	return a.UpdateS3OplogConfig(ctx, s3Config)
}

func (a *MockedOmAdmin) DeleteS3OplogStoreConfig(ctx context.Context, id string) error {
	if _, ok := a.s3OpLogConfigs[id]; !ok {
		return errors.New("failed to remove as the s3 oplog doesn't exist")
	}
	delete(a.s3OpLogConfigs, id)
	return nil
}

func (a *MockedOmAdmin) ReadBlockStoreConfigs(ctx context.Context) ([]backup.DataStoreConfig, error) {
	allConfigs := make([]backup.DataStoreConfig, 0)
	for _, v := range a.blockStoreConfigs {
		allConfigs = append(allConfigs, v)
	}

	sort.SliceStable(allConfigs, func(i, j int) bool {
		return allConfigs[i].Id < allConfigs[j].Id
	})

	return allConfigs, nil
}

func (a *MockedOmAdmin) CreateBlockStoreConfig(ctx context.Context, config backup.DataStoreConfig) error {
	a.blockStoreConfigs[config.Id] = config
	return nil
}

func (a *MockedOmAdmin) UpdateBlockStoreConfig(ctx context.Context, config backup.DataStoreConfig) error {
	a.blockStoreConfigs[config.Id] = config
	// OM backup service doesn't throw any errors if the config is not there
	return nil
}

func (a *MockedOmAdmin) DeleteBlockStoreConfig(ctx context.Context, id string) error {
	if _, ok := a.blockStoreConfigs[id]; !ok {
		return errors.New("failed to remove as the block store doesn't exist")
	}
	delete(a.blockStoreConfigs, id)
	return nil
}

func (a *MockedOmAdmin) ReadFileSystemStoreConfigs(ctx context.Context) ([]backup.DataStoreConfig, error) {
	allConfigs := make([]backup.DataStoreConfig, 0, len(a.fileSystemStoreConfigs))
	for _, v := range a.fileSystemStoreConfigs {
		allConfigs = append(allConfigs, v)
	}
	return allConfigs, nil
}

func (a *MockedOmAdmin) ReadGlobalAPIKeys(ctx context.Context) ([]Key, error) {
	return a.apiKeys, nil
}

func (a *MockedOmAdmin) CreateGlobalAPIKey(ctx context.Context, description string) (Key, error) {
	newKey := Key{
		Description: description,
		Roles:       []map[string]string{{"role_name": "GLOBAL_OWNER"}},
	}
	a.apiKeys = append(a.apiKeys, newKey)
	return newKey, nil
}

func (a *MockedOmAdmin) ReadOpsManagerVersion(ctx context.Context) (versionutil.OpsManagerVersion, error) {
	return versionutil.OpsManagerVersion{}, nil
}

func (a *MockedOmAdmin) UpdateAgentVersion(version string) {
	a.agentVersion = version
}

func (a *MockedOmAdmin) ReadAgentVersion() (string, error) {
	return a.agentVersion, nil
}

func (a *MockedOmAdmin) ReadLicenseState(_ context.Context) (omv1.OpsManagerLicenseStatus, error) {
	if a.LicenseReadErr != nil {
		return omv1.OpsManagerLicenseStatus{}, a.LicenseReadErr
	}
	if a.LicenseReadState != nil {
		return unmarshalMockLicenseState(a.LicenseReadState)
	}
	return unmarshalMockLicenseState(a.LicenseState)
}

func (a *MockedOmAdmin) InstallLicense(_ context.Context, rawArtifact json.RawMessage) (omv1.OpsManagerLicenseStatus, error) {
	if a.LicenseInstallErr != nil {
		return omv1.OpsManagerLicenseStatus{}, a.LicenseInstallErr
	}
	a.InstalledLicense = rawArtifact
	if artifactID, err := ArtifactIDFromRaw(string(rawArtifact)); err == nil {
		state, err := unmarshalMockLicenseState(a.LicenseState)
		if err != nil {
			return omv1.OpsManagerLicenseStatus{}, err
		}
		if !state.ListsArtifact(artifactID) {
			state.Licenses = append(state.Licenses, omv1.OpsManagerLicenseArtifactStatus{
				ArtifactID: artifactID,
				Usable:     true,
				Status:     "ACTIVE",
			})
		}
		for i, license := range state.Licenses {
			if license.ArtifactID != artifactID && license.Status != omv1.LicenseArtifactStatusReleased {
				state.Licenses[i].Status = omv1.LicenseArtifactStatusReleased
				state.Licenses[i].Usable = false
				state.Licenses[i].TermEnd = ""
			}
		}
		a.LicenseState = state
	}
	// After a PUT, reads see the state OM returned for it.
	a.LicenseReadState = nil
	return unmarshalMockLicenseState(a.LicenseState)
}

func (a *MockedOmAdmin) RecomputeLicense(_ context.Context) error {
	a.LicenseRecomputeCalls++
	return a.LicenseRecomputeErr
}

func (a *MockedOmAdmin) ReleaseLicense(_ context.Context, artifactID string) (omv1.OpsManagerLicenseStatus, error) {
	held, err := unmarshalMockLicenseState(a.LicenseState)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	if !held.ListsArtifact(artifactID) {
		notFound := 404
		return omv1.OpsManagerLicenseStatus{}, &apierror.Error{
			Status: &notFound,
			Detail: fmt.Sprintf("Ops Manager does not hold license %s", artifactID),
		}
	}
	a.ReleasedIDs = append(a.ReleasedIDs, artifactID)
	raw, err := marshalMockLicenseState(a.LicenseState)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	if licenses, ok := state["licenses"].([]any); ok {
		for i, license := range licenses {
			licenseMap, ok := license.(map[string]any)
			if !ok || licenseMap["artifactId"] != artifactID {
				continue
			}
			licenseMap["status"] = omv1.LicenseArtifactStatusReleased
			licenseMap["usable"] = false
			licenseMap["termEnd"] = nil
			licenses[i] = licenseMap
		}
		state["licenses"] = licenses
	}
	raw, err = json.Marshal(state)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	a.LicenseState = raw
	var response omv1.OpsManagerLicenseStatus
	if err := json.Unmarshal(raw, &response); err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	return response, nil
}

func marshalMockLicenseState(state any) (json.RawMessage, error) {
	if state == nil {
		return json.RawMessage(`{}`), nil
	}
	if raw, ok := state.(json.RawMessage); ok {
		return append(json.RawMessage(nil), raw...), nil
	}
	return json.Marshal(state)
}

func unmarshalMockLicenseState(state any) (omv1.OpsManagerLicenseStatus, error) {
	raw, err := marshalMockLicenseState(state)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	var response omv1.OpsManagerLicenseStatus
	if err := json.Unmarshal(raw, &response); err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	return response, nil
}

func (a *MockedOmAdmin) ReadLicenseReceipt(_ context.Context, artifactID string) (string, error) {
	a.LicenseReceiptCalls++
	if a.LicenseReceiptErr != nil {
		return "", a.LicenseReceiptErr
	}
	if a.LicenseReceipt == "" {
		return `{"artifactId":"` + artifactID + `"}`, nil
	}
	return a.LicenseReceipt, nil
}

func (a *MockedOmAdmin) ReadLicenseIdentity(_ context.Context) (IdentityView, error) {
	a.LicenseIdentityCalls++
	if a.LicenseIdentityErr != nil {
		return IdentityView{}, a.LicenseIdentityErr
	}
	return a.LicenseIdentity, nil
}

func (a *MockedOmAdmin) RotateLicenseIdentity(_ context.Context) error {
	a.IdentityRotationCalls++
	return a.IdentityRotationErr
}
