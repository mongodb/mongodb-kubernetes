package user

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
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
		return "", xerrors.Errorf("password is not set in password secret")
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

type MongoDBUserSpec struct {
	Roles    []Role `json:"roles,omitempty"`
	Username string `json:"username"`
	Database string `json:"db"`
	// +optional
	MongoDBResourceRef MongoDBResourceRef `json:"mongodbResourceRef"`
	// +optional
	PasswordSecretKeyRef SecretKeyRef `json:"passwordSecretKeyRef"`
	// +optional
	ConnectionStringSecretName string `json:"connectionStringSecretName"`
}

type MongoDBUserStatus struct {
	status.Common `json:",inline"`
	Roles         []Role           `json:"roles,omitempty"`
	Username      string           `json:"username"`
	Database      string           `json:"db"`
	Project       string           `json:"project"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
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
	return u.Status.Username != u.Spec.Username || u.Status.Database != u.Spec.Database
}

func (u *MongoDBUser) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	u.Status.UpdateCommonFields(phase, u.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		u.Status.Warnings = append(u.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}

	if phase == status.PhaseRunning {
		u.Status.Phase = status.PhaseUpdated
		u.Status.Roles = u.Spec.Roles
		u.Status.Database = u.Spec.Database
		u.Status.Username = u.Spec.Username
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

	return normalizeName(fmt.Sprintf("%s%s-%s", resourceRef, u.Name, database))
}

// normalizeName returns a string that conforms to RFC-1123.
// This logic is duplicated in the community operator in https://github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/blob/master/api/v1/mongodbcommunity_types.go.
// The logic should be reused if/when we unify the user types or observe that the logic needs to be changed for business logic reasons, to avoid modifying it
// in separate places in the future.
func normalizeName(name string) string {
	errors := validation.IsDNS1123Subdomain(name)
	if len(errors) == 0 {
		return name
	}

	// convert name to lowercase and replace invalid characters with '-'
	name = strings.ToLower(name)
	re := regexp.MustCompile("[^a-z0-9-]+")
	name = re.ReplaceAllString(name, "-")

	// Remove duplicate `-` resulting from contiguous non-allowed chars.
	re = regexp.MustCompile(`\-+`)
	name = re.ReplaceAllString(name, "-")

	name = strings.Trim(name, "-")

	if len(name) > validation.DNS1123SubdomainMaxLength {
		name = name[0:validation.DNS1123SubdomainMaxLength]
	}
	return name
}

func (u *MongoDBUser) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	u.Status.Warnings = warnings
}

func (u *MongoDBUser) GetStatus(...status.Option) interface{} {
	return u.Status
}

func (u MongoDBUser) GetStatusPath(...status.Option) string {
	return "/status"
}
