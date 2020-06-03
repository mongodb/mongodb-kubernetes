package operator

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/project"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube/configmap"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
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

func TestSSLOptionsArePassedCorrectly_SSLRequireValidMMSServerCertificates(t *testing.T) {
	client := mock.NewClient()

	cm := defaultConfigMap("cm1")
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "true"
	client.Create(context.TODO(), &cm)

	projectConfig, err := project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm1"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")

	cm = defaultConfigMap("cm2")
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "1"
	client.Create(context.TODO(), &cm)

	projectConfig, err = project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm2"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")

	cm = defaultConfigMap("cm3")
	// Setting this attribute to "false" will make it false, any other
	// value will result in this attribute being set to true.
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "false"
	client.Create(context.TODO(), &cm)

	projectConfig, err = project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm3"), "")

	assert.NoError(t, err)
	assert.False(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")
}

func TestSSLOptionsArePassedCorrectly_SSLMMSCAConfigMap(t *testing.T) {
	client := mock.NewClient()

	// This represents the ConfigMap holding the CustomCA
	cm := defaultConfigMap("configmap-with-ca-entry")
	cm.Data["mms-ca.crt"] = "---- some cert ----"
	cm.Data["this-field-is-not-required"] = "bla bla"
	client.Create(context.TODO(), &cm)

	// The second CM (the "Project" one) refers to the previous one, where
	// the certificate entry is stored.
	cm = defaultConfigMap("cm")
	cm.Data[util.SSLMMSCAConfigMap] = "configmap-with-ca-entry"
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "false"
	client.Create(context.TODO(), &cm)

	projectConfig, err := project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm"), "")

	assert.NoError(t, err)
	assert.False(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "configmap-with-ca-entry")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "---- some cert ----")
}

func TestSSLOptionsArePassedCorrectly_UseCustomCAConfigMap(t *testing.T) {
	client := mock.NewClient()

	// Passing "false" results in false to UseCustomCA
	cm := defaultConfigMap("cm")
	cm.Data[util.UseCustomCAConfigMap] = "false"
	client.Create(context.TODO(), &cm)

	projectConfig, err := project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm"), "")

	assert.NoError(t, err)
	assert.False(t, projectConfig.UseCustomCA)

	// Passing "true" results in true to UseCustomCA
	cm = defaultConfigMap("cm2")
	cm.Data[util.UseCustomCAConfigMap] = "true"
	client.Create(context.TODO(), &cm)

	projectConfig, err = project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm2"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// Passing any value different than "false" results in true.
	cm = defaultConfigMap("cm3")
	cm.Data[util.UseCustomCAConfigMap] = ""
	client.Create(context.TODO(), &cm)

	projectConfig, err = project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm3"), "")
	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// "1" also results in a true value
	cm = defaultConfigMap("cm4")
	cm.Data[util.UseCustomCAConfigMap] = "1"
	client.Create(context.TODO(), &cm)

	projectConfig, err = project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm4"), "")
	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// This last section only tests that the unit test is working fine
	// and having multiple ConfigMaps in the mocked client will not
	// result in contaminated checks.
	cm = defaultConfigMap("cm5")
	cm.Data[util.UseCustomCAConfigMap] = "false"
	client.Create(context.TODO(), &cm)

	projectConfig, err = project.ReadProjectConfig(client, objectKey(mock.TestNamespace, "cm5"), "")
	assert.NoError(t, err)
	assert.False(t, projectConfig.UseCustomCA)
}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	defer InitDefaultEnvVariables()

	os.Setenv(util.AutomationAgentImage, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
	InitDefaultEnvVariables()

	os.Setenv(util.AutomationAgentImagePullPolicy, "")
	assert.Panics(t, func() { defaultSetHelper().CreateOrUpdateInKubernetes() })
}

// TestComputeConfigMap_CreateNew checks the "create" features of 'computeConfigMap' function when the configmap is created
// if it doesn't exist (or the creation is skipped totally)
func TestComputeConfigMap_CreateNew(t *testing.T) {
	client := mock.NewClient()
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
	client := mock.NewClient()
	client.AddProjectConfigMap(om.TestGroupName, "")
	helper := NewKubeHelper(client)
	owner := mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

	key := objectKey(mock.TestNamespace, mock.TestProjectConfigMapName)

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
	client.CheckNumberOfOperations(t, mock.HItem(reflect.ValueOf(client.Update), cmap), 1)
}

func TestBuildService(t *testing.T) {
	mdb := DefaultReplicaSetBuilder().Build()
	svc := buildService(objectKey(mock.TestNamespace, "my-svc"), mdb, "label", 2000, mdbv1.MongoDBOpsManagerServiceDefinition{
		Type:           corev1.ServiceTypeClusterIP,
		Port:           2000,
		LoadBalancerIP: "loadbalancerip",
	})

	assert.Len(t, svc.OwnerReferences, 1)
	assert.Equal(t, mdb.Name, svc.OwnerReferences[0].Name)
	assert.Equal(t, mdb.GetObjectKind().GroupVersionKind().Kind, svc.OwnerReferences[0].Kind)
	assert.Equal(t, mock.TestNamespace, svc.Namespace)
	assert.Equal(t, "my-svc", svc.Name)
	assert.Equal(t, "loadbalancerip", svc.Spec.LoadBalancerIP)
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
	assert.Equal(t, "label", svc.Labels[AppLabelKey])
}

func defaultConfigMap(name string) corev1.ConfigMap {
	return configmap.Builder().
		SetName(name).
		SetNamespace(mock.TestNamespace).
		SetField(util.OmBaseUrl, "http://mycompany.com:8080").
		SetField(util.OmProjectName, "my-name").
		Build()
}
