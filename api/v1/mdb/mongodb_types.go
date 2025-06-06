package mdb

import (
	"encoding/json"
	"fmt"
	"github.com/blang/semver"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/fcv"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
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

	ClusterTopologySingleCluster = "SingleCluster"
	ClusterTopologyMultiCluster  = "MultiCluster"

	OIDCAuthorizationTypeGroupMembership = "GroupMembership"
	OIDCAuthorizationTypeUserID          = "UserID"

	OIDCAuthorizationMethodWorkforceIdentityFederation = "WorkforceIdentityFederation"
	OIDCAuthorizationMethodWorkloadIdentityFederation  = "WorkloadIdentityFederation"

	LabelResourceOwner = "mongodb.com/v1.mongodbResourceOwner"
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

func (m *MongoDB) IsAgentImageOverridden() bool {
	if m.Spec.PodSpec.IsAgentImageOverridden() {
		return true
	}

	if m.Spec.ShardPodSpec != nil && m.Spec.ShardPodSpec.IsAgentImageOverridden() {
		return true
	}

	if m.Spec.IsAgentImageOverridden() {
		return true
	}

	return false
}

func isAgentImageOverriden(containers []corev1.Container) bool {
	for _, c := range containers {
		if c.Name == util.AgentContainerName && c.Image != "" {
			return true
		}
	}
	return false
}

func (m *MongoDB) ForcedIndividualScaling() bool {
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

func (m *MongoDB) GetConnectionSpec() *ConnectionSpec {
	return &m.Spec.ConnectionSpec
}

func (m *MongoDB) GetPrometheus() *mdbcv1.Prometheus {
	return m.Spec.Prometheus
}

func (m *MongoDB) GetBackupSpec() *Backup {
	return m.Spec.Backup
}

func (m *MongoDB) GetResourceType() ResourceType {
	return m.Spec.ResourceType
}

func (m *MongoDB) IsShardedCluster() bool {
	return m.GetResourceType() == ShardedCluster
}

func (m *MongoDB) GetResourceName() string {
	return m.Name
}

func (m *MongoDB) GetOwnerLabels() map[string]string {
	return map[string]string{
		util.OperatorLabelName: util.OperatorLabelValue,
		LabelResourceOwner:     m.Name,
	}
}

// GetSecretsMountedIntoDBPod returns a list of all the optional secret names that are used by this resource.
func (m *MongoDB) GetSecretsMountedIntoDBPod() []string {
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

func (m *MongoDB) GetHostNameOverrideConfigmapName() string {
	return fmt.Sprintf("%s-hostname-override", m.Name)
}

type AdditionalMongodConfigType int

const (
	StandaloneConfig = iota
	ReplicaSetConfig
	MongosConfig
	ConfigServerConfig
	ShardConfig
)

func GetLastAdditionalMongodConfigByType(lastSpec *MongoDbSpec, configType AdditionalMongodConfigType) (*AdditionalMongodConfig, error) {
	if lastSpec == nil {
		return &AdditionalMongodConfig{}, nil
	}

	switch configType {
	case ReplicaSetConfig, StandaloneConfig:
		return lastSpec.GetAdditionalMongodConfig(), nil
	case MongosConfig:
		return lastSpec.MongosSpec.GetAdditionalMongodConfig(), nil
	case ConfigServerConfig:
		return lastSpec.ConfigSrvSpec.GetAdditionalMongodConfig(), nil
	case ShardConfig:
		return lastSpec.ShardSpec.GetAdditionalMongodConfig(), nil
	}
	return &AdditionalMongodConfig{}, nil
}

// GetLastAdditionalMongodConfigByType returns the last successfully achieved AdditionalMongodConfigType for the given component.
func (m *MongoDB) GetLastAdditionalMongodConfigByType(configType AdditionalMongodConfigType) (*AdditionalMongodConfig, error) {
	if m.Spec.GetResourceType() == ShardedCluster {
		panic(errors.Errorf("this method cannot be used from ShardedCluster controller; use non-method GetLastAdditionalMongodConfigByType and pass lastSpec from the deployment state."))
	}
	lastSpec, err := m.GetLastSpec()
	if err != nil || lastSpec == nil {
		return &AdditionalMongodConfig{}, err
	}
	return GetLastAdditionalMongodConfigByType(lastSpec, configType)
}

type ClusterSpecList []ClusterSpecItem

// ClusterSpecItem is the mongodb multi-cluster spec that is specific to a
// particular Kubernetes cluster, this maps to the statefulset created in each cluster
type ClusterSpecItem struct {
	// ClusterName is name of the cluster where the MongoDB Statefulset will be scheduled, the
	// name should have a one on one mapping with the service-account created in the central cluster
	// to talk to the workload clusters.
	ClusterName string `json:"clusterName,omitempty"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`
	// ExternalAccessConfiguration provides external access configuration for Multi-Cluster.
	// +optional
	ExternalAccessConfiguration *ExternalAccessConfiguration `json:"externalAccess,omitempty"`
	// Amount of members for this MongoDB Replica Set
	Members int `json:"members"`
	// MemberConfig allows to specify votes, priorities and tags for each of the mongodb process.
	// +optional
	MemberConfig []automationconfig.MemberOptions `json:"memberConfig,omitempty"`
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// +optional
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`
}

// ClusterSpecItemOverride is almost exact copy of ClusterSpecItem object.
// The object is used in ClusterSpecList in ShardedClusterComponentOverrideSpec in shard overrides.
// The difference lies in some fields being optional, e.g. Members to make it possible to NOT override fields and rely on
// what was set in top level shard configuration.
type ClusterSpecItemOverride struct {
	// ClusterName is name of the cluster where the MongoDB Statefulset will be scheduled, the
	// name should have a one on one mapping with the service-account created in the central cluster
	// to talk to the workload clusters.
	ClusterName string `json:"clusterName,omitempty"`
	// Amount of members for this MongoDB Replica Set
	// +optional
	Members *int `json:"members"`
	// MemberConfig allows to specify votes, priorities and tags for each of the mongodb process.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	MemberConfig []automationconfig.MemberOptions `json:"memberConfig,omitempty"`
	// +optional
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`
	// +optional
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`
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
	GetHorizonConfig() []MongoDBHorizonConfig
	GetAdditionalMongodConfig() *AdditionalMongodConfig
	GetExternalDomain() *string
	GetMemberOptions() []automationconfig.MemberOptions
	GetAgentConfig() AgentConfig
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDB `json:"items"`
}

type MongoDBHorizonConfig map[string]string

type MongoDBConnectivity struct {
	// ReplicaSetHorizons holds list of maps of horizons to be configured in each of MongoDB processes.
	// Horizons map horizon names to the node addresses for each process in the replicaset, e.g.:
	//  [
	//    {
	//      "internal": "my-rs-0.my-internal-domain.com:31843",
	//      "external": "my-rs-0.my-external-domain.com:21467"
	//    },
	//    {
	//      "internal": "my-rs-1.my-internal-domain.com:31843",
	//      "external": "my-rs-1.my-external-domain.com:21467"
	//    },
	//    ...
	//  ]
	// The key of each item in the map is an arbitrary, user-chosen string that
	// represents the name of the horizon. The value of the item is the host and,
	// optionally, the port that this mongod node will be connected to from.
	// +optional
	ReplicaSetHorizons []MongoDBHorizonConfig `json:"replicaSetHorizons,omitempty"`
}

type MongoDbStatus struct {
	status.Common                          `json:",inline"`
	BackupStatus                           *BackupStatus `json:"backup,omitempty"`
	status.MongodbShardedClusterSizeConfig `json:",inline"`
	SizeStatusInClusters                   *status.MongodbShardedSizeStatusInClusters `json:"sizeStatusInClusters,omitempty"`
	Members                                int                                        `json:"members,omitempty"`
	Version                                string                                     `json:"version"`
	Link                                   string                                     `json:"link,omitempty"`
	FeatureCompatibilityVersion            string                                     `json:"featureCompatibilityVersion,omitempty"`
	Warnings                               []status.Warning                           `json:"warnings,omitempty"`
}

type DbCommonSpec struct {
	// +kubebuilder:validation:Pattern=^[0-9]+.[0-9]+.[0-9]+(-.+)?$|^$
	// +kubebuilder:validation:Required
	Version                     string      `json:"version"`
	FeatureCompatibilityVersion *string     `json:"featureCompatibilityVersion,omitempty"`
	Agent                       AgentConfig `json:"agent,omitempty"`
	// +kubebuilder:validation:Format="hostname"
	ClusterDomain  string `json:"clusterDomain,omitempty"`
	ConnectionSpec `json:",inline"`

	// +kubebuilder:validation:Enum=DEBUG;INFO;WARN;ERROR;FATAL
	LogLevel LogLevel `json:"logLevel,omitempty"`
	// ExternalAccessConfiguration provides external access configuration.
	// +optional
	ExternalAccessConfiguration *ExternalAccessConfiguration `json:"externalAccess,omitempty"`

	Persistent *bool `json:"persistent,omitempty"`
	// +kubebuilder:validation:Enum=Standalone;ReplicaSet;ShardedCluster
	// +kubebuilder:validation:Required
	ResourceType ResourceType `json:"type"`
	// +optional
	Security     *Security            `json:"security,omitempty"`
	Connectivity *MongoDBConnectivity `json:"connectivity,omitempty"`
	Backup       *Backup              `json:"backup,omitempty"`

	// Prometheus configurations.
	// +optional
	Prometheus *mdbcv1.Prometheus `json:"prometheus,omitempty"`

	// +optional
	// StatefulSetConfiguration provides the statefulset override for each of the cluster's statefulset
	// if "StatefulSetConfiguration" is specified at cluster level under "clusterSpecList" that takes precedence over
	// the global one
	StatefulSetConfiguration *common.StatefulSetConfiguration `json:"statefulSet,omitempty"`

	// AdditionalMongodConfig is additional configuration that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	AdditionalMongodConfig *AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`

	// In few service mesh options for ex: Istio, by default we would need to duplicate the
	// service objects created per pod in all the clusters to enable DNS resolution. Users can
	// however configure their ServiceMesh with DNS proxy(https://istio.io/latest/docs/ops/configuration/traffic-management/dns-proxy/)
	// enabled in which case the operator doesn't need to create the service objects per cluster. This options tells the operator
	// whether it should create the service objects in all the clusters or not. By default, if not specified the operator would create the duplicate svc objects.
	// +optional
	DuplicateServiceObjects *bool `json:"duplicateServiceObjects,omitempty"`

	// Topology sets the desired cluster topology of MongoDB resources
	// It defaults (if empty or not set) to SingleCluster. If MultiCluster specified,
	// then clusterSpecList field is mandatory and at least one member cluster has to be specified.
	// +kubebuilder:validation:Enum=SingleCluster;MultiCluster
	// +optional
	Topology string `json:"topology,omitempty"`
}

type MongoDbSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	DbCommonSpec                           `json:",inline"`
	ShardedClusterSpec                     `json:",inline"`
	status.MongodbShardedClusterSizeConfig `json:",inline"`

	// Amount of members for this MongoDB Replica Set
	Members int             `json:"members,omitempty"`
	PodSpec *MongoDbPodSpec `json:"podSpec,omitempty"`
	// DEPRECATED please use `spec.statefulSet.spec.serviceName` to provide a custom service name.
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`

	// MemberConfig allows to specify votes, priorities and tags for each of the mongodb process.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	MemberConfig []automationconfig.MemberOptions `json:"memberConfig,omitempty"`
}

func (m *MongoDbSpec) GetExternalDomain() *string {
	if m.ExternalAccessConfiguration != nil {
		return m.ExternalAccessConfiguration.ExternalDomain
	}
	return nil
}

func (m *MongoDbSpec) GetHorizonConfig() []MongoDBHorizonConfig {
	return m.Connectivity.ReplicaSetHorizons
}

func (m *MongoDbSpec) GetMemberOptions() []automationconfig.MemberOptions {
	return m.MemberConfig
}

func (m *MongoDB) DesiredReplicas() int {
	return m.Spec.Members
}

func (m *MongoDB) CurrentReplicas() int {
	return m.Status.Members
}

// GetMongoDBVersion returns the version of the MongoDB.
func (m *MongoDbSpec) GetMongoDBVersion() string {
	return m.Version
}

func (m *MongoDbSpec) GetClusterDomain() string {
	if m.ClusterDomain != "" {
		return m.ClusterDomain
	}
	return "cluster.local"
}

func (m *MongoDbSpec) MinimumMajorVersion() uint64 {
	if m.FeatureCompatibilityVersion != nil && *m.FeatureCompatibilityVersion != "" {
		fcv := *m.FeatureCompatibilityVersion

		// ignore errors here as the format of FCV/version is handled by CRD validation
		semverFcv, _ := semver.Make(fmt.Sprintf("%s.0", fcv))
		return semverFcv.Major
	}
	semverVersion, _ := semver.Make(m.GetMongoDBVersion())
	return semverVersion.Major
}

// ProjectConfig contains the configuration expected from the `project` (ConfigMap) under Data.
type ProjectConfig struct {
	BaseURL     string
	ProjectName string
	OrgID       string
	Credentials string
	UseCustomCA bool
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
	SharedConnectionSpec `json:",inline"`
	// Name of the Secret holding credentials information
	// +kubebuilder:validation:Required
	Credentials string `json:"credentials"`
}

type SharedConnectionSpec struct {
	// Transient field - the name of the project. By default, is equal to the name of the resource
	// though can be overridden if the ConfigMap specifies a different name
	ProjectName string `json:"-"` // ignore when marshalling

	// Dev note: don't reference these two fields directly - use the `getProject` method instead

	OpsManagerConfig   *PrivateCloudConfig `json:"opsManager,omitempty"`
	CloudManagerConfig *PrivateCloudConfig `json:"cloudManager,omitempty"`
}

func (d *DbCommonSpec) IsAgentImageOverridden() bool {
	if d.StatefulSetConfiguration != nil && isAgentImageOverriden(d.StatefulSetConfiguration.SpecWrapper.Spec.Template.Spec.Containers) {
		return true
	}

	return false
}

func (d *DbCommonSpec) GetExternalDomain() *string {
	if d.ExternalAccessConfiguration != nil {
		return d.ExternalAccessConfiguration.ExternalDomain
	}
	return nil
}

func (d DbCommonSpec) GetAgentConfig() AgentConfig {
	return d.Agent
}

func (d *DbCommonSpec) GetAdditionalMongodConfig() *AdditionalMongodConfig {
	if d == nil || d.AdditionalMongodConfig == nil {
		return &AdditionalMongodConfig{}
	}

	return d.AdditionalMongodConfig
}

// UnmarshalJSON when unmarshalling a MongoDB instance, we don't want to have any nil references
// these are replaced with an empty instance to prevent nil references by calling InitDefaults
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

	return &lastSpec, nil
}

func (m *MongoDB) ServiceName() string {
	if m.Spec.StatefulSetConfiguration != nil {
		svc := m.Spec.StatefulSetConfiguration.SpecWrapper.Spec.ServiceName

		if svc != "" {
			return svc
		}
	}

	if m.Spec.Service == "" {
		return dns.GetServiceName(m.GetName())
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

func (m *MongoDB) MultiShardRsName(clusterIdx int, shardIdx int) string {
	return fmt.Sprintf("%s-%d-%d", m.Name, shardIdx, clusterIdx)
}

func (m *MongoDB) MultiMongosRsName(clusterIdx int) string {
	return fmt.Sprintf("%s-mongos-%d", m.Name, clusterIdx)
}

func (m *MongoDB) MultiConfigRsName(clusterIdx int) string {
	return fmt.Sprintf("%s-config-%d", m.Name, clusterIdx)
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
		if option, exists := status.GetOption(statusOptions, status.ShardedClusterSizeConfigOption{}); exists {
			if sizeConfig := option.(status.ShardedClusterSizeConfigOption).SizeConfig; sizeConfig != nil {
				m.Status.MongodbShardedClusterSizeConfig = *sizeConfig
			}
		}
		if option, exists := status.GetOption(statusOptions, status.ShardedClusterSizeStatusInClustersOption{}); exists {
			if sizeConfigInClusters := option.(status.ShardedClusterSizeStatusInClustersOption).SizeConfigInClusters; sizeConfigInClusters != nil {
				m.Status.SizeStatusInClusters = sizeConfigInClusters
			}
		}
	}

	if phase == status.PhaseRunning {
		m.Status.Version = m.Spec.Version
		m.Status.FeatureCompatibilityVersion = m.CalculateFeatureCompatibilityVersion()
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

func (m *MongoDB) GetStatus(...status.Option) interface{} {
	return m.Status
}

func (m *MongoDB) GetStatusWarnings() []status.Warning {
	return m.Status.Warnings
}

func (m *MongoDB) GetCommonStatus(...status.Option) *status.Common {
	return &m.Status.Common
}

func (m *MongoDB) GetPhase() status.Phase {
	return m.Status.Phase
}

func (m *MongoDB) GetStatusPath(...status.Option) string {
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
		m.Spec.Connectivity = NewConnectivity()
	}

	m.Spec.Security = EnsureSecurity(m.Spec.Security)

	if m.Spec.OpsManagerConfig == nil {
		m.Spec.OpsManagerConfig = NewOpsManagerConfig()
	}

	if m.Spec.CloudManagerConfig == nil {
		m.Spec.CloudManagerConfig = NewOpsManagerConfig()
	}

	// ProjectName defaults to the name of the resource
	m.Spec.ProjectName = m.Name
}

func (m *MongoDB) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(m.Namespace, m.Name)
}

// ExternalAccessConfiguration holds the custom Service override that will be merged into the operator created one.
type ExternalAccessConfiguration struct {
	// Provides a way to override the default (NodePort) Service
	// +optional
	ExternalService ExternalServiceConfiguration `json:"externalService,omitempty"`

	// An external domain that is used for exposing MongoDB to the outside world.
	// +optional
	ExternalDomain *string `json:"externalDomain,omitempty"`
}

// ExternalServiceConfiguration is a wrapper for the Service spec object.
type ExternalServiceConfiguration struct {
	// A wrapper for the Service spec object.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	SpecWrapper *common.ServiceSpecWrapper `json:"spec"`

	// A map of annotations that shall be added to the externally available Service.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

type MongoDbPodSpec struct {
	ContainerResourceRequirements `json:"-"`

	// +kubebuilder:pruning:PreserveUnknownFields
	PodTemplateWrapper common.PodTemplateSpecWrapper `json:"podTemplate,omitempty"`
	// Note, this field is not serialized in the CRD, it's only present here because of the
	// way we currently set defaults for this field in the operator, similar to "ContainerResourceRequirements"

	PodAntiAffinityTopologyKey string `json:"-"`

	// Note, that this field is used by MongoDB resources only, let's keep it here for simplicity
	Persistence *common.Persistence `json:"persistence,omitempty"`
}

func (m *MongoDbPodSpec) IsAgentImageOverridden() bool {
	if m.PodTemplateWrapper.PodTemplate != nil && isAgentImageOverriden(m.PodTemplateWrapper.PodTemplate.Spec.Containers) {
		return true
	}
	return false
}

type ContainerResourceRequirements struct {
	CpuLimit       string
	CpuRequests    string
	MemoryLimit    string
	MemoryRequests string
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

func (p PodSpecWrapper) GetCpuOrDefault() string {
	if p.CpuLimit == "" && p.CpuRequests == "" {
		return p.Default.CpuLimit
	}
	return p.CpuLimit
}

func (p PodSpecWrapper) GetMemoryOrDefault() string {
	// We don't set default if either Memory requests or Memory limits are specified by the User
	if p.MemoryLimit == "" && p.MemoryRequests == "" {
		return p.Default.MemoryLimit
	}
	return p.MemoryLimit
}

func (p PodSpecWrapper) GetCpuRequestsOrDefault() string {
	if p.CpuRequests == "" && p.CpuLimit == "" {
		return p.Default.CpuRequests
	}
	return p.CpuRequests
}

func (p PodSpecWrapper) GetMemoryRequestsOrDefault() string {
	// We don't set default if either Memory requests or Memory limits are specified by the User
	// otherwise it's possible to get failed Statefulset (e.g. the user specified limits of 200M but we default
	// requests to 500M though requests must be less than limits)
	if p.MemoryRequests == "" && p.MemoryLimit == "" {
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
	p.CpuLimit = cpu
	return p
}

func (p PodSpecWrapper) SetMemory(memory string) PodSpecWrapper {
	p.MemoryLimit = memory
	return p
}

func (p PodSpecWrapper) SetTopology(topology string) PodSpecWrapper {
	p.PodAntiAffinityTopologyKey = topology
	return p
}

func GetStorageOrDefault(config *common.PersistenceConfig, defaultConfig common.PersistenceConfig) string {
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

// Replicas returns the number of "user facing" replicas of the MongoDB resource. This method can be used for
// constructing the mongodb URL for example.
// 'Members' would be a more consistent function but go doesn't allow to have the same
func (m *MongoDbSpec) Replicas() int {
	var replicasCount int
	switch m.ResourceType {
	case Standalone:
		replicasCount = 1
	case ReplicaSet:
		replicasCount = m.Members
	case ShardedCluster:
		replicasCount = m.MongosCount
	default:
		panic("Unknown type of resource!")
	}
	return replicasCount
}

func (m *MongoDbSpec) GetResourceType() ResourceType {
	return m.ResourceType
}

func (m *MongoDbSpec) GetFeatureCompatibilityVersion() *string {
	return m.FeatureCompatibilityVersion
}

func NewConnectivity() *MongoDBConnectivity {
	return &MongoDBConnectivity{}
}

// PrivateCloudConfig returns and empty `PrivateCloudConfig` object
func NewOpsManagerConfig() *PrivateCloudConfig {
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
	return &Authentication{Modes: []AuthMode{}}
}

func newSecurity() *Security {
	return &Security{TLSConfig: &TLSConfig{}}
}

// BuildConnectionString returns a string with a connection string for this resource.
func (m *MongoDB) BuildConnectionString(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string) string {
	builder := NewMongoDBConnectionStringBuilder(*m, nil)
	return builder.BuildConnectionString(username, password, scheme, connectionParams)
}

func (m *MongoDB) GetAuthenticationModes() []string {
	return m.Spec.Security.Authentication.GetModes()
}

func (m *MongoDB) CalculateFeatureCompatibilityVersion() string {
	return fcv.CalculateFeatureCompatibilityVersion(m.Spec.Version, m.Status.FeatureCompatibilityVersion, m.Spec.FeatureCompatibilityVersion)
}

func (m *MongoDbSpec) IsInChangeVersion(lastSpec *MongoDbSpec) bool {
	if lastSpec != nil && (lastSpec.Version != m.Version) {
		return true
	}
	return false
}

func (m *MongoDbSpec) GetTopology() string {
	if m.Topology == "" {
		return ClusterTopologySingleCluster
	}
	return m.Topology
}

func (m *MongoDbSpec) IsMultiCluster() bool {
	return m.GetTopology() == ClusterTopologyMultiCluster
}

type MongoDBConnectionStringBuilder struct {
	MongoDB
	hostnames []string
}

// NewMongoDBConnectionStringBuilder creates a new instance of MongoDBConnectionStringBuilder.
// Parameters:
//   - mdb: The MongoDB resource object containing the configuration and metadata for the MongoDB instance.
//   - hostnames: A slice of strings representing the hostnames to be included in the connection string,
//     if this parameter is passed then no other hostnames will be generated or used.
func NewMongoDBConnectionStringBuilder(mdb MongoDB, hostnames []string) *MongoDBConnectionStringBuilder {
	return &MongoDBConnectionStringBuilder{
		MongoDB:   mdb,
		hostnames: hostnames,
	}
}

func (m *MongoDBConnectionStringBuilder) BuildConnectionString(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string) string {
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
		SetPort(m.Spec.GetAdditionalMongodConfig().GetPortOrDefault()).
		SetVersion(m.Spec.GetMongoDBVersion()).
		SetAuthenticationModes(m.Spec.GetSecurityAuthenticationModes()).
		SetClusterDomain(m.Spec.GetClusterDomain()).
		SetExternalDomain(m.Spec.GetExternalDomain()).
		SetIsReplicaSet(m.Spec.ResourceType == ReplicaSet).
		SetIsTLSEnabled(m.Spec.IsSecurityTLSConfigEnabled()).
		SetConnectionParams(connectionParams).
		SetScheme(scheme).
		SetHostnames(m.hostnames)

	return builder.Build()
}

// MongodbCleanUpOptions implements the required interface to be passed
// to the DeleteAllOf function, this cleans up resources of a given type with
// the provided Labels in a specific Namespace.
type MongodbCleanUpOptions struct {
	Namespace string
	Labels    map[string]string
}

func (m *MongodbCleanUpOptions) ApplyToDeleteAllOf(opts *client.DeleteAllOfOptions) {
	opts.Namespace = m.Namespace
	opts.LabelSelector = labels.SelectorFromValidatedSet(m.Labels)
}

func (m ClusterSpecList) GetExternalAccessConfigurationForMemberCluster(clusterName string) *ExternalAccessConfiguration {
	for _, csl := range m {
		if csl.ClusterName == clusterName {
			return csl.ExternalAccessConfiguration
		}
	}

	return nil
}

func (m ClusterSpecList) GetExternalDomainForMemberCluster(clusterName string) *string {
	if cfg := m.GetExternalAccessConfigurationForMemberCluster(clusterName); cfg != nil {
		return cfg.ExternalDomain
	}

	return nil
}

func (m ClusterSpecList) IsExternalDomainSpecifiedInClusterSpecList() bool {
	for _, item := range m {
		externalAccess := item.ExternalAccessConfiguration
		if externalAccess != nil && externalAccess.ExternalDomain != nil {
			return true
		}
	}

	return false
}
