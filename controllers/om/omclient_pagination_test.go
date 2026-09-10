package om

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endlessAgentsPage is an automation agents page that always reports a next page.
const endlessAgentsPage = `{"totalCount": 9223372036854775807, "links": [{"rel": "next"}], "results": [{"hostname": "host-%d", "typeName": "AUTOMATION", "lastConf": "2024-01-01T00:00:00Z"}]}`

// automationAgents registers the automation agents endpoint of group "1", counting every request it receives.
func automationAgents(requests *atomic.Int32, respond func(w http.ResponseWriter, r *http.Request)) handleFunc {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/api/public/v1.0/groups/1/agents/AUTOMATION", func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			respond(w, r)
		})
	}
}

func TestReadAutomationAgents_StopsAtMaxPagesWhenServerAlwaysReportsNext(t *testing.T) {
	var requests atomic.Int32
	srv := serverMock(automationAgents(&requests, func(w http.ResponseWriter, r *http.Request) {
		pageNum, _ := strconv.Atoi(r.URL.Query().Get("pageNum"))
		_, _ = fmt.Fprintf(w, endlessAgentsPage, pageNum)
	}))
	defer srv.Close()

	conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL, GroupID: "1"}, OptionRetryConfig(0, 0, 0))

	itemsSeen := 0
	found, err := TraversePages(context.Background(), conn.ReadAutomationAgents, func(interface{}) bool {
		itemsSeen++
		return false
	})

	require.Error(t, err)
	assert.False(t, found)
	assert.ErrorIs(t, err, ErrPageLimitExceeded)
	assert.Equal(t, int32(maxPages), requests.Load(), "traversal must stop after exactly maxPages requests")
	assert.Equal(t, maxPages, itemsSeen)
}

func TestReadAutomationAgents_ContextDeadlineAbortsHangingOpsManager(t *testing.T) {
	var requests atomic.Int32
	srv := serverMock(automationAgents(&requests, func(_ http.ResponseWriter, r *http.Request) {
		// Hang until the client gives up; returning on r.Context().Done() lets srv.Close() finish.
		<-r.Context().Done()
	}))
	defer srv.Close()

	conn := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := TraversePages(ctx, conn.ReadAutomationAgents, func(interface{}) bool { return false })

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(1), requests.Load(), "the timed-out request must not be retried")
}
