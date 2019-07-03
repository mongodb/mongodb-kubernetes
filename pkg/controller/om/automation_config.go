package om

import (
	"encoding/json"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// AutomationConfig maintains the raw map in the Deployment field
// and constructs structs to make use of go's type safety
type AutomationConfig struct {
	Auth       *Auth
	AgentSSL   *AgentSSL
	Deployment Deployment
}

// Apply merges the state of all concrete structs into the Deployment (map[string]interface{})
func (a *AutomationConfig) Apply() error {
	// applies all changes made to the Auth struct and merges with the corresponding map[string]interface{}
	// inside the Deployment
	mergedAuth, err := util.MergeWith(a.Auth, a.Deployment["auth"].(map[string]interface{}), &util.AgentTransformer{})
	if err != nil {
		return err
	}
	// same applies for the ssl object and map
	mergedSsl, err := util.MergeWith(a.AgentSSL, a.Deployment["ssl"].(map[string]interface{}), &util.AgentTransformer{})
	if err != nil {
		return err
	}

	a.Deployment["ssl"] = mergedSsl
	a.Deployment["auth"] = mergedAuth
	return nil
}

func NewAutomationConfig(deployment Deployment) *AutomationConfig {
	return &AutomationConfig{AgentSSL: &AgentSSL{}, Auth: NewAuth(), Deployment: deployment}
}

func NewAuth() *Auth {
	return &Auth{Users: make([]MongoDBUser, 0), AutoAuthMechanisms: make([]string, 0), DeploymentAuthMechanisms: make([]string, 0)}
}

type Auth struct {
	// Users is a list which contains the desired users at the project level.
	Users    []MongoDBUser `json:"usersWanted,omitempty"`
	Disabled bool          `json:"disabled"`
	// AuthoritativeSet indicates if the MongoDBUsers should be synced with the current list of Users
	AuthoritativeSet bool `json:"authoritativeSet,omitempty"`
	// AutoAuthMechanisms is a list of auth mechanisms the Automation Agent is able to use
	AutoAuthMechanisms []string `json:"autoAuthMechanisms,omitempty"`
	// DeploymentAuthMechanisms is a list of possible auth mechanisms that can be used within deployments
	DeploymentAuthMechanisms []string `json:"deploymentAuthMechanisms,omitempty"`
	// AutoUser is the MongoDB Automation Agent user, when x509 is enabled, it should be set to the subject of the AA's certificate
	AutoUser string `json:"autoUser,omitempty"`
	// Key is the contents of the KeyFile
	Key string `json:"key,omitempty"`
	// KeyFile is the path to a keyfile with read & write permissions. It is a required field if `Disabled=false`
	KeyFile string `json:"keyfile,omitempty"`
	// KeyFileWindows is required if `Disabled=false` even if the value is not used
	KeyFileWindows string `json:"keyfileWindows,omitempty"`
	// AutoPwd is a required field when going from `Disabled=false` to `Disabled=true`
	AutoPwd string `json:"autoPwd,omitempty"`
}

func (a *Auth) AddUser(user MongoDBUser) {
	a.Users = append(a.Users, user)
}

func (a *Auth) HasUser(username, db string) bool {
	_, user := a.GetUser(username, db)
	return user != nil
}

func (a *Auth) GetUser(username, db string) (int, *MongoDBUser) {
	for i, u := range a.Users {
		if u.Username == username && u.Database == db {
			return i, &u
		}
	}
	return -1, nil
}

func (a *Auth) UpdateUser(username, db string, user MongoDBUser) bool {
	i, foundUser := a.GetUser(username, db)
	if foundUser == nil {
		return false
	}
	a.Users[i] = user
	return true
}

func (a *Auth) RemoveUser(username, db string) {
	i, _ := a.GetUser(username, db)
	copy(a.Users[i:], a.Users[i+1:])
	a.Users = a.Users[:len(a.Users)-1]
}

// AgentSSL contains fields related to configuration Automation
// Agent SSL & authentication.
type AgentSSL struct {
	CAFilePath            string `json:"CAFilePath,omitempty"`
	AutoPEMKeyFilePath    string `json:"autoPEMKeyFilePath,omitempty"`
	ClientCertificateMode string `json:"clientCertificateMode,omitempty"`
}

func (a AgentSSL) SSLEnabled() bool {
	return a.CAFilePath != "" && a.AutoPEMKeyFilePath != "" &&
		(a.ClientCertificateMode == util.OptionalClientCertficates || a.ClientCertificateMode == util.RequireClientCertificates)
}

type MongoDBUser struct {
	Mechanisms                 []string `json:"mechanisms"`
	Roles                      []Role   `json:"roles"`
	Username                   string   `json:"user"`
	Database                   string   `json:"db"`
	AuthenticationRestrictions []string `json:"authenticationRestrictions"`
}

func (u *MongoDBUser) AddRole(role Role) {
	u.Roles = append(u.Roles, role)
}

type Role struct {
	Role     string `json:"role"`
	Database string `json:"db"`
}

func (ac *AutomationConfig) EnableX509Authentication() {
	ac.ensureAgentUsers()
	auth := ac.Auth
	auth.AutoPwd = util.MergoDelete
	auth.Disabled = false
	auth.AuthoritativeSet = true
	auth.Key = util.AutomationAgentKeyFileContents
	auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
	auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath
	ac.AgentSSL = &AgentSSL{
		AutoPEMKeyFilePath:    util.AutomationAgentPemFilePath,
		CAFilePath:            util.CAFilePathInContainer,
		ClientCertificateMode: util.RequireClientCertificates,
	}

	if !util.ContainsString(auth.AutoAuthMechanisms, util.AutomationConfigX509Option) {
		auth.AutoAuthMechanisms = append(auth.AutoAuthMechanisms, util.AutomationConfigX509Option)
	}
	if !util.ContainsString(auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) {
		auth.DeploymentAuthMechanisms = append(auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option)
	}
}

func (ac *AutomationConfig) DisableX509Authentication() {
	auth := ac.Auth
	// change back from subject to human readable user name
	auth.AutoUser = util.AutomationAgentUserName
	auth.Disabled = true
	// when going from disabled true -> false, a password is required for the Automation Agent
	auth.AutoPwd = util.DefaultAutomationAgentPassword
	ac.AgentSSL = &AgentSSL{
		AutoPEMKeyFilePath:    util.MergoDelete,
		ClientCertificateMode: util.OptionalClientCertficates,
	}
	auth.DeploymentAuthMechanisms = []string{}
	auth.AutoAuthMechanisms = []string{}
	// remove the monitoring and backup agent MongoDB users
	for _, user := range agentUsers() {
		if auth.HasUser(user.Username, user.Database) {
			auth.RemoveUser(user.Username, user.Database)
		}
	}
}
func (ac *AutomationConfig) CanEnableX509ProjectAuthentication() (bool, string) {
	if !ac.Deployment.AllProcessesAreTLSEnabled() {
		return false, "not all processes are TLS enabled, unable to enable x509 authentication"
	}
	return true, ""
}

func (ac *AutomationConfig) ensureAgentUsers() {
	auth := ac.Auth
	auth.AutoUser = util.AutomationAgentSubject
	for _, agentUser := range agentUsers() {
		if auth.HasUser(agentUser.Username, agentUser.Database) {
			auth.UpdateUser(agentUser.Username, agentUser.Database, agentUser)
		} else {
			auth.AddUser(agentUser)
		}
	}
}

// agentUsers returns the MongoDBUsers with all the required roles
// for the BackupAgent and the MonitoringAgent
func agentUsers() []MongoDBUser {
	return []MongoDBUser{
		{
			Username:                   util.BackupAgentSubject,
			Database:                   util.X509Db,
			AuthenticationRestrictions: []string{},
			Mechanisms:                 []string{},
			Roles: []Role{
				{
					Database: "admin",
					Role:     "clusterAdmin",
				},
				{
					Database: "admin",
					Role:     "readAnyDatabase",
				},
				{
					Database: "admin",
					Role:     "userAdminAnyDatabase",
				},
				{
					Database: "local",
					Role:     "readWrite",
				},
				{
					Database: "admin",
					Role:     "readWrite",
				},
			},
		},
		{
			Username:                   util.MonitoringAgentSubject,
			Database:                   util.X509Db,
			AuthenticationRestrictions: []string{},
			Mechanisms:                 []string{},
			Roles: []Role{
				{
					Database: "admin",
					Role:     "clusterMonitor",
				},
			},
		},
	}
}

// BuildAutomationConfigFromBytes takes in jsonBytes representing the Deployment
// and constructs an instance of AutomationConfig with all the concrete structs
// filled out.
func BuildAutomationConfigFromBytes(jsonBytes []byte) (*AutomationConfig, error) {
	deployment, err := BuildDeploymentFromBytes(jsonBytes)
	if err != nil {
		return nil, err
	}

	finalAutomationConfig := &AutomationConfig{Deployment: deployment}

	authMap, ok := deployment["auth"]
	if ok {
		authStr, err := json.Marshal(authMap)
		if err != nil {
			return nil, err
		}
		auth := &Auth{}
		if err := json.Unmarshal([]byte(authStr), auth); err != nil {
			return nil, err
		}
		finalAutomationConfig.Auth = auth
	}

	sslMap, ok := deployment["ssl"]
	if ok {
		sslStr, err := json.Marshal(sslMap)
		if err != nil {
			return nil, err
		}
		ssl := &AgentSSL{}
		if err := json.Unmarshal([]byte(sslStr), ssl); err != nil {
			return nil, err
		}
		finalAutomationConfig.AgentSSL = ssl
	}
	return finalAutomationConfig, nil
}
