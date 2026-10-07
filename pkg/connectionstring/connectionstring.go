// Presents a builder to programmatically build a MongoDB connection string.
//
// The request for a driver provided ConnString structure was declined in
// GODRIVER-2226, so the operator keeps its own resource agnostic builder.

package connectionstring

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/authtypes"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// Scheme states the connection string format.
// https://www.mongodb.com/docs/manual/reference/connection-string/#connection-string-formats
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
	IsSecurityTLSConfigEnabled() bool
}

// ForSpec returns the options for the given resource specification, filled
// with the fields the spec carries. The resource identity (name, namespace,
// service, hostnames), the external domain and the topology specific
// overrides stay with the caller.
func ForSpec(spec Spec, options Options) Options {
	options.Replicas = spec.Replicas()
	options.Version = spec.GetMongoDBVersion()
	options.AuthenticationModes = spec.GetSecurityAuthenticationModes()
	options.ClusterDomain = spec.GetClusterDomain()
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

	// Database is the database placed in the URI path. When empty the path
	// segment is empty and the connecting client picks its default database.
	Database string

	// Params are operator provided connection parameters. They override the
	// parameters derived from the resource configuration.
	Params map[string]string

	// UserParams come from user controlled configuration. They override
	// Params.
	UserParams map[string]string

	// AuthDatabase is the database the user authenticates against and becomes the
	// authSource of the connection string. A nil value leaves the authSource to the
	// authentication modes, an empty string is a database that is explicitly empty.
	// A $external database also drops the derived authMechanism, since the client
	// supplies the mechanism at connect time.
	AuthDatabase *string
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
	o.Username = user.Username
	o.Password = password
	o.AuthDatabase = &user.Database
	o.Database = user.ConnectionStringDatabase
	o.UserParams = StringParams(user.ConnectionStringOptions)
	return o
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
// operator and user provided ones, in increasing order of priority.
func (o Options) mergedParams() map[string]string {
	params := map[string]string{
		"ssl": strconv.FormatBool(o.IsTLSEnabled),
	}
	if o.IsReplicaSet {
		params["replicaSet"] = o.Name
	}

	// The authentication parameters only apply to connection strings that carry
	// credentials. A driver rejects an authMechanism without a username.
	if o.authenticated() {
		authSource, authMechanism := authSourceAndMechanism(o.AuthenticationModes, o.Version)
		if authSource != "" {
			params["authSource"] = authSource
		}
		if authMechanism != "" {
			params["authMechanism"] = authMechanism
		}
	}

	// The user's database is the authSource of a user connection string, even when
	// the database is empty.
	if o.AuthDatabase != nil {
		params["authSource"] = *o.AuthDatabase
	}

	maps.Copy(params, o.Params)
	maps.Copy(params, o.UserParams)

	return params
}

// authenticated reports whether the connection string carries credentials:
// SCRAM is enabled and the request provides a username and a password without
// targeting the $external database.
func (o Options) authenticated() bool {
	scramEnabled := stringutil.Contains(o.AuthenticationModes, util.SCRAM) ||
		stringutil.Contains(o.AuthenticationModes, util.SCRAMSHA1) ||
		stringutil.Contains(o.AuthenticationModes, util.SCRAMSHA256)
	externalAuth := o.AuthDatabase != nil && *o.AuthDatabase == constants.ExternalDB
	return scramEnabled && !externalAuth && o.Username != "" && o.Password != ""
}

// userinfo returns the "user:password@" prefix to use in the connection
// string, or the empty string when the request is unauthenticated or targets
// the $external database.
func (o Options) userinfo() string {
	if !o.authenticated() {
		return ""
	}
	return fmt.Sprintf("%s:%s@", stringutil.EncodeUserinfoComponent(o.Username), stringutil.EncodeUserinfoComponent(o.Password))
}

// Build builds a connection string with the given scheme.
func (o Options) Build(scheme Scheme) string {
	params := o.mergedParams()

	var uri string
	if scheme == SchemeMongoDBSRV {
		uri = fmt.Sprintf("mongodb+srv://%s", o.userinfo())
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
		uri = fmt.Sprintf("mongodb://%s", o.userinfo())
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

	uri += "/" + stringutil.EncodeUserinfoComponent(o.Database) + "?"

	// sorted parameters make the url stable
	for _, k := range slices.Sorted(maps.Keys(params)) {
		uri += fmt.Sprintf("%s=%s&", k, params[k])
	}
	return strings.TrimSuffix(uri, "&")
}

// authSourceAndMechanism returns the authSource and authMechanism implied by
// the configured authentication modes. The modes are applied in a fixed order,
// SCRAM then SCRAM-SHA-256 then SCRAM-SHA-1, so the last one configured wins.
// Only the bare SCRAM mode depends on the deployment version, which decides
// between SCRAM-SHA-256 and SCRAM-SHA-1.
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
