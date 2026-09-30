// Connection string building for the MongoDBCommunity resource, mirroring the
// MongoDB implementation in the enterprise operator.

package v1

import (
	"maps"

	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/authtypes"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectionstring"
)

// connectionOptions fills the resource level connection string settings.
// The hostnames, when non empty, replace the generated pod DNS names.
func (m *MongoDBCommunity) connectionOptions(hostnames []string) connectionstring.Options {
	options := connectionstring.ForSpec(&m.Spec, connectionstring.Options{
		Name:         m.Name,
		Namespace:    m.Namespace,
		Service:      m.ServiceName(),
		Port:         int32(m.GetMongodConfiguration().GetDBPort()),
		IsReplicaSet: true,
		Hostnames:    hostnames,
		Params:       connectionstring.OperatorParams(),
	})
	maps.Copy(options.Params, connectionstring.StringParams(m.Spec.AdditionalConnectionStringConfig.Object))
	return options
}

// ConnectionOptions fills the resource level connection string settings.
func (m *MongoDBCommunity) ConnectionOptions() connectionstring.Options {
	return m.connectionOptions(nil)
}

// ConnectionOptionsWithHostnames fills the connection string settings with
// hostnames computed by the caller. When non empty, the hostnames replace the
// generated pod DNS names.
func (m *MongoDBCommunity) ConnectionOptionsWithHostnames(hostnames []string) connectionstring.Options {
	return m.connectionOptions(hostnames)
}

// MongoURI returns a mongo uri which can be used to connect to this deployment
func (m *MongoDBCommunity) MongoURI() string {
	return m.connectionOptions(nil).Build(connectionstring.SchemeMongoDB)
}

// MongoSRVURI returns a mongo srv uri which can be used to connect to this deployment
func (m *MongoDBCommunity) MongoSRVURI() string {
	return m.connectionOptions(nil).Build(connectionstring.SchemeMongoDBSRV)
}

// authedOptions extends the resource level options with the authentication
// data of the given user.
func (m *MongoDBCommunity) authedOptions(user authtypes.User, password string) connectionstring.Options {
	return m.connectionOptions(nil).WithUser(user, password)
}

// MongoAuthUserURI returns a mongo uri which can be used to connect to this deployment
// and includes the authentication data for the user
func (m *MongoDBCommunity) MongoAuthUserURI(user authtypes.User, password string) string {
	return m.authedOptions(user, password).Build(connectionstring.SchemeMongoDB)
}

// MongoAuthUserSRVURI returns a mongo srv uri which can be used to connect to this deployment
// and includes the authentication data for the user
func (m *MongoDBCommunity) MongoAuthUserSRVURI(user authtypes.User, password string) string {
	return m.authedOptions(user, password).Build(connectionstring.SchemeMongoDBSRV)
}
