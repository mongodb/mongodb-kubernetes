package om

import (
	"encoding/json"
	"fmt"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/scram"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type AppDBSpec struct {
	// +kubebuilder:validation:Pattern=^[0-9]+.[0-9]+.[0-9]+(-.+)?$|^$
	Version string `json:"version,omitempty"`
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
	// Deprecated: This has been replaced by the ClusterDomain which should be
	// used instead
	ClusterName  string                     `json:"clusterName,omitempty"`
	Connectivity *mdbv1.MongoDBConnectivity `json:"connectivity,omitempty"`
	// AdditionalMongodConfig is additional configuration that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	AdditionalMongodConfig mdbv1.AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
	Agent                  mdbv1.AgentConfig            `json:"agent,omitempty"`
	Persistent             *bool                        `json:"persistent,omitempty"`
	ConnectionSpec         `json:",inline"`

	// PasswordSecretKeyRef contains a reference to the secret which contains the password
	// for the mongodb-ops-manager SCRAM-SHA user
	PasswordSecretKeyRef *userv1.SecretKeyRef `json:"passwordSecretKeyRef,omitempty"`

	// transient fields. These fields are cleaned before serialization, see 'MarshalJSON()'
	// note, that we cannot include the 'OpsManager' instance here as this creates circular dependency and problems with
	// 'DeepCopy'

	OpsManagerName string `json:"-"`
	Namespace      string `json:"-"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`
}

// GetAgentPasswordSecretNamespacedName returns the NamespacedName for the secret
// which contains the Automation Agent's password.
func (m AppDBSpec) GetAgentPasswordSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: m.Namespace,
		Name:      m.Name() + "-password",
	}
}

// GetAgentKeyfileSecretNamespacedName returns the NamespacedName for the secret
// which contains the keyfile.
func (m AppDBSpec) GetAgentKeyfileSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: m.Namespace,
		Name:      m.Name() + "-keyfile",
	}
}

// GetScramOptions returns a set of Options which is used to configure Scram Sha authentication
// in the AppDB.
func (m AppDBSpec) GetScramOptions() scram.Options {
	return scram.Options{
		AuthoritativeSet: false,
		KeyFile:          util.AutomationAgentKeyFilePathInContainer,
		AutoAuthMechanisms: []string{
			scram.Sha256,
			scram.Sha1,
		},
		AgentName:         util.AutomationAgentName,
		AutoAuthMechanism: scram.Sha1,
	}
}

// GetScramUsers returns a list of all scram users for this deployment.
// in this case it is just the Ops Manager user for the AppDB.
func (m AppDBSpec) GetScramUsers() []scram.User {
	return []scram.User{
		{

			Username: util.OpsManagerMongoDBUserName,
			Database: util.DefaultUserDatabase,

			// required roles for the AppDB user are outlined in the documentation
			// https://docs.opsmanager.mongodb.com/current/tutorial/prepare-backing-mongodb-instances/#replica-set-security
			Roles: []scram.Role{
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
			PasswordSecretName:         m.GetOpsManagerUserPasswordSecretName(),
			ScramCredentialsSecretName: m.OpsManagerUserScramCredentialsName(),
		},
	}
}

func (m AppDBSpec) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: m.Name(), Namespace: m.Namespace}
}

// GetOpsManagerUserPasswordSecretName returns the name of the secret
// that will store the Ops Manager user's password.
func (m AppDBSpec) GetOpsManagerUserPasswordSecretName() string {
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Name != "" {
		return m.PasswordSecretKeyRef.Name
	}
	return m.Name() + "-password"
}

// GetOpsManagerUserPasswordSecretKey returns the key that should be used to map to the Ops Manager user's
// password in the secret.
func (m AppDBSpec) GetOpsManagerUserPasswordSecretKey() string {
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Key != "" {
		return m.PasswordSecretKeyRef.Key
	}
	return util.DefaultAppDbPasswordKey
}

// OpsManagerUserScramCredentialsName returns the name of the Secret
// which will store the Ops Manager MongoDB user's scram credentials.
func (m AppDBSpec) OpsManagerUserScramCredentialsName() string {
	return m.Name() + "-scram-credentials"
}

type ConnectionSpec struct {
	// Transient field - the name of the project. By default is equal to the name of the resource
	// though can be overridden if the ConfigMap specifies a different name
	ProjectName string `json:"-"` // ignore when marshalling

	// Name of the Secret holding credentials information
	Credentials string `json:"credentials,omitempty"`

	// Dev note: don't reference these two fields directly - use the `getProject` method instead

	OpsManagerConfig   *mdbv1.PrivateCloudConfig `json:"opsManager,omitempty"`
	CloudManagerConfig *mdbv1.PrivateCloudConfig `json:"cloudManager,omitempty"`

	// Deprecated: This has been replaced by the PrivateCloudConfig which should be
	// used instead
	Project string `json:"project,omitempty"`

	// FIXME: LogLevel is not a required field for creating an Ops Manager connection, it should not be here.

	// +kubebuilder:validation:Enum=DEBUG;INFO;WARN;ERROR;FATAL
	LogLevel mdbv1.LogLevel `json:"logLevel,omitempty"`
}

type AppDbBuilder struct {
	appDb *AppDBSpec
}

// GetVersion returns the version of the MongoDB. In the case of the AppDB
// it is possible for this to be an empty string. For a regular mongodb, the regex
// version string validator will not allow this.
func (a AppDBSpec) GetVersion() string {
	if a.Version == "" {
		return util.BundledAppDbMongoDBVersion
	}
	return a.Version
}

func (a AppDBSpec) GetClusterDomain() string {
	if a.ClusterDomain != "" {
		return a.ClusterDomain
	}
	if a.ClusterName != "" {
		return a.ClusterName
	}
	return "cluster.local"
}

// Replicas returns the number of "user facing" replicas of the MongoDB resource. This method can be used for
// constructing the mongodb URL for example.
// 'Members' would be a more consistent function but go doesn't allow to have the same
// For AppDB there is a validation that number of members is in the range [3, 50]
func (a AppDBSpec) Replicas() int {
	return a.Members
}

func (a AppDBSpec) GetSecurityAuthenticationModes() []string {
	return a.GetSecurity().Authentication.GetModes()
}

func (a AppDBSpec) GetResourceType() mdbv1.ResourceType {
	return a.ResourceType
}

func (a AppDBSpec) IsSecurityTLSConfigEnabled() bool {
	return a.GetSecurity().TLSConfig.IsEnabled()
}

func (a AppDBSpec) GetFeatureCompatibilityVersion() *string {
	return a.FeatureCompatibilityVersion
}

func (a AppDBSpec) GetSecurity() *mdbv1.Security {
	if a.Security == nil {
		return &mdbv1.Security{}
	}
	return a.Security
}

func (a AppDBSpec) GetTLSConfig() *mdbv1.TLSConfig {
	if a.Security == nil || a.Security.TLSConfig == nil {
		return &mdbv1.TLSConfig{}
	}

	return a.Security.TLSConfig
}
func DefaultAppDbBuilder() *AppDbBuilder {
	appDb := &AppDBSpec{
		Version:              "",
		Members:              3,
		PodSpec:              &mdbv1.MongoDbPodSpec{},
		PasswordSecretKeyRef: &userv1.SecretKeyRef{},
	}
	return &AppDbBuilder{appDb: appDb}
}

func (b *AppDbBuilder) Build() *AppDBSpec {
	return b.appDb.DeepCopy()
}

func (m AppDBSpec) GetSecretName() string {
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

	m.ConnectionSpec.Credentials = ""
	m.ConnectionSpec.CloudManagerConfig = nil
	m.ConnectionSpec.OpsManagerConfig = nil
	m.ConnectionSpec.Project = ""
	// all resources have a pod spec
	if m.PodSpec == nil {
		m.PodSpec = mdbv1.NewMongoDbPodSpec()
	}
	return nil
}

// Name returns the name of the StatefulSet for the AppDB
func (m AppDBSpec) Name() string {
	return m.OpsManagerName + "-db"
}

func (m AppDBSpec) ProjectIDConfigMapName() string {
	return m.Name() + "-project-id"
}

func (m AppDBSpec) ServiceName() string {
	if m.Service == "" {
		return m.Name() + "-svc"
	}
	return m.Service
}

func (m AppDBSpec) AutomationConfigSecretName() string {
	return m.Name() + "-config"
}

// GetCAConfigMapName returns the name of the ConfigMap which contains
// the CA which will recognize the certificates used to connect to the AppDB
// deployment
func (a AppDBSpec) GetCAConfigMapName() string {
	security := a.Security
	if security != nil && security.TLSConfig != nil {
		return security.TLSConfig.CA
	}
	return ""
}

// GetTlsCertificatesSecretName returns the name of the secret
// which holds the certificates used to connect to the AppDB
func (a AppDBSpec) GetTlsCertificatesSecretName() string {
	tlsConfig := a.GetSecurity().TLSConfig
	if !tlsConfig.IsEnabled() {
		return ""
	}

	// maintain old behaviour if name is specified instead of prefix
	if tlsConfig.SecretRef.Name != "" {
		return tlsConfig.SecretRef.Name
	}

	return fmt.Sprintf("%s-%s-cert", tlsConfig.SecretRef.Prefix, a.Name())
}

// ConnectionURL returns the connection url to the AppDB
func (m AppDBSpec) ConnectionURL(userName, password string, connectionParams map[string]string) string {
	return mdbv1.BuildConnectionUrl(m.Name(), m.ServiceName(), m.Namespace, userName, password, m, connectionParams)
}

func (m *AppDBSpec) GetName() string {
	return m.Name()
}
func (m *AppDBSpec) GetNamespace() string {
	return m.Namespace
}
