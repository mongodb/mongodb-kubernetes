package om

// MaintainedMonarchComponents is the automation config section that tells the agent
// about the Monarch injector configuration for a standby cluster.
type MaintainedMonarchComponents struct {
	ReplicaSetID       string         `json:"replicaSetId"`
	ClusterPrefix      string         `json:"clusterPrefix"`
	AWSBucketName      string         `json:"awsBucketName"`
	AWSRegion          string         `json:"awsRegion"`
	AWSAccessKeyID     string         `json:"awsAccessKeyId"`
	AWSSecretAccessKey string         `json:"awsSecretAccessKey"`
	S3BucketEndpoint   string         `json:"s3BucketEndPoint,omitempty"`
	S3PathStyleAccess  bool           `json:"s3PathStyleAccess,omitempty"`
	InjectorConfig     InjectorConfig `json:"injectorConfig"`
}

type InjectorConfig struct {
	Version string          `json:"version"`
	SrcURI  string          `json:"srcURI,omitempty"`
	Shards  []InjectorShard `json:"shards"`
}

type InjectorShard struct {
	ShardID     string             `json:"shardId"`
	ReplSetName string             `json:"replSetName"`
	Instances   []InjectorInstance `json:"instances"`
}

type InjectorInstance struct {
	ID                 int    `json:"id"`
	Hostname           string `json:"hostname"`
	Disabled           bool   `json:"disabled"`
	Port               int    `json:"port"`
	ExternallyManaged  bool   `json:"externallyManaged"`
	HealthAPIEndpoint  string `json:"healthApiEndpoint"`
	MonarchAPIEndpoint string `json:"monarchApiEndpoint"`
}

// SetMaintainedMonarchComponents sets the maintainedMonarchComponents field in the automation config.
func (d Deployment) SetMaintainedMonarchComponents(mc []MaintainedMonarchComponents) {
	d["maintainedMonarchComponents"] = mc
}
