package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

const (
	testNamespace     = "test-ns"
	testSearchName    = "my-search"
	testMDBName       = "my-mongodb"
	testProjectCMName = "my-project-cm"
	testGroupID       = "test-group-id-123"
	testDefaultImage  = "quay.io/mongodb/metrics-forwarder:latest"
	testOMBaseURL     = "http://ops-manager.example.com:8080"
)

// newTestMongoDB creates a MongoDB resource with opsManager connection spec and a status with a project ID.
func newTestMongoDB(name, namespace, projectCMName, groupID string) *mdbv1.MongoDB {
	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				ResourceType: mdbv1.ReplicaSet,
				Version:      "8.2.0",
				ConnectionSpec: mdbv1.ConnectionSpec{
					SharedConnectionSpec: mdbv1.SharedConnectionSpec{
						OpsManagerConfig: &mdbv1.PrivateCloudConfig{
							ConfigMapRef: mdbv1.ConfigMapRef{Name: projectCMName},
						},
					},
					Credentials: "my-credentials",
				},
			},
			Members: 3,
		},
		Status: mdbv1.MongoDbStatus{
			ProjectId: groupID,
		},
	}
	return mdb
}

// newTestMongoDBSearch creates a MongoDBSearch resource pointing to a MongoDB enterprise source.
func newTestMongoDBSearch(name, namespace, mdbName string) *searchv1.MongoDBSearch {
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{Name: mdbName},
			},
			Clusters: []searchv1.ClusterSpec{{Name: ""}},
		},
		Status: searchv1.MongoDBSearchStatus{
			Version: "1.0.0",
		},
	}
}

// newTestProjectConfigMap creates a project configmap as expected by project.ReadProjectConfig.
func newTestProjectConfigMap(name, namespace, baseURL string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string]string{
			util.OmBaseUrl:     baseURL,
			util.OmProjectName: "test-project",
			util.OmOrgId:       "test-org-id",
		},
	}
}

// newMetricsForwarderReconciler creates the reconciler with a fake client populated with the given objects.
func newMetricsForwarderReconciler(defaultImage string, objects ...client.Object) (*MongoDBSearchMetricsForwarderReconciler, client.Client) {
	builder := mock.NewEmptyFakeClientBuilder()
	if len(objects) > 0 {
		builder.WithObjects(objects...)
	}
	fakeClient := builder.Build()
	kc := kubernetesClient.NewClient(fakeClient)

	r := &MongoDBSearchMetricsForwarderReconciler{
		kubeClient:         kc,
		secretClient:       secrets.SecretClient{KubeClient: kc},
		watch:              watch.NewResourceWatcher(),
		defaultImage:       defaultImage,
		omRequester:        newStubOMAgentRequester(testGroupID),
		otelConfigTemplate: searchcontroller.NewMetricsForwarderOTelConfigTemplate(),
		prepareSearch:      newPrepareSearch(""),
		clientForCluster:   func(string) kubernetesClient.Client { return kc },
	}
	return r, fakeClient
}

// stubOMAgentRequester is a test double for omAgentRequester that answers Ops Manager agent-auth
// requests from canned responses instead of hitting the network.
type stubOMAgentRequester struct {
	fn             func(projectConfig mdbv1.ProjectConfig, method, path, authHeader string, body any) ([]byte, error)
	getOMVersionFn func(projectConfig mdbv1.ProjectConfig) (versionutil.OpsManagerVersion, error)
}

func (s stubOMAgentRequester) RequestWithAgentAuth(projectConfig mdbv1.ProjectConfig, method, path, authHeader string, body any) ([]byte, error) {
	return s.fn(projectConfig, method, path, authHeader, body)
}

func (s stubOMAgentRequester) GetOMVersion(projectConfig mdbv1.ProjectConfig) (versionutil.OpsManagerVersion, error) {
	if s.getOMVersionFn != nil {
		return s.getOMVersionFn(projectConfig)
	}
	// Default: return the minimum supported version so existing tests are unaffected.
	return versionutil.OpsManagerVersion{VersionString: metricsForwarderMinOpsManagerVersion}, nil
}

// newStubOMAgentRequester returns a requester that resolves the group API to the given groupID and
// treats every host deletion as a no-op success. The OM version defaults to the minimum supported
// version (8.0.25) so existing tests are unaffected.
func newStubOMAgentRequester(groupID string) stubOMAgentRequester {
	return stubOMAgentRequester{
		fn: func(_ mdbv1.ProjectConfig, method, path, _ string, _ any) ([]byte, error) {
			switch {
			case method == "GET" && path == "/agents/api/group/v1":
				return []byte(fmt.Sprintf(`{"groupId":%q}`, groupID)), nil
			case method == "POST" && strings.HasSuffix(path, "/v1/delete"):
				return []byte(`{"results":[]}`), nil
			default:
				return nil, fmt.Errorf("unexpected OM agent request: %s %s", method, path)
			}
		},
	}
}

// newStubOMAgentRequesterWithVersion returns a stub that reports the given OM version and
// handles the standard group/delete endpoints for groupID.
func newStubOMAgentRequesterWithVersion(groupID string, omVersion versionutil.OpsManagerVersion) stubOMAgentRequester {
	stub := newStubOMAgentRequester(groupID)
	stub.getOMVersionFn = func(_ mdbv1.ProjectConfig) (versionutil.OpsManagerVersion, error) {
		return omVersion, nil
	}
	return stub
}

// newTestAgentKeySecret creates a Secret holding an Ops Manager agent API key.
func newTestAgentKeySecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{util.OmAgentApiKey: []byte("test-agent-api-key")},
	}
}

// newTestTopologyStateConfigMap builds the metrics-forwarder topology state ConfigMap for a single
// cluster (clusterName==""), so pre-deletion cleanup can compute which mongot hosts to remove.
func newTestTopologyStateConfigMap(t *testing.T, search *searchv1.MongoDBSearch, clusterState clusterTopologyState) *corev1.ConfigMap {
	t.Helper()
	state := searchTopologyState{Clusters: map[string]clusterTopologyState{"": clusterState}}
	stateJSON, err := json.Marshal(state)
	require.NoError(t, err)
	data := map[string]string{stateKey: string(stateJSON)}
	if search.UID != "" {
		data[stateOwnerUIDKey] = string(search.UID)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-metrics-forwarder-state", search.Name),
			Namespace: search.Namespace,
			Labels:    metricsForwarderLabels(search),
		},
		Data: data,
	}
}

// recordingDeleteHostsRequester returns an Ops Manager agent requester that appends every deregistered
// host id to dst and reports deletion as success.
func recordingDeleteHostsRequester(dst *[]string) stubOMAgentRequester {
	return stubOMAgentRequester{
		fn: func(_ mdbv1.ProjectConfig, method, path, _ string, body any) ([]byte, error) {
			if method == "POST" && strings.HasSuffix(path, "/v1/delete") {
				*dst = append(*dst, body.(deleteHostsRequest).HostIds...)
				return []byte(`{"results":[]}`), nil
			}
			return nil, fmt.Errorf("unexpected OM agent request: %s %s", method, path)
		},
	}
}

// getTopologyState reads and decodes the metrics-forwarder topology state ConfigMap,
// returning the single-cluster (clusterName=="") entry.
func getTopologyState(t *testing.T, c client.Client, search *searchv1.MongoDBSearch) clusterTopologyState {
	t.Helper()
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Namespace: search.Namespace,
		Name:      fmt.Sprintf("%s-metrics-forwarder-state", search.Name),
	}, cm))
	var state searchTopologyState
	require.NoError(t, json.Unmarshal([]byte(cm.Data[stateKey]), &state))
	return state.Clusters[""]
}

func TestOpenTopologyStateStore_StaleUIDResetsState(t *testing.T) {
	ctx := context.Background()
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = types.UID("new-search-uid")

	staleState := searchTopologyState{Clusters: map[string]clusterTopologyState{
		"": {Replicas: 3},
	}}
	stateJSON, err := json.Marshal(staleState)
	require.NoError(t, err)

	staleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-metrics-forwarder-state", search.Name),
			Namespace: search.Namespace,
			Labels:    metricsForwarderLabels(search),
		},
		Data: map[string]string{
			stateKey:         string(stateJSON),
			stateOwnerUIDKey: "old-search-uid",
		},
	}

	r, c := newMetricsForwarderReconciler(testDefaultImage, search, staleCM)
	store := r.openTopologyStateStore(search)

	_, err = store.ReadState(ctx)
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "stale owner UID must be treated as reset state")

	freshState := searchTopologyState{Clusters: map[string]clusterTopologyState{"": {Replicas: 1}}}
	require.NoError(t, store.WriteState(ctx, &freshState, zap.S()))

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{
		Namespace: search.Namespace,
		Name:      fmt.Sprintf("%s-metrics-forwarder-state", search.Name),
	}, cm))
	assert.Equal(t, string(search.UID), cm.Data[stateOwnerUIDKey])
	assert.Len(t, cm.OwnerReferences, 0, "topology state ConfigMap must not depend on owner references")
	assert.Equal(t, search.Name, cm.Labels["mongodb.com/search-name"])
	assert.Equal(t, search.Namespace, cm.Labels["mongodb.com/search-namespace"])
}

func reconcileMetricsForwarder(t *testing.T, r *MongoDBSearchMetricsForwarderReconciler, namespace, name string) reconcile.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: name},
	})
	require.NoError(t, err)
	return result
}

func getMongoDBSearch(t *testing.T, c client.Client, namespace, name string) *searchv1.MongoDBSearch {
	t.Helper()
	search := &searchv1.MongoDBSearch{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, search)
	require.NoError(t, err)
	return search
}

// envMap indexes a container's environment variables by name for easy assertion.
func envMap(env []corev1.EnvVar) map[string]corev1.EnvVar {
	m := make(map[string]corev1.EnvVar, len(env))
	for _, e := range env {
		m[e.Name] = e
	}
	return m
}

func TestBuildMetricsForwarderPodSpec_CustomResources(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("200m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
				},
			},
		},
	}
	resources := metricsForwarderResourceRequirements(search)
	podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, false)

	assert.Equal(t, resource.MustParse("200m"), podSpec.Containers[0].Resources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("256Mi"), podSpec.Containers[0].Resources.Requests[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("500m"), podSpec.Containers[0].Resources.Limits[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("512Mi"), podSpec.Containers[0].Resources.Limits[corev1.ResourceMemory])
}

func TestBuildMetricsForwarderPodSpec_Volumes(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "ns"},
	}
	resources := metricsForwarderResourceRequirements(search)

	t.Run("without CA cert", func(t *testing.T) {
		podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, false)

		assert.Len(t, podSpec.Volumes, 2)
		assert.Equal(t, "metrics-forwarder-config", podSpec.Volumes[0].Name)
		assert.Equal(t, search.MetricsForwarderConfigMapNameForCluster(0), podSpec.Volumes[0].ConfigMap.Name)
		assert.Equal(t, "agent-api-key", podSpec.Volumes[1].Name)
		assert.Equal(t, "agent-key-secret", podSpec.Volumes[1].Secret.SecretName)

		assert.Len(t, podSpec.Containers[0].VolumeMounts, 2)
		assert.Equal(t, "metrics-forwarder-config", podSpec.Containers[0].VolumeMounts[0].Name)
		assert.Equal(t, metricsForwarderConfigPath, podSpec.Containers[0].VolumeMounts[0].MountPath)
		assert.Equal(t, "agent-api-key", podSpec.Containers[0].VolumeMounts[1].Name)
	})

	t.Run("with CA cert", func(t *testing.T) {
		podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "my-ca-cm", 0, testDefaultImage, resources, false)

		assert.Len(t, podSpec.Volumes, 3)
		assert.Equal(t, metricsForwarderCACertVolumeName, podSpec.Volumes[2].Name)
		assert.Equal(t, "my-ca-cm", podSpec.Volumes[2].ConfigMap.Name)

		assert.Len(t, podSpec.Containers[0].VolumeMounts, 3)
		assert.Equal(t, metricsForwarderCACertVolumeName, podSpec.Containers[0].VolumeMounts[2].Name)
		assert.Equal(t, metricsForwarderCACertMountPath, podSpec.Containers[0].VolumeMounts[2].MountPath)
	})
}

func TestBuildMetricsForwarderPodSpec_EnvVars(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "ns"},
		Status: searchv1.MongoDBSearchStatus{
			Version: "1.0.0",
		},
	}
	resources := metricsForwarderResourceRequirements(search)
	podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, false)

	env := envMap(podSpec.Containers[0].Env)

	assert.Equal(t, "metadata.namespace", env["MONGOT_NAMESPACE"].ValueFrom.FieldRef.FieldPath)
	assert.Equal(t, "20", env["MEMORY_LIMITER_SPIKE_PERCENTAGE"].Value)
	assert.Equal(t, "8192", env["BATCH_SIZE"].Value)
	assert.Equal(t, "30s", env["BATCH_TIMEOUT"].Value)
	assert.Equal(t, "1000", env["SENDING_QUEUE_SIZE"].Value)
}

func TestBuildMetricsForwarderPodSpec_SecurityContext(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
	}
	resources := metricsForwarderResourceRequirements(search)

	t.Run("managed security context disabled", func(t *testing.T) {
		podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, false)
		assert.NotNil(t, podSpec.SecurityContext)
		assert.NotNil(t, podSpec.Containers[0].SecurityContext)
	})

	t.Run("managed security context enabled", func(t *testing.T) {
		podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, true)
		assert.Nil(t, podSpec.SecurityContext)
		assert.Nil(t, podSpec.Containers[0].SecurityContext)
	})
}

func TestBuildMetricsForwarderPodSpec_ContainerArgs(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
	}
	resources := metricsForwarderResourceRequirements(search)
	podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, false)

	assert.Equal(t, []string{"--config", "/etc/otelcol/config.yaml"}, podSpec.Containers[0].Args)
}

func TestMetricsForwarderLabels(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "my-search", Namespace: "ns"},
	}

	labels := metricsForwarderLabels(search)
	assert.Equal(t, "my-search-search-metrics-forwarder-0", labels["app"])
	assert.Equal(t, metricsForwarderLabelName, labels["component"])
	assert.Equal(t, "my-search", labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, "ns", labels[khandler.MongoDBSearchOwnerNamespaceLabel])
	_, hasCluster := labels[khandler.MongoDBSearchClusterNameLabel]
	assert.False(t, hasCluster, "single-cluster labels must not set cluster-name")

	memberLabels := metricsForwarderLabelsForCluster(search, "us-east", 3)
	assert.Equal(t, search.MetricsForwarderDeploymentNameForCluster(3), memberLabels["app"])
	assert.Equal(t, "my-search", memberLabels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, "ns", memberLabels[khandler.MongoDBSearchOwnerNamespaceLabel])
	assert.Equal(t, "us-east", memberLabels[khandler.MongoDBSearchClusterNameLabel])

	podLabels := metricsForwarderPodLabels(search)
	assert.Equal(t, "my-search-search-metrics-forwarder-0", podLabels["app"])
	assert.NotContains(t, podLabels, "component")
}

func TestMetricsForwarderResourceRequirements_Defaults(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
	}
	resources := metricsForwarderResourceRequirements(search)

	assert.Equal(t, resource.MustParse("100m"), resources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("128Mi"), resources.Requests[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("250m"), resources.Limits[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("256Mi"), resources.Limits[corev1.ResourceMemory])
}

func TestMetricsForwarderResourceRequirements_Override(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("300m"),
						},
					},
				},
			},
		},
	}
	resources := metricsForwarderResourceRequirements(search)

	// Overridden
	assert.Equal(t, resource.MustParse("300m"), resources.Requests[corev1.ResourceCPU])
}

func TestDeploymentConfigurationOverride_MetricsForwarder(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					Deployment: &v1.DeploymentConfiguration{
						SpecWrapper: v1.DeploymentSpecWrapper{
							Spec: appsv1.DeploymentSpec{
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Tolerations: []corev1.Toleration{
											{Key: "dedicated", Value: "metrics", Effect: corev1.TaintEffectNoSchedule},
										},
										NodeSelector: map[string]string{"node-type": "metrics"},
									},
								},
							},
						},
						MetadataWrapper: v1.DeploymentMetadataWrapper{
							Labels:      map[string]string{"custom-label": "value"},
							Annotations: map[string]string{"custom-annotation": "value"},
						},
					},
				},
			},
		},
	}

	resources := metricsForwarderResourceRequirements(search)
	podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, false)

	// Base spec: no tolerations
	assert.Empty(t, podSpec.Tolerations)

	// Simulate what ensureMetricsForwarderDeployment does
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   search.MetricsForwarderDeploymentNameForCluster(0),
			Labels: metricsForwarderLabels(search),
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: podSpec},
		},
	}

	depCfg := search.Spec.Observability.MetricsForwarder.Deployment
	dep.Spec = merge.DeploymentSpecs(dep.Spec, depCfg.SpecWrapper.Spec)
	dep.Labels = merge.StringToStringMap(dep.Labels, depCfg.MetadataWrapper.Labels)
	dep.Annotations = merge.StringToStringMap(dep.Annotations, depCfg.MetadataWrapper.Annotations)

	// Tolerations and node selector applied
	assert.Len(t, dep.Spec.Template.Spec.Tolerations, 1)
	assert.Equal(t, "dedicated", dep.Spec.Template.Spec.Tolerations[0].Key)
	assert.Equal(t, map[string]string{"node-type": "metrics"}, dep.Spec.Template.Spec.NodeSelector)

	// Labels and annotations merged
	assert.Equal(t, "value", dep.Labels["custom-label"])
	assert.Equal(t, "test-search-metrics-forwarder-0", dep.Labels["app"])
	assert.Equal(t, "value", dep.Annotations["custom-annotation"])

	// Container preserved
	assert.Len(t, dep.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, testDefaultImage, dep.Spec.Template.Spec.Containers[0].Image)
}

func TestDeploymentConfigurationOverride_MetricsForwarder_EnvVars(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					Deployment: &v1.DeploymentConfiguration{
						SpecWrapper: v1.DeploymentSpecWrapper{
							Spec: appsv1.DeploymentSpec{
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										// The container name must match the operator-created container
										// so the override merges into it rather than adding a new one.
										Containers: []corev1.Container{
											{
												Name: "metrics-forwarder",
												Env: []corev1.EnvVar{
													// Override an existing tuning env var.
													{Name: "BATCH_SIZE", Value: "4096"},
													// Add a brand new env var.
													{Name: "CUSTOM_ENV", Value: "custom-value"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	resources := metricsForwarderResourceRequirements(search)
	podSpec := buildMetricsForwarderPodSpec(search, "agent-key-secret", "", 0, testDefaultImage, resources, false)

	// Base spec uses the default BATCH_SIZE and has no custom env.
	baseEnv := envMap(podSpec.Containers[0].Env)
	assert.Equal(t, "8192", baseEnv["BATCH_SIZE"].Value)
	assert.NotContains(t, baseEnv, "CUSTOM_ENV")

	// Simulate what ensureMetricsForwarderDeployment does.
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: search.MetricsForwarderDeploymentNameForCluster(0)},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: podSpec},
		},
	}
	depCfg := search.Spec.Observability.MetricsForwarder.Deployment
	dep.Spec = merge.DeploymentSpecs(dep.Spec, depCfg.SpecWrapper.Spec)

	// The override container merged into the single existing container.
	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	mergedEnv := envMap(dep.Spec.Template.Spec.Containers[0].Env)

	// Overridden value wins.
	assert.Equal(t, "4096", mergedEnv["BATCH_SIZE"].Value)
	// New value is appended.
	assert.Equal(t, "custom-value", mergedEnv["CUSTOM_ENV"].Value)
	// Untouched defaults are preserved.
	assert.Equal(t, "30s", mergedEnv["BATCH_TIMEOUT"].Value)
}

func TestReconcile_EnterpriseSource_CreatesDeploymentAndConfigMap(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// Verify Deployment was created
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	require.NoError(t, err)
	assert.Equal(t, testDefaultImage, dep.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "metrics-forwarder", dep.Spec.Template.Spec.Containers[0].Name)

	// Verify ConfigMap was created
	cm := &corev1.ConfigMap{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderConfigMapNameForCluster(0),
	}, cm)
	require.NoError(t, err)
	assert.Contains(t, cm.Data, "config.yaml")

	// Verify status was updated
	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseRunning, updatedSearch.Status.MetricsForwarder.Phase)
}

func TestReconcile_DisabledMode_DeletesResources(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Observability = searchv1.ObservabilityConfig{
		MetricsForwarder: searchv1.MetricsForwarderConfig{
			Mode: searchv1.MetricsForwarderModeDisabled,
		},
	}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// Verify Deployment was NOT created
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil, "expected deployment to not exist")

	// Verify status shows disabled
	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseDisabled, updatedSearch.Status.MetricsForwarder.Phase)
}

// newTestMongoDBCommunity creates a minimal MongoDBCommunity source resource.
func newTestMongoDBCommunity(name, namespace string) *mdbcv1.MongoDBCommunity {
	return &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       mdbcv1.MongoDBCommunitySpec{Version: "8.2.0", Members: 3},
	}
}

func TestReconcile_CommunitySource_AddsNoFinalizer(t *testing.T) {
	// A MongoDBCommunity source does not run the forwarder, so the reconcile must add no finalizer —
	// one would leak and permanently block deletion of the MongoDBSearch.
	mdbc := newTestMongoDBCommunity(testMDBName, testNamespace)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdbc, search)
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updated := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	assert.NotContains(t, updated.Finalizers, util.SearchMetricsForwarderFinalizer)

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "no deployment should be created for a community source")
}

func TestReconcile_PrometheusDisabled_MetricsForwarderEnabled_Invalid(t *testing.T) {
	// When prometheus is explicitly disabled but the metrics forwarder is enabled (mode=enabled or
	// auto with internal source), the reconciler must report Invalid status because the forwarder
	// cannot scrape metrics without the prometheus endpoint.
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Observability = searchv1.ObservabilityConfig{
		Prometheus: searchv1.Prometheus{
			Mode: searchv1.PrometheusModeDisabled,
		},
		MetricsForwarder: searchv1.MetricsForwarderConfig{
			Mode: searchv1.MetricsForwarderModeEnabled,
		},
	}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search)
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Prometheus")
}

func TestReconcile_DeletionWhileDisabled_DeregistersHostsAndRemovesFinalizer(t *testing.T) {
	// Regression test: deleting a MongoDBSearch whose metrics forwarder was disabled must still
	// deregister its Ops Manager hosts and remove the finalizer. The disabled-mode reconcile path does
	// not own deletion handling, so without the top-level deletion check in Reconcile the finalizer
	// would leak (blocking deletion) and the monitored hosts would stay registered in Ops Manager.
	//
	// Because the forwarder was disabled, no Deployment was ever created. The two-phase deletion in
	// preDeletionCleanup completes in a single reconcile: phase 1 finds no Deployment to delete,
	// phase 2 sees no Deployment present, and the finalizer is removed immediately.
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Observability = searchv1.ObservabilityConfig{
		MetricsForwarder: searchv1.MetricsForwarderConfig{
			Mode: searchv1.MetricsForwarderModeDisabled,
		},
	}
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	// Internal enterprise sources resolve the agent key secret from the project id; see agents.ApiKeySecretName.
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	// Seed the topology state an enabled forwarder would have written: two mongot replicas.
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 2})

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM)

	// Capture the host ids passed to the Ops Manager delete-hosts API.
	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	// Trigger deletion: with the finalizer present the fake client sets a DeletionTimestamp instead of
	// removing the object outright.
	require.NoError(t, fakeClient.Delete(context.Background(), search))

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// Both mongot hosts from the persisted topology are deregistered from Ops Manager.
	stsName := search.StatefulSetNamespacedNameForCluster(0).Name
	assert.ElementsMatch(t, []string{
		mongotHostID(testGroupID, testNamespace, fmt.Sprintf("%s-0", stsName)),
		mongotHostID(testGroupID, testNamespace, fmt.Sprintf("%s-1", stsName)),
	}, deletedHostIDs)

	// The finalizer is removed, so the resource is fully deleted.
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: testSearchName}, &searchv1.MongoDBSearch{})
	assert.True(t, apierrors.IsNotFound(err), "expected MongoDBSearch to be deleted after finalizer removal, got err=%v", err)
}

func TestReconcile_DeletionWhileEnabled_WaitsForDeploymentThenDeregistersHosts(t *testing.T) {
	// When a MongoDBSearch is deleted while the forwarder is enabled, preDeletionCleanup must not
	// deregister OM hosts until the forwarder Deployment has been fully deleted. A live collector
	// would continue pushing metrics for those hosts, causing OM to implicitly re-add them and
	// making the deregistration a no-op.
	//
	// Phase 1 (first reconcile): the Deployment exists → deleted, Pending returned.
	// Phase 2 (second reconcile): Deployment is gone → hosts deregistered, finalizer removed.
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 1})

	// Pre-create a Deployment to simulate the forwarder having been running.
	existingDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.MetricsForwarderDeploymentNameForCluster(0),
			Namespace: testNamespace,
		},
	}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM, existingDep)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	require.NoError(t, fakeClient.Delete(context.Background(), search))

	// First reconcile: Deployment still exists → preDeletionCleanup deletes it and returns Pending.
	result1 := reconcileMetricsForwarder(t, r, testNamespace, testSearchName)
	assert.True(t, result1.RequeueAfter > 0 || result1.Requeue, "expected requeue on first deletion reconcile")
	assert.Empty(t, deletedHostIDs, "expected no host deregistration while Deployment still exists")

	// The Deployment should now be gone (deleted by phase 1).
	depErr := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: existingDep.Name}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(depErr), "expected Deployment to be deleted after first reconcile")

	// Second reconcile: Deployment is gone → hosts deregistered and finalizer removed.
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	stsName := search.StatefulSetNamespacedNameForCluster(0).Name
	assert.ElementsMatch(t, []string{
		mongotHostID(testGroupID, testNamespace, fmt.Sprintf("%s-0", stsName)),
	}, deletedHostIDs)

	finalErr := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: testSearchName}, &searchv1.MongoDBSearch{})
	assert.True(t, apierrors.IsNotFound(finalErr), "expected MongoDBSearch to be deleted after finalizer removal")
}

func TestReconcile_ScaleDown_DefersHostDeletionUntilPodTerminated(t *testing.T) {
	// A mongot host must not be deregistered while its pod is actively terminating: the OTel forwarder
	// keeps scraping until the process exits and OM would re-register the host. When a scaled-down pod
	// has a DeletionTimestamp (Kubernetes is terminating it), host deletion is deferred.
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName) // defaults to 1 replica
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	// Previous topology had 2 replicas, so my-search-search-1 was removed by scaling down to 1.
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 2})
	removedPodName := fmt.Sprintf("%s-1", search.StatefulSetNamespacedNameForCluster(0).Name)
	// The removed pod is actively terminating (DeletionTimestamp set).
	now := metav1.Now()
	terminatingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              removedPodName,
			Namespace:         testNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{"kubernetes"}, // required for fake client to set DeletionTimestamp
		},
	}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM, terminatingPod)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	result := reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// No host is deregistered while the pod has a DeletionTimestamp, and the pod is recorded as pending.
	assert.Empty(t, deletedHostIDs, "no host should be deregistered while the mongot pod is terminating")
	state := getTopologyState(t, fakeClient, search)
	assert.Equal(t, []string{removedPodName}, state.PendingHostDeletions)
	// The reconcile is requeued to retry once the pod terminates.
	assert.Equal(t, 15*time.Second, result.RequeueAfter)
}

func TestReconcile_NoDefaultImage_FailsInvalid(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler("", mdb, search, projectCM) // empty image
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, util.MetricsForwarderImageEnv)
}

func TestReconcile_EnterpriseSource_NoProjectID_Pending(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, "")
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhasePending, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "project")
}

func TestReconcile_NoStatusVersion_Pending(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	// The main Search controller has not reported a version yet, so the forwarder must wait.
	search.Status.Version = ""
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhasePending, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "version")

	// No Deployment should be created while the version is unknown.
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	assert.True(t, apierrors.IsNotFound(err), "expected no metrics forwarder Deployment, got err=%v", err)
}

func TestReconcile_ExplicitProjectConfig(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: testSearchName, Namespace: testNamespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mongo.example.com:27017"},
				},
			},
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					Mode: searchv1.MetricsForwarderModeEnabled,
					OpsManager: &searchv1.MetricsForwarderOpsManagerConfig{
						AgentCredentials:    corev1.LocalObjectReference{Name: "my-agent-secret"},
						ProjectConfigMapRef: corev1.LocalObjectReference{Name: testProjectCMName},
					},
				},
			},
			Clusters: []searchv1.ClusterSpec{{Name: ""}},
		},
		Status: searchv1.MongoDBSearchStatus{
			Version: "1.0.0",
		},
	}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret("my-agent-secret", testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, projectCM, agentSecret)
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// Verify Deployment was created with the explicit agent secret
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	require.NoError(t, err)

	// Check that the agent-api-key volume uses the forwarder-owned replicated copy.
	found := false
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == "agent-api-key" && vol.Secret != nil {
			assert.Equal(t, search.MetricsForwarderAgentKeySecretNameForCluster(0), vol.Secret.SecretName)
			found = true
		}
	}
	assert.True(t, found, "expected agent-api-key volume with replicated copy secret")

	// The group id resolved from the agent key via the OM agent API (served here by the stub requester)
	// must be baked into the metrics forwarder ConfigMap, proving the external/explicit group-resolution path ran.
	cm := &corev1.ConfigMap{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderConfigMapNameForCluster(0),
	}, cm)
	require.NoError(t, err)
	assert.Contains(t, cm.Data["config.yaml"], testGroupID)

	// Status should be OK
	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseRunning, updatedSearch.Status.MetricsForwarder.Phase)
}

// TestReconcile_ShardedSource_ConfigMapUsesClusterIndex verifies that for a sharded source on a
// non-zero-index cluster, the rendered OTel config's shard-name extraction regex uses that cluster's
// index. A hardcoded index would match nothing on member clusters with index != 0, silently failing
// to attribute per-shard metrics in Ops Manager.
func TestReconcile_ShardedSource_ConfigMapUsesClusterIndex(t *testing.T) {
	const clusterIndex = 2
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: testSearchName, Namespace: testNamespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					ShardedCluster: &searchv1.ExternalShardedClusterConfig{
						Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
						Shards: []searchv1.ExternalShardConfig{
							{ShardName: "shard0", Hosts: []string{"shard0.example.com:27017"}},
						},
					},
				},
			},
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					Mode: searchv1.MetricsForwarderModeEnabled,
					OpsManager: &searchv1.MetricsForwarderOpsManagerConfig{
						AgentCredentials:    corev1.LocalObjectReference{Name: "my-agent-secret"},
						ProjectConfigMapRef: corev1.LocalObjectReference{Name: testProjectCMName},
					},
				},
			},
			Clusters: []searchv1.ClusterSpec{{Name: "", Index: ptr.To(int32(clusterIndex))}},
		},
		Status: searchv1.MongoDBSearchStatus{Version: "1.0.0"},
	}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret("my-agent-secret", testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, projectCM, agentSecret)
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	cm := &corev1.ConfigMap{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderConfigMapNameForCluster(clusterIndex),
	}, cm)
	require.NoError(t, err)

	assert.Contains(t, cm.Data["config.yaml"], fmt.Sprintf("%s-search-%d-(.+)-svc", testSearchName, clusterIndex),
		"shard.name regex must extract using the cluster index")
	assert.NotContains(t, cm.Data["config.yaml"], fmt.Sprintf("%s-search-0-(.+)-svc", testSearchName),
		"shard.name regex must not hardcode index 0 for a non-zero-index cluster")
}

func TestReconcile_ExternalSource_NoOpsManagerConfig_Invalid(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: testSearchName, Namespace: testNamespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mongo.example.com:27017"},
				},
			},
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					Mode: searchv1.MetricsForwarderModeEnabled,
				},
			},
			Clusters: []searchv1.ClusterSpec{{Name: ""}},
		},
		Status: searchv1.MongoDBSearchStatus{
			Version: "1.0.0",
		},
	}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search)
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "opsManager")
}

func TestReconcile_AutoMode_InternalEnterprise_EnabledByDefault(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	// No explicit metrics forwarder config - auto mode by default
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// Should create the Deployment since auto mode with enterprise source enables it
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	require.NoError(t, err)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseRunning, updatedSearch.Status.MetricsForwarder.Phase)
}

func TestReconcile_ConfigMapHash_TriggersRollout(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// Get the config hash annotation
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	require.NoError(t, err)

	hash1 := dep.Spec.Template.Annotations[metricsForwarderConfigHashAnnotation]
	assert.NotEmpty(t, hash1)

	// Reconcile again with same config - hash should remain the same
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	require.NoError(t, err)

	hash2 := dep.Spec.Template.Annotations[metricsForwarderConfigHashAnnotation]
	assert.Equal(t, hash1, hash2)
}

func TestReconcile_SourceNotFound_Pending(t *testing.T) {
	// MongoDBSearch references a MongoDB that doesn't exist
	search := newTestMongoDBSearch(testSearchName, testNamespace, "nonexistent-mdb")

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search)
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhasePending, updatedSearch.Status.MetricsForwarder.Phase)
}

func TestReconcile_WithCACert(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)

	// Project ConfigMap with CA cert reference
	projectCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testProjectCMName, Namespace: testNamespace},
		Data: map[string]string{
			util.OmBaseUrl:         testOMBaseURL,
			util.OmProjectName:     "test-project",
			util.OmOrgId:           "test-org-id",
			util.SSLMMSCAConfigMap: "my-ca-configmap",
			util.SSLRequireValidMMSServerCertificates: "true",
		},
	}
	caCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-ca-configmap", Namespace: testNamespace},
		Data:       map[string]string{util.CaCertMMS: "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----"},
	}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, caCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	// Verify Deployment has the CA volume
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	require.NoError(t, err)

	foundCAVolume := false
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == metricsForwarderCACertVolumeName {
			assert.Equal(t, search.MetricsForwarderCACertConfigMapNameForCluster(0), vol.ConfigMap.Name)
			foundCAVolume = true
		}
	}
	assert.True(t, foundCAVolume, "expected mms-ca-cert volume to be present")

	var caCertMountPath string
	for _, vm := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
		if vm.Name == metricsForwarderCACertVolumeName {
			caCertMountPath = vm.MountPath
			break
		}
	}
	require.NotEmpty(t, caCertMountPath, "expected mms-ca-cert volume mount to be present")

	// Verify the collector ConfigMap has ca_file pointing to the mounted volume path.
	cm := &corev1.ConfigMap{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderConfigMapNameForCluster(0),
	}, cm)
	require.NoError(t, err)
	assert.Contains(t, cm.Data["config.yaml"], "ca_file: "+caCertMountPath+"/"+util.CaCertMMS,
		"expected exporters.otlp_http.tls.ca_file to point to the mounted CA cert volume")
}

func TestResolveFromEnterpriseSource(t *testing.T) {
	r := &MongoDBSearchMetricsForwarderReconciler{}

	t.Run("with project ID", func(t *testing.T) {
		mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
		ctx, groupId, st := r.resolveFromEnterpriseSource(mdb)
		assert.True(t, st.IsOK())
		assert.Equal(t, testGroupID, groupId)
		assert.Equal(t, testProjectCMName, ctx.projectConfigMapRef.Name)
		assert.Equal(t, testGroupID+"-group-secret", ctx.agentApiKeySecret.Name)
	})

	t.Run("without project ID", func(t *testing.T) {
		mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, "")
		_, _, st := r.resolveFromEnterpriseSource(mdb)
		assert.False(t, st.IsOK())
	})
}

func TestResolveFromExplicitProjectConfig(t *testing.T) {
	r := &MongoDBSearchMetricsForwarderReconciler{}

	t.Run("with valid config", func(t *testing.T) {
		search := &searchv1.MongoDBSearch{
			Spec: searchv1.MongoDBSearchSpec{
				Observability: searchv1.ObservabilityConfig{
					MetricsForwarder: searchv1.MetricsForwarderConfig{
						OpsManager: &searchv1.MetricsForwarderOpsManagerConfig{
							AgentCredentials:    corev1.LocalObjectReference{Name: "my-secret"},
							ProjectConfigMapRef: corev1.LocalObjectReference{Name: "my-cm"},
						},
					},
				},
			},
		}
		ctx, st := r.resolveFromExplicitProjectConfig(search)
		assert.True(t, st.IsOK())
		assert.Equal(t, "my-cm", ctx.projectConfigMapRef.Name)
		assert.Equal(t, "my-secret", ctx.agentApiKeySecret.Name)
	})

	t.Run("nil metricsForwarder", func(t *testing.T) {
		search := &searchv1.MongoDBSearch{}
		_, st := r.resolveFromExplicitProjectConfig(search)
		assert.False(t, st.IsOK())
	})

	t.Run("nil opsManager", func(t *testing.T) {
		search := &searchv1.MongoDBSearch{
			Spec: searchv1.MongoDBSearchSpec{
				Observability: searchv1.ObservabilityConfig{
					MetricsForwarder: searchv1.MetricsForwarderConfig{},
				},
			},
		}
		_, st := r.resolveFromExplicitProjectConfig(search)
		assert.False(t, st.IsOK())
	})
}

func TestComputeDeletedMongotPods(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)

	tests := []struct {
		name     string
		previous clusterTopologyState
		current  clusterTopologyState
		expected []string
	}{
		{
			name:     "unchanged non-sharded topology is a no-op",
			previous: clusterTopologyState{Replicas: 3},
			current:  clusterTopologyState{Replicas: 3},
			expected: nil,
		},
		{
			name:     "non-sharded scale-down deletes trailing pods",
			previous: clusterTopologyState{Replicas: 3},
			current:  clusterTopologyState{Replicas: 1},
			expected: []string{"my-search-search-0-1", "my-search-search-0-2"},
		},
		{
			name:     "non-sharded scale-up deletes nothing",
			previous: clusterTopologyState{Replicas: 1},
			current:  clusterTopologyState{Replicas: 3},
			expected: nil,
		},
		{
			name:     "removed shard deletes all of its pods",
			previous: clusterTopologyState{ShardReplicas: map[string]int{"shard0": 2, "shard1": 2}},
			current:  clusterTopologyState{ShardReplicas: map[string]int{"shard0": 2}},
			expected: []string{"my-search-search-0-shard1-0", "my-search-search-0-shard1-1"},
		},
		{
			name:     "per-shard scale-down deletes trailing pods of surviving shard",
			previous: clusterTopologyState{ShardReplicas: map[string]int{"shard0": 3, "shard1": 3}},
			current:  clusterTopologyState{ShardReplicas: map[string]int{"shard0": 3, "shard1": 1}},
			expected: []string{"my-search-search-0-shard1-1", "my-search-search-0-shard1-2"},
		},
		{
			name:     "removed shard and per-shard scale-down combined",
			previous: clusterTopologyState{ShardReplicas: map[string]int{"shard0": 2, "shard1": 2}},
			current:  clusterTopologyState{ShardReplicas: map[string]int{"shard0": 1}},
			expected: []string{"my-search-search-0-shard0-1", "my-search-search-0-shard1-0", "my-search-search-0-shard1-1"},
		},
		{
			name:     "unchanged sharded topology is a no-op",
			previous: clusterTopologyState{ShardReplicas: map[string]int{"shard0": 2, "shard1": 2}},
			current:  clusterTopologyState{ShardReplicas: map[string]int{"shard0": 2, "shard1": 2}},
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeDeletedMongotPods(search, 0, tc.previous, tc.current)
			assert.ElementsMatch(t, tc.expected, got)
		})
	}
}

// recordingOMAgentRequester captures the last agent-auth request and returns a canned response.
type recordingOMAgentRequester struct {
	resp      []byte
	err       error
	called    int
	gotMethod string
	gotPath   string
	gotAuth   string
	gotBody   any
}

func (r *recordingOMAgentRequester) RequestWithAgentAuth(_ mdbv1.ProjectConfig, method, path, authHeader string, body any) ([]byte, error) {
	r.called++
	r.gotMethod, r.gotPath, r.gotAuth, r.gotBody = method, path, authHeader, body
	return r.resp, r.err
}

func (r *recordingOMAgentRequester) GetOMVersion(_ mdbv1.ProjectConfig) (versionutil.OpsManagerVersion, error) {
	// recordingOMAgentRequester is used only for host-deletion tests; return a supported version.
	return versionutil.OpsManagerVersion{VersionString: metricsForwarderMinOpsManagerVersion}, nil
}

func TestCleanupRemovedMongotPods(t *testing.T) {
	const groupID = "grp-123"
	const agentSecretName = "agent-key-secret"
	podNames := []string{"my-search-search-0", "my-search-search-1"}

	wantHostIDs := []string{
		mongotHostID(groupID, testNamespace, podNames[0]),
		mongotHostID(groupID, testNamespace, podNames[1]),
	}

	// marshalResults builds a delete-hosts response body pairing each host id with the given status.
	marshalResults := func(statuses ...string) []byte {
		results := make([]deleteHostResult, len(statuses))
		for i, s := range statuses {
			results[i] = deleteHostResult{HostId: wantHostIDs[i], Status: s}
		}
		b, err := json.Marshal(deleteHostsResponse{Results: results})
		require.NoError(t, err)
		return b
	}

	tests := []struct {
		name        string
		resp        []byte
		respErr     error
		wantErr     string // substring; empty means no error expected
		wantHostIDs []string
	}{
		{
			name:        "all hosts deleted",
			resp:        marshalResults("DELETED", "DELETED"),
			wantHostIDs: wantHostIDs,
		},
		{
			name: "not found is treated as success",
			resp: marshalResults("NOT_FOUND", "DELETED"),
		},
		{
			name: "automation-managed host is skipped without error",
			resp: marshalResults("SKIPPED_MANAGED", "DELETED"),
		},
		{
			name:    "error status fails with the host id",
			resp:    marshalResults("ERROR", "DELETED"),
			wantErr: wantHostIDs[0],
		},
		{
			name:    "unexpected status fails",
			resp:    marshalResults("WAT", "DELETED"),
			wantErr: wantHostIDs[0],
		},
		{
			name:    "requester error is wrapped",
			respErr: fmt.Errorf("boom"),
			wantErr: "failed to call delete hosts API",
		},
		{
			name:    "malformed response body fails to parse",
			resp:    []byte("{not json"),
			wantErr: "failed to parse delete hosts API response",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
			agentSecret := newTestAgentKeySecret(agentSecretName, testNamespace)
			r, _ := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret)
			rec := &recordingOMAgentRequester{resp: tc.resp, err: tc.respErr}
			r.omRequester = rec

			err := r.cleanupRemovedMongotPods(context.Background(), search, podNames, groupID,
				mdbv1.ProjectConfig{BaseURL: testOMBaseURL}, agentSecretName, zap.NewNop().Sugar())

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			// The request is always a POST to the group-scoped delete endpoint carrying the computed host ids.
			require.Equal(t, 1, rec.called)
			assert.Equal(t, "POST", rec.gotMethod)
			assert.Equal(t, fmt.Sprintf("/agents/api/hosts/%s/v1/delete", groupID), rec.gotPath)
			require.IsType(t, deleteHostsRequest{}, rec.gotBody)
			assert.ElementsMatch(t, wantHostIDs, rec.gotBody.(deleteHostsRequest).HostIds)
		})
	}
}

func TestCleanupRemovedMongotPods_NoPodsSkipsRequest(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, _ := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret)
	rec := &recordingOMAgentRequester{}
	r.omRequester = rec

	err := r.cleanupRemovedMongotPods(context.Background(), search, nil, "grp-123",
		mdbv1.ProjectConfig{BaseURL: testOMBaseURL}, "agent-key-secret", zap.NewNop().Sugar())

	require.NoError(t, err)
	assert.Equal(t, 0, rec.called, "no hosts to delete should not call Ops Manager")
}

// newTestMongoDBSearchWithExplicitOpsManager builds a MongoDBSearch that explicitly sets
// .spec.observability.metricsForwarder.opsManager (pointing to the given project CM and credentials).
// Mode is set to Auto so the Reconcile switch does not fall through to the default/invalid branch.
func newTestMongoDBSearchWithExplicitOpsManager(name, namespace, mdbName, projectCMName, agentCredsName string) *searchv1.MongoDBSearch {
	s := newTestMongoDBSearch(name, namespace, mdbName)
	s.Spec.Observability = searchv1.ObservabilityConfig{
		MetricsForwarder: searchv1.MetricsForwarderConfig{
			Mode: searchv1.MetricsForwarderModeAuto,
			OpsManager: &searchv1.MetricsForwarderOpsManagerConfig{
				ProjectConfigMapRef: corev1.LocalObjectReference{Name: projectCMName},
				AgentCredentials:    corev1.LocalObjectReference{Name: agentCredsName},
			},
		},
	}
	return s
}

// newTestMongoDBSearchExternal builds a MongoDBSearch with an external (non-MCK-managed) MongoDB source
// and an explicit .spec.observability.metricsForwarder.opsManager.
// Mode is set to Auto so the Reconcile switch does not fall through to the default/invalid branch.
func newTestMongoDBSearchExternal(name, namespace, projectCMName, agentCredsName string) *searchv1.MongoDBSearch {
	s := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mongodb.example.com:27017"},
				},
			},
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					Mode: searchv1.MetricsForwarderModeAuto,
					OpsManager: &searchv1.MetricsForwarderOpsManagerConfig{
						ProjectConfigMapRef: corev1.LocalObjectReference{Name: projectCMName},
						AgentCredentials:    corev1.LocalObjectReference{Name: agentCredsName},
					},
				},
			},
			Clusters: []searchv1.ClusterSpec{{Name: ""}},
		},
		Status: searchv1.MongoDBSearchStatus{Version: "1.0.0"},
	}
	return s
}

// TestMetricsForwarder_OMVersionTooOld_ImplicitConnection: the connection is inferred from the
// underlying MongoDB resource (no explicit .opsManager override). When OM is too old the forwarder
// must report Unsupported and not deploy any resources.
func TestMetricsForwarder_OMVersionTooOld_ImplicitConnection(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: "8.0.0"})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseUnsupported, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, metricsForwarderMinOpsManagerVersion)

	// No Deployment must exist.
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created for unsupported OM version")
}

// TestMetricsForwarder_OMVersionTooOld_ExplicitConnection: the user explicitly set .opsManager.
// When OM is too old the forwarder must report Failed (stronger signal) and not deploy resources.
func TestMetricsForwarder_OMVersionTooOld_ExplicitConnection(t *testing.T) {
	const agentCredsName = "my-agent-creds"
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearchWithExplicitOpsManager(testSearchName, testNamespace, testMDBName, testProjectCMName, agentCredsName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(agentCredsName, testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentSecret)
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: "8.0.0"})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, metricsForwarderMinOpsManagerVersion)

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created for unsupported OM version")
}

// TestMetricsForwarder_ExternalSource_OMVersionTooOld: external sources always use the explicit path.
// When OM is too old the forwarder must report Failed.
func TestMetricsForwarder_ExternalSource_OMVersionTooOld(t *testing.T) {
	const agentCredsName = "my-agent-creds"
	search := newTestMongoDBSearchExternal(testSearchName, testNamespace, testProjectCMName, agentCredsName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(agentCredsName, testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, projectCM, agentSecret)
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: "8.0.0"})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, metricsForwarderMinOpsManagerVersion)
}

// TestMetricsForwarder_CloudManager_ImplicitConnection: implicit connection pointing at Cloud Manager.
// The metrics forwarding endpoint is only available on self-hosted OM; the check reports Unsupported.
func TestMetricsForwarder_CloudManager_ImplicitConnection(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: "v20240101"})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseUnsupported, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Cloud Manager")

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created when OM is Cloud Manager")
}

// TestMetricsForwarder_CloudManager_ExplicitConnection: user explicitly configured an OM connection
// that resolves to Cloud Manager. The metrics forwarding endpoint is unavailable on Cloud Manager,
// so the forwarder reports Failed (strong signal — user explicitly configured it).
func TestMetricsForwarder_CloudManager_ExplicitConnection(t *testing.T) {
	const agentCredsName = "my-agent-creds"
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearchWithExplicitOpsManager(testSearchName, testNamespace, testMDBName, testProjectCMName, agentCredsName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(agentCredsName, testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentSecret)
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: "v20240101"})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Cloud Manager")

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created for Cloud Manager explicit connection")
}

// TestMetricsForwarder_OMVersionSupported: OM at exactly the minimum version proceeds normally.
func TestMetricsForwarder_OMVersionSupported(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret)
	// Default stub already returns 8.0.25; being explicit here for clarity.
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: metricsForwarderMinOpsManagerVersion})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	dep := &appsv1.Deployment{}
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep))

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseRunning, updatedSearch.Status.MetricsForwarder.Phase)
}

// TestMetricsForwarder_OMVersionFetchError_Pending: when OM is unreachable the reconciler must
// report Pending and not deploy any resources, so it retries once OM is available.
func TestMetricsForwarder_OMVersionFetchError_Pending(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	stub := newStubOMAgentRequester(testGroupID)
	stub.getOMVersionFn = func(_ mdbv1.ProjectConfig) (versionutil.OpsManagerVersion, error) {
		return versionutil.OpsManagerVersion{}, fmt.Errorf("connection refused")
	}
	r.omRequester = stub

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhasePending, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Checking Ops Manager version compatibility")

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created while OM version check is pending")
}

// TestMetricsForwarder_OMVersionUnknown_Unsupported: an empty/unknown version string reports
// Unsupported because we cannot confirm the endpoint is available.
func TestMetricsForwarder_OMVersionUnknown_Unsupported(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: ""})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseUnsupported, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Could not determine Ops Manager version")

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created when OM version is unknown")
}

// TestMetricsForwarder_OMVersionSemverParseError_Unsupported: unparseable version + implicit connection
// reports Unsupported.
func TestMetricsForwarder_OMVersionSemverParseError_Unsupported(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace))
	// "a.b.c" has three dot-segments so OpsManagerVersion.Semver() attempts semver.Make("a.b.c"),
	// which fails because the components are non-numeric.
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: "a.b.c"})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseUnsupported, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Could not determine Ops Manager version")

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created when OM version cannot be parsed")
}

// TestMetricsForwarder_OMVersionUnknown_ExplicitConnection_Failed: unknown version + explicit connection
// reports Failed (user explicitly configured .opsManager, so a stronger signal is warranted).
func TestMetricsForwarder_OMVersionUnknown_ExplicitConnection_Failed(t *testing.T) {
	const agentCredsName = "my-agent-creds"
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearchWithExplicitOpsManager(testSearchName, testNamespace, testMDBName, testProjectCMName, agentCredsName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(agentCredsName, testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentSecret)
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: ""})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Could not determine Ops Manager version")

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created when OM version is unknown")
}

// TestMetricsForwarder_OMVersionSemverParseError_ExplicitConnection_Failed: unparseable version +
// explicit connection reports Failed.
func TestMetricsForwarder_OMVersionSemverParseError_ExplicitConnection_Failed(t *testing.T) {
	const agentCredsName = "my-agent-creds"
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearchWithExplicitOpsManager(testSearchName, testNamespace, testMDBName, testProjectCMName, agentCredsName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(agentCredsName, testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentSecret)
	r.omRequester = newStubOMAgentRequesterWithVersion(testGroupID, versionutil.OpsManagerVersion{VersionString: "a.b.c"})

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseFailed, updatedSearch.Status.MetricsForwarder.Phase)
	assert.Contains(t, updatedSearch.Status.MetricsForwarder.Message, "Could not determine Ops Manager version")

	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: search.MetricsForwarderDeploymentNameForCluster(0)}, dep)
	assert.True(t, apierrors.IsNotFound(err), "deployment should not be created when OM version cannot be parsed")
}

// callReconcileTopologyState invokes reconcileTopologyState directly against the
// single-cluster (clusterName=="", clusterIndex=0) work item, bypassing the full
// Reconcile path so each test targets one state-machine transition.
func callReconcileTopologyState(t *testing.T, r *MongoDBSearchMetricsForwarderReconciler, search *searchv1.MongoDBSearch, shardNames []string, agentSecretName string) (bool, error) {
	t.Helper()
	projectConfig := mdbv1.ProjectConfig{BaseURL: testOMBaseURL}
	w := clusterWorkItem{ClusterName: "", ClusterIndex: 0, Client: r.kubeClient}
	return r.reconcileTopologyState(context.Background(), search, shardNames, testGroupID, projectConfig, agentSecretName, w, zap.NewNop().Sugar())
}

// newTestMongoDBSearchWithReplicas creates a MongoDBSearch with an explicit replica count on the
// single (clusterName=="") cluster.
func newTestMongoDBSearchWithReplicas(name, namespace, mdbName string, replicas int32) *searchv1.MongoDBSearch {
	s := newTestMongoDBSearch(name, namespace, mdbName)
	s.Spec.Clusters = []searchv1.ClusterSpec{{Name: "", Replicas: &replicas}}
	return s
}

// TestReconcileTopologyState_FirstReconcile_RecordsCurrentReplicas verifies that the first call
// creates a state ConfigMap recording the current replica count and returns pending=false.
func TestReconcileTopologyState_FirstReconcile_RecordsCurrentReplicas(t *testing.T) {
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 2)
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callReconcileTopologyState(t, r, search, nil, "agent-key-secret")
	require.NoError(t, err)
	assert.False(t, pending, "no pending deletions on first reconcile")
	assert.Empty(t, deletedHostIDs, "no hosts to deregister on first reconcile")

	state := getTopologyState(t, fakeClient, search)
	assert.Equal(t, 2, state.Replicas)
	assert.Empty(t, state.PendingHostDeletions)
	assert.Empty(t, state.HostDeletionReadyAfter)
}

// TestReconcileTopologyState_StableTopology_NoAction verifies that a reconcile with no topology
// change records the same replica count and neither deregisters any host nor returns pending.
func TestReconcileTopologyState_StableTopology_NoAction(t *testing.T) {
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 3)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 3})
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret, stateCM)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callReconcileTopologyState(t, r, search, nil, "agent-key-secret")
	require.NoError(t, err)
	assert.False(t, pending)
	assert.Empty(t, deletedHostIDs)

	state := getTopologyState(t, fakeClient, search)
	assert.Equal(t, 3, state.Replicas)
	assert.Empty(t, state.PendingHostDeletions)
	assert.Empty(t, state.HostDeletionReadyAfter)
}

// TestReconcileTopologyState_ScaleDown_PodGone_EntersDeferralWindow verifies that a newly
// detected removed pod that is already gone (NotFound, no DeletionTimestamp) enters the
// HostDeletionReadyAfter map rather than being cleaned up immediately. The deferral window
// prevents the OTel forwarder from pushing metrics after the OM host is deregistered.
func TestReconcileTopologyState_ScaleDown_PodGone_EntersDeferralWindow(t *testing.T) {
	// Current: 1 replica. Previous: 2 replicas → pod stsName-1 is a new candidate.
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 1)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 2})
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret, stateCM)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callReconcileTopologyState(t, r, search, nil, "agent-key-secret")
	require.NoError(t, err)
	assert.True(t, pending, "pending because deferral window has not elapsed")
	assert.Empty(t, deletedHostIDs, "no OM call before deferral window elapses")

	stsName := search.StatefulSetNamespacedNameForCluster(0).Name
	removedPodName := fmt.Sprintf("%s-1", stsName)
	state := getTopologyState(t, fakeClient, search)
	assert.Empty(t, state.PendingHostDeletions)
	require.Contains(t, state.HostDeletionReadyAfter, removedPodName)
	assert.Greater(t, state.HostDeletionReadyAfter[removedPodName], time.Now().UnixNano(),
		"readyAt timestamp must be in the future")
}

// TestReconcileTopologyState_ScaleDown_PodTerminating_StaysPending verifies that a removed pod
// whose K8s pod still has a DeletionTimestamp moves into PendingHostDeletions. OM deregistration
// must wait until the pod is fully gone to avoid a race with the in-flight scrape cycle.
func TestReconcileTopologyState_ScaleDown_PodTerminating_StaysPending(t *testing.T) {
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 1)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 2})
	stsName := search.StatefulSetNamespacedNameForCluster(0).Name
	removedPodName := fmt.Sprintf("%s-1", stsName)
	now := metav1.Now()
	terminatingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              removedPodName,
			Namespace:         testNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{"kubernetes"}, // required for the fake client to honour DeletionTimestamp
		},
	}
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret, stateCM, terminatingPod)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callReconcileTopologyState(t, r, search, nil, "agent-key-secret")
	require.NoError(t, err)
	assert.True(t, pending)
	assert.Empty(t, deletedHostIDs)

	state := getTopologyState(t, fakeClient, search)
	assert.Equal(t, []string{removedPodName}, state.PendingHostDeletions)
	assert.Empty(t, state.HostDeletionReadyAfter)
}

// TestReconcileTopologyState_PendingPod_NowGone_MovesToDeferral verifies that a pod that was
// previously terminating and has since disappeared moves from PendingHostDeletions into
// HostDeletionReadyAfter (deferral window starts from when the pod disappears).
func TestReconcileTopologyState_PendingPod_NowGone_MovesToDeferral(t *testing.T) {
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 1)
	stsName := search.StatefulSetNamespacedNameForCluster(0).Name
	removedPodName := fmt.Sprintf("%s-1", stsName)
	// Previous state: pod was terminating. Current replica count matches (already scaled), so
	// computeDeletedMongotPods produces nothing new — the candidate comes from PendingHostDeletions.
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		Replicas:             1,
		PendingHostDeletions: []string{removedPodName},
	})
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret, stateCM)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callReconcileTopologyState(t, r, search, nil, "agent-key-secret")
	require.NoError(t, err)
	assert.True(t, pending, "deferral window has not elapsed yet")
	assert.Empty(t, deletedHostIDs)

	state := getTopologyState(t, fakeClient, search)
	assert.Empty(t, state.PendingHostDeletions, "pod must have left PendingHostDeletions")
	require.Contains(t, state.HostDeletionReadyAfter, removedPodName)
	assert.Greater(t, state.HostDeletionReadyAfter[removedPodName], time.Now().UnixNano(),
		"readyAt timestamp must be in the future")
}

// TestReconcileTopologyState_DeferralWindowElapsed_DeregistersOMHost verifies that once the
// readyAt timestamp has passed the pod's OM host is deregistered and the entry is removed from state.
func TestReconcileTopologyState_DeferralWindowElapsed_DeregistersOMHost(t *testing.T) {
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 1)
	stsName := search.StatefulSetNamespacedNameForCluster(0).Name
	removedPodName := fmt.Sprintf("%s-1", stsName)
	// Timestamp in the past: deferral window has already elapsed.
	pastTimestamp := time.Now().Add(-1 * time.Second).UnixNano()
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		Replicas:               1,
		HostDeletionReadyAfter: map[string]int64{removedPodName: pastTimestamp},
	})
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret, stateCM)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callReconcileTopologyState(t, r, search, nil, "agent-key-secret")
	require.NoError(t, err)
	assert.False(t, pending, "all hosts cleaned up — nothing more to wait for")
	require.Len(t, deletedHostIDs, 1)
	assert.Equal(t, mongotHostID(testGroupID, testNamespace, removedPodName), deletedHostIDs[0])

	state := getTopologyState(t, fakeClient, search)
	assert.Empty(t, state.PendingHostDeletions)
	assert.Empty(t, state.HostDeletionReadyAfter, "entry removed after successful deregistration")
}

// TestReconcileTopologyState_DeferralWindowNotElapsed_TimestampPreserved verifies that when the
// readyAt timestamp is still in the future the existing value is kept unchanged across reconciles.
// Without preservation the deferral clock would reset on every reconcile and cleanup would never happen.
func TestReconcileTopologyState_DeferralWindowNotElapsed_TimestampPreserved(t *testing.T) {
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 1)
	stsName := search.StatefulSetNamespacedNameForCluster(0).Name
	removedPodName := fmt.Sprintf("%s-1", stsName)
	futureTimestamp := time.Now().Add(hostDeletionDeferralWindow).UnixNano()
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		Replicas:               1,
		HostDeletionReadyAfter: map[string]int64{removedPodName: futureTimestamp},
	})
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret, stateCM)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callReconcileTopologyState(t, r, search, nil, "agent-key-secret")
	require.NoError(t, err)
	assert.True(t, pending)
	assert.Empty(t, deletedHostIDs)

	state := getTopologyState(t, fakeClient, search)
	assert.Equal(t, futureTimestamp, state.HostDeletionReadyAfter[removedPodName],
		"existing readyAt timestamp must be preserved, not reset to now+window")
}

// TestReconcileTopologyState_Sharded_RemovedShardAndScaleDown verifies that pods from a removed
// shard and pods from a shard that scaled down both become candidates simultaneously.
func TestReconcileTopologyState_Sharded_RemovedShardAndScaleDown(t *testing.T) {
	// 1 replica per shard in the current spec.
	search := newTestMongoDBSearchWithReplicas(testSearchName, testNamespace, testMDBName, 1)
	// Previous state: shard0 had 2 replicas, shard1 had 2 replicas.
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		ShardReplicas: map[string]int{"shard0": 2, "shard1": 2},
	})
	agentSecret := newTestAgentKeySecret("agent-key-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentSecret, stateCM)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	// Current shards: only shard0 with 1 replica. shard1 is gone entirely.
	pending, err := callReconcileTopologyState(t, r, search, []string{"shard0"}, "agent-key-secret")
	require.NoError(t, err)
	assert.True(t, pending, "deferral window has not elapsed")
	assert.Empty(t, deletedHostIDs, "no OM call before deferral window elapses")

	shard0StsName := search.MongotStatefulSetForClusterShard(0, "shard0").Name
	shard1StsName := search.MongotStatefulSetForClusterShard(0, "shard1").Name
	state := getTopologyState(t, fakeClient, search)
	// shard0 scaled 2→1: pod shard0-1 is a candidate.
	// shard1 fully removed (0 current replicas): pods shard1-0 and shard1-1 are candidates.
	expectedDeferred := []string{
		fmt.Sprintf("%s-1", shard0StsName),
		fmt.Sprintf("%s-0", shard1StsName),
		fmt.Sprintf("%s-1", shard1StsName),
	}
	for _, podName := range expectedDeferred {
		assert.Contains(t, state.HostDeletionReadyAfter, podName,
			"pod %s expected in HostDeletionReadyAfter", podName)
	}
}
