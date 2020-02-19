package operator

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStatefulsetCreationSuccessful(t *testing.T) {
	start := time.Now()
	helper := defaultSetHelper()

	err := helper.CreateOrUpdateInKubernetes()
	assert.NoError(t, err)
	assert.True(t, time.Now().Sub(start) < time.Second*4) // we waited only a little (considering 2 seconds of wait as well)
}

func TestStatefulsetCreationWaitsForCompletion(t *testing.T) {
	start := time.Now()
	helper := baseSetHelperDelayed(5000).
		SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().Build()).
		SetPodVars(defaultPodVars()).
		SetService("test-service").
		SetSecurity(&mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{},
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
		})
	err := helper.CreateOrUpdateInKubernetes()
	assert.NoError(t, err)

	// There was not waiting for the StatefulSet to be ready
	assert.False(t, time.Now().Sub(start) >= time.Second*2)

	ready := helper.Helper.isStatefulSetUpdated(helper.Namespace, helper.Name, zap.S())
	assert.False(t, ready)
}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	defer InitDefaultEnvVariables()

	os.Setenv(util.AutomationAgentImageUrl, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(util.AutomationAgentImagePullPolicy, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
}

// TestComputeConfigMap_CreateNew checks the "create" features of 'computeConfigMap' function when the configmap is created
// if it doesn't exist (or the creation is skipped totally)
func TestComputeConfigMap_CreateNew(t *testing.T) {
	client := newMockedClient()
	helper := NewKubeHelper(client)
	owner := mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
	key := objectKey("ns", "cfm")
	testData := map[string]string{"foo": "bar"}

	// Successful creation
	err := helper.computeConfigMap(key, func(cmap *corev1.ConfigMap) bool {
		cmap.Data = testData
		return true
	}, &owner)

	assert.NoError(t, err)

	cmap := &corev1.ConfigMap{}
	err = client.Get(context.TODO(), key, cmap)
	assert.NoError(t, err)
	assert.Equal(t, key.Name, cmap.Name)
	assert.Equal(t, key.Namespace, cmap.Namespace)
	assert.Equal(t, "test", cmap.OwnerReferences[0].Name)
	assert.Equal(t, testData, cmap.Data)

	// Creation skipped
	key2 := objectKey("ns", "cfm2")
	_ = helper.computeConfigMap(key2, func(cmap *corev1.ConfigMap) bool {
		return false
	}, &owner)

	err = client.Get(context.TODO(), key2, cmap)
	assert.True(t, apiErrors.IsNotFound(err))
}

func TestComputeConfigMap_UpdateExisting(t *testing.T) {
	client := newMockedClient()
	client.AddProjectConfigMap(om.TestGroupName, "")
	helper := NewKubeHelper(client)
	owner := mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

	key := objectKey(TestNamespace, TestProjectConfigMapName)

	// Successful update (data is appended)
	err := helper.computeConfigMap(key, func(cmap *corev1.ConfigMap) bool {
		cmap.Data["foo"] = "bla"
		return true
	}, &owner)

	assert.NoError(t, err)

	cmap := &corev1.ConfigMap{}
	err = client.Get(context.TODO(), key, cmap)
	assert.NoError(t, err)
	// We don't change the owner in case of update
	assert.Empty(t, cmap.OwnerReferences)
	// We added one key-value but the other must stay in the config map
	assert.True(t, len(cmap.Data) > 1)

	currentSize := len(cmap.Data)

	// Update skipped
	err = helper.computeConfigMap(key, func(cmap *corev1.ConfigMap) bool {
		return false
	}, &owner)

	assert.NoError(t, err)

	cmap = &corev1.ConfigMap{}
	err = client.Get(context.TODO(), key, cmap)
	assert.NoError(t, err)
	// The size of data must not change as there was no update
	assert.Len(t, cmap.Data, currentSize)

	// The only operation in history is the first update
	client.CheckNumberOfOperations(t, HItem(reflect.ValueOf(client.Update), cmap), 1)
}

func TestBuildService(t *testing.T) {
	mdb := DefaultReplicaSetBuilder().Build()
	svc := buildService(objectKey(TestNamespace, "my-svc"), mdb, "label", 2000, mdbv1.MongoDBOpsManagerServiceDefinition{
		Type:           corev1.ServiceTypeClusterIP,
		Port:           2000,
		LoadBalancerIP: "loadbalancerip",
	})

	assert.Len(t, svc.OwnerReferences, 1)
	assert.Equal(t, mdb.Name, svc.OwnerReferences[0].Name)
	assert.Equal(t, mdb.GetKind(), svc.OwnerReferences[0].Kind)
	assert.Equal(t, TestNamespace, svc.Namespace)
	assert.Equal(t, "my-svc", svc.Name)
	assert.Equal(t, "loadbalancerip", svc.Spec.LoadBalancerIP)
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
	assert.Equal(t, "label", svc.Labels[AppLabelKey])
}
