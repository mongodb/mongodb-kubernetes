package backup

import (
	"fmt"

	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
)

type S3ConfigResponse struct {
	S3Configs []S3Config `json:"results"`
}

// https://docs.opsmanager.mongodb.com/current/reference/api/admin/backup/snapshot/s3Configs/create-one-s3-blockstore-configuration/#request-body-parameters
type S3Config struct {
	S3Bucket
	S3Credentials

	// Flag indicating the style of this endpoint.
	// true: Path-style URL endpoint eg. s3.amazonaws.com/<bucket>
	// false: Virtual-host-style URL endpoint eg. <bucket>.s3.amazonaws.com
	PathStyleAccessEnabled bool `json:"pathStyleAccessEnabled"`

	// Flag indicating whether you can assign backup jobs to this data store.
	AssignmentEnabled bool `json:"assignmentEnabled"`

	// Flag indicating whether or not you accepted the terms of service for using S3-compatible stores with Ops Manager.
	// If this is false, the request results in an error and Ops Manager doesn’t create the S3-compatible store.
	AcceptedTos bool `json:"acceptedTos"`

	// 	Unique name that labels this S3 Snapshot Store.
	Id string `json:"id"`

	// Positive integer indicating the maximum number of connections to this S3 blockstore.
	MaxConnections int `json:"s3MaxConnections"`

	// Comma-separated list of hosts in the <hostname:port> format that can access this S3 blockstore.
	Uri string `json:"uri"`

	// fields the operator will not configure. All of these can be changed via the UI and the operator
	// will not reset their values on reconciliation
	EncryptedCredentials bool     `json:"encryptedCredentials"`
	Labels               []string `json:"labels,omitempty"`
	LoadFactor           int      `json:"loadFactor,omitempty"`

	// ErrorCode: INVALID_ATTRIBUTE, Detail: Invalid attribute s3AuthMethod specified
	//AuthMethod           string   `json:"s3AuthMethod"`

	WriteConcern string `json:"writeConcern,omitempty"`

	// Flag indicating whether this S3 blockstore enables server-side encryption.
	SseEnabled bool `json:"sseEnabled"`

	// Required for OM 4.4
	DisableProxyS3 *bool `json:"disableProxyS3,omitempty"`

	// Flag indicating whether this S3 blockstore only accepts connections encrypted using TLS.
	Ssl bool `json:"ssl"`
}

type S3Bucket struct {
	// Name of the S3 bucket that hosts the S3 blockstore.
	Name string `json:"s3BucketName"`
	// URL used to access this AWS S3 or S3-compatible bucket.
	Endpoint string `json:"s3BucketEndpoint"`
}

type S3Credentials struct {
	// AWS Access Key ID that can access the S3 bucket specified in s3BucketName.
	AccessKey string `json:"awsAccessKey"`

	// AWS Secret Access Key that can access the S3 bucket specified in s3BucketName.
	SecretKey string `json:"awsSecretKey"`
}

func NewS3Config(opsManager omv1.MongoDBOpsManager, id, uri string, bucket S3Bucket, s3Creds S3Credentials) S3Config {
	config := S3Config{
		S3Bucket:               bucket,
		S3Credentials:          s3Creds,
		AcceptedTos:            true,
		AssignmentEnabled:      true, // default to enabled. This will not be overridden on merge so it can be manually disabled in UI.
		SseEnabled:             false,
		DisableProxyS3:         nil,
		Id:                     id,
		Uri:                    uri,
		MaxConnections:         util.DefaultS3MaxConnections, // can be configured in UI
		Labels:                 []string{},
		EncryptedCredentials:   false,
		PathStyleAccessEnabled: true,
	}

	version, err := versionutil.StringToSemverVersion(opsManager.Spec.Version)
	if err == nil && version.Major == 4 && version.Minor == 4 {
		// DisableProxyS3 is only available in 4.4 version of Ops Manager.
		config.DisableProxyS3 = util.BooleanRef(false)
	}

	return config
}

func (s S3Config) Identifier() interface{} {
	return s.Id
}

// MergeIntoOpsManagerConfig performs the merge operation of the Operator config view ('s') into the OM owned one
// ('opsManagerS3Config')
func (s S3Config) MergeIntoOpsManagerConfig(opsManagerS3Config S3Config) S3Config {
	opsManagerS3Config.Id = s.Id
	opsManagerS3Config.S3Credentials = s.S3Credentials
	opsManagerS3Config.S3Bucket = s.S3Bucket
	opsManagerS3Config.PathStyleAccessEnabled = s.PathStyleAccessEnabled
	opsManagerS3Config.Uri = s.Uri
	return opsManagerS3Config
}

func (s S3Config) String() string {
	return fmt.Sprintf("id %s, uri: %s, enabled: %t, awsAccessKey: %s, awsSecretKey: %s, bucketEndpoint: %s, bucketName: %s, pathStyleAccessEnabled: %t",
		s.Id, util.RedactMongoURI(s.Uri), s.AssignmentEnabled, util.Redact(s.AccessKey), util.Redact(s.SecretKey), s.S3Bucket.Endpoint, s.S3Bucket.Name, s.PathStyleAccessEnabled)
}
