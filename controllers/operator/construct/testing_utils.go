package construct

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

func GetPodEnvOptions() func(options *DatabaseStatefulSetOptions) {
	return func(options *DatabaseStatefulSetOptions) {
		options.PodVars = &env.PodEnvVars{ProjectID: "abcd"}
	}
}
