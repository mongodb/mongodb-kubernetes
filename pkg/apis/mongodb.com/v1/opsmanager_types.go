package v1

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func init() {
	SchemeBuilder.Register(&MongoDBOpsManager{}, &MongoDBOpsManagerList{})
}

//=============== Ops Manager ===========================================

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
	Configuration map[string]string `json:"configuration,omitempty"`
	Version       string            `json:"version"`
	ClusterName   string            `json:"clusterName,omitempty"`

	// AdminSecret is the secret for the first admin user to create
	// has the fields: "Username", "Password", "FirstName", "LastName"
	AdminSecret string `json:"adminCredentials,omitempty"`
	AppDB       AppDB  `json:"applicationDatabase"`
}

type MongoDBOpsManagerStatus struct {
	OpsManagerStatus OpsManagerStatus `json:"opsManager,omitempty"`
	AppDbStatus      AppDbStatus      `json:"applicationDatabase,omitempty"`
}

type OpsManagerStatus struct {
	Version        string `json:"version"`
	Phase          Phase  `json:"phase"`
	Message        string `json:"message,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
}

// Everything the same as for MongoDbStatus
type AppDbStatus struct {
	MongoDbStatus
}

func (m *MongoDBOpsManager) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDBOpsManager
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}
	// setting ops manager name for the appdb
	m.Spec.AppDB.OpsManagerName = m.Name
	return nil
}

func (m *MongoDBOpsManager) MarshalJSON() ([]byte, error) {
	mdb := m.DeepCopyObject().(*MongoDBOpsManager) // prevent mutation of the original object

	mdb.Spec.AppDB.OpsManagerName = ""
	return json.Marshal(*mdb)
}

func (m *MongoDBOpsManager) SvcName() string {
	return m.Name + "-svc"
}

func (m *MongoDBOpsManager) AddConfigIfDoesntExist(key, value string) bool {
	if m.Spec.Configuration == nil {
		m.Spec.Configuration = make(map[string]string)
	}
	if _, ok := m.Spec.Configuration[key]; !ok {
		m.Spec.Configuration[key] = value
		return true
	}
	return false
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

func (m *MongoDBOpsManager) GetKind() string {
	return "MongoDBOpsManager"
}

func (m *MongoDBOpsManager) GetStatus() interface{} {
	return m.Status
}

func (m *MongoDBOpsManager) GetSpec() interface{} {
	return m.Spec
}

func (m *MongoDBOpsManager) APIKeySecretName() string {
	return m.Name + "-admin-key"
}

// todo for all methods below - reuse the ones from types.go
func (m *MongoDBOpsManager) UpdateErrorAppDb(msg string) {
	m.Status.AppDbStatus.Message = msg
	m.Status.AppDbStatus.LastTransition = util.Now()
	m.Status.AppDbStatus.Phase = PhaseFailed
}

func (m *MongoDBOpsManager) UpdatePendingAppDb(msg string) {
	if msg != "" {
		m.Status.AppDbStatus.Message = msg
	}
	if m.Status.AppDbStatus.Phase != PhasePending {
		m.Status.AppDbStatus.LastTransition = util.Now()
		m.Status.AppDbStatus.Phase = PhasePending
	}
}

func (m *MongoDBOpsManager) UpdateReconcilingAppDb() {
	m.Status.AppDbStatus.LastTransition = util.Now()
	m.Status.AppDbStatus.Phase = PhaseReconciling
}

func (m *MongoDBOpsManager) UpdateSuccessfulAppDb(object runtime.Object, args ...string) {
	spec := object.(*AppDB)

	// assign all fields common to the different resource types
	if len(args) >= DeploymentLinkIndex {
		m.Status.AppDbStatus.Link = args[DeploymentLinkIndex]
	}
	m.Status.AppDbStatus.Version = spec.Version
	m.Status.AppDbStatus.Message = ""
	m.Status.AppDbStatus.LastTransition = util.Now()
	m.Status.AppDbStatus.Phase = PhaseRunning
	m.Status.AppDbStatus.ResourceType = spec.ResourceType

	switch spec.ResourceType {
	case ReplicaSet:
		m.Status.AppDbStatus.Members = spec.Members
	}
}

// ConvertToEnvVarFormat takes a property in the form of
// mms.mail.transport, and converts it into the expected env var format of
// OM_PROP_mms_mail_transport
func ConvertNameToEnvVarFormat(propertyFormat string) string {
	withPrefix := fmt.Sprintf("%s%s", util.OmPropertyPrefix, propertyFormat)
	return strings.Replace(withPrefix, ".", "_", -1)
}

//=============== AppDB ===========================================

// Note, that as of alpha the AppDB has a limited schema comparing with a MongoDB struct

type AppDB struct {
	MongoDbSpec

	// transient field. This field is cleaned before serialization, see 'MarshalJSON()'
	// note, that we cannot include the 'OpsManager' instance here as this creates circular dependency and problems with
	// 'DeepCopy'
	OpsManagerName string `json:"omName,omitempty"`
}

// No Security and no AdditionalMongodConfig as of alpha
func (m *AppDB) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *AppDB
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}
	m.Security = nil
	m.AdditionalMongodConfig = nil
	m.ConnectionSpec.Credentials = ""
	m.ConnectionSpec.Project = ""
	// all resources have a pod spec
	if m.PodSpec == nil {
		m.PodSpec = newMongoDbPodSpec()
	}
	return nil
}

func (m *AppDB) Name() string {
	return m.OpsManagerName + "-db"
}

func (m *AppDB) ServiceName() string {
	if m.Service == "" {
		return m.Name() + "-svc"
	}
	return m.Service
}

func (m *AppDB) MongosRsName() string {
	return m.Name() + "-mongos"
}

// todo these two methods are added only to make AppDB implement runtime.Object
func (m *AppDB) GetObjectKind() schema.ObjectKind {
	return nil
}
func (m *AppDB) DeepCopyObject() runtime.Object {
	return nil
}
