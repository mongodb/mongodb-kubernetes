package mongot

type Config struct {
	SyncSource  ConfigSyncSource  `json:"syncSource"`
	Storage     ConfigStorage     `json:"storage"`
	Server      ConfigServer      `json:"server"`
	Metrics     ConfigMetrics     `json:"metrics"`
	HealthCheck ConfigHealthCheck `json:"healthCheck"`
	Logging     ConfigLogging     `json:"logging"`
}

type ConfigSyncSource struct {
	ReplicaSet ConfigReplicaSet `json:"replicaSet"`
}

type ConfigReplicaSet struct {
	HostAndPort    string  `json:"hostAndPort"`
	Username       string  `json:"username"`
	PasswordFile   string  `json:"passwordFile"`
	ReplicaSetName string  `json:"replicaSetName"`
	TLS            *bool   `json:"tls,omitempty"`
	ReadPreference *string `json:"readPreference,omitempty"`
}

type ConfigStorage struct {
	DataPath string `json:"dataPath"`
}

type ConfigServer struct {
	Wireproto *ConfigWireproto `json:"wireproto,omitempty"`
}

type ConfigWireproto struct {
	Address        string                `json:"address"`
	Authentication *ConfigAuthentication `json:"authentication,omitempty"`
	TLS            ConfigTLS             `json:"tls"`
}

type ConfigAuthentication struct {
	Mode    string `json:"mode"`
	KeyFile string `json:"keyFile"`
}

type ConfigTLSMode string

const (
	ConfigTLSModeTLS      ConfigTLSMode = "TLS"
	ConfigTLSModeDisabled ConfigTLSMode = "Disabled"
)

type ConfigTLS struct {
	Mode               ConfigTLSMode `json:"mode"`
	CertificateKeyFile *string       `json:"certificateKeyFile,omitempty"`
}

type ConfigMetrics struct {
	Enabled bool   `json:"enabled"`
	Address string `json:"address"`
}

type ConfigHealthCheck struct {
	Address string `json:"address"`
}

type ConfigLogging struct {
	Verbosity string  `json:"verbosity"`
	LogPath   *string `json:"logPath,omitempty"`
}
