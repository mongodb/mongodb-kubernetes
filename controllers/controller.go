package controllers

import (
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"strings"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var crdFuncMap map[string][]func(manager.Manager) error
var crdMultiFuncMap map[string][]func(manager.Manager, map[string]cluster.Cluster) error

var (
	mdb      = &mdbv1.MongoDB{}
	mdbu     = &user.MongoDBUser{}
	om       = &omv1.MongoDBOpsManager{}
	mdbmulti = &mdbmultiv1.MongoDBMulti{}
)

func init() {
	crdFuncMap = buildCrdFunctionMap()
	crdMultiFuncMap = buildCrdMultiFunctionMap()
}

// buildCrdFunctionMap creates a map which maps the name of the Custom
// Resource Definition to a function which adds the corresponding function
// to a manager.Manager for single cluster reconcilers
func buildCrdFunctionMap() map[string][]func(manager.Manager) error {
	return map[string][]func(manager.Manager) error{
		strings.ToLower(mdb.GetPlural()): {
			operator.AddStandaloneController,
			operator.AddReplicaSetController,
			operator.AddShardedClusterController,
			mdb.AddValidationToManager,
		},
		strings.ToLower(mdbu.GetPlural()): {
			operator.AddMongoDBUserController,
		},
		strings.ToLower(om.GetPlural()): {
			operator.AddOpsManagerController,
			om.AddValidationToManager,
		},
	}
}

// buildCrdMultiFunctionMap create a map which maps the name of the Custom
// Resource Definition to a function which adds the corresponding function
// to a manager.Manager and slice of cluster objects for single multi cluster reconcilers
func buildCrdMultiFunctionMap() map[string][]func(manager.Manager, map[string]cluster.Cluster) error {
	return map[string][]func(manager.Manager, map[string]cluster.Cluster) error{
		strings.ToLower(mdbmulti.GetPlural()): {
			operator.AddMultiReplicaSetController,
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
func AddToManager(m manager.Manager, crdsToWatchStr string, c map[string]cluster.Cluster) ([]string, error) {
	crdsToWatch := getCRDsToWatch(crdsToWatchStr)
	var addSingleToManagerFuncs []func(manager.Manager) error
	var addMultiToManagerFuncs []func(manager.Manager, map[string]cluster.Cluster) error

	for _, ctr := range crdsToWatch {
		addSingleToManagerFuncs = append(addSingleToManagerFuncs, crdFuncMap[strings.Trim(strings.ToLower(ctr), " ")]...)
		addMultiToManagerFuncs = append(addMultiToManagerFuncs, crdMultiFuncMap[strings.Trim(strings.ToLower(ctr), " ")]...)
	}

	for _, f := range addSingleToManagerFuncs {
		if err := f(m); err != nil {
			return []string{}, err
		}
	}

	for _, f := range addMultiToManagerFuncs {
		if err := f(m, c); err != nil {
			return []string{}, err
		}
	}
	return crdsToWatch, nil
}
