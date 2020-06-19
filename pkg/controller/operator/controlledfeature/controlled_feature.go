package controlledfeature

import "github.com/10gen/ops-manager-kubernetes/pkg/util"

type ControlledFeature struct {
	ManagementSystem ManagementSystem `json:"externalManagementSystem"`
	Policies         []Policy         `json:"policies"`
}

type ManagementSystem struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type PolicyType string

const (
	ExternallyManaged               PolicyType = "EXTERNALLY_MANAGED_LOCK"
	DisableAuthenticationMechanisms PolicyType = "DISABLE_AUTHENTICATION_MECHANISMS"
)

type Policy struct {
	PolicyType     PolicyType `json:"policy"`
	DisabledParams []string   `json:"disabledParams"`
}

func NewControlledFeature(options ...func(*ControlledFeature)) *ControlledFeature {
	cf := &ControlledFeature{
		ManagementSystem: ManagementSystem{
			Name:    util.OperatorName,
			Version: util.OperatorVersion,
		},
	}

	for _, op := range options {
		op(cf)
	}

	return cf
}

func FullyRestrictive() *ControlledFeature {
	return NewControlledFeature(OptionExternallyManaged, OptionDisableAuthenticationMechanism)
}

func OptionExternallyManaged(cf *ControlledFeature) {
	cf.Policies = append(cf.Policies, Policy{PolicyType: ExternallyManaged, DisabledParams: make([]string, 0)})
}

func OptionDisableAuthenticationMechanism(cf *ControlledFeature) {
	cf.Policies = append(cf.Policies, Policy{PolicyType: DisableAuthenticationMechanisms, DisabledParams: make([]string, 0)})
}
