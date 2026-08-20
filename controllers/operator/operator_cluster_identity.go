package operator

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// GetOperatorClusterName returns the logical cluster identity of the cluster this operator runs
// on — the name matched against clusterSpecList[].clusterName references in workload CRs. It is
// sourced from the `OPERATOR_CLUSTER_NAME` env var (the `operator.clusterIdentity.clusterName`
// Helm value).
//
// An empty string means the operator has no per-cluster identity and runs in hub-and-spoke mode.
func GetOperatorClusterName() string {
	return env.ReadOrDefault(util.OperatorClusterNameEnv, "") // nolint:forbidigo
}
