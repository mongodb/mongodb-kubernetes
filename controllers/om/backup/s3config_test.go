package backup

import (
	"encoding/json"
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

func TestNewS3ConfigSendsObjectLockOrPortableBackupFieldsByVersion(t *testing.T) {
	tests := []struct {
		version              string
		wantObjectLockFields bool
	}{
		{version: "8.0.27", wantObjectLockFields: true},
		{version: "8.0.28", wantObjectLockFields: false},
		{version: "9.0.1", wantObjectLockFields: true},
		{version: "9.0.2", wantObjectLockFields: false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			opsManager := omv1.NewOpsManagerBuilder().SetVersion(tt.version).Build()
			specConfig := omv1.S3Config{
				Name:                  "s3",
				ObjectLockEnabled:     new(true),
				ObjectRetentionDays:   new(30),
				ObjectRetentionMode:   new("GOVERNANCE"),
				PortableBackupEnabled: new(true),
			}

			config := NewS3Config(opsManager, specConfig, "", nil, S3Bucket{}, nil)
			if tt.wantObjectLockFields {
				assert.Equal(t, new(true), config.ObjectLockEnabled)
				assert.Equal(t, new(30), config.ObjectRetentionDays)
				assert.Equal(t, new("GOVERNANCE"), config.ObjectRetentionMode)
				assert.Nil(t, config.PortableBackupEnabled)
			} else {
				assert.Nil(t, config.ObjectLockEnabled)
				assert.Nil(t, config.ObjectRetentionDays)
				assert.Nil(t, config.ObjectRetentionMode)
				assert.Equal(t, new(true), config.PortableBackupEnabled)
			}

			payload, err := json.Marshal(config)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(payload, &fields))
			if tt.wantObjectLockFields {
				assert.NotContains(t, fields, "portableBackupEnabled")
			} else {
				assert.Equal(t, true, fields["portableBackupEnabled"])
				assert.NotContains(t, fields, "objectLockEnabled")
				assert.NotContains(t, fields, "objectRetentionDays")
				assert.NotContains(t, fields, "objectRetentionMode")
			}
		})
	}
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

func TestS3ConfigMergePreservesOmittedPortableBackupEnabled(t *testing.T) {
	omConfig := S3Config{
		Id:                    "s3",
		PortableBackupEnabled: new(true),
	}

	merged := (S3Config{Id: "s3"}).MergeIntoOpsManagerConfig(omConfig)

	assert.Equal(t, new(true), merged.PortableBackupEnabled)
}

func TestS3ConfigMergeOmitsDeprecatedFieldsAndPreservesPortableBackup(t *testing.T) {
	omConfig := S3Config{
		Id:                    "s3",
		ObjectLockEnabled:     new(true),
		ObjectRetentionDays:   new(30),
		ObjectRetentionMode:   new("GOVERNANCE"),
		PortableBackupEnabled: new(true),
	}

	merged := (S3Config{Id: "s3"}).MergeIntoOpsManagerConfig(omConfig)

	assert.Nil(t, merged.ObjectLockEnabled)
	assert.Nil(t, merged.ObjectRetentionDays)
	assert.Nil(t, merged.ObjectRetentionMode)
	assert.Equal(t, new(true), merged.PortableBackupEnabled)

	payload, err := json.Marshal(merged)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(payload, &fields))
	assert.NotContains(t, fields, "objectLockEnabled")
	assert.NotContains(t, fields, "objectRetentionDays")
	assert.NotContains(t, fields, "objectRetentionMode")
	assert.Equal(t, true, fields["portableBackupEnabled"])
}

func TestS3ConfigMergeAppliesExplicitFalsePortableBackupEnabled(t *testing.T) {
	omConfig := S3Config{
		Id:                    "s3",
		PortableBackupEnabled: new(true),
	}

	merged := (S3Config{Id: "s3", PortableBackupEnabled: new(false)}).MergeIntoOpsManagerConfig(omConfig)

	assert.Equal(t, new(false), merged.PortableBackupEnabled)
	payload, err := json.Marshal(merged)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(payload, &fields))
	assert.Equal(t, false, fields["portableBackupEnabled"])
}

func TestS3ConfigCreateOmitsUnsetPortableBackupEnabled(t *testing.T) {
	payload, err := json.Marshal(S3Config{})
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(payload, &fields))
	assert.NotContains(t, fields, "portableBackupEnabled")
}
