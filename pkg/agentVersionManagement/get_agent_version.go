package agentVersionManagement

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/blang/semver"
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

const (
	MappingFilePathEnv     = "MDB_OM_VERSION_MAPPING_PATH"
	mappingFileDefaultPath = "/usr/local/om_version_mapping.json"
)

var om6StaticContainersSupport = semver.Version{
	Major: 6,
	Minor: 0,
	Patch: 21,
}

var om7StaticContainersSupport = semver.Version{
	Major: 7,
	Minor: 0,
	Patch: 0,
}

var initializationMutex sync.Mutex

// AgentVersionManager handles the retrieval of agent versions.
// See https://docs.google.com/document/d/1rsj_Ng3IRGv74y1yMTiztfc0LEpBVizjGWFQTaYBz60/edit#heading=h.9frez6xnhit0
type AgentVersionManager struct {
	// This structure contains a map from semantic versions to semantic versions (string)
	omToAgentVersionMapping map[omv1.OpsManagerVersion]omv1.AgentVersion

	// Major version to latest OM version
	latestOMVersionsByMajor map[string]string

	// Agent version for Cloud Manager
	agentVersionCM string
}

var (
	versionManager      *AgentVersionManager
	lastUsedMappingPath string
)

func newAgentVersionManager(omVersionToAgentVersion map[omv1.OpsManagerVersion]omv1.AgentVersion, cmVersion string) *AgentVersionManager {
	omVersionsByMajor := make(map[string]string)

	if cmVersion == "" {
		zap.S().Warnf("No version provided for Cloud Manager agent")
	}

	for omVersion := range omVersionToAgentVersion {
		majorOmVersion := getMajorVersion(string(omVersion))

		if currentVersion, exists := omVersionsByMajor[majorOmVersion]; !exists || isLaterVersion(string(omVersion), currentVersion) {
			omVersionsByMajor[majorOmVersion] = string(omVersion)
		}
	}

	return &AgentVersionManager{
		omToAgentVersionMapping: omVersionToAgentVersion,
		latestOMVersionsByMajor: omVersionsByMajor,
		agentVersionCM:          cmVersion,
	}
}

// isLaterVersion compares two semantic versions and returns true if the first is later than the second
func isLaterVersion(version1, version2 string) bool {
	splitVersion := func(version string) []int {
		var parts []int
		for _, part := range strings.Split(strings.Split(version, "-")[0], ".") {
			if num, err := strconv.Atoi(part); err == nil {
				parts = append(parts, num)
			}
		}
		return parts
	}

	v1Parts := splitVersion(version1)
	v2Parts := splitVersion(version2)

	for i := 0; i < len(v1Parts) && i < len(v2Parts); i++ {
		if v1Parts[i] != v2Parts[i] {
			return v1Parts[i] > v2Parts[i]
		}
	}
	return len(v1Parts) > len(v2Parts)
}

func InitializeAgentVersionManager(mappingFilePath string) (*AgentVersionManager, error) {
	m, cmVersion, err := readReleaseFile(mappingFilePath)
	if err != nil {
		return nil, err
	}
	return newAgentVersionManager(m, cmVersion), nil
}

// GetAgentVersionManager returns the an instance of AgentVersionManager.
func GetAgentVersionManager() (*AgentVersionManager, error) {
	initializationMutex.Lock()
	defer initializationMutex.Unlock()
	mappingFilePath := env.ReadOrDefault(MappingFilePathEnv, mappingFileDefaultPath) // nolint:forbidigo
	if lastUsedMappingPath != mappingFilePath {
		lastUsedMappingPath = mappingFilePath
		var err error

		if versionManager, err = InitializeAgentVersionManager(mappingFilePath); err != nil {
			return nil, err
		}
	}

	return versionManager, nil
}

/*
The agent has the following mapping and support:
OM7 -> 107.0.x (meaning OM7 supports any agent version with major=107)
OM8 -> 108.0.x
OM6 would always use 12.0.x
CM -> Major.Minor can change in between updates and therefore this needs more special handling.
Unlike OM, there is no full guarantee that minor versions support each other for the same Major version.
*/

// GetAgentVersion returns the agent version to use with the Ops Manager
// readFromMapping is true in the case of AppDB, because they are started before OM, so we cannot rely on the endpoint
func (m *AgentVersionManager) GetAgentVersion(conn om.Connection, omVersion string, readFromMapping bool) (string, error) {
	isCM := versionutil.OpsManagerVersion{VersionString: omVersion}.IsCloudManager()
	if isCM {
		return m.getAgentVersionForCloudManagerFromMapping()
	}

	if readFromMapping {
		return m.getClosestAgentVersionForOM(omVersion)
	}

	supportsStatic, err := m.supportsStaticContainers(omVersion)
	if err != nil {
		return "", err
	}

	if !supportsStatic {
		return "", xerrors.Errorf("Ops Manager version %s does not support static containers, please use Ops Manager version of at least %s or %s", omVersion, om6StaticContainersSupport, om7StaticContainersSupport)
	}

	version, err := m.getAgentVersionFromOpsManager(conn)
	if err != nil {
		return "", err
	}
	return addVersionSuffixIfAbsent(version), nil
}

// supportsStaticContainers verifies whether the supplied omVersion supports static containers.
// Agent changes are not backported into older releases.
// Hence, we need to make sure that we run at least a specific agent version.
// The safest way for that is to make sure that the customer uses a specific omVersion, since that is tied to an
// agent version.
func (m *AgentVersionManager) supportsStaticContainers(omVersion string) (bool, error) {
	omVersionConverted := versionutil.OpsManagerVersion{VersionString: omVersion}
	givenVersion, err := omVersionConverted.Semver()
	if err != nil {
		return false, err
	}
	return givenVersion.GTE(om6StaticContainersSupport) || givenVersion.GTE(om7StaticContainersSupport), nil
}

// ReleaseFile and following structs are used to unmarshall release.json fields
type ReleaseFile struct {
	SupportedImages SupportedImages `json:"supportedImages"`
}

type SupportedImages struct {
	MongoDBAgent MongoDBAgent `json:"mongodb-agent"`
}

type MongoDBAgent struct {
	OpsManagerMapping omv1.OpsManagerVersionMapping `json:"opsManagerMapping"`
}

// readReleaseFile reads the version mapping from the release.json file
func readReleaseFile(filePath string) (map[omv1.OpsManagerVersion]omv1.AgentVersion, string, error) {
	var releaseFileContent ReleaseFile

	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "", xerrors.Errorf("failed reading file %s: %w", filePath, err)
	}

	if err = json.Unmarshal(fileBytes, &releaseFileContent); err != nil {
		return nil, "", xerrors.Errorf("failed unmarshalling bytes from file %s: %w", filePath, err)
	}

	mapping := releaseFileContent.SupportedImages.MongoDBAgent.OpsManagerMapping

	return mapping.OpsManager, mapping.CloudManager, nil
}

func getMajorVersion(version string) string {
	if version == "" {
		return ""
	}
	return strings.Split(version, ".")[0]
}

// getAgentVersionFromOpsManager retrieves the agent version by querying Ops Manager API.
func (m *AgentVersionManager) getAgentVersionFromOpsManager(conn om.Connection) (string, error) {
	agentResponse, err := conn.ReadAgentVersion()
	if err != nil {
		return "", err
	}
	return agentResponse.AutomationVersion, nil
}

func addVersionSuffixIfAbsent(version string) string {
	if version == "" {
		return ""
	}
	if strings.HasSuffix(version, "-1") {
		return version
	}
	return version + "-1"
}

func (m *AgentVersionManager) getAgentVersionForCloudManagerFromMapping() (string, error) {
	if m.agentVersionCM != "" {
		return m.agentVersionCM, nil
	}
	return "", xerrors.Errorf("No agent version found for Cloud Manager")
}

func (m *AgentVersionManager) getClosestAgentVersionForOM(omVersion string) (string, error) {
	if version, exists := m.omToAgentVersionMapping[omv1.OpsManagerVersion(omVersion)]; exists {
		return version.AgentVersion, nil
	}
	majorOmVersion := getMajorVersion(omVersion)
	latestAvailableOmVersion := m.latestOMVersionsByMajor[majorOmVersion]
	latestAgentVersion := m.omToAgentVersionMapping[omv1.OpsManagerVersion(latestAvailableOmVersion)] // TODO: return smallest one for monitoring agent not automation agent
	return addVersionSuffixIfAbsent(latestAgentVersion.AgentVersion), nil
}
