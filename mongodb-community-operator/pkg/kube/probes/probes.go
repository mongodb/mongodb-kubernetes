package probes

import corev1 "k8s.io/api/core/v1"

type Modification func(*corev1.Probe)

func Apply(funcs ...Modification) Modification {
	return func(probe *corev1.Probe) {
		for _, f := range funcs {
			f(probe)
		}
	}
}

func New(funcs ...Modification) corev1.Probe {
	probe := corev1.Probe{}
	for _, f := range funcs {
		f(&probe)
	}
	return probe
}

func WithExecCommand(cmd []string) Modification {
	return func(probe *corev1.Probe) {
		if probe.Exec == nil {
			probe.Exec = &corev1.ExecAction{}
		}
		probe.Exec.Command = cmd
	}
}

func WithFailureThreshold(failureThreshold int32) Modification {
	return func(probe *corev1.Probe) {
		probe.FailureThreshold = failureThreshold
	}
}

func WithInitialDelaySeconds(initialDelaySeconds int32) Modification {
	return func(probe *corev1.Probe) {
		probe.InitialDelaySeconds = initialDelaySeconds
	}
}

func WithSuccessThreshold(successThreshold int32) Modification {
	return func(probe *corev1.Probe) {
		probe.SuccessThreshold = successThreshold
	}
}

func WithPeriodSeconds(periodSeconds int32) Modification {
	return func(probe *corev1.Probe) {
		probe.PeriodSeconds = periodSeconds
	}
}

func WithTimeoutSeconds(timeoutSeconds int32) Modification {
	return func(probe *corev1.Probe) {
		probe.TimeoutSeconds = timeoutSeconds
	}
}

func WithHandler(handler corev1.ProbeHandler) Modification {
	return func(probe *corev1.Probe) {
		probe.ProbeHandler = handler
	}
}
