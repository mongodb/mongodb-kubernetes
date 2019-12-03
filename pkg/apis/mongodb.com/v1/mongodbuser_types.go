package v1

import (
	"context"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	SchemeBuilder.Register(&MongoDBUser{}, &MongoDBUserList{})
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
type MongoDBUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            MongoDBUserStatus `json:"status"`
	Spec              MongoDBUserSpec   `json:"spec"`
}

// GetPassword returns the password of the user as stored in the referenced
// secret. If the password secret reference is unset then a blank password and
// a nil error will be returned.
func (user MongoDBUser) GetPassword(kubeClient client.Client) (string, error) {
	if user.Spec.PasswordSecretKeyRef.Name == "" {
		return "", nil
	}

	nsName := client.ObjectKey{
		Namespace: user.Namespace,
		Name:      user.Spec.PasswordSecretKeyRef.Name,
	}

	secret := &kubev1.Secret{}
	if err := kubeClient.Get(context.TODO(), nsName, secret); err != nil {
		return "", fmt.Errorf("could not retrieve user password secret: %s", err)
	}

	passwordBytes, passwordIsSet := secret.Data[user.Spec.PasswordSecretKeyRef.Key]
	if !passwordIsSet {
		return "", fmt.Errorf("password is not set in password secret")
	}

	return string(passwordBytes), nil
}

// SecretKeyRef is a reference to a value in a given secret in the same
// namespace. Based on:
// https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.15/#secretkeyselector-v1-core
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type MongoDBResourceRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type MongoDBUserSpec struct {
	Roles                []Role             `json:"roles,omitempty"`
	Username             string             `json:"username"`
	Database             string             `json:"db"`
	MongoDBResourceRef   MongoDBResourceRef `json:"mongodbResourceRef"`
	PasswordSecretKeyRef SecretKeyRef       `json:"passwordSecretKeyRef"`

	// Deprecated: This has been replaced by the MongoDBResourceRef which should
	// be used instead
	Project string `json:"project"`
}

type MongoDBUserStatus struct {
	Roles          []Role          `json:"roles,omitempty"`
	Username       string          `json:"username"`
	Database       string          `json:"db"`
	Message        string          `json:"msg,omitempty"`
	Phase          Phase           `json:"phase"`
	LastTransition string          `json:"lastTransition"`
	Project        string          `json:"project"`
	Warnings       []StatusWarning `json:"warnings,omitempty"`
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

// Changed identifier determines if the user has changed a value that is used in
// uniquely identifying them. Either username or db. This function relies on the status
// of the resource and is required in order to remove the old user before
// adding a new one to avoid leaving stale state in Ops Manger.
func (u *MongoDBUser) ChangedIdentifier() bool {
	if u.Status.Username == "" || u.Status.Database == "" {
		return false
	}
	return u.Status.Username != u.Spec.Username || u.Status.Database != u.Spec.Database
}

func (u *MongoDBUser) UpdateError(msg string) {
	u.Status.Message = msg
	u.Status.LastTransition = util.Now()
	u.Status.Phase = PhaseFailed
}

func (u *MongoDBUser) UpdateSuccessful(other runtime.Object, _ ...string) {
	reconciledUser := other.(*MongoDBUser)
	u.Status.Roles = reconciledUser.Spec.Roles
	u.Status.Database = reconciledUser.Spec.Database
	u.Status.Username = reconciledUser.Spec.Username
	u.Status.Phase = PhaseUpdated
	u.Status.LastTransition = util.Now()
}

func (u *MongoDBUser) UpdatePending(msg string) {
	if msg != "" {
		u.Status.Message = msg
	}
	u.Status.Phase = PhasePending
}

func (u *MongoDBUser) UpdateReconciling() {
	u.Status.Phase = PhaseReconciling
}

func (m *MongoDBUser) SetWarnings(warnings []StatusWarning) {
	m.Status.Warnings = warnings
}

func (m *MongoDBUser) GetKind() string {
	return "MongoDBUser"
}

func (u *MongoDBUser) GetStatus() interface{} {
	return u.Status
}

func (u *MongoDBUser) GetSpec() interface{} {
	return u.Spec
}
