package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/pkg/test/httprecorder"
)

func TestLicenseAdmin_Paths(t *testing.T) {
	tests := []struct {
		name   string
		method string
		uri    string
		invoke func(context.Context, OpsManagerAdmin) error
	}{
		{
			name:   "ReadLicenseState",
			method: http.MethodGet,
			uri:    "/api/public/v1.0/admin/licenses",
			invoke: func(ctx context.Context, admin OpsManagerAdmin) error {
				_, err := admin.ReadLicenseState(ctx)
				return err
			},
		},
		{
			name:   "InstallLicense",
			method: http.MethodPut,
			uri:    "/api/public/v1.0/admin/licenses",
			invoke: func(ctx context.Context, admin OpsManagerAdmin) error {
				_, err := admin.InstallLicense(ctx, json.RawMessage(`{"signed":"artifact"}`))
				return err
			},
		},
		{
			name:   "RecomputeLicense",
			method: http.MethodPost,
			uri:    "/api/public/v1.0/admin/licenses/recompute",
			invoke: func(ctx context.Context, admin OpsManagerAdmin) error {
				return admin.RecomputeLicense(ctx)
			},
		},
		{
			name:   "ReadLicenseIdentity",
			method: http.MethodGet,
			uri:    "/api/public/v1.0/admin/artifacts/identity",
			invoke: func(ctx context.Context, admin OpsManagerAdmin) error {
				_, err := admin.ReadLicenseIdentity(ctx)
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := captureOMRequest(t, tc.invoke)
			assert.Equal(t, tc.method, got.Method)
			assert.Equal(t, tc.uri, got.RequestURI)
			assert.Empty(t, got.RawQuery)
		})
	}

	t.Run("RotateLicenseIdentity", func(t *testing.T) {
		srv, rec := httprecorder.NewServer(http.StatusOK, []byte(`{"outcome":"ROTATED"}`))
		defer srv.Close()

		require.NoError(t, NewOmAdmin(srv.URL, "", "", nil).RotateLicenseIdentity(context.Background()))

		got := rec.Last()
		assert.Equal(t, http.MethodPost, got.Method)
		assert.Equal(t, "/api/public/v1.0/admin/artifacts/identity/rotate", got.RequestURI)
		assert.Empty(t, got.RawQuery)
	})
}

func TestLicenseAdmin_ArtifactIDIsPathEscaped(t *testing.T) {
	const payload = "x/../../admin"
	escaped := url.PathEscape(payload)

	t.Run("ReleaseLicense", func(t *testing.T) {
		got := captureOMRequest(t, func(ctx context.Context, admin OpsManagerAdmin) error {
			_, err := admin.ReleaseLicense(ctx, payload)
			return err
		})
		assert.Equal(t, http.MethodDelete, got.Method)
		assert.Equal(t, "/api/public/v1.0/admin/licenses/"+escaped, got.RequestURI)
		assert.Empty(t, got.RawQuery)
	})

	t.Run("ReadLicenseReceipt", func(t *testing.T) {
		got := captureOMRequest(t, func(ctx context.Context, admin OpsManagerAdmin) error {
			_, err := admin.ReadLicenseReceipt(ctx, payload)
			return err
		})
		assert.Equal(t, http.MethodGet, got.Method)
		assert.Equal(t, "/api/public/v1.0/admin/artifacts/"+escaped+"/receipt", got.RequestURI)
		assert.Empty(t, got.RawQuery)
	})
}

func TestArtifactIDFromRaw(t *testing.T) {
	claims, err := json.Marshal(map[string]string{"artifact_id": "7b1e9c2a"})
	require.NoError(t, err)
	raw := `{"payload":"` + base64.StdEncoding.EncodeToString(claims) + `"}`
	id, err := ArtifactIDFromRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, "7b1e9c2a", id)

	unpadded := `{"payload":"` + base64.RawStdEncoding.EncodeToString(claims) + `"}`
	id, err = ArtifactIDFromRaw(unpadded)
	require.NoError(t, err)
	assert.Equal(t, "7b1e9c2a", id)

	for name, body := range map[string]string{
		"not json":        `not-json`,
		"no payload":      `{}`,
		"payload garbage": `{"payload":"!!!"}`,
		"claims garbage":  `{"payload":"bm90IGpzb24="}`,
		"no artifact id":  `{"payload":"e30="}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ArtifactIDFromRaw(body)
			assert.Error(t, err)
		})
	}
}

func TestReadLicenseState_ParsesResponse(t *testing.T) {
	const body = `{"enforcement":{"effectiveState":"GRACE"}}`
	srv, _ := httprecorder.NewServer(http.StatusOK, []byte(body))
	defer srv.Close()

	got, err := NewOmAdmin(srv.URL, "", "", nil).ReadLicenseState(context.Background())
	require.NoError(t, err)
	require.NotNil(t, got.Enforcement)
	assert.Equal(t, "GRACE", got.Enforcement.EffectiveState)
}

func TestReadLicenseState_UnsupportedAPIIsError(t *testing.T) {
	srv, _ := httprecorder.NewServer(http.StatusNotFound, []byte(`{"error":404,"errorCode":"RESOURCE_NOT_FOUND"}`))
	defer srv.Close()

	_, err := NewOmAdmin(srv.URL, "", "", nil).ReadLicenseState(context.Background())
	require.Error(t, err)
	assert.False(t, apierror.NewNonNil(err).IsLicenseEnforced())
}

func TestInstallLicense_ErrorBody(t *testing.T) {
	srv, _ := httprecorder.NewServer(http.StatusBadRequest, []byte(`{"error":400,"reason":"Bad Request","detail":"artifact is not a signed licence","errorCode":"INVALID_LICENSE"}`))
	defer srv.Close()

	_, err := NewOmAdmin(srv.URL, "", "", nil).InstallLicense(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	omErr := apierror.NewNonNil(err)
	assert.False(t, omErr.IsLicenseEnforced())
	assert.Equal(t, "INVALID_LICENSE", omErr.ErrorCode)
	assert.Equal(t, "artifact is not a signed licence", omErr.Detail)
}

func TestRotateLicenseIdentity_AcceptsRotatedOutcome(t *testing.T) {
	srv, _ := httprecorder.NewServer(http.StatusOK, []byte(`{"outcome":"ROTATED"}`))
	defer srv.Close()

	err := NewOmAdmin(srv.URL, "", "", nil).RotateLicenseIdentity(context.Background())
	require.NoError(t, err)
}

func TestRotateLicenseIdentity_RejectsNonRotatedOutcome(t *testing.T) {
	srv, _ := httprecorder.NewServer(http.StatusOK, []byte(`{"outcome":"EA_UNAVAILABLE"}`))
	defer srv.Close()

	err := NewOmAdmin(srv.URL, "", "", nil).RotateLicenseIdentity(context.Background())
	require.EqualError(t, err, "license identity rotation returned EA_UNAVAILABLE")
}

func TestReadLicenseReceipt_RawBodyFallback(t *testing.T) {
	srv, _ := httprecorder.NewServer(http.StatusOK, []byte(`{"receipt":{"id":"art-1"}}`))
	defer srv.Close()

	got, err := NewOmAdmin(srv.URL, "", "", nil).ReadLicenseReceipt(context.Background(), "art-1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"art-1"}`, got)
}

func TestReadLicenseIdentity_ParsesHolderID(t *testing.T) {
	srv, _ := httprecorder.NewServer(http.StatusOK, []byte(`{"holderId":"holder-1"}`))
	defer srv.Close()

	got, err := NewOmAdmin(srv.URL, "", "", nil).ReadLicenseIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "holder-1", got.HolderID)
}
