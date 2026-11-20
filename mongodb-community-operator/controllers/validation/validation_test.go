package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
)

func TestValidateAgentSettings(t *testing.T) {
	tests := []struct {
		name    string
		agent   mdbv1.AgentConfiguration
		wantErr string
	}{
		{name: "valid values", agent: mdbv1.AgentConfiguration{LogFile: "/var/log/agent.log", LogLevel: v1.LogLevelDebug}},
		{name: "empty values", agent: mdbv1.AgentConfiguration{}},
		{name: "logFile with newline", agent: mdbv1.AgentConfiguration{LogFile: "/var/log/agent.log\nmmsBaseUrl=x"}, wantErr: "spec.agent.logFile must not contain a line break or NUL byte"},
		{name: "logFile with carriage return", agent: mdbv1.AgentConfiguration{LogFile: "/var/log/agent.log\r"}, wantErr: "spec.agent.logFile must not contain a line break or NUL byte"},
		{name: "logFile with NUL", agent: mdbv1.AgentConfiguration{LogFile: "/var/log/agent.log\x00"}, wantErr: "spec.agent.logFile must not contain a line break or NUL byte"},
		{name: "logLevel with newline", agent: mdbv1.AgentConfiguration{LogLevel: "INFO\nmmsBaseUrl=x"}, wantErr: "spec.agent.logLevel must not contain a line break or NUL byte"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mdb := mdbv1.MongoDBCommunity{Spec: mdbv1.MongoDBCommunitySpec{AgentConfiguration: tt.agent}}
			err := validateAgentSettings(mdb)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
		})
	}
}
