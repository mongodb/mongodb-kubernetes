package operator

import (
	"testing"
	"time"

	"os"

	"github.com/stretchr/testify/assert"
)

func TestStatefulsetCreationSuccessful(t *testing.T) {
	start := time.Now()
	helper := defaultSetHelper()
	go startStatefulset(helper.Helper.kubeApi.(*MockedKubeApi))

	err := helper.CreateOrUpdateInKubernetes()
	assert.Nil(t, err)
	assert.True(t, time.Now().Sub(start) < time.Second*2) // we waited only a little
}

func TestStatefulsetCreationWaitsForCompletion(t *testing.T) {
	start := time.Now()
	err := defaultSetHelper().CreateOrUpdateInKubernetes()
	assert.Errorf(t, err, "failed to reach READY state")
	assert.True(t, time.Now().Sub(start) >= time.Second*2) // we have two retrials each waiting for one second
}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	os.Setenv(AutomationAgentImageUrl, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(AutomationAgentImagePullPolicy, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(StatefulSetWaitSecondsEnv, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(StatefulSetWaitRetrialsEnv, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()
}

func startStatefulset(api *MockedKubeApi) {
	time.Sleep(200 * time.Millisecond)
	api.startStatefulsets()
}
