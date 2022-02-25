package mdb

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"

	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connectionstring"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/ldap"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

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

	TransportSecurityNone TransportSecurity = "none"
	TransportSecurityTLS  TransportSecurity = "tls"
)

// MongoDB resources allow you to deploy Standalones, ReplicaSets or SharedClusters
// to your Kubernetes cluster

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=mongodb,scope=Namespaced,shortName=mdb
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the MongoDB deployment."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",description="Version of MongoDB server."
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type",description="The type of MongoDB deployment. One of 'ReplicaSet', 'ShardedCluster' and 'Standalone'."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDB resource was created."
type MongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status MongoDbStatus `json:"status"`
	Spec   MongoDbSpec   `json:"spec"`
}

func (in *MongoDB) ForcedIndividualScaling() bool {
	return false
}

func (m *MongoDB) GetSpec() DbSpec {
	return &m.Spec
}

func (m *MongoDB) GetProjectConfigMapNamespace() string {
	return m.GetNamespace()
}

func (m *MongoDB) GetCredentialsSecretNamespace() string {
	return m.GetNamespace()
}

func (m *MongoDB) GetProjectConfigMapName() string {
	return m.Spec.GetProject()
}

func (m *MongoDB) GetCredentialsSecretName() string {
	return m.Spec.Credentials
}

func (m *MongoDB) GetSecurity() *Security {
	return m.Spec.GetSecurity()
}

func (m *MongoDB) GetMinimumMajorVersion() uint64 {
	return m.Spec.MinimumMajorVersion()
}

func (m *MongoDB) AddValidationToManager(mgr manager.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(m).Complete()
}

func (m *MongoDB) GetBackupSpec() *Backup {
	return m.Spec.Backup
}

func (m *MongoDB) GetResourceType() ResourceType {
	return m.Spec.ResourceType
}

func (m *MongoDB) GetResourceName() string {
	return m.Name
}

// GetSecretsMountedIntoDBPod returns a list of all the optional secret names that are used by this resource.
func (m MongoDB) GetSecretsMountedIntoDBPod() []string {
	secrets := []string{}
	var tls string
	if m.Spec.ResourceType == ShardedCluster {
		for i := 0; i < m.Spec.ShardCount; i++ {
			tls = m.GetSecurity().MemberCertificateSecretName(m.ShardRsName(i))
			if tls != "" {
				secrets = append(secrets, tls)
			}
		}
		tls = m.GetSecurity().MemberCertificateSecretName(m.ConfigRsName())
		if tls != "" {
			secrets = append(secrets, tls)
		}
		tls = m.GetSecurity().MemberCertificateSecretName(m.ConfigRsName())
		if tls != "" {
			secrets = append(secrets, tls)
		}
	} else {
		tls = m.GetSecurity().MemberCertificateSecretName(m.Name)
		if tls != "" {
			secrets = append(secrets, tls)
		}
	}
	agentCerts := m.GetSecurity().AgentClientCertificateSecretName(m.Name).Name
	if agentCerts != "" {
		secrets = append(secrets, agentCerts)
	}
	if m.Spec.Security.Authentication != nil && m.Spec.Security.Authentication.Ldap != nil {
		secrets = append(secrets, m.Spec.GetSecurity().Authentication.Ldap.BindQuerySecretRef.Name)
		if m.Spec.Security.Authentication != nil && m.Spec.Security.Authentication.Agents.AutomationPasswordSecretRef.Name != "" {
			secrets = append(secrets, m.Spec.Security.Authentication.Agents.AutomationPasswordSecretRef.Name)
		}
	}
	return secrets
}

type AdditionalMongodConfigType int

const (
	StandaloneConfig = iota
	ReplicaSetConfig
	MongosConfig
	ConfigServerConfig
	ShardConfig
)

// GetLastAdditionalMongodConfigByType returns the last successfully achieved AdditionalMongodConfigType for the given component.
func (m *MongoDB) GetLastAdditionalMongodConfigByType(configType AdditionalMongodConfigType) (AdditionalMongodConfig, error) {
	lastSpec, err := m.GetLastSpec()
	if err != nil || lastSpec == nil {
		return AdditionalMongodConfig{}, err
	}

	switch configType {
	case ReplicaSetConfig, StandaloneConfig:
		return lastSpec.AdditionalMongodConfig, nil
	case MongosConfig:
		return lastSpec.MongosSpec.AdditionalMongodConfig, nil
	case ConfigServerConfig:
		return lastSpec.ConfigSrvSpec.AdditionalMongodConfig, nil
	case ShardConfig:
		return lastSpec.ShardSpec.AdditionalMongodConfig, nil
	}
	return AdditionalMongodConfig{}, nil
}

// +kubebuilder:object:generate=false
type DbSpec interface {
	Replicas() int
	GetClusterDomain() string
	GetMongoDBVersion() string
	GetSecurityAuthenticationModes() []string
	GetResourceType() ResourceType
	IsSecurityTLSConfigEnabled() bool
	GetFeatureCompatibilityVersion() *string
	GetTLSMode() tls.Mode
	GetHorizonConfig() []MongoDBHorizonConfig
	GetAdditionalMongodConfig() AdditionalMongodConfig
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
	// +kubebuilder:pruning:PreserveUnknownFields
	ShardedClusterSpec `json:",inline"`
	// +kubebuilder:validation:Pattern=^[0-9]+.[0-9]+.[0-9]+(-.+)?$|^$
	// +kubebuilder:validation:Required
	Version                     string  `json:"version"`
	FeatureCompatibilityVersion *string `json:"featureCompatibilityVersion,omitempty"`

	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`

	// ExposedExternally determines whether a NodePort service should be created for the resource
	ExposedExternally bool `json:"exposedExternally,omitempty"`

	// +kubebuilder:validation:Format="hostname"
	ClusterDomain  string `json:"clusterDomain,omitempty"`
	ConnectionSpec `json:",inline"`
	Persistent     *bool `json:"persistent,omitempty"`
	// +kubebuilder:validation:Enum=Standalone;ReplicaSet;ShardedCluster
	// +kubebuilder:validation:Required
	ResourceType ResourceType `json:"type"`
	Backup       *Backup      `json:"backup,omitempty"`

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

	// Amount of members for this MongoDB Replica Set
	Members int             `json:"members,omitempty"`
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`
	// +optional
	Security *Security `json:"security,omitempty"`

	Connectivity *MongoDBConnectivity `json:"connectivity,omitempty"`

	// AdditionalMongodConfig is additional configuration that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	AdditionalMongodConfig AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
}

func (s *MongoDbSpec) GetHorizonConfig() []MongoDBHorizonConfig {
	return s.Connectivity.ReplicaSetHorizons
}

func (s *MongoDbSpec) GetAdditionalMongodConfig() AdditionalMongodConfig {
	return s.AdditionalMongodConfig
}

// Backup contains configuration options for configuring
// backup for this MongoDB resource
type Backup struct {

	// +kubebuilder:validation:Enum=enabled;disabled;terminated
	// +optional
	Mode BackupMode `json:"mode"`

	// AutoTerminateOnDeletion indicates if the Operator should stop and terminate the Backup before the cleanup,
	// when the MongoDB CR is deleted
	// +optional
	AutoTerminateOnDeletion bool `json:"autoTerminateOnDeletion,omitempty"`
}

type AgentConfig struct {
	// +optional
	StartupParameters StartupParameters `json:"startupOptions"`
}

type StartupParameters map[string]string

func (s StartupParameters) ToCommandLineArgs() string {
	var keys []string
	for k := range s {
		keys = append(keys, k)
	}

	// order must be preserved to ensure the same set of command line arguments
	// results in the same StatefulSet template spec.
	sort.SliceStable(keys, func(i, j int) bool {
		return keys[i] < keys[j]
	})

	sb := strings.Builder{}
	for _, key := range keys {
		if value := s[key]; value != "" {
			sb.Write([]byte(fmt.Sprintf(" -%s=%s", key, value)))
		} else {
			sb.Write([]byte(fmt.Sprintf(" -%s", key)))
		}
	}
	return sb.String()
}

func (m *MongoDB) DesiredReplicas() int {
	return m.Spec.Members
}

func (m *MongoDB) CurrentReplicas() int {
	return m.Status.Members
}

// GetMongoDBVersion returns the version of the MongoDB.
func (ms MongoDbSpec) GetMongoDBVersion() string {
	return ms.Version
}

func (ms MongoDbSpec) GetClusterDomain() string {
	if ms.ClusterDomain != "" {
		return ms.ClusterDomain
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
	semverVersion, _ := semver.Make(m.GetMongoDBVersion())
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
	PublicAPIKey string

	// +required
	PrivateAPIKey string
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
	// +kubebuilder:validation:Required
	Credentials string `json:"credentials"`

	// Dev note: don't reference these two fields directly - use the `getProject` method instead

	OpsManagerConfig   *PrivateCloudConfig `json:"opsManager,omitempty"`
	CloudManagerConfig *PrivateCloudConfig `json:"cloudManager,omitempty"`

	// FIXME: LogLevel is not a required field for creating an Ops Manager connection, it should not be here.

	// +kubebuilder:validation:Enum=DEBUG;INFO;WARN;ERROR;FATAL
	LogLevel LogLevel `json:"logLevel,omitempty"`
}

type Security struct {
	TLSConfig      *TLSConfig      `json:"tls,omitempty"`
	Authentication *Authentication `json:"authentication,omitempty"`
	Roles          []MongoDbRole   `json:"roles,omitempty"`

	// +optional
	CertificatesSecretsPrefix string `json:"certsSecretPrefix"`
}

// MemberCertificateSecretName returns the name of the secret containing the member TLS certs.
func (s Security) MemberCertificateSecretName(defaultName string) string {
	// If one of the old fields tlsConfig.secretRef.name or tlsConfig.secretRef.prefix
	// are specified, they take precedence.
	if s.TLSConfig != nil {
		if s.TLSConfig.SecretRef.Name != "" {
			return s.TLSConfig.SecretRef.Name
		}
		if s.TLSConfig.SecretRef.Prefix != "" {
			return fmt.Sprintf("%s-%s-cert", s.TLSConfig.SecretRef.Prefix, defaultName)
		}
	}
	if s.CertificatesSecretsPrefix != "" {
		return fmt.Sprintf("%s-%s-cert", s.CertificatesSecretsPrefix, defaultName)
	}

	// The default behaviour is to use the `defaultname-cert` format
	return fmt.Sprintf("%s-cert", defaultName)
}

func (spec MongoDbSpec) GetSecurity() *Security {
	return spec.Security
}

func (s *Security) IsTLSEnabled() bool {
	if s == nil {
		return false
	}
	if s.TLSConfig != nil {
		if s.TLSConfig.Enabled || (s.TLSConfig.SecretRef.Prefix != "" || s.TLSConfig.SecretRef.Name != "") {
			return true
		}
	}
	return s.CertificatesSecretsPrefix != ""
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
func (s Security) AgentClientCertificateSecretName(resourceName string) corev1.SecretKeySelector {
	secretName := util.AgentSecretName

	if s.CertificatesSecretsPrefix != "" {
		secretName = fmt.Sprintf("%s-%s-%s", s.CertificatesSecretsPrefix, resourceName, util.AgentSecretName)
	}
	if s.ShouldUseClientCertificates() {
		secretName = s.Authentication.Agents.ClientCertificateSecretRefWrap.ClientCertificateSecretRef.Name
	}

	return corev1.SecretKeySelector{
		Key:                  util.AutomationAgentPemSecretKey,
		LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
	}
}

// The customer has set ClientCertificateSecretRef. This signals that client certs are required,
// even when no x509 agent-auth has been enabled.
func (s Security) ShouldUseClientCertificates() bool {
	return s.Authentication != nil && s.Authentication.Agents.ClientCertificateSecretRefWrap.ClientCertificateSecretRef.Name != ""
}

func (s Security) InternalClusterAuthSecretName(defaultName string) string {
	secretName := fmt.Sprintf("%s-clusterfile", defaultName)
	if s.CertificatesSecretsPrefix != "" {
		secretName = fmt.Sprintf("%s-%s", s.CertificatesSecretsPrefix, secretName)
	}
	return secretName
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

	// LDAP Configuration
	// +optional
	Ldap *Ldap `json:"ldap,omitempty"`

	// Agents contains authentication configuration properties for the agents
	// +optional
	Agents AgentAuthentication `json:"agents,omitempty"`

	// Clients should present valid TLS certificates
	RequiresClientTLSAuthentication bool `json:"requireClientTLSAuthentication,omitempty"`
}

type AuthenticationRestriction struct {
	ClientSource  []string `json:"clientSource,omitempty"`
	ServerAddress []string `json:"serverAddress,omitempty"`
}

type Resource struct {
	// +optional
	Db string `json:"db"`
	// +optional
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
	// +optional
	Privileges []Privilege     `json:"privileges"`
	Roles      []InheritedRole `json:"roles,omitempty"`
}

type AgentAuthentication struct {
	// Mode is the desired Authentication mode that the agents will use
	Mode string `json:"mode"`
	// +optional
	AutomationUserName string `json:"automationUserName"`
	// +optional
	AutomationPasswordSecretRef corev1.SecretKeySelector `json:"automationPasswordSecretRef"`
	// +optional
	AutomationLdapGroupDN string `json:"automationLdapGroupDN"`
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ClientCertificateSecretRefWrap ClientCertificateSecretRefWrapper `json:"clientCertificateSecretRef,omitempty"`
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
	// +optional
	Servers []string `json:"servers"`

	// +kubebuilder:validation:Enum=tls;none
	// +optional
	TransportSecurity *TransportSecurity `json:"transportSecurity"`
	// +optional
	ValidateLDAPServerConfig *bool `json:"validateLDAPServerConfig"`

	// Allows to point at a ConfigMap/key with a CA file to mount on the Pod
	CAConfigMapRef *corev1.ConfigMapKeySelector `json:"caConfigMapRef,omitempty"`

	// +optional
	BindQueryUser string `json:"bindQueryUser"`
	// +optional
	BindQuerySecretRef SecretRef `json:"bindQueryPasswordSecretRef"`
	// +optional
	AuthzQueryTemplate string `json:"authzQueryTemplate"`
	// +optional
	UserToDNMapping string `json:"userToDNMapping"`
}

type SecretRef struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

type TLSConfig struct {
	// DEPRECATED please enable TLS by setting `security.certsSecretPrefix` or `security.tls.secretRef.prefix`.
	// Enables TLS for this resource. This will make the operator try to mount a
	// Secret with a defined name (<resource-name>-cert).
	// This is only used when enabling TLS on a MongoDB resource, and not on the
	// AppDB, where TLS is configured by setting `secretRef.Name`.
	Enabled bool `json:"enabled,omitempty"`

	AdditionalCertificateDomains []string `json:"additionalCertificateDomains,omitempty"`

	// CA corresponds to a ConfigMap containing an entry for the CA certificate (ca.pem)
	// used to validate the certificates created already.
	CA string `json:"ca,omitempty"`
	// DEPRECATED please use security.certsSecretPrefix instead
	// SecretRef points to a Secret object containing the certificates to use when enabling TLS.
	// +optional
	SecretRef TLSSecretRef `json:"secretRef,omitempty"`
}

// IsSelfManaged returns true if the TLS is self-managed (cert provided by the customer), not Operator-managed
func (t TLSConfig) IsSelfManaged() bool {
	return t.CA != "" || (t.SecretRef.Prefix != "" || t.SecretRef.Name != "")
}

// TLSSecretRef contains a reference to a Secret object that contains certificates to
// be mounted. Defining this value will implicitly "enable" TLS on this resource.
type TLSSecretRef struct {
	// DEPRECATED please use security.certsSecretPrefix instead
	// +optional
	Name string `json:"name"`

	// TODO: make prefix required once name has been removed.

	// DEPRECATED please use security.certsSecretPrefix instead
	// +optional
	Prefix string `json:"prefix"`
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

// GetLastSpec returns the last spec that has successfully be applied.
func (m *MongoDB) GetLastSpec() (*MongoDbSpec, error) {
	lastSpecStr := annotations.GetAnnotation(m, util.LastAchievedSpec)
	if lastSpecStr == "" {
		return nil, nil
	}

	lastSpec := MongoDbSpec{}
	if err := json.Unmarshal([]byte(lastSpecStr), &lastSpec); err != nil {
		return nil, err
	}

	conf, err := getMapFromAnnotation(m, util.LastAchievedMongodAdditionalOptions)
	if err != nil {
		return nil, err
	}
	if conf != nil {
		lastSpec.AdditionalMongodConfig.Object = conf
	}

	conf, err = getMapFromAnnotation(m, util.LastAchievedMongodAdditionalMongosOptions)
	if err != nil {
		return nil, err
	}
	if conf != nil {
		lastSpec.MongosSpec.AdditionalMongodConfig.Object = conf
	}

	conf, err = getMapFromAnnotation(m, util.LastAchievedMongodAdditionalConfigServerOptions)
	if err != nil {
		return nil, err
	}
	if conf != nil {
		lastSpec.ConfigSrvSpec.AdditionalMongodConfig.Object = conf
	}

	conf, err = getMapFromAnnotation(m, util.LastAchievedMongodAdditionalShardOptions)
	if err != nil {
		return nil, err
	}
	if conf != nil {
		lastSpec.ShardSpec.AdditionalMongodConfig.Object = conf
	}
	return &lastSpec, nil
}

// getMapFromAnnotation returns the additional config map from a given annotation.
func getMapFromAnnotation(m client.Object, annotationKey string) (map[string]interface{}, error) {
	additionConfigStr := annotations.GetAnnotation(m, annotationKey)
	if additionConfigStr != "" {
		var conf map[string]interface{}
		if err := json.Unmarshal([]byte(additionConfigStr), &conf); err != nil {
			return nil, err
		}
		return conf, nil
	}
	return nil, nil
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
		if option, exists := status.GetOption(statusOptions, status.ReplicaSetMembersOption{}); exists {
			m.Status.Members = option.(status.ReplicaSetMembersOption).Members
		}
	case ShardedCluster:
		if option, exists := status.GetOption(statusOptions, status.ShardedClusterConfigServerOption{}); exists {
			m.Status.ConfigServerCount = option.(status.ShardedClusterConfigServerOption).Members
		}
		if option, exists := status.GetOption(statusOptions, status.ShardedClusterMongodsPerShardCountOption{}); exists {
			m.Status.MongodsPerShardCount = option.(status.ShardedClusterMongodsPerShardCountOption).Members
		}
		if option, exists := status.GetOption(statusOptions, status.ShardedClusterMongosOption{}); exists {
			m.Status.MongosCount = option.(status.ShardedClusterMongosOption).Members
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

// GetProject returns the name of the ConfigMap containing the information about connection to OM/CM, returns empty string if
// neither CloudManager not OpsManager configmap is set
func (c *ConnectionSpec) GetProject() string {
	// the contract is that either ops manager or cloud manager must be provided - the controller must validate this
	if c.OpsManagerConfig.ConfigMapRef.Name != "" {
		return c.OpsManagerConfig.ConfigMapRef.Name
	}
	if c.CloudManagerConfig.ConfigMapRef.Name != "" {
		return c.CloudManagerConfig.ConfigMapRef.Name
	}
	return ""
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
		if m.Spec.ConfigSrvSpec == nil {
			m.Spec.ConfigSrvSpec = &ShardedClusterComponentSpec{}
		}
		if m.Spec.MongosSpec == nil {
			m.Spec.MongosSpec = &ShardedClusterComponentSpec{}
		}
		if m.Spec.ShardSpec == nil {
			m.Spec.ShardSpec = &ShardedClusterComponentSpec{}
		}

	}

	if m.Spec.Connectivity == nil {
		m.Spec.Connectivity = newConnectivity()
	}

	m.Spec.Security = EnsureSecurity(m.Spec.Security)

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
	// +kubebuilder:pruning:PreserveUnknownFields
	PodAffinityWrapper PodAffinityWrapper `json:"podAffinity,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	NodeAffinityWrapper NodeAffinityWrapper `json:"nodeAffinity,omitempty"`

	// +kubebuilder:pruning:PreserveUnknownFields
	PodTemplateWrapper         PodTemplateSpecWrapper `json:"podTemplate,omitempty"`
	PodAntiAffinityTopologyKey string                 `json:"podAntiAffinityTopologyKey,omitempty"`

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
	Storage      string  `json:"storage,omitempty"`
	StorageClass *string `json:"storageClass,omitempty"`

	// +kubebuilder:pruning:PreserveUnknownFields
	LabelSelector *LabelSelectorWrapper `json:"labelSelector,omitempty"`
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

func (spec MongoDbSpec) GetTLSMode() tls.Mode {
	if spec.Security == nil || !spec.Security.IsTLSEnabled() {
		return tls.Disabled
	}
	return tls.GetTLSModeFromMongodConfig(spec.AdditionalMongodConfig.Object)
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

func (m MongoDbSpec) GetSecurityAuthenticationModes() []string {
	return m.GetSecurity().Authentication.GetModes()
}

func (m MongoDbSpec) GetResourceType() ResourceType {
	return m.ResourceType
}

func (m MongoDbSpec) IsSecurityTLSConfigEnabled() bool {
	return m.GetSecurity().IsTLSEnabled()
}

func (m MongoDbSpec) GetFeatureCompatibilityVersion() *string {
	return m.FeatureCompatibilityVersion
}

func newConnectivity() *MongoDBConnectivity {
	return &MongoDBConnectivity{}
}

// PrivateCloudConfig returns and empty `PrivateCloudConfig` object
func newOpsManagerConfig() *PrivateCloudConfig {
	return &PrivateCloudConfig{}
}

func EnsureSecurity(sec *Security) *Security {
	if sec == nil {
		sec = newSecurity()
	}
	if sec.TLSConfig == nil {
		sec.TLSConfig = &TLSConfig{}
	}
	if sec.Roles == nil {
		sec.Roles = make([]MongoDbRole, 0)
	}
	return sec
}

func newAuthentication() *Authentication {
	return &Authentication{Modes: []string{}}
}

func newSecurity() *Security {
	return &Security{TLSConfig: &TLSConfig{}}
}

// BuildConnectionString returns a string with a connection string for this resource.
func (m MongoDB) BuildConnectionString(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string) string {
	name := m.Name
	if m.Spec.ResourceType == ShardedCluster {
		name = m.MongosRsName()
	}
	builder := connectionstring.Builder().
		SetName(name).
		SetNamespace(m.Namespace).
		SetUsername(username).
		SetPassword(password).
		SetReplicas(m.Spec.Replicas()).
		SetService(m.ServiceName()).
		SetVersion(m.Spec.GetMongoDBVersion()).
		SetAuthenticationModes(m.Spec.GetSecurityAuthenticationModes()).
		SetClusterDomain(m.Spec.GetClusterDomain()).
		SetIsReplicaSet(m.Spec.ResourceType == ReplicaSet).
		SetIsTLSEnabled(m.Spec.IsSecurityTLSConfigEnabled()).
		SetConnectionParams(connectionParams).
		SetScheme(scheme)

	return builder.Build()
}
