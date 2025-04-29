package om

import (
	"encoding/json"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cast"
	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/generate"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

// AutomationConfig maintains the raw map in the Deployment field
// and constructs structs to make use of go's type safety
// Dev notes: actually, this object is just a wrapper for the `Deployment` object which is received from Ops Manager,
// and it's not equal to the AutomationConfig object from mms! It contains some transient struct fields for easier
// configuration which are merged into the `Deployment` object before sending it back to Ops Manager.
// As of right now only support configuring LogRotate for monitoring and backup via dedicated endpoints.
type AutomationConfig struct {
	Auth       *Auth
	AgentSSL   *AgentSSL
	Deployment Deployment
	Ldap       *ldap.Ldap
}

// Apply merges the state of all concrete structs into the Deployment (map[string]interface{})
func (ac *AutomationConfig) Apply() error {
	return applyInto(*ac, &ac.Deployment)
}

// applyInto is a helper method that does not mutate AutomationConfig "a", but only Deployment "deployment"
func applyInto(a AutomationConfig, into *Deployment) error {
	// applies all changes made to the Auth struct and merges with the corresponding map[string]interface{}
	// inside the Deployment
	if _, ok := a.Deployment["auth"]; ok {
		mergedAuth, err := util.MergeWith(a.Auth, a.Deployment["auth"].(map[string]interface{}), &util.AutomationConfigTransformer{})
		if err != nil {
			return err
		}
		(*into)["auth"] = mergedAuth
	}
	// the same applies for the ssl object and map
	if _, ok := a.Deployment["tls"]; ok {
		mergedTLS, err := util.MergeWith(a.AgentSSL, a.Deployment["tls"].(map[string]interface{}), &util.AutomationConfigTransformer{})
		if err != nil {
			return err
		}
		(*into)["tls"] = mergedTLS
	}

	if _, ok := a.Deployment["ldap"]; ok {
		mergedLdap, err := util.MergeWith(a.Ldap, a.Deployment["ldap"].(map[string]interface{}), &util.AutomationConfigTransformer{})
		if err != nil {
			return err
		}
		(*into)["ldap"] = mergedLdap
	}
	return nil
}

// EqualsWithoutDeployment returns true if two AutomationConfig objects are meaningful equal by following the following conditions:
// - Not taking AutomationConfig.Deployment into consideration.
// - Serializing ac A and ac B to ensure that we remove util.MergoDelete before comparing those two.
//
// Comparing Deployments will not work correctly in current AutomationConfig implementation. Helper
// structs, such as AutomationConfig.AgentSSL or AutomationConfig.Auth use non-pointer fields (without `omitempty`).
// When merging them into AutomationConfig.deployment, JSON unmarshaller renders them into their representations,
// and they get into the final result. Sadly, some tests (especially TestLDAPIsMerged) relies on this behavior.
func (ac *AutomationConfig) EqualsWithoutDeployment(b AutomationConfig) bool {
	deploymentsComparer := cmp.Comparer(func(x, y Deployment) bool {
		return true
	})

	acA, err := getSerializedAC(*ac)
	if err != nil {
		return false
	}

	acB, err := getSerializedAC(b)
	if err != nil {
		return false
	}

	return cmp.Equal(acA, acB, deploymentsComparer)
}

// getSerializedAC calls apply on a deepCopy which decodes the internal struct into the Deployment map. After decoding,
// we encode the map into its internal representation again. Doing that removes util.MergoDelete for proper comparison
func getSerializedAC(original AutomationConfig) (AutomationConfig, error) {
	empty := AutomationConfig{}
	deepCopy, err := util.MapDeepCopy(original.Deployment)
	if err != nil {
		return empty, err
	}
	err = applyInto(original, (*Deployment)(&deepCopy))
	if err != nil {
		return empty, err
	}

	ac, err := BuildAutomationConfigFromDeployment(deepCopy)
	if err != nil {
		return empty, err
	}
	return *ac, nil
}

// isEqualAfterModification returns true if two Deployment objects are equal ignoring their underlying custom types.
// depFunc might change the Deployment or might only change the types. In both cases it will fail the comparison
// as long as we don't ignore the types.
func isEqualAfterModification(changeDeploymentFunc func(Deployment) error, deployment Deployment) (bool, error) {
	original, err := util.MapDeepCopy(deployment) // original over the wire does not contain any types
	if err != nil {
		return false, err
	}
	if err := changeDeploymentFunc(deployment); err != nil { // might change types as well
		return false, err
	}

	deploymentWithoutTypes, err := maputil.StructToMap(deployment)
	if err != nil {
		return false, err
	}

	if equality.Semantic.DeepEqual(deploymentWithoutTypes, original) {
		return true, nil
	}
	return false, nil
}

// NewAutomationConfig returns an AutomationConfig instance with all reference
// types initialized with non-nil values
func NewAutomationConfig(deployment Deployment) *AutomationConfig {
	return &AutomationConfig{AgentSSL: &AgentSSL{}, Auth: NewAuth(), Deployment: deployment}
}

// NewAuth returns an empty Auth reference with all reference types initialised to non nil values
func NewAuth() *Auth {
	return &Auth{
		KeyFile:                  util.AutomationAgentKeyFilePathInContainer,
		KeyFileWindows:           util.AutomationAgentWindowsKeyFilePath,
		Users:                    make([]*MongoDBUser, 0),
		AutoAuthMechanisms:       make([]string, 0),
		DeploymentAuthMechanisms: make([]string, 0),
		AutoAuthMechanism:        "MONGODB-CR",
		Disabled:                 true,
		AuthoritativeSet:         true,
		AutoUser:                 util.AutomationAgentName,
	}
}

// SetVersion is needed only for the cluster config file when we use a headless agent
func (ac *AutomationConfig) SetVersion(configVersion int64) *AutomationConfig {
	ac.Deployment["version"] = configVersion
	return ac
}

// SetOptionsDownloadBase is needed only for the cluster config file when we use a headless agent
func (ac *AutomationConfig) SetOptionsDownloadBase(downloadBase string) *AutomationConfig {
	ac.Deployment["options"] = map[string]string{"downloadBase": downloadBase}

	return ac
}

// SetMongodbVersions is needed only for the cluster config file when we use a headless agent
func (ac *AutomationConfig) SetMongodbVersions(versionConfigs []MongoDbVersionConfig) *AutomationConfig {
	ac.Deployment["mongoDbVersions"] = versionConfigs

	return ac
}

func (ac *AutomationConfig) MongodbVersions() []MongoDbVersionConfig {
	return ac.Deployment["mongoDbVersions"].([]MongoDbVersionConfig)
}

// SetBaseUrlForAgents is needed only for the cluster config file when we use a headless agent
func (ac *AutomationConfig) SetBaseUrlForAgents(baseUrl string) *AutomationConfig {
	for _, v := range ac.Deployment.getBackupVersions() {
		cast.ToStringMap(v)["baseUrl"] = baseUrl
	}
	for _, v := range ac.Deployment.getMonitoringVersions() {
		cast.ToStringMap(v)["baseUrl"] = baseUrl
	}
	return ac
}

func (ac *AutomationConfig) Serialize() ([]byte, error) {
	return ac.Deployment.Serialize()
}

// GetAgentAuthMode returns the agentAuthMode of the given automationConfig. If empty or nil we return the empty string.
func (ac *AutomationConfig) GetAgentAuthMode() string {
	if ac == nil || ac.Auth == nil {
		return ""
	}
	return ac.Auth.AutoAuthMechanism
}

type Auth struct {
	// Users is a list which contains the desired users at the project level.
	Users    []*MongoDBUser `json:"usersWanted,omitempty"`
	Disabled bool           `json:"disabled"`
	// AuthoritativeSet indicates if the MongoDBUsers should be synced with the current list of Users
	AuthoritativeSet bool `json:"authoritativeSet"`
	// AutoAuthMechanisms is a list of auth mechanisms the Automation Agent is able to use
	AutoAuthMechanisms []string `json:"autoAuthMechanisms,omitempty"`

	// AutoAuthMechanism is the currently active agent authentication mechanism. This is a read only
	// field
	AutoAuthMechanism string `json:"autoAuthMechanism"`
	// DeploymentAuthMechanisms is a list of possible auth mechanisms that can be used within deployments
	DeploymentAuthMechanisms []string `json:"deploymentAuthMechanisms,omitempty"`
	// AutoUser is the MongoDB Automation Agent user, when x509 is enabled, it should be set to the subject of the AA's certificate
	AutoUser string `json:"autoUser,omitempty"`
	// Key is the contents of the KeyFile, the automation agent will ensure this a KeyFile with these contents exists at the `KeyFile` path
	Key string `json:"key,omitempty"`
	// KeyFile is the path to a keyfile with read & write permissions. It is a required field if `Disabled=false`
	KeyFile string `json:"keyfile,omitempty"`
	// KeyFileWindows is required if `Disabled=false` even if the value is not used
	KeyFileWindows string `json:"keyfileWindows,omitempty"`
	// AutoPwd is a required field when going from `Disabled=false` to `Disabled=true`
	AutoPwd string `json:"autoPwd,omitempty"`
	// NewAutoPwd is used for rotating the agent password
	NewAutoPwd string `json:"newAutoPwd,omitempty"`
	// LdapGroupDN is required when enabling LDAP authz and agents authentication on $external
	LdapGroupDN string `json:"autoLdapGroupDN,omitempty"`
}

// IsEnabled is a convenience function to aid readability
func (a *Auth) IsEnabled() bool {
	return !a.Disabled
}

// Enable is a convenience function to aid readability
func (a *Auth) Enable() {
	a.Disabled = false
}

// AddUser updates the Users list with the specified user
func (a *Auth) AddUser(user MongoDBUser) {
	a.Users = append(a.Users, &user)
}

// HasUser returns true if a user exists with the specified username and password
// or false if the user does not exist
func (a *Auth) HasUser(username, db string) bool {
	_, user := a.GetUser(username, db)
	return user != nil
}

// GetUser returns the index of the user with the given username and password
// and the user itself. -1 and a nil user are returned if the user does not exist
func (a *Auth) GetUser(username, db string) (int, *MongoDBUser) {
	for i, u := range a.Users {
		if u != nil && u.Username == username && u.Database == db {
			return i, u
		}
	}
	return -1, nil
}

// UpdateUser accepts a user ad updates the corresponding existing user.
// the user to update is identified by user.Username and user.Database
func (a *Auth) UpdateUser(user MongoDBUser) bool {
	i, foundUser := a.GetUser(user.Username, user.Database)
	if foundUser == nil {
		return false
	}
	a.Users[i] = &user
	return true
}

// EnsureUser adds the user to the Users list if it does not exist,
// it will update the existing user if it is already present.
func (a *Auth) EnsureUser(user MongoDBUser) {
	if a.HasUser(user.Username, user.Database) {
		a.UpdateUser(user)
	} else {
		a.AddUser(user)
	}
}

// EnsureUserRemoved will remove user of given username and password. A boolean
// indicating whether the underlying array was modified will be returned
func (a *Auth) EnsureUserRemoved(username, db string) bool {
	if a.HasUser(username, db) {
		a.RemoveUser(username, db)
		return true
	}
	return false
}

// RemoveUser assigns a nil value to the user. This nil value
// will flag this user for deletion when merging, see mergo_utils.go
func (a *Auth) RemoveUser(username, db string) {
	i, _ := a.GetUser(username, db)
	a.Users[i] = nil
}

// AgentSSL contains fields related to configuration Automation
// Agent SSL & authentication.
type AgentSSL struct {
	CAFilePath            string `json:"CAFilePath,omitempty"`
	AutoPEMKeyFilePath    string `json:"autoPEMKeyFilePath,omitempty"`
	ClientCertificateMode string `json:"clientCertificateMode,omitempty"`
}

type MongoDBUser struct {
	Mechanisms                 []string `json:"mechanisms"`
	Roles                      []*Role  `json:"roles"`
	Username                   string   `json:"user"`
	Database                   string   `json:"db"`
	AuthenticationRestrictions []string `json:"authenticationRestrictions"`

	// The clear text password to be assigned to the user
	InitPassword string `json:"initPwd,omitempty"`

	// ScramShaCreds are generated by the operator.
	ScramSha256Creds *ScramShaCreds `json:"scramSha256Creds"`
	ScramSha1Creds   *ScramShaCreds `json:"scramSha1Creds"`
}

type ScramShaCreds struct {
	IterationCount int    `json:"iterationCount"`
	Salt           string `json:"salt"`
	ServerKey      string `json:"serverKey"`
	StoredKey      string `json:"storedKey"`
}

func (u *MongoDBUser) AddRole(role *Role) {
	u.Roles = append(u.Roles, role)
}

type Role struct {
	Role     string `json:"role"`
	Database string `json:"db"`
}

type BuildConfig struct {
	Platform     string   `json:"platform"`
	Url          string   `json:"url"`
	GitVersion   string   `json:"gitVersion"`
	Architecture string   `json:"architecture"`
	Flavor       string   `json:"flavor"`
	MinOsVersion string   `json:"minOsVersion"`
	MaxOsVersion string   `json:"maxOsVersion"`
	Modules      []string `json:"modules"`
	// Note, that we are not including all "windows" parameters like "Win2008plus" as such distros won't be used
}

type MongoDbVersionConfig struct {
	Name   string         `json:"name"`
	Builds []*BuildConfig `json:"builds"`
}

// EnsureKeyFileContents makes sure a valid keyfile is generated and used for internal cluster authentication
func (ac *AutomationConfig) EnsureKeyFileContents() error {
	if ac.Auth.Key == "" || ac.Auth.Key == util.InvalidKeyFileContents {
		keyfileContents, err := generate.KeyFileContents()
		if err != nil {
			return err
		}
		ac.Auth.Key = keyfileContents
	}
	return nil
}

// EnsurePassword makes sure that there is an Automation Agent password
// that the agents will use to communicate with the deployments. The password
// is returned, so it can be provided to the other agents
func (ac *AutomationConfig) EnsurePassword() (string, error) {
	if ac.Auth.AutoPwd == "" || ac.Auth.AutoPwd == util.InvalidAutomationAgentPassword {
		automationAgentBackupPassword, err := generate.KeyFileContents()
		if err != nil {
			return "", err
		}
		ac.Auth.AutoPwd = automationAgentBackupPassword
		return automationAgentBackupPassword, nil
	}
	return ac.Auth.AutoPwd, nil
}

func (ac *AutomationConfig) CanEnableX509ProjectAuthentication() (bool, string) {
	if !ac.Deployment.AllProcessesAreTLSEnabled() {
		return false, "not all processes are TLS enabled, unable to enable x509 authentication"
	}
	return true, ""
}

func BuildAutomationConfigFromDeployment(deployment Deployment) (*AutomationConfig, error) {
	finalAutomationConfig := &AutomationConfig{Deployment: deployment}
	finalAutomationConfig.Auth = &Auth{}

	authMap, ok := deployment["auth"]
	if ok {
		authMarshalled, err := json.Marshal(authMap)
		if err != nil {
			return nil, err
		}
		auth := &Auth{}
		if err := json.Unmarshal(authMarshalled, auth); err != nil {
			return nil, err
		}
		finalAutomationConfig.Auth = auth
	}

	tlsMap, ok := deployment["tls"]
	if ok {
		sslMarshalled, err := json.Marshal(tlsMap)
		if err != nil {
			return nil, err
		}
		ssl := &AgentSSL{}
		if err := json.Unmarshal(sslMarshalled, ssl); err != nil {
			return nil, err
		}
		finalAutomationConfig.AgentSSL = ssl
	}

	ldapMap, ok := deployment["ldap"]
	if ok {
		ldapMarshalled, err := json.Marshal(ldapMap)
		if err != nil {
			return nil, err
		}
		acLdap := &ldap.Ldap{}
		if err := json.Unmarshal(ldapMarshalled, acLdap); err != nil {
			return nil, err
		}
		finalAutomationConfig.Ldap = acLdap
	}

	return finalAutomationConfig, nil
}

// BuildAutomationConfigFromBytes takes in jsonBytes representing the Deployment
// and constructs an instance of AutomationConfig with all the concrete structs
// filled out.
func BuildAutomationConfigFromBytes(jsonBytes []byte) (*AutomationConfig, error) {
	deployment, err := BuildDeploymentFromBytes(jsonBytes)
	if err != nil {
		return nil, err
	}
	return BuildAutomationConfigFromDeployment(deployment)
}
