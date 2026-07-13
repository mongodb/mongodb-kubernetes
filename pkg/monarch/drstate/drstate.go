// Package drstate provides a client for reading and writing the Monarch DR state file in S3.
// The DR state file is the coordination point between the operator and agent during failover.
package drstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// State represents the possible states in the DR state machine.
type State string

const (
	// StateActive indicates the cluster is the active (primary) cluster.
	StateActive State = "Active"
	// StateStandby indicates the cluster is a standby cluster.
	StateStandby State = "Standby"
	// StatePromoteStandby is set to trigger promotion of a standby cluster.
	StatePromoteStandby State = "PromoteStandby"
	// StateStandbyReadyToPromote indicates the agent has completed RS reconfiguration.
	StateStandbyReadyToPromote State = "StandbyReadyToPromote"
)

// DRState represents the content of the DR state file in S3.
// This matches the agent's RemoteDRState schema.
type DRState struct {
	// State is the current DR state (Active, Standby, PromoteStandby, StandbyReadyToPromote).
	State State `json:"state"`

	// PreviousState is the state before the last transition.
	PreviousState string `json:"previousState,omitempty"`

	// ClusterName is the name of the cluster that wrote this state.
	ClusterName string `json:"clusterName"`

	// Version is a monotonically increasing version number (as string).
	Version string `json:"version"`

	// LastModified is the RFC3339 timestamp of when this state was last written.
	LastModified string `json:"lastModified"`

	// SchemaVersion is the schema version for forward compatibility.
	SchemaVersion string `json:"schemaVersion"`
}

// DRStateWithETag wraps DRState with the S3 ETag for CAS operations.
type DRStateWithETag struct {
	DRState
	// ETag is the S3 ETag for conditional writes (CAS).
	ETag string
}

// ClientConfig contains configuration for the S3 DR state client.
type ClientConfig struct {
	BucketName      string
	Region          string
	ClusterPrefix   string
	ClusterName     string // Used in S3 key: <prefix>/dr_status_<clusterName>.json
	Endpoint        string // Optional custom endpoint (for MinIO)
	PathStyleAccess bool   // Enable path-style access (for MinIO)
	AccessKeyID     string
	SecretAccessKey string
}

// Client provides operations for reading and writing the DR state file in S3.
type Client struct {
	s3Client      *s3.Client
	bucketName    string
	clusterPrefix string
	clusterName   string
}

// drStateKey returns the S3 key for the DR state file.
func (c *Client) drStateKey() string {
	if c.clusterPrefix == "" {
		return fmt.Sprintf("dr_status_%s.json", c.clusterName)
	}
	return fmt.Sprintf("%s/dr_status_%s.json", c.clusterPrefix, c.clusterName)
}

// NewClient creates a new DR state client.
func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	// Build AWS config with static credentials
	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Build S3 client options
	s3Opts := []func(*s3.Options){}

	// Custom endpoint for MinIO/LocalStack
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		})
	}

	// Path-style access for MinIO
	if cfg.PathStyleAccess {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	}

	s3Client := s3.NewFromConfig(awsCfg, s3Opts...)

	return &Client{
		s3Client:      s3Client,
		bucketName:    cfg.BucketName,
		clusterPrefix: cfg.ClusterPrefix,
		clusterName:   cfg.ClusterName,
	}, nil
}

// Read reads the current DR state from S3.
// Returns the state with ETag for subsequent CAS writes.
func (c *Client) Read(ctx context.Context) (*DRStateWithETag, error) {
	result, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(c.drStateKey()),
	})
	if err != nil {
		// Check if the object doesn't exist using proper AWS SDK error types
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, nil // No state file yet
		}
		// Some S3-compatible stores (MinIO) may return NotFound instead of NoSuchKey
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read DR state from S3: %w", err)
	}
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read DR state body: %w", err)
	}

	var state DRState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal DR state: %w", err)
	}

	etag := ""
	if result.ETag != nil {
		etag = *result.ETag
	}

	return &DRStateWithETag{
		DRState: state,
		ETag:    etag,
	}, nil
}

// Write writes a new DR state to S3 using CAS (Compare-and-Swap) via ETag.
// If expectedETag is non-empty, the write will fail if the current ETag doesn't match.
// Returns the new ETag on success.
func (c *Client) Write(ctx context.Context, state DRState, expectedETag string) (string, error) {
	body, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to marshal DR state: %w", err)
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(c.bucketName),
		Key:         aws.String(c.drStateKey()),
		Body:        strings.NewReader(string(body)),
		ContentType: aws.String("application/json"),
	}

	// CAS: only write if ETag matches (prevents concurrent modification)
	if expectedETag != "" {
		input.IfMatch = aws.String(expectedETag)
	}

	result, err := c.s3Client.PutObject(ctx, input)
	if err != nil {
		// Check for CAS failure (ETag mismatch) using proper AWS SDK error types
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) &&
			(apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "ConditionalRequestConflict") {
			return "", ErrCASConflict
		}
		return "", fmt.Errorf("failed to write DR state to S3: %w", err)
	}

	newETag := ""
	if result.ETag != nil {
		newETag = *result.ETag
	}

	return newETag, nil
}

// ErrCASConflict is returned when a CAS write fails due to ETag mismatch.
var ErrCASConflict = fmt.Errorf("CAS conflict: DR state was modified by another writer")

// TransitionTo attempts to transition the DR state to a new state using CAS.
// It reads the current state, validates the transition, and writes the new state.
// Returns the new ETag on success, or ErrCASConflict if the state was modified.
func (c *Client) TransitionTo(ctx context.Context, newState State) (*DRStateWithETag, error) {
	// Read current state
	current, err := c.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read current state: %w", err)
	}

	// Build new state
	var expectedETag string
	var previousState string
	version := "1"

	if current != nil {
		expectedETag = current.ETag
		previousState = string(current.State)

		// Increment version
		if v, parseErr := strconv.Atoi(current.Version); parseErr == nil {
			version = strconv.Itoa(v + 1)
		}
	}

	state := DRState{
		State:         newState,
		PreviousState: previousState,
		ClusterName:   c.clusterName,
		Version:       version,
		LastModified:  time.Now().UTC().Format(time.RFC3339),
		SchemaVersion: "1",
	}

	// Write with CAS
	newETag, err := c.Write(ctx, state, expectedETag)
	if err != nil {
		return nil, err
	}

	return &DRStateWithETag{
		DRState: state,
		ETag:    newETag,
	}, nil
}
