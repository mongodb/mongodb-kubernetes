package construct

import (
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// Static architecture mounts the init-database image read-only as an OCI image volume, so the agent runs the launcher
// and probes straight from the image instead of reaching into a holder container through /proc/<pid>/root.
const (
	initDatabaseVolumeName = "mongodb-init-database"
	initDatabaseMountPath  = "/mnt/init-database"

	staticAgentLauncherPath         = initDatabaseMountPath + "/scripts/agent-launcher.sh"
	staticDatabaseLivenessProbeCmd  = initDatabaseMountPath + "/probes/probe.sh"
	staticDatabaseReadinessProbeCmd = initDatabaseMountPath + "/probes/readinessprobe"
)

// The launcher sources its lib from /opt/scripts. Image volumes only support directory subPaths, so the lib is linked
// there instead of mounted, which keeps every init-database image version working. Clusters without image volume
// support silently drop the volume, so fail with a message that names the cause.
var staticAgentLauncherCommand = []string{
	"/bin/bash", "-c",
	"[ -f " + staticAgentLauncherPath + " ] || { echo \"ERROR: " + initDatabaseMountPath + " is empty. The static architecture needs a Kubernetes cluster with image volume support.\" >&2; exit 1; }; " +
		"ln -sf " + initDatabaseMountPath + "/scripts/agent-launcher-lib.sh " + PvcMountPathScripts + "/agent-launcher-lib.sh && exec " + staticAgentLauncherPath,
}

func initDatabaseImageVolume(initDatabaseImage string) corev1.Volume {
	return corev1.Volume{
		Name: initDatabaseVolumeName,
		VolumeSource: corev1.VolumeSource{
			Image: &corev1.ImageVolumeSource{
				Reference:  initDatabaseImage,
				PullPolicy: corev1.PullPolicy(env.ReadOrPanic(util.ImagePullPolicyEnv)), // nolint:forbidigo
			},
		},
	}
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
