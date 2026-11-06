package apierror

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestError_UnmarshalReadsTheShapesOpsManagerSends(t *testing.T) {
	var artifact Error
	require.NoError(t, json.Unmarshal([]byte(`{"error":409,"reason":"ARTIFACT_OUTDATED","detail":"artifact is not newer than the installed one"}`), &artifact))
	require.NotNil(t, artifact.Status)
	assert.Equal(t, 409, *artifact.Status)
	assert.Equal(t, "ARTIFACT_OUTDATED", artifact.Reason)
	assert.Equal(t, "artifact is not newer than the installed one", artifact.Detail)

	var enumCode Error
	require.NoError(t, json.Unmarshal([]byte(`{"error":409,"reason":"Conflict","errorCode":"DUPLICATE_GLOBAL_WHITELIST_ENTRY","detail":"x"}`), &enumCode))
	assert.Equal(t, "DUPLICATE_GLOBAL_WHITELIST_ENTRY", enumCode.ErrorCode)
	assert.Equal(t, "Conflict", enumCode.Reason)

	assert.Contains(t, artifact.Error(), "ARTIFACT_OUTDATED")
}

func TestError_IsLicenseArtifactOutdated(t *testing.T) {
	conflict, badRequest := http.StatusConflict, http.StatusBadRequest

	assert.True(t, (&Error{Status: &conflict, ErrorCode: LicenseArtifactOutdated}).IsLicenseArtifactOutdated())
	assert.False(t, (&Error{Status: &conflict, ErrorCode: "SOME_OTHER_CONFLICT"}).IsLicenseArtifactOutdated())
	assert.False(t, (&Error{Status: &badRequest, ErrorCode: LicenseArtifactOutdated}).IsLicenseArtifactOutdated())
	assert.False(t, (*Error)(nil).IsLicenseArtifactOutdated())
}
