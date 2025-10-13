package om

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/authentication/authtypes"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/constants"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

const (
	appDBKeyfilePath            = "/var/lib/mongodb-mms-automation/authentication/keyfile"
	ClusterTopologyMultiCluster = "MultiCluster"
)

type AppDBSpec struct {
	// +kubebuilder:validation:Pattern=^[0-9]+.[0-9]+.[0-9]+(-.+)?$|^$
	Version string `json:"version"`
	// Amount of members for this MongoDB Replica Set
	// +kubebuilder:validation:Maximum=50
	// +kubebuilder:validation:Minimum=3
	Members                     int                   `json:"members,omitempty"`
	PodSpec                     *mdbv1.MongoDbPodSpec `json:"podSpec,omitempty"`
	FeatureCompatibilityVersion *string               `json:"featureCompatibilityVersion,omitempty"`

	// +optional
	Security      *mdbv1.Security `json:"security,omitempty"`
	ClusterDomain string          `json:"clusterDomain,omitempty"`
	// +kubebuilder:validation:Enum=Standalone;ReplicaSet;ShardedCluster
	ResourceType mdbv1.ResourceType `json:"type,omitempty"`

	Connectivity *mdbv1.MongoDBConnectivity `json:"connectivity,omitempty"`

	// ExternalAccessConfiguration provides external access configuration.
	// +optional
	ExternalAccessConfiguration *mdbv1.ExternalAccessConfiguration `json:"externalAccess,omitempty"`

	// AdditionalMongodConfig are additional configurations that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	AdditionalMongodConfig *mdbv1.AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`

	// specify configuration like startup flags and automation config settings for the AutomationAgent and MonitoringAgent
	AutomationAgent mdbv1.AgentConfig `json:"agent,omitempty"`

	// Specify configuration like startup flags just for the MonitoringAgent.
	// These take precedence over
	// the flags set in AutomationAgent
	MonitoringAgent mdbv1.MonitoringAgentConfig `json:"monitoringAgent,omitempty"`
	ConnectionSpec  `json:",inline"`

	// PasswordSecretKeyRef contains a reference to the secret which contains the password
	// for the mongodb-ops-manager SCRAM-SHA user
	PasswordSecretKeyRef *userv1.SecretKeyRef `json:"passwordSecretKeyRef,omitempty"`

	// Enables Prometheus integration on the AppDB.
	Prometheus *mdbcv1.Prometheus `json:"prometheus,omitempty"`

	// Transient fields.
	// These fields are cleaned before serialization, see 'MarshalJSON()'
	// note, that we cannot include the 'OpsManager' instance here as this creates circular dependency and problems with
	// 'DeepCopy'

	OpsManagerName string `json:"-"`
	Namespace      string `json:"-"`
	// this is an optional service, it will get the name "<rsName>-svc" in case not provided
	Service string `json:"service,omitempty"`

	// AutomationConfigOverride holds any fields that will be merged on top of the Automation Config
	// that the operator creates for the AppDB. Currently only the process.disabled and logRotate field is recognized.
	AutomationConfigOverride *mdbcv1.AutomationConfigOverride `json:"automationConfig,omitempty"`

	UpdateStrategyType appsv1.StatefulSetUpdateStrategyType `json:"-"`

	// MemberConfig allows to specify votes, priorities and tags for each of the mongodb process.
	// +optional
	MemberConfig []automationconfig.MemberOptions `json:"memberConfig,omitempty"`

	// +kubebuilder:validation:Enum=SingleCluster;MultiCluster
	// +optional
	Topology string `json:"topology,omitempty"`
	// +optional
	ClusterSpecList mdbv1.ClusterSpecList `json:"clusterSpecList,omitempty"`
}

func (m *AppDBSpec) GetAgentConfig() mdbv1.AgentConfig {
	return m.AutomationAgent
}

func (m *AppDBSpec) GetAgentLogLevel() mdbcv1.LogLevel {
	agentLogLevel := mdbcv1.LogLevelInfo
	if m.AutomationAgent.LogLevel != "" {
		agentLogLevel = mdbcv1.LogLevel(m.AutomationAgent.LogLevel)
	}
	return agentLogLevel
}

func (m *AppDBSpec) GetAgentMaxLogFileDurationHours() int {
	agentMaxLogFileDurationHours := automationconfig.DefaultAgentMaxLogFileDurationHours
	if m.AutomationAgent.MaxLogFileDurationHours != 0 {
		agentMaxLogFileDurationHours = m.AutomationAgent.MaxLogFileDurationHours
	}
	return agentMaxLogFileDurationHours
}

// ObjectKey returns the client.ObjectKey with m.OpsManagerName because the name is used to identify the object to enqueue and reconcile.
func (m *AppDBSpec) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(m.Namespace, m.OpsManagerName)
}

func (m *AppDBSpec) GetOwnerLabels() map[string]string {
	return map[string]string{
		util.OperatorLabelName: util.OperatorLabelValue,
		LabelResourceOwner:     m.OpsManagerName,
	}
}

// GetConnectionSpec returns nil because no connection spec for appDB is implemented for the watcher setup
func (m *AppDBSpec) GetConnectionSpec() *mdbv1.ConnectionSpec {
	return nil
}

func (m *AppDBSpec) GetMongodConfiguration() mdbcv1.MongodConfiguration {
	mongodConfig := mdbcv1.NewMongodConfiguration()
	if m.GetAdditionalMongodConfig() == nil || m.AdditionalMongodConfig.ToMap() == nil {
		return mongodConfig
	}
	for k, v := range m.AdditionalMongodConfig.ToMap() {
		mongodConfig.SetOption(k, v)
	}
	return mongodConfig
}

func (m *AppDBSpec) GetHorizonConfig() []mdbv1.MongoDBHorizonConfig {
	return nil // no horizon support for AppDB currently
}

func (m *AppDBSpec) GetAdditionalMongodConfig() *mdbv1.AdditionalMongodConfig {
	if m.AdditionalMongodConfig != nil {
		return m.AdditionalMongodConfig
	}
	return &mdbv1.AdditionalMongodConfig{}
}

func (m *AppDBSpec) GetMemberOptions() []automationconfig.MemberOptions {
	return m.MemberConfig
}

// GetAgentPasswordSecretNamespacedName returns the NamespacedName for the secret
// which contains the Automation Agent's password.
func (m *AppDBSpec) GetAgentPasswordSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: m.Namespace,
		Name:      m.Name() + "-agent-password",
	}
}

// GetAgentKeyfileSecretNamespacedName returns the NamespacedName for the secret
// which contains the keyfile.
func (m *AppDBSpec) GetAgentKeyfileSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: m.Namespace,
		Name:      m.Name() + "-keyfile",
	}
}

// GetAuthOptions returns a set of Options which is used to configure Scram Sha authentication
// in the AppDB.
func (m *AppDBSpec) GetAuthOptions() authtypes.Options {
	return authtypes.Options{
		AuthoritativeSet: false,
		KeyFile:          appDBKeyfilePath,
		AuthMechanisms: []string{
			constants.Sha256,
			constants.Sha1,
		},
		AgentName:         util.AutomationAgentName,
		AutoAuthMechanism: constants.Sha1,
	}
}

// GetAuthUsers returns a list of all scram users for this deployment.
// in this case it is just the Ops Manager user for the AppDB.
func (m *AppDBSpec) GetAuthUsers() []authtypes.User {
	passwordSecretName := m.GetOpsManagerUserPasswordSecretName()
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Name != "" {
		passwordSecretName = m.PasswordSecretKeyRef.Name
	}
	return []authtypes.User{
		{
			Username: util.OpsManagerMongoDBUserName,
			Database: util.DefaultUserDatabase,
			// required roles for the AppDB user are outlined in the documentation
			// https://docs.opsmanager.mongodb.com/current/tutorial/prepare-backing-mongodb-instances/#replica-set-security
			Roles: []authtypes.Role{
				{
					Name:     "readWriteAnyDatabase",
					Database: "admin",
				},
				{
					Name:     "dbAdminAnyDatabase",
					Database: "admin",
				},
				{
					Name:     "clusterMonitor",
					Database: "admin",
				},
				// Enables backup and restoration roles
				// https://docs.mongodb.com/manual/reference/built-in-roles/#backup-and-restoration-roles
				{
					Name:     "backup",
					Database: "admin",
				},
				{
					Name:     "restore",
					Database: "admin",
				},
				// Allows user to do db.fsyncLock required by CLOUDP-78890
				// https://docs.mongodb.com/manual/reference/built-in-roles/#hostManager
				{
					Name:     "hostManager",
					Database: "admin",
				},
			},
			PasswordSecretKey:          m.GetOpsManagerUserPasswordSecretKey(),
			PasswordSecretName:         passwordSecretName,
			ScramCredentialsSecretName: m.OpsManagerUserScramCredentialsName(),
		},
	}
}

// used in AppDBConfigurable to implement scram.Configurable
func (m *AppDBSpec) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: m.Name(), Namespace: m.Namespace}
}

// GetOpsManagerUserPasswordSecretName returns the name of the secret
// that will store the Ops Manager user's password.
func (m *AppDBSpec) GetOpsManagerUserPasswordSecretName() string {
	return m.Name() + "-om-password"
}

// GetOpsManagerUserPasswordSecretKey returns the key that should be used to map to the Ops Manager user's
// password in the secret.
func (m *AppDBSpec) GetOpsManagerUserPasswordSecretKey() string {
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Key != "" {
		return m.PasswordSecretKeyRef.Key
	}
	return util.DefaultAppDbPasswordKey
}

// OpsManagerUserScramCredentialsName returns the name of the Secret
// which will store the Ops Manager MongoDB user's scram credentials.
func (m *AppDBSpec) OpsManagerUserScramCredentialsName() string {
	return m.Name() + "-om-user-scram-credentials"
}

type ConnectionSpec struct {
	mdbv1.SharedConnectionSpec `json:",inline"`

	// Credentials differ to mdbv1.ConnectionSpec because they are optional here.

	// Name of the Secret holding credentials information
	Credentials string `json:"credentials,omitempty"`
}

type AppDbBuilder struct {
	appDb *AppDBSpec
}

// GetMongoDBVersion returns the version of the MongoDB.
// For AppDB we directly rely on the version field which can
// contain -ent or not for enterprise and static containers.
func (m *AppDBSpec) GetMongoDBVersion() string {
	return m.Version
}

func (m *AppDBSpec) GetClusterDomain() string {
	if m.ClusterDomain != "" {
		return m.ClusterDomain
	}
	return "cluster.local"
}

// Replicas returns the number of "user facing" replicas of the MongoDB resource. This method can be used for
// constructing the mongodb URL for example.
// 'Members' would be a more consistent function but go doesn't allow to have the same
// For AppDB there is a validation that number of members is in the range [3, 50]
func (m *AppDBSpec) Replicas() int {
	return m.Members
}

func (m *AppDBSpec) GetSecurityAuthenticationModes() []string {
	return m.GetSecurity().Authentication.GetModes()
}

func (m *AppDBSpec) GetResourceType() mdbv1.ResourceType {
	return m.ResourceType
}

func (m *AppDBSpec) IsSecurityTLSConfigEnabled() bool {
	return m.GetSecurity().IsTLSEnabled()
}

func (m *AppDBSpec) GetFeatureCompatibilityVersion() *string {
	return m.FeatureCompatibilityVersion
}

func (m *AppDBSpec) GetSecurity() *mdbv1.Security {
	if m.Security == nil {
		return &mdbv1.Security{}
	}
	return m.Security
}

func (m *AppDBSpec) GetTLSConfig() *mdbv1.TLSConfig {
	if m.Security == nil || m.Security.TLSConfig == nil {
		return &mdbv1.TLSConfig{}
	}

	return m.Security.TLSConfig
}

func DefaultAppDbBuilder() *AppDbBuilder {
	appDb := &AppDBSpec{
		Version:              "4.2.0",
		Members:              3,
		PodSpec:              &mdbv1.MongoDbPodSpec{},
		PasswordSecretKeyRef: &userv1.SecretKeyRef{},
	}
	return &AppDbBuilder{appDb: appDb}
}

func (b *AppDbBuilder) Build() *AppDBSpec {
	return b.appDb.DeepCopy()
}

func (m *AppDBSpec) GetSecretName() string {
	return m.Name() + "-password"
}

func (m *AppDBSpec) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *AppDBSpec
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}

	// if a reference is specified without a key, we will default to "password"
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Key == "" {
		m.PasswordSecretKeyRef.Key = util.DefaultAppDbPasswordKey
	}

	m.Credentials = ""
	m.CloudManagerConfig = nil
	m.OpsManagerConfig = nil

	// all resources have a pod spec
	if m.PodSpec == nil {
		m.PodSpec = mdbv1.NewMongoDbPodSpec()
	}
	return nil
}

// Name returns the name of the StatefulSet for the AppDB
func (m *AppDBSpec) Name() string {
	return m.OpsManagerName + "-db"
}

func (m *AppDBSpec) ProjectIDConfigMapName() string {
	return m.Name() + "-project-id"
}

func (m *AppDBSpec) ClusterMappingConfigMapName() string {
	return m.Name() + "-cluster-mapping"
}

func (m *AppDBSpec) LastAppliedMemberSpecConfigMapName() string {
	return m.Name() + "-member-spec"
}

func (m *AppDBSpec) ServiceName() string {
	if m.Service == "" {
		return m.Name() + "-svc"
	}
	return m.Service
}

func (m *AppDBSpec) AutomationConfigSecretName() string {
	return m.Name() + "-config"
}

func (m *AppDBSpec) MonitoringAutomationConfigSecretName() string {
	return m.Name() + "-monitoring-config"
}

func (m *AppDBSpec) GetAgentLogFile() string {
	return automationconfig.DefaultAgentLogFile
}

// This function is used in community to determine whether we need to create a single
// volume for data+logs or two separate ones
// unless spec.PodSpec.Persistence.MultipleConfig is set, a single volume will be created
func (m *AppDBSpec) HasSeparateDataAndLogsVolumes() bool {
	p := m.PodSpec.Persistence
	return p != nil && (p.MultipleConfig != nil && p.SingleConfig == nil)
}

func (m *AppDBSpec) GetUpdateStrategyType() appsv1.StatefulSetUpdateStrategyType {
	return m.UpdateStrategyType
}

// GetCAConfigMapName returns the name of the ConfigMap which contains
// the CA which will recognize the certificates used to connect to the AppDB
// deployment
func (m *AppDBSpec) GetCAConfigMapName() string {
	security := m.Security
	if security != nil && security.TLSConfig != nil {
		return security.TLSConfig.CA
	}
	return ""
}

// GetTlsCertificatesSecretName returns the name of the secret
// which holds the certificates used to connect to the AppDB
func (m *AppDBSpec) GetTlsCertificatesSecretName() string {
	return m.GetSecurity().MemberCertificateSecretName(m.Name())
}

func (m *AppDBSpec) GetName() string {
	return m.Name()
}

func (m *AppDBSpec) GetNamespace() string {
	return m.Namespace
}

func (m *AppDBSpec) DataVolumeName() string {
	return "data"
}

func (m *AppDBSpec) LogsVolumeName() string {
	return "logs"
}

func (m *AppDBSpec) NeedsAutomationConfigVolume() bool {
	return !vault.IsVaultSecretBackend()
}

func (m *AppDBSpec) AutomationConfigConfigMapName() string {
	return fmt.Sprintf("%s-automation-config-version", m.Name())
}

func (m *AppDBSpec) MonitoringAutomationConfigConfigMapName() string {
	return fmt.Sprintf("%s-monitoring-automation-config-version", m.Name())
}

// GetSecretsMountedIntoPod returns the list of strings mounted into the pod that we need to watch.
func (m *AppDBSpec) GetSecretsMountedIntoPod() []string {
	secrets := []string{}
	if m.PasswordSecretKeyRef != nil {
		secrets = append(secrets, m.PasswordSecretKeyRef.Name)
	}

	if m.Security.IsTLSEnabled() {
		secrets = append(secrets, m.GetTlsCertificatesSecretName())
	}
	return secrets
}

func (m *AppDBSpec) GetMemberClusterSpecByName(memberClusterName string) mdbv1.ClusterSpecItem {
	for _, clusterSpec := range m.GetClusterSpecList() {
		if clusterSpec.ClusterName == memberClusterName {
			return clusterSpec
		}
	}

	// In case the member cluster is not found in the cluster spec list, we return an empty ClusterSpecItem
	// with 0 members to handle the case of removing a cluster from the spec list without a panic.
	//
	// This is not ideal, because we don't consider other fields that were removed i
	return mdbv1.ClusterSpecItem{
		ClusterName: memberClusterName,
		Members:     0,
	}
}

func (m *AppDBSpec) BuildConnectionURL(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string, multiClusterHostnames []string) string {
	builder := connectionstring.Builder().
		SetName(m.Name()).
		SetNamespace(m.Namespace).
		SetUsername(username).
		SetPassword(password).
		SetReplicas(m.Replicas()).
		SetService(m.ServiceName()).
		SetVersion(m.GetMongoDBVersion()).
		SetAuthenticationModes(m.GetSecurityAuthenticationModes()).
		SetClusterDomain(m.GetClusterDomain()).
		SetExternalDomain(m.GetExternalDomain()).
		SetIsReplicaSet(true).
		SetIsTLSEnabled(m.IsSecurityTLSConfigEnabled()).
		SetConnectionParams(connectionParams).
		SetScheme(scheme)

	if m.IsMultiCluster() {
		builder.SetReplicas(len(multiClusterHostnames))
		builder.SetHostnames(multiClusterHostnames)
	}

	return builder.Build()
}

func (m *AppDBSpec) GetClusterSpecList() mdbv1.ClusterSpecList {
	if m.IsMultiCluster() {
		return m.ClusterSpecList
	} else {
		return mdbv1.ClusterSpecList{
			{
				ClusterName:  multicluster.LegacyCentralClusterName,
				Members:      m.Members,
				MemberConfig: m.GetMemberOptions(),
			},
		}
	}
}

func (m *AppDBSpec) IsMultiCluster() bool {
	return m.Topology == ClusterTopologyMultiCluster
}

func (m *AppDBSpec) NameForCluster(memberClusterNum int) string {
	if !m.IsMultiCluster() {
		return m.GetName()
	}

	return fmt.Sprintf("%s-%d", m.GetName(), memberClusterNum)
}

func (m *AppDBSpec) HeadlessServiceSelectorAppLabel(memberClusterNum int) string {
	return m.HeadlessServiceNameForCluster(memberClusterNum)
}

func (m *AppDBSpec) HeadlessServiceNameForCluster(memberClusterNum int) string {
	if !m.IsMultiCluster() {
		return m.ServiceName()
	}

	if m.Service == "" {
		return dns.GetMultiHeadlessServiceName(m.GetName(), memberClusterNum)
	}

	return fmt.Sprintf("%s-%d", m.Service, memberClusterNum)
}

func GetAppDBCaPemPath() string {
	return util.AppDBMmsCaFileDirInContainer + "ca-pem"
}

func (m *AppDBSpec) GetPodName(clusterIdx int, podIdx int) string {
	if m.IsMultiCluster() {
		return dns.GetMultiPodName(m.Name(), clusterIdx, podIdx)
	}
	return dns.GetPodName(m.Name(), podIdx)
}

func (m *AppDBSpec) GetExternalServiceName(clusterIdx int, podIdx int) string {
	if m.IsMultiCluster() {
		return dns.GetMultiExternalServiceName(m.GetName(), clusterIdx, podIdx)
	}
	return dns.GetExternalServiceName(m.Name(), podIdx)
}

func (m *AppDBSpec) GetExternalAccessConfiguration() *mdbv1.ExternalAccessConfiguration {
	return m.ExternalAccessConfiguration
}

func (m *AppDBSpec) GetExternalDomain() *string {
	if m.ExternalAccessConfiguration != nil {
		return m.ExternalAccessConfiguration.ExternalDomain
	}
	return nil
}

func (m *AppDBSpec) GetExternalAccessConfigurationForMemberCluster(clusterName string) *mdbv1.ExternalAccessConfiguration {
	for _, csl := range m.ClusterSpecList {
		if csl.ClusterName == clusterName && csl.ExternalAccessConfiguration != nil {
			return csl.ExternalAccessConfiguration
		}
	}

	return m.ExternalAccessConfiguration
}

func (m *AppDBSpec) GetExternalDomainForMemberCluster(clusterName string) *string {
	if cfg := m.GetExternalAccessConfigurationForMemberCluster(clusterName); cfg != nil {
		if externalDomain := cfg.ExternalDomain; externalDomain != nil {
			return externalDomain
		}
	}

	return m.GetExternalDomain()
}
