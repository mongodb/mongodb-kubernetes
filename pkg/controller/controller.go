package controller

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, operator.AddStandaloneController)
	AddToManagerFuncs = append(AddToManagerFuncs, operator.AddReplicaSetController)
	AddToManagerFuncs = append(AddToManagerFuncs, operator.AddShardedClusterController)
	AddToManagerFuncs = append(AddToManagerFuncs, operator.AddProjectController)
	AddToManagerFuncs = append(AddToManagerFuncs, operator.AddMongoDBUserController)
}

// AddToManagerFuncs is a list of functions to add all Controllers to the Manager
var AddToManagerFuncs []func(manager.Manager) error

// AddToManager adds all Controllers to the Manager
func AddToManager(m manager.Manager) error {
	for _, f := range AddToManagerFuncs {
		if err := f(m); err != nil {
			return err
		}
	}
	return nil
}
