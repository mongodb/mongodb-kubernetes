package authentication

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

type oidcAuthMechanism struct{}

func (o *oidcAuthMechanism) GetName() MechanismName {
	return MongoDBOIDC
}

func (o *oidcAuthMechanism) EnableAgentAuthentication(_ context.Context, _ kubernetesClient.Client, _ om.Connection, _ Options, _ *zap.SugaredLogger) error {
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

// oidcFieldMapping holds both directions of a single field conversion so they stay co-located.
// Asymmetric fields (AuthorizationMethod, AuthorizationType) are handled inline below.
type oidcFieldMapping struct {
	toAC func(*mdbv1.OIDCProviderConfig, *oidc.ProviderConfig)
	toCR func(*oidc.ProviderConfig, *mdbv1.OIDCProviderConfig)
}

var oidcFieldMappings = []oidcFieldMapping{
	{func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) { a.AuthNamePrefix = c.ConfigurationName },
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) { c.ConfigurationName = a.AuthNamePrefix }},
	{func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) { a.Audience = c.Audience },
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) { c.Audience = a.Audience }},
	{func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) { a.IssuerUri = c.IssuerURI },
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) { c.IssuerURI = a.IssuerUri }},
	{func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) { a.ClientId = c.ClientId },
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) { c.ClientId = a.ClientId }},
	{func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) { a.RequestedScopes = c.RequestedScopes },
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) { c.RequestedScopes = a.RequestedScopes }},
	{func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) { a.UserClaim = c.UserClaim },
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) { c.UserClaim = a.UserClaim }},
	{func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) { a.GroupsClaim = c.GroupsClaim },
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) { c.GroupsClaim = a.GroupsClaim }},
	{
		func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) {
			a.SupportsHumanFlows = c.AuthorizationMethod == mdbv1.OIDCAuthorizationMethodWorkforceIdentityFederation
			if c.AuthorizationMethod != mdbv1.OIDCAuthorizationMethodWorkforceIdentityFederation &&
				c.AuthorizationMethod != mdbv1.OIDCAuthorizationMethodWorkloadIdentityFederation {
				panic(fmt.Sprintf("unsupported OIDC authorization method: %s", c.AuthorizationMethod))
			}
		},
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) {
			if a.SupportsHumanFlows {
				c.AuthorizationMethod = mdbv1.OIDCAuthorizationMethodWorkforceIdentityFederation
			} else {
				c.AuthorizationMethod = mdbv1.OIDCAuthorizationMethodWorkloadIdentityFederation
			}
		},
	},
	{
		func(c *mdbv1.OIDCProviderConfig, a *oidc.ProviderConfig) {
			a.UseAuthorizationClaim = c.AuthorizationType == mdbv1.OIDCAuthorizationTypeGroupMembership
			if c.AuthorizationType != mdbv1.OIDCAuthorizationTypeGroupMembership &&
				c.AuthorizationType != mdbv1.OIDCAuthorizationTypeUserID {
				panic(fmt.Sprintf("unsupported OIDC authorization type: %s", c.AuthorizationType))
			}
		},
		func(a *oidc.ProviderConfig, c *mdbv1.OIDCProviderConfig) {
			if a.UseAuthorizationClaim {
				c.AuthorizationType = mdbv1.OIDCAuthorizationTypeGroupMembership
			} else {
				c.AuthorizationType = mdbv1.OIDCAuthorizationTypeUserID
			}
		},
	},
}

// MapOIDCProviderConfigs converts CR OIDC provider configs to the AC representation. See MapACOIDCToProviderConfigs for the reverse.
func MapOIDCProviderConfigs(oidcProviderConfigs []mdbv1.OIDCProviderConfig) []oidc.ProviderConfig {
	if len(oidcProviderConfigs) == 0 {
		return nil
	}
	result := make([]oidc.ProviderConfig, len(oidcProviderConfigs))
	for i := range oidcProviderConfigs {
		for _, f := range oidcFieldMappings {
			f.toAC(&oidcProviderConfigs[i], &result[i])
		}
	}
	return result
}

