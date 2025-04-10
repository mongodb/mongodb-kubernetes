// Presents a builder to programmatically build a MongoDB connection string.
//
// We are waiting for a more consistent solution to this, based on a
// ConnString structure.
//
// https://jira.mongodb.org/browse/GODRIVER-2226

package connectionstring

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

type ConnectionStringBuilder interface {
	BuildConnectionString(userName, password string, scheme Scheme, connectionParams map[string]string) string
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

	multiClusterHosts []string

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

func (b *builder) SetMultiClusterHosts(multiClusterHosts []string) *builder {
	b.multiClusterHosts = multiClusterHosts
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
	if stringutil.Contains(b.authenticationModes, util.SCRAM) &&
		b.username != "" && b.password != "" {

		userAuth = fmt.Sprintf("%s:%s@", url.QueryEscape(b.username), url.QueryEscape(b.password))
	}

	var uri string
	if b.scheme == SchemeMongoDBSRV {
		uri = fmt.Sprintf("mongodb+srv://%s", userAuth)
		uri += fmt.Sprintf("%s.%s.svc.%s", b.service, b.namespace, b.clusterDomain)
	} else {
		uri = fmt.Sprintf("mongodb://%s", userAuth)
		var hostnames []string
		if len(b.multiClusterHosts) > 0 {
			hostnames = b.multiClusterHosts
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

	authSource, authMechanism := authSourceAndMechanism(b.authenticationModes, b.version)
	if authSource != "" && authMechanism != "" {
		connectionParams["authSource"] = authSource
		connectionParams["authMechanism"] = authMechanism
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
	uri += "/?"

	// sorting parameters to make a url stable
	sort.Strings(keys)
	for _, k := range keys {
		uri += fmt.Sprintf("%s=%s&", k, connectionParams[k])
	}
	return strings.TrimSuffix(uri, "&")
}

func Builder() *builder {
	return &builder{
		port:             util.MongoDbDefaultPort,
		connectionParams: map[string]string{"connectTimeoutMS": "20000", "serverSelectionTimeoutMS": "20000"},
	}
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

	return authSource, authMechanism
}
