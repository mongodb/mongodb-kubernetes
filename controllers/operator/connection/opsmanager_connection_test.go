package connection

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

const (
	sourceGroupID = "source-project"
	targetGroupID = "target-project"
	sourceKey     = "source-keyfile-contents"
)

type mockConnWithReadFailure struct {
	om.Connection
	readErr error
}

func (m *mockConnWithReadFailure) ReadAutomationConfig() (*om.AutomationConfig, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.Connection.ReadAutomationConfig()
}

func testProjectConfig() mdbv1.ProjectConfig {
	return mdbv1.ProjectConfig{
		BaseURL:     om.TestURL,
		ProjectName: om.TestGroupName,
		OrgID:       om.TestOrgID,
	}
}

func testCredentials() mdbv1.Credentials {
	return mdbv1.Credentials{
		PublicAPIKey:  "pub",
		PrivateAPIKey: "priv",
	}
}

func newMockConn(groupID string, deployment om.Deployment) *om.MockedOmConnection {
	mock := om.NewMockedOmConnection(deployment)
	mock.ConfigureProject(&om.Project{ID: groupID, OrgID: om.TestOrgID})
	return mock
}

func sourceDeployment() om.Deployment {
	d := om.NewDeployment()
	d["processes"] = []om.Process{{
		"name":     "rs-0",
		"version":  "7.0.0",
		"hostname": "rs-0.example.com",
	}}
	d["auth"] = map[string]interface{}{
		"key":      sourceKey,
		"disabled": false,
	}
	d["version"] = int64(5)
	return d
}

func emptyTargetDeployment() om.Deployment {
	d := om.NewDeployment()
	d["version"] = int64(3)
	return d
}

func factoryFor(sourceConn om.Connection) om.ConnectionFactory {
	return func(ctx *om.OMContext) om.Connection {
		if ctx.GroupID == sourceGroupID {
			return sourceConn
		}
		return nil
	}
}

func TestEnsureTargetAutomationConfigSeeded_NoOp(t *testing.T) {
	tests := []struct {
		name              string
		sourceGroupID     string
		targetGroupID     string
		targetDeploy      om.Deployment
		expectedProcesses int
	}{
		{
			name:              "empty source project id",
			sourceGroupID:     "",
			targetGroupID:     targetGroupID,
			targetDeploy:      emptyTargetDeployment(),
			expectedProcesses: 0,
		},
		{
			name:              "source equals target",
			sourceGroupID:     targetGroupID,
			targetGroupID:     targetGroupID,
			targetDeploy:      emptyTargetDeployment(),
			expectedProcesses: 0,
		},
		{
			name:              "target already has processes",
			sourceGroupID:     sourceGroupID,
			targetGroupID:     targetGroupID,
			targetDeploy:      sourceDeployment(),
			expectedProcesses: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targetConn := newMockConn(tc.targetGroupID, tc.targetDeploy)
			sourceConn := newMockConn(sourceGroupID, sourceDeployment())

			err := EnsureTargetAutomationConfigSeeded(
				targetConn, tc.sourceGroupID, testProjectConfig(), testCredentials(),
				factoryFor(sourceConn), zap.NewNop().Sugar(),
			)
			assert.NoError(t, err)

			targetAC, _ := targetConn.ReadAutomationConfig()
			assert.Equal(t, tc.expectedProcesses, targetAC.Deployment.NumberOfProcesses(), "target processes should be unchanged")
		})
	}
}

func TestEnsureTargetAutomationConfigSeeded_CopiesSourceAC(t *testing.T) {
	targetConn := newMockConn(targetGroupID, emptyTargetDeployment())
	sourceConn := newMockConn(sourceGroupID, sourceDeployment())

	err := EnsureTargetAutomationConfigSeeded(
		targetConn, sourceGroupID, testProjectConfig(), testCredentials(),
		factoryFor(sourceConn), zap.NewNop().Sugar(),
	)
	assert.NoError(t, err)

	targetAC, _ := targetConn.ReadAutomationConfig()

	assert.Equal(t, 1, targetAC.Deployment.NumberOfProcesses(), "target should have source processes")

	authMap, ok := targetAC.Deployment["auth"].(map[string]interface{})
	require.True(t, ok, "auth map should exist")
	assert.Equal(t, sourceKey, authMap["key"], "target auth.key should equal source key")

	assert.Equal(t, int64(3), targetAC.Deployment.Version(), "target version should be retained")
}

func TestEnsureTargetAutomationConfigSeeded_SourceReadFailureStops(t *testing.T) {
	targetConn := newMockConn(targetGroupID, emptyTargetDeployment())
	failingSource := &mockConnWithReadFailure{
		Connection: newMockConn(sourceGroupID, sourceDeployment()),
		readErr:    xerrors.Errorf("simulated source read failure"),
	}

	err := EnsureTargetAutomationConfigSeeded(
		targetConn, sourceGroupID, testProjectConfig(), testCredentials(),
		factoryFor(failingSource), zap.NewNop().Sugar(),
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read source project automation config")

	targetAC, _ := targetConn.ReadAutomationConfig()
	assert.Equal(t, 0, targetAC.Deployment.NumberOfProcesses(), "target should be unchanged after source read failure")
}

func TestEnsureTargetAutomationConfigSeeded_TargetReadFailureStops(t *testing.T) {
	failingTarget := &mockConnWithReadFailure{
		Connection: newMockConn(targetGroupID, emptyTargetDeployment()),
		readErr:    xerrors.Errorf("simulated target read failure"),
	}
	sourceConn := newMockConn(sourceGroupID, sourceDeployment())

	err := EnsureTargetAutomationConfigSeeded(
		failingTarget, sourceGroupID, testProjectConfig(), testCredentials(),
		factoryFor(sourceConn), zap.NewNop().Sugar(),
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read target project automation config")
}
