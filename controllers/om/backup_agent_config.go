package om

import (
	"encoding/json"

	"github.com/spf13/cast"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type BackupAgentTemplate struct {
	Username      string                                `json:"username,omitempty"`
	Password      string                                `json:"password,omitempty"`
	SSLPemKeyFile string                                `json:"sslPEMKeyFile,omitempty"`
	LdapGroupDN   string                                `json:"ldapGroupDN,omitempty"`
	LogRotate     mdbv1.LogRotateForBackupAndMonitoring `json:"logRotate,omitempty"`
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

func (bac *BackupAgentConfig) SetLogRotate(logRotate mdbv1.LogRotateForBackupAndMonitoring) {
	bac.BackupAgentTemplate.LogRotate = logRotate
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

func (bac *BackupAgentConfig) EnableX509Authentication(backupAgentSubject, automationAgentPemFilePath string) {
	bac.BackupAgentTemplate.SSLPemKeyFile = automationAgentPemFilePath
	bac.SetAgentUserName(backupAgentSubject)
}

func (bac *BackupAgentConfig) DisableX509Authentication() {
	bac.BackupAgentTemplate.SSLPemKeyFile = util.MergoDelete
	bac.UnsetAgentUsername()
	bac.UnsetAgentPassword()
}

func (bac *BackupAgentConfig) EnableLdapAuthentication(backupAgentSubject string, backupAgentPwd string) {
	bac.SetAgentUserName(backupAgentSubject)
	bac.SetAgentPassword(backupAgentPwd)
}

func (bac *BackupAgentConfig) DisableLdapAuthentication() {
	bac.UnsetAgentUsername()
	bac.UnsetAgentPassword()
	bac.UnsetLdapGroupDN()
}

func (bac *BackupAgentConfig) SetLdapGroupDN(ldapGroupDn string) {
	bac.BackupAgentTemplate.LdapGroupDN = ldapGroupDn
}

func (bac *BackupAgentConfig) UnsetLdapGroupDN() {
	bac.BackupAgentTemplate.LdapGroupDN = util.MergoDelete
}

// ReadLogRotate extracts logRotate from the BackingMap and returns it as a
// typed LogRotateForBackupAndMonitoring. Returns nil if not present or empty.
func (bac *BackupAgentConfig) ReadLogRotate() *mdbv1.LogRotateForBackupAndMonitoring {
	lr, ok := bac.BackingMap["logRotate"].(map[string]any)
	if !ok || len(lr) == 0 {
		return nil
	}
	result := mdbv1.LogRotateForBackupAndMonitoring{
		SizeThresholdMB:  cast.ToInt(lr["sizeThresholdMB"]),
		TimeThresholdHrs: cast.ToInt(lr["timeThresholdHrs"]),
	}
	if result.SizeThresholdMB == 0 && result.TimeThresholdHrs == 0 {
		return nil
	}
	return &result
}

// LogPath returns the logPath from the BackingMap, or empty if absent.
func (bac *BackupAgentConfig) LogPath() string {
	logPath, _ := bac.BackingMap["logPath"].(string)
	return logPath
}

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
