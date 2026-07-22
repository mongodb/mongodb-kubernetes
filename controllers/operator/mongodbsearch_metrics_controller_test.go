package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
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
		stateReader:        fakeClient,
		secretClient:       secrets.SecretClient{KubeClient: kc},
		watch:              watch.NewResourceWatcher(),
		defaultImage:       defaultImage,
		omRequester:        newStubOMAgentRequester(testGroupID),
		otelConfigTemplate: searchcontroller.NewMetricsForwarderOTelConfigTemplate(),
		prepareSearch:      newPrepareSearch(""),
		clientForCluster:   func(string) kubernetesClient.Client { return kc },
		readerForCluster:   func(string) client.Reader { return kc },
		isLocalCluster:     func(string) bool { return true },
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

type changingSearchReader struct {
	client.Reader
	searches []*searchv1.MongoDBSearch
	gets     int
}

func (r *changingSearchReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	search, ok := obj.(*searchv1.MongoDBSearch)
	if !ok {
		return r.Reader.Get(ctx, key, obj, opts...)
	}
	index := min(r.gets, len(r.searches)-1)
	r.searches[index].DeepCopyInto(search)
	r.gets++
	return nil
}

// getTopologyState reads and decodes the metrics-forwarder topology state ConfigMap,
// returning the single-cluster (clusterName=="") entry.
func getFullTopologyState(t *testing.T, c client.Client, search *searchv1.MongoDBSearch) searchTopologyState {
	t.Helper()
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Namespace: search.Namespace,
		Name:      fmt.Sprintf("%s-metrics-forwarder-state", search.Name),
	}, cm))
	var state searchTopologyState
	require.NoError(t, json.Unmarshal([]byte(cm.Data[stateKey]), &state))
	return state
}

func getTopologyState(t *testing.T, c client.Client, search *searchv1.MongoDBSearch) clusterTopologyState {
	t.Helper()
	state := getFullTopologyState(t, c, search)
	return state.Clusters[""]
}

func TestOpenTopologyStateStore_UIDSemantics(t *testing.T) {
	tests := []struct {
		name          string
		recordedUID   *string
		wantNotFound  bool
		writeReplicas int
	}{
		{name: "missing marker adopts legacy state", writeReplicas: 4},
		{name: "matching marker preserves state", recordedUID: ptr.To("search-uid"), writeReplicas: 4},
		{name: "mismatched marker resets state", recordedUID: ptr.To("old-search-uid"), wantNotFound: true, writeReplicas: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
			search.UID = "search-uid"
			storedState := searchTopologyState{Clusters: map[string]clusterTopologyState{"": {Replicas: 3}}}
			stateJSON, err := json.Marshal(storedState)
			require.NoError(t, err)
			data := map[string]string{stateKey: string(stateJSON)}
			if tc.recordedUID != nil {
				data[stateOwnerUIDKey] = *tc.recordedUID
			}
			stateOwnerUID := search.UID
			if tc.wantNotFound {
				stateOwnerUID = "old-search-uid"
			}
			stateCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-metrics-forwarder-state", search.Name),
					Namespace: search.Namespace,
					Labels:    metricsForwarderLabels(search),
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion:         "mongodb.com/v1",
						Kind:               "MongoDBSearch",
						Name:               search.Name,
						UID:                stateOwnerUID,
						Controller:         ptr.To(true),
						BlockOwnerDeletion: ptr.To(true),
					}},
				},
				Data: data,
			}

			r, c := newMetricsForwarderReconciler(testDefaultImage, search, stateCM)
			store := r.openTopologyStateStore(search)
			state, err := store.ReadState(ctx)
			if tc.wantNotFound {
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err))
				state = &searchTopologyState{Clusters: map[string]clusterTopologyState{}}
			} else {
				require.NoError(t, err)
				assert.Equal(t, 3, state.Clusters[""].Replicas)
			}

			state.Clusters[""] = clusterTopologyState{Replicas: tc.writeReplicas}
			require.NoError(t, store.WriteState(ctx, state, zap.S()))

			cm := &corev1.ConfigMap{}
			require.NoError(t, c.Get(ctx, types.NamespacedName{
				Namespace: search.Namespace,
				Name:      fmt.Sprintf("%s-metrics-forwarder-state", search.Name),
			}, cm))
			assert.Equal(t, string(search.UID), cm.Data[stateOwnerUIDKey])
			assert.Equal(t, tc.writeReplicas, getTopologyState(t, c, search).Replicas)
			assert.Equal(t, string(search.UID), cm.Labels[khandler.MongoDBSearchOwnerUIDLabel])
			require.Len(t, cm.OwnerReferences, 1)
			assert.Equal(t, search.UID, cm.OwnerReferences[0].UID)
		})
	}
}

func TestOpenTopologyStateStore_RejectsMarkerlessForeignOwner(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "new-search-uid"
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		PendingHostDeletions: []string{"old-search-pod-0"},
	})
	delete(stateCM.Data, stateOwnerUIDKey)
	stateCM.OwnerReferences = search.GetOwnerReferences()
	stateCM.OwnerReferences[0].UID = "old-search-uid"

	r, _ := newMetricsForwarderReconciler(testDefaultImage, search, stateCM)
	_, err := r.openTopologyStateStore(search).ReadState(t.Context())

	require.True(t, apierrors.IsNotFound(err), err)
}

func TestOpenTopologyStateStore_MarkerlessOwnerIdentity(t *testing.T) {
	tests := []struct {
		name        string
		mutateOwner func(*metav1.OwnerReference)
		wantAdopt   bool
	}{
		{name: "exact owner", wantAdopt: true},
		{name: "legacy empty kind and API version", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.Kind = ""
			owner.APIVersion = ""
		}, wantAdopt: true},
		{name: "legacy empty kind", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.Kind = ""
		}, wantAdopt: true},
		{name: "legacy empty API version", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.APIVersion = ""
		}, wantAdopt: true},
		{name: "wrong non-empty kind", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.Kind = "MongoDB"
		}},
		{name: "wrong non-empty API version", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.APIVersion = "mongodb.com/v2"
		}},
		{name: "wrong name", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.Name = "other-search"
		}},
		{name: "wrong UID", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.UID = "old-search-uid"
		}},
		{name: "non-controller owner", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.Controller = ptr.To(false)
		}},
		{name: "owner without controller marker", mutateOwner: func(owner *metav1.OwnerReference) {
			owner.Controller = nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
			search.UID = "search-uid"
			stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
				PendingHostDeletions:   []string{"removed-pod-0"},
				HostDeletionReadyAfter: map[string]int64{"removed-pod-0": 0},
			})
			delete(stateCM.Data, stateOwnerUIDKey)
			stateCM.OwnerReferences = search.GetOwnerReferences()
			if tc.mutateOwner != nil {
				tc.mutateOwner(&stateCM.OwnerReferences[0])
			}
			r, _ := newMetricsForwarderReconciler(testDefaultImage, search, stateCM)

			state, err := r.openTopologyStateStore(search).ReadState(t.Context())

			if !tc.wantAdopt {
				require.True(t, apierrors.IsNotFound(err), err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{"removed-pod-0"}, state.Clusters[""].PendingHostDeletions)
			assert.Equal(t, map[string]int64{"removed-pod-0": 0}, state.Clusters[""].HostDeletionReadyAfter)
		})
	}
}

func TestStateStoreEmptyOwnerUIDPreservesExistingSemantics(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 3})
	delete(stateCM.Data, stateOwnerUIDKey)
	stateCM.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "mongodb.com/v2",
		Kind:       "MongoDB",
		Name:       "other-owner",
		UID:        "other-uid",
		Controller: ptr.To(false),
	}}
	_, c := newMetricsForwarderReconciler(testDefaultImage, search, stateCM)
	store := NewStateStore[searchTopologyState](
		metricsForwarderStateOwner{MongoDBSearch: search},
		search.GetOwnerReferences(),
		kubernetesClient.NewClient(c),
		"",
	)

	state, err := store.ReadState(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 3, state.Clusters[""].Replicas)
}

func TestReconcile_MarkerlessForeignStateDoesNotReplayPendingHosts(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "new-search-uid"
	search.Spec.Observability.MetricsForwarder.Mode = searchv1.MetricsForwarderModeEnabled
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		ClusterIndex:           ptr.To(0),
		HostDeletionReadyAfter: map[string]int64{"old-search-pod-0": 0},
	})
	delete(stateCM.Data, stateOwnerUIDKey)
	stateCM.OwnerReferences = search.GetOwnerReferences()
	stateCM.OwnerReferences[0].UID = "old-search-uid"
	r, c := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentSecret, stateCM)
	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	reconcileMetricsForwarder(t, r, search.Namespace, search.Name)

	assert.Empty(t, deletedHostIDs)
	currentState := getFullTopologyState(t, c, search)
	assert.Empty(t, currentState.Clusters[""].PendingHostDeletions)
	assert.Empty(t, currentState.Clusters[""].HostDeletionReadyAfter)
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(stateCM), cm))
	assert.Equal(t, string(search.UID), cm.Data[stateOwnerUIDKey])
	require.Len(t, cm.OwnerReferences, 1)
	assert.Equal(t, search.UID, cm.OwnerReferences[0].UID)
}

func TestReconcile_MarkerlessCurrentOwnerMigratesBeforeHostCleanup(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	search.Spec.Observability.MetricsForwarder.Mode = searchv1.MetricsForwarderModeEnabled
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		ClusterIndex:           ptr.To(0),
		PendingHostDeletions:   []string{"removed-pod-0"},
		HostDeletionReadyAfter: map[string]int64{"removed-pod-0": 0},
	})
	delete(stateCM.Data, stateOwnerUIDKey)
	stateCM.OwnerReferences = search.GetOwnerReferences()
	stateCM.OwnerReferences[0].Kind = ""
	stateCM.OwnerReferences[0].APIVersion = ""
	r, c := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentSecret, stateCM)
	withWatch, ok := c.(client.WithWatch)
	require.True(t, ok)
	var events []string
	r.kubeClient = kubernetesClient.NewClient(interceptor.NewClient(withWatch, interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == stateCM.Name {
				events = append(events, "state-write")
			}
			return cl.Update(ctx, obj, opts...)
		},
	}))
	r.omRequester = stubOMAgentRequester{fn: func(_ mdbv1.ProjectConfig, method, path, _ string, _ any) ([]byte, error) {
		if method == "POST" && strings.HasSuffix(path, "/v1/delete") {
			cm := &corev1.ConfigMap{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(stateCM), cm))
			assert.Equal(t, string(search.UID), cm.Data[stateOwnerUIDKey])
			assert.Equal(t, search.GetOwnerReferences(), cm.OwnerReferences)
			events = append(events, "host-delete")
			return []byte(`{"results":[]}`), nil
		}
		return nil, fmt.Errorf("unexpected OM agent request: %s %s", method, path)
	}}

	reconcileMetricsForwarder(t, r, search.Namespace, search.Name)

	require.Contains(t, events, "host-delete")
	assert.Less(t, slices.Index(events, "state-write"), slices.Index(events, "host-delete"))
}

func TestDeleteMetricsForwarderResourcesFromState_MigratesMarkerlessStateBeforeDelete(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	search.Spec.Observability.MetricsForwarder.Mode = searchv1.MetricsForwarderModeDisabled
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{ClusterIndex: ptr.To(0)})
	delete(stateCM.Data, stateOwnerUIDKey)
	stateCM.OwnerReferences = search.GetOwnerReferences()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
		Namespace: search.Namespace,
		UID:       "forwarder-uid",
		Labels:    metricsForwarderLabelsForCluster(search, "", 0),
	}}
	r, c := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, stateCM, deployment)
	withWatch, ok := c.(client.WithWatch)
	require.True(t, ok)
	var events []string
	r.kubeClient = kubernetesClient.NewClient(interceptor.NewClient(withWatch, interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == stateCM.Name {
				events = append(events, "state-write")
			}
			return cl.Update(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				cm := &corev1.ConfigMap{}
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(stateCM), cm))
				assert.Equal(t, string(search.UID), cm.Data[stateOwnerUIDKey])
				require.Len(t, cm.OwnerReferences, 1)
				assert.Equal(t, search.UID, cm.OwnerReferences[0].UID)
				events = append(events, "resource-delete")
			}
			return cl.Delete(ctx, obj, opts...)
		},
	}))
	r.clientForCluster = func(string) kubernetesClient.Client { return r.kubeClient }

	require.NoError(t, r.deleteMetricsForwarderResourcesFromState(t.Context(), search, zap.S()))

	require.Contains(t, events, "state-write")
	require.Contains(t, events, "resource-delete")
	assert.Less(t, slices.Index(events, "state-write"), slices.Index(events, "resource-delete"))
}

func TestReconcileCore_LegacyTopologyStateEntryCleanedAfterMoveToNamedClusters(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{
		ClusterIndex: ptr.To(0),
	})
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM)
	currentSearch := getMongoDBSearch(t, fakeClient, search.Namespace, search.Name)

	st := r.reconcileCore(t.Context(), currentSearch, zap.S())

	require.True(t, st.IsOK(), searchcontroller.MessageFromStatus(st))
	topologyState := getFullTopologyState(t, fakeClient, search)
	assert.NotContains(t, topologyState.Clusters, "")
	require.Contains(t, topologyState.Clusters, "cluster-a")
	require.NotNil(t, topologyState.Clusters["cluster-a"].ClusterIndex)
	assert.Equal(t, 0, *topologyState.Clusters["cluster-a"].ClusterIndex)
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{
		Name: search.MetricsForwarderDeploymentNameForCluster(0), Namespace: search.Namespace,
	}, &appsv1.Deployment{}))
}

func TestReconcileCoreRegistersMetricsCredentialAndCAWatches(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	projectCM.Data[util.SSLMMSCAConfigMap] = "ops-manager-ca"
	caConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "ops-manager-ca", Namespace: testNamespace},
		Data:       map[string]string{"mms-ca.crt": "certificate"},
	}
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, caConfigMap, agentKeySecret)
	currentSearch := getMongoDBSearch(t, fakeClient, search.Namespace, search.Name)

	st := r.reconcileCore(t.Context(), currentSearch, zap.S())

	require.True(t, st.IsOK(), searchcontroller.MessageFromStatus(st))
	watched := r.watch.GetWatchedResources()
	for _, resource := range []watch.Object{
		{ResourceType: watch.ConfigMap, Resource: client.ObjectKeyFromObject(projectCM)},
		{ResourceType: watch.ConfigMap, Resource: client.ObjectKeyFromObject(caConfigMap)},
		{ResourceType: watch.Secret, Resource: client.ObjectKeyFromObject(agentKeySecret)},
	} {
		assert.Contains(t, watched[resource], search.NamespacedName(), "missing dependency watch for %s", resource)
	}
}

func TestMongoDBSearchMetricsDependencyWatchesRouteRotations(t *testing.T) {
	searchKey := types.NamespacedName{Name: testSearchName, Namespace: testNamespace}
	for _, topology := range []struct {
		name    string
		watches func(*MongoDBSearchMetricsForwarderReconciler) []mongoDBSearchResourceWatch
	}{
		{name: "central", watches: centralMongoDBSearchMetricsForwarderResourceWatches},
		{name: "member", watches: memberMongoDBSearchMetricsForwarderResourceWatches},
	} {
		t.Run(topology.name, func(t *testing.T) {
			for _, tc := range []struct {
				name         string
				resourceType watch.Type
				newObject    func(data string) client.Object
			}{
				{
					name:         "ConfigMap",
					resourceType: watch.ConfigMap,
					newObject: func(data string) client.Object {
						return &corev1.ConfigMap{
							ObjectMeta: metav1.ObjectMeta{Name: "ops-manager-ca", Namespace: searchKey.Namespace},
							Data:       map[string]string{"value": data},
						}
					},
				},
				{
					name:         "Secret",
					resourceType: watch.Secret,
					newObject: func(data string) client.Object {
						return &corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{Name: "agent-key", Namespace: searchKey.Namespace},
							Data:       map[string][]byte{"value": []byte(data)},
						}
					},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r, _ := newMetricsForwarderReconciler(testDefaultImage)
					oldObject := tc.newObject("old")
					newObject := tc.newObject("new")
					r.watch.AddWatchedResourceIfNotAdded(oldObject.GetName(), oldObject.GetNamespace(), tc.resourceType, searchKey)
					var resourceWatch *mongoDBSearchResourceWatch
					watches := topology.watches(r)
					for i := range watches {
						if reflect.TypeOf(watches[i].obj) == reflect.TypeOf(oldObject) {
							resourceWatch = &watches[i]
							break
						}
					}
					require.NotNil(t, resourceWatch)
					assert.Empty(t, resourceWatch.predicates)

					for _, send := range []func(workqueue.TypedRateLimitingInterface[reconcile.Request]){
						func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
							resourceWatch.handler.Create(t.Context(), event.TypedCreateEvent[client.Object]{Object: oldObject}, q)
						},
						func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
							resourceWatch.handler.Update(t.Context(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldObject, ObjectNew: newObject}, q)
						},
						func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
							resourceWatch.handler.Delete(t.Context(), event.TypedDeleteEvent[client.Object]{Object: oldObject}, q)
						},
					} {
						q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
						send(q)
						require.Equal(t, 1, q.Len())
						request, shutdown := q.Get()
						require.False(t, shutdown)
						assert.Equal(t, searchKey, request.NamespacedName)
						q.Done(request)
						q.ShutDown()
					}
				})
			}
		})
	}
}

func TestReconcileCore_RejectsChangedPersistedClusterIndex(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(7))},
		{Name: "cluster-b", Index: ptr.To(int32(1))},
	}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
	stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
		"cluster-a": {ClusterIndex: ptr.To(0), Replicas: 1},
		"cluster-b": {ClusterIndex: ptr.To(1), Replicas: 1},
	}})
	require.NoError(t, err)
	stateCM.Data[stateKey] = string(stateJSON)
	oldDeployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
		Namespace: search.Namespace,
		Labels:    metricsForwarderLabelsForCluster(search, "cluster-a", 0),
	}}
	validDeployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:        search.MetricsForwarderDeploymentNameForCluster(1),
		Namespace:   search.Namespace,
		UID:         "cluster-b-deployment",
		Annotations: map[string]string{"sentinel": "unchanged"},
		Labels:      metricsForwarderLabelsForCluster(search, "cluster-b", 1),
	}}
	validConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        search.MetricsForwarderConfigMapNameForCluster(1),
			Namespace:   search.Namespace,
			UID:         "cluster-b-config",
			Annotations: map[string]string{"sentinel": "unchanged"},
			Labels:      metricsForwarderLabelsForCluster(search, "cluster-b", 1),
		},
		Data: map[string]string{"sentinel": "unchanged"},
	}
	r, fakeClient := newMetricsForwarderReconciler(
		testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM, oldDeployment, validDeployment, validConfigMap,
	)
	currentSearch := getMongoDBSearch(t, fakeClient, search.Namespace, search.Name)

	st := r.reconcileCore(t.Context(), currentSearch, zap.S())

	require.False(t, st.IsOK())
	assert.Contains(t, searchcontroller.MessageFromStatus(st), `cluster "cluster-a" index is immutable: persisted index 0, requested index 7`)
	topologyState := getFullTopologyState(t, fakeClient, search)
	require.NotNil(t, topologyState.Clusters["cluster-a"].ClusterIndex)
	assert.Equal(t, 0, *topologyState.Clusters["cluster-a"].ClusterIndex)
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(oldDeployment), &appsv1.Deployment{}))
	actualValidDeployment := &appsv1.Deployment{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(validDeployment), actualValidDeployment))
	assert.Equal(t, validDeployment.UID, actualValidDeployment.UID)
	assert.Equal(t, validDeployment.Annotations, actualValidDeployment.Annotations)
	actualValidConfigMap := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(validConfigMap), actualValidConfigMap))
	assert.Equal(t, validConfigMap.UID, actualValidConfigMap.UID)
	assert.Equal(t, validConfigMap.Annotations, actualValidConfigMap.Annotations)
	assert.Equal(t, validConfigMap.Data, actualValidConfigMap.Data)
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Name: search.MetricsForwarderDeploymentNameForCluster(7), Namespace: search.Namespace,
	}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err))
}

// callCleanupRemovedClusters loads the topology state, runs cleanupRemovedClusters against
// it, and persists the mutated state, as reconcileCore does.
func callCleanupRemovedClusters(t *testing.T, r *MongoDBSearchMetricsForwarderReconciler, search *searchv1.MongoDBSearch, agentSecretName string, currentWork []clusterWorkItem) (bool, error) {
	t.Helper()
	topologyState, err := r.loadTopologyState(t.Context(), search)
	require.NoError(t, err)
	pending, cleanupErr := r.cleanupRemovedClusters(t.Context(), search, testGroupID, mdbv1.ProjectConfig{}, agentSecretName, currentWork, topologyState, zap.S())
	require.NoError(t, r.openTopologyStateStore(search).WriteState(t.Context(), topologyState, zap.S()))
	return pending, cleanupErr
}

func TestCleanupRemovedClusters_LegacyStateCleansCentralResourcesAndStateEntry(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{ClusterIndex: ptr.To(0)})
	legacyDeployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
		Namespace: search.Namespace,
		Labels:    metricsForwarderLabelsForCluster(search, "", 0),
	}}
	r, centralClient := newMetricsForwarderReconciler(testDefaultImage, search, stateCM, legacyDeployment)
	memberDeployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
		Namespace: search.Namespace,
		Labels:    metricsForwarderLabelsForCluster(search, "cluster-a", 0),
	}}
	memberClient := mock.NewEmptyFakeClientBuilder().WithObjects(memberDeployment).Build()
	memberKubeClient := kubernetesClient.NewClient(memberClient)
	r.isLocalCluster = func(clusterName string) bool { return clusterName == "" }
	r.clientForCluster = func(clusterName string) kubernetesClient.Client {
		if clusterName == "" {
			return kubernetesClient.NewClient(centralClient)
		}
		return memberKubeClient
	}
	r.readerForCluster = func(clusterName string) client.Reader {
		if clusterName == "" {
			return centralClient
		}
		return memberClient
	}
	currentWork := []clusterWorkItem{{
		ClusterName: "cluster-a", ClusterIndex: 0, Client: memberKubeClient,
	}}

	pending, err := callCleanupRemovedClusters(t, r, search, "agent-key", currentWork)
	require.NoError(t, err)
	assert.True(t, pending)
	assert.True(t, apierrors.IsNotFound(centralClient.Get(t.Context(), client.ObjectKeyFromObject(legacyDeployment), &appsv1.Deployment{})))
	require.NoError(t, memberClient.Get(t.Context(), client.ObjectKeyFromObject(memberDeployment), &appsv1.Deployment{}))
	assert.Contains(t, getFullTopologyState(t, centralClient, search).Clusters, "")

	pending, err = callCleanupRemovedClusters(t, r, search, "agent-key", currentWork)
	require.NoError(t, err)
	assert.False(t, pending)
	assert.NotContains(t, getFullTopologyState(t, centralClient, search).Clusters, "")
	require.NoError(t, memberClient.Get(t.Context(), client.ObjectKeyFromObject(memberDeployment), &appsv1.Deployment{}))
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

func TestEnsureMetricsForwarderResources_SingleCluster_AdoptsOwnerRef(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage)
	legacyOwnerReferences := []metav1.OwnerReference{{
		APIVersion: "mongodb.com/v1",
		Kind:       "MongoDBSearch",
		Name:       search.Name,
		UID:        "old-search-uid",
	}}
	require.NoError(t, fakeClient.Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:            search.MetricsForwarderConfigMapNameForCluster(0),
		Namespace:       search.Namespace,
		OwnerReferences: legacyOwnerReferences,
	}}))
	require.NoError(t, fakeClient.Create(t.Context(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:            search.MetricsForwarderDeploymentNameForCluster(0),
		Namespace:       search.Namespace,
		OwnerReferences: legacyOwnerReferences,
	}}))

	require.NoError(t, r.ensureMetricsForwarderConfigMap(
		context.Background(),
		search,
		[]byte("receivers: {}"),
		r.clusterWorkItem(search, "", 0),
		zap.S(),
	))
	require.NoError(t, r.ensureMetricsForwarderDeployment(
		context.Background(),
		search,
		[]byte("receivers: {}"),
		testGroupID,
		"agent-key-secret",
		"",
		r.clusterWorkItem(search, "", 0),
		zap.S(),
	))

	cm := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      search.MetricsForwarderConfigMapNameForCluster(0),
		Namespace: search.Namespace,
	}, cm))
	require.Len(t, cm.OwnerReferences, 1)
	assert.Equal(t, search.UID, cm.OwnerReferences[0].UID)
	assert.Equal(t, search.Name, cm.Labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, search.Namespace, cm.Labels[khandler.MongoDBSearchOwnerNamespaceLabel])

	dep := &appsv1.Deployment{}
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
		Namespace: search.Namespace,
	}, dep))
	require.Len(t, dep.OwnerReferences, 1)
	assert.Equal(t, search.UID, dep.OwnerReferences[0].UID)
	assert.Equal(t, search.Name, dep.Labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, search.Namespace, dep.Labels[khandler.MongoDBSearchOwnerNamespaceLabel])
}

func TestEnsureMetricsForwarderResources_MultiCluster_NoOwnerRef(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	memberClient := mock.NewEmptyFakeClientBuilder().Build()
	r := newMongoDBSearchMetricsForwarderReconciler(
		mock.NewEmptyFakeClientBuilder().Build(),
		testDefaultImage,
		map[string]client.Client{"member-a": memberClient},
		"",
	)
	work := r.clusterWorkItem(search, "member-a", 0)

	require.NoError(t, r.ensureMetricsForwarderConfigMap(t.Context(), search, []byte("receivers: {}"), work, zap.S()))
	require.NoError(t, r.ensureMetricsForwarderDeployment(t.Context(), search, []byte("receivers: {}"), testGroupID, "agent-key-secret", "", work, zap.S()))

	cm := &corev1.ConfigMap{}
	require.NoError(t, memberClient.Get(t.Context(), types.NamespacedName{
		Name: search.MetricsForwarderConfigMapNameForCluster(0), Namespace: search.Namespace,
	}, cm))
	assert.Empty(t, cm.OwnerReferences)
	dep := &appsv1.Deployment{}
	require.NoError(t, memberClient.Get(t.Context(), types.NamespacedName{
		Name: search.MetricsForwarderDeploymentNameForCluster(0), Namespace: search.Namespace,
	}, dep))
	assert.Empty(t, dep.OwnerReferences)
}

func TestReplicateForwarderDependencies_OwnerReferencesFollowLocality(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-key", Namespace: search.Namespace},
		Data:       map[string][]byte{"key": []byte("value")},
	}
	sourceCA := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "om-ca", Namespace: search.Namespace},
		Data:       map[string]string{"ca-pem": "certificate"},
	}
	tests := []struct {
		name         string
		clusterName  string
		crossCluster bool
	}{
		{name: "same cluster"},
		{name: "cross cluster", clusterName: "member-a", crossCluster: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			central := mock.NewEmptyFakeClientBuilder().WithObjects(sourceSecret.DeepCopy(), sourceCA.DeepCopy()).Build()
			target := central
			var members map[string]client.Client
			if tc.crossCluster {
				target = mock.NewEmptyFakeClientBuilder().Build()
				members = map[string]client.Client{tc.clusterName: target}
			}
			r := newMongoDBSearchMetricsForwarderReconciler(central, testDefaultImage, members, "")
			work := r.clusterWorkItem(search, tc.clusterName, 0)

			require.NoError(t, r.replicateForwarderDependencies(t.Context(), search, sourceSecret.Name, sourceCA.Name, work, zap.S()))

			generatedSecret := &corev1.Secret{}
			require.NoError(t, target.Get(t.Context(), types.NamespacedName{
				Name: search.MetricsForwarderAgentKeySecretNameForCluster(0), Namespace: search.Namespace,
			}, generatedSecret))
			generatedCA := &corev1.ConfigMap{}
			require.NoError(t, target.Get(t.Context(), types.NamespacedName{
				Name: search.MetricsForwarderCACertConfigMapNameForCluster(0), Namespace: search.Namespace,
			}, generatedCA))
			if tc.crossCluster {
				assert.Empty(t, generatedSecret.OwnerReferences)
				assert.Empty(t, generatedCA.OwnerReferences)
			} else {
				require.Len(t, generatedSecret.OwnerReferences, 1)
				require.Len(t, generatedCA.OwnerReferences, 1)
				assert.Equal(t, search.UID, generatedSecret.OwnerReferences[0].UID)
				assert.Equal(t, search.UID, generatedCA.OwnerReferences[0].UID)
			}
		})
	}
}

func TestBuildClusterWorkList_TopologyCoverage(t *testing.T) {
	t.Run("named single-cluster uses index 0 and central client", func(t *testing.T) {
		central := mock.NewEmptyFakeClientBuilder().Build()
		r := newMongoDBSearchMetricsForwarderReconciler(central, testDefaultImage, nil, "")
		search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
		search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "kind-e2e-cluster-1"}}

		wl := r.buildClusterWorkList(search)
		require.Len(t, wl, 1)
		assert.Equal(t, "kind-e2e-cluster-1", wl[0].ClusterName)
		assert.Equal(t, 0, wl[0].ClusterIndex)
		assert.Equal(t, r.kubeClient, wl[0].Client)
	})

	t.Run("projected operator-per-cluster entry keeps pinned index", func(t *testing.T) {
		central := mock.NewEmptyFakeClientBuilder().Build()
		r := newMongoDBSearchMetricsForwarderReconciler(central, testDefaultImage, nil, "cluster-b")
		search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
		search.Spec.Clusters = []searchv1.ClusterSpec{{
			Name:  "cluster-b",
			Index: ptr.To(int32(7)),
		}}

		wl := r.buildClusterWorkList(search)
		require.Len(t, wl, 1)
		assert.Equal(t, "cluster-b", wl[0].ClusterName)
		assert.Equal(t, 7, wl[0].ClusterIndex)
		assert.Equal(t, r.kubeClient, wl[0].Client)
		require.Len(t, wl[0].OwnerReferences, 1)
		assert.Equal(t, search.UID, wl[0].OwnerReferences[0].UID)
	})

	t.Run("hub with every member client missing stays remote", func(t *testing.T) {
		t.Setenv(util.SearchEnableMultiClusterEnv, "true")
		central := mock.NewEmptyFakeClientBuilder().Build()
		r := newMongoDBSearchMetricsForwarderReconciler(central, testDefaultImage, nil, "")
		search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
		search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}

		wl := r.buildClusterWorkList(search)

		require.Len(t, wl, 1)
		assert.Nil(t, wl[0].Client)
		assert.Empty(t, wl[0].OwnerReferences)
		assert.Nil(t, r.readerForCluster("cluster-a"))
		assert.False(t, r.isLocalCluster("cluster-a"))
	})
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
		ObjectMeta: metav1.ObjectMeta{Name: "my-search", Namespace: "ns", UID: "search-uid"},
	}

	labels := metricsForwarderLabels(search)
	assert.Equal(t, "my-search-search-metrics-forwarder-0", labels["app"])
	assert.Equal(t, metricsForwarderLabelName, labels[khandler.MongoDBSearchComponentLabel])
	assert.Equal(t, "my-search", labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, "ns", labels[khandler.MongoDBSearchOwnerNamespaceLabel])
	assert.Equal(t, string(search.UID), labels[khandler.MongoDBSearchOwnerUIDLabel])
	assert.NotContains(t, labels, khandler.MongoDBSearchClusterNameLabel)

	memberLabels := metricsForwarderLabelsForCluster(search, "us-east", 3)
	assert.Equal(t, search.MetricsForwarderDeploymentNameForCluster(3), memberLabels["app"])
	assert.Equal(t, "my-search", memberLabels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, "ns", memberLabels[khandler.MongoDBSearchOwnerNamespaceLabel])
	assert.Equal(t, string(search.UID), memberLabels[khandler.MongoDBSearchOwnerUIDLabel])
	assert.Equal(t, "us-east", memberLabels[khandler.MongoDBSearchClusterNameLabel])

	podLabels := metricsForwarderPodLabels(search)
	assert.Equal(t, "my-search-search-metrics-forwarder-0", podLabels["app"])
	assert.NotContains(t, podLabels, khandler.MongoDBSearchComponentLabel)
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

func TestEnsureMetricsForwarderDeployment_OverridePreservesProtectedSearchLabels(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns", UID: "search-uid"},
		Spec: searchv1.MongoDBSearchSpec{
			Observability: searchv1.ObservabilityConfig{
				MetricsForwarder: searchv1.MetricsForwarderConfig{
					Deployment: &v1.DeploymentConfiguration{
						MetadataWrapper: v1.DeploymentMetadataWrapper{
							Labels: map[string]string{
								"custom-label":                            "custom-value",
								khandler.MongoDBSearchOwnerNameLabel:      "wrong-name",
								khandler.MongoDBSearchOwnerNamespaceLabel: "wrong-namespace",
								khandler.MongoDBSearchOwnerUIDLabel:       "wrong-uid",
								khandler.MongoDBSearchClusterNameLabel:    "wrong-cluster",
								khandler.MongoDBSearchComponentLabel:      "wrong-component",
							},
						},
					},
				},
			},
		},
	}
	r, kubeClient := newMetricsForwarderReconciler(testDefaultImage)

	require.NoError(t, r.ensureMetricsForwarderDeployment(t.Context(), search, []byte("config"), "group", "agent-key", "", r.clusterWorkItem(search, "member-a", 2), zap.S()))

	dep := &appsv1.Deployment{}
	require.NoError(t, kubeClient.Get(t.Context(), types.NamespacedName{Name: search.MetricsForwarderDeploymentNameForCluster(2), Namespace: search.Namespace}, dep))
	assert.Equal(t, "custom-value", dep.Labels["custom-label"])
	assert.Equal(t, search.Name, dep.Labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, search.Namespace, dep.Labels[khandler.MongoDBSearchOwnerNamespaceLabel])
	assert.Equal(t, string(search.UID), dep.Labels[khandler.MongoDBSearchOwnerUIDLabel])
	assert.Equal(t, "member-a", dep.Labels[khandler.MongoDBSearchClusterNameLabel])
	assert.Equal(t, metricsForwarderLabelName, dep.Labels[khandler.MongoDBSearchComponentLabel])
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

	t.Run("reconciliation disabled: manual ConfigMap edits survive", func(t *testing.T) {
		cmKey := types.NamespacedName{
			Namespace: testNamespace,
			Name:      search.MetricsForwarderConfigMapNameForCluster(0),
		}
		cm.Data["config.yaml"] = "manually-patched"
		require.NoError(t, fakeClient.Update(context.Background(), cm))

		updatedSearch.Annotations = map[string]string{searchv1.DisableReconciliationAnnotation: "true"}
		require.NoError(t, fakeClient.Update(context.Background(), updatedSearch))

		reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

		require.NoError(t, fakeClient.Get(context.Background(), cmKey, cm))
		assert.Equal(t, "manually-patched", cm.Data["config.yaml"],
			"a paused CR's forwarder ConfigMap must not be rewritten by the reconciler")
	})
}

func TestReconcile_DisabledMode_DeletesResources(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Observability = searchv1.ObservabilityConfig{
		MetricsForwarder: searchv1.MetricsForwarderConfig{
			Mode: searchv1.MetricsForwarderModeDisabled,
		},
	}
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
	stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
		"cluster-a": {ClusterIndex: ptr.To(0)},
		"cluster-b": {ClusterIndex: ptr.To(1)},
	}})
	require.NoError(t, err)
	stateCM.Data[stateKey] = string(stateJSON)
	existingDep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      search.MetricsForwarderDeploymentNameForCluster(1),
		Namespace: testNamespace,
		UID:       "metrics-deployment-uid",
		Labels:    metricsForwarderLabelsForCluster(search, "cluster-b", 1),
	}}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace), stateCM, existingDep)
	fakeClientWithWatch, ok := fakeClient.(client.WithWatch)
	require.True(t, ok)
	var propagationPolicy *metav1.DeletionPropagation
	var preconditions *metav1.Preconditions
	interceptedClient := kubernetesClient.NewClient(interceptor.NewClient(fakeClientWithWatch, interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				deleteOptions := &client.DeleteOptions{}
				for _, opt := range opts {
					opt.ApplyToDelete(deleteOptions)
				}
				propagationPolicy = deleteOptions.PropagationPolicy
				preconditions = deleteOptions.Preconditions
			}
			return cl.Delete(ctx, obj, opts...)
		},
	}))
	r.kubeClient = interceptedClient
	r.clientForCluster = func(string) kubernetesClient.Client { return interceptedClient }
	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	dep := &appsv1.Deployment{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(1),
	}, dep)
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil, "expected deployment to not exist")
	require.NotNil(t, propagationPolicy)
	assert.Equal(t, metav1.DeletePropagationForeground, *propagationPolicy)
	require.NotNil(t, preconditions)
	assert.Equal(t, existingDep.UID, *preconditions.UID)
	assert.NotEmpty(t, *preconditions.ResourceVersion)

	// Verify status shows disabled
	updatedSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	require.NotNil(t, updatedSearch.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseDisabled, updatedSearch.Status.MetricsForwarder.Phase)
}

func TestDeleteMetricsForwarderResourcesPreservesForeignReplacement(t *testing.T) {
	ctx := context.Background()
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = types.UID("search-uid")
	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.MetricsForwarderDeploymentNameForCluster(0),
			Namespace: search.Namespace,
			Labels:    map[string]string{"app": "foreign"},
		},
	}
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, foreign)

	err := r.deleteMetricsForwarderResources(ctx, search, []clusterWorkItem{{
		ClusterIndex: 0,
		Client:       r.kubeClient,
	}}, zap.S())
	require.NoError(t, err)
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(foreign), &appsv1.Deployment{}))
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

func TestReconcile_DeletionWithCommunitySourceRemovesExistingFinalizer(t *testing.T) {
	mdbc := newTestMongoDBCommunity(testMDBName, testNamespace)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdbc, search)
	require.NoError(t, fakeClient.Delete(context.Background(), search))

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(search), &searchv1.MongoDBSearch{})
	assert.True(t, apierrors.IsNotFound(err), "unsupported sources must not retain the metrics finalizer during deletion")
}

func TestReconcile_DeletionWithCommunitySourceRetainsFinalizerForPersistedHosts(t *testing.T) {
	mdbc := newTestMongoDBCommunity(testMDBName, testNamespace)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{ClusterIndex: ptr.To(0), Replicas: 1})
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdbc, search, stateCM)
	require.NoError(t, fakeClient.Delete(context.Background(), search))

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	remainingSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	assert.Contains(t, remainingSearch.Finalizers, util.SearchMetricsForwarderFinalizer)
	assert.Equal(t, 1, getTopologyState(t, fakeClient, search).Replicas)
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

func TestReconcile_DeletionTakesPriorityOverDisableAnnotationAndDisabledMode(t *testing.T) {
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
	search.Annotations = map[string]string{searchv1.DisableReconciliationAnnotation: "true"}
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

func TestReconcile_DeletionBypassesNormalValidationAndImageGates(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a"}, {Name: "cluster-b"}}
	search.Status.Version = ""
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)

	r, fakeClient := newMetricsForwarderReconciler("", mdb, search, projectCM, agentKeySecret)
	require.NoError(t, fakeClient.Delete(context.Background(), search))

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: testSearchName}, &searchv1.MongoDBSearch{})
	assert.True(t, apierrors.IsNotFound(err), "deletion cleanup must not be blocked by normal validation, image, or version gates")
}

func TestReconcile_MissingClusterClientSurfacesPending(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0))},
		{Name: "cluster-b", Index: ptr.To(int32(1))},
	}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret)
	r.clientForCluster = func(string) kubernetesClient.Client { return nil }

	st := r.reconcileCore(context.Background(), getMongoDBSearch(t, fakeClient, testNamespace, testSearchName), zap.S())

	require.Equal(t, status.PhasePending, st.Phase())
	assert.Contains(t, searchcontroller.MessageFromStatus(st), `cluster "cluster-a"`)
}

func TestPreDeletionCleanup_HostCleanupFailureRetainsFinalizer(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0))},
		{Name: "cluster-b", Index: ptr.To(int32(1))},
	}
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
	stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
		"cluster-a": {ClusterIndex: ptr.To(0), Replicas: 1},
		"cluster-b": {ClusterIndex: ptr.To(1), Replicas: 1},
	}})
	require.NoError(t, err)
	stateCM.Data[stateKey] = string(stateJSON)
	agentSecretName := "agent-key-secret"
	agentKeySecret := newTestAgentKeySecret(agentSecretName, testNamespace)
	r, _ := newMetricsForwarderReconciler(testDefaultImage, search, stateCM, agentKeySecret)
	clusterAHostID := mongotHostID(testGroupID, testNamespace, fmt.Sprintf("%s-0", search.StatefulSetNamespacedNameForCluster(0).Name))

	var requestedHostIDs []string
	r.omRequester = stubOMAgentRequester{fn: func(_ mdbv1.ProjectConfig, method, path, _ string, body any) ([]byte, error) {
		require.Equal(t, "POST", method)
		require.True(t, strings.HasSuffix(path, "/v1/delete"))
		hostIDs := body.(deleteHostsRequest).HostIds
		requestedHostIDs = append(requestedHostIDs, hostIDs...)
		if hostIDs[0] == clusterAHostID {
			return nil, fmt.Errorf("injected cluster-a host cleanup failure")
		}
		return []byte(`{"results":[]}`), nil
	}}

	st := r.preDeletionCleanup(
		context.Background(),
		search,
		nil,
		testGroupID,
		mdbv1.ProjectConfig{BaseURL: testOMBaseURL},
		agentSecretName,
		r.buildClusterWorkList(search),
		zap.NewNop().Sugar(),
	)

	require.False(t, st.IsOK())
	assert.Contains(t, searchcontroller.MessageFromStatus(st), "injected cluster-a host cleanup failure")
	assert.Contains(t, search.Finalizers, util.SearchMetricsForwarderFinalizer,
		"finalizer must stay until host cleanup succeeds")
}

func TestPreDeletionCleanup_AggregatesHostCleanupAcrossClusters(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0))},
		{Name: "cluster-b", Index: ptr.To(int32(1))},
	}
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
	stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
		"cluster-a": {ClusterIndex: ptr.To(0), Replicas: 1},
		"cluster-b": {ClusterIndex: ptr.To(1), Replicas: 1},
	}})
	require.NoError(t, err)
	stateCM.Data[stateKey] = string(stateJSON)
	agentSecretName := "agent-key-secret"
	agentKeySecret := newTestAgentKeySecret(agentSecretName, testNamespace)
	r, _ := newMetricsForwarderReconciler(testDefaultImage, search, stateCM, agentKeySecret)

	var requestedHostIDs []string
	r.omRequester = stubOMAgentRequester{fn: func(_ mdbv1.ProjectConfig, method, path, _ string, body any) ([]byte, error) {
		require.Equal(t, "POST", method)
		require.True(t, strings.HasSuffix(path, "/v1/delete"))
		requestedHostIDs = append(requestedHostIDs, body.(deleteHostsRequest).HostIds...)
		return nil, fmt.Errorf("injected host cleanup failure for request %d", len(requestedHostIDs))
	}}

	st := r.preDeletionCleanup(
		context.Background(),
		search,
		nil,
		testGroupID,
		mdbv1.ProjectConfig{BaseURL: testOMBaseURL},
		agentSecretName,
		r.buildClusterWorkList(search),
		zap.NewNop().Sugar(),
	)

	require.False(t, st.IsOK())
	message := searchcontroller.MessageFromStatus(st)
	assert.Contains(t, message, "injected host cleanup failure for request 1")
	assert.Contains(t, message, "injected host cleanup failure for request 2")
	assert.Len(t, requestedHostIDs, 2, "host cleanup must continue to cluster-b after cluster-a fails")
	assert.Contains(t, search.Finalizers, util.SearchMetricsForwarderFinalizer)
}

func TestReconcile_DeletionFailsClosedWhenClusterClientIsMissing(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0))},
		{Name: "cluster-b", Index: ptr.To(int32(1))},
	}
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret)
	r.clientForCluster = func(clusterName string) kubernetesClient.Client {
		if clusterName == "cluster-b" {
			return nil
		}
		return r.kubeClient
	}
	require.NoError(t, fakeClient.Delete(context.Background(), search))
	deletingSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)

	st := r.reconcileCore(context.Background(), deletingSearch, zap.S())

	require.False(t, st.IsOK())
	assert.Contains(t, searchcontroller.MessageFromStatus(st), `cluster="cluster-b": no Kubernetes client registered`)
	remainingSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)
	assert.Contains(t, remainingSearch.Finalizers, util.SearchMetricsForwarderFinalizer,
		"finalizer must stay while an unreachable cluster may still hold forwarder resources")
}

func TestReconcile_RemovedPerClusterOperatorCleansPersistedTopology(t *testing.T) {
	pendingPodName := "pending-removed-cluster-mongot"
	deferredPodName := "deferred-removed-cluster-mongot"
	for _, tc := range []struct {
		name           string
		deleteSearch   bool
		topology       map[string]clusterTopologyState
		extraHostIDs   []string
		wantSearchGone bool
	}{
		{
			name:         "during Search deletion",
			deleteSearch: true,
			topology: map[string]clusterTopologyState{
				"cluster-a": {ClusterIndex: ptr.To(0)},
				"cluster-b": {ClusterIndex: ptr.To(1), Replicas: 1},
			},
			wantSearchGone: true,
		},
		{
			name: "after cluster removal",
			topology: map[string]clusterTopologyState{
				"cluster-b": {
					ClusterIndex:           ptr.To(1),
					Replicas:               1,
					PendingHostDeletions:   []string{pendingPodName},
					HostDeletionReadyAfter: map[string]int64{deferredPodName: time.Now().Add(time.Hour).UnixNano()},
				},
			},
			extraHostIDs: []string{
				mongotHostID(testGroupID, testNamespace, pendingPodName),
				mongotHostID(testGroupID, testNamespace, deferredPodName),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
			search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
			search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
			search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
			projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
			agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
			stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
			stateJSON, err := json.Marshal(searchTopologyState{Clusters: tc.topology})
			require.NoError(t, err)
			stateCM.Data[stateKey] = string(stateJSON)
			removedClusterDep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
				Name:      search.MetricsForwarderDeploymentNameForCluster(1),
				Namespace: testNamespace,
				Labels:    metricsForwarderLabelsForCluster(search, "cluster-b", 1),
			}}
			r, fakeClient := newMetricsForwarderReconciler(
				testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM, removedClusterDep,
			)
			r.operatorClusterName = "cluster-b"
			var deletedHostIDs []string
			r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)
			if tc.deleteSearch {
				require.NoError(t, fakeClient.Delete(t.Context(), search))
			}

			result := reconcileMetricsForwarder(t, r, testNamespace, testSearchName)
			assert.True(t, result.RequeueAfter > 0 || result.Requeue)
			assert.Empty(t, deletedHostIDs)
			reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

			assert.True(t, apierrors.IsNotFound(fakeClient.Get(t.Context(), client.ObjectKeyFromObject(removedClusterDep), &appsv1.Deployment{})))
			stsName := search.StatefulSetNamespacedNameForCluster(1).Name
			wantHostIDs := append([]string{
				mongotHostID(testGroupID, testNamespace, fmt.Sprintf("%s-0", stsName)),
			}, tc.extraHostIDs...)
			assert.ElementsMatch(t, wantHostIDs, deletedHostIDs)
			err = fakeClient.Get(t.Context(), client.ObjectKeyFromObject(search), &searchv1.MongoDBSearch{})
			if tc.wantSearchGone {
				assert.True(t, apierrors.IsNotFound(err), "expected persisted cleanup before finalizer removal")
				return
			}
			require.NoError(t, err)
			assert.NotContains(t, getMongoDBSearch(t, fakeClient, testNamespace, testSearchName).Finalizers, util.SearchMetricsForwarderFinalizer)

			result = reconcileMetricsForwarder(t, r, testNamespace, testSearchName)
			assert.False(t, result.Requeue)
			assert.Equal(t, util.TWENTY_FOUR_HOURS, result.RequeueAfter)
		})
	}
}

func TestReconcile_RemovedPerClusterOperatorPreservesReaddedCluster(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	liveSearch := search.DeepCopy()
	liveSearch.Spec.Clusters = append(liveSearch.Spec.Clusters, searchv1.ClusterSpec{Name: "cluster-b", Index: ptr.To(int32(1))})
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentSecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
	stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
		"cluster-b": {ClusterIndex: ptr.To(1), Replicas: 1},
	}})
	require.NoError(t, err)
	stateCM.Data[stateKey] = string(stateJSON)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      search.MetricsForwarderDeploymentNameForCluster(1),
		Namespace: search.Namespace,
		UID:       "cluster-b-forwarder",
		Labels:    metricsForwarderLabelsForCluster(search, "cluster-b", 1),
	}}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentSecret, stateCM, deployment)
	r.operatorClusterName = "cluster-b"
	r.stateReader = mock.NewEmptyFakeClientBuilder().WithObjects(liveSearch, stateCM.DeepCopy()).Build()
	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	result := reconcileMetricsForwarder(t, r, testNamespace, testSearchName)
	assert.True(t, result.Requeue || result.RequeueAfter > 0)
	// The forwarder Deployment delete is recoverable (recreated once the operator's cache
	// catches up with the re-added cluster); the irreversible pieces are guarded: no Ops
	// Manager host deregistration and the persisted state entry stays.
	assert.Empty(t, deletedHostIDs)
	assert.Contains(t, getFullTopologyState(t, fakeClient, search).Clusters, "cluster-b")
	assert.Contains(t, getMongoDBSearch(t, fakeClient, testNamespace, testSearchName).Finalizers, util.SearchMetricsForwarderFinalizer)
}

func TestCleanupRemovedClusters_UncachedGuardBlocksHostDeregistration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		liveSearch func(search *searchv1.MongoDBSearch) *searchv1.MongoDBSearch
	}{
		{
			name: "removed cluster re-added on the live CR",
			liveSearch: func(search *searchv1.MongoDBSearch) *searchv1.MongoDBSearch {
				live := search.DeepCopy()
				live.Spec.Clusters = append(live.Spec.Clusters, searchv1.ClusterSpec{Name: "cluster-b", Index: ptr.To(int32(1))})
				return live
			},
		},
		{
			name: "same-name replacement CR with a different UID",
			liveSearch: func(search *searchv1.MongoDBSearch) *searchv1.MongoDBSearch {
				live := search.DeepCopy()
				live.UID = "replacement-search-uid"
				return live
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
			search.UID = "search-uid"
			search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
			stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
			stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
				"cluster-b": {ClusterIndex: ptr.To(1), Replicas: 1},
			}})
			require.NoError(t, err)
			stateCM.Data[stateKey] = string(stateJSON)
			agentKeySecret := newTestAgentKeySecret("agent-key-secret", search.Namespace)
			r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, search, agentKeySecret, stateCM)
			r.operatorClusterName = "cluster-b"
			reader := &changingSearchReader{
				Reader:   fakeClient,
				searches: []*searchv1.MongoDBSearch{tc.liveSearch(search)},
			}
			r.stateReader = reader
			var deletedHostIDs []string
			r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

			pending, err := callCleanupRemovedClusters(t, r, search, "agent-key-secret", nil)
			require.NoError(t, err)
			assert.False(t, pending)
			assert.Empty(t, deletedHostIDs, "hosts must not be deregistered when the live CR does not confirm the removal")
			assert.Contains(t, getFullTopologyState(t, fakeClient, search).Clusters, "cluster-b",
				"the persisted state entry must be retained for the next reconcile")
		})
	}
}

func TestCleanupRemovedClustersSkipsReaddedTargetWithoutSuppressingOtherCleanup(t *testing.T) {
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.UID = "search-uid"
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
	liveSearch := search.DeepCopy()
	liveSearch.Spec.Clusters = append(liveSearch.Spec.Clusters, searchv1.ClusterSpec{Name: "cluster-b", Index: ptr.To(int32(1))})
	clusterBDeployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      search.MetricsForwarderDeploymentNameForCluster(1),
		Namespace: search.Namespace,
		UID:       "cluster-b-forwarder",
		Labels:    metricsForwarderLabelsForCluster(search, "cluster-b", 1),
	}}
	clusterB := mock.NewEmptyFakeClientBuilder().WithObjects(clusterBDeployment).Build()
	clusterC := mock.NewEmptyFakeClientBuilder().Build()
	agentKeySecret := newTestAgentKeySecret("agent-key-secret", search.Namespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
	stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
		"cluster-b": {ClusterIndex: ptr.To(1), Replicas: 1},
		"cluster-c": {ClusterIndex: ptr.To(2), Replicas: 1},
	}})
	require.NoError(t, err)
	stateCM.Data[stateKey] = string(stateJSON)
	central := mock.NewEmptyFakeClientBuilder().WithObjects(search, agentKeySecret, stateCM).Build()
	r := newMongoDBSearchMetricsForwarderReconciler(
		central,
		testDefaultImage,
		map[string]client.Client{"cluster-b": clusterB, "cluster-c": clusterC},
		"",
	)
	r.stateReader = mock.NewEmptyFakeClientBuilder().WithObjects(liveSearch, stateCM.DeepCopy()).Build()
	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	pending, err := callCleanupRemovedClusters(t, r, search, "agent-key-secret", nil)

	require.NoError(t, err)
	assert.True(t, pending, "the re-added cluster's Deployment delete must requeue")
	topologyState := getFullTopologyState(t, central, search)
	assert.Contains(t, topologyState.Clusters, "cluster-b", "re-added cluster's state entry must be retained")
	assert.NotContains(t, topologyState.Clusters, "cluster-c")
	assert.Equal(t, []string{
		mongotHostID(testGroupID, testNamespace, fmt.Sprintf("%s-0", search.StatefulSetNamespacedNameForCluster(2).Name)),
	}, deletedHostIDs, "only the truly removed cluster's hosts are deregistered")
}

func TestReconcile_RemovedPerClusterOperatorRetriesFailedFinalizerRemoval(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}}
	search.Finalizers = []string{util.SearchMetricsForwarderFinalizer}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{})
	stateJSON, err := json.Marshal(searchTopologyState{Clusters: map[string]clusterTopologyState{
		"cluster-b": {ClusterIndex: ptr.To(1)},
	}})
	require.NoError(t, err)
	stateCM.Data[stateKey] = string(stateJSON)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM)
	r.operatorClusterName = "cluster-b"

	fakeClientWithWatch, ok := fakeClient.(client.WithWatch)
	require.True(t, ok)
	failFinalizerUpdate := true
	r.kubeClient = kubernetesClient.NewClient(interceptor.NewClient(fakeClientWithWatch, interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*searchv1.MongoDBSearch); ok && failFinalizerUpdate {
				failFinalizerUpdate = false
				return fmt.Errorf("injected finalizer update failure")
			}
			return cl.Update(ctx, obj, opts...)
		},
	}))

	firstResult := reconcileMetricsForwarder(t, r, testNamespace, testSearchName)
	assert.True(t, firstResult.RequeueAfter > 0 || firstResult.Requeue)
	assert.Contains(t, getMongoDBSearch(t, fakeClient, testNamespace, testSearchName).Finalizers, util.SearchMetricsForwarderFinalizer,
		"finalizer must survive a failed removal update and be retried")

	reconcileMetricsForwarder(t, r, testNamespace, testSearchName)

	finalState := getFullTopologyState(t, fakeClient, search)
	assert.NotContains(t, finalState.Clusters, "cluster-b")
	assert.NotContains(t, getMongoDBSearch(t, fakeClient, testNamespace, testSearchName).Finalizers, util.SearchMetricsForwarderFinalizer)
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
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{ClusterIndex: ptr.To(0), Replicas: 1})

	// Pre-create a Deployment to simulate the forwarder having been running.
	existingDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.MetricsForwarderDeploymentNameForCluster(0),
			Namespace: testNamespace,
			Labels:    metricsForwarderLabelsForCluster(search, "", 0),
		},
	}

	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM, existingDep)

	var deletedHostIDs []string
	r.omRequester = recordingDeleteHostsRequester(&deletedHostIDs)

	require.NoError(t, fakeClient.Delete(context.Background(), search))
	var propagationPolicy *metav1.DeletionPropagation
	fakeClientWithWatch, ok := fakeClient.(client.WithWatch)
	require.True(t, ok)
	interceptedClient := kubernetesClient.NewClient(interceptor.NewClient(fakeClientWithWatch, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				return apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, key.Name)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			deleteOptions := &client.DeleteOptions{}
			for _, opt := range opts {
				opt.ApplyToDelete(deleteOptions)
			}
			propagationPolicy = deleteOptions.PropagationPolicy
			return cl.Delete(ctx, obj, opts...)
		},
	}))
	r.kubeClient = interceptedClient
	r.clientForCluster = func(string) kubernetesClient.Client { return interceptedClient }

	// First reconcile: Deployment still exists → preDeletionCleanup deletes it and returns Pending.
	result1 := reconcileMetricsForwarder(t, r, testNamespace, testSearchName)
	assert.True(t, result1.RequeueAfter > 0 || result1.Requeue, "expected requeue on first deletion reconcile")
	assert.Empty(t, deletedHostIDs, "expected no host deregistration while Deployment still exists")
	require.NotNil(t, propagationPolicy)
	assert.Equal(t, metav1.DeletePropagationForeground, *propagationPolicy)

	// The Deployment should now be gone (deleted by phase 1).
	depErr := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: existingDep.Name}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(depErr), "expected Deployment to be deleted after first reconcile")
	midDeleteSearch := &searchv1.MongoDBSearch{}
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: testSearchName}, midDeleteSearch))
	require.NotNil(t, midDeleteSearch.DeletionTimestamp)
	assert.Contains(t, midDeleteSearch.Finalizers, util.SearchMetricsForwarderFinalizer, "finalizer must remain until host cleanup finishes")

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

func TestReconcileCore_MultiClusterTopologyStateWrittenOnce(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0))},
		{Name: "cluster-b", Index: ptr.To(int32(1))},
	}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret)

	fakeClientWithWatch, ok := fakeClient.(client.WithWatch)
	require.True(t, ok)
	stateCMName := fmt.Sprintf("%s-metrics-forwarder-state", search.Name)
	stateWrites := 0
	interceptedClient := kubernetesClient.NewClient(interceptor.NewClient(fakeClientWithWatch, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == stateCMName {
				return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, key.Name)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == stateCMName {
				stateWrites++
			}
			return cl.Create(ctx, obj, opts...)
		},
	}))
	r.kubeClient = interceptedClient
	currentSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)

	st := r.reconcileCore(context.Background(), currentSearch, zap.NewNop().Sugar())

	require.True(t, st.IsOK(), searchcontroller.MessageFromStatus(st))
	assert.Equal(t, 1, stateWrites, "both clusters' topology must land in one state write")
	stateCM := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: stateCMName}, stateCM))
	var topologyState searchTopologyState
	require.NoError(t, json.Unmarshal([]byte(stateCM.Data[stateKey]), &topologyState))
	require.Len(t, topologyState.Clusters, 2)
	require.NotNil(t, topologyState.Clusters["cluster-a"].ClusterIndex)
	require.NotNil(t, topologyState.Clusters["cluster-b"].ClusterIndex)
	assert.Equal(t, 0, *topologyState.Clusters["cluster-a"].ClusterIndex)
	assert.Equal(t, 1, *topologyState.Clusters["cluster-b"].ClusterIndex)
}

func TestReconcileCore_StateWriteFailurePreventsDeploymentCreation(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret)

	fakeClientWithWatch, ok := fakeClient.(client.WithWatch)
	require.True(t, ok)
	stateCMName := fmt.Sprintf("%s-metrics-forwarder-state", search.Name)
	injectedErr := fmt.Errorf("injected topology state write failure")
	interceptedClient := kubernetesClient.NewClient(interceptor.NewClient(fakeClientWithWatch, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == stateCMName {
				return injectedErr
			}
			return cl.Create(ctx, obj, opts...)
		},
	}))
	r.kubeClient = interceptedClient
	r.clientForCluster = func(string) kubernetesClient.Client { return interceptedClient }
	currentSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)

	st := r.reconcileCore(context.Background(), currentSearch, zap.NewNop().Sugar())

	require.False(t, st.IsOK())
	assert.Contains(t, searchcontroller.MessageFromStatus(st), injectedErr.Error())
	dep := &appsv1.Deployment{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderDeploymentNameForCluster(0),
	}, dep)
	assert.True(t, apierrors.IsNotFound(err), "forwarder Deployment must not start before topology state is durable")
	generatedAgentKeySecret := &corev1.Secret{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace,
		Name:      search.MetricsForwarderAgentKeySecretNameForCluster(0),
	}, generatedAgentKeySecret)
	assert.True(t, apierrors.IsNotFound(err), "forwarder dependencies must not be replicated before topology state is durable")
}

func TestReconcileCore_UncachedMissingTopologyIsRewrittenBeforeDeployment(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{ClusterIndex: ptr.To(0), Replicas: 1})
	stateCM.OwnerReferences = search.GetOwnerReferences()
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM)
	r.stateReader = mock.NewEmptyFakeClientBuilder().Build()

	fakeClientWithWatch, ok := fakeClient.(client.WithWatch)
	require.True(t, ok)
	var writes []string
	interceptedClient := kubernetesClient.NewClient(interceptor.NewClient(fakeClientWithWatch, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				writes = append(writes, "deployment")
			}
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == stateCM.Name {
				writes = append(writes, "state")
			}
			return cl.Update(ctx, obj, opts...)
		},
	}))
	r.kubeClient = interceptedClient
	r.clientForCluster = func(string) kubernetesClient.Client { return interceptedClient }
	currentSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)

	st := r.reconcileCore(context.Background(), currentSearch, zap.NewNop().Sugar())

	require.True(t, st.IsOK(), searchcontroller.MessageFromStatus(st))
	assert.Equal(t, []string{"state", "deployment"}, writes)
}

func TestReconcileCore_StableTopologyStateIsNotWritten(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{ClusterIndex: ptr.To(0), Replicas: 1})
	stateCM.OwnerReferences = search.GetOwnerReferences()
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM)

	fakeClientWithWatch, ok := fakeClient.(client.WithWatch)
	require.True(t, ok)
	stateWrites := 0
	interceptedClient := kubernetesClient.NewClient(interceptor.NewClient(fakeClientWithWatch, interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == stateCM.Name {
				stateWrites++
			}
			return cl.Update(ctx, obj, opts...)
		},
	}))
	r.kubeClient = interceptedClient
	currentSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)

	st := r.reconcileCore(context.Background(), currentSearch, zap.NewNop().Sugar())

	require.True(t, st.IsOK(), searchcontroller.MessageFromStatus(st))
	assert.Zero(t, stateWrites)
}

func TestReconcile_AggregatesAllMissingClusterClients(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0))},
		{Name: "cluster-b", Index: ptr.To(int32(1))},
	}
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret)
	r.clientForCluster = func(string) kubernetesClient.Client { return nil }

	st := r.reconcileCore(context.Background(), getMongoDBSearch(t, fakeClient, testNamespace, testSearchName), zap.S())

	require.Equal(t, status.PhasePending, st.Phase())
	message := searchcontroller.MessageFromStatus(st)
	assert.Contains(t, message, `cluster "cluster-a"`)
	assert.Contains(t, message, `cluster "cluster-b"`)
}

func TestReconcileCore_LegacyTopologyStateOwnerReferenceIsAdopted(t *testing.T) {
	mdb := newTestMongoDB(testMDBName, testNamespace, testProjectCMName, testGroupID)
	search := newTestMongoDBSearch(testSearchName, testNamespace, testMDBName)
	projectCM := newTestProjectConfigMap(testProjectCMName, testNamespace, testOMBaseURL)
	agentKeySecret := newTestAgentKeySecret(testGroupID+"-group-secret", testNamespace)
	stateCM := newTestTopologyStateConfigMap(t, search, clusterTopologyState{Replicas: 1})
	stateCM.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "mongodb.com/v1",
		Kind:       "MongoDBSearch",
		Name:       search.Name,
	}}
	r, fakeClient := newMetricsForwarderReconciler(testDefaultImage, mdb, search, projectCM, agentKeySecret, stateCM)
	currentSearch := getMongoDBSearch(t, fakeClient, testNamespace, testSearchName)

	st := r.reconcileCore(context.Background(), currentSearch, zap.NewNop().Sugar())

	require.True(t, st.IsOK(), searchcontroller.MessageFromStatus(st))
	updatedStateCM := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: stateCM.Name}, updatedStateCM))
	require.Len(t, updatedStateCM.OwnerReferences, 1)
	assert.Equal(t, search.UID, updatedStateCM.OwnerReferences[0].UID)
}

// callReconcileTopologyState invokes reconcileTopologyState directly against the
// single-cluster (clusterName=="", clusterIndex=0) work item, bypassing the full
// Reconcile path so each test targets one state-machine transition. It loads and
// persists the topology state around the call, as reconcileCore does.
func callReconcileTopologyState(t *testing.T, r *MongoDBSearchMetricsForwarderReconciler, search *searchv1.MongoDBSearch, shardNames []string, agentSecretName string) (bool, error) {
	t.Helper()
	projectConfig := mdbv1.ProjectConfig{BaseURL: testOMBaseURL}
	w := clusterWorkItem{ClusterName: "", ClusterIndex: 0, Client: r.kubeClient}
	topologyState, err := r.loadTopologyState(context.Background(), search)
	if err != nil {
		return false, err
	}
	pending, err := r.reconcileTopologyState(context.Background(), search, shardNames, testGroupID, projectConfig, agentSecretName, topologyState, w, zap.NewNop().Sugar())
	if err != nil {
		return false, err
	}
	return pending, r.openTopologyStateStore(search).WriteState(context.Background(), topologyState, zap.NewNop().Sugar())
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
