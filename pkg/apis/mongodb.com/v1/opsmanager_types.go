package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	SchemeBuilder.Register(&MongoDBOpsManager{}, &MongoDBOpsManagerList{})
}

//=============== Ops Manager ===========================================

// StatusPart is the logical constant for specific field in status in the MongoDBOpsManager
type StatusPart int

// ExtraParams is the constant for different properties that can be passed to update status method
type ExtraParams int

const (
	AppDb StatusPart = iota
	OpsManager
	Backup

	Status ExtraParams = iota
	BaseUrl
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
type MongoDBOpsManager struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MongoDBOpsManagerSpec   `json:"spec"`
	Status            MongoDBOpsManagerStatus `json:"status"`
}

func (om MongoDBOpsManager) AddValidationToManager(m manager.Manager) error {
	return ctrl.NewWebhookManagedBy(m).For(&om).Complete()
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
	Replicas      int               `json:"replicas"`
	// Deprecated: This has been replaced by the ClusterDomain which should be
	// used instead
	ClusterName   string `json:"clusterName,omitempty"`
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// AdminSecret is the secret for the first admin user to create
	// has the fields: "Username", "Password", "FirstName", "LastName"
	AdminSecret string `json:"adminCredentials,omitempty"`
	AppDB       AppDB  `json:"applicationDatabase"`

	JVMParams []string `json:"jvmParameters,omitempty"`

	// Backup
	Backup *MongoDBOpsManagerBackup `json:"backup,omitempty"`

	// MongoDBOpsManagerExternalConnectivity if sets allows for the creation of a Service for
	// accessing this Ops Manager resource from outside the Kubernetes cluster.
	MongoDBOpsManagerExternalConnectivity *MongoDBOpsManagerServiceDefinition `json:"externalConnectivity,omitempty"`

	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`

	// Configure HTTPS.
	Security MongoDBOpsManagerSecurity `json:"security,omitempty"`
}

type MongoDBOpsManagerSecurity struct {
	TLS struct {
		SecretRef struct {
			Name string `json:"name"`
		} `json:"secretRef"`
	} `json:"tls"`
}

// NewExtraStatusParams is the function to build the extra params container used for updating status field
// StatusPart is a mandatory parameter as it's necessary to know which part of status needs to be updated
func NewExtraStatusParams(status StatusPart) map[ExtraParams]interface{} {
	return map[ExtraParams]interface{}{Status: status}
}

func (ms MongoDBOpsManagerSpec) GetClusterDomain() string {
	if ms.ClusterDomain != "" {
		return ms.ClusterDomain
	}
	if ms.ClusterName != "" {
		return ms.ClusterName
	}
	return "cluster.local"
}

// MongoDBOpsManagerServiceDefinition struct that defines the mechanism by which this Ops Manager resource
// is exposed, via a Service, to the outside of the Kubernetes Cluster.
type MongoDBOpsManagerServiceDefinition struct {
	// Type of the `Service` to be created.
	Type corev1.ServiceType `json:"type,omitempty"`

	// Port in which this `Service` will listen to, this applies to `NodePort`.
	Port int32 `json:"port,omitempty"`

	// LoadBalancerIP IP that will be assigned to this LoadBalancer.
	LoadBalancerIP string `json:"loadBalancerIP,omitempty"`

	// ExternalTrafficPolicy mechanism to preserve the client source IP.
	// Only supported on GCE and Google Kubernetes Engine.
	ExternalTrafficPolicy corev1.ServiceExternalTrafficPolicyType `json:"externalTrafficPolicy,omitempty"`

	// Annotations is a list of annotations to be directly passed to the Service object.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// MongoDBOpsManagerBackup backup structure for Ops Manager resources
type MongoDBOpsManagerBackup struct {
	// Enabled indicates if Backups will be enabled for this Ops Manager.
	Enabled bool `json:"enabled"`

	// HeadDB specifies configuration options for the HeadDB
	HeadDB    *PersistenceConfig `json:"headDB,omitempty"`
	JVMParams []string           `json:"jvmParameters,omitempty"`

	// OplogStoreConfigs describes the list of oplog store configs used for backup
	OplogStoreConfigs []DataStoreConfig `json:"oplogStores,omitempty"`
	BlockStoreConfigs []DataStoreConfig `json:"blockStores,omitempty"`
	S3Configs         []S3Config        `json:"s3Stores,omitempty"`

	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`
}

type MongoDBOpsManagerStatus struct {
	OpsManagerStatus OpsManagerStatus `json:"opsManager,omitempty"`
	AppDbStatus      AppDbStatus      `json:"applicationDatabase,omitempty"`
	BackupStatus     BackupStatus     `json:"backup,omitempty"`
	Warnings         []StatusWarning  `json:"warnings,omitempty"`
}

type OpsManagerStatus struct {
	Version        string `json:"version,omitempty"`
	Replicas       int    `json:"replicas,omitempty"`
	Phase          Phase  `json:"phase"`
	Message        string `json:"message,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
	Url            string `json:"url,omitempty"`
}

type AppDbStatus struct {
	MongoDbStatus
}

type BackupStatus struct {
	Phase          Phase  `json:"phase"`
	Message        string `json:"message,omitempty"`
	LastTransition string `json:"lastTransition,omitempty"`
	Version        string `json:"version,omitempty"`
}

// DataStoreConfig is the description of the config used to reference to database. Reused by Oplog and Block stores
// Optionally references the user if the Mongodb is configured with authentication
type DataStoreConfig struct {
	Name               string             `json:"name"`
	MongoDBResourceRef MongoDBResourceRef `json:"mongodbResourceRef"`
	MongoDBUserRef     *MongoDBUserRef    `json:"mongodbUserRef,omitempty"`
}

func (f DataStoreConfig) Identifier() interface{} {
	return f.Name
}

type SecretRef struct {
	Name string `json:"name"`
}

type S3Config struct {
	MongoDBResourceRef     *MongoDBResourceRef `json:"mongodbResourceRef,omitempty"`
	MongoDBUserRef         *MongoDBUserRef     `json:"mongodbUserRef,omitempty"`
	S3SecretRef            SecretRef           `json:"s3SecretRef"`
	Name                   string              `json:"name"`
	PathStyleAccessEnabled bool                `json:"pathStyleAccessEnabled"`
	S3BucketEndpoint       string              `json:"s3BucketEndpoint"`
	S3BucketName           string              `json:"s3BucketName"`
}

func (s S3Config) Identifier() interface{} {
	return s.Name
}

// MongodbResourceObjectKey returns the "name-namespace" object key. Uses the AppDB name if the mongodb resource is not
// specified
func (s S3Config) MongodbResourceObjectKey(opsManager MongoDBOpsManager) client.ObjectKey {
	ns := opsManager.Namespace
	if s.MongoDBResourceRef == nil {
		return client.ObjectKey{}
	}
	if s.MongoDBResourceRef.Namespace != "" {
		ns = s.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: s.MongoDBResourceRef.Name, Namespace: ns}
}

func (s S3Config) MongodbUserObjectKey(defaultNamespace string) client.ObjectKey {
	ns := defaultNamespace
	if s.MongoDBResourceRef == nil {
		return client.ObjectKey{}
	}
	if s.MongoDBResourceRef.Namespace != "" {
		ns = s.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: s.MongoDBUserRef.Name, Namespace: ns}
}

// MongodbResourceObjectKey returns the object key for the mongodb resource referenced by the dataStoreConfig.
// It uses the "parent" object namespace if it is not overriden by 'MongoDBResourceRef.namespace'
func (f DataStoreConfig) MongodbResourceObjectKey(defaultNamespace string) client.ObjectKey {
	ns := defaultNamespace
	if f.MongoDBResourceRef.Namespace != "" {
		ns = f.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: f.MongoDBResourceRef.Name, Namespace: ns}
}

func (f DataStoreConfig) MongodbUserObjectKey(defaultNamespace string) client.ObjectKey {
	ns := defaultNamespace
	if f.MongoDBResourceRef.Namespace != "" {
		ns = f.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: f.MongoDBUserRef.Name, Namespace: ns}
}

type MongoDBUserRef struct {
	Name string `json:"name"`
}

func (m *MongoDBOpsManager) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDBOpsManager
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}
	m.InitDefaultFields()

	return nil
}

func (m *MongoDBOpsManager) InitDefaultFields() {
	// providing backward compatibility for the deployments which didn't specify the 'replicas' before Operator 1.3.1
	// This doesn't update the object in Api server so the real spec won't change
	// All newly created resources will pass through the normal validation so 'replicas' will never be 0
	if m.Spec.Replicas == 0 {
		m.Spec.Replicas = 1
	}

	if m.Spec.Backup == nil {
		m.Spec.Backup = newBackup()
	}

	security := newSecurityWithSCRAM()
	if m.Spec.AppDB.Security == nil {
		m.Spec.AppDB.Security = security
	} else {
		m.Spec.AppDB.Security.Authentication = security.Authentication
	}

	// setting ops manager name, namespace and ClusterDomain for the appdb (transient fields)
	m.Spec.AppDB.opsManagerName = m.Name
	m.Spec.AppDB.namespace = m.Namespace
	m.Spec.AppDB.ClusterDomain = m.Spec.GetClusterDomain()
	m.Spec.AppDB.ResourceType = ReplicaSet
}

func (m *MongoDBOpsManager) MarshalJSON() ([]byte, error) {
	mdb := m.DeepCopyObject().(*MongoDBOpsManager) // prevent mutation of the original object

	mdb.Spec.AppDB.opsManagerName = ""
	mdb.Spec.AppDB.namespace = ""
	mdb.Spec.AppDB.ClusterDomain = ""
	mdb.Spec.AppDB.ResourceType = ""

	if reflect.DeepEqual(m.Spec.Backup, newBackup()) {
		mdb.Spec.Backup = nil
	}

	if reflect.DeepEqual(m.Spec.AppDB.Security, newSecurityWithSCRAM()) {
		mdb.Spec.AppDB.Security = nil
	}

	if reflect.DeepEqual(mdb.Spec.AppDB.PodSpec, newMongoDbPodSpec()) {
		mdb.Spec.AppDB.PodSpec = nil
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

func (m *MongoDBOpsManager) UpdateError(object runtime.Object, msg string, args ...interface{}) {
	reconciledResource := object.(*MongoDBOpsManager)
	m.updateStatus(reconciledResource, PhaseFailed, msg, args...)
}

func (m *MongoDBOpsManager) UpdatePending(object runtime.Object, msg string, args ...interface{}) {
	reconciledResource := object.(*MongoDBOpsManager)
	m.updateStatus(reconciledResource, PhasePending, msg, args...)
}

func (m *MongoDBOpsManager) UpdateReconciling(args ...interface{}) {
	m.updateStatus(nil, PhaseReconciling, "", args...)
}

func (m *MongoDBOpsManager) UpdateSuccessful(object runtime.Object, args ...interface{}) {
	reconciledResource := object.(*MongoDBOpsManager)
	m.updateStatus(reconciledResource, PhaseRunning, "", args...)
}

func (m *MongoDBOpsManager) updateStatus(reconciledOpsManager *MongoDBOpsManager, phase Phase, msg string, args ...interface{}) {
	extraParams := args[0].(map[ExtraParams]interface{})

	switch extraParams[Status].(StatusPart) {
	case AppDb:
		m.updateStatusAppDb(reconciledOpsManager, phase, msg)
	case OpsManager:
		m.updateStatusOpsManager(reconciledOpsManager, phase, msg, extraParams)
	case Backup:
		m.updateStatusBackup(reconciledOpsManager, phase, msg)
	default:
		panic("Not clear which status part must be updated!")
	}
}

func (m *MongoDBOpsManager) updateStatusAppDb(reconciledOpsManager *MongoDBOpsManager, phase Phase, msg string) {
	m.Status.AppDbStatus.LastTransition = util.Now()
	m.Status.AppDbStatus.Phase = phase
	if reconciledOpsManager != nil {
		m.Status.Warnings = reconciledOpsManager.Status.Warnings
	}

	if msg != "" {
		m.Status.AppDbStatus.Message = msg
	}

	if phase == PhaseRunning {
		spec := reconciledOpsManager.Spec.AppDB
		m.Status.AppDbStatus.Version = spec.GetVersion()
		m.Status.AppDbStatus.Message = ""
		m.Status.AppDbStatus.ResourceType = spec.ResourceType
		m.Status.AppDbStatus.Members = spec.Members
	}
}

func (m *MongoDBOpsManager) updateStatusOpsManager(reconciledOpsManager *MongoDBOpsManager, phase Phase, msg string, params map[ExtraParams]interface{}) {
	m.Status.OpsManagerStatus.LastTransition = util.Now()
	m.Status.OpsManagerStatus.Phase = phase
	if reconciledOpsManager != nil {
		m.Status.Warnings = reconciledOpsManager.Status.Warnings
	}

	if msg != "" {
		m.Status.OpsManagerStatus.Message = msg
	}
	if baseUrl, ok := params[BaseUrl]; ok {
		m.Status.OpsManagerStatus.Url = baseUrl.(string)
	}
	if phase == PhaseRunning {
		m.Status.OpsManagerStatus.Replicas = m.Spec.Replicas
		m.Status.OpsManagerStatus.Version = m.Spec.Version
		m.Status.OpsManagerStatus.Message = ""
	}
}

func (m *MongoDBOpsManager) updateStatusBackup(reconciledOpsManager *MongoDBOpsManager, phase Phase, msg string) {
	m.Status.BackupStatus.LastTransition = util.Now()
	m.Status.BackupStatus.Phase = phase
	if reconciledOpsManager != nil {
		m.Status.Warnings = reconciledOpsManager.Status.Warnings
	}

	if msg != "" {
		m.Status.BackupStatus.Message = msg
	}

	if phase == PhaseRunning {
		m.Status.BackupStatus.Message = ""
		m.Status.BackupStatus.Version = m.Spec.Version
	}
}

func (m *MongoDBOpsManager) SetWarnings(warnings []StatusWarning) {
	m.Status.Warnings = warnings
}

func (m *MongoDBOpsManager) GetWarnings() []StatusWarning {
	return m.Status.Warnings
}

func (m *MongoDBOpsManager) AddWarningIfNotExists(warning StatusWarning) {
	m.Status.Warnings = StatusWarnings(m.Status.Warnings).AddIfNotExists(warning)
}

func (m *MongoDBOpsManager) GetKind() string {
	return "MongoDBOpsManager"
}

func (m MongoDBOpsManager) GetPlural() string {
	return "opsmanagers"
}

func (m *MongoDBOpsManager) GetStatus() interface{} {
	return m.Status
}

func (m *MongoDBOpsManager) GetSpec() interface{} {
	// Do not mutate the original object
	omCopy := m.DeepCopy()
	configuration := omCopy.Spec.Configuration
	if uri, ok := configuration[util.MmsMongoUri]; ok {
		configuration[util.MmsMongoUri] = util.RedactMongoURI(uri)
	}
	return omCopy.Spec
}

func (m *MongoDBOpsManager) APIKeySecretName() string {
	return m.Name + "-admin-key"
}

func (m *MongoDBOpsManager) BackupStatefulSetName() string {
	return fmt.Sprintf("%s-backup-daemon", m.GetName())
}

func (m MongoDBOpsManager) GetSchemePort() (corev1.URIScheme, int) {
	if m.Spec.Security.TLS.SecretRef.Name != "" {
		return SchemePortFromAnnotation("https")
	}
	return SchemePortFromAnnotation("http")
}

func (m MongoDBOpsManager) CentralURL() string {
	fqdn := util.GetServiceFQDN(m.SvcName(), m.Namespace, m.Spec.GetClusterDomain())
	scheme, port := m.GetSchemePort()

	// TODO use url.URL to build the url
	return fmt.Sprintf("%s://%s:%d", strings.ToLower(string(scheme)), fqdn, port)
}

func (m MongoDBOpsManager) BackupDaemonHostName() string {
	_, podnames := util.GetDNSNames(m.BackupStatefulSetName(), "", m.Namespace, m.Spec.GetClusterDomain(), 1)
	return podnames[0]
}

// newBackup returns an empty backup object
func newBackup() *MongoDBOpsManagerBackup {
	return &MongoDBOpsManagerBackup{Enabled: true}
}

// ConvertToEnvVarFormat takes a property in the form of
// mms.mail.transport, and converts it into the expected env var format of
// OM_PROP_mms_mail_transport
func ConvertNameToEnvVarFormat(propertyFormat string) string {
	withPrefix := fmt.Sprintf("%s%s", util.OmPropertyPrefix, propertyFormat)
	return strings.Replace(withPrefix, ".", "_", -1)
}

// OpsManagerPodSpecDefaultValues specifies default values for PodSpec for Ops Manager
// 5G is the default pod memory size (OM binary requires by default Xmx = 4.2+ G)
func OpsManagerPodSpecDefaultValues() MongoDbPodSpec {
	return NewEmptyPodSpecWrapperBuilder().
		SetMemory(util.DefaultMemoryOpsManager).
		SetPodAntiAffinityTopologyKey(util.DefaultAntiAffinityTopologyKey).
		Build().
		MongoDbPodSpec
}

func SchemePortFromAnnotation(annotation string) (corev1.URIScheme, int) {
	scheme := corev1.URISchemeHTTP
	port := util.OpsManagerDefaultPortHTTP
	if strings.ToUpper(annotation) == "HTTPS" {
		scheme = corev1.URISchemeHTTPS
		port = util.OpsManagerDefaultPortHTTPS
	}

	return scheme, port
}

//=============== AppDB ===========================================

// Note, that as of beta the AppDB has a limited schema comparing with a MongoDB struct

type AppDB struct {
	MongoDbSpec

	// PasswordSecretKeyRef contains a reference to the secret which contains the password
	// for the mongodb-ops-manager SCRAM-SHA user
	PasswordSecretKeyRef *SecretKeyRef `json:"passwordSecretKeyRef,omitempty"`

	// transient fields. These fields are cleaned before serialization, see 'MarshalJSON()'
	// note, that we cannot include the 'OpsManager' instance here as this creates circular dependency and problems with
	// 'DeepCopy'
	opsManagerName string
	namespace      string
}

type AppDbBuilder struct {
	appDb *AppDB
}

func DefaultAppDbBuilder() *AppDbBuilder {
	appDb := &AppDB{
		MongoDbSpec:          MongoDbSpec{Version: "", Members: 3, PodSpec: &MongoDbPodSpec{}},
		PasswordSecretKeyRef: &SecretKeyRef{},
	}
	return &AppDbBuilder{appDb: appDb}
}

func (b *AppDbBuilder) Build() *AppDB {
	return b.appDb.DeepCopy()
}

func (m AppDB) GetSecretName() string {
	return m.Name() + "-password"
}

func (m *AppDB) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *AppDB
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}

	// if a reference is specified without a key, we will default to "password"
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Key == "" {
		m.PasswordSecretKeyRef.Key = util.DefaultAppDbPasswordKey
	}

	// No AdditionalMongodConfig as of beta
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

func (m AppDB) Name() string {
	return m.opsManagerName + "-db"
}

func (m AppDB) ServiceName() string {
	if m.Service == "" {
		return m.Name() + "-svc"
	}
	return m.Service
}

func (m AppDB) AutomationConfigSecretName() string {
	return m.Name() + "-config"
}

// ConnectionURL returns the connection url to the AppDB
func (m AppDB) ConnectionURL(userName, password string, connectionParams map[string]string) string {
	return buildConnectionUrl(m.Name(), m.ServiceName(), m.namespace, userName, password, m.MongoDbSpec, connectionParams)
}

// todo these two methods are added only to make AppDB implement runtime.Object
func (m *AppDB) GetObjectKind() schema.ObjectKind {
	return nil
}
func (m *AppDB) DeepCopyObject() runtime.Object {
	return nil
}
