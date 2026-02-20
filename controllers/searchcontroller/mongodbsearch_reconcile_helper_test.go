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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
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
		SearchVersion: "0.60.0",
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
		SearchVersion: "0.60.0",
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
			errMsg:        "The MongoDB search version 0.55.0 doesn't support auto embeddings. Please use version 0.60.0 or newer.",
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
			searchVersion: "0.58.0",
			errMsg:        "The MongoDB search version 0.58.0 doesn't support auto embeddings. Please use version 0.60.0 or newer.",
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
	search := &searchv1.MongoDBSearch{
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
	}

	config := GetMongodConfigParametersForShard(search, "test-mdb-0", "cluster.local")

	setParameter, ok := config["setParameter"].(map[string]any)
	require.True(t, ok)

	mongotHost, ok := setParameter["mongotHost"].(string)
	require.True(t, ok)
	assert.Equal(t, "test-search-mongot-test-mdb-0-svc.test-ns.svc.cluster.local:27028", mongotHost)

	searchIndexHost, ok := setParameter["searchIndexManagementHostAndPort"].(string)
	require.True(t, ok)
	assert.Equal(t, "test-search-mongot-test-mdb-0-svc.test-ns.svc.cluster.local:27028", searchIndexHost)
}


func TestCreateShardMongotConfig(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test")

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[int][]string{
			0: {"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"},
			1: {"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"},
		},
	}

	config := mongot.Config{}
	createShardMongotConfig(search, shardedSource, 0)(&config)

	assert.Equal(t, []string{"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"}, config.SyncSource.ReplicaSet.HostAndPort)
	assert.Equal(t, search.SourceUsername(), config.SyncSource.ReplicaSet.Username)

	config2 := mongot.Config{}
	createShardMongotConfig(search, shardedSource, 1)(&config2)

	assert.Equal(t, []string{"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"}, config2.SyncSource.ReplicaSet.HostAndPort)
}

func TestShardedMongotConfigWithTLS(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test")

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[int][]string{
			0: {"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"},
			1: {"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"},
		},
		tlsConfig: &TLSSourceConfig{
			CAFileName: "ca-pem",
		},
	}

	config := mongot.Config{}
	createShardMongotConfig(search, shardedSource, 0)(&config)

	assert.NotNil(t, config.SyncSource.ReplicaSet.TLS)
	assert.False(t, *config.SyncSource.ReplicaSet.TLS, "ReplicaSet TLS should initially be false")
	assert.NotNil(t, config.SyncSource.Router)
	assert.NotNil(t, config.SyncSource.Router.TLS)
	assert.False(t, *config.SyncSource.Router.TLS, "Router TLS should initially be false")

	// Simulate what ensureEgressTlsConfig does when TLS is enabled
	tlsSourceConfig := shardedSource.TLSConfig()
	assert.NotNil(t, tlsSourceConfig, "TLS config should not be nil")

	// Apply the TLS modification (simulating ensureEgressTlsConfig behavior)
	config.SyncSource.ReplicaSet.TLS = ptr.To(true)
	config.SyncSource.CertificateAuthorityFile = ptr.To("/mongodb-automation/ca/" + tlsSourceConfig.CAFileName)
	if config.SyncSource.Router != nil {
		config.SyncSource.Router.TLS = ptr.To(true)
	}

	assert.True(t, *config.SyncSource.ReplicaSet.TLS, "ReplicaSet TLS should be enabled")
	assert.NotNil(t, config.SyncSource.CertificateAuthorityFile)
	assert.Equal(t, "/mongodb-automation/ca/ca-pem", *config.SyncSource.CertificateAuthorityFile)
	assert.True(t, *config.SyncSource.Router.TLS, "Router TLS should be enabled for sharded clusters")
}

func TestShardedMongotConfigWithoutTLS(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test")

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0"},
		hostSeeds: map[int][]string{
			0: {"my-cluster-0-0.svc:27017"},
		},
		tlsConfig: nil, // No TLS
	}

	config := mongot.Config{}
	createShardMongotConfig(search, shardedSource, 0)(&config)

	assert.NotNil(t, config.SyncSource.ReplicaSet.TLS)
	assert.False(t, *config.SyncSource.ReplicaSet.TLS, "ReplicaSet TLS should be false when source has no TLS")
	assert.NotNil(t, config.SyncSource.Router)
	assert.NotNil(t, config.SyncSource.Router.TLS)
	assert.False(t, *config.SyncSource.Router.TLS, "Router TLS should be false when source has no TLS")
	assert.Nil(t, config.SyncSource.CertificateAuthorityFile)
}

// mockShardedSource is a mock implementation of ShardedSearchSourceDBResource for testing
type mockShardedSource struct {
	shardNames []string
	hostSeeds  map[int][]string
	tlsConfig  *TLSSourceConfig
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

func (m *mockShardedSource) MongosHostAndPort() string {
	return "mongos-svc.test-ns.svc.cluster.local:27017"
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
	return m.tlsConfig
}

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




func TestGetMongosConfigParametersForSharded(t *testing.T) {
	tests := []struct {
		name          string
		search        *searchv1.MongoDBSearch
		shardNames    []string
		clusterDomain string
		expectedHost  string
	}{
		{
			name: "Internal service endpoint (no unmanaged LB)",
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
			shardNames:    []string{"test-mdb-0", "test-mdb-1"},
			clusterDomain: "cluster.local",
			// Uses first shard's internal service endpoint
			expectedHost: "test-search-mongot-test-mdb-0-svc.test-ns.svc.cluster.local:27028",
		},
		{
			name: "Empty shard names",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{},
			},
			shardNames:    []string{},
			clusterDomain: "cluster.local",
			expectedHost:  "", // No shards, no endpoint
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := GetMongosConfigParametersForSharded(tc.search, tc.shardNames, tc.clusterDomain)

			setParameter, ok := config["setParameter"].(map[string]any)
			require.True(t, ok, "setParameter should be a map")

			mongotHost, ok := setParameter["mongotHost"].(string)
			require.True(t, ok, "mongotHost should be a string")
			assert.Equal(t, tc.expectedHost, mongotHost)

			searchIndexHost, ok := setParameter["searchIndexManagementHostAndPort"].(string)
			require.True(t, ok, "searchIndexManagementHostAndPort should be a string")
			assert.Equal(t, tc.expectedHost, searchIndexHost)

			// useGrpcForSearch must always be true for mongos
			useGrpc, ok := setParameter["useGrpcForSearch"].(bool)
			require.True(t, ok, "useGrpcForSearch should be a bool")
			assert.True(t, useGrpc, "useGrpcForSearch must be true for mongos")
		})
	}
}


func TestTLSSecretPrefixNaming(t *testing.T) {
	testCases := []struct {
		name               string
		secretName         string
		secretPrefix       string
		resourceName       string
		expectedSecretName string
	}{
		{
			name:               "explicit secret name takes precedence",
			secretName:         "my-explicit-secret",
			secretPrefix:       "my-prefix",
			resourceName:       "my-search",
			expectedSecretName: "my-explicit-secret",
		},
		{
			name:               "prefix-based naming when no explicit name",
			secretName:         "",
			secretPrefix:       "my-prefix",
			resourceName:       "my-search",
			expectedSecretName: "my-prefix-my-search-search-cert",
		},
		{
			name:               "only explicit name specified",
			secretName:         "only-explicit",
			secretPrefix:       "",
			resourceName:       "my-search",
			expectedSecretName: "only-explicit",
		},
		{
			name:               "default naming when both empty",
			secretName:         "",
			secretPrefix:       "",
			resourceName:       "my-search",
			expectedSecretName: "my-search-search-cert",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch(tc.resourceName, "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{
							Name: tc.secretName,
						},
						CertsSecretPrefix: tc.secretPrefix,
					},
				}
			})

			secretNsName := search.TLSSecretNamespacedName()
			assert.Equal(t, tc.expectedSecretName, secretNsName.Name)
			assert.Equal(t, "default", secretNsName.Namespace)
		})
	}
}




func TestIsTLSConfigured(t *testing.T) {
	testCases := []struct {
		name           string
		setup          func(*searchv1.MongoDBSearch)
		expectedResult bool
	}{
		{
			name: "TLS with explicit secret name",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{Name: "my-secret"},
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "TLS with prefix",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "TLS with both",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{Name: "my-secret"},
						CertsSecretPrefix:    "my-prefix",
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "TLS with neither uses default",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{},
				}
			},
			expectedResult: true,
		},
		{
			name: "no TLS config",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{}
			},
			expectedResult: false,
		},
		{
			name: "security but no TLS",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: nil,
				}
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", tc.setup)
			assert.Equal(t, tc.expectedResult, search.IsTLSConfigured())
		})
	}
}

func TestIsSharedTLSCertificate(t *testing.T) {
	testCases := []struct {
		name           string
		setup          func(*searchv1.MongoDBSearch)
		expectedResult bool
	}{
		{
			name: "shared mode - explicit secret name set",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{Name: "my-shared-secret"},
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "shared mode - both name and prefix set",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{Name: "my-shared-secret"},
						CertsSecretPrefix:    "my-prefix",
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "per-shard mode - only prefix set",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			expectedResult: false,
		},
		{
			name: "per-shard mode - neither name nor prefix set",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{},
				}
			},
			expectedResult: false,
		},
		{
			name: "no TLS configured",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: nil,
				}
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", tc.setup)
			assert.Equal(t, tc.expectedResult, search.IsSharedTLSCertificate())
		})
	}
}

func TestTLSSecretNamespacedNameForShard(t *testing.T) {
	testCases := []struct {
		name               string
		secretPrefix       string
		shardName          string
		namespace          string
		expectedSecretName string
	}{
		{
			name:               "with prefix",
			secretPrefix:       "my-prefix",
			shardName:          "my-cluster-0",
			namespace:          "test-ns",
			expectedSecretName: "my-prefix-my-cluster-0-search-cert",
		},
		{
			name:               "without prefix",
			secretPrefix:       "",
			shardName:          "my-cluster-0",
			namespace:          "test-ns",
			expectedSecretName: "my-cluster-0-search-cert",
		},
		{
			name:               "with prefix - second shard",
			secretPrefix:       "prod",
			shardName:          "shard-1",
			namespace:          "mongodb",
			expectedSecretName: "prod-shard-1-search-cert",
		},
		{
			name:               "without prefix - different shard",
			secretPrefix:       "",
			shardName:          "shard-2",
			namespace:          "mongodb",
			expectedSecretName: "shard-2-search-cert",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", tc.namespace, func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: tc.secretPrefix,
					},
				}
			})

			secretNsName := search.TLSSecretNamespacedNameForShard(tc.shardName)
			assert.Equal(t, tc.expectedSecretName, secretNsName.Name)
			assert.Equal(t, tc.namespace, secretNsName.Namespace)
		})
	}
}

func TestTLSOperatorSecretNamespacedNameForShard(t *testing.T) {
	testCases := []struct {
		name               string
		shardName          string
		namespace          string
		expectedSecretName string
	}{
		{
			name:               "first shard",
			shardName:          "my-cluster-0",
			namespace:          "test-ns",
			expectedSecretName: "my-cluster-0-search-certificate-key",
		},
		{
			name:               "second shard",
			shardName:          "my-cluster-1",
			namespace:          "mongodb",
			expectedSecretName: "my-cluster-1-search-certificate-key",
		},
		{
			name:               "different shard naming",
			shardName:          "shard-prod-0",
			namespace:          "production",
			expectedSecretName: "shard-prod-0-search-certificate-key",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", tc.namespace)

			secretNsName := search.TLSOperatorSecretNamespacedNameForShard(tc.shardName)
			assert.Equal(t, tc.expectedSecretName, secretNsName.Name)
			assert.Equal(t, tc.namespace, secretNsName.Namespace)
		})
	}
}

func TestPerShardTLSResourceAdapter(t *testing.T) {
	testCases := []struct {
		name                       string
		secretPrefix               string
		shardName                  string
		namespace                  string
		expectedSourceSecretName   string
		expectedOperatorSecretName string
	}{
		{
			name:                       "with prefix",
			secretPrefix:               "my-prefix",
			shardName:                  "my-cluster-0",
			namespace:                  "test-ns",
			expectedSourceSecretName:   "my-prefix-my-cluster-0-search-cert",
			expectedOperatorSecretName: "my-cluster-0-search-certificate-key",
		},
		{
			name:                       "without prefix",
			secretPrefix:               "",
			shardName:                  "shard-1",
			namespace:                  "mongodb",
			expectedSourceSecretName:   "shard-1-search-cert",
			expectedOperatorSecretName: "shard-1-search-certificate-key",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", tc.namespace, func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: tc.secretPrefix,
					},
				}
			})

			adapter := &perShardTLSResource{
				MongoDBSearch: search,
				shardName:     tc.shardName,
			}

			// Test TLSSecretNamespacedName
			sourceSecret := adapter.TLSSecretNamespacedName()
			assert.Equal(t, tc.expectedSourceSecretName, sourceSecret.Name)
			assert.Equal(t, tc.namespace, sourceSecret.Namespace)

			// Test TLSOperatorSecretNamespacedName
			operatorSecret := adapter.TLSOperatorSecretNamespacedName()
			assert.Equal(t, tc.expectedOperatorSecretName, operatorSecret.Name)
			assert.Equal(t, tc.namespace, operatorSecret.Namespace)
		})
	}
}

func TestValidatePerShardTLSSecrets(t *testing.T) {
	testCases := []struct {
		name           string
		setup          func(*searchv1.MongoDBSearch)
		shardNames     []string
		existingSecret string // Name of secret to create (empty = no secrets)
		expectedOK     bool
		expectedPhase  status.Phase // status.PhasePending or status.PhaseFailed or "" for OK
	}{
		{
			name: "TLS not configured - returns OK",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{TLS: nil}
			},
			shardNames:    []string{"shard-0", "shard-1"},
			expectedOK:    true,
			expectedPhase: "",
		},
		{
			name: "shared mode - returns OK without checking per-shard secrets",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{Name: "shared-secret"},
					},
				}
			},
			shardNames:    []string{"shard-0", "shard-1"},
			expectedOK:    true,
			expectedPhase: "",
		},
		{
			name: "per-shard mode - missing secret returns Pending",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			shardNames:     []string{"shard-0", "shard-1"},
			existingSecret: "", // No secrets exist
			expectedOK:     false,
			expectedPhase:  status.PhasePending,
		},
		{
			name: "per-shard mode - first secret exists, second missing returns Pending",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			shardNames:     []string{"shard-0", "shard-1"},
			existingSecret: "my-prefix-shard-0-search-cert", // Only first shard's secret exists
			expectedOK:     false,
			expectedPhase:  status.PhasePending,
		},
		{
			name: "per-shard mode - all secrets exist returns OK",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			shardNames:     []string{"shard-0"},
			existingSecret: "my-prefix-shard-0-search-cert",
			expectedOK:     true,
			expectedPhase:  "",
		},
		{
			name: "per-shard mode without prefix - missing secret returns Pending",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{},
				}
			},
			shardNames:     []string{"shard-0"},
			existingSecret: "", // No secrets exist
			expectedOK:     false,
			expectedPhase:  status.PhasePending,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "test-ns", tc.setup)

			var objects []client.Object
			objects = append(objects, search)

			// Create the existing secret if specified
			if tc.existingSecret != "" {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tc.existingSecret,
						Namespace: "test-ns",
					},
					Data: map[string][]byte{
						"tls.crt": []byte("cert-data"),
						"tls.key": []byte("key-data"),
					},
				}
				objects = append(objects, secret)
			}

			fakeClient := newTestFakeClient(objects...)

			// Create a mock sharded source
			shardedSource := &mockShardedSource{
				shardNames: tc.shardNames,
			}

			helper := NewMongoDBSearchReconcileHelper(
				fakeClient,
				search,
				shardedSource,
				newTestOperatorSearchConfig(),
			)

			status := helper.validatePerShardTLSSecrets(t.Context(), zap.S(), tc.shardNames)

			if tc.expectedOK {
				assert.True(t, status.IsOK(), "Expected status to be OK")
			} else {
				assert.False(t, status.IsOK(), "Expected status to not be OK")
				assert.Equal(t, tc.expectedPhase, status.Phase())
			}
		})
	}
}

func TestValidatePerShardTLSSecretsAllExist(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Security = searchv1.Security{
			TLS: &searchv1.TLS{
				CertsSecretPrefix: "my-prefix",
			},
		}
	})

	shardNames := []string{"shard-0", "shard-1", "shard-2"}

	var objects []client.Object
	objects = append(objects, search)

	// Create all per-shard secrets
	for _, shardName := range shardNames {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("my-prefix-%s-search-cert", shardName),
				Namespace: "test-ns",
			},
			Data: map[string][]byte{
				"tls.crt": []byte("cert-data"),
				"tls.key": []byte("key-data"),
			},
		}
		objects = append(objects, secret)
	}

	fakeClient := newTestFakeClient(objects...)

	shardedSource := &mockShardedSource{
		shardNames: shardNames,
	}

	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		search,
		shardedSource,
		newTestOperatorSearchConfig(),
	)

	status := helper.validatePerShardTLSSecrets(t.Context(), zap.S(), shardNames)
	assert.True(t, status.IsOK(), "Expected status to be OK when all secrets exist")
}
