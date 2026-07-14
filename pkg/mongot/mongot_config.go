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
	SyncSource   ConfigSyncSource    `json:"syncSource"`
	Storage      ConfigStorage       `json:"storage"`
	Server       ConfigServer        `json:"server"`
	Metrics      ConfigMetrics       `json:"metrics"`
	HealthCheck  ConfigHealthCheck   `json:"healthCheck"`
	Logging      ConfigLogging       `json:"logging"`
	Embedding    *EmbeddingConfig    `json:"embedding,omitempty"`
	FeatureFlags *ConfigFeatureFlags `json:"featureflags,omitempty"`
}

type ConfigFeatureFlags struct {
	OverloadRetrySignal *bool `json:"overloadRetrySignal,omitempty"`
}

type EmbeddingConfig struct {
	QueryKeyFile              string `json:"queryKeyFile" yaml:"queryKeyFile,omitempty"`
	IndexingKeyFile           string `json:"indexingKeyFile" yaml:"indexingKeyFile,omitempty"`
	ProviderEndpoint          string `json:"providerEndpoint" yaml:"providerEndpoint,omitempty"`
	IsAutoEmbeddingViewWriter *bool  `json:"isAutoEmbeddingViewWriter" yaml:"isAutoEmbeddingViewWriter,omitempty"`
}

type ConfigSyncSource struct {
	ReplicaSet        ConfigReplicaSet         `json:"replicaSet"`
	Router            *ConfigRouter            `json:"router,omitempty"`
	ReplicationReader *ConfigReplicationReader `json:"replicationReader,omitempty"`
}

type ConfigReplicationReader struct {
	ReadPreference *string       `json:"readPreference,omitempty"`
	TagSets        [][]ConfigTag `json:"tagSets,omitempty"`
}

type ConfigTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ConfigRouter struct {
	HostAndPort []string         `json:"hostAndPort"`
	X509        *ConfigX509      `json:"x509,omitempty"`
	ScramAuth   *ConfigScramAuth `json:"scramAuth,omitempty"`
}

type ConfigReplicaSet struct {
	HostAndPort []string         `json:"hostAndPort"`
	X509        *ConfigX509      `json:"x509,omitempty"`
	ScramAuth   *ConfigScramAuth `json:"scramAuth,omitempty"`
}

type ConfigScramAuth struct {
	Username     string        `json:"username"`
	PasswordFile string        `json:"passwordFile"`
	TLS          *ScramAuthTLS `json:"tls,omitempty"`
	AuthSource   *string       `json:"authSource,omitempty"`
}

type ScramAuthTLS struct {
	Enabled                           bool    `json:"enabled"`
	TLSCertificateKeyFile             *string `json:"tlsCertificateKeyFile,omitempty"`
	TLSCertificateKeyFilePasswordFile *string `json:"tlsCertificateKeyFilePasswordFile,omitempty"`
	CertificateAuthorityFile          *string `json:"caFile,omitempty"`
}

type ConfigX509 struct {
	CertificateAuthorityFile          *string `json:"caFile,omitempty"`
	TLSCertificateKeyFile             *string `json:"tlsCertificateKeyFile,omitempty"`
	TLSCertificateKeyFilePasswordFile *string `json:"tlsCertificateKeyFilePasswordFile,omitempty"`
}

type ConfigStorage struct {
	DataPath string `json:"dataPath"`
}

type ConfigServer struct {
	Name      string           `json:"name,omitempty"`
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
	Mode                           ConfigTLSMode `json:"mode"`
	CertificateKeyFile             *string       `json:"certificateKeyFile,omitempty"`
	CertificateKeyFilePasswordFile *string       `json:"certificateKeyFilePasswordFile,omitempty"`
	CertificateAuthorityFile       *string       `json:"caFile,omitempty"`
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
