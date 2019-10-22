package operator

import (
	"context"
	"os"
	"testing"
	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
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
		SetLogger(zap.S()).
		SetPodSpec(defaultPodSpec()).
		SetPodVars(defaultPodVars()).
		SetService("test-service").
		SetSecurity(&mongodb.Security{
			TLSConfig: &mongodb.TLSConfig{},
			Authentication: &mongodb.Authentication{
				Modes: []string{},
			},
		})
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

func TestGetNamespaceAndNameForResource_WithNameAndNamespace(t *testing.T) {
	expectedNamespace, expectedName := "mytestnamespace", "mytestname"
	nsName, err := getNamespaceAndNameForResource(expectedName, expectedNamespace)
	assert.NoError(t, err)
	assert.Equal(t, expectedNamespace, nsName.Namespace)
	assert.Equal(t, expectedName, nsName.Name)
}

func TestGetNamespaceAndNameForResource_WithNamespacedName(t *testing.T) {
	expectedNamespace, expectedName := "mytestnamespace", "mytestname"
	nsName, err := getNamespaceAndNameForResource(expectedNamespace+"/"+expectedName, "irrelevant")
	assert.NoError(t, err)
	assert.Equal(t, expectedNamespace, nsName.Namespace)
	assert.Equal(t, expectedName, nsName.Name)
}

func TestGetNamespaceAndNameForResource_WithMultipleNamespaces(t *testing.T) {
	expectedNamespace, expectedName := "mytestnamespace", "mytestname"
	_, err := getNamespaceAndNameForResource(expectedNamespace+"/"+expectedNamespace+"/"+expectedName, "irrelevant")
	assert.Error(t, err)
}

func TestGetNamespaceAndNameForResource_WithEmptyNamespace(t *testing.T) {
	expectedNamespace, expectedName := "", "mytestname"
	_, err := getNamespaceAndNameForResource(expectedNamespace+"/"+expectedName, "irrelevant")
	assert.Error(t, err)
}

func TestGetNamespaceAndNameForResource_WithEmptyName(t *testing.T) {
	expectedNamespace, expectedName := "mytestnamespace", ""
	_, err := getNamespaceAndNameForResource(expectedNamespace+"/"+expectedName, "irrelevant")
	assert.Error(t, err)
}

func TestReadProjectConfig_WithInvalidNamespace(t *testing.T) {
	client := newMockedClient(nil)
	helper := KubeHelper{client: client}
	_, err := helper.readProjectConfig("irrelevant", TestProjectConfigMapName)
	assert.Error(t, err)
}

func TestReadProjectConfig_InDifferentNamespace(t *testing.T) {
	client := newMockedClient(nil)

	expectedBaseUrl, expectedProjectName, expectedOrgId := "http://mycompany.com:8080", "mytestproject", "org1234"
	project := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: TestProjectConfigMapName, Namespace: "mytestnamespace2"},
		Data: map[string]string{
			util.OmBaseUrl:     expectedBaseUrl,
			util.OmProjectName: expectedProjectName,
			util.OmOrgId:       expectedOrgId,
		}}
	client.Create(context.TODO(), project)

	helper := KubeHelper{client: client}
	actualProjectConfig, err := helper.readProjectConfig("irrelevant", project.ObjectMeta.Namespace+"/"+project.ObjectMeta.Name)
	assert.NoError(t, err)
	assert.Equal(t, expectedBaseUrl, actualProjectConfig.BaseURL)
	assert.Equal(t, expectedProjectName, actualProjectConfig.ProjectName)
	assert.Equal(t, expectedOrgId, actualProjectConfig.OrgID)
}

func TestReadCredentials_WithInvalidNamespace(t *testing.T) {
	client := newMockedClient(nil)
	helper := KubeHelper{client: client}
	_, err := helper.readCredentials("irrelevant", TestCredentialsSecretName)
	assert.Error(t, err)
}

func TestReadCredentials_InDifferentNamespace(t *testing.T) {
	client := newMockedClient(nil)

	expectedUser, expectedApiKey := "test@mycompany.com", "36lj245asg06s0h70245dstgft"
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TestCredentialsSecretName, Namespace: "mytestnamespace2"},
		StringData: map[string]string{util.OmUser: expectedUser, util.OmPublicApiKey: expectedApiKey}}
	client.Create(context.TODO(), credentials)

	helper := KubeHelper{client: client}
	actualCredentials, err := helper.readCredentials("irrelevant", credentials.ObjectMeta.Namespace+"/"+credentials.ObjectMeta.Name)
	assert.NoError(t, err)
	assert.Equal(t, expectedUser, actualCredentials.User)
	assert.Equal(t, expectedApiKey, actualCredentials.PublicAPIKey)
}
