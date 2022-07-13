package ldap

// Ldap holds all the fields required to configure LDAP authentication
type Ldap struct {
	AuthzQueryTemplate            string `json:"authzQueryTemplate,omitempty"`
	BindMethod                    string `json:"bindMethod"`
	BindQueryUser                 string `json:"bindQueryUser"`
	BindSaslMechanisms            string `json:"bindSaslMechanisms,omitempty"`
	Servers                       string `json:"servers"`
	TransportSecurity             string `json:"transportSecurity"`
	UserToDnMapping               string `json:"userToDNMapping,omitempty"`
	ValidateLDAPServerConfig      bool   `json:"validateLDAPServerConfig"`
	BindQueryPassword             string `json:"bindQueryPassword"`
	TimeoutMS                     int    `json:"timeoutMS,omitempty"`
	UserCacheInvalidationInterval int    `json:"userCacheInvalidationInterval,omitempty"`

	// Uses an undocumented property from the API used to mount
	// a CA file. This is done by the automation agent.
	// https://mongodb.slack.com/archives/CN0JB7XT2/p1594229779090700
	CaFileContents string `json:"CAFileContents"`
}
