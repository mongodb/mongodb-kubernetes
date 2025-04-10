package controlledfeature

import (
	"go.uber.org/zap"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
)

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
	DisableMongodConfig             PolicyType = "DISABLE_SET_MONGOD_CONFIG"
	DisableMongodVersion            PolicyType = "DISABLE_SET_MONGOD_VERSION"
)

type Policy struct {
	PolicyType     PolicyType `json:"policy"`
	DisabledParams []string   `json:"disabledParams"`
}

func newControlledFeature(options ...func(*ControlledFeature)) *ControlledFeature {
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

func OptionExternallyManaged(cf *ControlledFeature) {
	cf.Policies = append(cf.Policies, Policy{PolicyType: ExternallyManaged, DisabledParams: make([]string, 0)})
}

func OptionDisableAuthenticationMechanism(cf *ControlledFeature) {
	cf.Policies = append(cf.Policies, Policy{PolicyType: DisableAuthenticationMechanisms, DisabledParams: make([]string, 0)})
}

func OptionDisableMongodbConfig(disabledParams []string) func(*ControlledFeature) {
	return func(cf *ControlledFeature) {
		cf.Policies = append(cf.Policies, Policy{PolicyType: DisableMongodConfig, DisabledParams: disabledParams})
	}
}

func OptionDisableMongodbVersion(cf *ControlledFeature) {
	cf.Policies = append(cf.Policies, Policy{PolicyType: DisableMongodVersion})
}

type Updater interface {
	UpdateControlledFeature(cf *ControlledFeature) error
}

type Getter interface {
	GetControlledFeature() (*ControlledFeature, error)
}

// EnsureFeatureControls updates the controlled feature based on the provided MongoDB
// resource if the version of Ops Manager supports it
func EnsureFeatureControls(mdb mdbv1.MongoDB, updater Updater, omVersion versionutil.OpsManagerVersion, log *zap.SugaredLogger) workflow.Status {
	if !ShouldUseFeatureControls(omVersion) {
		log.Debugf("Ops Manager version is %s, which does not support Feature Controls API", omVersion)
		return workflow.OK()
	}

	cf := buildFeatureControlsByMdb(mdb)
	log.Debug("Configuring feature controls")
	if err := updater.UpdateControlledFeature(cf); err != nil {
		return workflow.Failed(err)
	}
	return workflow.OK()
}

// ClearFeatureControls cleares the controlled feature if the version of OpsManager supports it
func ClearFeatureControls(updater Updater, omVersion versionutil.OpsManagerVersion, log *zap.SugaredLogger) workflow.Status {
	if !ShouldUseFeatureControls(omVersion) {
		log.Debugf("Ops Manager version is %s, which does not support Feature Controls API", omVersion)
		return workflow.OK()
	}
	cf := newControlledFeature()
	// cf.Policies needs to be an empty list, instead of a nil pointer, for a valid API call.
	cf.Policies = make([]Policy, 0)
	if err := updater.UpdateControlledFeature(cf); err != nil {
		return workflow.Failed(err)
	}
	return workflow.OK()
}

// ShouldUseFeatureControls returns a boolean indicating if the feature controls
// should be enabled for the given version of Ops Manager
func ShouldUseFeatureControls(version versionutil.OpsManagerVersion) bool {
	// if we were not successfully able to determine a version
	// from Ops Manager, we can assume it is a legacy version
	if version.IsUnknown() {
		return false
	}

	// feature controls are enabled on Cloud Manager, e.g. v20191112
	if version.IsCloudManager() {
		return true
	}

	if _, err := version.Semver(); err != nil {
		return false
	}

	return true
}
