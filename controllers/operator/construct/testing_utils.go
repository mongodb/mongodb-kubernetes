package construct

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func GetPodEnvOptions() func(options *DatabaseStatefulSetOptions) {
	return func(options *DatabaseStatefulSetOptions) {
		options.PodVars = &env.PodEnvVars{ProjectID: "abcd"}
	}
}
