package v1alpha1

import (
	"reflect"

	opkit "github.com/rook/operator-kit"
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

var MongoDbReplicaSetResource = opkit.CustomResource{
	Name:    "mongodbreplicaset",
	Plural:  "mongodbreplicasets",
	Group:   "mongodb.com",
	Version: "v1alpha1",
	Scope:   apiextensionsv1beta1.NamespaceScoped,
	Kind:    reflect.TypeOf(MongoDbReplicaSet{}).Name(),
}

var MongoDbStandaloneResource = opkit.CustomResource{
	Name:    "mongodbreplicaset",
	Plural:  "mongodbreplicasets",
	Group:   "mongodb.com",
	Version: "v1alpha1",
	Scope:   apiextensionsv1beta1.NamespaceScoped,
	Kind:    reflect.TypeOf(MongoDbStandalone{}).Name(),
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
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
