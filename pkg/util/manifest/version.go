package manifest

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/blang/semver"
	"go.uber.org/zap"
)

// Important: 3.6.0 won't work here as semver library will consider 3.6.0-ent as lower than
// 3.6.0 (considers it to be an RC?)!
const MINIMUM_ALLOWED_MDB_VERSION = "3.5.0"

type Manifest struct {
	Updated  int                       `json:"updated"`
	Versions []om.MongoDbVersionConfig `json:"versions"`
}

type Provider interface {
	GetVersion() (*Manifest, error)
}

type FileProvider struct {
	FilePath string
}

func (p FileProvider) GetVersion() (*Manifest, error) {
	data, err := ioutil.ReadFile(p.FilePath)
	if err != nil {
		return nil, err
	}

	return readManifest(data)
}

type InternetProvider struct{}

func (InternetProvider) GetVersion() (*Manifest, error) {
	client, err := api.NewHTTPClient()
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(fmt.Sprintf("https://opsmanager.mongodb.com/static/version_manifest/%s.json", util.LatestOmVersion))
	if err != nil {
		return nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return readManifest(body)
}

// readManifest deserializes and proceses the manifest (filters out legacy versions and fixes links)
func readManifest(data []byte) (*Manifest, error) {
	versionManifest := &Manifest{}
	err := json.Unmarshal(data, &versionManifest)
	if err != nil {
		return nil, err
	}

	versionManifest.Versions = cutLegacyVersions(versionManifest.Versions, MINIMUM_ALLOWED_MDB_VERSION)
	fixLinksAndBuildModules(versionManifest.Versions)
	return versionManifest, nil
}

// cutLegacyVersions filters out the old Mongodb versions from version manifest - otherwise the automation config
// may get too big
func cutLegacyVersions(configs []om.MongoDbVersionConfig, firstAllowedVersion string) []om.MongoDbVersionConfig {
	minimumAllowedVersion, _ := semver.Make(firstAllowedVersion)
	var versions []om.MongoDbVersionConfig

	for _, version := range configs {
		manifestVersion, err := semver.Make(version.Name)
		if err != nil {
			zap.S().Warnf("Failed to parse version from version manifest: %s", err)
		} else if manifestVersion.GE(minimumAllowedVersion) {
			versions = append(versions, version)
		}
	}
	return versions
}

// fixLinksAndBuildModules iterates over build links and prefixes them with a correct domain
// (see mms AutomationMongoDbVersionSvc#buildRemoteUrl) and ensures that build.Modules has
// a non-nil value as this will cause the agent to fail cluster validation
func fixLinksAndBuildModules(configs []om.MongoDbVersionConfig) {
	for _, version := range configs {
		for _, build := range version.Builds {
			if strings.HasSuffix(version.Name, "-ent") {
				build.Url = "https://downloads.mongodb.com" + build.Url
			} else {
				build.Url = "https://fastdl.mongodb.org" + build.Url
			}
			// AA expects not nil element
			if build.Modules == nil {
				build.Modules = []string{}
			}
		}
	}
}
