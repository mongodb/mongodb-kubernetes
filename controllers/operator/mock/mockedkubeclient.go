package mock

import (
	"context"
	"fmt"
	"reflect"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1 "k8s.io/api/apps/v1"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	TestProjectConfigMapName  = om.TestGroupName
	TestCredentialsSecretName = "my-credentials"
	TestNamespace             = "my-namespace"
	TestMongoDBName           = "my-mongodb"
)

// NewDefaultFakeClient initializes returns fake kube client and omConnectionFactory that is suitable for most uses.
// This fake client is initialized with default project resources (project config map and credentials secret, see GetDefaultResources) that are required most of the time to reconcile the resource.
// It automatically adds list of objects to the client.
//
// The reason we couple kube fake client with OM's connection factory is that most tests we rely on the behavior of automatically marking created statefulset to be ready
// along with simulating that hostnames from monitoring are registered to the current OM connecti on.
func NewDefaultFakeClient(objects ...client.Object) (kubernetesClient.Client, *om.CachedOMConnectionFactory) {
	omConnectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	return NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory, objects...), omConnectionFactory
}

// NewDefaultFakeClientWithOMConnectionFactory is the same as NewDefaultFakeClient, but you can pass omConnectionFactory from outside.
func NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory *om.CachedOMConnectionFactory, objects ...client.Object) kubernetesClient.Client {
	return NewEmptyFakeClientWithInterceptor(omConnectionFactory, append(objects, GetDefaultResources()...)...)
}

// NewEmptyFakeClientWithInterceptor initializes empty fake kube client with interceptor for automatically marking statefulsets as ready.
// It doesn't add any default resources, but adds passed objects if any.
func NewEmptyFakeClientWithInterceptor(omConnectionFactory *om.CachedOMConnectionFactory, objects ...client.Object) kubernetesClient.Client {
	fakeClientBuilder := NewEmptyFakeClientBuilder()
	if len(objects) > 0 {
		fakeClientBuilder.WithObjects(objects...)
	}
	fakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
		Get: GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
	})

	return kubernetesClient.NewClient(fakeClientBuilder.Build())
}

// NewEmptyFakeClientBuilder return fully prepared fake client builder without any default resources or interceptors.
func NewEmptyFakeClientBuilder() *fake.ClientBuilder {
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

	err = searchv1.AddToScheme(s)
	if err != nil {
		return nil
	}

	err = mdbcv1.AddToScheme(s)
	if err != nil {
		return nil
	}

	builder.WithStatusSubresource(&mdbv1.MongoDB{}, &mdbmulti.MongoDBMultiCluster{}, &omv1.MongoDBOpsManager{}, &user.MongoDBUser{}, &searchv1.MongoDBSearch{}, &mdbcv1.MongoDBCommunity{})

	ot := testing.NewObjectTracker(s, scheme.Codecs.UniversalDecoder())
	return builder.WithScheme(s).WithObjectTracker(ot)
}

func GetFakeClientInterceptorGetFunc(omConnectionFactory *om.CachedOMConnectionFactory, markStsAsReady bool, addOMHosts bool) func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}

		switch v := obj.(type) {
		case *appsv1.StatefulSet:
			if markStsAsReady && omConnectionFactory != nil {
				markStatefulSetsReady(v, addOMHosts, omConnectionFactory.GetConnectionForResource(v))
			}
		}

		return nil
	}
}

func MarkAllStatefulSetsAsReady(ctx context.Context, namespace string, clients ...client.Client) error {
	var updatedStsList []string
	for _, c := range clients {
		stsList := appsv1.StatefulSetList{}
		if err := c.List(ctx, &stsList, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("error listing statefulsets in namespace %s in client %+v", namespace, c)
		}

		for _, sts := range stsList.Items {
			updatedSts := sts
			updatedSts.Status.UpdatedReplicas = *sts.Spec.Replicas
			updatedSts.Status.ReadyReplicas = *sts.Spec.Replicas
			updatedSts.Status.Replicas = *sts.Spec.Replicas
			if err := c.Status().Patch(ctx, &updatedSts, client.MergeFrom(&sts)); err != nil {
				return fmt.Errorf("error updating sts %s/%s in client %+v", sts.Namespace, sts.Name, c)
			}
			updatedStsList = append(updatedStsList, sts.Name)
		}
	}

	zap.S().Debugf("marked fake statefulsets as ready: %+v", updatedStsList)

	return nil
}

func GetDefaultResources() []client.Object {
	return []client.Object{
		GetProjectConfigMap(TestProjectConfigMapName, om.TestGroupName, ""),
		GetCredentialsSecret(om.TestUser, om.TestApiKey),
	}
}

func GetProjectConfigMap(configMapName string, projectName string, organizationId string) *corev1.ConfigMap {
	cm := configmap.Builder().
		SetName(configMapName).
		SetNamespace(TestNamespace).
		SetDataField(util.OmBaseUrl, "http://mycompany.example.com:8080").
		SetDataField(util.OmProjectName, projectName).
		SetDataField(util.OmOrgId, organizationId).
		Build()
	return &cm
}

func GetCredentialsSecret(publicKey string, privateKey string) *corev1.Secret {
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
	return credentials
}

func ApproveAllCSRs(ctx context.Context, m client.Client) {
	for _, csrObject := range GetMapForObject(m, &certsv1.CertificateSigningRequest{}) {
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

func CreateOrUpdate(ctx context.Context, m client.Client, obj client.Object) error {
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
			return fmt.Errorf("failed to get object: %w", err)
		}

		err = m.Create(ctx, obj)
		if err != nil {
			return fmt.Errorf("failed to create object: %w", err)
		}
		return nil
	}

	err = m.Update(ctx, obj)
	if err != nil {
		return fmt.Errorf("failed to update object: %w", err)
	}

	return nil
}

func markStatefulSetsReady(set *appsv1.StatefulSet, addOMHosts bool, omConn om.Connection) {
	set.Status.UpdatedReplicas = *set.Spec.Replicas
	set.Status.ReadyReplicas = *set.Spec.Replicas
	set.Status.Replicas = *set.Spec.Replicas

	if addOMHosts {
		if mockedOMConnection, ok := omConn.(*om.MockedOmConnection); ok {
			// For tests with external domains we set hostnames externally in test,
			// as we don't have ExternalAccessConfiguration object in stateful set.
			// For tests that don't set hostnames we preserve old behaviour.
			hostnames := mockedOMConnection.Hostnames
			if hostnames == nil {
				if val, ok := set.Annotations[handler.MongoDBMultiResourceAnnotation]; ok {
					hostnames = dns.GetMultiClusterProcessHostnames(val, set.Namespace, multicluster.MustGetClusterNumFromMultiStsName(set.Name), int(*set.Spec.Replicas), "cluster.local", nil)
				} else {
					// We also "register" automation agents.
					// So far we don't support custom cluster name
					hostnames, _ = dns.GetDnsForStatefulSet(*set, "", nil)
				}
			}
			mockedOMConnection.AddHosts(hostnames)
		}
	}
}

func GetMapForObject(m client.Client, obj apiruntime.Object) map[client.ObjectKey]apiruntime.Object {
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

func ObjectKeyFromApiObject(obj interface{}) client.ObjectKey {
	ns := reflect.ValueOf(obj).Elem().FieldByName("Namespace").String()
	name := reflect.ValueOf(obj).Elem().FieldByName("Name").String()

	return types.NamespacedName{Name: name, Namespace: ns}
}
