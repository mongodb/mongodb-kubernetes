package connectionstringsecret

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/authtypes"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
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

// UserSecretName returns the name of the per user connection string secret:
// the override when set, otherwise the generated
// "<resource>-<username>-<database>" name, with the $ prefix of $external
// databases trimmed.
func UserSecretName(override, resourceName, username, database string) string {
	if override != "" {
		return override
	}
	database = strings.TrimPrefix(database, "$")
	if resourceName != "" {
		resourceName += "-"
	}
	return util.NormalizeName(fmt.Sprintf("%s%s-%s", resourceName, username, database))
}

// ValidateOwnership returns an error when an existing connection string
// secret is not owned by the given resource, so it is not silently
// overwritten.
func ValidateOwnership(existing corev1.Secret, owner v1.Object) error {
	existingController := v1.GetControllerOf(&existing)
	if existingController == nil || existingController.UID != owner.GetUID() {
		return xerrors.Errorf("connection string secret %s already exists and is not managed by the operator", existing.Name)
	}
	return nil
}

// secretGetter reads corev1.Secrets, satisfied by the operator Kubernetes
// clients.
type secretGetter interface {
	GetSecret(ctx context.Context, objectKey types.NamespacedName) (corev1.Secret, error)
}

// ValidateExistingOwnership checks the connection string secret at the given
// coordinates, when one exists, for ownership by the given resource. A
// missing secret is not an error.
func ValidateExistingOwnership(ctx context.Context, c secretGetter, name, namespace string, owner v1.Object) error {
	existing, err := c.GetSecret(ctx, types.NamespacedName{Name: name, Namespace: namespace})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return ValidateOwnership(existing, owner)
}

// Publish creates or updates the given connection string secret. The
// repeat-call is a no-op (other than a Get) when the secret content does not
// change.
func Publish(ctx context.Context, c secret.GetUpdateCreator, s Secret) error {
	builder := secret.Builder().
		SetName(s.Name).
		SetNamespace(s.Namespace).
		SetAnnotations(s.Annotations).
		SetOwnerReferences(s.OwnerReferences)
	for field, value := range s.Fields {
		builder = builder.SetField(field, value)
	}

	return secret.CreateOrUpdate(ctx, c, builder.Build())
}

// PublishForUser builds the connection strings for the given user through
// the shared user options and publishes the secret. The password field is
// left out for $external users since they authenticate without one.
func PublishForUser(ctx context.Context, c secret.GetUpdateCreator, options connectionstring.Options, user authtypes.User, password string, s Secret) error {
	options = options.WithUser(user, password)
	standard, srv := options.BuildStandardAndSRV()
	s.Fields = map[string]string{
		StandardURIField:    standard,
		StandardSrvURIField: srv,
		UsernameField:       user.Username,
	}
	if user.Database != constants.ExternalDB {
		s.Fields[PasswordField] = password
	}
	return Publish(ctx, c, s)
}

// PublishForMongoDB creates or updates the credential-less connection
// string secret for the given MongoDB resource using the supplied
// hostname list (host:port entries). The caller is responsible for
// computing the correct hostname list for the topology: k8s pod DNS
// names plus any spec.externalMembers entries that should appear in
// the URI.
func PublishForMongoDB(ctx context.Context, c secret.GetUpdateCreator, mdb *mdbv1.MongoDB, hostnames []string) error {
	standard, srv := mdb.ConnectionOptionsWithHostnames(hostnames).BuildStandardAndSRV()

	return Publish(ctx, c, Secret{
		Name:            SecretName(mdb),
		Namespace:       mdb.Namespace,
		OwnerReferences: kube.BaseOwnerReference(mdb),
		Fields: map[string]string{
			StandardURIField:    standard,
			StandardSrvURIField: srv,
		},
	})
}
