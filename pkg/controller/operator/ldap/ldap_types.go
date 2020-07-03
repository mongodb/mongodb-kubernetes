package ldap

// Ldap holds all the fields required to configure LDAP authentication
type Ldap struct {
	AuthzQueryTemplate       string `json:"authzQueryTemplate"`
	BindMethod               string `json:"bindMethod"`
	BindQueryUser            string `json:"bindQueryUser"`
	BindSaslMechanisms       string `json:"bindSaslMechanisms"`
	Servers                  string `json:"servers"`
	TransportSecurity        string `json:"transportSecurity"`
	UserToDnMapping          string `json:"userToDNMapping"`
	ValidateLDAPServerConfig bool   `json:"validateLDAPServerConfig"`
	BindQueryPassword        string `json:"bindQueryPassword"`
}
