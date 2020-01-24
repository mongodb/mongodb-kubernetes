package om

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type VersionManifest struct {
	Updated  int                    `json:"updated"`
	Versions []MongoDbVersionConfig `json:"versions"`
}

type VersionManifestProvider interface {
	GetVersionManifest() (*VersionManifest, error)
}

type FileVersionManifestProvider struct {
	FilePath string
}

func (p FileVersionManifestProvider) GetVersionManifest() (*VersionManifest, error) {
	data, err := ioutil.ReadFile(p.FilePath)
	if err != nil {
		return nil, err
	}

	versionManifest := &VersionManifest{}
	err = json.Unmarshal(data, &versionManifest)
	if err != nil {
		return nil, err
	}
	fixLinksAndBuildModules(versionManifest.Versions)
	return versionManifest, nil
}

type InternetManifestProvider struct{}

func (InternetManifestProvider) GetVersionManifest() (*VersionManifest, error) {
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

	versionManifest := &VersionManifest{}
	err = json.Unmarshal(body, &versionManifest)
	if err != nil {
		return nil, err
	}
	fixLinksAndBuildModules(versionManifest.Versions)
	return versionManifest, nil
}

// fixLinksAndBuildModules iterates over build links and prefixes them with a correct domain
// (see mms AutomationMongoDbVersionSvc#buildRemoteUrl) and ensures that build.Modules has
// a non-nil value as this will cause the agent to fail cluster validation
func fixLinksAndBuildModules(configs []MongoDbVersionConfig) {
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
