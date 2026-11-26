package v1_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodbcommunity.mongodb.com_mongodbcommunity.yaml")))
}

// communityClient is needed because the shared envtest client only registers the enterprise scheme.
func communityClient(t *testing.T) client.Client {
	scheme := kruntime.NewScheme()
	require.NoError(t, mdbcv1.AddToScheme(scheme))
	c, err := client.New(env.Shared(t).Config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	return c
}

func TestMongoDBCommunityAgentSettingsCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := communityClient(t)

	tests := []struct {
		name          string
		logFile       string
		logLevel      v1.LogLevel
		errorContains string
	}{
		{name: "plain values are accepted", logFile: "/var/log/agent.log", logLevel: "DEBUG"},
		{name: "logFile with spaces is accepted", logFile: "/var/log/my agent.log"},
		{name: "logFile with a line feed is rejected", logFile: "/var/log/a.log\nlogLevel=DEBUG", errorContains: "spec.agent.logFile must not contain a line break or NUL byte"},
		{name: "logFile with a carriage return is rejected", logFile: "/var/log/a.log\r", errorContains: "spec.agent.logFile must not contain a line break or NUL byte"},
		{name: "logFile with a NUL byte is rejected", logFile: "/var/log/a.log\x00", errorContains: "spec.agent.logFile must not contain a line break or NUL byte"},
		{name: "logLevel with a line feed is rejected", logLevel: "INFO\nlogFile=/tmp/x", errorContains: "spec.agent.logLevel must not contain a line break or NUL byte"},
		{name: "logLevel with a carriage return is rejected", logLevel: "INFO\r", errorContains: "spec.agent.logLevel must not contain a line break or NUL byte"},
		{name: "logLevel with a NUL byte is rejected", logLevel: "INFO\x00", errorContains: "spec.agent.logLevel must not contain a line break or NUL byte"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mdbc := &mdbcv1.MongoDBCommunity{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-", Namespace: "default"},
				Spec: mdbcv1.MongoDBCommunitySpec{
					Members: 1,
					Type:    mdbcv1.ReplicaSet,
					Version: "8.0.0",
					Users:   []mdbcv1.MongoDBUser{},
					Security: mdbcv1.Security{
						Authentication: mdbcv1.Authentication{Modes: []mdbcv1.AuthMode{mdbcv1.Scram256AuthMode}},
					},
					AgentConfiguration: mdbcv1.AgentConfiguration{
						LogFile:  tc.logFile,
						LogLevel: tc.logLevel,
					},
				},
			}
			err := k8sClient.Create(ctx, mdbc)

			if tc.errorContains == "" {
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, k8sClient.Delete(context.Background(), mdbc)) })
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
