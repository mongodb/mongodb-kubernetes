package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfig_collectionEnabled(t *testing.T) {
	cfg := Config{
		CollectClusters:    true,
		CollectDeployments: false,
		CollectOperators:   true,
	}

	assert.True(t, cfg.collectionEnabled(Clusters))
	assert.False(t, cfg.collectionEnabled(Deployments))
	assert.True(t, cfg.collectionEnabled(Operators))
	assert.False(t, cfg.collectionEnabled(EventType("Unknown")))
}
