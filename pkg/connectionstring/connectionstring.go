// Presents a builder to programmatically build a MongoDB connection string.
//
// The request for a driver provided ConnString structure was declined in
// GODRIVER-2226, so the operator keeps its own resource agnostic builder.

package connectionstring

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/authtypes"
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
	ConnectionOptions() Options
}

// Spec is the part of a MongoDB resource specification that influences
// connection strings. The enterprise and community spec types implement it.
type Spec interface {
	Replicas() int
	GetMongoDBVersion() string
	GetSecurityAuthenticationModes() []string
	GetClusterDomain() string
	GetExternalDomain() *string
	IsSecurityTLSConfigEnabled() bool
}

// ForSpec returns the options for the given resource specification, filled
// with the fields the spec carries. The resource identity (name, namespace,
// service, hostnames) and the topology specific overrides stay with the
// caller.
func ForSpec(spec Spec, options Options) Options {
	options.Replicas = spec.Replicas()
	options.Version = spec.GetMongoDBVersion()
	options.AuthenticationModes = spec.GetSecurityAuthenticationModes()
	options.ClusterDomain = spec.GetClusterDomain()
	options.ExternalDomain = spec.GetExternalDomain()
	options.IsTLSEnabled = spec.IsSecurityTLSConfigEnabled()
	return options
}

// StringParams converts a map of arbitrary option values into their string
// representation used in connection string parameters.
func StringParams(options map[string]interface{}) map[string]string {
	params := make(map[string]string, len(options))
	for key, value := range options {
		params[key] = fmt.Sprintf("%v", value)
	}
	return params
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
	// parameters derived from the resource configuration.
	Params map[string]string

	// UserParams come from user controlled configuration. They override
	// Params.
	UserParams map[string]string
}

// BuildStandardAndSRV builds the connection string with both schemes, the
// standard mongodb one and the srv one, in that order.
func (o Options) BuildStandardAndSRV() (standard, srv string) {
	return o.Build(SchemeMongoDB), o.Build(SchemeMongoDBSRV)
}

// WithUser extends the options with the authentication data of the given
// user. The user's database becomes the authSource and the URI path comes
// from the user's connection string database.
func (o Options) WithUser(user authtypes.User, password string) Options {
	if o.Params == nil {
		o.Params = map[string]string{}
	}
	o.Username = user.Username
	o.Password = password
	o.Params["authSource"] = user.Database
	o.Database = user.ConnectionStringDatabase
	o.UserParams = StringParams(user.ConnectionStringOptions)
	return o
}

// Database returns the database to place in the URI path.
func (o Options) database() string {
	if o.Database != "" {
		return o.Database
	}
	return o.DefaultDatabase
}

// OperatorParams returns the connection parameters the operator applies to
// every connection string it builds. Callers include them in Options.Params
// and can override any of them.
func OperatorParams() map[string]string {
	return map[string]string{
		"connectTimeoutMS":         "20000",
		"serverSelectionTimeoutMS": "20000",
	}
}

// mergedParams combines the parameters derived from the resource with the
// operator and user provided ones, in increasing order of priority. The tls
// parameter carries the resource TLS setting unless a caller or user
// parameter overrides it by the same name.
func (o Options) mergedParams() map[string]string {
	params := map[string]string{}
	if o.IsReplicaSet {
		params["replicaSet"] = o.Name
	}
	if o.IsTLSEnabled {
		params["tls"] = "true"
	} else {
		params["tls"] = "false"
	}

	authSource, authMechanism := authSourceAndMechanism(o.AuthenticationModes, o.Version)
	if authSource != "" {
		params["authSource"] = authSource
	}

	maps.Copy(params, o.Params)

	// Omit authMechanism for $external users; the client supplies the
	// mechanism at connect time.
	if params["authSource"] == constants.ExternalDB {
		delete(params, "authMechanism")
	} else if _, ok := params["authMechanism"]; !ok && authMechanism != "" {
		params["authMechanism"] = authMechanism
	}

	maps.Copy(params, o.UserParams)

	return params
}

// userinfo returns the "user:password@" prefix to use in the connection
// string, or the empty string when the request is unauthenticated or targets
// the $external database.
func (o Options) userinfo(params map[string]string) string {
	scramEnabled := stringutil.Contains(o.AuthenticationModes, util.SCRAM) ||
		stringutil.Contains(o.AuthenticationModes, util.SCRAMSHA1) ||
		stringutil.Contains(o.AuthenticationModes, util.SCRAMSHA256)
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
				hostnames[i] = fmt.Sprintf("%s:%d", h, o.Port)
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

	if stringutil.Contains(authenticationModes, util.SCRAMSHA256) {
		authSource = util.DefaultUserDatabase
		authMechanism = "SCRAM-SHA-256"
	}

	if stringutil.Contains(authenticationModes, util.SCRAMSHA1) {
		authMechanism = "SCRAM-SHA-1"
	}

	return authSource, authMechanism
}
