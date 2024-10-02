package controllers

import (
	"context"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator"
)

var crdFuncMap map[string][]func(context.Context, manager.Manager, map[string]cluster.Cluster) error

var (
	mdb      = &mdbv1.MongoDB{}
	mdbu     = &user.MongoDBUser{}
	om       = &omv1.MongoDBOpsManager{}
	mdbmulti = &mdbmultiv1.MongoDBMultiCluster{}
)

func init() {
	crdFuncMap = buildCrdFunctionMap()
}

// buildCrdFunctionMap creates a map which maps the name of the Custom
// Resource Definition to a function which adds the corresponding function
// to a manager.Manager for single cluster reconcilers
func buildCrdFunctionMap() map[string][]func(context.Context, manager.Manager, map[string]cluster.Cluster) error {
	return map[string][]func(context.Context, manager.Manager, map[string]cluster.Cluster) error{
		strings.ToLower(mdb.GetPlural()): {
			operator.AddStandaloneController,
			operator.AddReplicaSetController,
			operator.AddShardedClusterController,
			mdb.AddValidationToManager,
		},
		strings.ToLower(om.GetPlural()): {
			operator.AddOpsManagerController,
			om.AddValidationToManager,
		},
		strings.ToLower(mdbmulti.GetPlural()): {
			operator.AddMultiReplicaSetController,
			mdbmulti.AddValidationToManager,
		},
		strings.ToLower(mdbu.GetPlural()): {
			operator.AddMongoDBUserController,
		},
	}
}

// getCRDsToWatch returns the CRDs which the operator will register
// and recognize. It will default to all the CRDs we have.
func getCRDsToWatch(watchCRDStr string) []string {
	defaultCRDstoWatch := []string{
		strings.ToLower(mdb.GetPlural()),
		strings.ToLower(mdbu.GetPlural()),
		strings.ToLower(om.GetPlural()),
	}
	if watchCRDStr == "" {
		return defaultCRDstoWatch
	}
	return strings.Split(watchCRDStr, ",")
}

// AddToManager adds all Controllers to the Manager
func AddToManager(ctx context.Context, m manager.Manager, crdsToWatchStr string, c map[string]cluster.Cluster) ([]string, error) {
	crdsToWatch := getCRDsToWatch(crdsToWatchStr)

	var addCRDFuncs []func(context.Context, manager.Manager, map[string]cluster.Cluster) error

	for _, ctr := range crdsToWatch {
		addCRDFuncs = append(addCRDFuncs, crdFuncMap[strings.Trim(strings.ToLower(ctr), " ")]...)
	}

	for _, f := range addCRDFuncs {
		if err := f(ctx, m, c); err != nil {
			return []string{}, err
		}
	}

	return crdsToWatch, nil
}
