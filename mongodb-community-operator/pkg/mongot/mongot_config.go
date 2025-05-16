package mongot

type Config struct {
	CommunityPrivatePreview CommunityPrivatePreview `json:"communityPrivatePreview"`
}

// CommunityPrivatePreview structure reflects private preview configuration from mongot:
// https://github.com/10gen/mongot/blob/060ec179af062ac2639678f4a613b8ab02c21597/src/main/java/com/xgen/mongot/config/provider/community/CommunityConfig.java#L100
// Comments are from the default config file: https://github.com/10gen/mongot/blob/375379e56a580916695a2f53e12fd4a99aa24f0b/deploy/community-resources/config.default.yml#L1-L0
type CommunityPrivatePreview struct {
	// Socket (IPv4/6) address of the sync source mongod
	MongodHostAndPort string `json:"mongodHostAndPort"`

	// Socket (IPv4/6) address on which to listen for wire protocol connections
	QueryServerAddress string `json:"queryServerAddress"`

	// Keyfile used for mongod -> mongot authentication
	KeyFilePath string `json:"keyFilePath"`

	// Filesystem path that all mongot data will be stored at
	DataPath string `json:"dataPath"`

	// Options for metrics
	Metrics Metrics `json:"metrics,omitempty"`

	// Options for logging
	Logging Logging `json:"logging,omitempty"`
}

type Metrics struct {
	// Whether to enable the Prometheus metrics endpoint
	Enabled bool `json:"enabled"`

	// Socket address (IPv4/6) on which the Prometheus /metrics endpoint will be exposed
	Address string `json:"address"`
}

type Logging struct {
	// Log level
	Verbosity string `json:"verbosity"`
}
