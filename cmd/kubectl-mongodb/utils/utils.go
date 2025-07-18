package utils

import (
	"fmt"
	"runtime/debug"
)

func GetBuildInfoString(buildInfo *debug.BuildInfo) string {
	var vcsHash string
	var vcsTime string
	for _, setting := range buildInfo.Settings {
		if setting.Key == "vcs.revision" {
			vcsHash = setting.Value
		}
		if setting.Key == "vcs.time" {
			vcsTime = setting.Value
		}
	}

	buildInfoStr := fmt.Sprintf("\nBuild: %s, %s", vcsHash, vcsTime)
	return buildInfoStr
}
