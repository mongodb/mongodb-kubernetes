package operator

import (
	"context"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstringsecret"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
)

// clusterConnectionStringSecretSuffix is appended to the MongoDB resource name to form the name of the
// credential-less connection string secret: "<mdb-name>-cluster-connection-string".
const clusterConnectionStringSecretSuffix = "-cluster-connection-string"

// clusterConnectionStringSecretName returns the name of the credential-less connection string secret.
func clusterConnectionStringSecretName(mdb *mdbv1.MongoDB) string {
	return mdb.Name + clusterConnectionStringSecretSuffix
}

// publishConnectionStringSecret creates or updates the credential-less connection string secret
// of the given MongoDB resource using the supplied hostname list (host:port entries). The caller
// computes the hostname list for the topology, the pod DNS names plus any spec.externalMembers
// entries that should appear in the URI.
func publishConnectionStringSecret(ctx context.Context, c secret.GetUpdateCreator, mdb *mdbv1.MongoDB, hostnames []string) error {
	standard, srv := mdb.ConnectionOptionsWithHostnames(hostnames).BuildStandardAndSRV()

	return connectionstringsecret.Publish(ctx, c, connectionstringsecret.Secret{
		Name:            clusterConnectionStringSecretName(mdb),
		Namespace:       mdb.Namespace,
		OwnerReferences: kube.BaseOwnerReference(mdb),
		Fields: map[string]string{
			connectionstringsecret.StandardURIField:    standard,
			connectionstringsecret.StandardSrvURIField: srv,
		},
	})
}
