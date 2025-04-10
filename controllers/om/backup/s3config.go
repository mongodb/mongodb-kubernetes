package backup

import (
	"fmt"

	"go.uber.org/zap"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
)

type authmode string

const (
	KEYS authmode = "KEYS"
	IAM  authmode = "IAM_ROLE"
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
	// false: Virtual-host-style URL endpoint e.g. <bucket>.s3.amazonaws.com
	PathStyleAccessEnabled bool `json:"pathStyleAccessEnabled"`

	// Flag indicating whether you can assign backup jobs to this data store.
	AssignmentEnabled bool `json:"assignmentEnabled"`

	// Flag indicating whether you accepted the terms of service for using S3-compatible stores with Ops Manager.
	// If this is false, the request results in an error and Ops Manager doesn't create the S3-compatible store.
	AcceptedTos bool `json:"acceptedTos"`

	// 	Unique name that labels this S3 Snapshot Store.
	Id string `json:"id"`

	// Positive integer indicating the maximum number of connections to this S3 blockstore.
	MaxConnections int `json:"s3MaxConnections"`

	// Comma-separated list of hosts in the <hostname:port> format that can access this S3 blockstore.
	Uri string `json:"uri"`

	// Fields the operator will not configure. All of these can be changed via the UI and the operator
	// will not reset their values on reconciliation
	EncryptedCredentials bool     `json:"encryptedCredentials"`
	Labels               []string `json:"labels"`
	LoadFactor           int      `json:"loadFactor,omitempty"`

	AuthMethod string `json:"s3AuthMethod"`

	WriteConcern string `json:"writeConcern,omitempty"`

	// Region where the S3 bucket resides.
	// This is currently set to the empty string to avoid HELP-22791.
	S3RegionOverride *string `json:"s3RegionOverride,omitempty"`

	// Flag indicating whether this S3 blockstore enables server-side encryption.
	SseEnabled bool `json:"sseEnabled"`

	// Required for OM 4.4
	DisableProxyS3 *bool `json:"disableProxyS3,omitempty"`

	// Flag indicating whether this S3 blockstore only accepts connections encrypted using TLS.
	Ssl bool `json:"ssl"`

	// CustomCertificates is a list of valid Certificate Authority certificates that apply to the associated S3 bucket.
	CustomCertificates []S3CustomCertificate `json:"customCertificates,omitempty"`
}

// S3CustomCertificate stores the filename or contents of a custom certificate PEM file.
type S3CustomCertificate struct {
	// Filename identifies the Certificate Authority PEM file.
	Filename string `json:"filename"`
	// CertString contains the contents of the Certificate Authority PEM file that comprise your Certificate Authority chain.
	CertString string `json:"certString"`
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

func NewS3Config(opsManager *omv1.MongoDBOpsManager, s3Config omv1.S3Config, uri string, s3CustomCertificates []S3CustomCertificate, bucket S3Bucket, s3Creds *S3Credentials) S3Config {
	authMode := IAM
	cred := S3Credentials{}

	if s3Creds != nil {
		authMode = KEYS
		cred = *s3Creds
	}

	config := S3Config{
		S3Bucket:               bucket,
		S3Credentials:          cred,
		AcceptedTos:            true,
		AssignmentEnabled:      true, // defaults to true. This will not be overridden on merge, so it can be manually disabled in UI.
		SseEnabled:             false,
		DisableProxyS3:         nil,
		Id:                     s3Config.Name,
		Uri:                    uri,
		MaxConnections:         util.DefaultS3MaxConnections, // can be configured in UI
		Labels:                 s3Config.AssignmentLabels,
		EncryptedCredentials:   false,
		PathStyleAccessEnabled: true,
		AuthMethod:             string(authMode),
		S3RegionOverride:       &s3Config.S3RegionOverride,
	}

	if _, err := versionutil.StringToSemverVersion(opsManager.Spec.Version); err == nil {
		config.DisableProxyS3 = util.BooleanRef(false)

		for _, certificate := range s3CustomCertificates {

			if s3Config.CustomCertificate {
				zap.S().Warn("CustomCertificate is deprecated. Please switch to customCertificates to add your appDB-CA")
			}

			// Historically, if s3Config.CustomCertificate was set to true, then we would use the appDBCa for s3Config.
			if !s3Config.CustomCertificate && certificate.Filename == omv1.GetAppDBCaPemPath() {
				continue
			}

			// Attributes that are only available in 5.0+ version of Ops Manager.
			// Both filename and path need to be provided.
			if certificate.CertString != "" && certificate.Filename != "" {
				// CustomCertificateSecretRefs needs to be a pointer for it to not be
				// passed as part of the API request.
				config.CustomCertificates = append(config.CustomCertificates, certificate)
			}
		}
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
	opsManagerS3Config.S3RegionOverride = s.S3RegionOverride
	opsManagerS3Config.Labels = s.Labels
	opsManagerS3Config.CustomCertificates = s.CustomCertificates
	return opsManagerS3Config
}

func (s S3Config) String() string {
	return fmt.Sprintf("id %s, uri: %s, enabled: %t, awsAccessKey: %s, awsSecretKey: %s, bucketEndpoint: %s, bucketName: %s, pathStyleAccessEnabled: %t",
		s.Id, util.RedactMongoURI(s.Uri), s.AssignmentEnabled, util.Redact(s.AccessKey), util.Redact(s.SecretKey), s.Endpoint, s.Name, s.PathStyleAccessEnabled)
}
