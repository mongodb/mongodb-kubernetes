// Connection string building for the MongoDB resource, mirroring the
// MongoDBCommunity implementation in the community operator.

package mdb

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstring"
)

// connectionStringExternalDomain returns the external domain a connection string of this
// resource uses. Both connection strings of a sharded cluster are built from the mongos tier,
// so its effective domain applies, and that resolution still falls back to spec.externalAccess.
// Replica sets and standalones only ever read the top level field. The member cluster is left
// empty on purpose, a connection string is cluster agnostic and does not honour the per member
// cluster domains from clusterSpecList.
func (m *MongoDB) connectionStringExternalDomain() *string {
	if m.IsShardedCluster() {
		return m.Spec.EffectiveExternalDomain(m.Spec.MongosSpec, TierMongos, "")
	}
	return m.Spec.GetExternalDomain()
}

// connectionOptions fills the resource level connection string settings.
// The hostnames, when non empty, replace the generated pod DNS names.
func (m *MongoDB) connectionOptions(hostnames []string) connectionstring.Options {
	name := m.Name
	if m.IsShardedCluster() {
		name = m.MongosRsName()
	} else if m.IsReplicaSet() {
		name = m.GetReplicaSetName()
	}

	options := connectionstring.ForSpec(&m.Spec, connectionstring.Options{
		Name:         name,
		Namespace:    m.Namespace,
		Service:      m.ServiceName(),
		Port:         m.Spec.GetAdditionalMongodConfig().GetPortOrDefault(),
		IsReplicaSet: m.IsReplicaSet(),
		Hostnames:    hostnames,
		Params:       connectionstring.OperatorParams(),
	})
	options.ExternalDomain = m.connectionStringExternalDomain()
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
