package om

import (
	"encoding/json"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type BackupAgentTemplate struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
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

func (bac *BackupAgentConfig) SetAgentUserName(backupAgentSubject string) {
	bac.BackupAgentTemplate.Username = backupAgentSubject
}

func (bac *BackupAgentConfig) UnsetAgentUsername() {
	bac.BackupAgentTemplate.Username = util.MergoDelete
}

func (bac *BackupAgentConfig) SetAgentPassword(pwd string) {
	bac.BackupAgentTemplate.Password = pwd
}

func (bac *BackupAgentConfig) UnsetAgentPassword() {
	bac.BackupAgentTemplate.Password = util.MergoDelete
}

func (bac *BackupAgentConfig) EnableX509Authentication(backupAgentSubject string) {
	bac.BackupAgentTemplate.SSLPemKeyFile = util.BackupAgentPemFilePath
	bac.SetAgentUserName(backupAgentSubject)
}

func (bac *BackupAgentConfig) DisableX509Authentication() {
	bac.BackupAgentTemplate.SSLPemKeyFile = util.MergoDelete
	bac.UnsetAgentUsername()
}

func (bac *BackupAgentConfig) EnableLdapAuthentication(backupAgentSubject string, backupAgentPwd string) {
	bac.SetAgentUserName(backupAgentSubject)
	bac.SetAgentPassword(backupAgentPwd)
}

func (bac *BackupAgentConfig) DisableLdapAuthentication() {
	bac.UnsetAgentUsername()
	bac.UnsetAgentPassword()
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
