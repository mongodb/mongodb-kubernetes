package connectionstringsecret

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// SecretNameSuffix is appended to the MongoDB resource name to form the
// secret name: "<mdb-name>-connection-string".
const SecretNameSuffix = "-connection-string"

// SecretName returns the Kubernetes name of the credential-less secret
// for the given MongoDB resource.
func SecretName(mdb *mdbv1.MongoDB) string {
	return mdb.Name + SecretNameSuffix
}

// PublishForMongoDB creates or updates the credential-less connection
// string secret for the given MongoDB resource using the supplied
// hostname list (host:port entries). The caller is responsible for
// computing the correct hostname list for the topology — k8s pod DNS
// names plus any spec.externalMembers entries that should appear in
// the URI. The repeat-call is a no-op (other than a Get) when the
// secret content does not change.
func PublishForMongoDB(ctx context.Context, c client.Client, mdb *mdbv1.MongoDB, hostnames []string) error {
	builder := mdbv1.NewMongoDBConnectionStringBuilder(*mdb, hostnames)
	std := builder.BuildConnectionString("", "", connectionstring.SchemeMongoDB, nil)
	srv := builder.BuildConnectionString("", "", connectionstring.SchemeMongoDBSRV, nil)

	s := secret.Builder().
		SetName(SecretName(mdb)).
		SetNamespace(mdb.Namespace).
		SetField("connectionString.standard", std).
		SetField("connectionString.standardSrv", srv).
		SetOwnerReferences(kube.BaseOwnerReference(mdb)).
		Build()

	return secret.CreateOrUpdate(ctx, kubernetesClient.NewClient(c), s)
}
