package connectionstring

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestBuild_DatabaseInPath(t *testing.T) {
	build := func(database string) string {
		return Builder().
			SetAuthenticationModes([]string{util.SCRAM}).
			SetHostnames([]string{"host:27017"}).
			SetUsername("user").
			SetPassword("password").
			SetDatabase(database).
			Build()
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
}

func TestBuild_AuthSourcePreservedOnVersionParseError(t *testing.T) {
	cs := Builder().
		SetAuthenticationModes([]string{util.SCRAM}).
		SetHostnames([]string{"host:27017"}).
		SetUsername("user").
		SetPassword("password").
		Build()
	assert.Contains(t, cs, "authSource=admin", "authSource must still be set when version is unknown")
	assert.NotContains(t, cs, "authMechanism=", "authMechanism must not be set when version is unknown")
}

func TestBuild_SSLRespectsIsTLSEnabled(t *testing.T) {
	build := func(isTLSEnabled bool, extraParams map[string]string) string {
		b := Builder()
		b.SetAuthenticationModes([]string{util.SCRAM}).
			SetHostnames([]string{"host:27017"}).
			SetUsername("user").
			SetPassword("password").
			SetIsTLSEnabled(isTLSEnabled).
			SetConnectionParams(extraParams)
		return b.Build()
	}

	t.Run("ssl=true when TLS enabled", func(t *testing.T) {
		assert.Contains(t, build(true, nil), "ssl=true")
	})
}

func TestBuild_CredentialEncoding(t *testing.T) {
	scram := func(username, password string) string {
		b := Builder()
		b.SetAuthenticationModes([]string{util.SCRAM}).
			SetHostnames([]string{"host:27017"}).
			SetUsername(username).
			SetPassword(password)
		return b.Build()
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
		b := Builder()
		b.SetAuthenticationModes([]string{"X509"}).
			SetHostnames([]string{"host:27017"}).
			SetUsername("user").
			SetPassword("password")
		assert.NotContains(t, b.Build(), "@")
	})

	t.Run("no auth with empty password", func(t *testing.T) {
		assert.NotContains(t, scram("user", ""), "@")
	})

	t.Run("no auth with empty username", func(t *testing.T) {
		assert.NotContains(t, scram("", "password"), "@")
	})
}
