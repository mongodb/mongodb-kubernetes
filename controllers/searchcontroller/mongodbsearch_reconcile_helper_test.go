package searchcontroller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func newTestMongoDBSearch(name, namespace string, modifications ...func(*searchv1.MongoDBSearch)) *searchv1.MongoDBSearch {
	mdbSearch := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{
					Name: "test-mongodb",
				},
			},
		},
	}

	for _, modify := range modifications {
		modify(mdbSearch)
	}

	return mdbSearch
}

func newTestMongoDBCommunity(name, namespace string, modifications ...func(*mdbcv1.MongoDBCommunity)) *mdbcv1.MongoDBCommunity {
	mdbc := &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mdbcv1.MongoDBCommunitySpec{
			Version: "8.2.0",
			Members: 3,
		},
	}

	for _, modify := range modifications {
		modify(mdbc)
	}

	return mdbc
}

func newTestOperatorSearchConfig() OperatorSearchConfig {
	config := OperatorSearchConfig{
		SearchRepo:    "test-repo",
		SearchName:    "mongot",
		SearchVersion: "0.0.0",
	}

	return config
}

func newTestFakeClient(objects ...client.Object) kubernetesClient.Client {
	clientBuilder := mock.NewEmptyFakeClientBuilder()
	clientBuilder.WithIndex(&searchv1.MongoDBSearch{}, MongoDBSearchIndexFieldName, func(obj client.Object) []string {
		mdbResource := obj.(*searchv1.MongoDBSearch).GetMongoDBResourceRef()
		return []string{mdbResource.Namespace + "/" + mdbResource.Name}
	})
	clientBuilder.WithObjects(objects...)
	return kubernetesClient.NewClient(clientBuilder.Build())
}

func reconcileMongoDBSearch(ctx context.Context, fakeClient kubernetesClient.Client, mdbSearch *searchv1.MongoDBSearch, mdbc *mdbcv1.MongoDBCommunity, operatorConfig OperatorSearchConfig) workflow.Status {
	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		mdbSearch,
		NewCommunityResourceSearchSource(mdbc),
		operatorConfig,
	)

	return helper.Reconcile(ctx, zap.S())
}

func TestMongoDBSearchReconcileHelper_ValidateSingleMongoDBSearchForSearchSource(t *testing.T) {
	mdbSearchSpec := searchv1.MongoDBSearchSpec{
		Source: &searchv1.MongoDBSource{
			MongoDBResourceRef: &userv1.MongoDBResourceRef{
				Name: "test-mongodb",
			},
		},
	}

	mdbSearch := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb-search",
			Namespace: "test",
		},
		Spec: mdbSearchSpec,
	}

	mdbc := &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb",
			Namespace: "test",
		},
	}

	cases := []struct {
		name          string
		objects       []*searchv1.MongoDBSearch
		expectedError string
	}{
		{
			name: "No MongoDBSearch",
		},
		{
			name:    "Single MongoDBSearch",
			objects: []*searchv1.MongoDBSearch{mdbSearch},
		},
		{
			name: "Multiple MongoDBSearch",
			objects: []*searchv1.MongoDBSearch{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-mongodb-search-1",
						Namespace: "test",
					},
					Spec: mdbSearchSpec,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-mongodb-search-2",
						Namespace: "test",
					},
					Spec: mdbSearchSpec,
				},
			},
			expectedError: "Found multiple MongoDBSearch resources for search source 'test-mongodb': test-mongodb-search-1, test-mongodb-search-2",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clientBuilder := mock.NewEmptyFakeClientBuilder()
			clientBuilder.WithIndex(&searchv1.MongoDBSearch{}, MongoDBSearchIndexFieldName, func(obj client.Object) []string {
				mdbResource := obj.(*searchv1.MongoDBSearch).GetMongoDBResourceRef()
				return []string{mdbResource.Namespace + "/" + mdbResource.Name}
			})
			for _, v := range c.objects {
				// TODO: why doesn't clientBuilder.WithObjects(c.objects...) work?
				clientBuilder.WithObjects(v)
			}

			helper := NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(clientBuilder.Build()), mdbSearch, NewCommunityResourceSearchSource(mdbc), OperatorSearchConfig{})
			err := helper.ValidateSingleMongoDBSearchForSearchSource(t.Context())
			if c.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, c.expectedError)
			}
		})
	}
}

func TestGetMongodConfigParameters_TransportAndPorts(t *testing.T) {
	cases := []struct {
		name            string
		withWireproto   bool
		expectedUseGrpc bool
	}{
		{
			name:            "grpc only (default)",
			withWireproto:   false,
			expectedUseGrpc: true,
		},
		{
			name:            "grpc + wireproto via annotation",
			withWireproto:   true,
			expectedUseGrpc: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-mongodb-search",
					Namespace: "test",
				},
			}
			if tc.withWireproto {
				search.Annotations = map[string]string{searchv1.ForceWireprotoAnnotation: "true"}
			}

			clusterDomain := "cluster.local"
			params := GetMongodConfigParameters(search, clusterDomain)

			setParams := params["setParameter"].(map[string]any)

			useGrpc := setParams["useGrpcForSearch"].(bool)
			assert.Equal(t, tc.expectedUseGrpc, useGrpc)

			expectedPort := search.GetMongotGrpcPort()
			if tc.withWireproto {
				expectedPort = search.GetMongotWireprotoPort()
			}
			expectedPrefix := fmt.Sprintf("%s.%s.svc.%s", search.Name+"-search-svc", search.Namespace, clusterDomain)
			expectedSuffix := fmt.Sprintf(":%d", expectedPort)

			for _, key := range []string{"mongotHost", "searchIndexManagementHostAndPort"} {
				value := setParams[key].(string)
				if !strings.HasPrefix(value, expectedPrefix) || !strings.HasSuffix(value, expectedSuffix) {
					t.Fatalf("%s mismatch: expected prefix %q and suffix %q, got %q", key, expectedPrefix, expectedSuffix, value)
				}
			}
		})
	}
}

func assertServiceBasicProperties(t *testing.T, svc corev1.Service, mdbSearch *searchv1.MongoDBSearch) {
	t.Helper()
	svcName := mdbSearch.SearchServiceNamespacedName()

	assert.Equal(t, svcName.Name, svc.Name)
	assert.Equal(t, svcName.Namespace, svc.Namespace)
	assert.Equal(t, "ClusterIP", string(svc.Spec.Type))
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.False(t, svc.Spec.PublishNotReadyAddresses)

	expectedAppLabel := svcName.Name
	assert.Equal(t, expectedAppLabel, svc.Labels["app"])
	assert.Equal(t, expectedAppLabel, svc.Spec.Selector["app"])
}

func assertServicePorts(t *testing.T, svc corev1.Service, expectedPorts map[string]int32) {
	t.Helper()

	portMap := make(map[string]int32)
	for _, port := range svc.Spec.Ports {
		portMap[port.Name] = port.Port
	}

	assert.Len(t, svc.Spec.Ports, len(expectedPorts), "Expected %d ports but got %d", len(expectedPorts), len(svc.Spec.Ports))

	for portName, expectedPort := range expectedPorts {
		actualPort, exists := portMap[portName]
		assert.True(t, exists, "Expected port %s to exist", portName)
		assert.Equal(t, expectedPort, actualPort, "Port %s has wrong value", portName)
	}
}

func TestMongoDBSearchReconcileHelper_ServiceCreation(t *testing.T) {
	cases := []struct {
		name          string
		modifySearch  func(*searchv1.MongoDBSearch)
		expectedPorts map[string]int32
	}{
		{
			name: "Prometheus enabled with custom port",
			modifySearch: func(search *searchv1.MongoDBSearch) {
				search.Spec.Prometheus = &searchv1.Prometheus{
					Port: 9999,
				}
			},
			expectedPorts: map[string]int32{
				"mongot-grpc": searchv1.MongotDefaultGrpcPort,
				"prometheus":  9999,
				"healthcheck": searchv1.MongotDefautHealthCheckPort,
			},
		},
		{
			name: "Prometheus disabled",
			modifySearch: func(search *searchv1.MongoDBSearch) {
				search.Spec.Prometheus = nil
			},
			expectedPorts: map[string]int32{
				"mongot-grpc": searchv1.MongotDefaultGrpcPort,
				"healthcheck": searchv1.MongotDefautHealthCheckPort,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mdbSearch := newTestMongoDBSearch("test-mongodb-search", "test", tc.modifySearch)
			mdbc := newTestMongoDBCommunity("test-mongodb", "test")
			fakeClient := newTestFakeClient(mdbSearch, mdbc)

			reconcileMongoDBSearch(t.Context(), fakeClient, mdbSearch, mdbc, newTestOperatorSearchConfig())

			svcName := mdbSearch.SearchServiceNamespacedName()
			svc, err := fakeClient.GetService(t.Context(), svcName)
			require.NoError(t, err)
			require.NotNil(t, svc)

			assertServiceBasicProperties(t, svc, mdbSearch)
			assertServicePorts(t, svc, tc.expectedPorts)
		})
	}
}

var testApiKeySecretName = "api-key-secret"
var embeddingWriterTrue = true
var mode = int32(400)
var expectedVolumes = []corev1.Volume{
	{
		Name: embeddingKeyVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  testApiKeySecretName,
				DefaultMode: &mode,
			},
		},
	},
	{
		Name: apiKeysTempVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	},
}
var expectedVolumeMount = []corev1.VolumeMount{
	{
		Name:      apiKeysTempVolumeName,
		MountPath: embeddingKeyFilePath,
		ReadOnly:  false,
	},
	{
		Name:      embeddingKeyVolumeName,
		MountPath: apiKeysTempVolumeMount,
		ReadOnly:  true,
	},
}

var apiKeySecret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "api-key-secret",
		Namespace: "mongodb",
	},
	Data: map[string][]byte{
		"indexing-key": []byte(""),
		"query-key":    []byte(""),
	},
}

func TestEnsureEmbeddingConfig_APIKeySecretAndProviderEndpont(t *testing.T) {
	providerEndpoint := "https://api.voyageai.com/v1/embeddings"

	search := newTestMongoDBSearch("mdb-searh", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{
			ProviderEndpoint: providerEndpoint,
			EmbeddingModelAPIKeySecret: &corev1.LocalObjectReference{
				Name: testApiKeySecretName,
			},
		}
	})

	conf := &mongot.Config{}
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Args:         []string{"echo", "test"},
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}

	embeddingWriterTrue := true
	expectedMongotConfig := mongot.Config{
		Embedding: mongot.EmbeddingConfig{
			ProviderEndpoint:          providerEndpoint,
			IsAutoEmbeddingViewWriter: &embeddingWriterTrue,
			QueryKeyFile:              fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName),
			IndexingKeyFile:           fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName),
		},
	}

	ctx := context.TODO()
	fakeClient := newTestFakeClient(search, apiKeySecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{})
	mongotModif, stsModif, _, err := helper.ensureEmbeddingConfig(ctx)
	assert.Nil(t, err)

	mongotModif(conf)
	stsModif(sts)

	assert.Equal(t, expectedVolumeMount, sts.Spec.Template.Spec.Containers[0].VolumeMounts)
	assert.Equal(t, expectedVolumes, sts.Spec.Template.Spec.Volumes)
	assert.Equal(t, expectedMongotConfig.Embedding, conf.Embedding)
}

func TestEnsureEmbeddingConfig_WOAutoEmbedding(t *testing.T) {
	search := newTestMongoDBSearch("mdb-searh", "mongodb")
	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{})
	ctx := context.TODO()
	mongotModif, stsModif, _, err := helper.ensureEmbeddingConfig(ctx)
	assert.Nil(t, err)

	conf := &mongot.Config{}
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Args:         []string{"echo", "test"},
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}

	mongotModif(conf)
	stsModif(sts)

	// because search CR didn't have autoEmbedding configured, there wont be any change in conf or sts
	assert.Equal(t, sts, sts)
	assert.Equal(t, conf, conf)
}

func TestEnsureEmbeddingConfig_JustAPIKeys(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{
			EmbeddingModelAPIKeySecret: &corev1.LocalObjectReference{
				Name: testApiKeySecretName,
			},
		}
	})
	fakeClient := newTestFakeClient(search, apiKeySecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{})
	ctx := context.TODO()
	mongotModif, stsModif, _, err := helper.ensureEmbeddingConfig(ctx)
	assert.Nil(t, err)

	conf := &mongot.Config{}
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Args:         []string{"echo", "test"},
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}

	mongotModif(conf)
	stsModif(sts)

	// We are just providing the autoEmbedding API Key secret, that's why we will only see that in the config
	// and we will see the volumes, mounts in sts
	assert.Equal(t, mongot.EmbeddingConfig{
		QueryKeyFile:              fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName),
		IndexingKeyFile:           fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName),
		IsAutoEmbeddingViewWriter: &embeddingWriterTrue,
		ProviderEndpoint:          "",
	}, conf.Embedding)

	assert.Equal(t, expectedVolumeMount, sts.Spec.Template.Spec.Containers[0].VolumeMounts)
	assert.Equal(t, expectedVolumes, sts.Spec.Template.Spec.Volumes)
}

func TestValidateSearchResource(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{
			EmbeddingModelAPIKeySecret: &corev1.LocalObjectReference{
				Name: testApiKeySecretName,
			},
		}
	})
	ctx := context.TODO()
	for _, tc := range []struct {
		apiKeySecret  *corev1.Secret
		errAssertion  assert.ErrorAssertionFunc
		errMsg        string
		searchVersion string
	}{
		{
			apiKeySecret: &corev1.Secret{},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("secrets \"%s\" not found", testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: testApiKeySecretName,
				},
			},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("secrets \"%s\" not found", testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
			},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("Required key \"%s\" is not present in the Secret mongodb/%s", indexingKeyName, testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
				Data: map[string][]byte{
					"indexing-key": []byte(""),
				},
			},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("Required key \"%s\" is not present in the Secret mongodb/%s", queryKeyName, testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
				Data: map[string][]byte{
					"indexing-key": []byte(""),
					"query-key":    []byte(""),
				},
			},
			errAssertion:  assert.Error,
			searchVersion: "0.55.0",
			errMsg:        "The MongoDB search version 0.55.0 doesn't support auto embeddings. Please use version 0.58.0 or newer.",
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
				Data: map[string][]byte{
					"indexing-key": []byte(""),
					"query-key":    []byte(""),
				},
			},
			errAssertion:  assert.NoError,
			searchVersion: "0.58.0",
			errMsg:        "",
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
				Data: map[string][]byte{
					"indexing-key": []byte(""),
					"query-key":    []byte(""),
				},
			},
			errAssertion:  assert.NoError,
			searchVersion: "1.58.0",
			errMsg:        "",
		},
	} {
		fakeClient := newTestFakeClient(search, tc.apiKeySecret)
		helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
			SearchVersion: tc.searchVersion,
		})
		_, err := helper.validateSearchResource(ctx)
		tc.errAssertion(t, err)
		if tc.errMsg != "" {
			assert.Equal(t, tc.errMsg, err.Error())
		}
	}
}
