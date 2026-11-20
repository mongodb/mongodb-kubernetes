package apierror

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestError_IsLicenseArtifactOutdated(t *testing.T) {
	conflict, badRequest := http.StatusConflict, http.StatusBadRequest

	assert.True(t, (&Error{Status: &conflict, ErrorCode: LicenseArtifactOutdated}).IsLicenseArtifactOutdated())
	assert.False(t, (&Error{Status: &conflict, ErrorCode: "SOME_OTHER_CONFLICT"}).IsLicenseArtifactOutdated())
	assert.False(t, (&Error{Status: &badRequest, ErrorCode: LicenseArtifactOutdated}).IsLicenseArtifactOutdated())
	assert.False(t, (*Error)(nil).IsLicenseArtifactOutdated())
}
