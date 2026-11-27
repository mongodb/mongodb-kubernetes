package construct

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/resource"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	opsManagerPodMemPercentage = 90
	oneMB                      = 1048576
	backupDaemonHealthPort     = 8090
	omAutoHeapEnvVar           = "OM_AUTO_HEAP"
)

// userMaxHeapFlagPrefixes are the JVM flags that mean the user sizes the max heap themselves.
var userMaxHeapFlagPrefixes = []string{"-Xmx", "-XX:MaxHeapSize=", "-XX:MaxRAMPercentage="}

// userInitialHeapSizePrefixes set only the initial heap, so we still need to provide -Xmx.
var userInitialHeapSizePrefixes = []string{"-Xms", "-XX:InitialHeapSize="}

// on its own this leaves the max heap at the JVM default of 25%, so we still provide -Xmx.
// MinRAMPercentage only matters on tiny heaps, so we don't treat it as a heap setting.
var userInitialHeapPercentagePrefixes = []string{"-XX:InitialRAMPercentage="}

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
		return []corev1.EnvVar{}, xerrors.Errorf("error calculating xmx from pod mem: %w", err)
	}

	// calculate xms from container's memory request if it is set, otherwise xms=xmx
	memRequests := omContainer.Resources.Requests.Memory()
	minPodMem, err := getPercentOfQuantityAsInt(*memRequests, opsManagerPodMemPercentage)
	if err != nil {
		return []corev1.EnvVar{}, xerrors.Errorf("error calculating xms from pod mem: %w", err)
	}

	// if only one of mem limits/requests is set, use that value for both xmx & xms
	if minPodMem == 0 {
		minPodMem = maxPodMem
	}
	if maxPodMem == 0 {
		maxPodMem = minPodMem
	}

	// OM's start script sizes the heap itself when OM_AUTO_HEAP=true, but only if we don't pass -Xmx.
	// The Backup Daemon start script has no auto heap, so this only applies to Ops Manager.
	mmsMaxMem, mmsMinMem := maxPodMem, minPodMem
	if isOmAutoHeapEnabled(omContainer.Env) {
		mmsMaxMem, mmsMinMem = 0, 0
	}
	mmsJvmEnvVar.Value = buildJvmEnvVar(m.JVMParams, mmsMaxMem, mmsMinMem)
	backupJvmEnvVar.Value = buildJvmEnvVar(m.Backup.JVMParams, maxPodMem, minPodMem)

	// If a debug port is set, add the JVM debug agent to the JVM parameters
	if debugPort := getDebugPort(omContainer.Ports); debugPort > 0 {
		mmsJvmEnvVar.Value += fmt.Sprintf(" -agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=%d", debugPort)
	}

	// Debug port reports the status of the AppDB, this can be used to determine if the Backup Daemon is in a good state.
	// this exposes a /health endpoint which returns {"sync_db":"OK","backup_db":"OK","mms_db":"OK"}
	// https://github.com/10gen/mms/blob/8c4047d67e157672051d37e340305d89ad20964a/server/src/main/com/xgen/svc/brs/grid/Daemon.java#L926
	backupJvmEnvVar.Value += fmt.Sprintf(" -DDAEMON.DEBUG.PORT=%d", backupDaemonHealthPort)
	return []corev1.EnvVar{mmsJvmEnvVar, backupJvmEnvVar}, nil
}

func getDebugPort(ports []corev1.ContainerPort) int32 {
	if len(ports) == 0 {
		return 0
	}

	for _, p := range ports {
		if p.Name == "debug" {
			return p.ContainerPort
		}
	}

	return 0
}

// getPercentOfQuantityAsInt returns the percentage of a given quantity as an int.
func getPercentOfQuantityAsInt(q resource.Quantity, percent int) (int, error) {
	quantityAsInt, canConvert := q.AsInt64()
	if !canConvert {
		// the container's mem can't be converted to int64, use default of 5G
		podMem, err := resource.ParseQuantity(util.DefaultMemoryOpsManager)
		if err != nil {
			return 0, err
		}
		quantityAsInt, canConvert = podMem.AsInt64()
		if !canConvert {
			return 0, xerrors.Errorf("cannot convert %s to int64", podMem.String())
		}
	}
	percentage := float64(percent) / 100.0

	return int(float64(quantityAsInt)*percentage) / oneMB, nil
}

// isOmAutoHeapEnabled uses the last OM_AUTO_HEAP entry, like Kubernetes does with duplicate env names.
func isOmAutoHeapEnabled(envVars []corev1.EnvVar) bool {
	enabled := false
	for _, envVar := range envVars {
		if envVar.Name == omAutoHeapEnvVar {
			enabled = envVar.Value == "true"
		}
	}
	return enabled
}

// buildJvmEnvVar returns the string representation of the JVM environment variable.
// maxPodMem and minPodMem are our xmx and xms in MB. A maxPodMem of 0 means we don't set either.
func buildJvmEnvVar(customParams []string, maxPodMem, minPodMem int) string {
	jvmParams := strings.Join(customParams, " ")

	// if both mem limits and mem requests are unset/have value 0, we don't want to override om's default JVM xmx/xms params
	if maxPodMem == 0 {
		return jvmParams
	}

	userXmsMB := int64(-1)
	userSetInitialHeapPercentage := false
	for param := range strings.FieldsSeq(jvmParams) {
		if hasAnyPrefix(param, userMaxHeapFlagPrefixes) {
			return jvmParams
		}
		if hasAnyPrefix(param, userInitialHeapPercentagePrefixes) {
			userSetInitialHeapPercentage = true
		}
		for _, prefix := range userInitialHeapSizePrefixes {
			if size, ok := strings.CutPrefix(param, prefix); ok {
				userXmsMB = parseJvmSizeMB(size)
			}
		}
	}

	// with only a user initial heap we still set xmx, otherwise the -Xmx8096m from mms.conf applies.
	// keep the user's xms unless it's above our xmx, since the JVM won't start then.
	memParams := fmt.Sprintf("-Xmx%dm", maxPodMem)
	userXmsFits := userXmsMB >= 0 && userXmsMB <= int64(maxPodMem)
	if !userXmsFits && !userSetInitialHeapPercentage {
		memParams += fmt.Sprintf(" -Xms%dm", minPodMem)
	}

	if jvmParams != "" {
		jvmParams += " "
	}

	return jvmParams + memParams
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// parseJvmSizeMB converts a JVM size like "2g" or "512m" to MB, or returns -1 if it can't parse it.
func parseJvmSizeMB(size string) int64 {
	multipliers := map[byte]int64{'k': 1 << 10, 'm': 1 << 20, 'g': 1 << 30, 't': 1 << 40}
	multiplier := int64(1)
	if size != "" {
		if m, ok := multipliers[strings.ToLower(size[len(size)-1:])[0]]; ok {
			multiplier = m
			size = size[:len(size)-1]
		}
	}
	value, err := strconv.ParseInt(size, 10, 64)
	if err != nil || value < 0 {
		return -1
	}
	return value * multiplier / oneMB
}
