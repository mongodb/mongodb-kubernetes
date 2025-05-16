package operator

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// operatorNamespace returns the current namespace where the Operator is deployed
func operatorNamespace() string {
	return env.ReadOrPanic(util.CurrentNamespace) // nolint:forbidigo
}
