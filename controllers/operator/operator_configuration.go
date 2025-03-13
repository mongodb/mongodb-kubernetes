package operator

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

// operatorNamespace returns the current namespace where the Operator is deployed
func operatorNamespace() string {
	return env.ReadOrPanic(util.CurrentNamespace) // nolint:forbidigo
}
