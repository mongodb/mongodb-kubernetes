package searchcontroller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ghodss/yaml"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
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

func TestGetMongodConfigParameters_ManagedLB(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb-search",
			Namespace: "test",
		},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Mode: searchv1.LBModeManaged,
			},
		},
	}

	clusterDomain := "cluster.local"
	params := GetMongodConfigParameters(search, clusterDomain)

	setParams := params["setParameter"].(map[string]any)

	expectedEndpoint := "test-mongodb-search-search-lb-svc.test.svc.cluster.local:27029"
	assert.Equal(t, expectedEndpoint, setParams["mongotHost"])
	assert.Equal(t, expectedEndpoint, setParams["searchIndexManagementHostAndPort"])
	assert.Equal(t, true, setParams["useGrpcForSearch"])
}

func TestGetMongodConfigParameters_NoLB(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb-search",
			Namespace: "test",
		},
	}

	clusterDomain := "cluster.local"
	params := GetMongodConfigParameters(search, clusterDomain)

	setParams := params["setParameter"].(map[string]any)

	// Without LB, should point directly to mongot headless service
	expectedEndpoint := "test-mongodb-search-search-svc.test.svc.cluster.local:27028"
	assert.Equal(t, expectedEndpoint, setParams["mongotHost"])
	assert.Equal(t, expectedEndpoint, setParams["searchIndexManagementHostAndPort"])
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
    hostAndPort: null`

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

func TestEnsureMongotConfig_PerPodModes(t *testing.T) {
	cases := []struct {
		name             string
		replicas         int
		hasAutoEmbedding bool
		expectedKeys     []string
		notExpectedKeys  []string
	}{
		{
			name:             "single config mode - no embedding",
			replicas:         1,
			hasAutoEmbedding: false,
			expectedKeys:     []string{MongotConfigFilename},
			notExpectedKeys:  []string{MongotConfigLeaderFilename, MongotConfigFollowerFilename},
		},
		{
			name:             "per-pod config mode - single replica with embedding",
			replicas:         1,
			hasAutoEmbedding: true,
			expectedKeys:     []string{MongotConfigLeaderFilename, MongotConfigFollowerFilename, "test-search-search-0"},
			notExpectedKeys:  []string{MongotConfigFilename},
		},
		{
			name:             "per-pod config mode - 3 replicas with embedding",
			replicas:         3,
			hasAutoEmbedding: true,
			expectedKeys:     []string{MongotConfigLeaderFilename, MongotConfigFollowerFilename, "test-search-search-0", "test-search-search-1", "test-search-search-2"},
			notExpectedKeys:  []string{MongotConfigFilename},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "test-ns")
			search.Spec.Replicas = tc.replicas
			if tc.hasAutoEmbedding {
				search.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{}
			}
			fakeClient := newTestFakeClient(search)
			helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig())
			cmName := search.MongotConfigConfigMapNamespacedName()
			stsName := search.StatefulSetNamespacedName().Name

			embeddingMod := func(c *mongot.Config) {
				c.Embedding = &mongot.EmbeddingConfig{IsAutoEmbeddingViewWriter: ptr.To(true)}
			}
			_, err := helper.ensureMongotConfig(t.Context(), zap.S(), cmName, stsName, embeddingMod)
			require.NoError(t, err)

			cm, err := fakeClient.GetConfigMap(t.Context(), cmName)
			require.NoError(t, err)

			for _, key := range tc.expectedKeys {
				assert.Contains(t, cm.Data, key)
			}
			for _, key := range tc.notExpectedKeys {
				assert.NotContains(t, cm.Data, key)
			}

			if tc.hasAutoEmbedding {
				var leaderConfig, followerConfig mongot.Config
				require.NoError(t, yaml.Unmarshal([]byte(cm.Data[MongotConfigLeaderFilename]), &leaderConfig))
				require.NoError(t, yaml.Unmarshal([]byte(cm.Data[MongotConfigFollowerFilename]), &followerConfig))
				assert.True(t, *leaderConfig.Embedding.IsAutoEmbeddingViewWriter)
				assert.False(t, *followerConfig.Embedding.IsAutoEmbeddingViewWriter)
			}
		})
	}
}

func TestEnsureMongotConfig_TransitionBetweenModes(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	search.Spec.Replicas = 1
	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig())
	cmName := search.MongotConfigConfigMapNamespacedName()
	stsName := search.StatefulSetNamespacedName().Name

	embeddingMod := func(c *mongot.Config) {
		c.Embedding = &mongot.EmbeddingConfig{IsAutoEmbeddingViewWriter: ptr.To(true)}
	}

	// Create ConfigMap in single config mode
	_, err := helper.ensureMongotConfig(t.Context(), zap.S(), cmName, stsName, embeddingMod)
	require.NoError(t, err)

	// Transition to per-pod config mode - verify old key is cleaned up
	search.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{}
	_, err = helper.ensureMongotConfig(t.Context(), zap.S(), cmName, stsName, embeddingMod)
	require.NoError(t, err)

	cm, err := fakeClient.GetConfigMap(t.Context(), cmName)
	require.NoError(t, err)
	assert.NotContains(t, cm.Data, MongotConfigFilename, "config.yml should be removed after transition")

	// Transition back to single config mode - verify per-pod keys are cleaned up
	search.Spec.AutoEmbedding = nil
	_, err = helper.ensureMongotConfig(t.Context(), zap.S(), cmName, stsName, embeddingMod)
	require.NoError(t, err)

	cm, err = fakeClient.GetConfigMap(t.Context(), cmName)
	require.NoError(t, err)
	assert.NotContains(t, cm.Data, MongotConfigLeaderFilename, "config-leader.yml should be removed after transition")
	assert.NotContains(t, cm.Data, MongotConfigFollowerFilename, "config-follower.yml should be removed after transition")
	assert.NotContains(t, cm.Data, stsName+"-0", "pod role key should be removed after transition")
	assert.NotContains(t, cm.Data, stsName+"-1", "pod role key should be removed after transition")
}

func TestCreateSearchStatefulSetFunc_ConfigMounting(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	labels := map[string]string{"app": "test-svc"}

	// Single config mode
	sts := &appsv1.StatefulSet{}
	CreateSearchStatefulSetFunc(search, "sts", "ns", "svc", "cm", labels, "img:v1", false)(sts)
	assert.Contains(t, sts.Spec.Template.Spec.Containers[0].Args[1], MongotConfigPath)

	// Per-pod config mode
	sts = &appsv1.StatefulSet{}
	CreateSearchStatefulSetFunc(search, "sts", "ns", "svc", "cm", labels, "img:v1", true)(sts)
	startupCmd := sts.Spec.Template.Spec.Containers[0].Args[1]
	assert.Contains(t, startupCmd, MongotPerPodConfigDirPath)
	assert.Contains(t, startupCmd, "ROLE=$(cat")
}

func TestGetMongodConfigParametersForShard(t *testing.T) {
	tests := []struct {
		name           string
		search         *searchv1.MongoDBSearch
		shardName      string
		clusterDomain  string
		expectedHost   string
		useUnmanagedLB bool
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
			shardName:      "test-mdb-0",
			clusterDomain:  "cluster.local",
			expectedHost:   "test-search-search-0-test-mdb-0-svc.test-ns.svc.cluster.local:27028",
			useUnmanagedLB: false,
		},
		{
			name: "Unmanaged LB endpoint for shard via template",
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
						Mode:     searchv1.LBModeUnmanaged,
						Endpoint: "lb-{shardName}.example.com:27028",
					},
				},
			},
			shardName:      "test-mdb-0",
			clusterDomain:  "cluster.local",
			expectedHost:   "lb-test-mdb-0.example.com:27028",
			useUnmanagedLB: true,
		},
		{
			name: "Unmanaged LB endpoint for second shard via template",
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
						Mode:     searchv1.LBModeUnmanaged,
						Endpoint: "lb-{shardName}.example.com:27028",
					},
				},
			},
			shardName:      "test-mdb-1",
			clusterDomain:  "cluster.local",
			expectedHost:   "lb-test-mdb-1.example.com:27028",
			useUnmanagedLB: true,
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

func TestCreateShardMongotConfig(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
			Mode:     searchv1.LBModeUnmanaged,
			Endpoint: "lb-{shardName}.example.com:27028",
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[int][]string{
			0: {"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"},
			1: {"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"},
		},
	}

	config := mongot.Config{}
	createMongotConfigForShard(search, shardedSource, shardedSource.shardNames[0])(&config)

	assert.Equal(t, []string{"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"}, config.SyncSource.ReplicaSet.HostAndPort)
	assert.Equal(t, search.SourceUsername(), config.SyncSource.ReplicaSet.Username)

	config2 := mongot.Config{}
	createMongotConfigForShard(search, shardedSource, shardedSource.shardNames[1])(&config2)

	assert.Equal(t, []string{"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"}, config2.SyncSource.ReplicaSet.HostAndPort)
}

func TestShardedMongotConfigWithTLS(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
			Mode:     searchv1.LBModeUnmanaged,
			Endpoint: "lb-{shardName}.example.com:27028",
		}
	})

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
	createMongotConfigForShard(search, shardedSource, shardedSource.shardNames[0])(&config)

	require.NotNil(t, config.SyncSource.ReplicaSet.TLS)
	assert.False(t, *config.SyncSource.ReplicaSet.TLS, "ReplicaSet TLS should initially be false")
	require.NotNil(t, config.SyncSource.Router)
	require.NotNil(t, config.SyncSource.Router.TLS)
	assert.False(t, *config.SyncSource.Router.TLS, "Router TLS should initially be false")

	// Simulate what ensureEgressTlsConfig does when TLS is enabled
	tlsSourceConfig := shardedSource.TLSConfig()
	require.NotNil(t, tlsSourceConfig, "TLS config should not be nil")

	// Apply the TLS modification (simulating ensureEgressTlsConfig behavior)
	config.SyncSource.ReplicaSet.TLS = ptr.To(true)
	config.SyncSource.CertificateAuthorityFile = ptr.To("/mongodb-automation/ca/" + tlsSourceConfig.CAFileName)
	if config.SyncSource.Router != nil {
		config.SyncSource.Router.TLS = ptr.To(true)
	}

	assert.True(t, *config.SyncSource.ReplicaSet.TLS, "ReplicaSet TLS should be enabled")
	require.NotNil(t, config.SyncSource.CertificateAuthorityFile)
	assert.Equal(t, "/mongodb-automation/ca/ca-pem", *config.SyncSource.CertificateAuthorityFile)
	assert.True(t, *config.SyncSource.Router.TLS, "Router TLS should be enabled for sharded clusters")
}

func TestShardedMongotConfigWithoutTLS(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
			Mode:     searchv1.LBModeUnmanaged,
			Endpoint: "lb-{shardName}.example.com:27028",
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0"},
		hostSeeds: map[int][]string{
			0: {"my-cluster-0-0.svc:27017"},
		},
		tlsConfig: nil, // No TLS
	}

	config := mongot.Config{}
	createMongotConfigForShard(search, shardedSource, shardedSource.shardNames[0])(&config)

	require.NotNil(t, config.SyncSource.ReplicaSet.TLS)
	assert.False(t, *config.SyncSource.ReplicaSet.TLS, "ReplicaSet TLS should be false when source has no TLS")
	require.NotNil(t, config.SyncSource.Router)
	require.NotNil(t, config.SyncSource.Router.TLS)
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

func (m *mockShardedSource) GetUnmanagedLBEndpointForShard(shardName string) string {
	return ""
}

func (m *mockShardedSource) MongosHostAndPort() string {
	return "mongos-svc.test-ns.svc.cluster.local:27017"
}

// Implement SearchSourceDBResource interface
func (m *mockShardedSource) HostSeeds(shardName string) []string {
	for idx, name := range m.shardNames {
		if name == shardName {
			return m.hostSeeds[idx]
		}
	}
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

func (m *mockShardedSource) ResourceType() mdbv1.ResourceType {
	return mdbv1.ShardedCluster
}

func TestBuildShardSearchHeadlessService(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test")
	shardName := "my-cluster-0"

	svc := buildSearchHeadlessServiceForShard(search, shardName)

	assert.Equal(t, "test-search-search-0-my-cluster-0-svc", svc.Name)
	assert.Equal(t, "test", svc.Namespace)
	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
	assert.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)

	// Check selector points to the shard StatefulSet
	assert.Equal(t, "test-search-search-0-my-cluster-0", svc.Spec.Selector["app"])

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

func TestValidateMultipleReplicasConfig(t *testing.T) {
	mdbSearchSpec := searchv1.MongoDBSearchSpec{
		Source: &searchv1.MongoDBSource{
			MongoDBResourceRef: &userv1.MongoDBResourceRef{
				Name: "test-mongodb",
			},
		},
	}

	mdbc := &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb",
			Namespace: "test",
		},
	}

	tests := []struct {
		name          string
		search        *searchv1.MongoDBSearch
		expectedError string
	}{
		{
			name: "Single replica - no LB required",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test",
				},
				Spec: mdbSearchSpec,
			},
			expectedError: "",
		},
		{
			name: "Multiple replicas without LB - error",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Replicas: 3,
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mongodb",
						},
					},
				},
			},
			expectedError: "multiple mongot replicas (3) require load balancer configuration; please configure load balancing in spec.lb.",
		},
		{
			name: "Multiple replicas with unmanaged LB - valid",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Replicas: 3,
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mongodb",
						},
					},
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Mode:     searchv1.LBModeUnmanaged,
						Endpoint: "lb.example.com:27028",
					},
				},
			},
			expectedError: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clientBuilder := mock.NewEmptyFakeClientBuilder()
			clientBuilder.WithObjects(mdbc)

			helper := NewMongoDBSearchReconcileHelper(
				kubernetesClient.NewClient(clientBuilder.Build()),
				tc.search,
				NewCommunityResourceSearchSource(mdbc),
				OperatorSearchConfig{},
			)

			err := helper.ValidateMultipleReplicasConfig()
			if tc.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tc.expectedError)
			}
		})
	}
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
			expectedHost: "test-search-search-0-test-mdb-0-svc.test-ns.svc.cluster.local:27028",
		},
		{
			name: "Unmanaged LB endpoint - uses first shard's endpoint via template",
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
						Mode:     searchv1.LBModeUnmanaged,
						Endpoint: "lb-{shardName}.example.com:27028",
					},
				},
			},
			shardNames:    []string{"test-mdb-0", "test-mdb-1"},
			clusterDomain: "cluster.local",
			// Mongos uses first shard's unmanaged LB endpoint via template substitution
			expectedHost: "lb-test-mdb-0.example.com:27028",
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

func TestEndpointTemplateSubstitution(t *testing.T) {
	testCases := []struct {
		name             string
		endpointTemplate string
		shardName        string
		expectedEndpoint string
	}{
		{
			name:             "simple template substitution",
			endpointTemplate: "lb-{shardName}.example.com:27028",
			shardName:        "my-cluster-0",
			expectedEndpoint: "lb-my-cluster-0.example.com:27028",
		},
		{
			name:             "template with shard name at end",
			endpointTemplate: "mongot-lb-{shardName}:27028",
			shardName:        "shard-1",
			expectedEndpoint: "mongot-lb-shard-1:27028",
		},
		{
			name:             "template with complex shard name",
			endpointTemplate: "lb-{shardName}.search.mongodb.svc.cluster.local:27028",
			shardName:        "my-sharded-cluster-shard-0",
			expectedEndpoint: "lb-my-sharded-cluster-shard-0.search.mongodb.svc.cluster.local:27028",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
					Mode:     searchv1.LBModeUnmanaged,
					Endpoint: tc.endpointTemplate,
				}
			})

			assert.True(t, search.HasEndpointTemplate())
			assert.True(t, search.IsShardedUnmanagedLB())
			assert.False(t, search.IsReplicaSetUnmanagedLB())

			endpoint := search.GetEndpointForShard(tc.shardName)
			assert.Equal(t, tc.expectedEndpoint, endpoint)
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

func TestValidateEndpointTemplate(t *testing.T) {
	testCases := []struct {
		name          string
		endpoint      string
		expectError   bool
		errorContains string
	}{
		{
			name:        "valid template",
			endpoint:    "lb-{shardName}.example.com:27028",
			expectError: false,
		},
		{
			name:        "valid template with placeholder at end",
			endpoint:    "mongot-{shardName}:27028",
			expectError: false,
		},
		{
			name:          "only placeholder is invalid",
			endpoint:      "{shardName}",
			expectError:   true,
			errorContains: "must contain more than just",
		},
		{
			name:        "multiple placeholders are supported",
			endpoint:    "lb-{shardName}-{shardName}.example.com:27028",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
					Mode:     searchv1.LBModeUnmanaged,
					Endpoint: tc.endpoint,
				}
			})

			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TODO: explicit secret name for sharded clusters is not supported
func TestValidateTLSConfig(t *testing.T) {
	testCases := []struct {
		name          string
		secretName    string
		secretPrefix  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "explicit secret name is valid",
			secretName:  "my-secret",
			expectError: false,
		},
		{
			name:         "prefix is valid",
			secretPrefix: "my-prefix",
			expectError:  false,
		},
		{
			name:         "both specified is valid",
			secretName:   "my-secret",
			secretPrefix: "my-prefix",
			expectError:  false,
		},
		{
			name:         "neither specified uses default",
			secretName:   "",
			secretPrefix: "",
			expectError:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{
							Name: tc.secretName,
						},
						CertsSecretPrefix: tc.secretPrefix,
					},
				}
			})

			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
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
			expectedSecretName: "my-prefix-test-search-search-0-my-cluster-0-cert",
		},
		{
			name:               "without prefix",
			secretPrefix:       "",
			shardName:          "my-cluster-0",
			namespace:          "test-ns",
			expectedSecretName: "test-search-search-0-my-cluster-0-cert",
		},
		{
			name:               "with prefix - second shard",
			secretPrefix:       "prod",
			shardName:          "shard-1",
			namespace:          "mongodb",
			expectedSecretName: "prod-test-search-search-0-shard-1-cert",
		},
		{
			name:               "without prefix - different shard",
			secretPrefix:       "",
			shardName:          "shard-2",
			namespace:          "mongodb",
			expectedSecretName: "test-search-search-0-shard-2-cert",
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

			secretNsName := search.TLSSecretForShard(tc.shardName)
			assert.Equal(t, tc.expectedSecretName, secretNsName.Name)
			assert.Equal(t, tc.namespace, secretNsName.Namespace)
		})
	}
}

func TestReconcileSharded_CertificateKeySecretRefRejected(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Security = searchv1.Security{
			TLS: &searchv1.TLS{
				CertificateKeySecret: corev1.LocalObjectReference{Name: "shared-cert"},
			},
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"shard-0", "shard-1"},
	}

	helper := NewMongoDBSearchReconcileHelper(
		newTestFakeClient(search),
		search,
		shardedSource,
		newTestOperatorSearchConfig(),
	)

	result := helper.reconcileSharded(t.Context(), zap.S(), shardedSource, "8.0.0")

	assert.False(t, result.IsOK())
	assert.Equal(t, status.PhaseFailed, result.Phase())

	msgOpt, exists := status.GetOption(result.StatusOptions(), status.MessageOption{})
	require.True(t, exists)
	assert.Contains(t, msgOpt.(status.MessageOption).Message, "spec.security.tls.certificateKeySecretRef is not supported for sharded clusters")
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
			existingSecret: "my-prefix-test-search-search-0-shard-0-cert", // Only first shard's secret exists
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
			existingSecret: "my-prefix-test-search-search-0-shard-0-cert",
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
				Name:      fmt.Sprintf("my-prefix-test-search-search-0-%s-cert", shardName),
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

func TestEnsureX509ClientCertConfig_NoopWhenNotConfigured(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")

	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig())

	mongotMod, stsMod, err := helper.ensureX509ClientCertConfig(t.Context())
	require.NoError(t, err)

	// Apply modifications and verify no changes
	config := &mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				Username:     "original-user",
				PasswordFile: "/original/path",
				AuthSource:   ptr.To("admin"),
			},
		},
	}
	mongotMod(config)

	assert.Equal(t, "original-user", config.SyncSource.ReplicaSet.Username)
	assert.Equal(t, "/original/path", config.SyncSource.ReplicaSet.PasswordFile)
	assert.Equal(t, "admin", *config.SyncSource.ReplicaSet.AuthSource)
	assert.Nil(t, config.SyncSource.ReplicaSet.X509)

	sts := newBaseMongotStatefulSet()
	stsMod(sts)
	assert.Empty(t, sts.Spec.Template.Spec.Volumes)
}

func TestEnsureX509ClientCertConfig_ErrorWhenTLSNotConfigured(t *testing.T) {
	// should error out if the x509 auth is configured between mongot -> mongod but tls is not enabled
	// for search source
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source.X509 = &searchv1.X509Auth{
			ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
		}
	})

	dbSource := &mockShardedSource{tlsConfig: nil} // No TLS on source

	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, dbSource, newTestOperatorSearchConfig())

	_, _, err := helper.ensureX509ClientCertConfig(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tls must be enabled")
}

func TestEnsureX509ClientCertConfig_MongotAndStsModification(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source.X509 = &searchv1.X509Auth{
			ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
		}
	})

	dbSource := &mockShardedSource{tlsConfig: &TLSSourceConfig{CAFileName: "ca-pem"}}

	x509Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "x509-cert", Namespace: "test-ns"},
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}

	fakeClient := newTestFakeClient(search, x509Secret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, dbSource, newTestOperatorSearchConfig())

	mongotMod, stsMod, err := helper.ensureX509ClientCertConfig(t.Context())
	require.NoError(t, err)

	// Apply mongot modification to a config with both ReplicaSet and Router (sharded scenario)
	config := &mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				Username:     "search-sync-source",
				PasswordFile: TempSourceUserPasswordPath,
				AuthSource:   ptr.To("admin"),
			},
			Router: &mongot.ConfigRouter{
				HostAndPort:  "mongos-svc:27017",
				Username:     "search-sync-source",
				PasswordFile: TempSourceUserPasswordPath,
			},
		},
	}
	mongotMod(config)

	// ReplicaSet: username/password cleared, authSource=$external, x509 cert path set
	assert.Empty(t, config.SyncSource.ReplicaSet.Username)
	assert.Empty(t, config.SyncSource.ReplicaSet.PasswordFile)
	assert.Equal(t, "$external", *config.SyncSource.ReplicaSet.AuthSource)
	require.NotNil(t, config.SyncSource.ReplicaSet.X509)
	require.NotNil(t, config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile)
	assert.True(t, strings.HasPrefix(*config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile, X509ClientCertOperatorMountPath))
	assert.True(t, strings.HasSuffix(*config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile, ".pem"))
	assert.Nil(t, config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFilePasswordFile)

	// Router: same x509 modifications, cert path matches ReplicaSet
	assert.Empty(t, config.SyncSource.Router.Username)
	assert.Empty(t, config.SyncSource.Router.PasswordFile)
	assert.Equal(t, "$external", *config.SyncSource.Router.AuthSource)
	require.NotNil(t, config.SyncSource.Router.X509)
	require.NotNil(t, config.SyncSource.Router.X509.TLSCertificateKeyFile)
	assert.Equal(t, *config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile, *config.SyncSource.Router.X509.TLSCertificateKeyFile)
	assert.Nil(t, config.SyncSource.Router.X509.TLSCertificateKeyFilePasswordFile)

	// Apply STS modification and verify volumes
	sts := newBaseMongotStatefulSet()
	stsMod(sts)

	// Verify x509 volume exists and points to operator-managed secret
	var x509Volume *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == "x509-client-cert" {
			x509Volume = &sts.Spec.Template.Spec.Volumes[i]
		}
	}
	require.NotNil(t, x509Volume, "x509-client-cert volume should exist")
	assert.Equal(t, "test-search-x509-client-cert", x509Volume.VolumeSource.Secret.SecretName)

	// Verify x509 volume mount on mongot container
	mongotContainer := sts.Spec.Template.Spec.Containers[0]
	var x509Mount *corev1.VolumeMount
	for i := range mongotContainer.VolumeMounts {
		if mongotContainer.VolumeMounts[i].Name == "x509-client-cert" {
			x509Mount = &mongotContainer.VolumeMounts[i]
		}
	}
	require.NotNil(t, x509Mount, "x509-client-cert volume mount should exist")
	assert.Equal(t, X509ClientCertOperatorMountPath, x509Mount.MountPath)
	assert.True(t, x509Mount.ReadOnly)

	// No key password volume should exist
	for _, v := range sts.Spec.Template.Spec.Volumes {
		assert.NotEqual(t, "x509-key-password", v.Name, "x509-key-password volume should not exist")
	}
}

func TestEnsureX509ClientCertConfig_KeyPassword(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source.X509 = &searchv1.X509Auth{
			ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
		}
	})

	dbSource := &mockShardedSource{tlsConfig: &TLSSourceConfig{CAFileName: "ca-pem"}}

	// Secret includes the optional key password
	x509Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "x509-cert", Namespace: "test-ns"},
		Data: map[string][]byte{
			"tls.crt":                 []byte("cert-data"),
			"tls.key":                 []byte("key-data"),
			"tls.keyFilePassword": []byte("my-key-password"),
		},
	}

	fakeClient := newTestFakeClient(search, x509Secret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, dbSource, newTestOperatorSearchConfig())

	mongotMod, stsMod, err := helper.ensureX509ClientCertConfig(t.Context())
	require.NoError(t, err)

	// Verify mongot config has key password path
	config := &mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				Username:     "search-sync-source",
				PasswordFile: TempSourceUserPasswordPath,
			},
		},
	}
	mongotMod(config)

	require.NotNil(t, config.SyncSource.ReplicaSet.X509)
	require.NotNil(t, config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFilePasswordFile)
	assert.Equal(t, TempX509KeyPasswordPath, *config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFilePasswordFile)

	// Verify STS has key password volume and mount
	sts := newBaseMongotStatefulSet()
	stsMod(sts)

	var keyPasswordVolume *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == "x509-key-password" {
			keyPasswordVolume = &sts.Spec.Template.Spec.Volumes[i]
		}
	}
	require.NotNil(t, keyPasswordVolume, "x509-key-password volume should exist")
	assert.Equal(t, "x509-cert", keyPasswordVolume.VolumeSource.Secret.SecretName)

	mongotContainer := sts.Spec.Template.Spec.Containers[0]
	var keyPasswordMount *corev1.VolumeMount
	for i := range mongotContainer.VolumeMounts {
		if mongotContainer.VolumeMounts[i].Name == "x509-key-password" {
			keyPasswordMount = &mongotContainer.VolumeMounts[i]
		}
	}
	require.NotNil(t, keyPasswordMount, "x509-key-password volume mount should exist")
	assert.Equal(t, X509KeyPasswordMountPath, keyPasswordMount.MountPath)
	assert.Equal(t, X509KeyPasswordSecretKey, keyPasswordMount.SubPath)

	// Verify prepend command for file permissions
	assert.True(t, len(mongotContainer.Args) > 0)
	argsJoined := strings.Join(mongotContainer.Args, " ")
	assert.Contains(t, argsJoined, "x509-key-password")
}

// newBaseMongotStatefulSet creates a minimal StatefulSet with a mongot container for testing modifications.
func newBaseMongotStatefulSet() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							Args:         []string{"echo", "test"},
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}
}

func TestReconcileSharded_CreatesPerShardResources(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[int][]string{
			0: {"my-cluster-0-0.my-cluster-sh.test-ns.svc.cluster.local:27017"},
			1: {"my-cluster-1-0.my-cluster-sh.test-ns.svc.cluster.local:27017"},
		},
	}

	fakeClient := newTestFakeClient(search)

	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		search,
		shardedSource,
		newTestOperatorSearchConfig(),
	)

	// Pass 1: creates shard-0 resources, returns Pending (StatefulSet not ready)
	result := helper.reconcileSharded(t.Context(), zap.S(), shardedSource, "8.0.0")
	assert.False(t, result.IsOK())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), search.Namespace, fakeClient))

	// Pass 2: shard-0 ready, creates shard-1 resources, returns Pending
	result = helper.reconcileSharded(t.Context(), zap.S(), shardedSource, "8.0.0")
	assert.False(t, result.IsOK())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), search.Namespace, fakeClient))

	// Pass 3: all shards ready, returns OK
	result = helper.reconcileSharded(t.Context(), zap.S(), shardedSource, "8.0.0")
	assert.True(t, result.IsOK())

	// Verify per-shard Services
	for _, shardName := range shardedSource.GetShardNames() {
		svcNsName := search.MongotServiceForShard(shardName)
		svc, err := fakeClient.GetService(t.Context(), svcNsName)
		require.NoError(t, err)

		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s-svc", shardName), svc.Name)
		assert.Equal(t, "test-ns", svc.Namespace)
		assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s", shardName), svc.Spec.Selector["app"])

		portMap := make(map[string]int32)
		for _, p := range svc.Spec.Ports {
			portMap[p.Name] = p.Port
		}
		assert.Equal(t, int32(27028), portMap["mongot-grpc"])
		assert.Equal(t, int32(8080), portMap["healthcheck"])
	}

	// Verify per-shard StatefulSets
	for _, shardName := range shardedSource.GetShardNames() {
		stsNsName := search.MongotStatefulSetForShard(shardName)
		sts, err := fakeClient.GetStatefulSet(t.Context(), stsNsName)
		require.NoError(t, err)

		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s", shardName), sts.Name)
		assert.Equal(t, "test-ns", sts.Namespace)
		assert.Equal(t, shardName, sts.Labels["shard"])
	}

	// Verify per-shard ConfigMaps
	for _, shardName := range shardedSource.GetShardNames() {
		cmNsName := search.MongotConfigMapForShard(shardName)
		cm, err := fakeClient.GetConfigMap(t.Context(), cmNsName)
		require.NoError(t, err)

		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s-config", shardName), cm.Name)
		assert.Contains(t, cm.Data, MongotConfigFilename)
	}
}
