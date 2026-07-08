package memberwatch

import (
	"go.uber.org/zap"
)

type MockedMemberHealthCheck struct {
	Server  string
	Healthy bool
}

func NewMockedMemberHealthCheck(server string) ClusterHealthChecker {
	return &MockedMemberHealthCheck{
		Server:  server,
		Healthy: true,
	}
}

func (m *MockedMemberHealthCheck) IsClusterHealthy(_ *zap.SugaredLogger) bool {
	return m.Healthy
}
