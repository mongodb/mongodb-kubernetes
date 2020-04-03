package controller

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"strings"
)

var crdFuncMap map[string][]func(manager.Manager) error

var (
	mdb  = &mdbv1.MongoDB{}
	mdbu = &mdbv1.MongoDBUser{}
	om   = mdbv1.MongoDBOpsManager{}
)

func init() {
	crdFuncMap = buildCrdFunctionMap()
}

// buildCrdFunctionMap create a map which maps the name of the Custom
// Resource Definition to a function which adds the corresponding function
// to a manager.Manager
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
func AddToManager(m manager.Manager, crdsToWatchStr string) ([]string, error) {
	crdsToWatch := getCRDsToWatch(crdsToWatchStr)
	var addToManagerFuncs []func(manager.Manager) error
	for _, ctr := range crdsToWatch {
		addToManagerFuncs = append(addToManagerFuncs, crdFuncMap[strings.Trim(strings.ToLower(ctr), " ")]...)
	}
	for _, f := range addToManagerFuncs {
		if err := f(m); err != nil {
			return []string{}, err
		}
	}
	return crdsToWatch, nil
}
