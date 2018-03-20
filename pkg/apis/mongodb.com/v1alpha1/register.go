package v1alpha1

import (
	"reflect"

	crd "github.com/10gen/ops-manager-kubernetes/operator/crd"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	localSchemeBuilder = &SchemeBuilder
	AddToScheme        = SchemeBuilder.AddToScheme
)

// schemeGroupVersion is group version used to register these objects
var SchemeGroupVersion = schema.GroupVersion{Group: "mongodb.com", Version: "v1alpha1"}

var MongoDbStandaloneResource = crd.CustomResource{
	Name:    "mongodbstandalone",
	Plural:  "mongodbstandalones",
	Group:   "mongodb.com",
	Version: "v1alpha1",
	Scope:   apiextensionsv1beta1.NamespaceScoped,
	Kind:    reflect.TypeOf(MongoDbStandalone{}).Name(),
}

var MongoDbReplicaSetResource = crd.CustomResource{
	Name:    "mongodbreplicaset",
	Plural:  "mongodbreplicasets",
	Group:   "mongodb.com",
	Version: "v1alpha1",
	Scope:   apiextensionsv1beta1.NamespaceScoped,
	Kind:    reflect.TypeOf(MongoDbReplicaSet{}).Name(),
}

var MongoDbShardedClusterResource = crd.CustomResource{
	Name:    "mongodbshardedcluster",
	Plural:  "mongodbshardedclusters",
	Group:   "mongodb.com",
	Version: "v1alpha1",
	Scope:   apiextensionsv1beta1.NamespaceScoped,
	Kind:    reflect.TypeOf(MongoDbShardedCluster{}).Name(),
}

func init() {
	// We only register manually written functions here. The registration of the
	// generated functions takes place in the generated files. The separation
	// makes the code compile even when the generated files are missing.
	localSchemeBuilder.Register(addKnownTypes)
}

// Resource takes an unqualified resource and returns back a Group qualified GroupResource
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// Adds the list of known types to api.Scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&MongoDbReplicaSet{},
		&MongoDbReplicaSetList{},
		&MongoDbStandalone{},
		&MongoDbStandaloneList{},
		&MongoDbShardedCluster{},
		&MongoDbShardedClusterList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
