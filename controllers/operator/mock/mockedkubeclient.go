package mock

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"

	"github.com/hashicorp/go-multierror"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/config"

	"golang.org/x/xerrors"

	"github.com/go-logr/logr"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	"k8s.io/apimachinery/pkg/types"

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

	"github.com/10gen/ops-manager-kubernetes/controllers/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	appsv1 "k8s.io/api/apps/v1"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
)

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
func (c *MockedConfigMapClient) GetConfigMap(ctx context.Context, objectKey client.ObjectKey) (corev1.ConfigMap, error) {
	cm := corev1.ConfigMap{}
	if err := c.client.Get(ctx, objectKey, &cm); err != nil {
		return corev1.ConfigMap{}, err
	}
	return cm, nil
}

// UpdateConfigMap provides a thin wrapper and client.Client to update corev1.ConfigMap types
func (c *MockedConfigMapClient) UpdateConfigMap(ctx context.Context, cm corev1.ConfigMap) error {
	if err := c.client.Update(ctx, &cm); err != nil {
		return err
	}
	return nil
}

// CreateConfigMap provides a thin wrapper and client.Client to create corev1.ConfigMap types
func (c *MockedConfigMapClient) CreateConfigMap(ctx context.Context, cm corev1.ConfigMap) error {
	if err := c.client.Create(ctx, &cm); err != nil {
		return err
	}
	return nil
}

func (m *MockedConfigMapClient) DeleteConfigMap(ctx context.Context, key client.ObjectKey) error {
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := m.client.Delete(ctx, &cm); err != nil {
		return err
	}
	return nil
}

type MockedSecretClient struct {
	client client.Client
}

// GetSecret provides a thin wrapper and client.Client to access corev1.Secret types
func (c *MockedSecretClient) GetSecret(ctx context.Context, objectKey client.ObjectKey) (corev1.Secret, error) {
	s := corev1.Secret{}
	if err := c.client.Get(ctx, objectKey, &s); err != nil {
		return corev1.Secret{}, err
	}
	return s, nil
}

// UpdateSecret provides a thin wrapper and client.Client to update corev1.Secret types
func (c *MockedSecretClient) UpdateSecret(ctx context.Context, secret corev1.Secret) error {
	if err := c.client.Update(ctx, &secret); err != nil {
		return err
	}
	return nil
}

// CreateSecret provides a thin wrapper and client.Client to create corev1.Secret types
func (c *MockedSecretClient) CreateSecret(ctx context.Context, secret corev1.Secret) error {
	return c.client.Create(ctx, &secret)
}

// DeleteSecret provides a thin wrapper and client.Client to delete corev1.Secret types
func (c *MockedSecretClient) DeleteSecret(ctx context.Context, key client.ObjectKey) error {
	s := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := c.client.Delete(ctx, &s); err != nil {
		return err
	}
	return nil
}

type MockedServiceClient struct {
	client client.Client
}

// GetService provides a thin wrapper and client.Client to access corev1.Service types
func (c *MockedServiceClient) GetService(ctx context.Context, objectKey client.ObjectKey) (corev1.Service, error) {
	s := corev1.Service{}
	if err := c.client.Get(ctx, objectKey, &s); err != nil {
		return corev1.Service{}, err
	}
	return s, nil
}

// UpdateService provides a thin wrapper and client.Client to update corev1.Service types
func (c *MockedServiceClient) UpdateService(ctx context.Context, secret corev1.Service) error {
	if err := c.client.Update(ctx, &secret); err != nil {
		return err
	}
	return nil
}

// CreateService provides a thin wrapper and client.Client to create corev1.Service types
func (c *MockedServiceClient) CreateService(ctx context.Context, s corev1.Service) error {
	return c.client.Create(ctx, &s)
}

// DeleteService provides a thin wrapper and client.Client to delete corev1.Service types
func (c *MockedSecretClient) DeleteService(ctx context.Context, key client.ObjectKey) error {
	s := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := c.client.Delete(ctx, &s); err != nil {
		return err
	}
	return nil
}

func (c *MockedSecretClient) ReadSecret(ctx context.Context, secretName types.NamespacedName, basePath string) (map[string]string, error) {
	return map[string]string{}, &errors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound}}
}

type MockedStatefulSetClient struct {
	client client.Client
}

// GetStatefulSet provides a thin wrapper and client.Client to access appsv1.StatefulSet types
func (c *MockedStatefulSetClient) GetStatefulSet(ctx context.Context, objectKey client.ObjectKey) (appsv1.StatefulSet, error) {
	sts := appsv1.StatefulSet{}
	if err := c.client.Get(ctx, objectKey, &sts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return sts, nil
}

// GetPod provides a thin wrapper and client.Client to access corev1.Pod types
func (c *MockedStatefulSetClient) GetPod(ctx context.Context, objectKey client.ObjectKey) (corev1.Pod, error) {
	pod := corev1.Pod{}
	if err := c.client.Get(ctx, objectKey, &pod); err != nil {
		return corev1.Pod{}, err
	}
	return pod, nil
}

// UpdateStatefulSet provides a thin wrapper and client.Client to update appsv1.StatefulSet types
func (c *MockedStatefulSetClient) UpdateStatefulSet(ctx context.Context, sts appsv1.StatefulSet) (appsv1.StatefulSet, error) {
	updatesSts := sts
	if err := c.client.Update(ctx, &updatesSts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return updatesSts, nil
}

// CreateStatefulSet provides a thin wrapper and client.Client to create appsv1.StatefulSet types
func (c *MockedStatefulSetClient) CreateStatefulSet(ctx context.Context, sts appsv1.StatefulSet) error {
	return c.client.Create(ctx, &sts)
}

// DeleteStatefulSet provides a thin wrapper and client.Client to delete appsv1.StatefulSet types
func (c *MockedStatefulSetClient) DeleteStatefulSet(ctx context.Context, key client.ObjectKey) error {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := c.client.Delete(ctx, &sts); err != nil {
		return err
	}
	return nil
}

// MockedClient is the mocked implementation of client.Client from controller-runtime library
type MockedClient struct {
	client.Client
	*MockedConfigMapClient
	*MockedSecretClient
	*MockedServiceClient
	*MockedStatefulSetClient

	// if the StatefulSet created must be marked ready right after creation
	markStsReady  bool
	UpdateFunc    func(ctx context.Context, obj apiruntime.Object) error
	objectTracker testing.ObjectTracker
}

var _ kubernetesClient.Client = &MockedClient{}

func NewClient() *MockedClient {
	builder := fake.ClientBuilder{}
	s, err := v1.SchemeBuilder.Build()
	if err != nil {
		return nil
	}
	err = metav1.AddMetaToScheme(s)
	if err != nil {
		return nil
	}

	err = corev1.AddToScheme(s)
	if err != nil {
		return nil
	}

	err = appsv1.AddToScheme(s)
	if err != nil {
		return nil
	}

	ot := testing.NewObjectTracker(s, scheme.Codecs.UniversalDecoder())
	cl := builder.WithScheme(s).WithObjectTracker(ot).Build()

	api := MockedClient{Client: cl, objectTracker: ot}
	api.MockedConfigMapClient = &MockedConfigMapClient{client: &api}
	api.MockedSecretClient = &MockedSecretClient{client: &api}
	api.MockedServiceClient = &MockedServiceClient{client: &api}
	api.MockedStatefulSetClient = &MockedStatefulSetClient{client: &api}

	// mark StatefulSet ready right away by default
	api.markStsReady = true

	// ugly but seems the only way to clean om global variable for current connection (as golang doesnt' have setup()/teardown()
	// methods for testing
	om.CurrMockedConnection = nil

	return &api
}

func (m *MockedClient) GroupVersionKindFor(obj apiruntime.Object) (schema.GroupVersionKind, error) {
	panic("not implemented")
}

func (m *MockedClient) IsObjectNamespaced(obj apiruntime.Object) (bool, error) {
	panic("not implemented")
}

func (m *MockedClient) RESTMapper() meta.RESTMapper {
	return nil
}

func (m *MockedClient) Scheme() *apiruntime.Scheme {
	return nil
}

func (m *MockedClient) WithResource(ctx context.Context, object client.Object) *MockedClient {
	if object.GetResourceVersion() != "" {
		object.SetResourceVersion("")
	}

	err := m.Create(ctx, object)
	if err != nil {
		// panicking here instead of adding to return type as this function
		// is used to initialize the mocked client, with this we can ensure we never
		// start in a situation with a resource that has a naming violation.
		panic(err)
	}
	return m
}

func (m *MockedClient) AddProjectConfigMap(ctx context.Context, projectName, organizationId string) *MockedClient {
	cm := configmap.Builder().
		SetName(TestProjectConfigMapName).
		SetNamespace(TestNamespace).
		SetDataField(util.OmBaseUrl, "http://mycompany.com:8080").
		SetDataField(util.OmProjectName, projectName).
		SetDataField(util.OmOrgId, organizationId).
		Build()

	err := m.Create(ctx, &cm)
	if err != nil {
		panic(err)
	}
	return m
}

// AddCredentialsSecret creates the Secret that stores Ops Manager credentials for the test environment.
func (m *MockedClient) AddCredentialsSecret(ctx context.Context, publicKey, privateKey string) *MockedClient {
	stringData := map[string]string{util.OmPublicApiKey: publicKey, util.OmPrivateKey: privateKey}
	data := map[string][]byte{}
	for s, s2 := range stringData {
		data[s] = []byte(s2)
	}
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TestCredentialsSecretName, Namespace: TestNamespace},
		// we are using Data and not SecretData because our internal secret.Builder only writes information into
		// secret.Data not secret.StringData.
		Data: data,
	}
	err := m.Create(ctx, credentials)
	if err != nil {
		panic(err)
	}
	return m
}

func (m *MockedClient) WithStsReady(ready bool) *MockedClient {
	m.markStsReady = ready
	return m
}

func (m *MockedClient) AddDefaultMdbConfigResources(ctx context.Context) *MockedClient {
	m = m.AddProjectConfigMap(ctx, om.TestGroupName, "")
	return m.AddCredentialsSecret(ctx, om.TestUser, om.TestApiKey)
}

func (m *MockedClient) SubResource(subResource string) client.SubResourceClient {
	panic("implement me")
}

// Get retrieves an obj for the given object key from the Kubernetes Cluster.
// obj must be a struct pointer so that obj can be updated with the response
// returned by the Server.
func (m *MockedClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) (e error) {
	err := m.Client.Get(ctx, key, obj, opts...)
	if err == nil {
		switch v := obj.(type) {
		case *appsv1.StatefulSet:
			m.onStatefulsetUpdate(v)
		}
	}

	return err
}

func (m *MockedClient) ApproveAllCSRs(ctx context.Context) {
	for _, csrObject := range m.GetMapForObject(&certsv1.CertificateSigningRequest{}) {
		csr := csrObject.(*certsv1.CertificateSigningRequest)
		approvedCondition := certsv1.CertificateSigningRequestCondition{
			Type: certsv1.CertificateApproved,
		}
		csr.Status.Conditions = append(csr.Status.Conditions, approvedCondition)
		if err := m.Update(ctx, csr); err != nil {
			panic(err)
		}
	}
}

// Create saves the object obj in the Kubernetes cluster.
func (m *MockedClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := validateDNS1123Subdomain(obj); err != nil {
		return err
	}

	return m.Client.Create(ctx, obj, opts...)
}

// Update updates the given obj in the Kubernetes cluster. obj must be a
// struct pointer so that obj can be updated with the content returned by the Server.
func (m *MockedClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	existingObj := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	_ = m.Client.Get(context.TODO(), key, existingObj)

	// Update the object with RV, otherwise occ will not allow the rv to have changed in between.
	// Since we are not properly calling this method to retrieve the rv we can do it here instead.
	if existingObj != nil {
		obj.SetResourceVersion(existingObj.GetResourceVersion())
	}

	return m.Client.Update(ctx, obj, opts...)
}

var _ client.StatusWriter = &MockedStatusWriter{}

type MockedStatusWriter struct {
	parent *MockedClient
}

func (m *MockedStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	panic("implement me")
}

func (m *MockedStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return m.parent.Update(ctx, obj)
}

func (m *MockedStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return m.parent.Patch(ctx, obj, patch)
}

func (m *MockedClient) Status() client.StatusWriter {
	// MockedClient also implements StatusWriter and the Update function does what we need
	return &MockedStatusWriter{parent: m}
}

// GetAndUpdate Not used in enterprise, these only exist in community.
func (m *MockedClient) GetAndUpdate(ctx context.Context, nsName types.NamespacedName, obj client.Object, updateFunc func()) error {
	return nil
}

func (m *MockedClient) CreateOrUpdate(ctx context.Context, obj client.Object) error {
	// Determine the object's metadata
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return fmt.Errorf("object is not a metav1.Object")
	}

	namespace := metaObj.GetNamespace()
	name := metaObj.GetName()

	existingObj := obj.DeepCopyObject().(client.Object) // Create a deep copy to store the existing object
	err := m.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, existingObj)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get object: %v", err)
		}

		err = m.Create(ctx, obj)
		if err != nil {
			return fmt.Errorf("failed to create object: %v", err)
		}
		return nil
	}

	err = m.Update(ctx, obj)
	if err != nil {
		return fmt.Errorf("failed to update object: %v", err)
	}

	return nil
}

// onStatefulsetUpdate emulates statefulsets reaching their desired state, also OM automation agents get "registered"
func (m *MockedClient) onStatefulsetUpdate(set *appsv1.StatefulSet) {
	if m.markStsReady {
		markStatefulSetsReady(set)
	}
}

func markStatefulSetsReady(set *appsv1.StatefulSet) {
	set.Status.UpdatedReplicas = *set.Spec.Replicas
	set.Status.ReadyReplicas = *set.Spec.Replicas
	set.Status.Replicas = *set.Spec.Replicas

	if om.CurrMockedConnection != nil {
		// For tests with external domains we set hostnames externally in test,
		// as we don't have ExternalAccessConfiguration object in stateful set.
		// For tests that don't set hostnames we preserve old behaviour.
		hostnames := om.CurrMockedConnection.Hostnames
		if hostnames == nil {
			if val, ok := set.Annotations[handler.MongoDBMultiResourceAnnotation]; ok {
				hostnames = dns.GetMultiClusterProcessHostnames(val, set.Namespace, multicluster.MustGetClusterNumFromMultiStsName(set.Name), int(*set.Spec.Replicas), "cluster.local", nil)
			} else {
				// We also "register" automation agents.
				// So far we don't support custom cluster name
				hostnames, _ = dns.GetDnsForStatefulSet(*set, "", nil)
			}
		}
		om.CurrMockedConnection.AddHosts(hostnames)
	}
}

func (m *MockedClient) GetMapForObject(obj apiruntime.Object) map[client.ObjectKey]apiruntime.Object {
	switch obj.(type) {
	case *corev1.Secret:
		secrets := &corev1.SecretList{}
		if err := m.List(context.TODO(), secrets); err != nil {
			return nil
		}
		secretMap := make(map[client.ObjectKey]apiruntime.Object, len(secrets.Items))
		for _, secret := range secrets.Items {
			secretMap[client.ObjectKey{
				Namespace: secret.Namespace,
				Name:      secret.Name,
			}] = &secret
		}
		return secretMap
	case *appsv1.StatefulSet:
		statefulSets := &appsv1.StatefulSetList{}
		if err := m.List(context.TODO(), statefulSets); err != nil {
			return nil
		}
		statefulSetMap := make(map[client.ObjectKey]apiruntime.Object, len(statefulSets.Items))
		for _, statefulSet := range statefulSets.Items {
			statefulSetMap[client.ObjectKey{
				Namespace: statefulSet.Namespace,
				Name:      statefulSet.Name,
			}] = &statefulSet
		}
		return statefulSetMap
	case *corev1.Service:
		services := &corev1.ServiceList{}
		if err := m.List(context.TODO(), services); err != nil {
			return nil
		}
		serviceMap := make(map[client.ObjectKey]apiruntime.Object, len(services.Items))
		for _, service := range services.Items {
			serviceMap[client.ObjectKey{
				Namespace: service.Namespace,
				Name:      service.Name,
			}] = &service
		}
		return serviceMap
	default:
		return nil
	}
}

// HistoryItem is an item that describe the invocation of 'client.client' method.
type HistoryItem struct {
	function     *runtime.Func
	resourceType reflect.Type
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

func (m *MockedManager) GetHTTPClient() *http.Client {
	panic("implement me")
}

func NewEmptyManager() *MockedManager {
	return &MockedManager{Client: NewClient()}
}

func NewManager(ctx context.Context, object client.Object) *MockedManager {
	return &MockedManager{Client: NewClient().WithResource(ctx, object)}
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
	return *admission.NewDecoder(apiruntime.NewScheme())
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

func (m *MockedManager) GetWebhookServer() webhook.Server {
	return nil
}

func (m *MockedManager) AddMetricsExtraHandler(path string, handler http.Handler) error {
	return nil
}

func (m *MockedManager) Elected() <-chan struct{} {
	return nil
}

func (m *MockedManager) GetLogger() logr.Logger {
	return logr.Logger{}
}

func (m *MockedManager) GetControllerOptions() config.Controller {
	duration := time.Duration(0)
	return config.Controller{
		CacheSyncTimeout: duration,
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
		errs = multierror.Append(errs, xerrors.Errorf("resource name: [%s] failed validation of type %s", objName, reflect.TypeOf(obj)))
		for _, err := range validationErrs {
			errs = multierror.Append(errs, xerrors.Errorf(err))
		}
		return errs
	}
	return nil
}
