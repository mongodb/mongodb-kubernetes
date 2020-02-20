package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/blang/semver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func init() {
	SchemeBuilder.Register(&MongoDB{}, &MongoDBList{})
}

type LogLevel string
type Phase string

type ResourceType string

type SSLMode string

const (
	Debug LogLevel = "DEBUG"
	Info  LogLevel = "INFO"
	Warn  LogLevel = "WARN"
	Error LogLevel = "ERROR"
	Fatal LogLevel = "FATAL"

	// PhaseReconciling means the controller is in the middle of reconciliation process
	PhaseReconciling Phase = "Reconciling"

	// PhasePending means the reconciliation has finished but the resource is neither in Error nor Running state -
	// most of all waiting for some event to happen (CSRs approved, shard rebalanced etc)
	PhasePending Phase = "Pending"

	// PhaseRunning means the Mongodb Resource is in a running state
	PhaseRunning Phase = "Running"

	// PhaseFailed means the Mongodb Resource is in a failed state
	PhaseFailed Phase = "Failed"

	// PhaseUpdated means a MongoDBUser was successfully updated
	PhaseUpdated Phase = "Updated"

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
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
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
	MongodbShardedClusterSizeConfig
	Members        int             `json:"members,omitempty"`
	Version        string          `json:"version"`
	Phase          Phase           `json:"phase"`
	Message        string          `json:"message,omitempty"`
	Link           string          `json:"link,omitempty"`
	LastTransition string          `json:"lastTransition,omitempty"`
	ResourceType   ResourceType    `json:"type"`
	Warnings       []StatusWarning `json:"warnings,omitempty"`
}

type MongoDbSpec struct {
	Version                     string  `json:"version,omitempty"`
	FeatureCompatibilityVersion *string `json:"featureCompatibilityVersion,omitempty"`

	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`

	// ExposedExternally determines whether a NodePort service should be created for the resource
	ExposedExternally bool `json:"exposedExternally,omitempty"`

	// Deprecated: This has been replaced by the ClusterDomain which should be
	// used instead
	ClusterName   string `json:"clusterName,omitempty"`
	ClusterDomain string `json:"clusterDomain,omitempty"`
	ConnectionSpec
	Persistent   *bool        `json:"persistent,omitempty"`
	ResourceType ResourceType `json:"type,omitempty"`
	// sharded cluster
	ConfigSrvPodSpec *MongoDbPodSpec `json:"configSrvPodSpec,omitempty"`
	MongosPodSpec    *MongoDbPodSpec `json:"mongosPodSpec,omitempty"`
	ShardPodSpec     *MongoDbPodSpec `json:"shardPodSpec,omitempty"`
	MongodbShardedClusterSizeConfig

	// replica set
	Members int             `json:"members,omitempty"`
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`

	Security *Security `json:"security,omitempty"`

	Connectivity *MongoDBConnectivity `json:"connectivity,omitempty"`

	// AdditionalMongodConfig is additional configuration that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	AdditionalMongodConfig *AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
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

// SSLProjectConfig contains the configuration options that are relevant for MMS SSL configuraiton
type SSLProjectConfig struct {
	// This is set to true if baseUrl is HTTPS
	SSLRequireValidMMSServerCertificates bool

	// Name of a configmap containing a `mms-ca.crt` entry that will be mounted
	// on every Pod.
	SSLMMSCAConfigMap string

	// SSLMMSCAConfigMap will contain the CA cert, used to push multiple
	SSLMMSCAConfigMapContents string
}

// ProjectConfig contains the configuration expected from the `project` (ConfigMap) attribute in
// `.spec.project`.
type ProjectConfig struct {
	// +required
	BaseURL string
	// +required
	ProjectName string
	// +optional
	OrgID string
	// +optional
	Credentials string
	// +optional
	AuthMode string
	// +optional
	UseCustomCA bool
	// +optional
	SSLProjectConfig
}

func (ms *MongoDbSpec) SetParametersFromConfigMap(cm *ProjectConfig) {
	if cm.AuthMode == util.LegacyX509InConfigMapValue {
		ms.Security.Authentication.Enabled = true
		ms.Security.Authentication.Modes = []string{util.X509}
	}

	if ms.Credentials == "" {
		ms.Credentials = cm.Credentials
	}

	if cm.ProjectName != "" {
		ms.ProjectName = cm.ProjectName
	}
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
	ProjectName string `json:"-"` // ignore when marshalling
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

type AdditionalMongodConfig struct {
	Net NetSpec `json:"net"`
}

type NetSpec struct {
	SSL SSLSpec `json:"ssl"`
}

type SSLSpec struct {
	Mode SSLMode `json:"mode,omitempty"`

	// Below are parameters that may be useful but that haven't been tested with
	// the automation config so are commented out
	//AllowInvalidCertificates bool   `json:"allowInvalidCertificates,omitempty"`
	//AllowInvalidHostnames    bool   `json:"allowInvalidHostnames,omitempty"`
	//DisabledProtocols        string `json:"disabledProtocols,omitempty"`
	//FIPSMode                 bool   `json:"FIPSMode,omitempty"`

	// allowConnectionsWithoutCertificates has been omitted as it is not
	// respected by the automation agent.
}

type Security struct {
	TLSConfig      *TLSConfig      `json:"tls,omitempty"`
	Authentication *Authentication `json:"authentication,omitempty"`

	// Deprecated: This has been replaced by Authentication.InternalCluster which
	// should be used instead
	ClusterAuthMode string `json:"clusterAuthenticationMode,omitempty"`
}

// Authentication holds various authentication related settings that affect
// this MongoDB resource.
type Authentication struct {
	Enabled         bool     `json:"enabled"`
	Modes           []string `json:"modes"`
	InternalCluster string   `json:"internalCluster,omitempty"`
	// IgnoreUnknownUsers maps to the inverse of auth.authoritativeSet
	IgnoreUnknownUsers bool `json:"ignoreUnknownUsers,omitempty"`
}

// IsX509Enabled determines if X509 is to be enabled at the project level
// it does not necessarily mean that the agents are using X509 authentication
func (a *Authentication) IsX509Enabled() bool {
	return util.ContainsString(a.Modes, util.X509)
}

// GetAgentMechanism returns the authentication mechanism that the agents will be using.
// The agents will use X509 if it is the only mechanism specified, otherwise they will use SCRAM if specified
// and no auth if no mechanisms exist.
func (a *Authentication) GetAgentMechanism() string {
	if len(a.Modes) == 1 && a.Modes[0] == util.X509 {
		return util.X509
	} else if util.ContainsString(a.Modes, util.SCRAM) {
		return util.SCRAM
	}
	return ""
}

type TLSConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	AdditionalCertificateDomains []string `json:"additionalCertificateDomains,omitempty"`

	// CA corresponds to a Secret containing an entry for the CA certificate (ca.pem)
	// used to sign the certificates created already.
	// benelgar: should this read "used to validate"?
	CA string `json:"ca,omitempty"`
}

func (spec MongoDbSpec) GetTLSConfig() *TLSConfig {
	if spec.Security == nil || spec.Security.TLSConfig == nil {
		return &TLSConfig{}
	}

	return spec.Security.TLSConfig
}

// when we marshal a MongoDB, we don't want to marshal any "empty" fields
// by setting them to nil, they will be left out with `omitempty`
func (m *MongoDB) MarshalJSON() ([]byte, error) {
	type MongoDBJSON MongoDB

	mdb := m.DeepCopyObject().(*MongoDB) // prevent mutation of the original object
	if reflect.DeepEqual(m.Spec.PodSpec, newMongoDbPodSpec()) {
		mdb.Spec.PodSpec = nil
	}
	if mdb.Spec.ResourceType == ShardedCluster {
		if reflect.DeepEqual(m.Spec.ConfigSrvPodSpec, newMongoDbPodSpec()) {
			mdb.Spec.ConfigSrvPodSpec = nil
		}
		if reflect.DeepEqual(m.Spec.MongosPodSpec, newMongoDbPodSpec()) {
			mdb.Spec.MongosPodSpec = nil
		}
		if reflect.DeepEqual(m.Spec.ShardPodSpec, newMongoDbPodSpec()) {
			mdb.Spec.ShardPodSpec = nil
		}
	}

	if reflect.DeepEqual(mdb.Spec.AdditionalMongodConfig, newAdditionalMongodConfig()) {
		mdb.Spec.AdditionalMongodConfig = nil
	}

	if reflect.DeepEqual(mdb.Spec.OpsManagerConfig, newOpsManagerConfig()) {
		mdb.Spec.OpsManagerConfig = nil
	}

	if reflect.DeepEqual(mdb.Spec.CloudManagerConfig, newOpsManagerConfig()) {
		mdb.Spec.CloudManagerConfig = nil
	}

	if reflect.DeepEqual(mdb.Spec.Security, newSecurity()) || reflect.DeepEqual(mdb.Spec.Security, &Security{}) {
		mdb.Spec.Security = nil
	}

	if reflect.DeepEqual(mdb.Spec.Connectivity, newConnectivity()) {
		mdb.Spec.Connectivity = nil
	}

	return json.Marshal((MongoDBJSON)(*mdb))
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

// UpdateError called when the CR object (MongoDB resource) needs to transition to
// error state.
func (m *MongoDB) UpdateError(msg string) {
	m.Status.Message = msg
	m.Status.LastTransition = util.Now()
	m.Status.Phase = PhaseFailed
}

// UpdatePending called when the CR object (MongoDB resource) needs to transition to
// pending state.
func (m *MongoDB) UpdatePending(msg string, args ...string) {
	if msg != "" {
		m.Status.Message = msg
	}
	if m.Status.Phase != PhasePending {
		m.Status.LastTransition = util.Now()
		m.Status.Phase = PhasePending
	}
}

// UpdateReconciling called when the CR object (MongoDB resource) needs to transition to
// reconciling state.
func (m *MongoDB) UpdateReconciling() {
	m.Status.LastTransition = util.Now()
	m.Status.Phase = PhaseReconciling
}

// UpdateSuccessful called when the CR object (MongoDB resource) needs to transition to
// successful state. This means that the CR object and the underlying MongoDB deployment
// are ready to work
func (m *MongoDB) UpdateSuccessful(object runtime.Object, args ...string) {
	reconciledResource := object.(*MongoDB)
	spec := reconciledResource.Spec

	// assign all fields common to the different resource types
	if len(args) >= DeploymentLinkIndex {
		m.Status.Link = args[DeploymentLinkIndex]
	}
	m.Status.Version = spec.Version
	m.Status.Message = ""
	m.Status.LastTransition = util.Now()
	m.Status.Phase = PhaseRunning
	m.Status.ResourceType = spec.ResourceType

	m.Status.Warnings = reconciledResource.Status.Warnings

	switch spec.ResourceType {
	case ReplicaSet:
		m.Status.Members = spec.Members
	case ShardedCluster:
		m.Status.MongosCount = spec.MongosCount
		m.Status.MongodsPerShardCount = spec.MongodsPerShardCount
		m.Status.ConfigServerCount = spec.ConfigServerCount
		m.Status.ShardCount = spec.ShardCount
	}
}

func (m *MongoDB) SetWarnings(warnings []StatusWarning) {
	m.Status.Warnings = warnings
}

func (m *MongoDB) GetKind() string {
	return "MongoDB"
}

func (m *MongoDB) GetStatus() interface{} {
	return m.Status
}

func (m *MongoDB) GetSpec() interface{} {
	return m.Spec
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
		m.Spec.PodSpec = newMongoDbPodSpec()
	}

	if m.Spec.ResourceType == ShardedCluster {
		if m.Spec.ConfigSrvPodSpec == nil {
			m.Spec.ConfigSrvPodSpec = newMongoDbPodSpec()
		}
		if m.Spec.MongosPodSpec == nil {
			m.Spec.MongosPodSpec = newMongoDbPodSpec()
		}
		if m.Spec.ShardPodSpec == nil {
			m.Spec.ShardPodSpec = newMongoDbPodSpec()
		}
	}

	if m.Spec.Connectivity == nil {
		m.Spec.Connectivity = newConnectivity()
	}

	if m.Spec.AdditionalMongodConfig == nil {
		m.Spec.AdditionalMongodConfig = newAdditionalMongodConfig()
	}

	ensureSecurity(&m.Spec)

	if m.Spec.Security.Authentication.InternalCluster == "" {
		// old value was lowercase, new value is uppercase
		m.Spec.Security.Authentication.InternalCluster = strings.ToUpper(m.Spec.Security.ClusterAuthMode)
	}

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
	return client.ObjectKey{Name: m.Name, Namespace: m.Namespace}
}

// ConnectionURL returns connection url to the MongoDB based on its internal state. Username and password are
// provided as parameters as they need to be fetched by the caller
func (m *MongoDB) ConnectionURL(userName, password string, connectionParams map[string]string) string {
	statefulsetName := m.Name
	if m.Spec.ResourceType == ShardedCluster {
		statefulsetName = m.MongosRsName()
	}
	return buildConnectionUrl(statefulsetName, m.ServiceName(), m.Namespace, userName, password, m.Spec, connectionParams)
}

// MongodbShardedClusterSizeConfig describes the numbers and sizes of replica sets inside
// sharded cluster
type MongodbShardedClusterSizeConfig struct {
	ShardCount           int `json:"shardCount,omitempty"`
	MongodsPerShardCount int `json:"mongodsPerShardCount,omitempty"`
	MongosCount          int `json:"mongosCount,omitempty"`
	ConfigServerCount    int `json:"configServerCount,omitempty"`
}

type MongoDbPodSpec struct {
	Cpu                        string                     `json:"cpu,omitempty"`
	CpuRequests                string                     `json:"cpuRequests,omitempty"`
	Memory                     string                     `json:"memory,omitempty"`
	MemoryRequests             string                     `json:"memoryRequests,omitempty"`
	PodAffinity                *corev1.PodAffinity        `json:"podAffinity,omitempty"`
	NodeAffinity               *corev1.NodeAffinity       `json:"nodeAffinity,omitempty"`
	SecurityContext            *corev1.PodSecurityContext `json:"securityContext,omitempty"`
	PodTemplate                *corev1.PodTemplateSpec    `json:"podTemplate,omitempty"`
	PodAntiAffinityTopologyKey string                     `json:"podAntiAffinityTopologyKey,omitempty"`

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
	Data    PersistenceConfig `json:"data,omitempty"`
	Journal PersistenceConfig `json:"journal,omitempty"`
	Logs    PersistenceConfig `json:"logs,omitempty"`
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
func newMongoDbPodSpec() *MongoDbPodSpec {
	return &MongoDbPodSpec{}
}

func (spec MongoDbSpec) GetTLSMode() SSLMode {
	if spec.Security == nil || spec.Security.TLSConfig == nil || !spec.Security.TLSConfig.Enabled {
		return DisabledSSLMode
	}

	if spec.AdditionalMongodConfig == nil {
		return RequireSSLMode
	}

	return validModeOrDefault(spec.AdditionalMongodConfig.Net.SSL.Mode)
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

// Returns an empty `AdditionalMongodConfig` object for marshalling/unmarshalling
// json representation of the MDB object.
func newAdditionalMongodConfig() *AdditionalMongodConfig {
	return &AdditionalMongodConfig{}
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

	if spec.Security.Authentication == nil {
		spec.Security.Authentication = newAuthentication()
	}
}

func newAuthentication() *Authentication {
	return &Authentication{Modes: []string{}}
}

func newSecurity() *Security {
	return &Security{TLSConfig: &TLSConfig{}, Authentication: newAuthentication()}
}

func buildConnectionUrl(statefulsetName, serviceName, namespace, userName, password string, spec MongoDbSpec, connectionParams map[string]string) string {
	if util.ContainsString(spec.Security.Authentication.Modes, util.SCRAM) && (userName == "" || password == "") {
		panic("Dev error: UserName and Password must be specified if the resource has SCRAM-SHA enabled")
	}
	replicasCount := spec.Replicas()

	hostnames, _ := util.GetDNSNames(statefulsetName, serviceName, namespace, spec.GetClusterDomain(), replicasCount)
	uri := "mongodb://"
	if util.ContainsString(spec.Security.Authentication.Modes, util.SCRAM) {
		uri += fmt.Sprintf("%s:%s@", userName, password)
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
	if spec.Security.TLSConfig.Enabled {
		params["ssl"] = "true"
	}
	if util.ContainsString(spec.Security.Authentication.Modes, util.SCRAM) {
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
