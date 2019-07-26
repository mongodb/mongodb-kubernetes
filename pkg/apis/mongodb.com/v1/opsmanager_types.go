package v1

import (
	"fmt"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(&MongoDBOpsManager{}, &MongoDBOpsManagerList{})
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
type MongoDBOpsManager struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MongoDBOpsManagerSpec   `json:"spec"`
	Status            MongoDBOpsManagerStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBOpsManagerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBOpsManager `json:"items"`
}

type MongoDBOpsManagerSpec struct {
	Configuration map[string]string `json:"configuration"`
	Version       string            `json:"version"`
	ClusterName   string            `json:"clusterName,omitempty"`
}

type MongoDBOpsManagerStatus struct {
	OpsManagerStatus OpsManagerStatus `json:"opsManager,omitempty"`
}

type OpsManagerStatus struct {
	Version        string `json:"version"`
	Phase          Phase  `json:"phase"`
	Message        string `json:"message,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
}

func (m MongoDBOpsManager) SvcName() string {
	return "om-svc"
}

func (m MongoDBOpsManager) AddConfigIfDoesntExist(key, value string) {
	if _, ok := m.Spec.Configuration[key]; !ok {
		m.Spec.Configuration[key] = value
	}
}

func (m *MongoDBOpsManager) UpdateError(msg string) {
	m.Status.OpsManagerStatus.Message = msg
	m.Status.OpsManagerStatus.LastTransition = util.Now()
	m.Status.OpsManagerStatus.Phase = PhaseFailed
}

func (m *MongoDBOpsManager) UpdatePending(msg string) {
	if msg != "" {
		m.Status.OpsManagerStatus.Message = msg
	}
	if m.Status.OpsManagerStatus.Phase != PhasePending {
		m.Status.OpsManagerStatus.LastTransition = util.Now()
		m.Status.OpsManagerStatus.Phase = PhasePending
	}
}

func (m *MongoDBOpsManager) UpdateReconciling() {
	m.Status.OpsManagerStatus.LastTransition = util.Now()
	m.Status.OpsManagerStatus.Phase = PhaseReconciling
}

func (m *MongoDBOpsManager) UpdateSuccessful(object runtime.Object, args ...string) {
	reconciledResource := object.(*MongoDBOpsManager)
	spec := reconciledResource.Spec

	m.Status.OpsManagerStatus.Version = spec.Version
	m.Status.OpsManagerStatus.Message = ""
	m.Status.OpsManagerStatus.LastTransition = util.Now()
	m.Status.OpsManagerStatus.Phase = PhaseRunning
}

func (m *MongoDBOpsManager) GetStatus() interface{} {
	return m.Status
}

func (m *MongoDBOpsManager) GetSpec() interface{} {
	return m.Spec
}

// ConvertToEnvVarFormat takes a property in the form of
// mms.mail.transport, and converts it into the expected env var format of
// OM_PROP_mms_mail_transport
func ConvertNameToEnvVarFormat(propertyFormat string) string {
	withPrefix := fmt.Sprintf("%s%s", util.OmPropertyPrefix, propertyFormat)
	return strings.Replace(withPrefix, ".", "_", -1)
}
