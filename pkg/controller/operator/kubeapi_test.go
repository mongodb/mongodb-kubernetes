package operator

import (
	"context"
	"runtime"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"

	"reflect"

	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
)

// todo rename the file to client_test.go later

const (
	TestProjectConfigMapName  = om.TestGroupName
	TestCredentialsSecretName = "my-credentials"
	TestNamespace             = "my-namespace"
)

// MockedClient is the mocked implementation of client.Client from controller-runtime library
type MockedClient struct {
	// Note that we have to specify 'apiruntime.Object' as values for maps to make 'getMapForObject()' method work
	// (poor polymorphism in Go... )
	// so please make sure that you put correct data to maps
	sets            map[client.ObjectKey]apiruntime.Object
	services        map[client.ObjectKey]apiruntime.Object
	configMaps      map[client.ObjectKey]apiruntime.Object
	secrets         map[client.ObjectKey]apiruntime.Object
	standalones     map[client.ObjectKey]apiruntime.Object
	replicaSets     map[client.ObjectKey]apiruntime.Object
	shardedClusters map[client.ObjectKey]apiruntime.Object
	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*HistoryItem
	// the delay for statefulsets "creation"
	StsCreationDelayMillis time.Duration
}

func newMockedClient(object apiruntime.Object) *MockedClient {
	return newMockedClientDetailed(object, om.TestGroupName, "")
}

// newMockedClientDelayed creates the kube client that emulates delay in statefulset creation
func newMockedClientDelayed(object apiruntime.Object, delayMillis time.Duration) *MockedClient {
	mockedClient := newMockedClient(object)
	mockedClient.StsCreationDelayMillis = delayMillis
	return mockedClient
}

// newMockedClientDetailed creates mocked Kubernetes client adding the kubernetes 'object' in parallel. This is necessary
// as in new controller-runtime library reconciliation code fetches the existing object so it should be added beforehand
func newMockedClientDetailed(object apiruntime.Object, projectName, organizationId string) *MockedClient {
	api := MockedClient{}
	api.sets = make(map[client.ObjectKey]apiruntime.Object)
	api.services = make(map[client.ObjectKey]apiruntime.Object)
	api.configMaps = make(map[client.ObjectKey]apiruntime.Object)
	api.secrets = make(map[client.ObjectKey]apiruntime.Object)
	api.standalones = make(map[client.ObjectKey]apiruntime.Object)
	api.replicaSets = make(map[client.ObjectKey]apiruntime.Object)
	api.shardedClusters = make(map[client.ObjectKey]apiruntime.Object)

	// initialize config map and secret to emulate user preparing environment
	project := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: TestProjectConfigMapName, Namespace: TestNamespace},
		Data:       map[string]string{util.OmBaseUrl: "http://mycompany.com:8080", util.OmProjectName: projectName, util.OmOrgId: organizationId}}
	api.Create(context.TODO(), project)

	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TestCredentialsSecretName, Namespace: TestNamespace},
		StringData: map[string]string{util.OmUser: "test@mycompany.com", util.OmPublicApiKey: "36lj245asg06s0h70245dstgft"}}
	api.Create(context.TODO(), credentials)

	if object != nil {
		api.Create(context.TODO(), object)
	}

	// no delay in creation by default
	api.StsCreationDelayMillis = 0

	// ugly but seems the only way to clean om global variable for current connection (as golang doesnt' have setup()/teardown()
	// methods for testing
	om.CurrMockedConnection = nil

	return &api
}

// Get retrieves an obj for the given object key from the Kubernetes Cluster.
// obj must be a struct pointer so that obj can be updated with the response
// returned by the Server.
func (k *MockedClient) Get(ctx context.Context, key client.ObjectKey, obj apiruntime.Object) (e error) {
	resMap := k.getMapForObject(obj)
	k.addToHistory(reflect.ValueOf(k.Get), obj)
	if _, exists := resMap[key]; !exists {
		return fmt.Errorf("%T %s doesn't exists!", obj, key)
	}
	// Golang cannot update pointers if they are declared as interfaces... Have to use reflection
	//*obj = *(resMap[key])
	v := reflect.ValueOf(obj).Elem()
	v.Set(reflect.ValueOf(resMap[key]).Elem())
	return nil
}

// List retrieves list of objects for a given namespace and list options. On a
// successful call, Items field in the list will be populated with the
// result returned from the server.
func (k *MockedClient) List(ctx context.Context, opts *client.ListOptions, list apiruntime.Object) error {
	// we don't need this
	return nil
}

// Create saves the object obj in the Kubernetes cluster.
func (k *MockedClient) Create(ctx context.Context, obj apiruntime.Object) error {
	key := objectKeyFromApiObject(obj)
	resMap := k.getMapForObject(obj)
	k.addToHistory(reflect.ValueOf(k.Create), obj)

	if err := k.Get(ctx, key, obj); err == nil {
		return fmt.Errorf("%T %s already exists!", obj, key)
	}

	// for secrets we perform some additional manipulation with data - copying it from string field to binary one
	switch s := obj.(type) {
	case *corev1.Secret:
		{
			if s.Data == nil {
				s.Data = make(map[string][]byte)
			}
			for k, v := range s.StringData {
				// seems the in-memory bytes are already decoded
				//sDec, _ := b64.StdEncoding.DecodeString(v)
				s.Data[k] = []byte(v)
			}
		}
	}

	resMap[key] = obj

	switch v := obj.(type) {
	case *appsv1.StatefulSet:
		k.onStatefulsetUpdate(v)
	}

	return nil
}

// Update updates the given obj in the Kubernetes cluster. obj must be a
// struct pointer so that obj can be updated with the content returned by the Server.
func (k *MockedClient) Update(ctx context.Context, obj apiruntime.Object) error {
	key := objectKeyFromApiObject(obj)
	k.addToHistory(reflect.ValueOf(k.Update), obj)

	resMap := k.getMapForObject(obj)
	resMap[key] = obj

	switch v := obj.(type) {
	case *appsv1.StatefulSet:
		k.onStatefulsetUpdate(v)
	}
	return nil
}

// Delete deletes the given obj from Kubernetes cluster.
func (k *MockedClient) Delete(ctx context.Context, obj apiruntime.Object, opts ...client.DeleteOptionFunc) error {
	k.addToHistory(reflect.ValueOf(k.Delete), obj)
	// we don't need this implementation
	return nil
}

func (k *MockedClient) Status() client.StatusWriter {
	// MockedClient also implements StatusWriter and the Update function does what we need
	k.addToHistory(reflect.ValueOf(k.Status), nil)
	return k
}

// onStatefulsetUpdate emulates statefulsets reaching their desired state, also OM automation agents get "registered"
func (k *MockedClient) onStatefulsetUpdate(set *appsv1.StatefulSet) {
	if k.StsCreationDelayMillis == 0 {
		markStatefulSetsReady(set)
	} else {
		go func() {
			time.Sleep(k.StsCreationDelayMillis * time.Millisecond)
			markStatefulSetsReady(set)
		}()
	}
}

func markStatefulSetsReady(set *appsv1.StatefulSet) {
	set.Status.ReadyReplicas = *set.Spec.Replicas

	if om.CurrMockedConnection != nil {
		// We also "register" automation agents.
		// So far we don't support custom cluster name
		hostnames, _ := GetDnsForStatefulSet(set, "")

		om.CurrMockedConnection.AddHosts(hostnames)
	}
}

func (oc *MockedClient) addToHistory(value reflect.Value, obj apiruntime.Object) {
	oc.history = append(oc.history, HItem(value, obj))
}

func (oc *MockedClient) getMapForObject(obj apiruntime.Object) map[client.ObjectKey]apiruntime.Object {
	switch obj.(type) {
	case *appsv1.StatefulSet:
		return oc.sets
	case *corev1.Secret:
		return oc.secrets
	case *corev1.ConfigMap:
		return oc.configMaps
	case *corev1.Service:
		return oc.services
	case *v1.MongoDbStandalone:
		return oc.standalones
	case *v1.MongoDbReplicaSet:
		return oc.replicaSets
	case *v1.MongoDbShardedCluster:
		return oc.shardedClusters
	}
	return nil
}

func (oc *MockedClient) CheckOrderOfOperations(t *testing.T, value ...*HistoryItem) {
	j := 0
	matched := ""
	for _, h := range oc.history {
		if *h == *value[j] {
			matched += fmt.Sprintf("%s ", h.String())
			j++
		}
		if j == len(value) {
			break
		}
	}
	assert.Equal(t, len(value), j, "Only %d of %d expected operations happened in expected order (%s)", j, len(value), matched)
}

func (oc *MockedClient) CheckNumberOfOperations(t *testing.T, value *HistoryItem, expected int) {
	count := 0
	for _, h := range oc.history {
		if *h == *value {
			count++
		}
	}
	assert.Equal(t, expected, count, "Expected to have been %d %s operations but there were %d", expected, value.function.Name(), count)
}

func (oc *MockedClient) CheckOperationsDidntHappen(t *testing.T, value ...*HistoryItem) {
	for _, h := range oc.history {
		for _, o := range value {
			assert.NotEqual(t, o, h, "Operation %v is not expected to happen", h)
		}
	}
}

func (oc *MockedClient) getSet(key client.ObjectKey) *appsv1.StatefulSet {
	return oc.sets[key].(*appsv1.StatefulSet)
}

// HistoryItem is an item that describe the invocation of 'client.client' method.
type HistoryItem struct {
	function     *runtime.Func
	resourceType reflect.Type
}

func HItem(value reflect.Value, obj apiruntime.Object) *HistoryItem {
	historyItem := &HistoryItem{function: runtime.FuncForPC(value.Pointer())}
	if obj != nil {
		historyItem.resourceType = reflect.ValueOf(obj).Type()
	} else {
		historyItem.resourceType = nil
	}
	return historyItem
}

func (h HistoryItem) String() string {
	resourceTypeStr := "nil"
	if h.resourceType != nil {
		resourceTypeStr = h.resourceType.String()
	}
	return fmt.Sprintf("%s-%s", h.function.Name(), resourceTypeStr)
}

// MockedManager is the mock implementation of `Manager` from controller-runtime library. The only interesting method though
// is `getClient`
type MockedManager struct {
	client *MockedClient
}

func newMockedManager(object apiruntime.Object) *MockedManager {
	return &MockedManager{client: newMockedClient(object)}
}
func newMockedManagerDetailed(object apiruntime.Object, projectName, organizationId string) *MockedManager {
	return &MockedManager{client: newMockedClientDetailed(object, projectName, organizationId)}
}

func newMockedManagerSpecificClient(c *MockedClient) *MockedManager {
	return &MockedManager{client: c}
}

func (m *MockedManager) Add(runnable manager.Runnable) error {
	return nil
}

// SetFields will set any dependencies on an object for which the object has implemented the inject
// interface - e.g. inject.Client.
func (m *MockedManager) SetFields(interface{}) error {
	return nil
}

// Start starts all registered Controllers and blocks until the Stop channel is closed.
// Returns an error if there is an error starting any controller.
func (m *MockedManager) Start(<-chan struct{}) error {
	return nil
}

// GetConfig returns an initialized Config
func (m *MockedManager) GetConfig() *rest.Config {
	return nil
}

// GetScheme returns and initialized Scheme
func (m *MockedManager) GetScheme() *apiruntime.Scheme {
	return nil
}

// GetAdmissionDecoder returns the runtime.Decoder based on the scheme.
func (m *MockedManager) GetAdmissionDecoder() types.Decoder {
	return nil
}

// GetClient returns a client configured with the Config
func (m *MockedManager) GetClient() client.Client {
	return m.client
}

// GetFieldIndexer returns a client.FieldIndexer configured with the client
func (m *MockedManager) GetFieldIndexer() client.FieldIndexer {
	return nil
}

// GetCache returns a cache.Cache
func (m *MockedManager) GetCache() cache.Cache {
	return nil
}

// GetRecorder returns a new EventRecorder for the provided name
func (m *MockedManager) GetRecorder(name string) record.EventRecorder {
	return nil
}

// GetRESTMapper returns a RESTMapper
func (m *MockedManager) GetRESTMapper() meta.RESTMapper {
	return nil
}
