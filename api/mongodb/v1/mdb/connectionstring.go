// Connection string building for the MongoDB resource, mirroring the
// MongoDBCommunity implementation in the community operator.

package mdb

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstring"
)

// connectionOptions fills the resource level connection string settings.
// The hostnames, when non empty, replace the generated pod DNS names.
func (m *MongoDB) connectionOptions(hostnames []string) connectionstring.Options {
	name := m.Name
	// Both connection strings are built from the mongos tier for a sharded cluster, so the external
	// domain must be resolved from spec.mongos.externalAccess too, not only from the top-level field.
	// EffectiveExternalDomain still falls back to spec.externalAccess for the mongos tier. The member
	// cluster is left empty on purpose: a connection string is cluster-agnostic, so per-cluster
	// clusterSpecList domains are not honoured here.
	externalDomain := m.Spec.GetExternalDomain()
	if m.IsShardedCluster() {
		name = m.MongosRsName()
		externalDomain = m.Spec.EffectiveExternalDomain(m.Spec.MongosSpec, TierMongos, "")
	} else if m.IsReplicaSet() {
		name = m.GetReplicaSetName()
	}

	options := connectionstring.ForSpec(&m.Spec, connectionstring.Options{
		Name:         name,
		Namespace:    m.Namespace,
		Service:      m.ServiceName(),
		Port:         m.Spec.GetAdditionalMongodConfig().GetPortOrDefault(),
		IsReplicaSet: m.Spec.ResourceType == ReplicaSet,
		Hostnames:    hostnames,
		Params:       connectionstring.OperatorParams(),
	})
	// The sharded topology can resolve a different domain than the top level spec.
	options.ExternalDomain = externalDomain
	return options
}

// ConnectionOptions fills the resource level connection string settings.
func (m *MongoDB) ConnectionOptions() connectionstring.Options {
	return m.connectionOptions(nil)
}

// ConnectionOptionsWithHostnames fills the connection string settings with
// hostnames computed by the caller for topologies that need it (sharded
// clusters and external members). When non empty, the hostnames replace the
// generated pod DNS names.
func (m *MongoDB) ConnectionOptionsWithHostnames(hostnames []string) connectionstring.Options {
	return m.connectionOptions(hostnames)
}
