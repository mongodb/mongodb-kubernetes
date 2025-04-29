package versionutil

import (
	"regexp"
	"strings"

	"github.com/blang/semver"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

var semverRegex *regexp.Regexp

// StringToSemverVersion returns semver.Version for the 'version' provided as a string.
// Important: this method is a bit hacky as ignores everything after patch and must be used only when needed
// (so far only for creating the semver for OM version as this was needed to support IBM)
func StringToSemverVersion(version string) (semver.Version, error) {
	v, err := semver.Make(version)
	if err != nil {
		// Regex adapted from https://semver.org/#is-there-a-suggested-regular-expression-regex-to-check-a-semver-string
		// but removing the parts after the patch
		if semverRegex == nil {
			semverRegex = regexp.MustCompile(`^(?P<major>0|[1-9]\d*)\.(?P<minor>0|[1-9]\d*)\.(?P<patch>0|[1-9]\d*)?(-|$)`)
		}
		result := semverRegex.FindStringSubmatch(version)
		if len(result) < 4 {
			return semver.Version{}, xerrors.Errorf("Ops Manager Status spec.version %s is invalid", version)
		}
		// Concatenate Major.Minor.Patch
		v, err = semver.Make(result[1] + "." + result[2] + "." + result[3])
	}
	return v, err
}

// StaticContainersOperatorVersion gets the Operator version for the Static Containers Architecture based on
// util.OperatorVersion variable which is set during the build time. For development, it's "latest".
func StaticContainersOperatorVersion() string {
	if len(util.OperatorVersion) == 0 {
		return "latest"
	}
	return util.OperatorVersion
}

type OpsManagerVersion struct {
	VersionString string
}

func (v OpsManagerVersion) Semver() (semver.Version, error) {
	if v.IsCloudManager() {
		return semver.Version{}, nil
	}

	versionParts := strings.Split(v.VersionString, ".") // [4 2 4 56729 20191105T2247Z]
	if len(versionParts) < 3 {
		return semver.Version{}, nil
	}

	sv, err := semver.Make(strings.Join(versionParts[:3], "."))
	if err != nil {
		return semver.Version{}, err
	}

	return sv, nil
}

func (v OpsManagerVersion) IsCloudManager() bool {
	return strings.HasPrefix(strings.ToLower(v.VersionString), "v")
}

func (v OpsManagerVersion) IsUnknown() bool {
	return v.VersionString == ""
}

func (v OpsManagerVersion) String() string {
	return v.VersionString
}

// GetVersionFromOpsManagerApiHeader returns the major, minor and patch version from the string
// which is returned in the header of all Ops Manager responses in the form of:
// gitHash=f7bdac406b7beceb1415fd32c81fc64501b6e031; versionString=4.2.4.56729.20191105T2247Z
func GetVersionFromOpsManagerApiHeader(versionString string) string {
	if versionString == "" || !strings.Contains(versionString, "versionString=") {
		return ""
	}

	splitString := strings.Split(versionString, "versionString=")

	if len(splitString) == 2 {
		return splitString[1]
	}

	return ""
}

func IsDowngrade(oldV string, currentV string) bool {
	oldVersion, err := semver.Make(oldV)
	if err != nil {
		return false
	}
	currentVersion, err := semver.Make(currentV)
	if err != nil {
		return false
	}
	return oldVersion.GT(currentVersion)
}
