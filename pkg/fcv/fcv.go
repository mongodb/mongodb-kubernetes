package fcv

import "github.com/mongodb/mongodb-kubernetes/pkg/util"

func CalculateFeatureCompatibilityVersion(currentVersionFromCR string, fcvFromStatus string, fcvFromCR *string) string {
	majorMinorVersionFromCR, setVersion, _ := util.MajorMinorVersion(currentVersionFromCR)

	// if fcvFromCR has been set by the customer
	if fcvFromCR != nil {
		convertedFcv := *fcvFromCR

		// If the fcvFromCR is set to util.AlwaysMatchVersionFCV, return the current version as is.
		// It does not matter whether we upgraded or downgraded
		if convertedFcv == util.AlwaysMatchVersionFCV {
			return majorMinorVersionFromCR
		}

		return convertedFcv
	}

	// It is the first deployment; fcvFromStatus is empty since it's not been set yet.
	// We can use the currentVersionFromCR as FCV
	if fcvFromStatus == "" {
		return majorMinorVersionFromCR
	}

	lastAppliedMajorMinorVersion, setLastAppliedVersion, _ := util.MajorMinorVersion(fcvFromStatus + ".0")
	// We don't support jumping 2 versions at once in fcvFromCR, in this case we need to use the higher fcvFromCR.
	// We don't need to check the other way around, since mdb does not support downgrading by 2 versions.
	if setVersion.Major-setLastAppliedVersion.Major >= 2 {
		return majorMinorVersionFromCR
	}

	// if no value is set, we want to use the lowest between new and old
	comparisons, err := util.CompareVersions(currentVersionFromCR, fcvFromStatus+".0")
	if err != nil {
		return ""
	}

	// return the smaller one from both
	if comparisons == -1 {
		return majorMinorVersionFromCR
	}

	return lastAppliedMajorMinorVersion
}
