package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventTypeMethods(t *testing.T) {
	tests := []struct {
		eventType       EventType
		expectedPayload string
		expectedTimeKey string
	}{
		{
			eventType:       Deployments,
			expectedTimeKey: "lastSendTimestampDeployments",
		},
		{
			eventType:       Operators,
			expectedTimeKey: "lastSendTimestampOperators",
		},
		{
			eventType:       Clusters,
			expectedTimeKey: "lastSendTimestampClusters",
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			assert.Equal(t, tt.expectedTimeKey, tt.eventType.GetTimeStampKey(), "GetTimeStampKey() mismatch")
		})
	}
}
