package construct

import (
	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// Static architecture mounts the init-database image read-only as an OCI image volume, so the agent runs the launcher
// and probes straight from the image.
const (
	initDatabaseVolumeName = "mongodb-init-database"
	initDatabaseMountPath  = "/mnt/init-database"

	staticAgentLauncherPath         = initDatabaseMountPath + "/scripts/agent-launcher.sh"
	staticDatabaseLivenessProbeCmd  = initDatabaseMountPath + "/probes/probe.sh"
	staticDatabaseReadinessProbeCmd = initDatabaseMountPath + "/probes/readinessprobe"
)

// staticAgentLauncherCommand runs the launcher from the init-database image. The launcher sources its lib from the
// same directory, so nothing is copied or linked first. The check fails with the cause when the cluster dropped the
// image volume, instead of exec failing on a missing file.
var staticAgentLauncherCommand = []string{
	"/bin/bash", "-c",
	initDatabaseImageVolumeCheck(staticAgentLauncherPath) + "exec " + staticAgentLauncherPath,
}

func initDatabaseImageVolumeCheck(requiredFile string) string {
	return "[ -f " + requiredFile + " ] || { echo \"ERROR: " + initDatabaseMountPath + " is empty. The static architecture needs a Kubernetes cluster with image volume support.\" >&2; exit 1; }; "
}

func initDatabaseImageVolume(initDatabaseImage string) corev1.Volume {
	return imageVolume(initDatabaseVolumeName, initDatabaseImage)
}

func initDatabaseImageVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      initDatabaseVolumeName,
		MountPath: initDatabaseMountPath,
		ReadOnly:  true,
	}
}

// WithoutHolderContainer drops the holder container that static Pods no longer run. Pod templates written for older
// operator versions may still name it, and merging it would add a container without an image.
func WithoutHolderContainer(spec appsv1.StatefulSetSpec, log *zap.SugaredLogger) appsv1.StatefulSetSpec {
	containers := make([]corev1.Container, 0, len(spec.Template.Spec.Containers))
	for _, c := range spec.Template.Spec.Containers {
		if c.Name == util.AgentContainerUtilitiesName {
			if log == nil {
				log = zap.S()
			}
			log.Warnf("Ignoring container %q from the user-supplied podTemplate, static Pods no longer run it", c.Name)
			continue
		}
		containers = append(containers, c)
	}
	spec.Template.Spec.Containers = containers
	return spec
}
