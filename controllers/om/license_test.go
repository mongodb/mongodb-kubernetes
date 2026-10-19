package om

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
)

func TestUpdateDeployment_UnknownCodeUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":400,"errorCode":"INVALID_ARGUMENT","detail":"bad automation config"}`))
	}))
	defer srv.Close()

	conn := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	_, err := conn.UpdateDeployment(context.Background(), NewDeployment())
	require.Error(t, err)
	assert.False(t, apierror.NewNonNil(err).IsLicenseEnforced())
}
