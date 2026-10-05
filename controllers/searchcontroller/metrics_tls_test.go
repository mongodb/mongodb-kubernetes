package searchcontroller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/pkg/mongot"
)

func newMetricsTLSSearch(mods ...func(*searchv1.MongoDBSearch)) *searchv1.MongoDBSearch {
	return newTestMongoDBSearch("search", "ns", append([]func(*searchv1.MongoDBSearch){
		func(s *searchv1.MongoDBSearch) {
			s.Spec.Version = minMetricsTLSMongotVersion
			s.Spec.Observability.Prometheus.TLS = &searchv1.PrometheusTLS{
				ServerCertificateSecretRef: corev1.LocalObjectReference{Name: "metrics-server"},
				ClientCAConfigMapRef:       corev1.LocalObjectReference{Name: "metrics-client-ca"},
			}
		},
	}, mods...)...)
}

func TestEnsureMetricsTlsConfig(t *testing.T) {
	search := newMetricsTLSSearch()
	serverSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-server", Namespace: "ns"},
		Data:       map[string][]byte{"tls.crt": []byte("CERT"), "tls.key": []byte("KEY")},
	}
	clientCA := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-client-ca", Namespace: "ns"},
		Data:       map[string]string{"ca.crt": "CA"},
	}
	c := newTestFakeClient(serverSecret, clientCA)
	helper := NewMongoDBSearchReconcileHelper(c, search, nil, newTestOperatorSearchConfig(), nil, "", nil)

	mongotMod, stsMod, err := helper.ensureMetricsTlsConfig(context.Background(), c, map[string]string{}, nil)
	require.NoError(t, err)

	config := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, nil), mongotMod)(&config)
	require.NotNil(t, config.Metrics.TLS)
	assert.True(t, config.Metrics.Enabled)
	assert.Contains(t, ptr.Deref(config.Metrics.TLS.CertificateKeyFile, ""), MetricsServerCertOperatorMountPath)
	assert.Equal(t, MetricsClientCAConfigMapMountPath+tlsCACertName, ptr.Deref(config.Metrics.TLS.CAFile, ""))
	assert.Nil(t, config.Metrics.TLS.CertificateKeyFilePasswordFile)

	// The operator-managed combined PEM Secret is created with the cert+key.
	operatorSecret := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(), search.MetricsServerOperatorSecret(), operatorSecret))
	require.Len(t, operatorSecret.Data, 1)
	for _, v := range operatorSecret.Data {
		assert.Contains(t, string(v), "CERT")
		assert.Contains(t, string(v), "KEY")
	}

	// Volumes and mounts are added read-only.
	sts := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: MongotContainerName, Args: []string{"-c", "echo"}}}}}}}
	stsMod(sts)
	volumes := map[string]bool{}
	for _, v := range sts.Spec.Template.Spec.Volumes {
		volumes[v.Name] = true
	}
	assert.True(t, volumes["metrics-server-cert"], "server cert volume present")
	assert.True(t, volumes["metrics-client-ca"], "client CA volume present")
	mounts := map[string]bool{}
	for _, m := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
		mounts[m.Name] = true
	}
	assert.True(t, mounts["metrics-server-cert"])
	assert.True(t, mounts["metrics-client-ca"])
}

func TestEnsureMetricsTlsConfig_NoTLSPreservesHTTP(t *testing.T) {
	search := newTestMongoDBSearch("search", "ns")
	c := newTestFakeClient()
	helper := NewMongoDBSearchReconcileHelper(c, search, nil, newTestOperatorSearchConfig(), nil, "", nil)

	mongotMod, _, err := helper.ensureMetricsTlsConfig(context.Background(), c, map[string]string{}, nil)
	require.NoError(t, err)

	config := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, nil), mongotMod)(&config)
	assert.True(t, config.Metrics.Enabled)
	assert.Nil(t, config.Metrics.TLS)
}

func TestEnsureMetricsTlsConfig_RejectsDisabledPrometheus(t *testing.T) {
	search := newMetricsTLSSearch(func(s *searchv1.MongoDBSearch) {
		s.Spec.Observability.Prometheus.Mode = searchv1.PrometheusModeDisabled
	})
	c := newTestFakeClient()
	helper := NewMongoDBSearchReconcileHelper(c, search, nil, newTestOperatorSearchConfig(), nil, "", nil)

	_, _, err := helper.ensureMetricsTlsConfig(context.Background(), c, map[string]string{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode: enabled")
}

func TestEnsureMetricsTlsConfig_RejectsUnsupportedVersion(t *testing.T) {
	search := newMetricsTLSSearch(func(s *searchv1.MongoDBSearch) {
		s.Spec.Version = "1.70.1"
	})
	c := newTestFakeClient()
	helper := NewMongoDBSearchReconcileHelper(c, search, nil, newTestOperatorSearchConfig(), nil, "", nil)

	_, _, err := helper.ensureMetricsTlsConfig(context.Background(), c, map[string]string{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support Prometheus metrics TLS")
}

func TestEnsureMetricsTlsConfig_ServerCertRotationChangesPath(t *testing.T) {
	search := newMetricsTLSSearch()
	serverSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-server", Namespace: "ns"},
		Data:       map[string][]byte{"tls.crt": []byte("CERT1"), "tls.key": []byte("KEY1")},
	}
	clientCA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "metrics-client-ca", Namespace: "ns"}, Data: map[string]string{"ca.crt": "CA"}}
	c := newTestFakeClient(serverSecret, clientCA)
	helper := NewMongoDBSearchReconcileHelper(c, search, nil, newTestOperatorSearchConfig(), nil, "", nil)

	mod1, _, err := helper.ensureMetricsTlsConfig(context.Background(), c, map[string]string{}, nil)
	require.NoError(t, err)
	cfg1 := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, nil), mod1)(&cfg1)

	// Rotate the server certificate/key.
	updated := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "metrics-server", Namespace: "ns"}, updated))
	updated.Data = map[string][]byte{"tls.crt": []byte("CERT2"), "tls.key": []byte("KEY2")}
	require.NoError(t, c.Update(context.Background(), updated))

	mod2, _, err := helper.ensureMetricsTlsConfig(context.Background(), c, map[string]string{}, nil)
	require.NoError(t, err)
	cfg2 := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, nil), mod2)(&cfg2)

	assert.NotEqual(t, ptr.Deref(cfg1.Metrics.TLS.CertificateKeyFile, ""), ptr.Deref(cfg2.Metrics.TLS.CertificateKeyFile, ""),
		"rotating the server cert must change the mounted file path so the config hash (and pod template) changes")
}

func TestHashMountedContent_ChangesOnCARotation(t *testing.T) {
	search := newMetricsTLSSearch()
	clientCA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "metrics-client-ca", Namespace: "ns"}, Data: map[string]string{"ca.crt": "CA1"}}
	c := newTestFakeClient(clientCA)
	content := MountedContent{ConfigMaps: []types.NamespacedName{search.MetricsClientCAConfigMap()}}

	hash1, err := HashMountedContent(context.Background(), c, content)
	require.NoError(t, err)
	require.NotEmpty(t, hash1)

	updated := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "metrics-client-ca", Namespace: "ns"}, updated))
	updated.Data = map[string]string{"ca.crt": "CA2"}
	require.NoError(t, c.Update(context.Background(), updated))

	hash2, err := HashMountedContent(context.Background(), c, content)
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}
