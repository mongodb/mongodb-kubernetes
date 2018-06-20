package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateOmProcess(t *testing.T) {
	process := createProcess(defaultSetHelper().BuildStatefulSet(), baseStandalone())

	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "testStandalone", process.Name())
	assert.Equal(t, "testStandalone-0.test-service.mongodb.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0", process.Version())
}
