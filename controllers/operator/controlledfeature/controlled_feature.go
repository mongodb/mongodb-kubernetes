package controlledfeature

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"github.com/blang/semver"
	"go.uber.org/zap"
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
		return workflow.Failed(err.Error())
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

	sv, err := version.Semver()
	if err != nil {
		return false
	}

	// feature was closed Oct 01 2019  https://jira.mongodb.org/browse/CLOUDP-46339
	// 4.2.2 was cut Oct 02 2019
	// 4.3.0 was cut Sept 12 2019
	// 4.3.1 was cut Oct 03 2019

	// You need 4.2.2 or later
	// 4.3.1 or later
	// or any 4.4 onwards to make use of Feature Controls

	minFourTwoVersion := semver.Version{
		Major: 4,
		Minor: 2,
		Patch: 2,
	}

	minFourThreeVersion := semver.Version{
		Major: 4,
		Minor: 3,
		Patch: 1,
	}

	minFourFourVersion := semver.Version{
		Major: 4,
		Minor: 4,
		Patch: 0,
	}

	if isFourTwo(sv) {
		return sv.GTE(minFourTwoVersion)
	} else if isFourThree(sv) {
		return sv.GTE(minFourThreeVersion)
	} else if isFourFour(sv) {
		return sv.GTE(minFourFourVersion)
	} else { // otherwise it's an older version, so we will use the tag
		return false
	}
}

func isFourTwo(version semver.Version) bool {
	return isMajorMinor(version, 4, 2)
}

func isFourThree(version semver.Version) bool {
	return isMajorMinor(version, 4, 3)
}

func isFourFour(version semver.Version) bool {
	return isMajorMinor(version, 4, 4)
}

func isMajorMinor(v semver.Version, major, minor uint64) bool {
	return v.Major == major && v.Minor == minor
}
