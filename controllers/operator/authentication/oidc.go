package authentication

import (
	"fmt"
	"slices"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

var MongoDBOIDCMechanism = &oidcAuthMechanism{}

type oidcAuthMechanism struct{}

func (o *oidcAuthMechanism) GetName() MechanismName {
	return MongoDBOIDC
}

func (o *oidcAuthMechanism) EnableAgentAuthentication(_ om.Connection, _ Options, _ *zap.SugaredLogger) error {
	return xerrors.Errorf("OIDC agent authentication is not supported")
}

func (o *oidcAuthMechanism) DisableAgentAuthentication(_ om.Connection, _ *zap.SugaredLogger) error {
	return xerrors.Errorf("OIDC agent authentication is not supported")
}

func (o *oidcAuthMechanism) EnableDeploymentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC)) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC))
		}
		ac.OIDCProviderConfigs = opts.OIDCProviderConfigs

		return nil
	}, log)
}

func (o *oidcAuthMechanism) DisableDeploymentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC))
		ac.OIDCProviderConfigs = nil

		return nil
	}, log)
}

func (o *oidcAuthMechanism) IsAgentAuthenticationConfigured(*om.AutomationConfig, Options) bool {
	return false
}

func (o *oidcAuthMechanism) IsDeploymentAuthenticationConfigured(ac *om.AutomationConfig, opts Options) bool {
	return o.IsDeploymentAuthenticationEnabled(ac) && oidcProviderConfigsEqual(ac.OIDCProviderConfigs, opts.OIDCProviderConfigs)
}

func (o *oidcAuthMechanism) IsDeploymentAuthenticationEnabled(ac *om.AutomationConfig) bool {
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC))
}

func oidcProviderConfigsEqual(lhs []oidc.ProviderConfig, rhs []oidc.ProviderConfig) bool {
	if len(lhs) != len(rhs) {
		return false
	}

	lhsSorted := sortOIDCPProviderConfigs(lhs)
	rhsSorted := sortOIDCPProviderConfigs(rhs)

	return slices.EqualFunc(lhsSorted, rhsSorted, oidcProviderConfigEqual)
}

func sortOIDCPProviderConfigs(configs []oidc.ProviderConfig) []oidc.ProviderConfig {
	configsSeq := slices.Values(configs)
	return slices.SortedFunc(configsSeq, func(l, r oidc.ProviderConfig) int {
		return strings.Compare(l.AuthNamePrefix, r.AuthNamePrefix)
	})
}

func oidcProviderConfigEqual(l, r oidc.ProviderConfig) bool {
	return l.AuthNamePrefix == r.AuthNamePrefix &&
		l.Audience == r.Audience &&
		l.IssuerUri == r.IssuerUri &&
		slices.Equal(l.RequestedScopes, r.RequestedScopes) &&
		l.UserClaim == r.UserClaim &&
		l.GroupsClaim == r.GroupsClaim &&
		l.SupportsHumanFlows == r.SupportsHumanFlows &&
		l.UseAuthorizationClaim == r.UseAuthorizationClaim
}

func MapOIDCProviderConfigs(oidcProviderConfigs []mdbv1.OIDCProviderConfig) []oidc.ProviderConfig {
	if len(oidcProviderConfigs) == 0 {
		return nil
	}

	result := make([]oidc.ProviderConfig, len(oidcProviderConfigs))
	for i, providerConfig := range oidcProviderConfigs {
		clientId := providerConfig.ClientId
		if clientId == "" {
			clientId = util.MergoDelete
		}

		groupsClaim := providerConfig.GroupsClaim
		if groupsClaim == "" {
			groupsClaim = util.MergoDelete
		}

		result[i] = oidc.ProviderConfig{
			AuthNamePrefix:        providerConfig.ConfigurationName,
			Audience:              providerConfig.Audience,
			IssuerUri:             providerConfig.IssuerURI,
			ClientId:              clientId,
			RequestedScopes:       providerConfig.RequestedScopes,
			UserClaim:             providerConfig.UserClaim,
			GroupsClaim:           groupsClaim,
			SupportsHumanFlows:    mapToSupportHumanFlows(providerConfig.AuthorizationMethod),
			UseAuthorizationClaim: mapToUseAuthorizationClaim(providerConfig.AuthorizationType),
		}
	}

	return result
}

func mapToSupportHumanFlows(authMethod mdbv1.OIDCAuthorizationMethod) bool {
	switch authMethod {
	case mdbv1.OIDCAuthorizationMethodWorkforceIdentityFederation:
		return true
	case mdbv1.OIDCAuthorizationMethodWorkloadIdentityFederation:
		return false
	}

	panic(fmt.Sprintf("unsupported OIDC authorization method: %s", authMethod))
}

func mapToUseAuthorizationClaim(authType mdbv1.OIDCAuthorizationType) bool {
	switch authType {
	case mdbv1.OIDCAuthorizationTypeGroupMembership:
		return true
	case mdbv1.OIDCAuthorizationTypeUserID:
		return false
	}

	panic(fmt.Sprintf("unsupported OIDC authorization type: %s", authType))
}
