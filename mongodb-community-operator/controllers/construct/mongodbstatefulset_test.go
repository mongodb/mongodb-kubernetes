package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
)

func TestMongodbContainer_SignalHandling(t *testing.T) {
	tests := []struct {
		name     string
		isStatic bool
		wantExec bool
	}{
		{
			name:     "Non-static architecture uses exec mongod",
			isStatic: false,
			wantExec: true,
		},
		{
			name:     "Static architecture uses trap and background mongod",
			isStatic: true,
			wantExec: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mongodConfig := v1.NewMongodConfiguration()
			mongodConfig.SetOption("storage.dbPath", "/data")

			containerMod := mongodbContainer("test-image", []corev1.VolumeMount{}, mongodConfig, tt.isStatic)

			testContainer := &corev1.Container{}
			containerMod(testContainer)

			assert.Len(t, testContainer.Command, 3)
			assert.Equal(t, "/bin/sh", testContainer.Command[0])
			assert.Equal(t, "-c", testContainer.Command[1])
			commandScript := testContainer.Command[2]

			if tt.isStatic {
				assert.Contains(t, commandScript, "trap cleanup SIGTERM", "Static architecture should include signal trap")
				assert.Contains(t, commandScript, "cleanup() {", "Static architecture should include cleanup function")
				assert.Contains(t, commandScript, "mongod -f /data/automation-mongod.conf &", "Static architecture should run mongod in background")
				assert.Contains(t, commandScript, "wait \"$MONGOD_PID\"", "Static architecture should wait for mongod process")
				assert.Contains(t, commandScript, "termination_timeout_seconds", "Static architecture should include timeout configuration")
				assert.Contains(t, commandScript, "while [ -e \"/proc/${MONGOD_PID}\" ]", "Static architecture should include robust process waiting")
				assert.Contains(t, commandScript, "kill -15 \"$MONGOD_PID\"", "Static architecture should send SIGTERM to mongod")
			} else {
				assert.NotContains(t, commandScript, "trap cleanup SIGTERM", "Non-static architecture should not include signal trap")
				assert.NotContains(t, commandScript, "cleanup() {", "Non-static architecture should not include cleanup function")
				assert.Contains(t, commandScript, "exec mongod -f /data/automation-mongod.conf", "Non-static architecture should exec mongod")
			}

			assert.Contains(t, commandScript, "Waiting for config and keyfile files to be created by the agent", "Should wait for agent files")
			assert.Contains(t, commandScript, "Starting mongod...", "Should start mongod")
		})
	}
}
