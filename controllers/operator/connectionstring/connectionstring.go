// Presents a builder to programmatically build a MongoDB connection string.
//
// We are waiting for a more consistent solution to this, based on a
// ConnString structure.
//
// https://jira.mongodb.org/browse/GODRIVER-2226

package connectionstring

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

type ConnectionStringBuilder interface {
	BuildConnectionString(userName, password, specDb string, scheme Scheme, connectionParams map[string]string) string
}

// Scheme states the connection string format.
// https://docs.mongodb.com/manual/reference/connection-string/#connection-string-formats
type Scheme string

const (
	SchemeMongoDB    Scheme = "mongodb"
	SchemeMongoDBSRV Scheme = "mongodb+srv"
)

// builder is a struct that we'll use to build a connection string.
type builder struct {
	name      string
	namespace string

	username string
	password string
	replicas int
	port     int32
	service  string
	version  string

	authenticationModes []string
	clusterDomain       string
	externalDomain      *string
	isReplicaSet        bool
	isTLSEnabled        bool

	hostnames []string
	database  string

	scheme           Scheme
	connectionParams map[string]string
}

func (b *builder) SetName(name string) *builder {
	b.name = name
	return b
}

func (b *builder) SetNamespace(namespace string) *builder {
	b.namespace = namespace
	return b
}

func (b *builder) SetUsername(username string) *builder {
	b.username = username
	return b
}

func (b *builder) SetPassword(password string) *builder {
	b.password = password
	return b
}

func (b *builder) SetReplicas(replicas int) *builder {
	b.replicas = replicas
	return b
}

func (b *builder) SetService(service string) *builder {
	b.service = service
	return b
}

func (b *builder) SetPort(port int32) *builder {
	b.port = port
	return b
}

func (b *builder) SetVersion(version string) *builder {
	b.version = version
	return b
}

func (b *builder) SetAuthenticationModes(authenticationModes []string) *builder {
	b.authenticationModes = authenticationModes
	return b
}

func (b *builder) SetClusterDomain(clusterDomain string) *builder {
	b.clusterDomain = clusterDomain
	return b
}

func (b *builder) SetExternalDomain(externalDomain *string) *builder {
	b.externalDomain = externalDomain
	return b
}

func (b *builder) SetIsReplicaSet(isReplicaSet bool) *builder {
	b.isReplicaSet = isReplicaSet
	return b
}

func (b *builder) SetIsTLSEnabled(isTLSEnabled bool) *builder {
	b.isTLSEnabled = isTLSEnabled
	return b
}

func (b *builder) SetHostnames(hostnames []string) *builder {
	b.hostnames = hostnames
	return b
}

func (b *builder) SetDatabase(database string) *builder {
	b.database = database
	return b
}

func (b *builder) SetScheme(scheme Scheme) *builder {
	b.scheme = scheme
	return b
}

func (b *builder) SetConnectionParams(cParams map[string]string) *builder {
	for key, value := range cParams {
		b.connectionParams[key] = value
	}
	return b
}

// Build builds a new connection string from the builder.
func (b *builder) Build() string {
	var userAuth string
	scramEnabled := stringutil.Contains(b.authenticationModes, util.SCRAM) ||
		stringutil.Contains(b.authenticationModes, util.SCRAMSHA1)
	if scramEnabled && b.username != "" && b.password != "" {
		userAuth = fmt.Sprintf("%s:%s@", stringutil.EncodeUserinfoComponent(b.username), stringutil.EncodeUserinfoComponent(b.password))
	}

	var uri string
	if b.scheme == SchemeMongoDBSRV {
		uri = fmt.Sprintf("mongodb+srv://%s", userAuth)
		uri += fmt.Sprintf("%s.%s.svc.%s", b.service, b.namespace, b.clusterDomain)
	} else {
		uri = fmt.Sprintf("mongodb://%s", userAuth)
		var hostnames []string
		if len(b.hostnames) > 0 {
			hostnames = b.hostnames
		} else {
			hostnames, _ = dns.GetDNSNames(b.name, b.service, b.namespace, b.clusterDomain, b.replicas, b.externalDomain)
			for i, h := range hostnames {
				hostnames[i] = fmt.Sprintf("%s:%d", h, b.port)
			}
		}
		uri += strings.Join(hostnames, ",")
	}

	connectionParams := make(map[string]string)
	if b.isReplicaSet {
		connectionParams["replicaSet"] = b.name
	}

	if b.isTLSEnabled {
		connectionParams["ssl"] = "true"
	}

	if src := resolveAuthSource(b.authenticationModes, b.database); src != "" {
		connectionParams["authSource"] = src
	}
	if mech := resolveAuthMechanism(b.authenticationModes, b.version); mech != "" {
		connectionParams["authMechanism"] = mech
	}

	// Merge received (b.connectionParams) on top of local (connectionParams)
	// Make sure that received parameters have priority.
	for k, v := range b.connectionParams {
		connectionParams[k] = v
	}

	var keys []string
	for k := range connectionParams {
		keys = append(keys, k)
	}
	uri += "/" + PathDatabase(b.database) + "?"

	// sorting parameters to make a url stable
	sort.Strings(keys)
	for _, k := range keys {
		uri += fmt.Sprintf("%s=%s&", k, connectionParams[k])
	}
	return strings.TrimSuffix(uri, "&")
}

// PathDatabase returns the database to use in the URI path component.
// $external is an auth-only pseudo-database and must not appear in the path.
func PathDatabase(db string) string {
	if db == "$external" {
		return ""
	}
	return db
}

func Builder() *builder {
	return &builder{
		port:             util.MongoDbDefaultPort,
		connectionParams: map[string]string{"connectTimeoutMS": "20000", "serverSelectionTimeoutMS": "20000"},
	}
}

// resolveAuthSource returns the authSource for the URI. specDb takes precedence;
// when empty, any SCRAM variant falls back to the default user database.
func resolveAuthSource(authenticationModes []string, specDb string) string {
	if specDb != "" {
		return specDb
	}
	if stringutil.Contains(authenticationModes, util.SCRAM) || stringutil.Contains(authenticationModes, util.SCRAMSHA1) {
		return util.DefaultUserDatabase
	}
	return ""
}

// resolveAuthMechanism returns the authMechanism for the URI.
// SCRAM-SHA-1 mode takes precedence over version-based SCRAM negotiation.
func resolveAuthMechanism(authenticationModes []string, version string) string {
	if stringutil.Contains(authenticationModes, util.SCRAMSHA1) {
		return "SCRAM-SHA-1"
	}
	if stringutil.Contains(authenticationModes, util.SCRAM) {
		comparison, err := util.CompareVersions(version, util.MinimumScramSha256MdbVersion)
		if err != nil {
			return ""
		}
		if comparison < 0 {
			return "SCRAM-SHA-1"
		}
		return "SCRAM-SHA-256"
	}
	return ""
}
