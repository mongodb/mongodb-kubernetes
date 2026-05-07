package project

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestSSLOptionsArePassedCorrectly_SSLRequireValidMMSServerCertificates(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	cm := defaultConfigMap("cm1")
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "true"
	err := client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err := ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm1"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")

	cm = defaultConfigMap("cm2")
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "1"
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm2"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")

	cm = defaultConfigMap("cm3")
	// Setting this attribute to "false" will make it false, any other
	// value will result in this attribute being set to true.
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "false"
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm3"), "")

	assert.NoError(t, err)
	assert.False(t, projectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")
}

func TestSSLOptionsArePassedCorrectly_SSLMMSCAConfigMap(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	// This represents the ConfigMap holding the CustomCA
	cm := defaultConfigMap("configmap-with-ca-entry")
	cm.Data["mms-ca.crt"] = "---- some cert ----"
	cm.Data["this-field-is-not-required"] = "bla bla"
	err := client.Create(ctx, &cm)
	assert.NoError(t, err)

	// The second CM (the "Project" one) refers to the previous one, where
	// the certificate entry is stored.
	cm = defaultConfigMap("cm")
	cm.Data[util.SSLMMSCAConfigMap] = "configmap-with-ca-entry"
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "false"
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err := ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm"), "")

	assert.NoError(t, err)
	assert.False(t, projectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "configmap-with-ca-entry")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "---- some cert ----")
}

func TestMissingRequiredFieldsFromCM(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	t.Run("missing url", func(t *testing.T) {
		cm := defaultConfigMap("cm1")
		delete(cm.Data, util.OmBaseUrl)
		err := client.Create(ctx, &cm)
		assert.NoError(t, err)
		_, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm1"), "")
		assert.Error(t, err)
	})
	t.Run("missing orgID", func(t *testing.T) {
		cm := defaultConfigMap("cm1")
		delete(cm.Data, util.OmOrgId)
		err := client.Create(ctx, &cm)
		assert.NotNil(t, err)
		_, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm1"), "")
		assert.Error(t, err)
	})
}

func defaultConfigMap(name string) corev1.ConfigMap {
	return configmap.Builder().
		SetName(name).
		SetNamespace(mock.TestNamespace).
		SetDataField(util.OmBaseUrl, "http://mycompany.example.com:8080").
		SetDataField(util.OmOrgId, "123abc").
		SetDataField(util.OmProjectName, "my-name").
		Build()
}
