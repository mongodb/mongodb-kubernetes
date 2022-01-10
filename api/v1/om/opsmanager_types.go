package om

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	mdbc "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBOpsManager{}, &MongoDBOpsManagerList{})
}

const (
	queryableBackupConfigPath  string = "brs.queryable.proxyPort"
	queryableBackupDefaultPort int32  = 25999
)

// The MongoDBOpsManager resource allows you to deploy Ops Manager within your Kubernetes cluster

// +k8s:deepcopy-gen=true
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=opsmanagers,scope=Namespaced,shortName=om,singular=opsmanager
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
	Spec              MongoDBOpsManagerSpec `json:"spec"`
	// +optional
	Status MongoDBOpsManagerStatus `json:"status"`
}

func (om *MongoDBOpsManager) ForcedIndividualScaling() bool {
	return false
}

func (om MongoDBOpsManager) AddValidationToManager(m manager.Manager) error {
	return ctrl.NewWebhookManagedBy(m).For(&om).Complete()
}

func (om MongoDBOpsManager) GetAppDBProjectConfig(client secrets.SecretClient) (mdbv1.ProjectConfig, error) {
	var operatorVaultSecretPath string
	if client.VaultClient != nil {
		operatorVaultSecretPath = client.VaultClient.OperatorSecretPath()
	}
	secretName, err := om.APIKeySecretName(client, operatorVaultSecretPath)
	if err != nil {
		return mdbv1.ProjectConfig{}, err
	}

	return mdbv1.ProjectConfig{
		BaseURL:     om.CentralURL(),
		ProjectName: om.Spec.AppDB.Name(),
		Credentials: secretName,
	}, nil
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

	Version string `json:"version"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas int `json:"replicas"`
	// Deprecated: This has been replaced by the ClusterDomain which should be
	// used instead
	// +kubebuilder:validation:Format="hostname"
	ClusterName string `json:"clusterName,omitempty"`
	// +optional
	// +kubebuilder:validation:Format="hostname"
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// AdminSecret is the secret for the first admin user to create
	// has the fields: "Username", "Password", "FirstName", "LastName"
	AdminSecret string    `json:"adminCredentials,omitempty"`
	AppDB       AppDBSpec `json:"applicationDatabase"`

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

	// Configure HTTPS.
	// +optional
	Security *MongoDBOpsManagerSecurity `json:"security,omitempty"`

	// Configure custom StatefulSet configuration
	// +optional
	StatefulSetConfiguration *mdbc.StatefulSetConfiguration `json:"statefulSet,omitempty"`
}

type MongoDBOpsManagerSecurity struct {
	// +optional
	TLS MongoDBOpsManagerTLS `json:"tls"`

	// +optional
	CertificatesSecretsPrefix string `json:"certsSecretPrefix"`
}

type MongoDBOpsManagerTLS struct {
	// +optional
	SecretRef TLSSecretRef `json:"secretRef"`
	// +optional
	CA string `json:"ca"`
}

type TLSSecretRef struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
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

func (ms MongoDBOpsManagerSpec) GetOpsManagerCA() string {
	if ms.Security != nil {
		return ms.Security.TLS.CA
	}
	return ""
}
func (m MongoDBOpsManager) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(m.Namespace, m.Name)
}

func (m MongoDBOpsManager) AppDBStatefulSetObjectKey() client.ObjectKey {
	return kube.ObjectKey(m.Namespace, m.Spec.AppDB.Name())
}

// MongoDBOpsManagerServiceDefinition struct that defines the mechanism by which this Ops Manager resource
// is exposed, via a Service, to the outside of the Kubernetes Cluster.
type MongoDBOpsManagerServiceDefinition struct {
	// Type of the `Service` to be created.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=LoadBalancer;NodePort
	Type corev1.ServiceType `json:"type"`

	// Port in which this `Service` will listen to, this applies to `NodePort`.
	Port int32 `json:"port,omitempty"`

	// LoadBalancerIP IP that will be assigned to this LoadBalancer.
	LoadBalancerIP string `json:"loadBalancerIP,omitempty"`

	// ExternalTrafficPolicy mechanism to preserve the client source IP.
	// Only supported on GCE and Google Kubernetes Engine.
	// +kubebuilder:validation:Enum=Cluster;Local
	ExternalTrafficPolicy corev1.ServiceExternalTrafficPolicyType `json:"externalTrafficPolicy,omitempty"`

	// Annotations is a list of annotations to be directly passed to the Service object.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// MongoDBOpsManagerBackup backup structure for Ops Manager resources
type MongoDBOpsManagerBackup struct {
	// Enabled indicates if Backups will be enabled for this Ops Manager.
	Enabled                bool  `json:"enabled"`
	ExternalServiceEnabled *bool `json:"externalServiceEnabled,omitempty"`

	// Members indicate the number of backup daemon pods to create.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Members int `json:"members,omitempty"`

	// HeadDB specifies configuration options for the HeadDB
	HeadDB    *mdbv1.PersistenceConfig `json:"headDB,omitempty"`
	JVMParams []string                 `json:"jvmParameters,omitempty"`

	// S3OplogStoreConfigs describes the list of s3 oplog store configs used for backup.
	S3OplogStoreConfigs []S3Config `json:"s3OpLogStores,omitempty"`

	// OplogStoreConfigs describes the list of oplog store configs used for backup
	OplogStoreConfigs        []DataStoreConfig              `json:"opLogStores,omitempty"`
	BlockStoreConfigs        []DataStoreConfig              `json:"blockStores,omitempty"`
	S3Configs                []S3Config                     `json:"s3Stores,omitempty"`
	FileSystemStoreConfigs   []FileSystemStoreConfig        `json:"fileSystemStores,omitempty"`
	StatefulSetConfiguration *mdbc.StatefulSetConfiguration `json:"statefulSet,omitempty"`

	// QueryableBackupSecretRef references the secret which contains the pem file which is used
	// for queryable backup. This will be mounted into the Ops Manager pod.
	// +optional
	QueryableBackupSecretRef SecretRef `json:"queryableBackupSecretRef,omitempty"`
}

type MongoDBOpsManagerStatus struct {
	OpsManagerStatus OpsManagerStatus `json:"opsManager,omitempty"`
	AppDbStatus      AppDbStatus      `json:"applicationDatabase,omitempty"`
	BackupStatus     BackupStatus     `json:"backup,omitempty"`
}

type OpsManagerStatus struct {
	status.Common `json:",inline"`
	Replicas      int              `json:"replicas,omitempty"`
	Version       string           `json:"version,omitempty"`
	Url           string           `json:"url,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

type AppDbStatus struct {
	mdbv1.MongoDbStatus `json:",inline"`
}

type BackupStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

type FileSystemStoreConfig struct {
	Name string `json:"name"`
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
	// This is only set to "true" when user is running in EKS and is using AWS IRSA to configure
	// S3 snapshot store. For more details refer this: https://aws.amazon.com/blogs/opensource/introducing-fine-grained-iam-roles-service-accounts/
	// +optional
	IRSAEnabled bool `json:"irsaEnabled"`
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

	if m.Spec.Backup.Members == 0 {
		m.Spec.Backup.Members = 1
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

func (m *MongoDBOpsManager) BackupServiceName() string {
	return m.BackupStatefulSetName() + "-svc"
}

func (ms *MongoDBOpsManagerSpec) BackupSvcPort() (int32, error) {
	if port, ok := ms.Configuration[queryableBackupConfigPath]; ok {
		val, err := strconv.Atoi(port)
		if err != nil {
			return -1, err
		}
		return int32(val), nil
	}
	return queryableBackupDefaultPort, nil
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
}

func (m *MongoDBOpsManager) updateStatusAppDb(phase status.Phase, statusOptions ...status.Option) {
	m.Status.AppDbStatus.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.ReplicaSetMembersOption{}); exists {
		m.Status.AppDbStatus.Members = option.(status.ReplicaSetMembersOption).Members
	}

	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		m.Status.AppDbStatus.Warnings = append(m.Status.AppDbStatus.Warnings, option.(status.WarningsOption).Warnings...)
	}

	if phase == status.PhaseRunning {
		spec := m.Spec.AppDB
		m.Status.AppDbStatus.Version = spec.GetMongoDBVersion()
		m.Status.AppDbStatus.Message = ""
	}
}

func (m *MongoDBOpsManager) updateStatusOpsManager(phase status.Phase, statusOptions ...status.Option) {
	m.Status.OpsManagerStatus.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.BaseUrlOption{}); exists {
		m.Status.OpsManagerStatus.Url = option.(status.BaseUrlOption).BaseUrl
	}

	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		m.Status.OpsManagerStatus.Warnings = append(m.Status.OpsManagerStatus.Warnings, option.(status.WarningsOption).Warnings...)
	}

	if phase == status.PhaseRunning {
		m.Status.OpsManagerStatus.Replicas = m.Spec.Replicas
		m.Status.OpsManagerStatus.Version = m.Spec.Version
		m.Status.OpsManagerStatus.Message = ""
	}
}

func (m *MongoDBOpsManager) updateStatusBackup(phase status.Phase, statusOptions ...status.Option) {
	m.Status.BackupStatus.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		m.Status.BackupStatus.Warnings = append(m.Status.BackupStatus.Warnings, option.(status.WarningsOption).Warnings...)
	}
	if phase == status.PhaseRunning {
		m.Status.BackupStatus.Message = ""
		m.Status.BackupStatus.Version = m.Spec.Version
	}
}

func (m *MongoDBOpsManager) SetWarnings(warnings []status.Warning, options ...status.Option) {
	for _, part := range getPartsFromStatusOptions(options...) {
		switch part {
		case status.OpsManager:
			m.Status.OpsManagerStatus.Warnings = warnings
		case status.Backup:
			m.Status.BackupStatus.Warnings = warnings
		case status.AppDb:
			m.Status.AppDbStatus.Warnings = warnings
		}
	}
}

func (m *MongoDBOpsManager) AddOpsManagerWarningIfNotExists(warning status.Warning) {
	m.Status.OpsManagerStatus.Warnings = status.Warnings(m.Status.OpsManagerStatus.Warnings).AddIfNotExists(warning)
}
func (m *MongoDBOpsManager) AddAppDBWarningIfNotExists(warning status.Warning) {
	m.Status.AppDbStatus.Warnings = status.Warnings(m.Status.AppDbStatus.Warnings).AddIfNotExists(warning)
}
func (m *MongoDBOpsManager) AddBackupWarningIfNotExists(warning status.Warning) {
	m.Status.BackupStatus.Warnings = status.Warnings(m.Status.BackupStatus.Warnings).AddIfNotExists(warning)
}

func (m MongoDBOpsManager) GetPlural() string {
	return "opsmanagers"
}

func (m *MongoDBOpsManager) GetStatus(options ...status.Option) interface{} {
	if part, exists := status.GetOption(options, status.OMPartOption{}); exists {
		switch part.Value().(status.Part) {
		case status.OpsManager:
			return m.Status.OpsManagerStatus
		case status.AppDb:
			return m.Status.AppDbStatus
		case status.Backup:
			return m.Status.BackupStatus
		}
	}
	return m.Status
}

func (m MongoDBOpsManager) GetStatusPath(options ...status.Option) string {
	if part, exists := status.GetOption(options, status.OMPartOption{}); exists {
		switch part.Value().(status.Part) {
		case status.OpsManager:
			return "/status/opsManager"
		case status.AppDb:
			return "/status/applicationDatabase"
		case status.Backup:
			return "/status/backup"
		}
	}
	// we should never actually reach this
	return "/status"
}

// APIKeySecretName returns the secret object name to store the API key to communicate to ops-manager.
// To ensure backward compatibility it checks if a secret key is present with the old format name({$ops-manager-name}-admin-key),
// if not it returns the new name format ({$ops-manager-namespace}-${ops-manager-name}-admin-key), to have multiple om deployments
// with the same name.
func (m *MongoDBOpsManager) APIKeySecretName(client secrets.SecretClientInterface, operatorSecretPath string) (string, error) {
	oldAPISecretName := fmt.Sprintf("%s-admin-key", m.Name)
	operatorNamespace := env.ReadOrPanic(util.CurrentNamespace)
	oldAPIKeySecretNamespacedName := types.NamespacedName{Name: oldAPISecretName, Namespace: operatorNamespace}

	_, err := client.ReadSecret(oldAPIKeySecretNamespacedName, fmt.Sprintf("%s/%s/%s", operatorSecretPath, operatorNamespace, oldAPISecretName))
	if err != nil {
		if secrets.SecretNotExist(err) {
			return fmt.Sprintf("%s-%s-admin-key", m.Namespace, m.Name), nil
		}

		return "", err
	}
	return oldAPISecretName, nil
}

func (m *MongoDBOpsManager) GetSecurity() MongoDBOpsManagerSecurity {
	if m.Spec.Security == nil {
		return MongoDBOpsManagerSecurity{}
	}
	return *m.Spec.Security
}

func (m *MongoDBOpsManager) BackupStatefulSetName() string {
	return fmt.Sprintf("%s-backup-daemon", m.GetName())
}

func (m MongoDBOpsManager) GetSchemePort() (corev1.URIScheme, int) {
	if m.IsTLSEnabled() {
		return SchemePortFromAnnotation("https")
	}
	return SchemePortFromAnnotation("http")
}

func (m MongoDBOpsManager) IsTLSEnabled() bool {
	return m.Spec.Security != nil && (m.Spec.Security.TLS.SecretRef.Name != "" || m.Spec.Security.CertificatesSecretsPrefix != "")
}

func (m MongoDBOpsManager) TLSCertificateSecretName() string {
	// The old field has the precedence
	if m.GetSecurity().TLS.SecretRef.Name != "" {
		return m.GetSecurity().TLS.SecretRef.Name
	}
	if m.GetSecurity().CertificatesSecretsPrefix != "" {
		return fmt.Sprintf("%s-%s-cert", m.GetSecurity().CertificatesSecretsPrefix, m.Name)
	}
	return ""
}

func (m MongoDBOpsManager) CentralURL() string {
	fqdn := dns.GetServiceFQDN(m.SvcName(), m.Namespace, m.Spec.GetClusterDomain())
	scheme, port := m.GetSchemePort()

	// TODO use url.URL to build the url
	return fmt.Sprintf("%s://%s:%d", strings.ToLower(string(scheme)), fqdn, port)
}

func (m MongoDBOpsManager) DesiredReplicas() int {
	return m.Spec.AppDB.Members
}

func (m MongoDBOpsManager) CurrentReplicas() int {
	return m.Status.AppDbStatus.Members
}

// MemberNames returns the *current* names of Application Database members
// Note, that it's wrong to rely on the status/spec here as the state in StatefulSet maybe different
func (m MongoDBOpsManager) AppDBMemberNames(currentMembersCount int) []string {
	names := make([]string, currentMembersCount)

	for i := 0; i < currentMembersCount; i++ {
		names[i] = fmt.Sprintf("%s-%d", m.Spec.AppDB.Name(), i)
	}
	return names
}

func (m MongoDBOpsManager) BackupDaemonHostNames() []string {
	_, podnames := dns.GetDNSNames(m.BackupStatefulSetName(), "", m.Namespace, m.Spec.GetClusterDomain(), m.Spec.Backup.Members)
	return podnames
}

func (m MongoDBOpsManager) BackupDaemonFQDNs() []string {
	hostnames, _ := dns.GetDNSNames(m.BackupStatefulSetName(), m.BackupServiceName(), m.Namespace, m.Spec.GetClusterDomain(), m.Spec.Backup.Members)
	return hostnames
}

func (m MongoDBOpsManager) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: m.Name, Namespace: m.Namespace}
}

func (m MongoDBOpsManager) GetMongoDBVersionForAnnotation() string {
	return m.Spec.AppDB.Version
}

func (m MongoDBOpsManager) IsChangingVersion() bool {
	prevVersion := m.getPreviousVersion()
	return prevVersion != "" && prevVersion != m.Spec.AppDB.Version
}

func (m MongoDBOpsManager) getPreviousVersion() string {
	return annotations.GetAnnotation(&m, annotations.LastAppliedMongoDBVersion)
}

// GetAppDBUpdateStrategyType returns the update strategy type the AppDB Statefulset needs to be configured with.
// This depends whether or not a version change is in progress.
func (m MongoDBOpsManager) GetAppDBUpdateStrategyType() appsv1.StatefulSetUpdateStrategyType {
	if !m.IsChangingVersion() {
		return appsv1.RollingUpdateStatefulSetStrategyType
	}
	return appsv1.OnDeleteStatefulSetStrategyType
}

// GetSecretsMountedIntoPod returns the list of strings mounted into the pod that we need to watch.
func (m MongoDBOpsManager) GetSecretsMountedIntoPod() []string {
	secrets := []string{}
	tls := m.TLSCertificateSecretName()
	if tls != "" {
		secrets = append(secrets, tls)
	}

	if m.Spec.AdminSecret != "" {
		secrets = append(secrets, m.Spec.AdminSecret)
	}

	if m.Spec.Backup != nil {
		for _, config := range m.Spec.Backup.S3Configs {
			if config.S3SecretRef.Name != "" {
				secrets = append(secrets, config.S3SecretRef.Name)
			}
		}
	}

	return secrets
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

func getPartsFromStatusOptions(options ...status.Option) []status.Part {
	var parts []status.Part
	for _, part := range options {
		if omPart, ok := part.(status.OMPartOption); ok {
			statusPart := omPart.Value().(status.Part)
			parts = append(parts, statusPart)
		}
	}
	return parts
}

// AppDBConfigurable holds information needed to enable SCRAM-SHA
// and combines AppDBSpec (includes SCRAM configuration)
// and MongoDBOpsManager instance (used as the owner reference for the SCRAM related resources)
type AppDBConfigurable struct {
	AppDBSpec
	OpsManager MongoDBOpsManager
}

// GetOwnerReferences returns the OwnerReferences pointing to the MongoDBOpsManager instance and used by SCRAM related resources.
func (m AppDBConfigurable) GetOwnerReferences() []metav1.OwnerReference {
	groupVersionKind := schema.GroupVersionKind{
		Group:   GroupVersion.Group,
		Version: GroupVersion.Version,
		Kind:    m.OpsManager.Kind,
	}
	ownerReference := *metav1.NewControllerRef(&m.OpsManager, groupVersionKind)
	return []metav1.OwnerReference{ownerReference}
}
