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

func FullyRestrictive() *ControlledFeature {
	return &ControlledFeature{
		ManagementSystem: ManagementSystem{
			Name:    util.OperatorName,
			Version: util.OperatorVersion,
		},
		Policies: []Policy{
			{
				PolicyType:     ExternallyManaged,
				DisabledParams: make([]string, 0),
			},
			{
				PolicyType:     DisableAuthenticationMechanisms,
				DisabledParams: make([]string, 0),
			},
		},
	}
}
