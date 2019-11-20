package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	Replicas      int               `json:"replicas"`

	// AdminSecret is the secret for the first admin user to create
	// has the fields: "Username", "Password", "FirstName", "LastName"
	AdminSecret string `json:"adminCredentials,omitempty"`
	AppDB       AppDB  `json:"applicationDatabase"`

	// Backup
	Backup *MongoDBOpsManagerBackup `json:"backup,omitempty"`
}

// MongoDBOpsManagerBackup backup structure for Ops Manager resources
type MongoDBOpsManagerBackup struct {
	// Enabled indicates if Backups will be enabled for this Ops Manager.
	Enabled bool `json:"enabled,omitempty"`

	// HeadDB specifies configuration options for the HeadDB
	HeadDB *PersistenceConfig `json:"headDB,omitempty"`
}

type MongoDBOpsManagerStatus struct {
	OpsManagerStatus OpsManagerStatus `json:"opsManager,omitempty"`
	AppDbStatus      AppDbStatus      `json:"applicationDatabase,omitempty"`
	Warnings         []StatusWarning  `json:"warnings,omitempty"`
}

type OpsManagerStatus struct {
	Version        string `json:"version"`
	Replicas       int    `json:"replicas,omitempty"`
	Phase          Phase  `json:"phase"`
	Message        string `json:"message,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
	Url            string `json:"url,omitempty"`
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

	// providing backward compatibility for the deployments which didn't specify the 'replicas' before Operator 1.3.1
	// This doesn't update the object in Api server so the real spec won't change
	// All newly created resources will pass through the normal validation so 'replicas' will never be 0
	if m.Spec.Replicas == 0 {
		m.Spec.Replicas = 1
	}

	if m.Spec.Backup == nil {
		m.Spec.Backup = newBackup()
		// by default backup is enabled
		m.Spec.Backup.Enabled = true
	}
	return nil
}

func (m *MongoDBOpsManager) MarshalJSON() ([]byte, error) {
	mdb := m.DeepCopyObject().(*MongoDBOpsManager) // prevent mutation of the original object

	mdb.Spec.AppDB.OpsManagerName = ""

	if reflect.DeepEqual(m.Spec.Backup, newBackup()) {
		mdb.Spec.Backup = nil
	}
	return json.Marshal(*mdb)
}

func (m *MongoDBOpsManager) SvcName() string {
	return m.Name + "-svc"
}

func (m *MongoDBOpsManager) BackupSvcName() string {
	return m.BackupStatefulSetName() + "-svc"
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

	if len(args) > 0 {
		m.Status.OpsManagerStatus.Url = args[0]
	}

	m.Status.OpsManagerStatus.Replicas = spec.Replicas
	m.Status.OpsManagerStatus.Version = spec.Version
	m.Status.OpsManagerStatus.Message = ""
	m.Status.OpsManagerStatus.LastTransition = util.Now()
	m.Status.OpsManagerStatus.Phase = PhaseRunning
}

func (m *MongoDBOpsManager) SetWarnings(warnings []StatusWarning) {
	m.Status.Warnings = warnings
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

func (m *MongoDBOpsManager) BackupStatefulSetName() string {
	return fmt.Sprintf("%s-backup-daemon", m.GetName())
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

	m.Status.AppDbStatus.Version = spec.Version
	m.Status.AppDbStatus.Message = ""
	m.Status.AppDbStatus.LastTransition = util.Now()
	m.Status.AppDbStatus.Phase = PhaseRunning
	m.Status.AppDbStatus.ResourceType = spec.ResourceType
	m.Status.AppDbStatus.Members = spec.Members
}

// newBackup returns an empty backup object
func newBackup() *MongoDBOpsManagerBackup {
	return &MongoDBOpsManagerBackup{}
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
	m.ConnectionSpec.CloudManagerConfig = nil
	m.ConnectionSpec.OpsManagerConfig = nil
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

func (m *AppDB) AutomationConfigSecretName() string {
	return m.Name() + "-config"
}

// todo these two methods are added only to make AppDB implement runtime.Object
func (m *AppDB) GetObjectKind() schema.ObjectKind {
	return nil
}
func (m *AppDB) DeepCopyObject() runtime.Object {
	return nil
}
