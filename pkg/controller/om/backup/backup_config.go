package backup

type Status string

const (
	Inactive    Status = "INACTIVE"
	Started     Status = "STARTED"
	Stopped     Status = "STOPPED"
	Terminating Status = "TERMINATING"
)

type ConfigReader interface {
	// ReadBackupConfigs returns all host clusters registered in OM. If there's no backup enabled the status is supposed
	// to be Inactive
	ReadBackupConfigs() (*ConfigsResponse, error)

	// ReadBackupConfig reads an individual backup config by cluster id
	ReadBackupConfig(clusterID string) (*Config, error)
}

// ConfigUpdater is something can update an existing Backup Config
type ConfigUpdater interface {
	UpdateBackupConfig(config *Config) (*Config, error)
	UpdateBackupStatus(clusterID string, status Status) error
}

type ConfigReadUpdater interface {
	ConfigReader
	ConfigUpdater
}

/*
{
      "authMechanismName": "NONE",
      "clusterId": "5ba4ec37a957713d7f9bcb9a",
      "encryptionEnabled": false,
      "excludedNamespaces": [],
      "groupId": "5ba0c398a957713d7f8653bd",
      "links": [
		...
      ],
      "sslEnabled": false,
      "statusName": "INACTIVE"
    }
*/
type ConfigsResponse struct {
	Configs []*Config `json:"results"`
}

type Config struct {
	ClusterId               string   `json:"clusterId"`
	EncryptionEnabled       bool     `json:"encryptionEnabled"`
	ExcludedNamespaces      []string `json:"excludedNamespaces"`
	IncludedNamespaces      []string `json:"includedNamespaces"`
	Password                string   `json:"password"`
	Provisioned             bool     `json:"provisioned"`
	TlsEnabled              bool     `json:"sslEnabled"`
	Status                  Status   `json:"statusName"`
	StorageEngineName       string   `json:"storageEngineName"`
	SyncSource              string   `json:"syncSource"`
	ProjectId               string   `json:"groupId"`
	AuthenticationMechanism string   `json:"authMechanismName"`
}
