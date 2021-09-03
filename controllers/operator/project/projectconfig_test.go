package project

import (
	"context"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestSSLOptionsArePassedCorrectly_SSLRequireValidMMSServerCertificates(t *testing.T) {
	client := mock.NewClient()

	cm := defaultConfigMap("cm1")
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "true"
	client.Create(context.TODO(), &cm)

	projectConfig, err := ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm1"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")

	cm = defaultConfigMap("cm2")
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "1"
	client.Create(context.TODO(), &cm)

	projectConfig, err = ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm2"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")

	cm = defaultConfigMap("cm3")
	// Setting this attribute to "false" will make it false, any other
	// value will result in this attribute being set to true.
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "false"
	client.Create(context.TODO(), &cm)

	projectConfig, err = ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm3"), "")

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

	projectConfig, err := ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm"), "")

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

	projectConfig, err := ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm"), "")

	assert.NoError(t, err)
	assert.False(t, projectConfig.UseCustomCA)

	// Passing "true" results in true to UseCustomCA
	cm = defaultConfigMap("cm2")
	cm.Data[util.UseCustomCAConfigMap] = "true"
	client.Create(context.TODO(), &cm)

	projectConfig, err = ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm2"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// Passing any value different than "false" results in true.
	cm = defaultConfigMap("cm3")
	cm.Data[util.UseCustomCAConfigMap] = ""
	client.Create(context.TODO(), &cm)

	projectConfig, err = ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm3"), "")
	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// "1" also results in a true value
	cm = defaultConfigMap("cm4")
	cm.Data[util.UseCustomCAConfigMap] = "1"
	client.Create(context.TODO(), &cm)

	projectConfig, err = ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm4"), "")
	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// This last section only tests that the unit test is working fine
	// and having multiple ConfigMaps in the mocked client will not
	// result in contaminated checks.
	cm = defaultConfigMap("cm5")
	cm.Data[util.UseCustomCAConfigMap] = "false"
	client.Create(context.TODO(), &cm)

	projectConfig, err = ReadProjectConfig(client, kube.ObjectKey(mock.TestNamespace, "cm5"), "")
	assert.NoError(t, err)
	assert.False(t, projectConfig.UseCustomCA)
}

func defaultConfigMap(name string) corev1.ConfigMap {
	return configmap.Builder().
		SetName(name).
		SetNamespace(mock.TestNamespace).
		SetDataField(util.OmBaseUrl, "http://mycompany.com:8080").
		SetDataField(util.OmProjectName, "my-name").
		Build()
}
