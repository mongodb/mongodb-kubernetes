package api

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/pkg/test/httprecorder"
)

// captureOMRequest runs invoke against a recording Ops Manager stub and returns the single request
// the stub received. The admin's user and key are deliberately empty so that the digest challenge
// round-trip in Client.Request is skipped and the stub need not reply 401.
func captureOMRequest(t *testing.T, invoke func(admin OpsManagerAdmin) error) httprecorder.RequestInfo {
	t.Helper()

	srv, rec := httprecorder.NewServer(http.StatusOK, []byte("{}"))
	defer srv.Close()

	require.NoError(t, invoke(NewOmAdmin(srv.URL, "", "", nil)))

	return rec.Last()
}

// backupStoreIDPayloads are the injection primitives an attacker can place in a backup store name
// (spec.backup.{opLogStores,blockStores,s3Stores,s3OpLogStores}[].name), which the Operator uses
// verbatim as the Ops Manager config id.
var backupStoreIDPayloads = []string{
	"x/../../api/public/v1.0/groups",       // '..' traversal out of the admin backup endpoints
	"store/../snapshot/mongoConfigs/other", // traversal into a sibling backup store family
	"store?pretty=true",                    // '?' query injection: truncates the path client-side
	"store#/admin",                         // '#' fragment: truncates the path client-side
}

// TestBackupStoreConfigID_IsPathEscaped asserts that every admin API method which splices a backup
// store id into the request path keeps that id a single URL-escaped path segment. A different
// request-target means the Operator's global-owner-authenticated PUT/DELETE was forged to an
// attacker-chosen Ops Manager endpoint.
func TestBackupStoreConfigID_IsPathEscaped(t *testing.T) {
	sinks := []struct {
		name   string
		prefix string
		invoke func(admin OpsManagerAdmin, id string) error
	}{
		{
			name:   "UpdateOplogStoreConfig",
			prefix: "/api/public/v1.0/admin/backup/oplog/mongoConfigs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.UpdateOplogStoreConfig(backup.DataStoreConfig{Id: id})
			},
		},
		{
			name:   "DeleteOplogStoreConfig",
			prefix: "/api/public/v1.0/admin/backup/oplog/mongoConfigs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.DeleteOplogStoreConfig(id)
			},
		},
		{
			name:   "UpdateS3OplogConfig",
			prefix: "/api/public/v1.0/admin/backup/oplog/s3Configs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.UpdateS3OplogConfig(backup.S3Config{Id: id})
			},
		},
		{
			name:   "DeleteS3OplogStoreConfig",
			prefix: "/api/public/v1.0/admin/backup/oplog/s3Configs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.DeleteS3OplogStoreConfig(id)
			},
		},
		{
			name:   "UpdateBlockStoreConfig",
			prefix: "/api/public/v1.0/admin/backup/snapshot/mongoConfigs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.UpdateBlockStoreConfig(backup.DataStoreConfig{Id: id})
			},
		},
		{
			name:   "DeleteBlockStoreConfig",
			prefix: "/api/public/v1.0/admin/backup/snapshot/mongoConfigs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.DeleteBlockStoreConfig(id)
			},
		},
		{
			name:   "UpdateS3Config",
			prefix: "/api/public/v1.0/admin/backup/snapshot/s3Configs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.UpdateS3Config(backup.S3Config{Id: id})
			},
		},
		{
			name:   "DeleteS3Config",
			prefix: "/api/public/v1.0/admin/backup/snapshot/s3Configs/",
			invoke: func(admin OpsManagerAdmin, id string) error {
				return admin.DeleteS3Config(id)
			},
		},
	}

	for _, sink := range sinks {
		for _, payload := range backupStoreIDPayloads {
			t.Run(sink.name+"/"+payload, func(t *testing.T) {
				got := captureOMRequest(t, func(admin OpsManagerAdmin) error {
					return sink.invoke(admin, payload)
				})
				t.Logf("%s(%q) -> Ops Manager received: %s %s (RawQuery=%q)",
					sink.name, payload, got.Method, got.RequestURI, got.RawQuery)

				assert.Equalf(t, sink.prefix+url.PathEscape(payload), got.RequestURI,
					"the backup store id must remain a single URL-escaped path segment; a different "+
						"request-target means the global-owner-authenticated request was forged to an "+
						"attacker-chosen Ops Manager endpoint")
				assert.Emptyf(t, got.RawQuery,
					"the backup store id must not be able to inject query parameters into the Ops Manager request; got %q", got.RawQuery)
			})
		}
	}
}

// TestBackupStoreConfigID_PercentIsEscaped asserts that an id which already looks percent-encoded is
// escaped again, so that Ops Manager cannot decode it back into a path separator.
func TestBackupStoreConfigID_PercentIsEscaped(t *testing.T) {
	const payload = "a%2Fb"

	got := captureOMRequest(t, func(admin OpsManagerAdmin) error {
		return admin.DeleteOplogStoreConfig(payload)
	})
	assert.Equal(t, "/api/public/v1.0/admin/backup/oplog/mongoConfigs/a%252Fb", got.RequestURI)
	assert.Equal(t, "/api/public/v1.0/admin/backup/oplog/mongoConfigs/"+payload, got.Path,
		"the server must decode the segment back to the literal id, not to a path separator")
}

// TestDaemonConfigPathParams_AreEscaped pins the encoding of the backup daemon config paths. The
// head DB directory used to be escaped with url.QueryEscape at the call site and is now escaped
// centrally with url.PathEscape, so this guards that swap; the hostname was never escaped at all.
func TestDaemonConfigPathParams_AreEscaped(t *testing.T) {
	const (
		hostName  = "om-0-backup-daemon.om-svc.mongodb.svc.cluster.local"
		headDbDir = "/head/"
	)

	t.Run("ReadDaemonConfig", func(t *testing.T) {
		got := captureOMRequest(t, func(admin OpsManagerAdmin) error {
			_, err := admin.ReadDaemonConfig(hostName, headDbDir)
			return err
		})

		assert.Equal(t,
			"/api/public/v1.0/admin/backup/daemon/configs/"+hostName+"/%2Fhead%2F",
			got.RequestURI)
	})

	t.Run("UpdateDaemonConfig", func(t *testing.T) {
		got := captureOMRequest(t, func(admin OpsManagerAdmin) error {
			return admin.UpdateDaemonConfig(backup.NewDaemonConfig(hostName, headDbDir, nil))
		})

		assert.Equal(t,
			"/api/public/v1.0/admin/backup/daemon/configs/"+hostName+"/%2Fhead%2F",
			got.RequestURI)
	})

	t.Run("CreateDaemonConfig with traversal in the hostname", func(t *testing.T) {
		const payload = "host/../../../groups"
		got := captureOMRequest(t, func(admin OpsManagerAdmin) error {
			return admin.CreateDaemonConfig(payload, headDbDir, nil)
		})

		assert.Equal(t,
			"/api/public/v1.0/admin/backup/daemon/configs/"+url.PathEscape(payload),
			got.RequestURI)
	})
}
