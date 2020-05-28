package agents

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// UpgradeAll iterates over all the MongoDB resources registered in the system the agents for MongoDB objects registered in the system if necessary
func UpgradeAll(client client.Client, omConnectionFactory om.ConnectionFactory, watchNamespace string) {
	// TODO
	// perform periodically:
	// 1. read all mongodb objects with Running status
	// - from the single namespace if 'watchNamespace' != *
	// - from all namespaces if 'watchNamespace' == * (seems we'll have to iterate over all namespaces)
	// 2. for each of them:
	// - create the om connection object (project.ReadOrCreateProject())
	// - make the api request to update the agent to the latest one

}
