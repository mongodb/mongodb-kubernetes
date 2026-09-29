// Presents a builder to programmatically build a MongoDB connection string.
//
// We are waiting for a more consistent solution to this, based on a
// ConnString structure.
//
// https://jira.mongodb.org/browse/GODRIVER-2226

package connectionstring

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// Scheme states the connection string format.
// https://docs.mongodb.com/manual/reference/connection-string/#connection-string-formats
type Scheme string

const (
	SchemeMongoDB    Scheme = "mongodb"
	SchemeMongoDBSRV Scheme = "mongodb+srv"
)

// ConnectionStringBuilder is implemented by resources that know how to fill
// in the resource specific parts of a connection string request.
type ConnectionStringBuilder interface {
	BuildConnectionString(userName, password, connectionStringDatabase string, scheme Scheme, connectionParams map[string]string) string
}

// ProtectedConnectionParams are connection string parameters that the operator
// controls through other means, so any value supplied for them in user or
// resource configuration is dropped. Without this a user could for example
// disable TLS for their own connection while the deployment still requires it.
var ProtectedConnectionParams = map[string]struct{}{
	"replicaSet": {},
	"ssl":        {},
}

// Options carries everything needed to build a MongoDB connection string.
// It is resource agnostic: single cluster, multi cluster and community
// resources all fill one of these and build through the same code path.
type Options struct {
	Name      string
	Namespace string
	Service   string

	Username string
	Password string
	Replicas int
	Port     int32
	Version  string

	AuthenticationModes []string
	ClusterDomain       string
	ExternalDomain      *string
	IsReplicaSet        bool
	IsTLSEnabled        bool

	// Hostnames replaces the generated DNS names when non empty. Entries
	// must already include the port.
	Hostnames []string

	// Database is the database placed in the URI path. When empty the
	// DefaultDatabase is used instead, allowing callers to opt into a
	// default database such as "admin" for user connection strings.
	Database        string
	DefaultDatabase string

	// Params are operator provided connection parameters. They override the
	// parameters derived from the resource configuration. Protected keys are
	// dropped.
	Params map[string]string

	// UserParams come from user controlled configuration. They override
	// Params and protected keys are dropped.
	UserParams map[string]string
}

// Database returns the database to place in the URI path.
func (o Options) database() string {
	if o.Database != "" {
		return o.Database
	}
	return o.DefaultDatabase
}

// port returns the mongod port, defaulting to the standard one when unset.
func (o Options) port() int32 {
	if o.Port == 0 {
		return util.MongoDbDefaultPort
	}
	return o.Port
}

// mergedParams combines the parameters derived from the resource with the
// operator and user provided ones, in increasing order of priority. Keys in
// ProtectedConnectionParams never make it through from user input.
func (o Options) mergedParams() map[string]string {
	params := map[string]string{
		"connectTimeoutMS":         "20000",
		"serverSelectionTimeoutMS": "20000",
	}
	if o.IsReplicaSet {
		params["replicaSet"] = o.Name
	}
	if o.IsTLSEnabled {
		params["ssl"] = "true"
	} else {
		params["ssl"] = "false"
	}

	authSource, authMechanism := authSourceAndMechanism(o.AuthenticationModes, o.Version)
	if authSource != "" {
		params["authSource"] = authSource
	}

	maps.Copy(params, filterProtectedParams(o.Params))

	// Omit authMechanism for $external users; the client supplies the
	// mechanism at connect time.
	if params["authSource"] == constants.ExternalDB {
		delete(params, "authMechanism")
	} else if _, ok := params["authMechanism"]; !ok && authMechanism != "" {
		params["authMechanism"] = authMechanism
	}

	maps.Copy(params, filterProtectedParams(o.UserParams))

	return params
}

// userinfo returns the "user:password@" prefix to use in the connection
// string, or the empty string when the request is unauthenticated or targets
// the $external database.
func (o Options) userinfo(params map[string]string) string {
	scramEnabled := stringutil.Contains(o.AuthenticationModes, util.SCRAM) ||
		stringutil.Contains(o.AuthenticationModes, util.SCRAMSHA1)
	if !scramEnabled || o.Username == "" || o.Password == "" {
		return ""
	}
	if params["authSource"] == constants.ExternalDB {
		return ""
	}
	return fmt.Sprintf("%s:%s@", stringutil.EncodeUserinfoComponent(o.Username), stringutil.EncodeUserinfoComponent(o.Password))
}

// Build builds a connection string with the given scheme.
func (o Options) Build(scheme Scheme) string {
	params := o.mergedParams()

	var uri string
	if scheme == SchemeMongoDBSRV {
		uri = fmt.Sprintf("mongodb+srv://%s", o.userinfo(params))
		// When an external domain is configured, the internal service FQDN is not reachable from
		// outside the cluster, so we publish the external domain instead. Provisioning the
		// _mongodb._tcp SRV records in that zone is the customer's responsibility, just as it is
		// for the A records that externalAccess already depends on.
		if o.ExternalDomain != nil && *o.ExternalDomain != "" {
			uri += *o.ExternalDomain
		} else {
			uri += fmt.Sprintf("%s.%s.svc.%s", o.Service, o.Namespace, o.ClusterDomain)
		}
	} else {
		uri = fmt.Sprintf("mongodb://%s", o.userinfo(params))
		var hostnames []string
		if len(o.Hostnames) > 0 {
			hostnames = o.Hostnames
		} else {
			hostnames, _ = dns.GetDNSNames(o.Name, o.Service, o.Namespace, o.ClusterDomain, o.Replicas, o.ExternalDomain)
			for i, h := range hostnames {
				hostnames[i] = fmt.Sprintf("%s:%d", h, o.port())
			}
		}
		uri += strings.Join(hostnames, ",")
	}

	uri += "/" + stringutil.EncodeUserinfoComponent(o.database()) + "?"

	// sorting parameters to make a url stable
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		uri += fmt.Sprintf("%s=%s&", k, params[k])
	}
	return strings.TrimSuffix(uri, "&")
}

// builder offers a fluent API over Options for callers that build the
// connection string incrementally.
type builder struct {
	options Options
	scheme  Scheme
}

func Builder() *builder {
	return &builder{
		options: Options{
			Port: util.MongoDbDefaultPort,
		},
	}
}

func (b *builder) SetName(name string) *builder {
	b.options.Name = name
	return b
}

func (b *builder) SetNamespace(namespace string) *builder {
	b.options.Namespace = namespace
	return b
}

func (b *builder) SetUsername(username string) *builder {
	b.options.Username = username
	return b
}

func (b *builder) SetPassword(password string) *builder {
	b.options.Password = password
	return b
}

func (b *builder) SetReplicas(replicas int) *builder {
	b.options.Replicas = replicas
	return b
}

func (b *builder) SetService(service string) *builder {
	b.options.Service = service
	return b
}

func (b *builder) SetPort(port int32) *builder {
	b.options.Port = port
	return b
}

func (b *builder) SetVersion(version string) *builder {
	b.options.Version = version
	return b
}

func (b *builder) SetAuthenticationModes(authenticationModes []string) *builder {
	b.options.AuthenticationModes = authenticationModes
	return b
}

func (b *builder) SetClusterDomain(clusterDomain string) *builder {
	b.options.ClusterDomain = clusterDomain
	return b
}

func (b *builder) SetExternalDomain(externalDomain *string) *builder {
	b.options.ExternalDomain = externalDomain
	return b
}

func (b *builder) SetIsReplicaSet(isReplicaSet bool) *builder {
	b.options.IsReplicaSet = isReplicaSet
	return b
}

func (b *builder) SetIsTLSEnabled(isTLSEnabled bool) *builder {
	b.options.IsTLSEnabled = isTLSEnabled
	return b
}

func (b *builder) SetHostnames(hostnames []string) *builder {
	b.options.Hostnames = hostnames
	return b
}

func (b *builder) SetConnectionStringDatabase(connectionStringDatabase string) *builder {
	b.options.Database = connectionStringDatabase
	return b
}

func (b *builder) SetScheme(scheme Scheme) *builder {
	b.scheme = scheme
	return b
}

func (b *builder) SetConnectionParams(cParams map[string]string) *builder {
	maps.Copy(b.options.Params, cParams)
	return b
}

// Build builds a new connection string from the builder.
func (b *builder) Build() string {
	return b.options.Build(b.scheme)
}

func filterProtectedParams(params map[string]string) map[string]string {
	filtered := make(map[string]string, len(params))
	for key, value := range params {
		if _, protected := ProtectedConnectionParams[key]; !protected {
			filtered[key] = value
		}
	}
	return filtered
}

// authSourceAndMechanism returns AuthSource and AuthMechanism.
func authSourceAndMechanism(authenticationModes []string, version string) (string, string) {
	var authSource string
	var authMechanism string
	if stringutil.Contains(authenticationModes, util.SCRAM) {
		authSource = util.DefaultUserDatabase

		comparison, err := util.CompareVersions(version, util.MinimumScramSha256MdbVersion)
		if err != nil {
			return "", ""
		}
		if comparison < 0 {
			authMechanism = "SCRAM-SHA-1"
		} else {
			authMechanism = "SCRAM-SHA-256"
		}
	}

	if stringutil.Contains(authenticationModes, util.SCRAMSHA1) {
		authMechanism = "SCRAM-SHA-1"
	}

	return authSource, authMechanism
}
