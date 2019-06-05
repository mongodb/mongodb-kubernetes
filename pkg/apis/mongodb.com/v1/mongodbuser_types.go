package v1

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

type MongoDBUserSpec struct {
	Roles    []Role `json:"roles,omitempty"`
	Username string `json:"username"`
	Database string `json:"db"`
	Project  string `json:"project"`
}

type MongoDBUserStatus struct {
	Roles          []Role `json:"roles,omitempty"`
	Username       string `json:"username"`
	Database       string `json:"db"`
	Message        string `json:"msg"`
	Phase          Phase  `json:"phase"`
	LastTransition string `json:"lastTransition"`
	Project        string `json:"project"`
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
func (u *MongoDBUser) UpdatePending() {
	u.Status.Phase = PhasePending
}
