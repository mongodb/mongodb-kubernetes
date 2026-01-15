package searchcontroller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ghodss/yaml"
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
			EmbeddingModelAPIKeySecret: corev1.LocalObjectReference{
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
		Embedding: &mongot.EmbeddingConfig{
			ProviderEndpoint:          providerEndpoint,
			IsAutoEmbeddingViewWriter: &embeddingWriterTrue,
			QueryKeyFile:              fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName),
			IndexingKeyFile:           fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName),
		},
	}

	ctx := context.TODO()
	fakeClient := newTestFakeClient(search, apiKeySecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
		SearchVersion: "0.58.0",
	})
	mongotModif, stsModif, err := helper.ensureEmbeddingConfig(ctx, nil)
	assert.Nil(t, err)

	mongotModif(conf)
	stsModif(sts)

	assert.Equal(t, expectedVolumeMount, sts.Spec.Template.Spec.Containers[0].VolumeMounts)
	assert.Equal(t, expectedVolumes, sts.Spec.Template.Spec.Volumes)
	assert.Equal(t, expectedMongotConfig.Embedding, conf.Embedding)
}

func TestEnsureEmbeddingConfig_WOAutoEmbedding(t *testing.T) {
	mongotCMWithoutEmbedding := `healthCheck:
  address: ""
logging:
  verbosity: ""
metrics:
  address: ""
  enabled: false
server: {}
storage:
  dataPath: ""
syncSource:
  replicaSet:
    hostAndPort: null
    passwordFile: ""
    username: ""`

	search := newTestMongoDBSearch("mdb-searh", "mongodb")
	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
		SearchVersion: "0.58.0",
	})
	ctx := context.TODO()
	mongotModif, stsModif, err := helper.ensureEmbeddingConfig(ctx, nil)
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

	// verify that if the embedding config is not provided in the CR, the mongot config's YAML representation
	// doesn't have even zero values of embedding config.
	cm, err := yaml.Marshal(conf)
	assert.Nil(t, err)
	assert.YAMLEq(t, mongotCMWithoutEmbedding, string(cm))

	// because search CR didn't have autoEmbedding configured, there wont be any change in conf or sts
	assert.Equal(t, sts, sts)
	assert.Equal(t, conf, conf)
}

func TestEnsureEmbeddingConfig_JustAPIKeys(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{
			EmbeddingModelAPIKeySecret: corev1.LocalObjectReference{
				Name: testApiKeySecretName,
			},
		}
	})
	fakeClient := newTestFakeClient(search, apiKeySecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
		SearchVersion: "0.58.0",
	})
	ctx := context.TODO()
	mongotModif, stsModif, err := helper.ensureEmbeddingConfig(ctx, nil)
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
	assert.Equal(t, &mongot.EmbeddingConfig{
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
			EmbeddingModelAPIKeySecret: corev1.LocalObjectReference{
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
		_, _, err := helper.ensureEmbeddingConfig(ctx, nil)
		tc.errAssertion(t, err)
		if tc.errMsg != "" {
			assert.Equal(t, tc.errMsg, err.Error())
		}
	}
}

func TestGetMongodConfigParametersForShard(t *testing.T) {
	tests := []struct {
		name          string
		search        *searchv1.MongoDBSearch
		shardName     string
		clusterDomain string
		expectedHost  string
		useExternalLB bool
	}{
		{
			name: "Internal service endpoint (no external LB)",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
				},
			},
			shardName:     "test-mdb-0",
			clusterDomain: "cluster.local",
			expectedHost:  "test-search-mongot-test-mdb-0-svc.test-ns.svc.cluster.local:27028",
			useExternalLB: false,
		},
		{
			name: "External LB endpoint for shard",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Mode: searchv1.LBModeExternal,
						External: &searchv1.ExternalLBConfig{
							Sharded: &searchv1.ShardedExternalLBConfig{
								Endpoints: []searchv1.ShardEndpoint{
									{ShardName: "test-mdb-0", Endpoint: "lb-shard0.example.com:27028"},
									{ShardName: "test-mdb-1", Endpoint: "lb-shard1.example.com:27028"},
								},
							},
						},
					},
				},
			},
			shardName:     "test-mdb-0",
			clusterDomain: "cluster.local",
			expectedHost:  "lb-shard0.example.com:27028",
			useExternalLB: true,
		},
		{
			name: "External LB endpoint for second shard",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Mode: searchv1.LBModeExternal,
						External: &searchv1.ExternalLBConfig{
							Sharded: &searchv1.ShardedExternalLBConfig{
								Endpoints: []searchv1.ShardEndpoint{
									{ShardName: "test-mdb-0", Endpoint: "lb-shard0.example.com:27028"},
									{ShardName: "test-mdb-1", Endpoint: "lb-shard1.example.com:27028"},
								},
							},
						},
					},
				},
			},
			shardName:     "test-mdb-1",
			clusterDomain: "cluster.local",
			expectedHost:  "lb-shard1.example.com:27028",
			useExternalLB: true,
		},
		{
			name: "Fallback to internal when shard not in endpoint map",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Mode: searchv1.LBModeExternal,
						External: &searchv1.ExternalLBConfig{
							Sharded: &searchv1.ShardedExternalLBConfig{
								Endpoints: []searchv1.ShardEndpoint{
									{ShardName: "test-mdb-0", Endpoint: "lb-shard0.example.com:27028"},
								},
							},
						},
					},
				},
			},
			shardName:     "test-mdb-2", // Not in endpoint map
			clusterDomain: "cluster.local",
			expectedHost:  "test-search-mongot-test-mdb-2-svc.test-ns.svc.cluster.local:27028",
			useExternalLB: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := GetMongodConfigParametersForShard(tc.search, tc.shardName, tc.clusterDomain)

			setParameter, ok := config["setParameter"].(map[string]any)
			require.True(t, ok, "setParameter should be a map")

			mongotHost, ok := setParameter["mongotHost"].(string)
			require.True(t, ok, "mongotHost should be a string")
			assert.Equal(t, tc.expectedHost, mongotHost)

			searchIndexHost, ok := setParameter["searchIndexManagementHostAndPort"].(string)
			require.True(t, ok, "searchIndexManagementHostAndPort should be a string")
			assert.Equal(t, tc.expectedHost, searchIndexHost)
		})
	}
}

func TestMongoDBSearch_LBHelperMethods(t *testing.T) {
	tests := []struct {
		name                string
		search              *searchv1.MongoDBSearch
		expectExternalLB    bool
		expectShardedLB     bool
		expectedEndpointMap map[string]string
		expectedReplicas    int
	}{
		{
			name: "No LB config",
			search: &searchv1.MongoDBSearch{
				Spec: searchv1.MongoDBSearchSpec{},
			},
			expectExternalLB:    false,
			expectShardedLB:     false,
			expectedEndpointMap: map[string]string{},
			expectedReplicas:    1,
		},
		{
			name: "Envoy mode",
			search: &searchv1.MongoDBSearch{
				Spec: searchv1.MongoDBSearchSpec{
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Mode: searchv1.LBModeEnvoy,
					},
				},
			},
			expectExternalLB:    false,
			expectShardedLB:     false,
			expectedEndpointMap: map[string]string{},
			expectedReplicas:    1,
		},
		{
			name: "External mode with single endpoint",
			search: &searchv1.MongoDBSearch{
				Spec: searchv1.MongoDBSearchSpec{
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Mode: searchv1.LBModeExternal,
						External: &searchv1.ExternalLBConfig{
							Endpoint: "lb.example.com:27028",
						},
					},
				},
			},
			expectExternalLB:    true,
			expectShardedLB:     false,
			expectedEndpointMap: map[string]string{},
			expectedReplicas:    1,
		},
		{
			name: "External mode with sharded endpoints",
			search: &searchv1.MongoDBSearch{
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						Replicas: 1,
					},
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Mode: searchv1.LBModeExternal,
						External: &searchv1.ExternalLBConfig{
							Sharded: &searchv1.ShardedExternalLBConfig{
								Endpoints: []searchv1.ShardEndpoint{
									{ShardName: "shard-0", Endpoint: "lb0.example.com:27028"},
									{ShardName: "shard-1", Endpoint: "lb1.example.com:27028"},
								},
							},
						},
					},
				},
			},
			expectExternalLB: true,
			expectShardedLB:  true,
			expectedEndpointMap: map[string]string{
				"shard-0": "lb0.example.com:27028",
				"shard-1": "lb1.example.com:27028",
			},
			expectedReplicas: 1,
		},
		{
			name: "Custom replicas",
			search: &searchv1.MongoDBSearch{
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						Replicas: 3,
					},
				},
			},
			expectExternalLB:    false,
			expectShardedLB:     false,
			expectedEndpointMap: map[string]string{},
			expectedReplicas:    3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectExternalLB, tc.search.IsExternalLBMode())
			assert.Equal(t, tc.expectShardedLB, tc.search.IsShardedExternalLB())
			assert.Equal(t, tc.expectedEndpointMap, tc.search.GetShardEndpointMap())
			assert.Equal(t, tc.expectedReplicas, tc.search.GetReplicas())
		})
	}
}

// TestCreateShardMongotConfig tests the createShardMongotConfig function
// which creates mongot configuration for a specific shard.
func TestCreateShardMongotConfig(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
			Mode: searchv1.LBModeExternal,
			External: &searchv1.ExternalLBConfig{
				Sharded: &searchv1.ShardedExternalLBConfig{
					Endpoints: []searchv1.ShardEndpoint{
						{ShardName: "my-cluster-0", Endpoint: "lb0.example.com:27028"},
						{ShardName: "my-cluster-1", Endpoint: "lb1.example.com:27028"},
					},
				},
			},
		}
	})

	// Create a mock sharded source
	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[int][]string{
			0: {"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"},
			1: {"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"},
		},
	}

	// Test shard 0
	config := mongot.Config{}
	createShardMongotConfig(search, shardedSource, 0)(&config)

	assert.Equal(t, []string{"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"}, config.SyncSource.ReplicaSet.HostAndPort)
	assert.Equal(t, search.SourceUsername(), config.SyncSource.ReplicaSet.Username)

	// Test shard 1
	config2 := mongot.Config{}
	createShardMongotConfig(search, shardedSource, 1)(&config2)

	assert.Equal(t, []string{"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"}, config2.SyncSource.ReplicaSet.HostAndPort)
}

// mockShardedSource is a mock implementation of ShardedSearchSourceDBResource for testing
type mockShardedSource struct {
	shardNames []string
	hostSeeds  map[int][]string
}

func (m *mockShardedSource) GetShardCount() int {
	return len(m.shardNames)
}

func (m *mockShardedSource) GetShardNames() []string {
	return m.shardNames
}

func (m *mockShardedSource) HostSeedsForShard(shardIdx int) []string {
	return m.hostSeeds[shardIdx]
}

func (m *mockShardedSource) GetExternalLBEndpointForShard(shardName string) string {
	return ""
}

// Implement SearchSourceDBResource interface
func (m *mockShardedSource) HostSeeds() []string {
	return nil
}

func (m *mockShardedSource) Validate() error {
	return nil
}

func (m *mockShardedSource) KeyfileSecretName() string {
	return ""
}

func (m *mockShardedSource) TLSConfig() *TLSSourceConfig {
	return nil
}

// TestBuildShardSearchHeadlessService tests the buildShardSearchHeadlessService function
func TestBuildShardSearchHeadlessService(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test")
	shardName := "my-cluster-0"

	svc := buildShardSearchHeadlessService(search, shardName)

	assert.Equal(t, "test-search-mongot-my-cluster-0-svc", svc.Name)
	assert.Equal(t, "test", svc.Namespace)
	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
	assert.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)

	// Check selector points to the shard StatefulSet
	assert.Equal(t, "test-search-mongot-my-cluster-0", svc.Spec.Selector["app"])

	// Check ports
	var grpcPort, healthPort *corev1.ServicePort
	for i := range svc.Spec.Ports {
		switch svc.Spec.Ports[i].Name {
		case "mongot-grpc":
			grpcPort = &svc.Spec.Ports[i]
		case "healthcheck":
			healthPort = &svc.Spec.Ports[i]
		}
	}

	require.NotNil(t, grpcPort, "grpc port should exist")
	assert.Equal(t, int32(27028), grpcPort.Port)

	require.NotNil(t, healthPort, "healthcheck port should exist")
	assert.Equal(t, int32(8080), healthPort.Port)
}
