package operator

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"os"

	"github.com/stretchr/testify/assert"
)

func TestStatefulsetCreationSuccessful(t *testing.T) {
	start := time.Now()
	helper := defaultSetHelper()

	err := helper.CreateOrUpdateInKubernetes()
	assert.Nil(t, err)
	assert.True(t, time.Now().Sub(start) < time.Second*2) // we waited only a little
}

func TestStatefulsetCreationWaitsForCompletion(t *testing.T) {
	start := time.Now()
	helper := baseSetHelperDelayed(5000).SetLogger(zap.S()).SetPodSpec(defaultPodSpec()).SetPodVars(defaultPodVars()).SetService("test-service")
	err := helper.CreateOrUpdateInKubernetes()
	assert.Errorf(t, err, "failed to reach READY state")
	assert.True(t, time.Now().Sub(start) >= time.Second*2) // we have two retrials each waiting for one second
}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	os.Setenv(util.AutomationAgentImageUrl, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(util.AutomationAgentImagePullPolicy, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(util.PodWaitSecondsEnv, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(util.PodWaitRetriesEnv, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()
}
