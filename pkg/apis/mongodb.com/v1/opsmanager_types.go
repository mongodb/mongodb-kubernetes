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
	Configuration map[string]string `json:"configuration"`
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

// when unmarshaling a MongoDB instance, we don't want to have any nil references
// these are replaced with an empty instance to prevent nil references
func (m *MongoDBOpsManager) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDBOpsManager
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}
	// adding the reference from appdb to om
	m.Spec.AppDB.OpsManager = m
	return nil
}

func (m *MongoDBOpsManager) MarshalJSON() ([]byte, error) {
	mdb := m.DeepCopyObject().(*MongoDBOpsManager) // prevent mutation of the original object
	mdb.Spec.AppDB.OpsManager = nil

	return json.Marshal((MongoDBOpsManager)(*mdb))
}

func (m *MongoDBOpsManager) SvcName() string {
	return "om-svc"
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

func (m *MongoDBOpsManager) GetStatus() interface{} {
	return m.Status
}

func (m *MongoDBOpsManager) GetSpec() interface{} {
	return m.Spec
}

func (m *MongoDBOpsManager) APIKeySecretName() string {
	return m.Name + "-admin-key"
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

	// transient reference to OpsManager
	OpsManager *MongoDBOpsManager
}

// No Security and no AdditionalMongodConfig as of alpha
func (m *AppDB) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *AppDB
	if err := json.Unmarshal(data, (*AppDB)(m)); err != nil {
		return err
	}
	// adding the reference from appdb to om
	m.Security = nil
	m.AdditionalMongodConfig = nil
	return nil
}

func (m *AppDB) Name() string {
	return m.OpsManager.Name + "-db"
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

// todo for all methods below - reuse the ones from types.go
func (m *AppDB) UpdateError(msg string) {
	m.OpsManager.Status.AppDbStatus.Message = msg
	m.OpsManager.Status.AppDbStatus.LastTransition = util.Now()
	m.OpsManager.Status.AppDbStatus.Phase = PhaseFailed
}

// UpdatePending called when the CR object (MongoDB resource) needs to transition to
// pending state.
func (m *AppDB) UpdatePending(msg string) {
	if msg != "" {
		m.OpsManager.Status.AppDbStatus.Message = msg
	}
	if m.OpsManager.Status.AppDbStatus.Phase != PhasePending {
		m.OpsManager.Status.AppDbStatus.LastTransition = util.Now()
		m.OpsManager.Status.AppDbStatus.Phase = PhasePending
	}
}

// UpdateReconciling called when the CR object (MongoDB resource) needs to transition to
// reconciling state.
func (m *AppDB) UpdateReconciling() {
	m.OpsManager.Status.AppDbStatus.LastTransition = util.Now()
	m.OpsManager.Status.AppDbStatus.Phase = PhaseReconciling
}

// UpdateSuccessful called when the CR object (MongoDB resource) needs to transition to
// successful state. This means that the CR object and the underlying MongoDB deployment
// are ready to work
func (m *AppDB) UpdateSuccessful(object runtime.Object, args ...string) {
	reconciledResource := object.(*MongoDB)
	spec := reconciledResource.Spec

	// assign all fields common to the different resource types
	if len(args) >= DeploymentLinkIndex {
		m.OpsManager.Status.AppDbStatus.Link = args[DeploymentLinkIndex]
	}
	m.OpsManager.Status.AppDbStatus.Version = spec.Version
	m.OpsManager.Status.AppDbStatus.Message = ""
	m.OpsManager.Status.AppDbStatus.LastTransition = util.Now()
	m.OpsManager.Status.AppDbStatus.Phase = PhaseRunning
	m.OpsManager.Status.AppDbStatus.ResourceType = spec.ResourceType

	switch spec.ResourceType {
	case ReplicaSet:
		m.OpsManager.Status.AppDbStatus.Members = spec.Members
	case ShardedCluster:
		m.OpsManager.Status.AppDbStatus.MongosCount = spec.MongosCount
		m.OpsManager.Status.AppDbStatus.MongodsPerShardCount = spec.MongodsPerShardCount
		m.OpsManager.Status.AppDbStatus.ConfigServerCount = spec.ConfigServerCount
		m.OpsManager.Status.AppDbStatus.ShardCount = spec.ShardCount
	}
}

func (m *AppDB) GetStatus() interface{} {
	return m.OpsManager.Status.AppDbStatus
}

func (m *AppDB) GetSpec() interface{} {
	return m
}

// todo these two methods are added only to make AppDB implement Updatable
func (m *AppDB) GetObjectKind() schema.ObjectKind {
	return nil
}
func (m *AppDB) DeepCopyObject() runtime.Object {
	return nil
}
