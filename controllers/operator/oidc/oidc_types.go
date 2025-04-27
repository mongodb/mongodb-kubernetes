package oidc

type ProviderConfig struct {
	AuthNamePrefix        string   `json:"authNamePrefix"`
	Audience              string   `json:"audience"`
	IssuerUri             string   `json:"issuerUri"`
	ClientId              string   `json:"clientId"`
	RequestedScopes       []string `json:"requestedScopes"`
	UserClaim             string   `json:"userClaim"`
	GroupsClaim           string   `json:"groupsClaim"`
	SupportsHumanFlows    bool     `json:"supportsHumanFlows"`
	UseAuthorizationClaim bool     `json:"useAuthorizationClaim"`
}
