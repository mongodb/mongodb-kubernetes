package connectionstring

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

func TestBuild_DatabaseInPath(t *testing.T) {
	build := func(database string) string {
		options := Options{
			AuthenticationModes: []string{util.SCRAM},
			Hostnames:           []string{"host:27017"},
			Username:            "user",
			Password:            "password",
			Database:            database,
		}
		return options.Build(SchemeMongoDB)
	}

	t.Run("database appears in URI path", func(t *testing.T) {
		cs := build("mydb")
		assert.Contains(t, cs, "/mydb?")
		assert.NotContains(t, cs, "/?")
	})

	t.Run("no database produces empty path segment", func(t *testing.T) {
		cs := build("")
		assert.Contains(t, cs, "/?")
	})

	t.Run("reserved URI characters in database are percent-encoded", func(t *testing.T) {
		cs := build("my?db#name")
		assert.Contains(t, cs, "/my%3Fdb%23name?")
		assert.NotContains(t, cs, "/my?db#name?")
	})

	t.Run("space in database is percent-encoded", func(t *testing.T) {
		cs := build("my db")
		assert.Contains(t, cs, "/my%20db?")
	})
}

func TestBuild_CredentialEncoding(t *testing.T) {
	scram := func(username, password string, modes ...string) string {
		if len(modes) == 0 {
			modes = []string{util.SCRAM}
		}
		options := Options{
			AuthenticationModes: modes,
			Hostnames:           []string{"host:27017"},
			Username:            username,
			Password:            password,
		}
		return options.Build(SchemeMongoDB)
	}

	// userinfo extracts the "username:password" segment from the connection string.
	userinfo := func(conn string) string {
		start := strings.Index(conn, "://") + 3
		end := strings.Index(conn, "@")
		return conn[start:end]
	}

	// Space must be encoded as %20 (not +), so pymongo's unquote_plus and the Go driver both decode it correctly.
	t.Run("password with space", func(t *testing.T) {
		assert.Contains(t, userinfo(scram("user", "p w")), "p%20w")
	})
	t.Run("username with space", func(t *testing.T) {
		assert.Contains(t, userinfo(scram("u s", "password")), "u%20s")
	})

	// Plus must be encoded as %2B; pymongo uses unquote_plus which would otherwise decode + as space.
	t.Run("password with plus", func(t *testing.T) {
		assert.Contains(t, userinfo(scram("user", "p+w")), "p%2Bw")
	})

	// Structural separators must be encoded to avoid breaking URI parsing.
	t.Run("colon in username", func(t *testing.T) {
		assert.Contains(t, userinfo(scram("us:er", "password")), "us%3Aer:password")
	})
	t.Run("at sign in password", func(t *testing.T) {
		assert.Contains(t, userinfo(scram("user", "p@w")), "p%40w")
	})

	// No credentials — no userinfo segment in the output.
	t.Run("no auth without SCRAM", func(t *testing.T) {
		assert.NotContains(t, scram("user", "password", "X509"), "@")
	})

	t.Run("no auth with empty password", func(t *testing.T) {
		assert.NotContains(t, scram("user", ""), "@")
	})

	t.Run("no auth with empty username", func(t *testing.T) {
		assert.NotContains(t, scram("", "password"), "@")
	})

	t.Run("no auth for the external database", func(t *testing.T) {
		options := Options{
			AuthenticationModes: []string{util.SCRAM},
			Hostnames:           []string{"host:27017"},
			Username:            "user",
			Password:            "password",
			AuthDatabase:        ptr.To(constants.ExternalDB),
		}
		assert.NotContains(t, options.Build(SchemeMongoDB), "@")
		assert.NotContains(t, options.Build(SchemeMongoDB), "authMechanism")
		assert.Contains(t, options.Build(SchemeMongoDB), "authSource=$external")
	})
}

func TestBuild_Parameters(t *testing.T) {
	base := Options{
		Hostnames:    []string{"host:27017"},
		IsReplicaSet: true,
		Name:         "my-rs",
		IsTLSEnabled: true,
		Params:       OperatorParams(),
	}

	t.Run("operator parameters are included", func(t *testing.T) {
		cs := base.Build(SchemeMongoDB)
		assert.Contains(t, cs, "connectTimeoutMS=20000")
		assert.Contains(t, cs, "serverSelectionTimeoutMS=20000")
		assert.Contains(t, cs, "replicaSet=my-rs")
		assert.Contains(t, cs, "ssl=true")
	})

	t.Run("parameters are sorted for a stable URI", func(t *testing.T) {
		cs := base.Build(SchemeMongoDB)
		query := cs[strings.Index(cs, "?")+1:]
		keys := strings.Split(query, "&")
		for i := 1; i < len(keys); i++ {
			assert.Less(t, keys[i-1], keys[i])
		}
	})

	t.Run("caller supplied parameters override the derived ones", func(t *testing.T) {
		options := base
		options.Params["replicaSet"] = "other-rs"
		options.Params["ssl"] = "false"
		cs := options.Build(SchemeMongoDB)
		assert.Contains(t, cs, "replicaSet=other-rs")
		assert.Contains(t, cs, "ssl=false")
	})

	t.Run("caller supplied parameters are passed through as is", func(t *testing.T) {
		options := base
		options.Params = map[string]string{"ssl": "false"}
		cs := options.Build(SchemeMongoDB)
		assert.Contains(t, cs, "ssl=false")
		assert.NotContains(t, cs, "tls=")
	})

	t.Run("user parameters override operator parameters", func(t *testing.T) {
		options := base
		options.Params["readPreference"] = "primary"
		options.UserParams = map[string]string{"readPreference": "secondary"}
		assert.Contains(t, options.Build(SchemeMongoDB), "readPreference=secondary")
	})

	t.Run("scram derives auth source and mechanism", func(t *testing.T) {
		options := base
		options.AuthenticationModes = []string{util.SCRAM}
		options.Version = "4.2.0"
		options.Username = "user"
		options.Password = "password"
		cs := options.Build(SchemeMongoDB)
		assert.Contains(t, cs, "authSource=admin")
		assert.Contains(t, cs, "authMechanism=SCRAM-SHA-256")
	})

	t.Run("no auth parameters without credentials", func(t *testing.T) {
		options := base
		options.AuthenticationModes = []string{util.SCRAM}
		options.Version = "4.2.0"
		cs := options.Build(SchemeMongoDB)
		assert.NotContains(t, cs, "authSource")
		assert.NotContains(t, cs, "authMechanism")
	})

	t.Run("a caller supplied authMechanism is kept for $external users", func(t *testing.T) {
		options := base
		options.AuthenticationModes = []string{util.SCRAM}
		options.Version = "4.2.0"
		options.AuthDatabase = ptr.To(constants.ExternalDB)
		options.Params = map[string]string{"authMechanism": "MONGODB-X509"}
		cs := options.Build(SchemeMongoDB)
		assert.Contains(t, cs, "authSource=$external")
		assert.Contains(t, cs, "authMechanism=MONGODB-X509")
	})
}

func TestBuild_SRV_TLSParameter(t *testing.T) {
	srvOptions := func(tlsEnabled bool) Options {
		return Options{
			Service:       "my-rs-svc",
			Namespace:     "my-ns",
			ClusterDomain: "cluster.local",
			IsReplicaSet:  true,
			Name:          "my-rs",
			IsTLSEnabled:  tlsEnabled,
		}
	}

	t.Run("SRV without TLS includes ssl=false", func(t *testing.T) {
		assert.Contains(t, srvOptions(false).Build(SchemeMongoDBSRV), "ssl=false")
	})

	t.Run("SRV with TLS includes ssl=true", func(t *testing.T) {
		cs := srvOptions(true).Build(SchemeMongoDBSRV)
		assert.Contains(t, cs, "ssl=true")
		assert.NotContains(t, cs, "ssl=false")
	})

	t.Run("standard connection without TLS includes ssl=false", func(t *testing.T) {
		options := Options{Hostnames: []string{"host:27017"}, IsReplicaSet: true, Name: "my-rs"}
		assert.Contains(t, options.Build(SchemeMongoDB), "ssl=false")
	})

	t.Run("a caller supplied ssl parameter overrides the derived one", func(t *testing.T) {
		options := srvOptions(false)
		options.Params = map[string]string{"ssl": "true"}
		cs := options.Build(SchemeMongoDBSRV)
		assert.Contains(t, cs, "ssl=true")
		assert.NotContains(t, cs, "ssl=false")
		assert.NotContains(t, cs, "tls=")
	})
}
