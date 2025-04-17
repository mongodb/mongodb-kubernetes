package authentication

import (
	"fmt"
	"slices"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/oidc"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
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
		// TODO merge configs with existing ones, and don't overwrite read only values
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
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC)) && oidcProviderConfigsEqual(ac.OIDCProviderConfigs, opts.OIDCProviderConfigs)
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

func oidcProviderConfigEqual(l oidc.ProviderConfig, r oidc.ProviderConfig) bool {
	if l.AuthNamePrefix != r.AuthNamePrefix {
		return false
	}

	if l.Audience != r.Audience {
		return false
	}

	if l.IssuerUri != r.IssuerUri {
		return false
	}

	if !slices.Equal(l.RequestedScopes, r.RequestedScopes) {
		return false
	}

	if l.UserClaim != r.UserClaim {
		return false
	}

	if l.GroupsClaim != r.GroupsClaim {
		return false
	}

	if l.SupportsHumanFlows != r.SupportsHumanFlows {
		return false
	}

	if l.UseAuthorizationClaim != r.UseAuthorizationClaim {
		return false
	}

	return true
}

func MapOIDCProviderConfigs(oidcProviderConfigs []mdbv1.OIDCProviderConfig) []oidc.ProviderConfig {
	if len(oidcProviderConfigs) == 0 {
		return nil
	}

	result := make([]oidc.ProviderConfig, len(oidcProviderConfigs))
	for i, providerConfig := range oidcProviderConfigs {
		result[i] = oidc.ProviderConfig{
			AuthNamePrefix:        providerConfig.ConfigurationName,
			Audience:              providerConfig.Audience,
			IssuerUri:             providerConfig.IssuerURI,
			ClientId:              providerConfig.ClientId,
			RequestedScopes:       providerConfig.RequestedScopes,
			UserClaim:             providerConfig.UserClaim,
			GroupsClaim:           providerConfig.GroupsClaim,
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
