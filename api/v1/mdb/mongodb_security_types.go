package mdb

import (
	"fmt"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
	corev1 "k8s.io/api/core/v1"
	"strings"
)

type Security struct {
	TLSConfig      *TLSConfig      `json:"tls,omitempty"`
	Authentication *Authentication `json:"authentication,omitempty"`
	Roles          []MongoDbRole   `json:"roles,omitempty"`

	// +optional
	CertificatesSecretsPrefix string `json:"certsSecretPrefix"`
}

func newSecurity() *Security {
	return &Security{TLSConfig: &TLSConfig{}}
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

// MemberCertificateSecretName returns the name of the secret containing the member TLS certs.
func (s *Security) MemberCertificateSecretName(defaultName string) string {
	if s.CertificatesSecretsPrefix != "" {
		return fmt.Sprintf("%s-%s-cert", s.CertificatesSecretsPrefix, defaultName)
	}

	// The default behaviour is to use the `defaultname-cert` format
	return fmt.Sprintf("%s-cert", defaultName)
}

func (d *DbCommonSpec) GetSecurity() *Security {
	if d.Security == nil {
		return &Security{}
	}
	return d.Security
}
func (s *Security) IsTLSEnabled() bool {
	if s == nil {
		return false
	}
	if s.TLSConfig != nil {
		if s.TLSConfig.Enabled {
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
		return string(s.Authentication.Modes[0])
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
// as the only auth mechanism.
func (s Security) RequiresClientTLSAuthentication() bool {
	if s.Authentication == nil {
		return false
	}

	if len(s.Authentication.Modes) == 1 && s.Authentication.IsX509Enabled() {
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
	Enabled         bool       `json:"enabled"`
	Modes           []AuthMode `json:"modes,omitempty"`
	InternalCluster string     `json:"internalCluster,omitempty"`
	// IgnoreUnknownUsers maps to the inverse of auth.authoritativeSet
	IgnoreUnknownUsers bool `json:"ignoreUnknownUsers,omitempty"`

	// LDAP Configuration
	// +optional
	Ldap *Ldap `json:"ldap,omitempty"`

	// Configuration for OIDC providers
	// +optional
	OIDCProviderConfigs []OIDCProviderConfig `json:"oidcProviderConfigs,omitempty"`

	// Agents contains authentication configuration properties for the agents
	// +optional
	Agents AgentAuthentication `json:"agents,omitempty"`

	// Clients should present valid TLS certificates
	RequiresClientTLSAuthentication bool `json:"requireClientTLSAuthentication,omitempty"`
}

func newAuthentication() *Authentication {
	return &Authentication{Modes: []AuthMode{}}
}

// +kubebuilder:validation:Enum=X509;SCRAM;SCRAM-SHA-1;MONGODB-CR;SCRAM-SHA-256;LDAP;OIDC
type AuthMode string

func ConvertAuthModesToStrings(authModes []AuthMode) []string {
	stringAuth := make([]string, len(authModes))
	for i, auth := range authModes {
		stringAuth[i] = string(auth)
	}
	return stringAuth
}

func IsAuthPresent(authModes []AuthMode, auth string) bool {
	for _, authMode := range authModes {
		if string(authMode) == auth {
			return true
		}
	}
	return false
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
	ClientCertificateSecretRefWrap common.ClientCertificateSecretRefWrapper `json:"clientCertificateSecretRef,omitempty"`
}

// IsX509Enabled determines if X509 is to be enabled at the project level
// it does not necessarily mean that the agents are using X509 authentication
func (a *Authentication) IsX509Enabled() bool {
	if a == nil || !a.Enabled {
		return false
	}

	return stringutil.Contains(a.GetModes(), util.X509)
}

// IsLDAPEnabled determines if LDAP is to be enabled at the project level
func (a *Authentication) IsLDAPEnabled() bool {
	if a == nil || !a.Enabled {
		return false
	}

	return stringutil.Contains(a.GetModes(), util.LDAP)
}

// IsOIDCEnabled determines if OIDC is to be enabled at the project level
func (a *Authentication) IsOIDCEnabled() bool {
	if a == nil || !a.Enabled {
		return false
	}

	return stringutil.Contains(a.GetModes(), util.OIDC)
}

// GetModes returns the modes of the Authentication instance of an empty
// list if it is nil
func (a *Authentication) GetModes() []string {
	if a == nil {
		return []string{}
	}
	return ConvertAuthModesToStrings(a.Modes)
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
	// +optional
	TimeoutMS int `json:"timeoutMS"`
	// +optional
	UserCacheInvalidationInterval int `json:"userCacheInvalidationInterval"`
}

type OIDCProviderConfig struct {
	// Unique label that identifies this configuration. This label is visible to your Ops Manager users and is used when
	// creating users and roles for authorization. It is case-sensitive and can only contain the following characters:
	//  - alphanumeric characters (combination of a to z and 0 to 9)
	//  - hyphens (-)
	//  - underscores (_)
	// +kubebuilder:validation:Pattern="^[a-zA-Z0-9-_]+$"
	// +kubebuilder:validation:Required
	ConfigurationName string `json:"configurationName"`

	// Issuer value provided by your registered IdP application. Using this URI, MongoDB finds an OpenID Provider
	// Configuration Document, which should be available in the /.wellknown/open-id-configuration endpoint.
	// For MongoDB 7.0, 7.3, and 8.0+, the combination of issuerURI and audience must be unique across OIDC provider configurations.
	// For other MongoDB versions, the issuerURI itself must be unique.

	// +kubebuilder:validation:Required
	IssuerURI string `json:"issuerURI"`

	// Entity that your external identity provider intends the token for.
	// Enter the audience value from the app you registered with external Identity Provider.
	// +kubebuilder:validation:Required
	Audience string `json:"audience"`

	// Select GroupMembership to grant authorization based on IdP user group membership, or select UserID to grant
	// an individual user authorization.
	// +kubebuilder:validation:Required
	AuthorizationType OIDCAuthorizationType `json:"authorizationType"`

	// The identifier of the claim that includes the user principal identity.
	// Accept the default value unless your IdP uses a different claim.
	// +kubebuilder:default=sub
	// +kubebuilder:validation:Required
	UserClaim string `json:"userClaim"`

	// The identifier of the claim that includes the principal's IdP user group membership information.
	// Accept the default value unless your IdP uses a different claim, or you need a custom claim.
	// Required when selected GroupMembership as the authorization type, ignored otherwise
	// +kubebuilder:validation:Optional
	GroupsClaim *string `json:"groupsClaim"`

	// Configure single-sign-on for human user access to Ops Manager deployments with Workforce Identity Federation.
	// For programmatic, application access to Ops Manager deployments use Workload Identity Federation.
	// Only one Workforce Identity Federation IdP can be configured per MongoDB resource
	// +kubebuilder:validation:Required
	AuthorizationMethod OIDCAuthorizationMethod `json:"authorizationMethod"`

	// Unique identifier for your registered application. Enter the clientId value from the app you
	// registered with an external Identity Provider.
	// Required when selected Workforce Identity Federation authorization method
	// +kubebuilder:validation:Optional
	ClientId *string `json:"clientId"`

	// Tokens that give users permission to request data from the authorization endpoint.
	// Only used for Workforce Identity Federation authorization method
	// +kubebuilder:validation:Optional
	RequestedScopes []string `json:"requestedScopes,omitempty"`
}

// +kubebuilder:validation:Enum=GroupMembership;UserID
type OIDCAuthorizationType string

// +kubebuilder:validation:Enum=WorkforceIdentityFederation;WorkloadIdentityFederation
type OIDCAuthorizationMethod string

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
}

func (m *MongoDbSpec) GetTLSConfig() *TLSConfig {
	if m.Security == nil || m.Security.TLSConfig == nil {
		return &TLSConfig{}
	}

	return m.Security.TLSConfig
}

func (m *MongoDbSpec) GetSecurityAuthenticationModes() []string {
	return m.GetSecurity().Authentication.GetModes()
}

func (d *DbCommonSpec) IsSecurityTLSConfigEnabled() bool {
	return d.GetSecurity().IsTLSEnabled()
}

func (m *MongoDB) GetLDAP(password, caContents string) *ldap.Ldap {
	if !m.IsLDAPEnabled() {
		return nil
	}

	mdbLdap := m.Spec.Security.Authentication.Ldap
	transportSecurity := GetTransportSecurity(mdbLdap)

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

		TimeoutMS:                     mdbLdap.TimeoutMS,
		UserCacheInvalidationInterval: mdbLdap.UserCacheInvalidationInterval,
	}
}

func GetTransportSecurity(mdbLdap *Ldap) TransportSecurity {
	transportSecurity := TransportSecurityNone
	if mdbLdap.TransportSecurity != nil && strings.ToLower(string(*mdbLdap.TransportSecurity)) != "none" {
		transportSecurity = TransportSecurityTLS
	}
	return transportSecurity
}

func (m *MongoDB) IsLDAPEnabled() bool {
	if m.Spec.Security == nil || m.Spec.Security.Authentication == nil {
		return false
	}
	return m.Spec.Security.Authentication.IsLDAPEnabled()
}

func (m *MongoDB) IsOIDCEnabled() bool {
	if m.Spec.Security == nil || m.Spec.Security.Authentication == nil {
		return false
	}
	return m.Spec.Security.Authentication.IsOIDCEnabled()
}

func (m *MongoDB) GetAuthenticationModes() []string {
	return m.Spec.Security.Authentication.GetModes()
}
