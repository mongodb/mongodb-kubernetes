package project

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	corev1 "k8s.io/api/core/v1"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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
	assert.True(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "")

	cm = defaultConfigMap("cm2")
	cm.Data[util.SSLRequireValidMMSServerCertificates] = "1"
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm2"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

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
	assert.False(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

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
	assert.False(t, projectConfig.SSLProjectConfig.SSLRequireValidMMSServerCertificates)

	assert.Equal(t, projectConfig.SSLMMSCAConfigMap, "configmap-with-ca-entry")
	assert.Equal(t, projectConfig.SSLMMSCAConfigMapContents, "---- some cert ----")
}

func TestSSLOptionsArePassedCorrectly_UseCustomCAConfigMap(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	// Passing "false" results in false to UseCustomCA
	cm := defaultConfigMap("cm")
	cm.Data[util.UseCustomCAConfigMap] = "false"
	err := client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err := ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm"), "")

	assert.NoError(t, err)
	assert.False(t, projectConfig.UseCustomCA)

	// Passing "true" results in true to UseCustomCA
	cm = defaultConfigMap("cm2")
	cm.Data[util.UseCustomCAConfigMap] = "true"
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm2"), "")

	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// Passing any value different from "false" results in true.
	cm = defaultConfigMap("cm3")
	cm.Data[util.UseCustomCAConfigMap] = ""
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm3"), "")
	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// "1" also results in a true value
	cm = defaultConfigMap("cm4")
	cm.Data[util.UseCustomCAConfigMap] = "1"
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm4"), "")
	assert.NoError(t, err)
	assert.True(t, projectConfig.UseCustomCA)

	// This last section only tests that the unit test is working fine
	// and having multiple ConfigMaps in the mocked client will not
	// result in contaminated checks.
	cm = defaultConfigMap("cm5")
	cm.Data[util.UseCustomCAConfigMap] = "false"
	err = client.Create(ctx, &cm)
	assert.NoError(t, err)

	projectConfig, err = ReadProjectConfig(ctx, client, kube.ObjectKey(mock.TestNamespace, "cm5"), "")
	assert.NoError(t, err)
	assert.False(t, projectConfig.UseCustomCA)
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
