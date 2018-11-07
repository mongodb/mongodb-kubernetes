package v1

import (
	"reflect"

	crd "github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/crd"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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
var SchemeGroupVersion = schema.GroupVersion{Group: "mongodb.com", Version: "v1"}

var MongoDbStandaloneResource = crd.CustomResource{
	Name:       "mongodbstandalone",
	Plural:     "mongodbstandalones",
	ShortName:  "mst",
	Group:      "mongodb.com",
	Version:    "v1",
	Scope:      apiextensionsv1beta1.NamespaceScoped,
	Kind:       reflect.TypeOf(MongoDbStandalone{}).Name(),
	Validation: createStandaloneValidation(),
}

var MongoDbReplicaSetResource = crd.CustomResource{
	Name:       "mongodbreplicaset",
	Plural:     "mongodbreplicasets",
	ShortName:  "mrs",
	Group:      "mongodb.com",
	Version:    "v1",
	Scope:      apiextensionsv1beta1.NamespaceScoped,
	Kind:       reflect.TypeOf(MongoDbReplicaSet{}).Name(),
	Validation: createReplicaSetValidation(),
}

var MongoDbShardedClusterResource = crd.CustomResource{
	Name:       "mongodbshardedcluster",
	Plural:     "mongodbshardedclusters",
	ShortName:  "msc",
	Group:      "mongodb.com",
	Version:    "v1",
	Scope:      apiextensionsv1beta1.NamespaceScoped,
	Kind:       reflect.TypeOf(MongoDbShardedCluster{}).Name(),
	Validation: createShardedClusterValidation(),
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

func createStandaloneValidation() *apiextensionsv1beta1.JSONSchemaProps {
	return specValidation(commonRequiredProperties(), commonPropertiesValidation())
}

func createReplicaSetValidation() *apiextensionsv1beta1.JSONSchemaProps {
	required := append(commonRequiredProperties(), "members")
	validation := commonPropertiesValidation()
	validation["members"] = replicaSetMembersValidation()

	return specValidation(required, validation)
}

func createShardedClusterValidation() *apiextensionsv1beta1.JSONSchemaProps {
	required := append(commonRequiredProperties(), "shardCount", "mongodsPerShardCount", "mongosCount", "configServerCount")
	validation := commonPropertiesValidation()
	validation["shardCount"] = unboundedMembersValidation()
	validation["mongosCount"] = unboundedMembersValidation()
	validation["mongodsPerShardCount"] = replicaSetMembersValidation()
	validation["configServerCount"] = replicaSetMembersValidation()

	return specValidation(required, validation)
}

func unboundedMembersValidation() apiextensionsv1beta1.JSONSchemaProps {
	return apiextensionsv1beta1.JSONSchemaProps{
		Type:    "integer",
		Minimum: util.Float64Ref(1),
	}
}
func replicaSetMembersValidation() apiextensionsv1beta1.JSONSchemaProps {
	return apiextensionsv1beta1.JSONSchemaProps{
		Type:    "integer",
		Minimum: util.Float64Ref(1),
		Maximum: util.Float64Ref(50),
	}
}

func commonPropertiesValidation() map[string]apiextensionsv1beta1.JSONSchemaProps {
	return map[string]apiextensionsv1beta1.JSONSchemaProps{
		"spec": {
			Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
				"version":     {Type: "string"},
				"credentials": {Type: "string"},
				"project":     {Type: "string"},
			},
		},
	}
}
func specValidation(required []string, validation map[string]apiextensionsv1beta1.JSONSchemaProps) *apiextensionsv1beta1.JSONSchemaProps {
	return &apiextensionsv1beta1.JSONSchemaProps{
		Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
			"spec": {
				Properties: validation,
				Required:   required,
			},
		},
	}
}
func commonRequiredProperties() []string {
	return []string{"credentials", "project", "version"}
}
