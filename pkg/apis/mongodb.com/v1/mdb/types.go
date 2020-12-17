package mdb

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/scale"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/ldap"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	appsv1 "k8s.io/api/apps/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/blang/semver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDB{}, &MongoDBList{})
}

type LogLevel string

type ResourceType string

type SSLMode string

type TransportSecurity string

const (
	Debug LogLevel = "DEBUG"
	Info  LogLevel = "INFO"
	Warn  LogLevel = "WARN"
	Error LogLevel = "ERROR"
	Fatal LogLevel = "FATAL"

	Standalone     ResourceType = "Standalone"
	ReplicaSet     ResourceType = "ReplicaSet"
	ShardedCluster ResourceType = "ShardedCluster"

	RequireSSLMode  SSLMode = "requireSSL"
	PreferSSLMode   SSLMode = "preferSSL"
	AllowSSLMode    SSLMode = "allowSSL"
	DisabledSSLMode SSLMode = "disabled"

	RequireTLSMode SSLMode = "requireTLS"
	PreferTLSMode  SSLMode = "preferTLS"
	AllowTLSMode   SSLMode = "allowTLS"

	DeploymentLinkIndex = 0

	TransportSecurityNone TransportSecurity = "none"
	TransportSecurityTLS  TransportSecurity = "tls"
)

// MongoDB resources allow you to deploy Standalones, ReplicaSets or SharedClusters
// to your Kubernetes cluster

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=mongodb,scope=Namespaced,shortName=mdb
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the MongoDB deployment."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",description="Version of MongoDB server."
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".status.type",description="The type of MongoDB deployment. One of 'ReplicaSet', 'ShardedCluster' and 'Standalone'."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDB resource was created."
type MongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            MongoDbStatus `json:"status"`
	Spec              MongoDbSpec   `json:"spec"`
}

func (mdb MongoDB) AddValidationToManager(m manager.Manager) error {
	return ctrl.NewWebhookManagedBy(m).For(&mdb).Complete()
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDB `json:"items"`
}

// MongoDBHorizonConfig holds a map of horizon names to the node addresses,
// e.g.
// {
//   "internal": "my-rs-2.my-internal-domain.com:31843",
//   "external": "my-rs-2.my-external-domain.com:21467"
// }
// The key of each item in the map is an arbitrary, user-chosen string that
// represents the name of the horizon. The value of the item is the host and,
// optionally, the port that this mongod node will be connected to from.
type MongoDBHorizonConfig map[string]string

type MongoDBConnectivity struct {
	ReplicaSetHorizons []MongoDBHorizonConfig `json:"replicaSetHorizons,omitempty"`
}

type MongoDbStatus struct {
	status.Common                   `json:",inline"`
	BackupStatus                    *BackupStatus `json:"backup,omitempty"`
	MongodbShardedClusterSizeConfig `json:",inline"`
	Members                         int              `json:"members,omitempty"`
	Version                         string           `json:"version"`
	Link                            string           `json:"link,omitempty"`
	Warnings                        []status.Warning `json:"warnings,omitempty"`
}

type BackupMode string

type BackupStatus struct {
	StatusName string `json:"statusName"`
}

type MongoDbSpec struct {
	ShardedClusterSpec `json:",inline"`

	Version                     string  `json:"version,omitempty"`
	FeatureCompatibilityVersion *string `json:"featureCompatibilityVersion,omitempty"`

	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`

	// ExposedExternally determines whether a NodePort service should be created for the resource
	ExposedExternally bool `json:"exposedExternally,omitempty"`

	// Deprecated: This has been replaced by the ClusterDomain which should be
	// used instead
	ClusterName    string `json:"clusterName,omitempty"`
	ClusterDomain  string `json:"clusterDomain,omitempty"`
	ConnectionSpec `json:",inline"`
	Persistent     *bool        `json:"persistent,omitempty"`
	ResourceType   ResourceType `json:"type,omitempty"`
	Backup         *Backup      `json:"backup,omitempty"`

	// sharded clusters

	// TODO: uncomment once we remove podSpec and support the various statefulSet specs
	// +optional
	//ConfigSrvStatefulSetConfiguration *StatefulSetConfiguration `json:"configSrvStatefulSet,omitempty"`
	// +optional
	//MongosStatefulSetConfiguration *StatefulSetConfiguration `json:"mongosStatefulSet,omitempty"`
	// +optional
	//ShardStatefulSetConfiguration *StatefulSetConfiguration `json:"shardStatefulSet,omitempty"`
	// +optional
	//StatefulSetConfiguration *StatefulSetConfiguration `json:"statefulSet,omitempty"`

	MongodbShardedClusterSizeConfig `json:",inline"`

	Agent AgentConfig `json:"agent,omitempty"`

	// replica set
	Members int             `json:"members,omitempty"`
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`

	Security *Security `json:"security,omitempty"`

	Connectivity *MongoDBConnectivity `json:"connectivity,omitempty"`

	// AdditionalMongodConfig is additional configuration that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	AdditionalMongodConfig AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
}

// Backup contains configuration options for configuring
// backup for this MongoDB resource
type Backup struct {

	// +kubebuilder:validation:Enum=enabled;disabled;terminated
	Mode BackupMode `json:"mode"`
}

type AgentConfig struct {
	StartupParameters StartupParameters `json:"startupOptions"`
}

type StartupParameters map[string]string

func (m *MongoDB) DesiredReplicaSetMembers() int {
	return m.Spec.Members
}

func (m *MongoDB) CurrentReplicaSetMembers() int {
	return m.Status.Members
}

// StatefulSetConfiguration holds the optional custom StatefulSet
// that should be merged into the operator created one.
type StatefulSetConfiguration struct {
	Spec appsv1.StatefulSetSpec `json:"spec"`
}

// GetVersion returns the version of the MongoDB. In the case of the AppDB
// it is possible for this to be an empty string. For a regular mongodb, the regex
// version string validator will not allow this.
func (ms MongoDbSpec) GetVersion() string {
	if ms.Version == "" {
		return util.BundledAppDbMongoDBVersion
	}
	return ms.Version
}

func (ms MongoDbSpec) GetClusterDomain() string {
	if ms.ClusterDomain != "" {
		return ms.ClusterDomain
	}
	if ms.ClusterName != "" {
		return ms.ClusterName
	}
	return "cluster.local"
}

// TODO docs
func (m MongoDbSpec) MinimumMajorVersion() uint64 {
	if m.FeatureCompatibilityVersion != nil && *m.FeatureCompatibilityVersion != "" {
		fcv := *m.FeatureCompatibilityVersion

		// ignore errors here as the format of FCV/version is handled by CRD validation
		semverFcv, _ := semver.Make(fmt.Sprintf("%s.0", fcv))
		return semverFcv.Major
	}
	semverVersion, _ := semver.Make(m.GetVersion())
	return semverVersion.Major
}

// ProjectConfig contains the configuration expected from the `project` (ConfigMap) attribute in
// `.spec.project`.
type ProjectConfig struct {
	// +required
	BaseURL string
	// +optional
	ProjectName string
	// +optional
	OrgID string
	// +optional
	Credentials string
	// +optional
	UseCustomCA bool
	// +optional
	env.SSLProjectConfig
}

// Credentials contains the configuration expected from the `credentials` (Secret)` attribute in
// `.spec.credentials`.
type Credentials struct {
	// +required
	User string

	// +required
	PublicAPIKey string
}

type ConfigMapRef struct {
	Name string `json:"name,omitempty"`
}

type PrivateCloudConfig struct {
	ConfigMapRef ConfigMapRef `json:"configMapRef,omitempty"`
}

// ConnectionSpec holds fields required to establish an Ops Manager connection
// note, that the fields are marked as 'omitempty' as otherwise they are shown for AppDB
// which is not good
type ConnectionSpec struct {
	// Transient field - the name of the project. By default is equal to the name of the resource
	// though can be overridden if the ConfigMap specifies a different name
	ProjectName string `json:"-"` // ignore when marshalling

	// Name of the Secret holding credentials information
	Credentials string `json:"credentials,omitempty"`

	// Dev note: don't reference these two fields directly - use the `getProject` method instead
	OpsManagerConfig   *PrivateCloudConfig `json:"opsManager,omitempty"`
	CloudManagerConfig *PrivateCloudConfig `json:"cloudManager,omitempty"`

	// Deprecated: This has been replaced by the PrivateCloudConfig which should be
	// used instead
	Project string `json:"project,omitempty"`

	// FIXME: LogLevel is not a required field for creating an Ops Manager connection, it should not be here.
	LogLevel LogLevel `json:"logLevel,omitempty"`
}

type Security struct {
	TLSConfig      *TLSConfig      `json:"tls,omitempty"`
	Authentication *Authentication `json:"authentication,omitempty"`

	// Deprecated: This has been replaced by Authentication.InternalCluster which
	// should be used instead
	ClusterAuthMode string `json:"clusterAuthenticationMode,omitempty"`

	Roles []MongoDbRole `json:"roles,omitempty"`
}

func (spec MongoDbSpec) GetSecurity() *Security {
	if spec.Security == nil {
		return &Security{}
	}
	return spec.Security
}

// GetAgentMechanism returns the authentication mechanism that the agents will be using.
// The agents will use X509 if it is the only mechanism specified, otherwise they will use SCRAM if specified
// and no auth if no mechanisms exist.
func (s *Security) GetAgentMechanism(currentMechanism string) string {
	if s == nil || s.Authentication == nil {
		return ""
	}
	auth := s.Authentication
	if !s.Authentication.Enabled {
		return ""
	}

	if currentMechanism == "MONGODB-X509" {
		return util.X509
	}

	// If we arrive here, this should
	//  ALWAYS be true, as we do not allow
	// agents.mode to be empty
	// if more than one mode in specified in
	// spec.authentication.modes
	// The check is done in the validation webhook
	if len(s.Authentication.Modes) == 1 {
		return s.Authentication.Modes[0]
	}
	return auth.Agents.Mode
}

// ShouldUseX509 determines if the deployment should have X509 authentication configured
// whether it was configured explicitly or if it required as it would be performing
// an illegal transition otherwise.
func (s *Security) ShouldUseX509(currentAgentAuthMode string) bool {
	return s.GetAgentMechanism(currentAgentAuthMode) == util.X509
}

// AgentClientCertificateSecretName returns the name of the Secret that holds the agent
// client TLS certificates.
// If no custom name has been defined, it returns the default one.
func (s Security) AgentClientCertificateSecretName() corev1.SecretKeySelector {
	secretName := util.AgentSecretName
	if s.ShouldUseClientCertificates() {
		secretName = s.Authentication.Agents.ClientCertificateSecretRef.Name
	}

	return corev1.SecretKeySelector{
		Key:                  util.AutomationAgentPemSecretKey,
		LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
	}
}

// The customer has set ClientCertificateSecretRef. This signals that client certs are required,
// even when no x509 agent-auth has been enabled.
func (s Security) ShouldUseClientCertificates() bool {
	return s.Authentication != nil && s.Authentication.Agents.ClientCertificateSecretRef.Name != ""
}

// RequiresClientTLSAuthentication checks if client TLS authentication is required, depending
// on a set of defined attributes in the MongoDB resource. This can be explicitly set, setting
// `Authentication.RequiresClientTLSAuthentication` to true or implicitly by setting x509 auth
//  as the only auth mechanism.
func (s Security) RequiresClientTLSAuthentication() bool {
	if s.Authentication == nil {
		return false
	}

	if len(s.Authentication.Modes) == 1 && stringutil.Contains(s.Authentication.Modes, util.X509) {
		return true
	}

	return s.Authentication.RequiresClientTLSAuthentication
}

func (s *Security) ShouldUseLDAP(currentAgentAuthMode string) bool {
	return s.GetAgentMechanism(currentAgentAuthMode) == util.LDAP
}

func (s *Security) GetInternalClusterAuthenticationMode() string {
	if s == nil || s.Authentication == nil {
		return ""
	}
	if s.Authentication.InternalCluster != "" {
		return strings.ToUpper(s.Authentication.InternalCluster)
	}
	if s.ClusterAuthMode != "" {
		return strings.ToUpper(s.ClusterAuthMode)
	}
	return ""
}

// Authentication holds various authentication related settings that affect
// this MongoDB resource.
type Authentication struct {
	Enabled         bool     `json:"enabled"`
	Modes           []string `json:"modes,omitempty"`
	InternalCluster string   `json:"internalCluster,omitempty"`
	// IgnoreUnknownUsers maps to the inverse of auth.authoritativeSet
	IgnoreUnknownUsers bool `json:"ignoreUnknownUsers,omitempty"`

	// LDAP
	Ldap *Ldap `json:"ldap"`

	// Agents contains authentication configuration properties for the agents
	Agents AgentAuthentication `json:"agents"`

	// Clients should present valid TLS certificates
	RequiresClientTLSAuthentication bool `json:"requireClientTLSAuthentication,omitempty"`
}

type AuthenticationRestriction struct {
	ClientSource  []string `json:"clientSource,omitempty"`
	ServerAddress []string `json:"serverAddress,omitempty"`
}

type Resource struct {
	Db         string `json:"db"`
	Collection string `json:"collection"`
	Cluster    *bool  `json:"cluster,omitempty"`
}

type Privilege struct {
	Actions  []string `json:"actions"`
	Resource Resource `json:"resource"`
}

type InheritedRole struct {
	Db   string `json:"db"`
	Role string `json:"role"`
}

type MongoDbRole struct {
	Role                       string                      `json:"role"`
	AuthenticationRestrictions []AuthenticationRestriction `json:"authenticationRestrictions,omitempty"`
	Db                         string                      `json:"db"`
	Privileges                 []Privilege                 `json:"privileges,omitempty"`
	Roles                      []InheritedRole             `json:"roles,omitempty"`
}

type AgentAuthentication struct {
	// Mode is the desired Authentication mode that the agents will use
	Mode string `json:"mode"`

	AutomationUserName string `json:"automationUserName"`

	AutomationPasswordSecretRef corev1.SecretKeySelector `json:"automationPasswordSecretRef"`

	AutomationLdapGroupDN string `json:"automationLdapGroupDN"`

	ClientCertificateSecretRef corev1.SecretKeySelector `json:"clientCertificateSecretRef,omitempty"`
}

// IsX509Enabled determines if X509 is to be enabled at the project level
// it does not necessarily mean that the agents are using X509 authentication
func (a *Authentication) IsX509Enabled() bool {
	return stringutil.Contains(a.GetModes(), util.X509)
}

// IsLDAPEnabled determines if LDAP is to be enabled at the project level
func (a *Authentication) isLDAPEnabled() bool {
	return stringutil.Contains(a.GetModes(), util.LDAP)
}

// GetModes returns the modes of the Authentication instance of an empty
// list if it is nil
func (a *Authentication) GetModes() []string {
	if a == nil {
		return []string{}
	}
	return a.Modes
}

type Ldap struct {
	Servers []string `json:"servers"`

	// +kubebuilder:validation:Enum=tls;none
	TransportSecurity        *TransportSecurity `json:"transportSecurity"`
	ValidateLDAPServerConfig *bool              `json:"validateLDAPServerConfig"`

	// Allows to point at a ConfigMap/key with a CA file to mount on the Pod
	CAConfigMapRef *corev1.ConfigMapKeySelector `json:"caConfigMapRef,omitempty"`

	BindQueryUser      string       `json:"bindQueryUser"`
	BindQuerySecretRef TLSSecretRef `json:"bindQueryPasswordSecretRef"`

	AuthzQueryTemplate string `json:"authzQueryTemplate"`
	UserToDNMapping    string `json:"userToDNMapping"`
}

type TLSConfig struct {
	// Enables TLS for this resource. This will make the operator try to mount a
	// Secret with a defined name (<resource-name>-cert).
	// This is only used when enabling TLS on a MongoDB resource, and not on the
	// AppDB, where TLS is configured by setting `secretRef.Name`.
	Enabled bool `json:"enabled,omitempty"`

	AdditionalCertificateDomains []string `json:"additionalCertificateDomains,omitempty"`

	// CA corresponds to a ConfigMap containing an entry for the CA certificate (ca.pem)
	// used to validate the certificates created already.
	CA string `json:"ca,omitempty"`

	// AppDB-only attributes
	//
	// SecretRef points to a Secret object containing the certificates to use when enabling
	// TLS on the AppDB.
	SecretRef TLSSecretRef `json:"secretRef,omitempty"`
}

func (t *TLSConfig) IsEnabled() bool {
	if t == nil {
		return false
	}
	return t.Enabled || t.SecretRef.Name != ""
}

// IsSelfManaged returns true if the TLS is self-managed (cert provided by the customer), not Operator-managed
func (t TLSConfig) IsSelfManaged() bool {
	return t.CA != "" || t.SecretRef.Name != ""
}

// TLSSecretRef contains a reference to a Secret object that contains certificates to
// be mounted. Defining this value will implicitly "enable" TLS on this resource.
type TLSSecretRef struct {
	Name string `json:"name,omitempty"`
}

func (spec MongoDbSpec) GetTLSConfig() *TLSConfig {
	if spec.Security == nil || spec.Security.TLSConfig == nil {
		return &TLSConfig{}
	}

	return spec.Security.TLSConfig
}

// when unmarshaling a MongoDB instance, we don't want to have any nil references
// these are replaced with an empty instance to prevent nil references
func (m *MongoDB) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDB
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}

	m.InitDefaults()

	return nil
}

func (m *MongoDB) ServiceName() string {
	if m.Spec.Service == "" {
		return m.Name + "-svc"
	}
	return m.Spec.Service
}

func (m *MongoDB) ConfigSrvServiceName() string {
	return m.Name + "-cs"
}

func (m *MongoDB) ShardServiceName() string {
	return m.Name + "-sh"
}

func (m *MongoDB) MongosRsName() string {
	return m.Name + "-mongos"
}

func (m *MongoDB) ConfigRsName() string {
	return m.Name + "-config"
}

func (m *MongoDB) ShardRsName(i int) string {
	// Unfortunately the pattern used by OM (name_idx) doesn't work as Kubernetes doesn't create the stateful set with an
	// exception: "a DNS-1123 subdomain must consist of lower case alphanumeric characters, '-' or '.'"
	return fmt.Sprintf("%s-%d", m.Name, i)
}

func (m MongoDB) IsLDAPEnabled() bool {
	if m.Spec.Security == nil || m.Spec.Security.Authentication == nil {
		return false
	}
	return stringutil.Contains(m.Spec.Security.Authentication.Modes, util.LDAP)
}

func (m *MongoDB) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	m.Status.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.BackupStatusOption{}); exists {
		if m.Status.BackupStatus == nil {
			m.Status.BackupStatus = &BackupStatus{}
		}
		m.Status.BackupStatus.StatusName = option.(status.BackupStatusOption).Value().(string)
	}

	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		m.Status.Warnings = append(m.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}
	if option, exists := status.GetOption(statusOptions, status.BaseUrlOption{}); exists {
		m.Status.Link = option.(status.BaseUrlOption).BaseUrl
	}
	switch m.Spec.ResourceType {
	case ReplicaSet:
		if option, exists := status.GetOption(statusOptions, scale.ReplicaSetMembersOption{}); exists {
			m.Status.Members = option.(scale.ReplicaSetMembersOption).Members
		}
	case ShardedCluster:
		if option, exists := status.GetOption(statusOptions, scale.ShardedClusterConfigServerOption{}); exists {
			m.Status.ConfigServerCount = option.(scale.ShardedClusterConfigServerOption).Members
		}
		if option, exists := status.GetOption(statusOptions, scale.ShardedClusterMongodsPerShardCountOption{}); exists {
			m.Status.MongodsPerShardCount = option.(scale.ShardedClusterMongodsPerShardCountOption).Members
		}
		if option, exists := status.GetOption(statusOptions, scale.ShardedClusterMongosOption{}); exists {
			m.Status.MongosCount = option.(scale.ShardedClusterMongosOption).Members
		}
	}

	if phase == status.PhaseRunning {
		m.Status.Version = m.Spec.Version
		m.Status.Message = ""

		switch m.Spec.ResourceType {
		case ShardedCluster:
			m.Status.ShardCount = m.Spec.ShardCount
		}
	}
}

func (m *MongoDB) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	m.Status.Warnings = warnings
}

func (m *MongoDB) AddWarningIfNotExists(warning status.Warning) {
	m.Status.Warnings = status.Warnings(m.Status.Warnings).AddIfNotExists(warning)
}

func (m MongoDB) GetPlural() string {
	return "mongodb"
}

func (m *MongoDB) GetStatus(...status.Option) interface{} {
	return m.Status
}

func (m MongoDB) GetStatusPath(...status.Option) string {
	return "/status"
}

// GetProject returns the name of the ConfigMap containing the information about connection to OM/CM
func (c *ConnectionSpec) GetProject() string {
	// the contract is that either ops manager or cloud manager must be provided - the controller must validate this
	if c.OpsManagerConfig.ConfigMapRef.Name != "" {
		return c.OpsManagerConfig.ConfigMapRef.Name
	}
	if c.CloudManagerConfig.ConfigMapRef.Name != "" {
		return c.CloudManagerConfig.ConfigMapRef.Name
	}
	// failback to the deprecated field
	return c.Project
}

// InitDefaults makes sure the MongoDB resource has correct state after initialization:
// - prevents any references from having nil values.
// - makes sure the spec is in correct state
//
// should not be called directly, used in tests and unmarshalling
func (m *MongoDB) InitDefaults() {
	// al resources have a pod spec
	if m.Spec.PodSpec == nil {
		m.Spec.PodSpec = NewMongoDbPodSpec()
	}

	if m.Spec.ResourceType == ShardedCluster {
		if m.Spec.ConfigSrvPodSpec == nil {
			m.Spec.ConfigSrvPodSpec = NewMongoDbPodSpec()
		}
		if m.Spec.MongosPodSpec == nil {
			m.Spec.MongosPodSpec = NewMongoDbPodSpec()
		}
		if m.Spec.ShardPodSpec == nil {
			m.Spec.ShardPodSpec = NewMongoDbPodSpec()
		}
	}

	if m.Spec.Connectivity == nil {
		m.Spec.Connectivity = newConnectivity()
	}

	ensureSecurity(&m.Spec)

	if m.Spec.OpsManagerConfig == nil {
		m.Spec.OpsManagerConfig = newOpsManagerConfig()
	}

	if m.Spec.CloudManagerConfig == nil {
		m.Spec.CloudManagerConfig = newOpsManagerConfig()
	}

	// ProjectName defaults to the name of the resource
	m.Spec.ProjectName = m.Name
}

func (m *MongoDB) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(m.Namespace, m.Name)
}

// ConnectionURL returns connection url to the MongoDB based on its internal state. Username and password are
// provided as parameters as they need to be fetched by the caller
func (m *MongoDB) ConnectionURL(userName, password string, connectionParams map[string]string) string {
	statefulsetName := m.Name
	if m.Spec.ResourceType == ShardedCluster {
		statefulsetName = m.MongosRsName()
	}

	return BuildConnectionUrl(statefulsetName, m.ServiceName(), m.Namespace, userName, password, m.Spec, connectionParams)
}

func (m MongoDB) GetLDAP(password, caContents string) *ldap.Ldap {
	if !m.IsLDAPEnabled() {
		return nil
	}

	mdbLdap := m.Spec.Security.Authentication.Ldap
	transportSecurity := TransportSecurityNone
	if mdbLdap.TransportSecurity != nil {
		transportSecurity = TransportSecurityTLS
	}

	validateServerConfig := true
	if mdbLdap.ValidateLDAPServerConfig != nil {
		validateServerConfig = *mdbLdap.ValidateLDAPServerConfig
	}

	return &ldap.Ldap{
		BindQueryUser:            mdbLdap.BindQueryUser,
		BindQueryPassword:        password,
		Servers:                  strings.Join(mdbLdap.Servers, ","),
		TransportSecurity:        string(transportSecurity),
		CaFileContents:           caContents,
		ValidateLDAPServerConfig: validateServerConfig,

		// Related to LDAP Authorization
		AuthzQueryTemplate: mdbLdap.AuthzQueryTemplate,
		UserToDnMapping:    mdbLdap.UserToDNMapping,

		// TODO: Enable LDAP SASL bind method
		BindMethod:         "simple",
		BindSaslMechanisms: "",
	}

}

type MongoDbPodSpec struct {
	// DEPRECATED. Please set this value using `spec.podTemplate` instead.
	Cpu string `json:"cpu,omitempty"`
	// DEPRECATED. Please set this value using `spec.podTemplate` instead.
	CpuRequests string `json:"cpuRequests,omitempty"`
	// DEPRECATED. Please set this value using `spec.podTemplate` instead.
	Memory string `json:"memory,omitempty"`
	// DEPRECATED. Please set this value using `spec.podTemplate` instead.
	MemoryRequests string `json:"memoryRequests,omitempty"`

	PodAffinity                *corev1.PodAffinity     `json:"podAffinity,omitempty"`
	NodeAffinity               *corev1.NodeAffinity    `json:"nodeAffinity,omitempty"`
	PodTemplate                *corev1.PodTemplateSpec `json:"podTemplate,omitempty"`
	PodAntiAffinityTopologyKey string                  `json:"podAntiAffinityTopologyKey,omitempty"`

	// Note, that this field is used by MongoDB resources only, let's keep it here for simplicity
	Persistence *Persistence `json:"persistence,omitempty"`
}

// This is a struct providing the opportunity to customize the pod created under the hood.
// It naturally delegates to inner object and provides some defaults that can be overriden in each specific case
// TODO remove in favor or 'StatefulSetHelper.setPodSpec(podSpec, defaultPodSpec)'
type PodSpecWrapper struct {
	MongoDbPodSpec
	// These are the default values, unfortunately Golang doesn't provide the possibility to inline default values into
	// structs so use the operator.NewDefaultPodSpec constructor for this
	Default MongoDbPodSpec
}

type Persistence struct {
	SingleConfig   *PersistenceConfig         `json:"single,omitempty"`
	MultipleConfig *MultiplePersistenceConfig `json:"multiple,omitempty"`
}

type MultiplePersistenceConfig struct {
	Data    *PersistenceConfig `json:"data,omitempty"`
	Journal *PersistenceConfig `json:"journal,omitempty"`
	Logs    *PersistenceConfig `json:"logs,omitempty"`
}

type PersistenceConfig struct {
	Storage       string                `json:"storage,omitempty"`
	StorageClass  *string               `json:"storageClass,omitempty"`
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

func (p PodSpecWrapper) GetCpuOrDefault() string {
	if p.Cpu == "" && p.CpuRequests == "" {
		return p.Default.Cpu
	}
	return p.Cpu
}

func (p PodSpecWrapper) GetMemoryOrDefault() string {
	// We don't set default if either Memory requests or Memory limits are specified by the User
	if p.Memory == "" && p.MemoryRequests == "" {
		return p.Default.Memory
	}
	return p.Memory
}

func (p PodSpecWrapper) GetCpuRequestsOrDefault() string {
	if p.CpuRequests == "" && p.Cpu == "" {
		return p.Default.CpuRequests
	}
	return p.CpuRequests
}

func (p PodSpecWrapper) GetMemoryRequestsOrDefault() string {
	// We don't set default if either Memory requests or Memory limits are specified by the User
	// otherwise it's possible to get failed Statefulset (e.g. the user specified limits of 200M but we default
	//requests to 500M though requests must be less than limits)
	if p.MemoryRequests == "" && p.Memory == "" {
		return p.Default.MemoryRequests
	}
	return p.MemoryRequests
}

func (p PodSpecWrapper) GetTopologyKeyOrDefault() string {
	if p.PodAntiAffinityTopologyKey == "" {
		return p.Default.PodAntiAffinityTopologyKey
	}
	return p.PodAntiAffinityTopologyKey
}

func (p PodSpecWrapper) SetCpu(cpu string) PodSpecWrapper {
	p.Cpu = cpu
	return p
}

func (p PodSpecWrapper) SetMemory(memory string) PodSpecWrapper {
	p.Memory = memory
	return p
}

func (p PodSpecWrapper) SetTopology(topology string) PodSpecWrapper {
	p.PodAntiAffinityTopologyKey = topology
	return p
}

func GetStorageOrDefault(config *PersistenceConfig, defaultConfig PersistenceConfig) string {
	if config == nil || config.Storage == "" {
		return defaultConfig.Storage
	}
	return config.Storage
}

// Create a MongoDbPodSpec reference without any nil references
// used to initialize any MongoDbPodSpec fields with valid values
// in order to prevent panicking at runtime.
func NewMongoDbPodSpec() *MongoDbPodSpec {
	return &MongoDbPodSpec{}
}

func (spec MongoDbSpec) GetTLSMode() SSLMode {
	if spec.Security == nil || !spec.Security.TLSConfig.IsEnabled() {
		return DisabledSSLMode
	}

	// spec.Security.TLSConfig.IsEnabled() is true -> requireSSLMode
	if spec.AdditionalMongodConfig == nil {
		return RequireSSLMode
	}
	mode := maputil.ReadMapValueAsString(spec.AdditionalMongodConfig, "net", "ssl", "mode")
	if mode == "" {
		return RequireSSLMode
	}
	return validModeOrDefault(SSLMode(mode))
}

// Replicas returns the number of "user facing" replicas of the MongoDB resource. This method can be used for
// constructing the mongodb URL for example.
// 'Members' would be a more consistent function but go doesn't allow to have the same
func (spec MongoDbSpec) Replicas() int {
	var replicasCount int
	switch spec.ResourceType {
	case Standalone:
		replicasCount = 1
	case ReplicaSet:
		replicasCount = spec.Members
	case ShardedCluster:
		replicasCount = spec.MongosCount
	default:
		panic("Unknown type of resource!")
	}
	return replicasCount
}

// validModeOrDefault returns a valid mode for the Net.SSL.Mode string
func validModeOrDefault(mode SSLMode) SSLMode {
	if mode == "" {
		return RequireSSLMode
	}

	if mode == RequireTLSMode {
		mode = RequireSSLMode
	} else if mode == PreferTLSMode {
		mode = PreferSSLMode
	} else if mode == AllowTLSMode {
		mode = AllowSSLMode
	}

	return mode
}

func newConnectivity() *MongoDBConnectivity {
	return &MongoDBConnectivity{}
}

// PrivateCloudConfig returns and empty `PrivateCloudConfig` object
func newOpsManagerConfig() *PrivateCloudConfig {
	return &PrivateCloudConfig{}
}

func ensureSecurity(spec *MongoDbSpec) {
	if spec.Security == nil {
		spec.Security = newSecurity()
		return
	}
	if spec.Security.TLSConfig == nil {
		spec.Security.TLSConfig = &TLSConfig{}
	}
	if spec.Security.Roles == nil {
		spec.Security.Roles = make([]MongoDbRole, 0)
	}
}

func newAuthentication() *Authentication {
	return &Authentication{Modes: []string{}}
}

func newSecurity() *Security {
	return &Security{TLSConfig: &TLSConfig{}}
}

func BuildConnectionUrl(statefulsetName, serviceName, namespace, userName, password string, spec MongoDbSpec, connectionParams map[string]string) string {
	if stringutil.Contains(spec.Security.Authentication.GetModes(), util.SCRAM) && (userName == "" || password == "") {
		panic("Dev error: UserName and Password must be specified if the resource has SCRAM-SHA enabled")
	}
	replicasCount := spec.Replicas()

	hostnames, _ := util.GetDNSNames(statefulsetName, serviceName, namespace, spec.GetClusterDomain(), replicasCount)
	uri := "mongodb://"
	if stringutil.Contains(spec.Security.Authentication.GetModes(), util.SCRAM) {
		uri += fmt.Sprintf("%s:%s@", url.QueryEscape(userName), url.QueryEscape(password))
	}
	for i, h := range hostnames {
		hostnames[i] = fmt.Sprintf("%s:%d", h, util.MongoDbDefaultPort)
	}
	uri += strings.Join(hostnames, ",")

	// default and calculated query parameters
	params := map[string]string{"connectTimeoutMS": "20000", "serverSelectionTimeoutMS": "20000"}
	if spec.ResourceType == ReplicaSet {
		params["replicaSet"] = statefulsetName
	}
	if spec.Security.TLSConfig.IsEnabled() {
		params["ssl"] = "true"
	}
	if stringutil.Contains(spec.Security.Authentication.GetModes(), util.SCRAM) {
		params["authSource"] = util.DefaultUserDatabase

		comparison, err := util.CompareVersions(spec.GetVersion(), util.MinimumScramSha256MdbVersion)
		if err != nil {
			// This is the dev error - the object must have a correct state by this stage and the version must be
			// validated in the controller/web hook
			panic(err)
		}
		if comparison < 0 {
			params["authMechanism"] = "SCRAM-SHA-1"
		} else {
			params["authMechanism"] = "SCRAM-SHA-256"
		}
	}
	// custom parameters may override the default ones
	for k, v := range connectionParams {
		params[k] = v
	}

	var keys []string
	for k := range params {
		keys = append(keys, k)
	}
	uri += "/?"
	// sorting parameters to make a url stable
	sort.Strings(keys)
	for _, k := range keys {
		uri += fmt.Sprintf("%s=%s&", k, params[k])
	}
	return strings.TrimSuffix(uri, "&")
}
