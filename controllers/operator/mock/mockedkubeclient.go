package mock

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-multierror"
	"k8s.io/apimachinery/pkg/util/validation"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	jsonpatch "github.com/evanphx/json-patch"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/config/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
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

type MockedConfigMapClient struct {
	client client.Client
}

// GetConfigMap provides a thin wrapper and client.client to access corev1.ConfigMap types
func (c *MockedConfigMapClient) GetConfigMap(objectKey client.ObjectKey) (corev1.ConfigMap, error) {
	cm := corev1.ConfigMap{}
	if err := c.client.Get(context.TODO(), objectKey, &cm); err != nil {
		return corev1.ConfigMap{}, err
	}
	return cm, nil
}

// UpdateConfigMap provides a thin wrapper and client.Client to update corev1.ConfigMap types
func (c *MockedConfigMapClient) UpdateConfigMap(cm corev1.ConfigMap) error {
	if err := c.client.Update(context.TODO(), &cm); err != nil {
		return err
	}
	return nil
}

// CreateConfigMap provides a thin wrapper and client.Client to create corev1.ConfigMap types
func (c *MockedConfigMapClient) CreateConfigMap(cm corev1.ConfigMap) error {
	if err := c.client.Create(context.TODO(), &cm); err != nil {
		return err
	}
	return nil
}

func (m *MockedConfigMapClient) DeleteConfigMap(key client.ObjectKey) error {
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := m.client.Delete(context.TODO(), &cm); err != nil {
		return err
	}
	return nil
}

type MockedSecretClient struct {
	client client.Client
}

// GetSecret provides a thin wrapper and client.Client to access corev1.Secret types
func (c *MockedSecretClient) GetSecret(objectKey client.ObjectKey) (corev1.Secret, error) {
	s := corev1.Secret{}
	if err := c.client.Get(context.TODO(), objectKey, &s); err != nil {
		return corev1.Secret{}, err
	}
	return s, nil
}

// UpdateSecret provides a thin wrapper and client.Client to update corev1.Secret types
func (c *MockedSecretClient) UpdateSecret(secret corev1.Secret) error {
	if err := c.client.Update(context.TODO(), &secret); err != nil {
		return err
	}
	return nil
}

// CreateSecret provides a thin wrapper and client.Client to create corev1.Secret types
func (c *MockedSecretClient) CreateSecret(secret corev1.Secret) error {
	if err := c.client.Create(context.TODO(), &secret); err != nil {
		return err
	}
	return nil
}

// DeleteSecret provides a thin wrapper and client.Client to delete corev1.Secret types
func (c *MockedSecretClient) DeleteSecret(key client.ObjectKey) error {
	s := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := c.client.Delete(context.TODO(), &s); err != nil {
		return err
	}
	return nil
}

type MockedServiceClient struct {
	client client.Client
}

// GetService provides a thin wrapper and client.Client to access corev1.Service types
func (c *MockedServiceClient) GetService(objectKey client.ObjectKey) (corev1.Service, error) {
	s := corev1.Service{}
	if err := c.client.Get(context.TODO(), objectKey, &s); err != nil {
		return corev1.Service{}, err
	}
	return s, nil
}

// UpdateService provides a thin wrapper and client.Client to update corev1.Service types
func (c *MockedServiceClient) UpdateService(secret corev1.Service) error {
	if err := c.client.Update(context.TODO(), &secret); err != nil {
		return err
	}
	return nil
}

// CreateService provides a thin wrapper and client.Client to create corev1.Service types
func (c *MockedServiceClient) CreateService(s corev1.Service) error {
	if err := c.client.Create(context.TODO(), &s); err != nil {
		return err
	}
	return nil
}

// DeleteService provides a thin wrapper and client.Client to delete corev1.Service types
func (c *MockedSecretClient) DeleteService(key client.ObjectKey) error {
	s := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := c.client.Delete(context.TODO(), &s); err != nil {
		return err
	}
	return nil
}

type MockedStatefulSetClient struct {
	client client.Client
}

// GetService provides a thin wrapper and client.Client to access appsv1.StatefulSet types
func (c *MockedStatefulSetClient) GetStatefulSet(objectKey client.ObjectKey) (appsv1.StatefulSet, error) {
	sts := appsv1.StatefulSet{}
	if err := c.client.Get(context.TODO(), objectKey, &sts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return sts, nil
}

// GetPod provides a thin wrapper and client.Client to access corev1.Pod types
func (c *MockedStatefulSetClient) GetPod(objectKey client.ObjectKey) (corev1.Pod, error) {
	pod := corev1.Pod{}
	if err := c.client.Get(context.TODO(), objectKey, &pod); err != nil {
		return corev1.Pod{}, err
	}
	return pod, nil
}

// UpdateStatefulSet provides a thin wrapper and client.Client to update appsv1.StatefulSet types
func (c *MockedStatefulSetClient) UpdateStatefulSet(sts appsv1.StatefulSet) (appsv1.StatefulSet, error) {
	updatesSts := sts
	if err := c.client.Update(context.TODO(), &updatesSts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return updatesSts, nil
}

// CreateStatefulSet provides a thin wrapper and client.Client to create appsv1.StatefulSet types
func (c *MockedStatefulSetClient) CreateStatefulSet(sts appsv1.StatefulSet) error {
	if err := c.client.Create(context.TODO(), &sts); err != nil {
		return err
	}
	return nil
}

// DeleteStatefulSet provides a thin wrapper and client.Client to delete appsv1.StatefulSet types
func (c *MockedStatefulSetClient) DeleteStatefulSet(key client.ObjectKey) error {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := c.client.Delete(context.TODO(), &sts); err != nil {
		return err
	}
	return nil
}

// MockedClient is the mocked implementation of client.Client from controller-runtime library
type MockedClient struct {
	*MockedConfigMapClient
	*MockedSecretClient
	*MockedServiceClient
	*MockedStatefulSetClient

	// backingMap contains all of the maps of all apiruntime.Objects. Using the GetMapForObject
	// function will dynamically initialize a new map for the type in question
	backingMap map[reflect.Type]map[client.ObjectKey]apiruntime.Object

	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*HistoryItem

	// if the StatefulSet created must be marked ready right after creation
	markStsReady bool
	UpdateFunc   func(ctx context.Context, obj apiruntime.Object) error
}

var _ kubernetesClient.Client = &MockedClient{}

func NewClient() *MockedClient {
	api := MockedClient{}
	api.MockedConfigMapClient = &MockedConfigMapClient{client: &api}
	api.MockedSecretClient = &MockedSecretClient{client: &api}
	api.MockedServiceClient = &MockedServiceClient{client: &api}
	api.MockedStatefulSetClient = &MockedStatefulSetClient{client: &api}

	api.backingMap = map[reflect.Type]map[client.ObjectKey]apiruntime.Object{}

	// mark StatefulSet ready right away by default
	api.markStsReady = true

	// ugly but seems the only way to clean om global variable for current connection (as golang doesnt' have setup()/teardown()
	// methods for testing
	om.CurrMockedConnection = nil

	return &api
}

func (m *MockedClient) RESTMapper() meta.RESTMapper {
	return nil
}

func (m *MockedClient) Scheme() *apiruntime.Scheme {
	return nil
}

func (m *MockedClient) WithResource(object client.Object) *MockedClient {
	err := m.Create(context.TODO(), object.(client.Object))
	if err != nil {
		// panicking here instead of adding to return type as this function
		// is used to initialize the mocked client, with this we can ensure we never
		// start in a situation with a resource that has a naming violation.
		panic(err)
	}
	return m
}

func (m *MockedClient) AddProjectConfigMap(projectName, organizationId string) *MockedClient {
	cm := configmap.Builder().
		SetName(TestProjectConfigMapName).
		SetNamespace(TestNamespace).
		SetDataField(util.OmBaseUrl, "http://mycompany.com:8080").
		SetDataField(util.OmProjectName, projectName).
		SetDataField(util.OmOrgId, organizationId).
		Build()

	err := m.Create(context.TODO(), &cm)
	if err != nil {
		panic(err)
	}
	return m
}

// AddCredentialsSecret creates the Secret that stores Ops Manager credentials for the test environment.
func (m *MockedClient) AddCredentialsSecret(omUser, omPublicKey string) *MockedClient {
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TestCredentialsSecretName, Namespace: TestNamespace},
		StringData: map[string]string{util.OmUser: omUser, util.OmPublicApiKey: omPublicKey}}
	err := m.Create(context.TODO(), credentials)
	if err != nil {
		panic(err)
	}
	return m
}

func (m *MockedClient) WithStsReady(ready bool) *MockedClient {
	m.markStsReady = ready
	return m
}

func (m *MockedClient) AddDefaultMdbConfigResources() *MockedClient {
	m = m.AddProjectConfigMap(om.TestGroupName, "")
	return m.AddCredentialsSecret(om.TestUser, om.TestApiKey)
}

// Get retrieves an obj for the given object key from the Kubernetes Cluster.
// obj must be a struct pointer so that obj can be updated with the response
// returned by the Server.
func (k *MockedClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object) (e error) {
	resMap := k.GetMapForObject(obj)
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
	for _, csrObject := range k.GetMapForObject(&certsv1.CertificateSigningRequest{}) {
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
func (k *MockedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	// we don't need this
	return nil
}

// Create saves the object obj in the Kubernetes cluster.
func (k *MockedClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	obj = obj.DeepCopyObject().(client.Object)
	key := ObjectKeyFromApiObject(obj)
	resMap := k.GetMapForObject(obj)

	if err := validateDNS1123Subdomain(obj); err != nil {
		return err
	}

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
func (k *MockedClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := validateDNS1123Subdomain(obj); err != nil {
		return err
	}
	obj = obj.DeepCopyObject().(client.Object)
	k.addToHistory(reflect.ValueOf(k.Update), obj)
	if k.UpdateFunc != nil {
		return k.UpdateFunc(ctx, obj)
	}
	return k.doUpdate(ctx, obj)
}

func (k *MockedClient) doUpdate(ctx context.Context, obj client.Object) error {
	key := ObjectKeyFromApiObject(obj)

	resMap := k.GetMapForObject(obj)
	resMap[key] = obj

	switch v := obj.(type) {
	case *appsv1.StatefulSet:
		k.onStatefulsetUpdate(v)
	}
	return nil
}

// Delete deletes the given obj from Kubernetes cluster.
func (k *MockedClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	k.addToHistory(reflect.ValueOf(k.Delete), obj)

	key := ObjectKeyFromApiObject(obj)

	resMap := k.GetMapForObject(obj)
	delete(resMap, key)

	return nil
}

func (k *MockedClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return nil
}

func (k *MockedClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	// Finding the object to patch
	resMap := k.GetMapForObject(obj)
	k.addToHistory(reflect.ValueOf(k.Patch), obj)
	key := ObjectKeyFromApiObject(obj)
	if _, exists := resMap[key]; !exists {
		return &errors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound}}
	}
	targetObject := resMap[key]

	// Performing patch (serializing to bytes and then deserializing the result back)
	patchBytes, err := patch.Data(nil)
	if err != nil {
		return err
	}
	var jsonPatch jsonpatch.Patch
	jsonPatch, err = jsonpatch.DecodePatch(patchBytes)
	if err != nil {
		return err
	}

	var jsonObject []byte
	jsonObject, err = json.Marshal(targetObject)
	if err != nil {
		return err
	}
	jsonObject, err = jsonPatch.Apply(jsonObject)
	if err != nil {
		return err
	}

	newObject := obj.DeepCopyObject()
	if err = json.Unmarshal(jsonObject, newObject); err != nil {
		return err
	}
	resMap[key] = newObject

	return nil
}

func (k *MockedClient) Status() client.StatusWriter {
	// MockedClient also implements StatusWriter and the Update function does what we need
	k.addToHistory(reflect.ValueOf(k.Status), nil)
	return k
}

// Not used in enterprise, these only exist in community.
func (k *MockedClient) GetAndUpdate(nsName types.NamespacedName, obj client.Object, updateFunc func()) error {
	return nil
}

// Not used in enterprise, these only exist in community.
func (k *MockedClient) CreateOrUpdate(obj apiruntime.Object) error {
	return nil
}

// onStatefulsetUpdate emulates statefulsets reaching their desired state, also OM automation agents get "registered"
func (k *MockedClient) onStatefulsetUpdate(set *appsv1.StatefulSet) {
	if k.markStsReady {
		markStatefulSetsReady(set)
	}
}

func markStatefulSetsReady(set *appsv1.StatefulSet) {
	set.Status.UpdatedReplicas = *set.Spec.Replicas
	set.Status.ReadyReplicas = *set.Spec.Replicas

	if om.CurrMockedConnection != nil {
		// check first if it's multi-cluster STS
		var hostnames []string
		if val, ok := set.Annotations[handler.MongoDBMultiResourceAnnotation]; ok {
			hostnames = util.GetMultiClusterAgentHostnames(val, set.Namespace, multicluster.MustGetClusterNumFromMDBMName(set.Name), int(*set.Spec.Replicas))
		} else {
			// We also "register" automation agents.
			// So far we don't support custom cluster name
			hostnames, _ = util.GetDnsForStatefulSet(*set, "")
		}
		om.CurrMockedConnection.AddHosts(hostnames)
	}
}

func (oc *MockedClient) addToHistory(value reflect.Value, obj apiruntime.Object) {
	oc.history = append(oc.history, HItem(value, obj))
}

func (m *MockedClient) GetMapForObject(obj apiruntime.Object) map[client.ObjectKey]apiruntime.Object {
	t := reflect.TypeOf(obj)
	if _, ok := m.backingMap[t]; !ok {
		m.backingMap[t] = map[client.ObjectKey]apiruntime.Object{}
	}
	return m.backingMap[t]
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

func (oc *MockedClient) GetSet(key client.ObjectKey) *appsv1.StatefulSet {
	return oc.GetMapForObject(&appsv1.StatefulSet{})[key].(*appsv1.StatefulSet)
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
	Client *MockedClient
}

func NewEmptyManager() *MockedManager {
	return &MockedManager{Client: NewClient()}
}

func NewManager(object client.Object) *MockedManager {
	return &MockedManager{Client: NewClient().WithResource(object)}
}

func NewManagerSpecificClient(c *MockedClient) *MockedManager {
	return &MockedManager{Client: c}
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
func (m *MockedManager) Start(_ context.Context) error {
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
	return m.Client
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

func (m *MockedManager) AddMetricsExtraHandler(path string, handler http.Handler) error {
	return nil
}

func (m *MockedManager) Elected() <-chan struct{} {
	return nil
}

func (m *MockedManager) GetLogger() logr.Logger {
	return nil
}

func (m *MockedManager) GetControllerOptions() v1alpha1.ControllerConfigurationSpec {
	var duration = time.Duration(0)
	return v1alpha1.ControllerConfigurationSpec{
		CacheSyncTimeout: &duration,
	}
}

func ObjectKeyFromApiObject(obj interface{}) client.ObjectKey {
	ns := reflect.ValueOf(obj).Elem().FieldByName("Namespace").String()
	name := reflect.ValueOf(obj).Elem().FieldByName("Name").String()

	return types.NamespacedName{Name: name, Namespace: ns}
}

// validateDNS1123Subdomain ensures that the given Kubernetes object has a name which adheres
// to DNS1123.
func validateDNS1123Subdomain(obj apiruntime.Object) error {
	objName := reflect.ValueOf(obj).Elem().FieldByName("Name").String()
	validationErrs := validation.IsDNS1123Subdomain(objName)
	var errs error
	if len(validationErrs) > 0 {
		errs = multierror.Append(errs, fmt.Errorf("resource name: [%s] failed validation of type %s", objName, reflect.TypeOf(obj)))
		for _, err := range validationErrs {
			errs = multierror.Append(errs, fmt.Errorf(err))
		}
		return errs
	}
	return nil
}
