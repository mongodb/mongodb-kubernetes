package connectionstring

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

func TestBuild_DatabaseInPath(t *testing.T) {
	build := func(database, defaultDatabase string) string {
		options := Options{
			AuthenticationModes: []string{util.SCRAM},
			Hostnames:           []string{"host:27017"},
			Username:            "user",
			Password:            "password",
			Database:            database,
			DefaultDatabase:     defaultDatabase,
		}
		return options.Build(SchemeMongoDB)
	}

	t.Run("database appears in URI path", func(t *testing.T) {
		cs := build("mydb", "")
		assert.Contains(t, cs, "/mydb?")
		assert.NotContains(t, cs, "/?")
	})

	t.Run("no database produces empty path segment", func(t *testing.T) {
		cs := build("", "")
		assert.Contains(t, cs, "/?")
	})

	t.Run("default database fills an empty path", func(t *testing.T) {
		cs := build("", "admin")
		assert.Contains(t, cs, "/admin?")
	})

	t.Run("explicit database wins over the default", func(t *testing.T) {
		cs := build("mydb", "admin")
		assert.Contains(t, cs, "/mydb?")
	})

	t.Run("reserved URI characters in database are percent-encoded", func(t *testing.T) {
		cs := build("my?db#name", "")
		assert.Contains(t, cs, "/my%3Fdb%23name?")
		assert.NotContains(t, cs, "/my?db#name?")
	})

	t.Run("space in database is percent-encoded", func(t *testing.T) {
		cs := build("my db", "")
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
			Params:              map[string]string{"authSource": constants.ExternalDB},
		}
		assert.NotContains(t, options.Build(SchemeMongoDB), "@")
		assert.NotContains(t, options.Build(SchemeMongoDB), "authMechanism")
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
		assert.Contains(t, cs, "tls=true")
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
		options.Params["tls"] = "false"
		cs := options.Build(SchemeMongoDB)
		assert.Contains(t, cs, "replicaSet=other-rs")
		assert.Contains(t, cs, "tls=false")
	})

	t.Run("caller supplied parameters are passed through as is", func(t *testing.T) {
		options := base
		options.Params = map[string]string{"tls": "false"}
		cs := options.Build(SchemeMongoDB)
		assert.Contains(t, cs, "tls=false")
		assert.NotContains(t, cs, "ssl=")
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
		cs := options.Build(SchemeMongoDB)
		assert.Contains(t, cs, "authSource=admin")
		assert.Contains(t, cs, "authMechanism=SCRAM-SHA-256")
	})

	t.Run("a caller supplied authMechanism is kept for $external users", func(t *testing.T) {
		options := base
		options.AuthenticationModes = []string{util.SCRAM}
		options.Version = "4.2.0"
		options.Params = map[string]string{"authSource": constants.ExternalDB, "authMechanism": "MONGODB-X509"}
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

	t.Run("SRV without TLS includes tls=false", func(t *testing.T) {
		assert.Contains(t, srvOptions(false).Build(SchemeMongoDBSRV), "tls=false")
	})

	t.Run("SRV with TLS includes tls=true", func(t *testing.T) {
		cs := srvOptions(true).Build(SchemeMongoDBSRV)
		assert.Contains(t, cs, "tls=true")
		assert.NotContains(t, cs, "tls=false")
	})

	t.Run("standard connection without TLS includes tls=false", func(t *testing.T) {
		options := Options{Hostnames: []string{"host:27017"}, IsReplicaSet: true, Name: "my-rs"}
		assert.Contains(t, options.Build(SchemeMongoDB), "tls=false")
	})

	t.Run("a caller supplied tls parameter overrides the derived one", func(t *testing.T) {
		options := srvOptions(false)
		options.Params = map[string]string{"tls": "true"}
		cs := options.Build(SchemeMongoDBSRV)
		assert.Contains(t, cs, "tls=true")
		assert.NotContains(t, cs, "tls=false")
		assert.NotContains(t, cs, "ssl=")
	})
}
