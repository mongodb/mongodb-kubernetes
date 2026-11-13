package user

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBUser{}, &MongoDBUserList{})
}

// The MongoDBUser resource allows you to create, deletion and configure
// users for your MongoDB deployments

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:shortName=mdbu
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The current state of the MongoDB User."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDB User resource was created."
type MongoDBUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status MongoDBUserStatus `json:"status"`
	Spec   MongoDBUserSpec   `json:"spec"`
}

func (u *MongoDBUser) GetCommonStatus(options ...status.Option) *status.Common {
	return &u.Status.Common
}

// GetPassword returns the password of the user as stored in the referenced
// secret. If the password secret reference is unset then a blank password and
// a nil error will be returned.
func (u MongoDBUser) GetPassword(ctx context.Context, secretClient secrets.SecretClient) (string, error) {
	if u.Spec.PasswordSecretKeyRef.Name == "" {
		return "", nil
	}

	nsName := client.ObjectKey{
		Namespace: u.Namespace,
		Name:      u.Spec.PasswordSecretKeyRef.Name,
	}
	var databaseSecretPath string
	if vault.IsVaultSecretBackend() {
		databaseSecretPath = secretClient.VaultClient.DatabaseSecretPath()
	}
	secretData, err := secretClient.ReadSecret(ctx, nsName, databaseSecretPath)
	if err != nil {
		return "", xerrors.Errorf("could not retrieve user password secret: %w", err)
	}

	passwordBytes, passwordIsSet := secretData[u.Spec.PasswordSecretKeyRef.Key]
	if !passwordIsSet {
		return "", xerrors.Errorf("passwordSecretKeyRef.key is not set in password secret %v", nsName)
	}

	return passwordBytes, nil
}

// SecretKeyRef is a reference to a value in a given secret in the same
// namespace. Based on:
// https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.15/#secretkeyselector-v1-core
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key,omitempty"`
}

type MongoDBResourceRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.clientCertificate) || (has(self.username) && size(self.username) > 0)",message="spec.username is required unless spec.clientCertificate is set"
// +kubebuilder:validation:XValidation:rule="!has(self.clientCertificate) || !has(self.username) || size(self.username) == 0",message="spec.username and spec.clientCertificate are mutually exclusive: the username of an x509 user is the certificate subject, which the operator derives"
// +kubebuilder:validation:XValidation:rule="!has(self.clientCertificate) || self.db == '$external'",message="spec.clientCertificate requires spec.db to be $external: the user authenticates with its x509 certificate"
// +kubebuilder:validation:XValidation:rule="!has(self.clientCertificate) || !has(self.passwordSecretKeyRef) || !has(self.passwordSecretKeyRef.name) || size(self.passwordSecretKeyRef.name) == 0",message="spec.clientCertificate and spec.passwordSecretKeyRef are mutually exclusive: the user authenticates with its x509 certificate"
type MongoDBUserSpec struct {
	Roles []Role `json:"roles,omitempty"`
	// Username is the name of the user. Mutually exclusive with clientCertificate: the username of an
	// x509 user is the subject of its certificate, for example CN=my-app,OU=my-namespace,O=my-rs-app-client,
	// which the operator derives and reports in status.username.
	// +optional
	Username string `json:"username,omitempty"`
	Database string `json:"db"`
	// ConnectionStringDatabase is an optional database name for the connection string URI path
	// (e.g. .../myapp?...). When unset, the URI path is omitted and which database is used
	// depends on the connecting client.
	// +optional
	ConnectionStringDatabase string `json:"connectionStringDatabase,omitempty"`
	// +optional
	MongoDBResourceRef MongoDBResourceRef `json:"mongodbResourceRef"`
	// +optional
	PasswordSecretKeyRef SecretKeyRef `json:"passwordSecretKeyRef"`
	// +optional
	ConnectionStringSecretName string `json:"connectionStringSecretName"`
	// ClientCertificate requests the operator to issue an x509 client certificate for this user, for an
	// application to connect with. The referenced MongoDB resource must use operator-managed
	// certificates (spec.security.managedCertificate) and x509 authentication, and must be in the
	// same namespace as this user. The field's presence turns the request on, clientCertificate: {} uses
	// the defaults. The username is the minted certificate's subject, which the operator derives and reports
	// in status.username.
	// +optional
	ClientCertificate *ClientCertificate `json:"clientCertificate,omitempty"`
}

// ClientCertificate configures the x509 client certificate the operator issues for a user.
type ClientCertificate struct {
	// Subject of the requested certificate. The username is derived from it. Defaults:
	// commonName is the MongoDBUser name, organizations is <resource>-app-client and
	// organizationalUnits is the namespace. It must differ from the mongodb deployment's membership
	// subject in organizations or organizationalUnits, or mongod treats the user as a cluster member.
	// +optional
	Subject *ClientCertificateSubject `json:"subject,omitempty"`
}

// ClientCertificateSubject is the subject of a user's client certificate.
type ClientCertificateSubject struct {
	// +optional
	CommonName string `json:"commonName,omitempty"`
	// +optional
	Organizations []string `json:"organizations,omitempty"`
	// +optional
	OrganizationalUnits []string `json:"organizationalUnits,omitempty"`
}

type MongoDBUserStatus struct {
	status.Common `json:",inline"`
	Roles         []Role           `json:"roles,omitempty"`
	Username      string           `json:"username"`
	Database      string           `json:"db"`
	Project       string           `json:"project"`
	ProjectId     string           `json:"projectId,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
	// ClientCertificate tells an application where to find what it needs to connect, when
	// spec.clientCertificate is set.
	// +optional
	ClientCertificate *ClientCertificateStatus `json:"clientCertificate,omitempty"`
}

type ClientCertificateStatus struct {
	// SecretName is the kubernetes.io/tls Secret with the client certificate and key (tls.crt, tls.key).
	SecretName string `json:"secretName"`
	// ServerCAConfigMapName is the ConfigMap with the CA that verifies the deployment's server
	// certificates (key ca-pem). The operator keeps it up to date.
	ServerCAConfigMapName string `json:"serverCAConfigMapName"`
}

type Role struct {
	RoleName string `json:"name"`
	Database string `json:"db"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBUser `json:"items"`
}

// ChangedIdentifier determines if the user has changed a value that is used in
// uniquely identifying them. Either username or db. This function relies on the status
// of the resource and is required in order to remove the old user before
// adding a new one to avoid leaving stale state in Ops Manger.
func (u *MongoDBUser) ChangedIdentifier() bool {
	if u.Status.Username == "" || u.Status.Database == "" {
		return false
	}
	return u.Status.Username != u.GetUsername() || u.Status.Database != u.Spec.Database
}

// RegisteredUsername returns the name to remove from MongoDB when the user is deleted. For every user
// without spec.clientCertificate it is GetUsername, unchanged. With spec.clientCertificate it is
// status.username, the name recorded when the user was registered, so it is still the right name if
// the subject was edited since. It falls back to GetUsername for a user that was never registered.
func (u *MongoDBUser) RegisteredUsername() string {
	if u.Spec.ClientCertificate != nil && u.Status.Username != "" {
		return u.Status.Username
	}
	return u.GetUsername()
}

// GetUsername returns the name the user is supposed to be registered with in MongoDB. For every user
// without spec.clientCertificate it is spec.username. With spec.clientCertificate it is the subject of
// the user's certificate (see ClientCertificateSubject), because mongod matches an x509 user by the
// subject of the certificate it is presented. RegisteredUsername returns the name to remove for a user
// that already exists.
func (u *MongoDBUser) GetUsername() string {
	if u.Spec.ClientCertificate == nil {
		return u.Spec.Username
	}
	return u.ClientCertificateSubject().String()
}

// ClientCertificateSubject returns the subject of the user's client certificate. It is built from
// the user only, so it is the same wherever it is computed. Defaults: commonName is the user's
// name, organizations is <referenced MongoDB resource>-app-client and organizationalUnits is the
// namespace. spec.clientCertificate.subject overrides each of them.
func (u *MongoDBUser) ClientCertificateSubject() ClientCertificateSubject {
	sub := ClientCertificateSubject{
		CommonName:          u.Name,
		Organizations:       []string{u.Spec.MongoDBResourceRef.Name + "-app-client"},
		OrganizationalUnits: []string{u.Namespace},
	}
	if u.Spec.ClientCertificate == nil || u.Spec.ClientCertificate.Subject == nil {
		return sub
	}
	override := u.Spec.ClientCertificate.Subject
	if override.CommonName != "" {
		sub.CommonName = override.CommonName
	}
	if len(override.Organizations) > 0 {
		sub.Organizations = override.Organizations
	}
	if len(override.OrganizationalUnits) > 0 {
		sub.OrganizationalUnits = override.OrganizationalUnits
	}
	return sub
}

// String renders the subject the way mongod reports it for an x509 user, most specific attribute
// first: CN, then OU, then O. This is the username, so it must match mongod's form exactly.
func (s ClientCertificateSubject) String() string {
	parts := []string{"CN=" + s.CommonName}
	for _, ou := range s.OrganizationalUnits {
		parts = append(parts, "OU="+ou)
	}
	for _, o := range s.Organizations {
		parts = append(parts, "O="+o)
	}
	return strings.Join(parts, ",")
}

// ClientCertificateName returns the name of the cert-manager Certificate for the user's client
// certificate.
func (u *MongoDBUser) ClientCertificateName() string {
	return u.Name + "-client-cert"
}

// ClientCertificateSecretName returns the name of the kubernetes.io/tls Secret the user's client
// certificate is written to (keys tls.crt and tls.key).
func (u *MongoDBUser) ClientCertificateSecretName() string {
	return u.Name + "-cert"
}

func (u *MongoDBUser) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	u.Status.UpdateCommonFields(phase, u.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		u.Status.Warnings = append(u.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}
	if option, exists := status.GetOption(statusOptions, status.ProjectIdOption{}); exists {
		u.Status.ProjectId = option.(status.ProjectIdOption).ProjectId
	}
	// The name the user is registered with in MongoDB: spec.username, or the client certificate
	// subject when spec.clientCertificate is set. The controller passes it once the user is registered.
	if option, exists := status.GetOption(statusOptions, status.UsernameOption{}); exists {
		u.Status.Username = option.(status.UsernameOption).Username
	}

	if phase == status.PhaseRunning {
		u.Status.Phase = status.PhaseUpdated
		u.Status.Roles = u.Spec.Roles
		u.Status.Database = u.Spec.Database
	}
}

func (u MongoDBUser) GetConnectionStringSecretName() string {
	if u.Spec.ConnectionStringSecretName != "" {
		return u.Spec.ConnectionStringSecretName
	}
	var resourceRef string
	if u.Spec.MongoDBResourceRef.Name != "" {
		resourceRef = u.Spec.MongoDBResourceRef.Name + "-"
	}

	database := u.Spec.Database
	if database == "$external" {
		database = strings.TrimPrefix(database, "$")
	}

	return NormalizeName(fmt.Sprintf("%s%s-%s", resourceRef, u.Name, database))
}

// NormalizeName returns a string that conforms to RFC-1123.
func NormalizeName(name string) string {
	return util.NormalizeName(name)
}

func (u *MongoDBUser) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	u.Status.Warnings = warnings
}

func (u *MongoDBUser) GetStatus(...status.Option) any {
	return u.Status
}

func (u MongoDBUser) GetStatusPath(...status.Option) string {
	return "/status"
}
