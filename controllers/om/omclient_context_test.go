package om

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangingAutomationConfig registers the automation config endpoint of group "1" and never answers, counting every
// request it receives. Returning on r.Context().Done() lets srv.Close() finish. The server only notices the client
// going away once the request body has been consumed.
func hangingAutomationConfig(requests *atomic.Int32) handleFunc {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/api/public/v1.0/groups/1/automationConfig", func(_ http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		})
	}
}

func TestUpdateDeployment_CancelledContextAbortsHangingOpsManager(t *testing.T) {
	var requests atomic.Int32
	srv := serverMock(hangingAutomationConfig(&requests))
	defer srv.Close()

	conn := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := conn.UpdateDeployment(ctx, NewDeployment())
		done <- err
	}()

	// wait for the PUT to be in flight before cancelling
	require.Eventually(t, func() bool { return requests.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	cancel()

	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), context.Canceled.Error())
	assert.Equal(t, int32(1), requests.Load(), "the cancelled request must not be retried")
}
