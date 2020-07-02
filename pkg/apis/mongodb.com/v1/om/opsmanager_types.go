package om

import (
	"encoding/json"
	"fmt"
	"strings"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	userv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/user"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBOpsManager{}, &MongoDBOpsManagerList{})
}

// The MongoDBOpsManager resource allows you to deploy Ops Manager within your Kubernetes cluster

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="The number of replicas of MongoDBOpsManager."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".spec.version",description="The version of MongoDBOpsManager."
// +kubebuilder:printcolumn:name="State (OpsManager)",type="string",JSONPath=".status.opsManager.phase",description="The current state of the MongoDBOpsManager."
// +kubebuilder:printcolumn:name="State (AppDB)",type="string",JSONPath=".status.applicationDatabase.phase",description="The current state of the MongoDBOpsManager Application Database."
// +kubebuilder:printcolumn:name="State (Backup)",type="string",JSONPath=".status.backup.phase",description="The current state of the MongoDBOpsManager Backup Daemon."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDBOpsManager resource was created."
// +kubebuilder:printcolumn:name="Warnings",type="string",JSONPath=".status.warnings",description="Warnings."
type MongoDBOpsManager struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MongoDBOpsManagerSpec   `json:"spec"`
	Status            MongoDBOpsManagerStatus `json:"status"`
}

func (om MongoDBOpsManager) AddValidationToManager(m manager.Manager) error {
	return ctrl.NewWebhookManagedBy(m).For(&om).Complete()
}

func (om MongoDBOpsManager) GetAppDBProjectConfig() mdbv1.ProjectConfig {
	return mdbv1.ProjectConfig{
		BaseURL:     om.CentralURL(),
		ProjectName: om.Spec.AppDB.Name(),
		Credentials: om.APIKeySecretName(),
	}
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBOpsManagerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBOpsManager `json:"items"`
}

type MongoDBOpsManagerSpec struct {
	// The configuration properties passed to Ops Manager/Backup Daemon
	// +optional
	Configuration map[string]string `json:"configuration,omitempty"`

	Version  string `json:"version"`
	Replicas int    `json:"replicas"`
	// Deprecated: This has been replaced by the ClusterDomain which should be
	// used instead
	ClusterName string `json:"clusterName,omitempty"`
	// +optional
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// AdminSecret is the secret for the first admin user to create
	// has the fields: "Username", "Password", "FirstName", "LastName"
	AdminSecret string `json:"adminCredentials,omitempty"`
	AppDB       AppDB  `json:"applicationDatabase"`

	// Custom JVM parameters passed to the Ops Manager JVM
	// +optional
	JVMParams []string `json:"jvmParameters,omitempty"`

	// Backup
	// +optional
	Backup *MongoDBOpsManagerBackup `json:"backup,omitempty"`

	// MongoDBOpsManagerExternalConnectivity if sets allows for the creation of a Service for
	// accessing this Ops Manager resource from outside the Kubernetes cluster.
	// +optional
	MongoDBOpsManagerExternalConnectivity *MongoDBOpsManagerServiceDefinition `json:"externalConnectivity,omitempty"`

	// +optional
	// Deprecated: This field has been removed, it is only here to perform validations
	PodSpec *mdbv1.MongoDbPodSpec `json:"podSpec,omitempty"`

	// Configure HTTPS.
	// +optional
	Security *MongoDBOpsManagerSecurity `json:"security,omitempty"`

	// Configure custom StatefulSet configuration
	// +optional
	StatefulSetConfiguration *mdbv1.StatefulSetConfiguration `json:"statefulSet,omitempty"`
}

type MongoDBOpsManagerSecurity struct {
	TLS MongoDBOpsManagerTLS `json:"tls"`
}

type MongoDBOpsManagerTLS struct {
	SecretRef mdbv1.TLSSecretRef `json:"secretRef"`
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
	HeadDB    *mdbv1.PersistenceConfig `json:"headDB,omitempty"`
	JVMParams []string                 `json:"jvmParameters,omitempty"`

	// OplogStoreConfigs describes the list of oplog store configs used for backup
	OplogStoreConfigs []DataStoreConfig `json:"oplogStores,omitempty"`
	BlockStoreConfigs []DataStoreConfig `json:"blockStores,omitempty"`
	S3Configs         []S3Config        `json:"s3Stores,omitempty"`
	// Deprecated: this field has been removed, it is only here to perform validations
	PodSpec                  *mdbv1.MongoDbPodSpec           `json:"podSpec,omitempty"`
	StatefulSetConfiguration *mdbv1.StatefulSetConfiguration `json:"statefulSet,omitempty"`
}

type MongoDBOpsManagerStatus struct {
	OpsManagerStatus OpsManagerStatus `json:"opsManager,omitempty"`
	AppDbStatus      AppDbStatus      `json:"applicationDatabase,omitempty"`
	BackupStatus     BackupStatus     `json:"backup,omitempty"`
	Warnings         []status.Warning `json:"warnings,omitempty"`
}

type OpsManagerStatus struct {
	status.Common `json:",inline"`
	Replicas      int    `json:"replicas,omitempty"`
	Version       string `json:"version,omitempty"`
	Url           string `json:"url,omitempty"`
}

type AppDbStatus struct {
	mdbv1.MongoDbStatus `json:",inline"`
}

type BackupStatus struct {
	status.Common `json:",inline"`
	Version       string `json:"version,omitempty"`
}

// DataStoreConfig is the description of the config used to reference to database. Reused by Oplog and Block stores
// Optionally references the user if the Mongodb is configured with authentication
type DataStoreConfig struct {
	Name               string                    `json:"name"`
	MongoDBResourceRef userv1.MongoDBResourceRef `json:"mongodbResourceRef"`
	MongoDBUserRef     *MongoDBUserRef           `json:"mongodbUserRef,omitempty"`
}

func (f DataStoreConfig) Identifier() interface{} {
	return f.Name
}

type SecretRef struct {
	Name string `json:"name"`
}

type S3Config struct {
	MongoDBResourceRef     *userv1.MongoDBResourceRef `json:"mongodbResourceRef,omitempty"`
	MongoDBUserRef         *MongoDBUserRef            `json:"mongodbUserRef,omitempty"`
	S3SecretRef            SecretRef                  `json:"s3SecretRef"`
	Name                   string                     `json:"name"`
	PathStyleAccessEnabled bool                       `json:"pathStyleAccessEnabled"`
	S3BucketEndpoint       string                     `json:"s3BucketEndpoint"`
	S3BucketName           string                     `json:"s3BucketName"`
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

	m.Spec.AppDB.Security = ensureSecurityWithSCRAM(m.Spec.AppDB.Security)

	// setting ops manager name, namespace and ClusterDomain for the appdb (transient fields)
	m.Spec.AppDB.OpsManagerName = m.Name
	m.Spec.AppDB.Namespace = m.Namespace
	m.Spec.AppDB.ClusterDomain = m.Spec.GetClusterDomain()
	m.Spec.AppDB.ResourceType = mdbv1.ReplicaSet
}

func ensureSecurityWithSCRAM(specSecurity *mdbv1.Security) *mdbv1.Security {
	if specSecurity == nil {
		specSecurity = &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{}}
	}
	// the only allowed authentication is SCRAM - it's implicit to the user and hidden from him
	specSecurity.Authentication = &mdbv1.Authentication{Modes: []string{util.SCRAM}}
	return specSecurity
}

func (m *MongoDBOpsManager) SvcName() string {
	return m.Name + "-svc"
}

func (m *MongoDBOpsManager) AppDBMongoConnectionStringSecretName() string {
	return m.Spec.AppDB.Name() + "-connection-string"
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

func (m *MongoDBOpsManager) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	var statusPart status.Part
	if option, exists := status.GetOption(statusOptions, status.OMPartOption{}); exists {
		statusPart = option.(status.OMPartOption).StatusPart
	}

	switch statusPart {
	case status.AppDb:
		m.updateStatusAppDb(phase, statusOptions...)
	case status.OpsManager:
		m.updateStatusOpsManager(phase, statusOptions...)
	case status.Backup:
		m.updateStatusBackup(phase, statusOptions...)
	}

	// It may make sense to keep separate warnings per status part - this needs some refactoring for
	// validation layer though (the one shared with validation webhook)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		m.Status.Warnings = append(m.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}
}

func (m *MongoDBOpsManager) updateStatusAppDb(phase status.Phase, statusOptions ...status.Option) {
	m.Status.AppDbStatus.UpdateCommonFields(phase, statusOptions...)

	if phase == status.PhaseRunning {
		spec := m.Spec.AppDB
		m.Status.AppDbStatus.Version = spec.GetVersion()
		m.Status.AppDbStatus.Message = ""
		m.Status.AppDbStatus.ResourceType = spec.ResourceType
		m.Status.AppDbStatus.Members = spec.Members
	}
}

func (m *MongoDBOpsManager) updateStatusOpsManager(phase status.Phase, statusOptions ...status.Option) {
	m.Status.OpsManagerStatus.UpdateCommonFields(phase, statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.BaseUrlOption{}); exists {
		m.Status.OpsManagerStatus.Url = option.(status.BaseUrlOption).BaseUrl
	}

	if phase == status.PhaseRunning {
		m.Status.OpsManagerStatus.Replicas = m.Spec.Replicas
		m.Status.OpsManagerStatus.Version = m.Spec.Version
		m.Status.OpsManagerStatus.Message = ""
	}
}

func (m *MongoDBOpsManager) updateStatusBackup(phase status.Phase, statusOptions ...status.Option) {
	m.Status.BackupStatus.UpdateCommonFields(phase, statusOptions...)

	if phase == status.PhaseRunning {
		m.Status.BackupStatus.Message = ""
		m.Status.BackupStatus.Version = m.Spec.Version
	}
}

func (m *MongoDBOpsManager) SetWarnings(warnings []status.Warning) {
	m.Status.Warnings = warnings
}

func (m *MongoDBOpsManager) AddWarningIfNotExists(warning status.Warning) {
	m.Status.Warnings = status.Warnings(m.Status.Warnings).AddIfNotExists(warning)
}

func (m MongoDBOpsManager) GetPlural() string {
	return "opsmanagers"
}

func (m *MongoDBOpsManager) GetStatus() interface{} {
	return m.Status
}

func (m *MongoDBOpsManager) APIKeySecretName() string {
	return m.Name + "-admin-key"
}

func (m *MongoDBOpsManager) BackupStatefulSetName() string {
	return fmt.Sprintf("%s-backup-daemon", m.GetName())
}

func (m MongoDBOpsManager) GetSchemePort() (corev1.URIScheme, int) {
	if m.Spec.Security != nil && m.Spec.Security.TLS.SecretRef.Name != "" {
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

func SchemePortFromAnnotation(annotation string) (corev1.URIScheme, int) {
	scheme := corev1.URISchemeHTTP
	port := util.OpsManagerDefaultPortHTTP
	if strings.ToUpper(annotation) == "HTTPS" {
		scheme = corev1.URISchemeHTTPS
		port = util.OpsManagerDefaultPortHTTPS
	}

	return scheme, port
}
