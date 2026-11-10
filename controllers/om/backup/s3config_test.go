package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
)

func TestNewS3ConfigCopiesObjectLockRetentionFields(t *testing.T) {
	opsManager := omv1.NewOpsManagerBuilder().SetVersion("8.0.27").Build()
	specConfig := omv1.S3Config{
		Name:                "s3",
		ObjectLockEnabled:   new(true),
		ObjectRetentionDays: new(30),
		ObjectRetentionMode: new("GOVERNANCE"),
	}

	config := NewS3Config(opsManager, specConfig, "", nil, S3Bucket{}, nil)

	require.NotNil(t, config.ObjectRetentionDays)
	assert.Equal(t, 30, *config.ObjectRetentionDays)
	require.NotNil(t, config.ObjectRetentionMode)
	assert.Equal(t, "GOVERNANCE", *config.ObjectRetentionMode)
}

func TestNewS3ConfigOmitsObjectLockRetentionFieldsBefore8_0_27(t *testing.T) {
	// OM versions older than 8.0.27 reject the retention attributes with
	// INVALID_ATTRIBUTE, so the operator must not send them.
	opsManager := omv1.NewOpsManagerBuilder().SetVersion("8.0.26").Build()
	specConfig := omv1.S3Config{
		Name:                "s3",
		ObjectLockEnabled:   new(true),
		ObjectRetentionDays: new(30),
		ObjectRetentionMode: new("GOVERNANCE"),
	}

	config := NewS3Config(opsManager, specConfig, "", nil, S3Bucket{}, nil)

	assert.Nil(t, config.ObjectRetentionDays)
	assert.Nil(t, config.ObjectRetentionMode)
	assert.Equal(t, new(true), config.ObjectLockEnabled)
}

func TestS3ConfigMergeAppliesObjectLockRetentionFields(t *testing.T) {
	operatorView := S3Config{
		Id:                  "s3",
		ObjectLockEnabled:   new(true),
		ObjectRetentionDays: new(30),
		ObjectRetentionMode: new("COMPLIANCE"),
	}
	omConfig := S3Config{
		Id:                "s3",
		ObjectLockEnabled: new(true),
	}

	merged := operatorView.MergeIntoOpsManagerConfig(omConfig)

	require.NotNil(t, merged.ObjectRetentionDays)
	assert.Equal(t, 30, *merged.ObjectRetentionDays)
	require.NotNil(t, merged.ObjectRetentionMode)
	assert.Equal(t, "COMPLIANCE", *merged.ObjectRetentionMode)
}
