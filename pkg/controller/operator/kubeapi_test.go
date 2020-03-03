package operator

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube/configmap"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"

	"reflect"

	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
)

// todo rename the file to client_test.go later

const (
	TestProjectConfigMapName  = om.TestGroupName
	TestCredentialsSecretName = "my-credentials"
	TestNamespace             = "my-namespace"
	TestMongoDBName           = "my-mongodb"
)

// MockedClient is the mocked implementation of client.Client from controller-runtime library
type MockedClient struct {
	// Note that we have to specify 'apiruntime.Object' as values for maps to make 'getMapForObject()' method work
	// (poor polymorphism in Go... )
	// so please make sure that you put correct data to maps
	sets             map[client.ObjectKey]apiruntime.Object
	services         map[client.ObjectKey]apiruntime.Object
	configMaps       map[client.ObjectKey]apiruntime.Object
	secrets          map[client.ObjectKey]apiruntime.Object
	mongoDbResources map[client.ObjectKey]apiruntime.Object
	opsManagers      map[client.ObjectKey]apiruntime.Object
	csrs             map[client.ObjectKey]apiruntime.Object
	users            map[client.ObjectKey]apiruntime.Object
	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*HistoryItem
	// the delay for statefulsets "creation"
	StsCreationDelayMillis time.Duration
	UpdateFunc             func(ctx context.Context, obj apiruntime.Object) error
}

func newMockedClient() *MockedClient {
	api := MockedClient{}
	api.sets = make(map[client.ObjectKey]apiruntime.Object)
	api.services = make(map[client.ObjectKey]apiruntime.Object)
	api.configMaps = make(map[client.ObjectKey]apiruntime.Object)
	api.secrets = make(map[client.ObjectKey]apiruntime.Object)
	api.mongoDbResources = make(map[client.ObjectKey]apiruntime.Object)
	api.opsManagers = make(map[client.ObjectKey]apiruntime.Object)
	api.csrs = make(map[client.ObjectKey]apiruntime.Object)
	api.users = make(map[client.ObjectKey]apiruntime.Object)

	// no delay in creation by default
	api.StsCreationDelayMillis = 0

	// ugly but seems the only way to clean om global variable for current connection (as golang doesnt' have setup()/teardown()
	// methods for testing
	om.CurrMockedConnection = nil

	return &api
}

func (m *MockedClient) WithResource(object apiruntime.Object) *MockedClient {
	m.Create(context.TODO(), object.DeepCopyObject())
	return m
}

func (m *MockedClient) AddProjectConfigMap(projectName, organizationId string) *MockedClient {
	cm := configmap.Builder().
		SetName(TestProjectConfigMapName).
		SetNamespace(TestNamespace).
		SetField(util.OmBaseUrl, "http://mycompany.com:8080").
		SetField(util.OmProjectName, projectName).
		SetField(util.OmOrgId, organizationId).
		Build()

	m.Create(context.TODO(), &cm)
	return m
}

func (m *MockedClient) AddCredentialsSecret(omUser, omPublicKey string) *MockedClient {
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TestCredentialsSecretName, Namespace: TestNamespace},
		StringData: map[string]string{util.OmUser: omUser, util.OmPublicApiKey: omPublicKey}}
	m.Create(context.TODO(), credentials)
	return m
}

func (m *MockedClient) WithStsCreationDelay(delayMillis time.Duration) *MockedClient {
	m.StsCreationDelayMillis = delayMillis
	return m
}

func (m *MockedClient) AddDefaultMdbConfigResources() *MockedClient {
	m = m.AddProjectConfigMap(om.TestGroupName, "")
	return m.AddCredentialsSecret(om.TestUser, om.TestApiKey)
}

// Get retrieves an obj for the given object key from the Kubernetes Cluster.
// obj must be a struct pointer so that obj can be updated with the response
// returned by the Server.
func (k *MockedClient) Get(ctx context.Context, key client.ObjectKey, obj apiruntime.Object) (e error) {
	resMap := k.getMapForObject(obj)
	k.addToHistory(reflect.ValueOf(k.Get), obj)
	if _, exists := resMap[key]; !exists {
		return &errors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound}}
	}
	// Golang cannot update pointers if they are declared as interfaces... Have to use reflection
	//*obj = *(resMap[key])
	v := reflect.ValueOf(obj).Elem()
	v.Set(reflect.ValueOf(resMap[key]).Elem())
	return nil
}

func (k *MockedClient) ApproveAllCSRs() {
	for _, csrObject := range k.csrs {
		csr := csrObject.(*certsv1.CertificateSigningRequest)
		approvedCondition := certsv1.CertificateSigningRequestCondition{
			Type: certsv1.CertificateApproved,
		}
		csr.Status.Conditions = append(csr.Status.Conditions, approvedCondition)
		if err := k.Update(context.Background(), csr); err != nil {
			panic(err)
		}
	}
}

// List retrieves list of objects for a given namespace and list options. On a
// successful call, Items field in the list will be populated with the
// result returned from the server.
func (k *MockedClient) List(ctx context.Context, list apiruntime.Object, opts ...client.ListOption) error {
	// we don't need this
	return nil
}

// Create saves the object obj in the Kubernetes cluster.
func (k *MockedClient) Create(ctx context.Context, obj apiruntime.Object, opts ...client.CreateOption) error {
	obj = obj.DeepCopyObject()
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
func (k *MockedClient) Update(ctx context.Context, obj apiruntime.Object, opts ...client.UpdateOption) error {
	obj = obj.DeepCopyObject()
	k.addToHistory(reflect.ValueOf(k.Update), obj)
	if k.UpdateFunc != nil {
		return k.UpdateFunc(ctx, obj)
	}
	return k.doUpdate(ctx, obj)
}

func (k *MockedClient) doUpdate(ctx context.Context, obj apiruntime.Object) error {
	key := objectKeyFromApiObject(obj)

	resMap := k.getMapForObject(obj)
	resMap[key] = obj

	switch v := obj.(type) {
	case *appsv1.StatefulSet:
		k.onStatefulsetUpdate(v)
	}
	return nil
}

// Delete deletes the given obj from Kubernetes cluster.
func (k *MockedClient) Delete(ctx context.Context, obj apiruntime.Object, opts ...client.DeleteOption) error {
	k.addToHistory(reflect.ValueOf(k.Delete), obj)

	key := objectKeyFromApiObject(obj)

	resMap := k.getMapForObject(obj)
	delete(resMap, key)

	return nil
}

func (k *MockedClient) DeleteAllOf(ctx context.Context, obj apiruntime.Object, opts ...client.DeleteAllOfOption) error {
	return nil
}

func (k *MockedClient) Patch(ctx context.Context, obj apiruntime.Object, patch client.Patch, opts ...client.PatchOption) error {
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
	set.Status.UpdatedReplicas = *set.Spec.Replicas
	set.Status.ReadyReplicas = *set.Spec.Replicas

	if om.CurrMockedConnection != nil {
		// We also "register" automation agents.
		// So far we don't support custom cluster name
		hostnames, _ := util.GetDnsForStatefulSet(*set, "")

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
	case *mdbv1.MongoDB:
		return oc.mongoDbResources
	case *certsv1.CertificateSigningRequest:
		return oc.csrs
	case *mdbv1.MongoDBUser:
		return oc.users
	case *mdbv1.MongoDBOpsManager:
		return oc.opsManagers
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
			if *h == *o {
				assert.Fail(t, "Operation is not expected to happen", "%v is not expected to happen", *h)
			}
		}
	}
}

func (oc *MockedClient) ClearHistory() {
	oc.history = []*HistoryItem{}
}

func (oc *MockedClient) getSet(key client.ObjectKey) *appsv1.StatefulSet {
	return oc.sets[key].(*appsv1.StatefulSet)
}

// convenience method to get a helper from the mocked client
func (oc *MockedClient) helper() *KubeHelper {
	helper := NewKubeHelper(oc)
	return &helper
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

func newEmptyMockedManager() *MockedManager {
	return &MockedManager{client: newMockedClient()}
}

func newMockedManager(object apiruntime.Object) *MockedManager {
	return &MockedManager{client: newMockedClient().WithResource(object)}
}

func newMockedManagerSpecificClient(c *MockedClient) *MockedManager {
	return &MockedManager{client: c}
}

func (m *MockedManager) Add(runnable manager.Runnable) error {
	return nil
}

func (m *MockedManager) AddHealthzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *MockedManager) AddReadyzCheck(name string, check healthz.Checker) error {
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
func (m *MockedManager) GetAdmissionDecoder() admission.Decoder {
	// just returning nothing
	d, _ := admission.NewDecoder(apiruntime.NewScheme())
	return *d
}

// GetAPIReader returns the client reader
func (m *MockedManager) GetAPIReader() client.Reader {
	return nil
}

// GetClient returns a client configured with the Config
func (m *MockedManager) GetClient() client.Client {
	return m.client
}

func (m *MockedManager) GetEventRecorderFor(name string) record.EventRecorder {
	return nil
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

func (m *MockedManager) GetWebhookServer() *webhook.Server {
	return nil
}
