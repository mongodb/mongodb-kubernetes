package versionutil

import (
	"fmt"
	"regexp"

	"github.com/blang/semver"
)

var semverRegex *regexp.Regexp

func StringToSemverVersion(version string) (semver.Version, error) {
	v, err := semver.Make(version)
	if err != nil {
		// Regex adapted from https://semver.org/#is-there-a-suggested-regular-expression-regex-to-check-a-semver-string
		// but removing the parts after the patch
		if semverRegex == nil {
			semverRegex = regexp.MustCompile(`^(?P<major>0|[1-9]\d*)\.(?P<minor>0|[1-9]\d*)\.(?P<patch>0|[1-9]\d*)?(-|$)`)
		}
		result := semverRegex.FindStringSubmatch(version)
		if result == nil || len(result) < 4 {
			return semver.Version{}, fmt.Errorf("Ops Manager Status spec.version %s is invalid", version)
		}
		// Concatenate Major.Minor.Patch
		v, err = semver.Make(result[1] + "." + result[2] + "." + result[3])
	}
	return v, err
}
