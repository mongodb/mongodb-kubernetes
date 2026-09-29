package connectionstringsecret

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
)

// SecretNameSuffix is appended to the MongoDB resource name to form the
// secret name: "<mdb-name>-cluster-connection-string".
const SecretNameSuffix = "-cluster-connection-string"

// Field names used to store the connection strings in the secret.
const (
	StandardURIField    = "connectionString.standard"
	StandardSrvURIField = "connectionString.standardSrv"
	UsernameField       = "username"
	PasswordField       = "password"
)

// SecretName returns the Kubernetes name of the credential-less secret
// for the given MongoDB resource.
func SecretName(mdb *mdbv1.MongoDB) string {
	return mdb.Name + SecretNameSuffix
}

// Secret describes a connection string secret to create or update.
type Secret struct {
	Name            string
	Namespace       string
	Annotations     map[string]string
	OwnerReferences []v1.OwnerReference
	Fields          map[string]string
}

// Publish creates or updates the given connection string secret. The
// repeat-call is a no-op (other than a Get) when the secret content does not
// change.
func Publish(ctx context.Context, c client.Client, s Secret) error {
	builder := secret.Builder().
		SetName(s.Name).
		SetNamespace(s.Namespace).
		SetAnnotations(s.Annotations).
		SetOwnerReferences(s.OwnerReferences)
	for field, value := range s.Fields {
		builder = builder.SetField(field, value)
	}

	return secret.CreateOrUpdate(ctx, kubernetesClient.NewClient(c), builder.Build())
}

// PublishForMongoDB creates or updates the credential-less connection
// string secret for the given MongoDB resource using the supplied
// hostname list (host:port entries). The caller is responsible for
// computing the correct hostname list for the topology: k8s pod DNS
// names plus any spec.externalMembers entries that should appear in
// the URI.
func PublishForMongoDB(ctx context.Context, c client.Client, mdb *mdbv1.MongoDB, hostnames []string) error {
	builder := mdbv1.NewMongoDBConnectionStringBuilder(*mdb, hostnames)
	std := builder.BuildConnectionString("", "", "", connectionstring.SchemeMongoDB, nil)
	srv := builder.BuildConnectionString("", "", "", connectionstring.SchemeMongoDBSRV, nil)

	return Publish(ctx, c, Secret{
		Name:            SecretName(mdb),
		Namespace:       mdb.Namespace,
		OwnerReferences: kube.BaseOwnerReference(mdb),
		Fields: map[string]string{
			StandardURIField:    std,
			StandardSrvURIField: srv,
		},
	})
}
