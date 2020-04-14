package om

import (
	"encoding/json"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type BackupAgentTemplate struct {
	Username      string `json:"username"`
	SSLPemKeyFile string `json:"sslPEMKeyFile"`
}

type BackupAgentConfig struct {
	BackupAgentTemplate *BackupAgentTemplate
	BackingMap          map[string]interface{}
}

func (bac *BackupAgentConfig) Apply() error {
	merged, err := util.MergeWith(bac.BackupAgentTemplate, bac.BackingMap, &util.AutomationConfigTransformer{})
	if err != nil {
		return err
	}
	bac.BackingMap = merged
	return nil
}

func (bac *BackupAgentConfig) EnableX509Authentication(backupAgentSubject string) {
	bac.BackupAgentTemplate.SSLPemKeyFile = util.BackupAgentPemFilePath
	bac.BackupAgentTemplate.Username = backupAgentSubject
}

func (bac *BackupAgentConfig) DisableX509Authentication() {
	bac.BackupAgentTemplate.SSLPemKeyFile = util.MergoDelete
	bac.BackupAgentTemplate.Username = util.MergoDelete
}

// BuildBackupAgentConfigFromBytes
func BuildBackupAgentConfigFromBytes(jsonBytes []byte) (*BackupAgentConfig, error) {
	fullMap := make(map[string]interface{})
	if err := json.Unmarshal(jsonBytes, &fullMap); err != nil {
		return nil, err
	}

	config := &BackupAgentConfig{BackingMap: fullMap}
	template := &BackupAgentTemplate{}
	if username, ok := fullMap["username"].(string); ok {
		template.Username = username
	}

	if sslPemKeyfile, ok := fullMap["sslPEMKeyFile"].(string); ok {
		template.SSLPemKeyFile = sslPemKeyfile
	}

	config.BackupAgentTemplate = template

	return config, nil
}
