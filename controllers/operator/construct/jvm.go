package construct

import (
	"fmt"
	"strings"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	opsManagerPodMemPercentage = 90
	oneMB                      = 1048576
)

// setJvmArgsEnvVars sets the correct environment variables for JVM size parameters.
// This method must be invoked on the final version of the StatefulSet (after user statefulSet spec
// was merged)
func setJvmArgsEnvVars(om omv1.MongoDBOpsManagerSpec, containerName string, sts *appsv1.StatefulSet) error {
	jvmParamsEnvVars, err := buildJvmParamsEnvVars(om, containerName, sts.Spec.Template)
	if err != nil {
		return err
	}
	// pass Xmx java parameter to container (note, that we don't need to sort the env variables again
	// as the jvm params order is consistent)
	for _, envVar := range jvmParamsEnvVars {
		omContainer := container.GetByName(containerName, sts.Spec.Template.Spec.Containers)
		omContainer.Env = append(omContainer.Env, envVar)
	}
	return nil
}

// buildJvmParamsEnvVars returns a slice of corev1.EnvVars that should be added to the Backup Daemon
// or Ops Manager containers.
func buildJvmParamsEnvVars(m omv1.MongoDBOpsManagerSpec, containerName string, template corev1.PodTemplateSpec) ([]corev1.EnvVar, error) {
	mmsJvmEnvVar := corev1.EnvVar{Name: util.MmsJvmParamEnvVar}
	backupJvmEnvVar := corev1.EnvVar{Name: util.BackupDaemonJvmParamEnvVar}
	omContainer := container.GetByName(containerName, template.Spec.Containers)
	// calculate xmx from container's memory limit
	memLimits := omContainer.Resources.Limits.Memory()
	maxPodMem, err := getPercentOfQuantityAsInt(*memLimits, opsManagerPodMemPercentage)
	if err != nil {
		return []corev1.EnvVar{}, fmt.Errorf("error calculating xmx from pod mem: %e", err)
	}

	// calculate xms from container's memory request if it is set, otherwise xms=xmx
	memRequests := omContainer.Resources.Requests.Memory()
	minPodMem, err := getPercentOfQuantityAsInt(*memRequests, opsManagerPodMemPercentage)
	if err != nil {
		return []corev1.EnvVar{}, fmt.Errorf("error calculating xms from pod mem: %e", err)
	}

	// if only one of mem limits/requests is set, use that value for both xmx & xms
	if minPodMem == 0 {
		minPodMem = maxPodMem
	}
	if maxPodMem == 0 {
		maxPodMem = minPodMem
	}

	memParams := fmt.Sprintf("-Xmx%dm -Xms%dm", maxPodMem, minPodMem)
	mmsJvmEnvVar.Value = buildJvmEnvVar(m.JVMParams, memParams)
	backupJvmEnvVar.Value = buildJvmEnvVar(m.Backup.JVMParams, memParams)

	return []corev1.EnvVar{mmsJvmEnvVar, backupJvmEnvVar}, nil
}

// getPercentOfQuantityAsInt returns the percentage of a given quantity as an int.
func getPercentOfQuantityAsInt(q resource.Quantity, percent int) (int, error) {
	quantityAsInt, canConvert := q.AsInt64()
	if !canConvert {
		// the container's mem can't be converted to int64, use default of 5G
		podMem, err := resource.ParseQuantity(util.DefaultMemoryOpsManager)
		quantityAsInt, canConvert = podMem.AsInt64()
		if err != nil {
			return 0, err
		}
		if !canConvert {
			return 0, fmt.Errorf("cannot convert %s to int64", podMem.String())
		}
	}
	percentage := float64(percent) / 100.0

	return int(float64(quantityAsInt)*percentage) / oneMB, nil
}

// buildJvmEnvVar returns the string representation of the JVM environment variable
func buildJvmEnvVar(customParams []string, containerMemParams string) string {
	jvmParams := strings.Join(customParams, " ")

	// if both mem limits and mem requests are unset/have value 0, we don't want to override om's default JVM xmx/xms params
	if strings.Contains(containerMemParams, "-Xmx0m") {
		return jvmParams
	}

	if strings.Contains(jvmParams, "Xmx") {
		return jvmParams
	}

	if jvmParams != "" {
		jvmParams += " "
	}

	return jvmParams + containerMemParams
}
