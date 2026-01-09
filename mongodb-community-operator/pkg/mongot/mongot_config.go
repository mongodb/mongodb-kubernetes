package mongot

type Modification func(*Config)

func NOOP() Modification {
	return func(config *Config) {}
}

func Apply(modifications ...Modification) func(*Config) {
	return func(config *Config) {
		for _, mod := range modifications {
			mod(config)
		}
	}
}

type Config struct {
	SyncSource  ConfigSyncSource  `json:"syncSource"`
	Storage     ConfigStorage     `json:"storage"`
	Server      ConfigServer      `json:"server"`
	Metrics     ConfigMetrics     `json:"metrics"`
	HealthCheck ConfigHealthCheck `json:"healthCheck"`
	Logging     ConfigLogging     `json:"logging"`
	Embedding   *EmbeddingConfig  `json:"embedding,omitempty"`
}

type EmbeddingConfig struct {
	QueryKeyFile              string `json:"queryKeyFile" yaml:"queryKeyFile,omitempty"`
	IndexingKeyFile           string `json:"indexingKeyFile" yaml:"indexingKeyFile,omitempty"`
	ProviderEndpoint          string `json:"providerEndpoint" yaml:"providerEndpoint,omitempty"`
	IsAutoEmbeddingViewWriter *bool  `json:"isAutoEmbeddingViewWriter" yaml:"isAutoEmbeddingViewWriter,omitempty"`
}

type ConfigSyncSource struct {
	ReplicaSet               ConfigReplicaSet `json:"replicaSet"`
	CertificateAuthorityFile *string          `json:"caFile,omitempty"`
}

type ConfigReplicaSet struct {
	HostAndPort    []string `json:"hostAndPort"`
	Username       string   `json:"username"`
	PasswordFile   string   `json:"passwordFile"`
	TLS            *bool    `json:"tls,omitempty"`
	ReadPreference *string  `json:"readPreference,omitempty"`
	AuthSource     *string  `json:"authSource,omitempty"`
}

type ConfigStorage struct {
	DataPath string `json:"dataPath"`
}

type ConfigServer struct {
	Wireproto *ConfigWireproto `json:"wireproto,omitempty"`
	Grpc      *ConfigGrpc      `json:"grpc,omitempty"`
}

type ConfigWireproto struct {
	Address        string                `json:"address"`
	Authentication *ConfigAuthentication `json:"authentication,omitempty"`
	TLS            *ConfigWireprotoTLS   `json:"tls,omitempty"`
}

type ConfigGrpc struct {
	Address string         `json:"address"`
	TLS     *ConfigGrpcTLS `json:"tls,omitempty"`
}

type ConfigAuthentication struct {
	Mode    string `json:"mode"`
	KeyFile string `json:"keyFile"`
}

type ConfigTLSMode string

const (
	ConfigTLSModeTLS      ConfigTLSMode = "TLS"
	ConfigTLSModeMTLS     ConfigTLSMode = "mTLS"
	ConfigTLSModeDisabled ConfigTLSMode = "Disabled"
)

type ConfigWireprotoTLS struct {
	Mode               ConfigTLSMode `json:"mode"`
	CertificateKeyFile *string       `json:"certificateKeyFile,omitempty"`
}

type ConfigGrpcTLS struct {
	Mode                     ConfigTLSMode `json:"mode"`
	CertificateKeyFile       *string       `json:"certificateKeyFile,omitempty"`
	CertificateAuthorityFile *string       `json:"caFile,omitempty"`
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
